package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
)

type coachServer struct {
	client              *http.Client
	jaegerUIURL         string
	jaegerQueryURL      string
	jaegerQueryMaxLimit int
	webURL              string
	scenarios           map[string]scenario.Definition
	scenarioSet         []scenario.Definition
	levels              []levelDefinition
	page                *template.Template
	findRecentTraces    func(ctx context.Context, service, operation string, since time.Time, need, limit int, batchID string) ([]traceRecord, error)

	actionMu sync.Mutex
	mu       sync.RWMutex
	state    learnerSession

	subscribers      map[int]chan coachSnapshot
	nextSubscriberID int
}

type publicScenario struct {
	ID             string           `json:"id"`
	Title          string           `json:"title"`
	Objective      string           `json:"objective"`
	Prompt         string           `json:"prompt"`
	Hint1          string           `json:"hint_1"`
	Hint2          string           `json:"hint_2"`
	Route          string           `json:"route"`
	TrafficPath    string           `json:"traffic_path"`
	FocusService   string           `json:"focus_service"`
	FocusOperation string           `json:"focus_operation"`
	Assessment     publicAssessment `json:"assessment"`
}

type trafficRequest struct {
	ScenarioID string `json:"scenario_id"`
	Count      int    `json:"count"`
}

type gradeRequest struct {
	ScenarioID        string   `json:"scenario_id"`
	SuspectedService  string   `json:"suspected_service"`
	SuspectedIssue    string   `json:"suspected_issue"`
	SelectedTraceID   string   `json:"selected_trace_id"`
	SelectedSpan      string   `json:"selected_span"`
	SelectedAttribute string   `json:"selected_attribute"`
	FaultyTraceIDs    []string `json:"faulty_trace_ids"`
	HealthyTraceID    string   `json:"healthy_trace_id"`
	BeforeTraceID     string   `json:"before_trace_id"`
	AfterTraceID      string   `json:"after_trace_id"`
	FailingTraceIDs   []string `json:"failing_trace_ids"`
}

type selectLevelRequest struct {
	Level int `json:"level"`
}

const defaultTraceBatchSize = 5

//go:embed favicon.ico
var coachFavicon []byte

func main() {
	scenarios, err := app.LoadScenarios()
	if err != nil {
		log.Fatalf("load scenarios: %v", err)
	}

	scenarioSet := make([]scenario.Definition, 0, len(scenarios))
	for _, def := range scenarios {
		scenarioSet = append(scenarioSet, def)
	}
	sort.Slice(scenarioSet, func(i, j int) bool {
		return scenarioSet[i].ID < scenarioSet[j].ID
	})

	levels, err := buildLevels(scenarioSet)
	if err != nil {
		log.Fatalf("build levels: %v", err)
	}

	s := &coachServer{
		client:      &http.Client{Timeout: 10 * time.Second},
		jaegerUIURL: strings.TrimRight(defaultEnv("JAEGER_UI_URL", ""), "/"),
		jaegerQueryURL: strings.TrimRight(defaultEnv(
			"JAEGER_QUERY_URL",
			defaultEnv("JAEGER_UI_URL", ""),
		), "/"),
		jaegerQueryMaxLimit: defaultEnvInt("JAEGER_QUERY_MAX_LIMIT", defaultJaegerQueryMaxLimit),
		webURL:              strings.TrimRight(defaultEnv("WEB_URL", "http://shop-web:8080"), "/"),
		scenarios:           scenarios,
		scenarioSet:         scenarioSet,
		levels:              levels,
		page:                template.Must(template.New("page").Parse(pageTemplate)),
		state:               newLearnerSession(levels),
		subscribers:         map[int]chan coachSnapshot{},
	}

	addr := ":8080"
	log.Printf("coach listening on %s", addr)
	if err := http.ListenAndServe(addr, s.routes()); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *coachServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(s.index))
	mux.Handle("/api/state", http.HandlerFunc(s.stateSnapshot))
	mux.Handle("/api/events", http.HandlerFunc(s.events))
	mux.Handle("/api/scenarios/random", http.HandlerFunc(s.nextChallenge))
	mux.Handle("/api/challenges/next", http.HandlerFunc(s.nextChallenge))
	mux.Handle("/api/levels/select", http.HandlerFunc(s.selectLevel))
	mux.Handle("/api/traffic", http.HandlerFunc(s.generateTraffic))
	mux.Handle("/api/grade", http.HandlerFunc(s.grade))
	mux.Handle("/favicon.ico", http.HandlerFunc(serveFavicon))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	return mux
}

func (s *coachServer) index(w http.ResponseWriter, r *http.Request) {
	s.actionMu.Lock()
	selectedLevel, def := s.selectedLevelAndScenario()
	generated, err := s.prepareSelectedLevelScenario(r.Context(), defaultTraceBatchSize)

	s.mu.Lock()
	if err != nil {
		log.Printf("prepare selected challenge for %s (%s): %v", s.levelLabel(selectedLevel), def.ID, err)
		s.setFeedbackLocked("The current challenge is loaded, but automatic trace generation failed. Refresh or request a new challenge and try again.", false)
	} else {
		s.clearFeedbackLocked()
	}
	snapshot, subscribers := s.snapshotAndSubscribersLocked()
	s.mu.Unlock()
	s.actionMu.Unlock()

	if generated > 0 || err != nil {
		s.broadcast(snapshot, subscribers)
	}

	payload, _ := json.Marshal(snapshot)
	data := map[string]any{
		"InitialState": template.JS(payload),
		"JaegerURL":    s.jaegerUIURL,
	}
	if err := s.page.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *coachServer) stateSnapshot(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app.WriteJSON(w, http.StatusOK, s.snapshotLocked())
}

func (s *coachServer) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	id, ch := s.subscribe()
	defer s.unsubscribe(id)

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case snapshot, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(snapshot)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *coachServer) nextChallenge(w http.ResponseWriter, r *http.Request) {
	s.actionMu.Lock()
	level, current := s.selectedLevelAndScenario()
	next := s.pickRandomForLevel(level, current.ID)

	s.mu.Lock()
	s.setLevelScenarioLocked(level, next)
	s.mu.Unlock()

	_, err := s.prepareLevelScenario(r.Context(), level, defaultTraceBatchSize)

	s.mu.Lock()
	if err != nil {
		log.Printf("prepare new challenge for %s (%s): %v", s.levelLabel(level), next.ID, err)
		s.setFeedbackLocked(fmt.Sprintf("A fresh challenge was selected for %s, but automatic trace generation failed. Refresh or try again.", s.levelLabel(level)), false)
	} else {
		s.clearFeedbackLocked()
	}
	snapshot, subscribers := s.snapshotAndSubscribersLocked()
	s.mu.Unlock()
	s.actionMu.Unlock()

	s.broadcast(snapshot, subscribers)
	app.WriteJSON(w, http.StatusOK, snapshot)
}

