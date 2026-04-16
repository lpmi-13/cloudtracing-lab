package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
)

func TestRealScenarioCatalogBuildsLevels(t *testing.T) {
	defs, err := scenario.Load(filepath.Join("..", "..", "scenarios", "scenarios.json"))
	if err != nil {
		t.Fatalf("load scenarios: %v", err)
	}

	levels, err := buildLevels(defs)
	if err != nil {
		t.Fatalf("buildLevels: %v", err)
	}
	if len(levels) != 5 {
		t.Fatalf("expected 5 levels, got %d", len(levels))
	}
	for _, level := range levels {
		if len(level.Scenarios) < 3 {
			t.Fatalf("level %d expected at least 3 scenarios, got %d", level.Number, len(level.Scenarios))
		}
	}
}

func TestBuildLevelsRequiresThreeVariantsPerLevel(t *testing.T) {
	defs := testScenarioSet()
	defs = defs[:len(defs)-1]

	if _, err := buildLevels(defs); err == nil || !strings.Contains(err.Error(), "needs at least 3 scenario variants") {
		t.Fatalf("expected missing-variant error, got %v", err)
	}
}

func TestBuildPreparedChallengeCreatesCulpritSpanAssessment(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 1)
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty: []traceRecord{
			traceFixture("trace-1", def, nil),
		},
	}, func(id string) string { return "/trace/" + id })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	if !challenge.Public.Ready {
		t.Fatal("expected challenge to be ready")
	}
	if challenge.Public.ReferenceTrace == nil || challenge.Public.ReferenceTrace.ID != "trace-1" {
		t.Fatalf("expected reference trace trace-1, got %+v", challenge.Public.ReferenceTrace)
	}
	if len(challenge.Public.SpanChoices) == 0 {
		t.Fatal("expected span choices")
	}
	if challenge.ExpectedSpanID != spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation) {
		t.Fatalf("unexpected expected span id: %s", challenge.ExpectedSpanID)
	}
}

func TestGradeSubmissionRequiresSpanEvidence(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 1)
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty: []traceRecord{
			traceFixture("trace-1", def, nil),
		},
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	result := gradeSubmission(def, challenge, gradeRequest{
		SuspectedService: def.ExpectedService,
		SuspectedIssue:   def.ExpectedIssue,
		SelectedSpan:     spanChoiceID("edge-api", "GET /api/search"),
	})
	if result.Pass {
		t.Fatal("expected wrong span to fail")
	}

	result = gradeSubmission(def, challenge, gradeRequest{
		SuspectedService: def.ExpectedService,
		SuspectedIssue:   def.ExpectedIssue,
		SelectedSpan:     challenge.ExpectedSpanID,
	})
	if !result.Pass {
		t.Fatalf("expected correct span to pass, got %q", result.Message)
	}
}

func TestGradeSubmissionRequiresMixedTraceClassification(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 2)
	challenge := &preparedChallenge{
		Public:                  publicAssessment{Ready: true, Type: def.AssessmentType},
		ExpectedFaultyTraceIDs:  []string{"trace-a", "trace-b"},
		ExpectedHealthyTraceIDs: []string{"trace-c"},
	}

	result := gradeSubmission(def, challenge, gradeRequest{
		SuspectedService: def.ExpectedService,
		SuspectedIssue:   def.ExpectedIssue,
		FaultyTraceIDs:   []string{"trace-a"},
		HealthyTraceID:   "trace-c",
	})
	if result.Pass {
		t.Fatal("expected incomplete faulty set to fail")
	}

	result = gradeSubmission(def, challenge, gradeRequest{
		SuspectedService: def.ExpectedService,
		SuspectedIssue:   def.ExpectedIssue,
		FaultyTraceIDs:   []string{"trace-b", "trace-a"},
		HealthyTraceID:   "trace-c",
	})
	if !result.Pass {
		t.Fatalf("expected exact mixed trace selection to pass, got %q", result.Message)
	}
}

func TestGradeSubmissionAllowsAnyValidBeforeAfterPair(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 3)
	challenge := &preparedChallenge{
		Public:                 publicAssessment{Ready: true, Type: def.AssessmentType},
		ExpectedBeforeTraceIDs: []string{"before-1", "before-2"},
		ExpectedAfterTraceIDs:  []string{"after-1", "after-2"},
	}

	result := gradeSubmission(def, challenge, gradeRequest{
		SuspectedService: def.ExpectedService,
		SuspectedIssue:   def.ExpectedIssue,
		BeforeTraceID:    "before-2",
		AfterTraceID:     "after-1",
	})
	if !result.Pass {
		t.Fatalf("expected allowed before/after pair to pass, got %q", result.Message)
	}
}

