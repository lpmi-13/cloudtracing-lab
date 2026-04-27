package main

import (
	"fmt"
	"math/rand"
	"sort"
	"time"

	"cloudtracing/internal/scenario"
)

const (
	correctTarget           = 5
	maxAttemptsPerChallenge = 2
)

type levelDefinition struct {
	Number    int
	Title     string
	Summary   string
	Scenarios []scenario.Definition
}

type levelSession struct {
	CorrectCount      int
	IncorrectAttempts int
	Current           scenario.Definition
	Prepared          bool
	Challenge         *preparedChallenge
}

type learnerSession struct {
	SelectedLevel int
	Feedback      string
	FeedbackOK    bool
	HasFeedback   bool
	Levels        map[int]*levelSession
}

type publicLevel struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	Unlocked      bool   `json:"unlocked"`
	Selected      bool   `json:"selected"`
	ReadyToMoveOn bool   `json:"ready_to_move_on"`
	CorrectCount  int    `json:"correct_count"`
	CorrectTarget int    `json:"correct_target"`
}

type coachSnapshot struct {
	Levels          []publicLevel  `json:"levels"`
	CurrentScenario publicScenario `json:"current_scenario"`
	Feedback        string         `json:"feedback"`
	FeedbackOK      bool           `json:"feedback_ok"`
	HasFeedback     bool           `json:"has_feedback"`
	JaegerUIURL     string         `json:"jaeger_ui_url"`
	SelectedLevel   int            `json:"selected_level"`
	CorrectTarget   int            `json:"correct_target"`
}

var levelBlueprints = []struct {
	Number  int
	Title   string
	Summary string
}{
	{Number: 1, Title: "Level 1", Summary: "Find the Slow Span"},
	{Number: 2, Title: "Level 2", Summary: "Find the Slow Service With Noise"},
	{Number: 3, Title: "Level 3", Summary: "What Changed?"},
	{Number: 4, Title: "Level 4", Summary: "Dig Into the Details"},
	{Number: 5, Title: "Level 5", Summary: "Find the Intermittent Failure"},
}

func buildLevels(defs []scenario.Definition) ([]levelDefinition, error) {
	grouped := make(map[int][]scenario.Definition, len(levelBlueprints))
	for _, def := range defs {
		if err := validateScenarioDefinition(def); err != nil {
			return nil, err
		}
		if def.Level < 1 || def.Level > len(levelBlueprints) {
			return nil, fmt.Errorf("scenario %q has invalid level %d", def.ID, def.Level)
		}
		grouped[def.Level] = append(grouped[def.Level], def)
	}

	levels := make([]levelDefinition, 0, len(levelBlueprints))
	for _, blueprint := range levelBlueprints {
		scenarios := grouped[blueprint.Number]
		if len(scenarios) == 0 {
			return nil, fmt.Errorf("level %d has no scenarios", blueprint.Number)
		}
		if len(scenarios) < 3 {
			return nil, fmt.Errorf("level %d needs at least 3 scenario variants, found %d", blueprint.Number, len(scenarios))
		}
		sort.Slice(scenarios, func(i, j int) bool {
			return scenarios[i].ID < scenarios[j].ID
		})
		levels = append(levels, levelDefinition{
			Number:    blueprint.Number,
			Title:     blueprint.Title,
			Summary:   blueprint.Summary,
			Scenarios: scenarios,
		})
	}

	return levels, nil
}

func validateScenarioDefinition(def scenario.Definition) error {
	if def.ID == "" {
		return fmt.Errorf("scenario is missing an id")
	}
	if def.AssessmentType == "" {
		return fmt.Errorf("scenario %q is missing an assessment_type", def.ID)
	}
	if def.Objective == "" || def.Prompt == "" || def.AssessmentPrompt == "" {
		return fmt.Errorf("scenario %q is missing objective, prompt, or assessment prompt", def.ID)
	}
	if def.ExpectedService == "" || def.ExpectedIssue == "" {
		return fmt.Errorf("scenario %q is missing expected service or issue", def.ID)
	}
	if def.AnswerKey.Service == "" {
		def.AnswerKey.Service = def.ExpectedService
	}
	if def.AnswerKey.Issue == "" {
		def.AnswerKey.Issue = def.ExpectedIssue
	}
	if def.AnswerKey.Service == "" || def.AnswerKey.Issue == "" {
		return fmt.Errorf("scenario %q is missing answer key service or issue", def.ID)
	}

	switch def.AssessmentType {
	case assessmentTraceSearchSpan, assessmentCulpritSpan, assessmentSpanAttribute:
		if def.AnswerKey.SpanOperation == "" {
			return fmt.Errorf("scenario %q is missing answer key span_operation", def.ID)
		}
	}
	if def.AssessmentType == assessmentSpanAttribute {
		if def.AnswerKey.AttributeKey == "" && def.AnswerKey.SpanAttributeKey == "" {
			return fmt.Errorf("scenario %q is missing answer key attribute", def.ID)
		}
	}
	return nil
}

func newLearnerSession(levels []levelDefinition) learnerSession {
	session := learnerSession{
		SelectedLevel: 1,
		Levels:        make(map[int]*levelSession, len(levels)),
	}

	for _, level := range levels {
		session.Levels[level.Number] = &levelSession{}
	}

	if len(levels) > 0 {
		session.Levels[1].Current = levels[0].Scenarios[0]
	}

	return session
}

