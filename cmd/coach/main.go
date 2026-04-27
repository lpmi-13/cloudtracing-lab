package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	mux.Handle("/app.css", http.HandlerFunc(serveCoachCSS))
	mux.Handle("/app.js", http.HandlerFunc(serveCoachJS))
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveCoachIndex(w, r)
}

func (s *coachServer) preparedSnapshot(ctx context.Context) (coachSnapshot, []chan coachSnapshot, int, error) {
	s.actionMu.Lock()
	selectedLevel, def := s.selectedLevelAndScenario()
	generated, err := s.prepareSelectedLevelScenario(ctx, defaultTraceBatchSize)

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

	return snapshot, subscribers, generated, err
}

func (s *coachServer) stateSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, subscribers, generated, err := s.preparedSnapshot(r.Context())
	if generated > 0 || err != nil {
		s.broadcast(snapshot, subscribers)
	}
	app.WriteJSON(w, http.StatusOK, snapshot)
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
			s.setFeedbackLocked(fmt.Sprintf("%s %s remains at %d/%d correct.", result.Message, s.levelLabel(level), state.CorrectCount, correctTarget), false)
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
			s.setFeedbackLocked(fmt.Sprintf("%s %s remains at %d/%d correct. %d %s remain on this challenge before the coach loads a new one.", result.Message, s.levelLabel(level), state.CorrectCount, correctTarget, remaining, attemptLabel), false)
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
		if err != nil {
			s.setFeedbackLocked("The coach loaded a new challenge, but automatic trace generation failed. Refresh or request a new challenge and try again.", false)
		} else {
			s.clearFeedbackLocked()
		}
		snapshot, subscribers := s.snapshotAndSubscribersLocked()
		s.mu.Unlock()
		s.actionMu.Unlock()
		s.broadcast(snapshot, subscribers)
		app.WriteJSON(w, http.StatusOK, snapshot)
		return
	}

	if state.CorrectCount < correctTarget {
		state.CorrectCount++
	}
	correctCount := state.CorrectCount
	next := s.pickRandomForLevel(level, current.ID)
	s.setLevelScenarioLocked(level, next)
	s.mu.Unlock()

	_, err := s.prepareLevelScenario(r.Context(), level, defaultTraceBatchSize)

	s.mu.Lock()
	message := fmt.Sprintf("Correct. %s is now at %d/%d correct.", s.levelLabel(level), correctCount, correctTarget)
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