func TestGradeSubmissionRequiresSupportingAttribute(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 4)
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty: []traceRecord{
			traceFixture("trace-attribute", def, map[string]string{
				def.AnswerKey.AttributeKey: def.AnswerKey.AttributeValue,
			}),
		},
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	result := gradeSubmission(def, challenge, gradeRequest{
		SuspectedService:  def.ExpectedService,
		SuspectedIssue:    def.ExpectedIssue,
		SelectedSpan:      challenge.ExpectedSpanID,
		SelectedAttribute: "lab.failure_mode=wrong.value",
	})
	if result.Pass {
		t.Fatal("expected wrong attribute to fail")
	}

	result = gradeSubmission(def, challenge, gradeRequest{
		SuspectedService:  def.ExpectedService,
		SuspectedIssue:    def.ExpectedIssue,
		SelectedSpan:      challenge.ExpectedSpanID,
		SelectedAttribute: challenge.ExpectedAttributeID,
	})
	if !result.Pass {
		t.Fatalf("expected correct supporting attribute to pass, got %q", result.Message)
	}
}

func TestUnlockNextLevelRequiresFiveCorrectAttemptsOnSelectedLevel(t *testing.T) {
	s := newTestCoachServer(t, testScenarioSet())

	s.mu.Lock()
	defer s.mu.Unlock()

	for attempts := 1; attempts < masteryTarget; attempts++ {
		s.state.Levels[1].MasteryCount = attempts
		if got := s.unlockNextLevelIfEligibleLocked(); got != 0 {
			t.Fatalf("expected no unlock at %d attempts, got level %d", attempts, got)
		}
		if s.state.Levels[2].Unlocked {
			t.Fatalf("level 2 should still be locked at %d attempts", attempts)
		}
	}

	s.state.Levels[1].MasteryCount = masteryTarget
	if got := s.unlockNextLevelIfEligibleLocked(); got != 2 {
		t.Fatalf("expected level 2 to unlock at five attempts, got %d", got)
	}
	if !s.state.Levels[2].Unlocked {
		t.Fatal("expected level 2 to be unlocked")
	}
}

func TestUnlockingDoesNotSkipAheadWhenAnotherLevelIsSelected(t *testing.T) {
	s := newTestCoachServer(t, testScenarioSet())

	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Levels[2].Unlocked = true
	s.state.Levels[1].MasteryCount = masteryTarget
	s.state.Levels[2].MasteryCount = masteryTarget
	s.state.SelectedLevel = 1

	if got := s.unlockNextLevelIfEligibleLocked(); got != 0 {
		t.Fatalf("expected no new unlock while level 1 is selected, got %d", got)
	}
	if s.state.Levels[3].Unlocked {
		t.Fatal("level 3 should still be locked")
	}

	s.state.SelectedLevel = 2
	if got := s.unlockNextLevelIfEligibleLocked(); got != 3 {
		t.Fatalf("expected level 3 to unlock when level 2 is selected and mastered, got %d", got)
	}
}

func TestEffectiveTraceSearchLimitClampsToJaegerMax(t *testing.T) {
	s := &coachServer{jaegerQueryMaxLimit: 14}

	if got := s.effectiveTraceSearchLimit(4, 30); got != 14 {
		t.Fatalf("expected limit 14 for requested 30, got %d", got)
	}
	if got := s.effectiveTraceSearchLimit(4, 0); got != 14 {
		t.Fatalf("expected default limit to clamp at 14, got %d", got)
	}
	if got := s.effectiveTraceSearchLimit(4, 3); got != 8 {
		t.Fatalf("expected need-driven expansion to 8, got %d", got)
	}
}

func TestEffectiveTraceSearchLimitHonorsHigherConfiguredMax(t *testing.T) {
	s := &coachServer{jaegerQueryMaxLimit: 50}

	if got := s.effectiveTraceSearchLimit(4, 30); got != 30 {
		t.Fatalf("expected configured max to allow 30, got %d", got)
	}
}

func TestFilterRecentTracesRequiresMatchingBatchID(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 1)
	want := traceFixture("wanted", def, nil)
	want.Start = time.Now()
	want.Spans[0].Tags[app.BatchAttribute] = "batch-2"

	skip := traceFixture("skip", def, nil)
	skip.Start = want.Start.Add(20 * time.Millisecond)
	skip.Spans[0].Tags[app.BatchAttribute] = "batch-1"

	got := filterRecentTraces([]traceRecord{skip, want}, want.Start.Add(-50*time.Millisecond), "batch-2")
	if len(got) != 1 || got[0].ID != "wanted" {
		t.Fatalf("expected only batch-2 trace, got %+v", got)
	}
}

func newTestCoachServer(t *testing.T, defs []scenario.Definition) *coachServer {
	t.Helper()

	levels, err := buildLevels(defs)
	if err != nil {
		t.Fatalf("buildLevels: %v", err)
	}

	return &coachServer{
		client:              http.DefaultClient,
		jaegerQueryMaxLimit: defaultJaegerQueryMaxLimit,
		webURL:              "http://example.test",
		scenarioSet:         defs,
		levels:              levels,
		state:               newLearnerSession(levels),
		subscribers:         map[int]chan coachSnapshot{},
	}
}