func (s *coachServer) selectLevel(w http.ResponseWriter, r *http.Request) {
	var req selectLevelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.actionMu.Lock()
	s.mu.Lock()
	if req.Level < 1 || req.Level > len(s.levels) {
		s.mu.Unlock()
		s.actionMu.Unlock()
		http.Error(w, "unknown level", http.StatusNotFound)
		return
	}

	s.state.SelectedLevel = req.Level
	selected := s.ensureScenarioForLevelLocked(req.Level)
	prepared := s.levelStateLocked(req.Level).Prepared
	s.mu.Unlock()

	var err error
	if !prepared {
		_, err = s.prepareLevelScenario(r.Context(), req.Level, defaultTraceBatchSize)
	}

	s.mu.Lock()
	if err != nil {
		log.Printf("prepare selected level challenge for %s (%s): %v", s.levelLabel(req.Level), selected.ID, err)
		message := fmt.Sprintf("%s selected.", s.levelLabel(req.Level))
		message += " The challenge loaded, but automatic trace generation failed. Refresh or request a new challenge and try again."
		s.setFeedbackLocked(message, false)
	} else {
		s.clearFeedbackLocked()
	}
	snapshot, subscribers := s.snapshotAndSubscribersLocked()
	s.mu.Unlock()
	s.actionMu.Unlock()

	s.broadcast(snapshot, subscribers)
	app.WriteJSON(w, http.StatusOK, snapshot)
}

func (s *coachServer) generateTraffic(w http.ResponseWriter, r *http.Request) {
	var req trafficRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	def, ok := s.scenarios[req.ScenarioID]
	if !ok {
		http.Error(w, "unknown scenario", http.StatusNotFound)
		return
	}

	successes, err := s.generateScenarioTraffic(r.Context(), def, req.Count)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.markPreparedIfCurrent(def.ID)

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"generated": successes,
		"target":    s.webURL + def.TrafficPath,
	})
}

func (s *coachServer) grade(w http.ResponseWriter, r *http.Request) {
	var req gradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.actionMu.Lock()
	s.mu.Lock()
	level := s.state.SelectedLevel
	state := s.levelStateLocked(level)
	current := s.ensureScenarioForLevelLocked(level)
	if req.ScenarioID != current.ID {
		s.setFeedbackLocked("The active challenge changed. Review the current level and challenge before submitting again.", false)
		snapshot, subscribers := s.snapshotAndSubscribersLocked()
		s.mu.Unlock()
		s.actionMu.Unlock()
		s.broadcast(snapshot, subscribers)
		app.WriteJSON(w, http.StatusConflict, snapshot)
		return
	}

	result := gradeSubmission(current, state.Challenge, req)
	if !result.Pass {
		if state.Challenge == nil || !state.Challenge.Public.Ready {
			s.setFeedbackLocked(fmt.Sprintf("%s %s remains at %d/%d mastery.", result.Message, s.levelLabel(level), state.MasteryCount, masteryTarget), false)
			snapshot, subscribers := s.snapshotAndSubscribersLocked()
			s.mu.Unlock()
			s.actionMu.Unlock()
			s.broadcast(snapshot, subscribers)
			app.WriteJSON(w, http.StatusOK, snapshot)
			return
		}

		state.IncorrectAttempts++
		if state.IncorrectAttempts < maxAttemptsPerChallenge {
			remaining := maxAttemptsPerChallenge - state.IncorrectAttempts
			attemptLabel := "attempt"
			if remaining != 1 {
				attemptLabel = "attempts"
			}
			s.setFeedbackLocked(fmt.Sprintf("%s %s remains at %d/%d mastery. %d %s remain on this challenge before the coach loads a new one.", result.Message, s.levelLabel(level), state.MasteryCount, masteryTarget, remaining, attemptLabel), false)
			snapshot, subscribers := s.snapshotAndSubscribersLocked()
			s.mu.Unlock()
			s.actionMu.Unlock()
			s.broadcast(snapshot, subscribers)
			app.WriteJSON(w, http.StatusOK, snapshot)
			return
		}

		next := s.pickRandomForLevelDifferentVariant(level, current.ID, current.VariantGroup)
		s.setLevelScenarioLocked(level, next)
		s.mu.Unlock()

		_, err := s.prepareLevelScenario(r.Context(), level, defaultTraceBatchSize)

		s.mu.Lock()
		message := fmt.Sprintf("%s %s remains at %d/%d mastery. You used both attempts on this challenge, so the coach loaded a new one.", result.Message, s.levelLabel(level), state.MasteryCount, masteryTarget)
		if err != nil {
			message += " The new challenge loaded, but automatic trace generation failed. Refresh or request a new challenge and try again."
		}
		s.setFeedbackLocked(message, false)
		snapshot, subscribers := s.snapshotAndSubscribersLocked()
		s.mu.Unlock()
		s.actionMu.Unlock()
		s.broadcast(snapshot, subscribers)
		app.WriteJSON(w, http.StatusOK, snapshot)
		return
	}

	if state.MasteryCount < masteryTarget {
		state.MasteryCount++
	}
	masteryCount := state.MasteryCount
	next := s.pickRandomForLevel(level, current.ID)
	s.setLevelScenarioLocked(level, next)
	s.mu.Unlock()

	_, err := s.prepareLevelScenario(r.Context(), level, defaultTraceBatchSize)

	s.mu.Lock()
	message := fmt.Sprintf("Correct. %s is now at %d/%d mastery.", s.levelLabel(level), masteryCount, masteryTarget)
	if err != nil {
		message += "\n\nA fresh challenge was selected, but automatic trace generation failed. Refresh or request a new challenge and try again."
	}
	s.setFeedbackLocked(message, true)
	snapshot, subscribers := s.snapshotAndSubscribersLocked()
	s.mu.Unlock()
	s.actionMu.Unlock()

	s.broadcast(snapshot, subscribers)
	app.WriteJSON(w, http.StatusOK, snapshot)
}

func (s *coachServer) prepareSelectedLevelScenario(ctx context.Context, count int) (int, error) {
	s.mu.RLock()
	level := s.state.SelectedLevel
	s.mu.RUnlock()
	return s.prepareLevelScenario(ctx, level, count)
}

