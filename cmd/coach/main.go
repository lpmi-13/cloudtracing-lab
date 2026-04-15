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
	"strings"
	"sync"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
)

type coachServer struct {
	client           *http.Client
	jaegerURL        string
	webURL           string
	scenarios        map[string]scenario.Definition
	scenarioSet      []scenario.Definition
	levels           []levelDefinition
	page             *template.Template
	findRecentTraces func(ctx context.Context, service, operation string, since time.Time, need, limit int) ([]traceRecord, error)

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
		jaegerURL:   strings.TrimRight(defaultEnv("JAEGER_UI_URL", ""), "/"),
		webURL:      strings.TrimRight(defaultEnv("WEB_URL", "http://shop-web:8080"), "/"),
		scenarios:   scenarios,
		scenarioSet: scenarioSet,
		levels:      levels,
		page:        template.Must(template.New("page").Parse(pageTemplate)),
		state:       newLearnerSession(levels),
		subscribers: map[int]chan coachSnapshot{},
	}

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

	addr := ":8080"
	log.Printf("coach listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *coachServer) index(w http.ResponseWriter, r *http.Request) {
	s.actionMu.Lock()
	selectedLevel, def := s.selectedLevelAndScenario()
	generated, err := s.prepareSelectedLevelScenario(r.Context(), defaultTraceBatchSize)

	s.mu.Lock()
	if err != nil {
		s.setFeedbackLocked("The current challenge is loaded, but automatic trace generation failed. Refresh or request a new challenge and try again.", false)
	} else if generated > 0 {
		s.setFeedbackLocked(fmt.Sprintf("Prepared %s for %s. %s", freshTraceText(generated), s.levelLabel(selectedLevel), focusTraceFeedback(def)), false)
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
		"JaegerURL":    s.jaegerURL,
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
	state := s.levelStateLocked(level)
	state.Current = next
	state.Prepared = false
	state.Challenge = nil
	s.mu.Unlock()

	generated, err := s.prepareLevelScenario(r.Context(), level, defaultTraceBatchSize)

	s.mu.Lock()
	if err != nil {
		s.setFeedbackLocked(fmt.Sprintf("A fresh challenge was selected for %s, but automatic trace generation failed. Refresh or try again.", s.levelLabel(level)), false)
	} else if generated > 0 {
		s.setFeedbackLocked(fmt.Sprintf("Prepared %s for a new challenge in %s. %s", freshTraceText(generated), s.levelLabel(level), focusTraceFeedback(next)), false)
	} else {
		s.setFeedbackLocked(fmt.Sprintf("Loaded the current challenge for %s. %s", s.levelLabel(level), focusTraceFeedback(next)), false)
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

	target := s.levelStateLocked(req.Level)
	if !target.Unlocked {
		s.setFeedbackLocked(fmt.Sprintf("%s is still locked. Master the previous level at %d/%d to unlock it.", s.levelLabel(req.Level), masteryTarget, masteryTarget), false)
		snapshot, subscribers := s.snapshotAndSubscribersLocked()
		s.mu.Unlock()
		s.actionMu.Unlock()
		s.broadcast(snapshot, subscribers)
		app.WriteJSON(w, http.StatusConflict, snapshot)
		return
	}

	s.state.SelectedLevel = req.Level
	unlockedLevel := s.unlockNextLevelIfEligibleLocked()
	selected := s.ensureScenarioForLevelLocked(req.Level)
	prepared := s.levelStateLocked(req.Level).Prepared
	s.mu.Unlock()

	generated := 0
	var err error
	if !prepared {
		generated, err = s.prepareLevelScenario(r.Context(), req.Level, defaultTraceBatchSize)
	}

	s.mu.Lock()
	message := fmt.Sprintf("%s selected.", s.levelLabel(req.Level))
	if unlockedLevel > 0 {
		message += fmt.Sprintf(" %s is now unlocked.", s.levelLabel(unlockedLevel))
	}
	if err != nil {
		message += " The challenge loaded, but automatic trace generation failed. Refresh or request a new challenge and try again."
	} else if generated > 0 {
		message += fmt.Sprintf(" Prepared %s. %s", freshTraceText(generated), focusTraceFeedback(selected))
	} else {
		message += " " + focusTraceFeedback(selected)
	}
	s.setFeedbackLocked(message, false)
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
		s.setFeedbackLocked(fmt.Sprintf("%s %s remains at %d/%d mastery.", result.Message, s.levelLabel(level), state.MasteryCount, masteryTarget), false)
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
	unlockedLevel := s.unlockNextLevelIfEligibleLocked()
	next := s.pickRandomForLevel(level, current.ID)
	state.Current = next
	state.Prepared = false
	state.Challenge = nil
	s.mu.Unlock()

	generated, err := s.prepareLevelScenario(r.Context(), level, defaultTraceBatchSize)

	s.mu.Lock()
	message := fmt.Sprintf("Correct. %s is now at %d/%d mastery.", s.levelLabel(level), masteryCount, masteryTarget)
	if unlockedLevel > 0 {
		message += fmt.Sprintf("\n\n%s unlocked and is now selectable.", s.levelLabel(unlockedLevel))
	}
	if err != nil {
		message += "\n\nA fresh challenge was selected, but automatic trace generation failed. Refresh or request a new challenge and try again."
	} else {
		message += fmt.Sprintf("\n\nPrepared %s for the next challenge in this level. %s", freshTraceText(generated), focusTraceFeedback(next))
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
		pathSuccesses, err := s.generateTrafficRequests(ctx, trafficPath, def.ID, count)
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

func freshTraceText(count int) string {
	if count == 1 {
		return "1 fresh trace"
	}
	return fmt.Sprintf("%d fresh traces", count)
}

func focusTraceFeedback(def scenario.Definition) string {
	return startGuideFor(def)
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
    <meta name="description" content="Level-based trace practice with shared progression, five-attempt mastery gates, and synchronized state across every open coach tab.">
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
        --locked: #e7ded0;
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
        gap: 18px;
      }
      .panel {
        background: var(--panel);
        border: 1px solid var(--border);
        border-radius: 20px;
        padding: 20px;
        box-shadow: 0 12px 28px rgba(0, 0, 0, 0.06);
      }
      .grid {
        display: grid;
        grid-template-columns: 1.45fr 1fr;
        gap: 18px;
      }
      .level-grid {
        display: grid;
        grid-template-columns: repeat(5, minmax(0, 1fr));
        gap: 12px;
      }
      @media (max-width: 1080px) {
        .level-grid {
          grid-template-columns: repeat(2, minmax(0, 1fr));
        }
      }
      @media (max-width: 840px) {
        .grid { grid-template-columns: 1fr; }
        .level-grid { grid-template-columns: 1fr; }
      }
      h1, h2, h3, h4 { margin-top: 0; }
      p { line-height: 1.45; }
      .muted { color: var(--muted); }
      .badge {
        display: inline-block;
        padding: 6px 10px;
        border-radius: 999px;
        background: var(--accent-soft);
        margin-right: 8px;
        margin-bottom: 8px;
      }
      .eyebrow {
        font-size: 0.84rem;
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
        padding: 10px 14px;
        cursor: pointer;
        text-decoration: none;
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
        border-radius: 18px;
        border: 1px solid var(--border);
        background: white;
        color: var(--ink);
        padding: 14px;
        display: grid;
        gap: 10px;
      }
      .level-button.selected {
        background: var(--accent);
        color: white;
        border-color: var(--accent-strong);
      }
      .level-button.locked {
        background: var(--locked);
        color: var(--muted);
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
      .level-summary {
        font-size: 0.96rem;
        line-height: 1.35;
      }
      .level-progress {
        font-size: 0.92rem;
        font-weight: 600;
      }
      .actions {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
      }
      .status-row {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
      }
      .status-pill {
        padding: 6px 10px;
        border-radius: 999px;
        background: #f3ede3;
      }
      #feedback {
        min-height: 72px;
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
        min-height: 72px;
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
      .assessment-card {
        padding: 14px;
        border-radius: 14px;
        background: #f3ede3;
      }
      .evidence-list {
        margin: 10px 0 0;
        padding-left: 20px;
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
    </style>
  </head>
  <body>
    <main class="stack">
      <section class="panel stack">
        <div class="eyebrow">Shared Progression</div>
        <div class="status-row">
          <strong id="selected-level-title"></strong>
          <span id="selected-level-progress" class="status-pill"></span>
          <span class="status-pill">Mastery gate: 5 correct attempts</span>
          <span class="status-pill">Unlocked levels stay selectable</span>
        </div>
        <p class="muted">Every open coach tab shares the same selected level, current challenge, and mastery counts. Unlocking stays sequential: master the selected level to open the next one.</p>
        <div id="levels" class="level-grid"></div>
      </section>

      <section class="grid">
        <article class="panel stack">
          <div>
            <div class="badge">Localhost Ports / Ingress</div>
            <div class="badge">Python Web Tier</div>
            <div class="badge">Go + Python App Tier</div>
            <div class="badge">PostgreSQL + Redis + Meilisearch</div>
          </div>
          <div>
            <div class="eyebrow">Current Challenge</div>
            <h2 id="title"></h2>
            <p id="objective"></p>
            <p id="prompt" class="muted"></p>
            <p class="muted">Selecting an unlocked level loads that level's current challenge. Requesting a new challenge keeps you in the same level and seeds fresh traces across the shop endpoints.</p>
          </div>
          <div class="actions">
            {{if .JaegerURL}}<a class="button" target="_blank" rel="noreferrer" href="{{.JaegerURL}}">Open Jaeger</a>{{end}}
            <button id="next-challenge" type="button">New Challenge</button>
          </div>
          <div>
            <strong>Assessment Contract</strong>
            <p id="assessment-prompt" class="muted"></p>
            <p id="start-guide" class="muted"></p>
            <div id="reference-trace" class="reference-trace muted"></div>
            <div class="assessment-card">
              <div class="eyebrow">Required Evidence</div>
              <ul id="required-evidence" class="evidence-list"></ul>
              <p id="pass-condition" class="muted"></p>
            </div>
          </div>
          <div>
            <strong>Need a hint?</strong>
            <p class="muted">Use hints only if you are stuck moving from the entry span to the next service layer.</p>
            <div id="hint-shell" class="hint-shell" aria-hidden="true">
              <div id="hint-box" class="muted"></div>
            </div>
            <div class="actions">
              <button id="hint" type="button">Show Hint</button>
            </div>
          </div>
        </article>

        <aside class="panel stack">
          <div>
            <h3>Submit Diagnosis</h3>
            <label class="field-label" for="service">Culprit service</label>
            <select id="service">
              <option value="">Select service</option>
              <option value="catalog-api">catalog-api</option>
              <option value="inventory-api">inventory-api</option>
              <option value="orders-api">orders-api</option>
              <option value="payments-api">payments-api</option>
            </select>
            <label class="field-label" for="issue">Failure mode</label>
            <select id="issue">
              <option value="">Select issue type</option>
              <option value="expensive_search_query">expensive search query</option>
              <option value="n_plus_one_queries">n plus one queries</option>
              <option value="lock_wait_timeout">lock wait timeout</option>
              <option value="expensive_sort">expensive sort</option>
            </select>
            <div id="assessment-fields" class="stack"></div>
            <div class="actions">
              <button id="submit" type="button">Check Answer</button>
            </div>
          </div>
          <div>
            <h4>Coach Feedback</h4>
            <div id="feedback"></div>
          </div>
        </aside>
      </section>
    </main>

    <div id="busy-overlay" class="hidden" aria-live="polite" aria-busy="true">
      <div class="panel modal">
        <div class="spinner" aria-hidden="true"></div>
        <strong id="busy-title">Loading...</strong>
      </div>
    </div>

    <script>
      const initialState = {{.InitialState}};
      const learnerLoopHint = "1. Read the scenario.\n2. Open Jaeger.\n3. Use the assessment contract to decide what evidence you need.";
      const minimumSubmitBusyMs = 700;
      let coachState = initialState;
      let hintLevel = 0;
      let lastScenarioID = "";
      let lastSelectedLevel = 0;

      function currentScenario() {
        return coachState.current_scenario || {};
      }

      function currentAssessment() {
        return currentScenario().assessment || {};
      }

      function selectedLevel() {
        return (coachState.levels || []).find((level) => level.selected) || null;
      }

      function setFeedback(message, ok = false) {
        const box = document.getElementById("feedback");
        box.textContent = message || "";
        box.className = ok ? "ok" : "";
      }

      function delay(ms) {
        return new Promise((resolve) => window.setTimeout(resolve, ms));
      }

      function hintsForCurrent() {
        const current = currentScenario();
        return [learnerLoopHint, current.hint_1, current.hint_2].filter(Boolean);
      }

      function renderHints() {
        const hints = hintsForCurrent();
        const shell = document.getElementById("hint-shell");
        const box = document.getElementById("hint-box");
        const button = document.getElementById("hint");
        const isOpen = hints.length > 0 && hintLevel > 0;

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
          button.disabled = disabled || button.dataset.unlocked !== "true";
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
          button.dataset.unlocked = String(level.unlocked);
          if (level.selected) {
            button.classList.add("selected");
          }
          if (!level.unlocked) {
            button.classList.add("locked");
          }
          if (level.mastered) {
            button.classList.add("mastered");
          }
          button.disabled = !level.unlocked;
          button.innerHTML =
            "<div class=\"level-topline\">" +
              "<strong>" + level.title + "</strong>" +
              "<span>" + (level.unlocked ? "Unlocked" : "Locked") + "</span>" +
            "</div>" +
            "<div class=\"level-summary\">" + level.summary + "</div>" +
            "<div class=\"level-progress\">" + level.mastery_count + "/" + level.mastery_target + " correct" + (level.mastered ? " • Mastered" : "") + "</div>";
          if (level.unlocked) {
            button.addEventListener("click", () => selectLevel(level.number));
          }
          root.appendChild(button);
        });
      }

      function renderReferenceTrace(assessment) {
        const shell = document.getElementById("reference-trace");
        shell.innerHTML = "";

        if (assessment.reference_trace) {
          const intro = document.createElement("span");
          intro.textContent = "Reference trace: ";
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

        shell.textContent = "The candidate traces for this challenge are ready below. Open Jaeger to inspect them.";
      }

      function renderEvidenceList(assessment) {
        const list = document.getElementById("required-evidence");
        list.innerHTML = "";
        (assessment.required_evidence || []).forEach((item) => {
          const li = document.createElement("li");
          li.textContent = item;
          list.appendChild(li);
        });
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

      function renderAssessmentFields(force) {
        const shell = document.getElementById("assessment-fields");
        const assessment = currentAssessment();
        const signature = [
          assessment.type || "",
          String(assessment.ready),
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
          case "culprit_span":
            appendSelect(shell, "selected-span", "Culprit span", assessment.span_choices, "Select the span");
            break;
          case "healthy_faulty":
            appendChoiceGroup(shell, "faulty-trace", "checkbox", "Regressed traces", assessment.trace_choices);
            appendChoiceGroup(shell, "healthy-trace", "radio", "Healthy comparison trace", assessment.trace_choices);
            break;
          case "before_after":
            appendSelect(shell, "before-trace", "Before trace", assessment.before_trace_choices, "Select a baseline trace");
            appendSelect(shell, "after-trace", "After trace", assessment.after_trace_choices, "Select a regressed trace");
            break;
          case "span_attribute":
            appendSelect(shell, "selected-span", "Culprit span", assessment.span_choices, "Select the span");
            appendSelect(shell, "selected-attribute", "Supporting attribute", assessment.attribute_choices, "Select the attribute");
            break;
          case "intermittent_failure":
            appendChoiceGroup(shell, "failing-trace", "checkbox", "Failing traces", assessment.trace_choices);
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
          selected_span: "",
          selected_attribute: "",
          faulty_trace_ids: [],
          healthy_trace_id: "",
          before_trace_id: "",
          after_trace_id: "",
          failing_trace_ids: []
        };

        switch (assessment.type) {
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
          case "culprit_span":
            return payload.selected_span ? "" : "Select the culprit span before submitting.";
          case "healthy_faulty":
            if (payload.faulty_trace_ids.length === 0) {
              return "Select every regressed trace before submitting.";
            }
            return payload.healthy_trace_id ? "" : "Select the healthy comparison trace before submitting.";
          case "before_after":
            if (!payload.before_trace_id) {
              return "Select a before trace before submitting.";
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
        document.getElementById("prompt").textContent = current.prompt || "";
        document.getElementById("assessment-prompt").textContent = assessment.prompt || "";
        document.getElementById("start-guide").textContent = assessment.start_guide || "";
        document.getElementById("pass-condition").textContent = assessment.pass_condition || "";
        document.getElementById("selected-level-title").textContent = selected ? (selected.title + ": " + selected.summary) : "Level";
        document.getElementById("selected-level-progress").textContent = selected ? (selected.mastery_count + "/" + selected.mastery_target + " correct") : "";

        if (scenarioChanged) {
          document.getElementById("service").value = "";
          document.getElementById("issue").value = "";
          hintLevel = 0;
        }

        renderReferenceTrace(assessment);
        renderEvidenceList(assessment);
        renderAssessmentFields(scenarioChanged || document.getElementById("assessment-fields").childElementCount === 0);
        renderLevels();
        renderHints();
        setFeedback(coachState.feedback, coachState.feedback_ok);

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
          setFeedback("Selecting the level failed. Refresh the page and try again.");
        } finally {
          clearBusy();
        }
      }

      async function nextChallenge() {
        setBusy("Preparing a new challenge...");
        try {
          await requestSnapshot("/api/challenges/next", {method: "POST"});
        } catch (error) {
          setFeedback("Preparing a new challenge failed. Refresh the page and try again.");
        } finally {
          clearBusy();
        }
      }

      async function submit() {
        const suspectedService = document.getElementById("service").value;
        const suspectedIssue = document.getElementById("issue").value;
        const current = currentScenario();
        const assessment = currentAssessment();

        if (!suspectedService || !suspectedIssue) {
          setFeedback("Select both a culprit service and a failure mode before submitting.");
          return;
        }

        const payload = assessmentPayload(assessment);
        const validationMessage = validateAssessment(assessment, payload);
        if (validationMessage) {
          setFeedback(validationMessage);
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
          setFeedback("Submitting the diagnosis failed. Refresh the page and try again.");
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

      render();
      connectEvents();
    </script>
  </body>
</html>
`