func firstScenarioByLevel(t *testing.T, defs []scenario.Definition, level int) scenario.Definition {
	t.Helper()
	for _, def := range defs {
		if def.Level == level {
			return def
		}
	}
	t.Fatalf("missing scenario for level %d", level)
	return scenario.Definition{}
}

func traceFixture(id string, def scenario.Definition, extraTags map[string]string) traceRecord {
	start := time.Unix(1700000000, 0)
	culpritTags := map[string]string{
		"db.system":       "postgresql",
		"lab.query_label": def.AnswerKey.SpanOperation,
	}
	for key, value := range extraTags {
		culpritTags[key] = value
	}

	apiOperation := strings.Replace(def.FocusOperation, "GET ", "GET /api", 1)
	if apiOperation == def.FocusOperation {
		apiOperation = "GET /api" + def.Route
	}

	return traceRecord{
		ID:         id,
		Start:      start,
		DurationMS: 900,
		Spans: []traceSpan{
			{ID: "root", Service: def.FocusService, Operation: def.FocusOperation, Start: start, DurationMS: 900, Tags: map[string]string{}},
			{ID: "edge", Service: "edge-api", Operation: apiOperation, Start: start.Add(10 * time.Millisecond), DurationMS: 820, Tags: map[string]string{}},
			{ID: "culprit", Service: def.ExpectedService, Operation: def.AnswerKey.SpanOperation, Start: start.Add(20 * time.Millisecond), DurationMS: 780, Tags: culpritTags},
		},
	}
}

func testScenarioSet() []scenario.Definition {
	type template struct {
		level          int
		assessmentType string
		route          string
		op             string
		service        string
		issue          string
		span           string
		attrKey        string
		attrValue      string
	}

	templates := []template{
		{level: 1, assessmentType: assessmentCulpritSpan, route: "/search", op: "GET /search", service: "catalog-api", issue: "expensive_search_query", span: "catalog.search.meilisearch"},
		{level: 2, assessmentType: assessmentHealthyFaulty, route: "/checkout", op: "GET /checkout", service: "inventory-api", issue: "n_plus_one_queries", span: "stock.reserve.n_plus_one"},
		{level: 3, assessmentType: assessmentBeforeAfter, route: "/account/orders", op: "GET /account/orders", service: "orders-api", issue: "expensive_sort", span: "orders.history.expensive_sort"},
		{level: 4, assessmentType: assessmentSpanAttribute, route: "/checkout", op: "GET /checkout", service: "payments-api", issue: "lock_wait_timeout", span: "payments.idempotency.lock_wait", attrKey: "lab.failure_mode", attrValue: "lock_wait_timeout"},
		{level: 5, assessmentType: assessmentIntermittent, route: "/search", op: "GET /search", service: "catalog-api", issue: "expensive_search_query", span: "catalog.search.meilisearch"},
	}

	defs := make([]scenario.Definition, 0, len(templates)*3)
	for _, item := range templates {
		for variant := 1; variant <= 3; variant++ {
			defs = append(defs, scenario.Definition{
				ID:               fmt.Sprintf("level-%d-variant-%d", item.level, variant),
				Level:            item.level,
				VariantGroup:     fmt.Sprintf("group-%d", item.level),
				AssessmentType:   item.assessmentType,
				AssessmentPrompt: "Provide the required evidence for this level.",
				Title:            fmt.Sprintf("Level %d variant %d", item.level, variant),
				Objective:        fmt.Sprintf("Practice level %d", item.level),
				Prompt:           "Inspect the trace and submit the required evidence.",
				Route:            item.route,
				TrafficPath:      fmt.Sprintf("%s?variant=%d", item.route, variant),
				FocusService:     "shop-web",
				FocusOperation:   item.op,
				ExpectedService:  item.service,
				ExpectedIssue:    item.issue,
				AnswerKey: scenario.AnswerKey{
					Service:        item.service,
					Issue:          item.issue,
					SpanOperation:  item.span,
					AttributeKey:   item.attrKey,
					AttributeValue: item.attrValue,
				},
				BatchPlan: testBatchPlan(item.assessmentType),
			})
		}
	}
	return defs
}

func testBatchPlan(assessmentType string) scenario.BatchPlan {
	switch assessmentType {
	case assessmentCulpritSpan:
		return scenario.BatchPlan{FaultyCount: 2, BackgroundCount: 1}
	case assessmentHealthyFaulty:
		return scenario.BatchPlan{FaultyCount: 2, HealthyCount: 1, BackgroundCount: 1}
	case assessmentBeforeAfter:
		return scenario.BatchPlan{BeforeCount: 1, AfterCount: 1, BackgroundCount: 1}
	case assessmentSpanAttribute:
		return scenario.BatchPlan{FaultyCount: 2, BackgroundCount: 1}
	case assessmentIntermittent:
		return scenario.BatchPlan{FaultyCount: 2, HealthyCount: 2, BackgroundCount: 1}
	default:
		return scenario.BatchPlan{}
	}
}