func (s *coachServer) prepareLevelScenario(ctx context.Context, level, count int) (int, error) {
	s.mu.Lock()
	def := s.ensureScenarioForLevelLocked(level)
	state := s.levelStateLocked(level)
	prepared := state.Prepared && state.Challenge != nil
	s.mu.Unlock()

	if prepared {
		return 0, nil
	}

	challenge, generated, err := s.prepareChallenge(ctx, def, count)

	s.mu.Lock()
	if state := s.levelStateLocked(level); state != nil && state.Current.ID == def.ID && challenge != nil {
		state.Prepared = true
		state.Challenge = challenge
	}
	s.mu.Unlock()

	if err != nil && challenge == nil {
		return generated, err
	}
	return generated, err
}

func (s *coachServer) selectedLevelAndScenario() (int, scenario.Definition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	level := s.state.SelectedLevel
	return level, s.ensureScenarioForLevelLocked(level)
}

func (s *coachServer) markPreparedIfCurrent(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, level := range s.levels {
		state := s.levelStateLocked(level.Number)
		if state.Current.ID == id {
			state.Prepared = true
			state.Challenge = nil
		}
	}
}

func (s *coachServer) generateScenarioTraffic(ctx context.Context, def scenario.Definition, count int) (int, error) {
	if count <= 0 {
		count = defaultTraceBatchSize
	}

	var firstErr error
	var successes int
	for _, trafficPath := range s.trafficPathsForScenario(def) {
		pathSuccesses, err := s.generateTrafficRequests(ctx, trafficPath, def.ID, newTraceBatchID(), count)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		successes += pathSuccesses
	}

	if successes == 0 && firstErr != nil {
		return 0, firstErr
	}

	return successes, nil
}

func (s *coachServer) trafficPathsForScenario(def scenario.Definition) []string {
	pathsByRoute := map[string]string{}
	otherRoutes := make([]string, 0, len(s.scenarioSet))

	for _, candidate := range s.scenarioSet {
		if candidate.Route == "" || candidate.TrafficPath == "" {
			continue
		}
		if candidate.Route == def.Route {
			pathsByRoute[candidate.Route] = def.TrafficPath
			continue
		}

		existing, ok := pathsByRoute[candidate.Route]
		if !ok {
			otherRoutes = append(otherRoutes, candidate.Route)
			pathsByRoute[candidate.Route] = candidate.TrafficPath
			continue
		}
		if candidate.TrafficPath < existing {
			pathsByRoute[candidate.Route] = candidate.TrafficPath
		}
	}

	sort.Strings(otherRoutes)

	paths := make([]string, 0, len(otherRoutes)+1)
	for _, route := range otherRoutes {
		paths = append(paths, pathsByRoute[route])
	}
	paths = append(paths, def.TrafficPath)
	return paths
}

func (s *coachServer) toPublic(def scenario.Definition, challenge *preparedChallenge) publicScenario {
	assessment := assessmentShell(def)
	if challenge != nil {
		assessment = challenge.Public
	}
	return publicScenario{
		ID:             def.ID,
		Title:          def.Title,
		Objective:      def.Objective,
		Prompt:         def.Prompt,
		Hint1:          def.Hint1,
		Hint2:          def.Hint2,
		Route:          def.Route,
		TrafficPath:    def.TrafficPath,
		FocusService:   def.FocusService,
		FocusOperation: def.FocusOperation,
		Assessment:     assessment,
	}
}

func defaultEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func defaultEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		log.Printf("invalid %s=%q, using %d", key, value, fallback)
		return fallback
	}
	return parsed
}

func serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
	http.ServeContent(w, r, "favicon.ico", time.Time{}, bytes.NewReader(coachFavicon))
}