func pickRandomScenario(defs []scenario.Definition, exclude string) scenario.Definition {
	filtered := make([]scenario.Definition, 0, len(defs))
	for _, def := range defs {
		if def.ID == exclude && len(defs) > 1 {
			continue
		}
		filtered = append(filtered, def)
	}
	if len(filtered) == 0 {
		return defs[0]
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return filtered[rng.Intn(len(filtered))]
}

func (s *coachServer) levelDefinition(number int) (levelDefinition, bool) {
	for _, level := range s.levels {
		if level.Number == number {
			return level, true
		}
	}
	return levelDefinition{}, false
}

func (s *coachServer) levelStateLocked(level int) *levelSession {
	return s.state.Levels[level]
}

func (s *coachServer) selectedLevelStateLocked() *levelSession {
	return s.levelStateLocked(s.state.SelectedLevel)
}

func (s *coachServer) ensureScenarioForLevelLocked(level int) scenario.Definition {
	state := s.levelStateLocked(level)
	if state.Current.ID != "" {
		return state.Current
	}

	def, _ := s.levelDefinition(level)
	state.Current = pickRandomScenario(def.Scenarios, "")
	state.IncorrectAttempts = 0
	state.Prepared = false
	state.Challenge = nil
	return state.Current
}

func (s *coachServer) pickRandomForLevel(level int, exclude string) scenario.Definition {
	def, ok := s.levelDefinition(level)
	if !ok {
		return scenario.Definition{}
	}
	return pickRandomScenario(def.Scenarios, exclude)
}

func (s *coachServer) pickRandomForLevelDifferentVariant(level int, excludeID, excludeVariantGroup string) scenario.Definition {
	def, ok := s.levelDefinition(level)
	if !ok {
		return scenario.Definition{}
	}

	filtered := make([]scenario.Definition, 0, len(def.Scenarios))
	for _, candidate := range def.Scenarios {
		if candidate.ID == excludeID && len(def.Scenarios) > 1 {
			continue
		}
		if excludeVariantGroup != "" && candidate.VariantGroup == excludeVariantGroup {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		return pickRandomScenario(def.Scenarios, excludeID)
	}
	return pickRandomScenario(filtered, "")
}

func (s *coachServer) levelLabel(level int) string {
	def, ok := s.levelDefinition(level)
	if !ok {
		return fmt.Sprintf("Level %d", level)
	}
	return fmt.Sprintf("%s: %s", def.Title, def.Summary)
}

func (s *coachServer) setLevelScenarioLocked(level int, next scenario.Definition) {
	state := s.levelStateLocked(level)
	if state == nil {
		return
	}
	state.Current = next
	state.IncorrectAttempts = 0
	state.Prepared = false
	state.Challenge = nil
}

func (s *coachServer) setFeedbackLocked(message string, ok bool) {
	s.state.Feedback = message
	s.state.FeedbackOK = ok
	s.state.HasFeedback = true
}

func (s *coachServer) clearFeedbackLocked() {
	s.state.Feedback = ""
	s.state.FeedbackOK = false
	s.state.HasFeedback = false
}

func (s *coachServer) snapshotLocked() coachSnapshot {
	levels := make([]publicLevel, 0, len(s.levels))
	for _, level := range s.levels {
		state := s.levelStateLocked(level.Number)
		levels = append(levels, publicLevel{
			Number:        level.Number,
			Title:         level.Title,
			Summary:       level.Summary,
			Unlocked:      true,
			Selected:      s.state.SelectedLevel == level.Number,
			ReadyToMoveOn: state.CorrectCount >= correctTarget,
			CorrectCount:  state.CorrectCount,
			CorrectTarget: correctTarget,
		})
	}

	selected := s.ensureScenarioForLevelLocked(s.state.SelectedLevel)
	selectedState := s.levelStateLocked(s.state.SelectedLevel)
	return coachSnapshot{
		Levels:          levels,
		CurrentScenario: s.toPublic(selected, selectedState.Challenge),
		Feedback:        s.state.Feedback,
		FeedbackOK:      s.state.FeedbackOK,
		HasFeedback:     s.state.HasFeedback,
		JaegerUIURL:     s.jaegerUIURL,
		SelectedLevel:   s.state.SelectedLevel,
		CorrectTarget:   correctTarget,
	}
}

func (s *coachServer) snapshotAndSubscribersLocked() (coachSnapshot, []chan coachSnapshot) {
	snapshot := s.snapshotLocked()
	subscribers := make([]chan coachSnapshot, 0, len(s.subscribers))
	for _, ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	return snapshot, subscribers
}

func (s *coachServer) broadcast(snapshot coachSnapshot, subscribers []chan coachSnapshot) {
	for _, ch := range subscribers {
		select {
		case ch <- snapshot:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshot:
			default:
			}
		}
	}
}

func (s *coachServer) subscribe() (int, chan coachSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan coachSnapshot, 1)
	id := s.nextSubscriberID
	s.nextSubscriberID++
	s.subscribers[id] = ch

	ch <- s.snapshotLocked()
	return id, ch
}

func (s *coachServer) unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, ok := s.subscribers[id]
	if !ok {
		return
	}
	delete(s.subscribers, id)
	close(ch)
}