const pageTemplate = `
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Trace Coach</title>
    <meta name="description" content="Level-based trace practice with self-selected starting points, five-attempt mastery tracking, and synchronized state across every open coach tab.">
    <link rel="icon" href="/favicon.ico">
    <style>
      :root {
        --bg: #efe7d4;
        --panel: #fff9ef;
        --ink: #1f2120;
        --muted: #5d635d;
        --accent: #a53d24;
        --accent-soft: #f5d8c6;
        --accent-strong: #7f2613;
        --success: #215f3c;
        --success-soft: #dceedd;
        --border: #d3c2ae;
      }
      html {
        min-height: 100%;
        background: #e8dcc6;
      }
      body {
        margin: 0;
        min-height: 100vh;
        font-family: "Iowan Old Style", Georgia, serif;
        background-color: var(--bg);
        background-image:
          radial-gradient(circle at top left, rgba(165, 61, 36, 0.12), transparent 28%),
          linear-gradient(180deg, #f5ecdd 0%, #e8dcc6 100%);
        background-repeat: no-repeat;
        background-size: 100% 100%;
        color: var(--ink);
      }
      main {
        max-width: 1160px;
        margin: 0 auto;
        padding: 28px 20px 48px;
      }
      .stack {
        display: grid;
        gap: 16px;
        align-content: start;
      }
      .panel {
        background: var(--panel);
        border: 1px solid var(--border);
        border-radius: 20px;
        padding: 18px;
        box-shadow: 0 12px 28px rgba(0, 0, 0, 0.06);
      }
      .grid {
        display: grid;
        grid-template-columns: 1.45fr 1fr;
        gap: 18px;
      }
      .progression-panel {
        padding: 14px 16px;
      }
      .progression-topline {
        display: flex;
        justify-content: space-between;
        gap: 12px;
        align-items: flex-start;
      }
      .progression-copy {
        display: grid;
        gap: 4px;
      }
      .progression-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: flex-end;
      }
      .progression-body {
        margin-top: 12px;
      }
      .progression-body.collapsed {
        display: none;
      }
      .level-grid {
        display: grid;
        grid-template-columns: repeat(5, minmax(0, 1fr));
        gap: 8px;
      }
      @media (max-width: 1080px) {
        .level-grid {
          grid-template-columns: repeat(2, minmax(0, 1fr));
        }
      }
      @media (max-width: 840px) {
        .grid { grid-template-columns: 1fr; }
        .level-grid { grid-template-columns: 1fr; }
        .progression-topline {
          align-items: stretch;
          flex-direction: column;
        }
        .progression-actions {
          justify-content: flex-start;
        }
        .level-summary {
          display: none;
        }
        main {
          padding: 18px 16px 36px;
        }
      }
      h1, h2, h3, h4 { margin-top: 0; }
      p { line-height: 1.45; }
      .muted { color: var(--muted); }
      .eyebrow {
        font-size: 0.76rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--muted);
      }
      button, select, a.button {
        font: inherit;
      }
      button, a.button {
        background: var(--accent);
        color: white;
        border: none;
        border-radius: 999px;
        padding: 9px 14px;
        cursor: pointer;
        text-decoration: none;
      }
      .ghost-button {
        background: transparent;
        color: var(--ink);
        border: 1px solid var(--border);
      }
      button:disabled, .level-button:disabled {
        opacity: 0.68;
        cursor: default;
      }
      select {
        width: 100%;
        padding: 10px 12px;
        border-radius: 12px;
        border: 1px solid var(--border);
        margin-bottom: 10px;
        background: white;
      }
      .level-button {
        width: 100%;
        text-align: left;
        border-radius: 16px;
        border: 1px solid var(--border);
        background: white;
        color: var(--ink);
        padding: 12px;
        display: grid;
        gap: 6px;
      }
      .level-button.selected {
        background: var(--accent);
        color: white;
        border-color: var(--accent-strong);
      }
      .level-button.mastered:not(.selected) {
        background: #f8eee3;
      }
      .level-topline {
        display: flex;
        justify-content: space-between;
        gap: 10px;
        align-items: center;
      }
      .level-state {
        font-size: 0.78rem;
        text-transform: uppercase;
        letter-spacing: 0.05em;
      }
      .level-summary {
        font-size: 0.88rem;
        line-height: 1.35;
      }
      .level-progress {
        font-size: 0.82rem;
        font-weight: 600;
      }
      .actions {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        align-self: start;
      }
      .status-pill {
        padding: 6px 10px;
        border-radius: 999px;
        background: #f3ede3;
        font-size: 0.9rem;
      }
      .challenge-header {
        display: grid;
        gap: 10px;
      }
      .challenge-copy {
        display: grid;
        gap: 8px;
      }
      .hidden {
        display: none;
      }
      #feedback {
        padding: 12px 14px;
        border-radius: 14px;
        background: #f3ede3;
        white-space: pre-line;
      }
      #feedback.ok {
        background: var(--success-soft);
        color: var(--success);
      }
      .hint-shell {
        display: grid;
        grid-template-rows: 0fr;
        opacity: 0;
        overflow: hidden;
        transform: translateY(-8px);
        transition:
          grid-template-rows 280ms cubic-bezier(0.2, 0.8, 0.2, 1),
          opacity 220ms ease,
          transform 280ms cubic-bezier(0.2, 0.8, 0.2, 1);
      }
      .hint-shell.open {
        grid-template-rows: 1fr;
        opacity: 1;
        transform: translateY(0);
        transition-delay: 90ms;
      }
      .hint-shell > * {
        min-height: 0;
      }
      #hint-box {
        padding: 12px 14px;
        border-radius: 14px;
        background: #f3ede3;
        white-space: pre-line;
      }
      code {
        background: #f3ead8;
        padding: 2px 6px;
        border-radius: 8px;
      }
      .field-label {
        display: block;
        margin-bottom: 6px;
        font-weight: 600;
      }
      .checkbox-group {
        display: grid;
        gap: 8px;
        margin-bottom: 12px;
      }
      .choice {
        display: flex;
        gap: 10px;
        align-items: flex-start;
        padding: 10px 12px;
        border-radius: 12px;
        border: 1px solid var(--border);
        background: white;
      }
      .choice input {
        margin-top: 4px;
      }
      .reference-trace {
        display: grid;
        gap: 6px;
        padding: 10px 12px;
        border-radius: 12px;
        background: #f3ede3;
      }
      #busy-overlay {
        position: fixed;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        background: rgba(31, 33, 32, 0.34);
        backdrop-filter: blur(3px);
        z-index: 1000;
      }
      #busy-overlay.hidden {
        display: none;
      }
      .modal {
        min-width: min(320px, calc(100vw - 40px));
        max-width: 420px;
        text-align: center;
      }
      .onboarding-overlay {
        position: fixed;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 20px;
        background: rgba(31, 33, 32, 0.34);
        backdrop-filter: blur(3px);
        z-index: 900;
      }
      .onboarding-overlay.hidden {
        display: none;
      }
      .onboarding-modal {
        width: min(480px, calc(100vw - 40px));
        padding: 22px;
      }
      .onboarding-step {
        display: grid;
        gap: 14px;
        animation: modal-step-in 180ms ease;
      }
      .onboarding-step.hidden {
        display: none;
      }
      .skill-options {
        display: grid;
        gap: 10px;
      }
      .skill-option {
        width: 100%;
        display: grid;
        gap: 4px;
        text-align: left;
        background: white;
        color: var(--ink);
        border: 1px solid var(--border);
        border-radius: 12px;
        padding: 12px 14px;
      }
      .skill-option:hover:not(:disabled), .skill-option:focus-visible {
        border-color: var(--accent);
        box-shadow: 0 0 0 3px rgba(165, 61, 36, 0.14);
      }
      .skill-option span {
        color: var(--muted);
        font-size: 0.92rem;
        line-height: 1.35;
      }
      .modal-error {
        color: var(--accent-strong);
        margin: 0;
      }
      .spinner {
        width: 34px;
        height: 34px;
        margin: 0 auto 14px;
        border-radius: 50%;
        border: 3px solid rgba(165, 61, 36, 0.18);
        border-top-color: var(--accent);
        animation: spin 0.85s linear infinite;
      }
      @keyframes spin {
        to { transform: rotate(360deg); }
      }
      @keyframes modal-step-in {
        from {
          opacity: 0;
          transform: translateY(6px);
        }
        to {
          opacity: 1;
          transform: translateY(0);
        }
      }
    </style>
  </head>
  <body>
    <main class="stack">
      <section class="panel progression-panel">
        <div class="progression-topline">
          <div class="progression-copy">
            <div class="eyebrow">Progression</div>
            <strong id="selected-level-title"></strong>
          </div>
          <div class="progression-actions">
            <span id="selected-level-progress" class="status-pill"></span>
            <button id="toggle-levels" class="ghost-button" type="button" aria-controls="levels-wrap" aria-expanded="true">Hide Levels</button>
          </div>
        </div>
        <div id="levels-wrap" class="progression-body">
          <div id="levels" class="level-grid"></div>
        </div>
      </section>

      <section class="grid">
        <article class="panel stack">
          <div class="challenge-header">
            <h2 id="title"></h2>
            <p id="objective" class="muted"></p>
            <div id="reference-trace" class="reference-trace muted"></div>
          </div>
          <div class="actions">
            {{if .JaegerURL}}<a class="button" target="_blank" rel="noreferrer" href="{{.JaegerURL}}">Open Jaeger</a>{{end}}
            <button id="next-challenge" type="button">New Challenge</button>
          </div>
          <div class="challenge-copy">
            <p id="assessment-prompt" class="muted"></p>
          </div>
          <div id="hint-panel" class="stack">
            <div class="actions">
              <button id="hint" type="button">Show Hint</button>
            </div>
            <div id="hint-shell" class="hint-shell" aria-hidden="true">
              <div id="hint-box" class="muted"></div>
            </div>
          </div>
        </article>

        <aside class="panel stack">
          <div>
            <h3>Submit Answer</h3>
            <div id="service-field">
              <label class="field-label" for="service">Culprit service</label>
              <select id="service">
                <option value="">Select service</option>
                <option value="catalog-api">catalog-api</option>
                <option value="inventory-api">inventory-api</option>
                <option value="orders-api">orders-api</option>
                <option value="payments-api">payments-api</option>
              </select>
            </div>
            <div id="issue-field">
              <label class="field-label" for="issue">Failure mode</label>
              <select id="issue">
                <option value="">Select issue type</option>
                <option value="expensive_search_query">expensive search query</option>
                <option value="n_plus_one_queries">n plus one queries</option>
                <option value="lock_wait_timeout">lock wait timeout</option>
                <option value="expensive_sort">expensive sort</option>
              </select>
            </div>
            <div id="assessment-fields" class="stack"></div>
            <div class="actions">
              <button id="submit" type="button">Check Answer</button>
            </div>
          </div>
          <div id="feedback-panel" class="hidden">
            <h4>Coach Feedback</h4>
            <div id="feedback"></div>
          </div>
        </aside>
      </section>
    </main>

    <div id="skill-modal" class="onboarding-overlay hidden" role="dialog" aria-modal="true" aria-labelledby="skill-modal-title">
      <div class="panel onboarding-modal">
        <div id="skill-step-choice" class="onboarding-step">
          <div class="eyebrow">Start Point</div>
          <h2 id="skill-modal-title">How familiar are you with distributed tracing?</h2>
          <div class="skill-options">
            <button class="skill-option" type="button" data-skill-choice="no-experience" data-level="1">
              <strong>No experience</strong>
              <span>Start with reading one trace and finding the slow span.</span>
            </button>
            <button class="skill-option" type="button" data-skill-choice="familiar" data-level="3">
              <strong>Familiar</strong>
              <span>Start with before-and-after trace comparisons.</span>
            </button>
            <button class="skill-option" type="button" data-skill-choice="expert" data-level="5">
              <strong>Expert</strong>
              <span>Start with intermittent failures and noisy evidence.</span>
            </button>
          </div>
          <p id="skill-modal-error" class="modal-error hidden"></p>
        </div>
        <div id="skill-step-explainer" class="onboarding-step hidden">
          <div class="eyebrow">Flexible Practice</div>
          <h2>Move between levels any time</h2>
          <p class="muted">All levels are available from the progression bar at the top. If the starting point feels too easy or too hard, choose a different level or revisit a specific skill whenever you want more practice.</p>
          <div class="actions">
            <button id="skill-modal-close" type="button">Start Practicing</button>
          </div>
        </div>
      </div>
    </div>

    <div id="busy-overlay" class="hidden" aria-live="polite" aria-busy="true">
      <div class="panel modal">
        <div class="spinner" aria-hidden="true"></div>
        <strong id="busy-title">Loading...</strong>
      </div>
    </div>

    <script>
      const initialState = {{.InitialState}};
      const minimumSubmitBusyMs = 700;
      const skillPlacementKey = "cloudtracing.skillPlacement.v1";
      const mobileProgressionQuery = window.matchMedia("(max-width: 840px)");
      let coachState = initialState;
      let hintLevel = 0;
      let lastScenarioID = "";
      let lastSelectedLevel = 0;
      let levelsCollapsed = mobileProgressionQuery.matches;
      let levelsCollapseTouched = false;

      function currentScenario() {
        return coachState.current_scenario || {};
      }

      function currentAssessment() {
        return currentScenario().assessment || {};
      }

      function requiresDiagnosis(assessment) {
        return assessment.type !== "trace_search_span";
      }

      function selectedLevel() {
        return (coachState.levels || []).find((level) => level.selected) || null;
      }

      function setFeedback(message, ok = false, visible = false) {
        const panel = document.getElementById("feedback-panel");
        const box = document.getElementById("feedback");
        box.textContent = message || "";
        box.classList.toggle("ok", ok);
        panel.classList.toggle("hidden", !visible);
      }

      function setLevelsCollapsed(collapsed) {
        levelsCollapsed = collapsed;
        document.getElementById("levels-wrap").classList.toggle("collapsed", collapsed);
        document.getElementById("toggle-levels").setAttribute("aria-expanded", String(!collapsed));
        document.getElementById("toggle-levels").textContent = collapsed ? "Show Levels" : "Hide Levels";
      }

      function syncLevelsCollapsed() {
        if (levelsCollapseTouched) {
          return;
        }
        setLevelsCollapsed(mobileProgressionQuery.matches);
      }

      function toggleLevels() {
        levelsCollapseTouched = true;
        setLevelsCollapsed(!levelsCollapsed);
      }

      function delay(ms) {
        return new Promise((resolve) => window.setTimeout(resolve, ms));
      }

      function hintsForCurrent() {
        const current = currentScenario();
        const assessment = currentAssessment();
        return [assessment.start_guide, current.prompt, current.hint_1, current.hint_2].filter(Boolean);
      }

      function renderAssessmentFieldVisibility(assessment) {
        const hidden = !requiresDiagnosis(assessment);
        document.getElementById("service-field").classList.toggle("hidden", hidden);
        document.getElementById("issue-field").classList.toggle("hidden", hidden);
      }

      function renderHints() {
        const hints = hintsForCurrent();
        const panel = document.getElementById("hint-panel");
        const shell = document.getElementById("hint-shell");
        const box = document.getElementById("hint-box");
        const button = document.getElementById("hint");
        const isOpen = hints.length > 0 && hintLevel > 0;

        panel.classList.toggle("hidden", hints.length === 0);
        shell.classList.toggle("open", isOpen);
        shell.setAttribute("aria-hidden", String(!isOpen));

        if (hints.length === 0) {
          box.textContent = "";
          button.disabled = true;
          button.textContent = "Hints Unavailable";
          return;
        }

        if (hintLevel === 0) {
          box.textContent = "";
          button.disabled = false;
          button.textContent = "Show Hint";
          return;
        }

        const level = Math.min(hintLevel, hints.length);
        box.textContent = hints[level - 1];
        button.disabled = level >= hints.length;
        button.textContent = level >= hints.length ? "No More Hints" : "Show Another Hint";
      }

      function showHint() {
        const hints = hintsForCurrent();
        if (hintLevel < hints.length) {
          hintLevel++;
          renderHints();
        }
      }

      function toggleInputs(disabled) {
        document.getElementById("submit").disabled = disabled;
        document.getElementById("next-challenge").disabled = disabled;
        document.getElementById("hint").disabled = disabled;
        document.getElementById("service").disabled = disabled;
        document.getElementById("issue").disabled = disabled;
        document.querySelectorAll("#assessment-fields select, #assessment-fields input").forEach((element) => {
          element.disabled = disabled;
        });
        document.querySelectorAll(".level-button").forEach((button) => {
          button.disabled = disabled;
        });
      }

      function setBusy(message) {
        document.getElementById("busy-title").textContent = message;
        document.getElementById("busy-overlay").classList.remove("hidden");
        toggleInputs(true);
      }

      function clearBusy() {
        document.getElementById("busy-overlay").classList.add("hidden");
        toggleInputs(false);
        renderLevels();
      }

      function renderLevels() {
        const root = document.getElementById("levels");
        root.innerHTML = "";

        (coachState.levels || []).forEach((level) => {
          const button = document.createElement("button");
          button.type = "button";
          button.className = "level-button";
          button.dataset.unlocked = "true";
          if (level.selected) {
            button.classList.add("selected");
          }
          if (level.mastered) {
            button.classList.add("mastered");
          }
          button.innerHTML =
            "<div class=\"level-topline\">" +
              "<strong>" + level.title + "</strong>" +
              "<span class=\"level-state\">" + (level.mastered ? "Mastered" : "Open") + "</span>" +
            "</div>" +
            "<div class=\"level-summary\">" + level.summary + "</div>" +
            "<div class=\"level-progress\">" + level.mastery_count + "/" + level.mastery_target + " correct</div>";
          button.addEventListener("click", () => selectLevel(level.number));
          root.appendChild(button);
        });
      }

      function hasSeenSkillPlacement() {
        try {
          return window.localStorage.getItem(skillPlacementKey) === "done";
        } catch (error) {
          return false;
        }
      }

      function markSkillPlacementSeen() {
        try {
          window.localStorage.setItem(skillPlacementKey, "done");
        } catch (error) {
        }
      }

      function setSkillModalVisible(visible) {
        document.getElementById("skill-modal").classList.toggle("hidden", !visible);
      }

      function showSkillExplanation() {
        document.getElementById("skill-step-choice").classList.add("hidden");
        document.getElementById("skill-step-explainer").classList.remove("hidden");
        document.getElementById("skill-modal-close").focus();
      }

      function setSkillChoiceDisabled(disabled) {
        document.querySelectorAll("[data-skill-choice]").forEach((button) => {
          button.disabled = disabled;
        });
      }

      async function chooseSkillPlacement(event) {
        const button = event.currentTarget;
        const level = Number(button.dataset.level);
        const error = document.getElementById("skill-modal-error");
        error.textContent = "";
        error.classList.add("hidden");
        setSkillChoiceDisabled(true);

        try {
          await requestSnapshot("/api/levels/select", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify({level})
          });
          markSkillPlacementSeen();
          showSkillExplanation();
        } catch (failure) {
          error.textContent = "Could not set your starting level. Try again.";
          error.classList.remove("hidden");
          setSkillChoiceDisabled(false);
        }
      }

      function closeSkillPlacement() {
        setSkillModalVisible(false);
      }

      function maybeShowSkillPlacement() {
        if (!hasSeenSkillPlacement()) {
          setSkillModalVisible(true);
        }
      }

      function renderReferenceTrace(assessment) {
        const shell = document.getElementById("reference-trace");
        shell.innerHTML = "";

        if (assessment.investigation_link) {
          const intro = document.createElement("span");
          intro.textContent = "Open the prepared trace search:";
          shell.appendChild(intro);

          if (assessment.investigation_link.url) {
            const link = document.createElement("a");
            link.href = assessment.investigation_link.url;
            link.target = "_blank";
            link.rel = "noreferrer";
            link.textContent = assessment.investigation_link.label;
            shell.appendChild(link);
          } else {
            shell.appendChild(document.createTextNode(assessment.investigation_link.label));
          }
          return;
        }

        if (assessment.reference_trace) {
          const intro = document.createElement("span");
          intro.textContent = "Open the reference trace:";
          shell.appendChild(intro);

          if (assessment.reference_trace.url) {
            const link = document.createElement("a");
            link.href = assessment.reference_trace.url;
            link.target = "_blank";
            link.rel = "noreferrer";
            link.textContent = assessment.reference_trace.label;
            shell.appendChild(link);
          } else {
            shell.appendChild(document.createTextNode(assessment.reference_trace.label));
          }
          return;
        }

        if (assessment.unavailable_reason) {
          shell.textContent = assessment.unavailable_reason;
          return;
        }

        switch (assessment.type) {
          case "healthy_faulty":
            shell.textContent = "Open the trace links below, pick the slow ones, and choose the one healthy trace.";
            break;
          case "before_after":
            shell.textContent = "Pick one baseline trace and one slow trace from the shared candidate list below.";
            break;
          case "intermittent_failure":
            shell.textContent = "Open the trace links below and select the failing ones.";
            break;
          default:
            shell.textContent = "Open Jaeger to inspect the prepared traces below.";
        }
      }

      function appendNote(container, text) {
        const note = document.createElement("p");
        note.className = "muted";
        note.textContent = text;
        container.appendChild(note);
      }

      function appendSelect(container, id, labelText, options, placeholder) {
        const label = document.createElement("label");
        label.className = "field-label";
        label.htmlFor = id;
        label.textContent = labelText;
        container.appendChild(label);

        const select = document.createElement("select");
        select.id = id;

        const empty = document.createElement("option");
        empty.value = "";
        empty.textContent = placeholder;
        select.appendChild(empty);

        (options || []).forEach((option) => {
          const item = document.createElement("option");
          item.value = option.id;
          item.textContent = option.label;
          select.appendChild(item);
        });
        container.appendChild(select);
      }

      function appendChoiceGroup(container, name, type, labelText, options) {
        const label = document.createElement("div");
        label.className = "field-label";
        label.textContent = labelText;
        container.appendChild(label);

        const group = document.createElement("div");
        group.className = "checkbox-group";

        (options || []).forEach((option) => {
          const row = document.createElement("label");
          row.className = "choice";

          const input = document.createElement("input");
          input.type = type;
          input.name = name;
          input.value = option.id;
          row.appendChild(input);

          const text = document.createElement("div");
          const title = document.createElement("div");
          title.textContent = option.label;
          text.appendChild(title);

          if (option.url) {
            const link = document.createElement("a");
            link.href = option.url;
            link.target = "_blank";
            link.rel = "noreferrer";
            link.textContent = "Open trace";
            text.appendChild(link);
          }

          row.appendChild(text);
          group.appendChild(row);
        });

        container.appendChild(group);
      }

      function selectedServiceValue() {
        return document.getElementById("service")?.value || "";
      }

      function selectedTraceValue() {
        return document.getElementById("selected-trace")?.value || "";
      }

      function selectedSpanValue() {
        return document.getElementById("selected-span")?.value || "";
      }

      function traceSpanChoicesForAssessment(assessment) {
        const traceID = selectedTraceValue();
        if (!traceID) {
          return [];
        }
        return (assessment.trace_span_choices || {})[traceID] || [];
      }

      function spanChoicesForAssessment(assessment) {
        const service = selectedServiceValue();
        if (!service) {
          return [];
        }
        return (assessment.span_choices || []).filter((option) => option.service === service);
      }

      function attributeChoicesForAssessment(assessment) {
        const spanID = selectedSpanValue();
        if (!spanID) {
          return [];
        }
        return (assessment.span_attribute_choices || {})[spanID] || [];
      }

      function restoreSelectValue(id, value) {
        const select = document.getElementById(id);
        if (!select || !value) {
          return;
        }
        if ([...select.options].some((option) => option.value === value)) {
          select.value = value;
        }
      }

      function restoreCheckedValues(name, values) {
        const wanted = new Set(values || []);
        document.querySelectorAll("input[name=\"" + name + "\"]").forEach((input) => {
          input.checked = wanted.has(input.value);
        });
      }

      function renderAssessmentFields(force) {
        const shell = document.getElementById("assessment-fields");
        const assessment = currentAssessment();
        const previousState = {
          selectedTraceID: selectedTraceValue(),
          selectedSpanID: selectedSpanValue(),
          selectedAttributeID: document.getElementById("selected-attribute")?.value || "",
          beforeTraceID: document.getElementById("before-trace")?.value || "",
          afterTraceID: document.getElementById("after-trace")?.value || "",
          faultyTraceIDs: checkedValues("faulty-trace"),
          healthyTraceID: checkedValue("healthy-trace"),
          failingTraceIDs: checkedValues("failing-trace")
        };
        const signature = [
          assessment.type || "",
          String(assessment.ready),
          assessment.investigation_link ? assessment.investigation_link.label : "",
          assessment.reference_trace ? assessment.reference_trace.id : "",
          (assessment.trace_choices || []).length,
          (assessment.span_choices || []).length,
          (assessment.attribute_choices || []).length
        ].join(":");

        if (!force && shell.dataset.signature === signature) {
          return;
        }

        shell.innerHTML = "";
        shell.dataset.signature = signature;
        if (!assessment.ready) {
          const note = document.createElement("p");
          note.className = "muted";
          note.textContent = assessment.unavailable_reason || "The challenge is still preparing its assessment evidence.";
          shell.appendChild(note);
          return;
        }

        switch (assessment.type) {
          case "trace_search_span":
            appendSelect(shell, "selected-trace", "Trace used", assessment.trace_choices, "Select the trace");
            restoreSelectValue("selected-trace", previousState.selectedTraceID);
            if (!selectedTraceValue()) {
              appendNote(shell, "Select the trace you inspected to load its span choices.");
              break;
            }
            appendSelect(shell, "selected-span", "Culprit span", traceSpanChoicesForAssessment(assessment), "Select the span");
            restoreSelectValue("selected-span", previousState.selectedSpanID);
            break;
          case "culprit_span":
            if (!selectedServiceValue()) {
              appendNote(shell, "Select the culprit service to load its span choices.");
              break;
            }
            appendSelect(shell, "selected-span", "Culprit span", spanChoicesForAssessment(assessment), "Select the span");
            restoreSelectValue("selected-span", previousState.selectedSpanID);
            break;
          case "healthy_faulty":
            appendChoiceGroup(shell, "faulty-trace", "checkbox", "Slow traces", assessment.trace_choices);
            appendChoiceGroup(shell, "healthy-trace", "radio", "Healthy trace", assessment.trace_choices);
            restoreCheckedValues("faulty-trace", previousState.faultyTraceIDs);
            restoreCheckedValues("healthy-trace", previousState.healthyTraceID ? [previousState.healthyTraceID] : []);
            break;
          case "before_after":
            appendSelect(shell, "before-trace", "Before trace", assessment.trace_choices, "Select a baseline trace");
            appendSelect(shell, "after-trace", "After trace", assessment.trace_choices, "Select a slow trace");
            restoreSelectValue("before-trace", previousState.beforeTraceID);
            restoreSelectValue("after-trace", previousState.afterTraceID);
            break;
          case "span_attribute":
            if (!selectedServiceValue()) {
              appendNote(shell, "Select the culprit service to load its span choices.");
              break;
            }
            appendSelect(shell, "selected-span", "Culprit span", spanChoicesForAssessment(assessment), "Select the span");
            restoreSelectValue("selected-span", previousState.selectedSpanID);
            if (!selectedSpanValue()) {
              appendNote(shell, "Select the culprit span to load its supporting attributes.");
              break;
            }
            appendSelect(shell, "selected-attribute", "Supporting attribute", attributeChoicesForAssessment(assessment), "Select the attribute");
            restoreSelectValue("selected-attribute", previousState.selectedAttributeID);
            break;
          case "intermittent_failure":
            appendChoiceGroup(shell, "failing-trace", "checkbox", "Failing traces", assessment.trace_choices);
            restoreCheckedValues("failing-trace", previousState.failingTraceIDs);
            break;
        }
      }

      function checkedValues(name) {
        return Array.from(document.querySelectorAll("input[name=\"" + name + "\"]:checked")).map((element) => element.value);
      }

      function checkedValue(name) {
        const selected = document.querySelector("input[name=\"" + name + "\"]:checked");
        return selected ? selected.value : "";
      }

      function assessmentPayload(assessment) {
        const payload = {
          selected_trace_id: "",
          selected_span: "",
          selected_attribute: "",
          faulty_trace_ids: [],
          healthy_trace_id: "",
          before_trace_id: "",
          after_trace_id: "",
          failing_trace_ids: []
        };

        switch (assessment.type) {
          case "trace_search_span":
            payload.selected_trace_id = document.getElementById("selected-trace")?.value || "";
            payload.selected_span = document.getElementById("selected-span")?.value || "";
            break;
          case "culprit_span":
            payload.selected_span = document.getElementById("selected-span")?.value || "";
            break;
          case "healthy_faulty":
            payload.faulty_trace_ids = checkedValues("faulty-trace");
            payload.healthy_trace_id = checkedValue("healthy-trace");
            break;
          case "before_after":
            payload.before_trace_id = document.getElementById("before-trace")?.value || "";
            payload.after_trace_id = document.getElementById("after-trace")?.value || "";
            break;
          case "span_attribute":
            payload.selected_span = document.getElementById("selected-span")?.value || "";
            payload.selected_attribute = document.getElementById("selected-attribute")?.value || "";
            break;
          case "intermittent_failure":
            payload.failing_trace_ids = checkedValues("failing-trace");
            break;
        }
        return payload;
      }

      function validateAssessment(assessment, payload) {
        if (!assessment.ready) {
          return "The challenge is still preparing. Wait for the evidence fields to load.";
        }

        switch (assessment.type) {
          case "trace_search_span":
            if (!payload.selected_trace_id) {
              return "Select the trace you inspected before submitting.";
            }
            return payload.selected_span ? "" : "Select the culprit span before submitting.";
          case "culprit_span":
            return payload.selected_span ? "" : "Select the culprit span before submitting.";
          case "healthy_faulty":
            if (payload.faulty_trace_ids.length === 0) {
              return "Select every slow trace before submitting.";
            }
            return payload.healthy_trace_id ? "" : "Select the healthy trace before submitting.";
          case "before_after":
            if (!payload.before_trace_id) {
              return "Select a before trace before submitting.";
            }
            if (payload.before_trace_id === payload.after_trace_id) {
              return "Select two different traces before submitting.";
            }
            return payload.after_trace_id ? "" : "Select an after trace before submitting.";
          case "span_attribute":
            if (!payload.selected_span) {
              return "Select the culprit span before submitting.";
            }
            return payload.selected_attribute ? "" : "Select the supporting attribute before submitting.";
          case "intermittent_failure":
            return payload.failing_trace_ids.length > 0 ? "" : "Select every failing trace before submitting.";
          default:
            return "";
        }
      }

      function render() {
        const current = currentScenario();
        const assessment = currentAssessment();
        const selected = selectedLevel();
        const scenarioChanged = current.id !== lastScenarioID || coachState.selected_level !== lastSelectedLevel;

        document.getElementById("title").textContent = current.title || "";
        document.getElementById("objective").textContent = current.objective || "";
        document.getElementById("assessment-prompt").textContent = assessment.prompt || "";
        document.getElementById("selected-level-title").textContent = selected ? (selected.title + " • " + selected.summary) : "Level";
        document.getElementById("selected-level-progress").textContent = selected ? (selected.mastery_count + "/" + selected.mastery_target + " correct") : "";

        if (scenarioChanged) {
          document.getElementById("service").value = "";
          document.getElementById("issue").value = "";
          hintLevel = 0;
        }

        renderAssessmentFieldVisibility(assessment);
        renderReferenceTrace(assessment);
        renderAssessmentFields(scenarioChanged || document.getElementById("assessment-fields").childElementCount === 0);
        renderLevels();
        renderHints();
        setFeedback(coachState.feedback, coachState.feedback_ok, coachState.has_feedback);

        lastScenarioID = current.id || "";
        lastSelectedLevel = coachState.selected_level || 0;
      }

      function applySnapshot(snapshot) {
        coachState = snapshot;
        render();
      }

      async function readSnapshot(response) {
        const text = await response.text();
        if (!text) {
          return null;
        }

        try {
          return JSON.parse(text);
        } catch (error) {
          return null;
        }
      }

      async function requestSnapshot(url, options = {}) {
        const response = await fetch(url, options);
        const snapshot = await readSnapshot(response);
        if (snapshot) {
          applySnapshot(snapshot);
        }
        if (!response.ok && !snapshot) {
          throw new Error("request failed with status " + response.status);
        }
        return snapshot;
      }

      async function selectLevel(level) {
        setBusy("Loading that level...");
        try {
          await requestSnapshot("/api/levels/select", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify({level})
          });
        } catch (error) {
          setFeedback("Selecting the level failed. Refresh the page and try again.", false, true);
        } finally {
          clearBusy();
        }
      }

      async function nextChallenge() {
        setBusy("Preparing a new challenge...");
        try {
          await requestSnapshot("/api/challenges/next", {method: "POST"});
        } catch (error) {
          setFeedback("Preparing a new challenge failed. Refresh the page and try again.", false, true);
        } finally {
          clearBusy();
        }
      }

      async function submit() {
        const suspectedService = document.getElementById("service").value;
        const suspectedIssue = document.getElementById("issue").value;
        const current = currentScenario();
        const assessment = currentAssessment();

        if (requiresDiagnosis(assessment) && (!suspectedService || !suspectedIssue)) {
          setFeedback("Select both a culprit service and a failure mode before submitting.", false, true);
          return;
        }

        const payload = assessmentPayload(assessment);
        const validationMessage = validateAssessment(assessment, payload);
        if (validationMessage) {
          setFeedback(validationMessage, false, true);
          return;
        }

        const minimumBusy = delay(minimumSubmitBusyMs);
        setBusy("Checking your answer...");
        try {
          await requestSnapshot("/api/grade", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify({
              scenario_id: current.id,
              suspected_service: suspectedService,
              suspected_issue: suspectedIssue,
              selected_trace_id: payload.selected_trace_id,
              selected_span: payload.selected_span,
              selected_attribute: payload.selected_attribute,
              faulty_trace_ids: payload.faulty_trace_ids,
              healthy_trace_id: payload.healthy_trace_id,
              before_trace_id: payload.before_trace_id,
              after_trace_id: payload.after_trace_id,
              failing_trace_ids: payload.failing_trace_ids
            })
          });
        } catch (error) {
          setFeedback("Submitting the diagnosis failed. Refresh the page and try again.", false, true);
        } finally {
          await minimumBusy;
          clearBusy();
        }
      }

      function connectEvents() {
        const stream = new EventSource("/api/events");
        stream.onmessage = (event) => {
          try {
            applySnapshot(JSON.parse(event.data));
          } catch (error) {
          }
        };
      }

      document.getElementById("next-challenge").addEventListener("click", nextChallenge);
      document.getElementById("hint").addEventListener("click", showHint);
      document.getElementById("submit").addEventListener("click", submit);
      document.querySelectorAll("[data-skill-choice]").forEach((button) => {
        button.addEventListener("click", chooseSkillPlacement);
      });
      document.getElementById("skill-modal-close").addEventListener("click", closeSkillPlacement);
      document.getElementById("service").addEventListener("change", () => {
        const type = currentAssessment().type;
        if (type === "culprit_span" || type === "span_attribute") {
          renderAssessmentFields(true);
        }
      });
      document.getElementById("assessment-fields").addEventListener("change", (event) => {
        const type = currentAssessment().type;
        if (event.target.id === "selected-trace" && type === "trace_search_span") {
          renderAssessmentFields(true);
        }
        if (event.target.id === "selected-span" && type === "span_attribute") {
          renderAssessmentFields(true);
        }
      });
      document.getElementById("toggle-levels").addEventListener("click", toggleLevels);
      mobileProgressionQuery.addEventListener("change", syncLevelsCollapsed);

      syncLevelsCollapsed();
      render();
      connectEvents();
      maybeShowSkillPlacement();
    </script>
  </body>
</html>
`
