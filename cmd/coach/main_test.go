package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestNewLearnerSessionStartsWithoutVisibleFeedback(t *testing.T) {
	levels, err := buildLevels(testScenarioSet())
	if err != nil {
		t.Fatalf("buildLevels: %v", err)
	}

	session := newLearnerSession(levels)
	if session.HasFeedback {
		t.Fatal("expected feedback to start hidden")
	}
	if session.Feedback != "" {
		t.Fatalf("expected no initial feedback message, got %q", session.Feedback)
	}
}

func TestBuildPreparedChallengeCreatesTraceSearchSpanAssessment(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 1)
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty: []traceRecord{
			traceFixture("trace-1", def, map[string]string{app.BatchAttribute: "batch-17"}),
		},
		Healthy: []traceRecord{
			traceFixture("trace-2", def, map[string]string{app.BatchAttribute: "batch-17"}),
		},
	}, func(id string) string { return "/trace/" + id }, func(service, operation string, limit int, tags map[string]string) string {
		return fmt.Sprintf("/search?service=%s&operation=%s&limit=%d&batch=%s", service, operation, limit, tags[app.BatchAttribute])
	}, func(string, string, []string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	if !challenge.Public.Ready {
		t.Fatal("expected challenge to be ready")
	}
	if challenge.Public.InvestigationLink == nil || challenge.Public.InvestigationLink.URL == "" {
		t.Fatalf("expected investigation link, got %+v", challenge.Public.InvestigationLink)
	}
	if !strings.Contains(challenge.Public.InvestigationLink.URL, "batch=batch-17") {
		t.Fatalf("expected batch-pinned investigation link, got %q", challenge.Public.InvestigationLink.URL)
	}
	if len(challenge.Public.TraceChoices) != 2 {
		t.Fatalf("expected two trace choices, got %+v", challenge.Public.TraceChoices)
	}
	if len(challenge.Public.TraceSpanChoices["trace-1"]) == 0 {
		t.Fatal("expected per-trace span choices")
	}
	if len(challenge.Public.TraceSpanChoices["trace-2"]) == 0 {
		t.Fatal("expected distractor trace span choices")
	}
	if len(challenge.ExpectedTraceIDs) != 1 || challenge.ExpectedTraceIDs[0] != "trace-1" {
		t.Fatalf("expected accepted trace trace-1, got %+v", challenge.ExpectedTraceIDs)
	}
	if challenge.ExpectedSpanID != spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation) {
		t.Fatalf("unexpected expected span id: %s", challenge.ExpectedSpanID)
	}
}

func TestBuildPreparedChallengeNormalizesTraceSearchSpanChoiceCounts(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 1)
	faulty := traceFixture("trace-faulty", def, nil)
	faulty.Spans = append(faulty.Spans,
		traceSpan{ID: "extra-a", Service: "inventory-api", Operation: "inventory.lookup.reserve_window", Start: faulty.Start.Add(30 * time.Millisecond), DurationMS: 210, Tags: map[string]string{}},
		traceSpan{ID: "extra-b", Service: "payments-api", Operation: "payments.authorize.prepare", Start: faulty.Start.Add(40 * time.Millisecond), DurationMS: 160, Tags: map[string]string{}},
	)
	healthy := traceFixture("trace-healthy", def, nil)

	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty:  []traceRecord{faulty},
		Healthy: []traceRecord{healthy},
	}, func(string) string { return "" }, func(string, string, int, map[string]string) string { return "" }, func(string, string, []string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	gotFaulty := challenge.Public.TraceSpanChoices["trace-faulty"]
	gotHealthy := challenge.Public.TraceSpanChoices["trace-healthy"]
	if len(gotFaulty) != len(gotHealthy) {
		t.Fatalf("expected matching span-choice counts, got faulty=%d healthy=%d", len(gotFaulty), len(gotHealthy))
	}
	if !containsSpanChoice(gotFaulty, challenge.ExpectedSpanID) {
		t.Fatalf("expected culprit span to remain available after truncation, got %+v", gotFaulty)
	}
}

func TestBuildPreparedChallengeMixedTraceOrderDoesNotDependOnTiming(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 2)
	base := time.Unix(1700000000, 0)

	healthyFirst := traceFixture("trace-healthy", def, nil)
	faultyAFirst := traceFixture("trace-faulty-a", def, nil)
	faultyBFirst := traceFixture("trace-faulty-b", def, nil)
	healthyFirst.Start = base.Add(3 * time.Second)
	faultyAFirst.Start = base.Add(2 * time.Second)
	faultyBFirst.Start = base.Add(1 * time.Second)

	challengeA, err := buildPreparedChallenge(def, traceGroups{
		Healthy: []traceRecord{healthyFirst},
		Faulty:  []traceRecord{faultyAFirst, faultyBFirst},
	}, func(id string) string { return "/trace/" + id }, func(string, string, int, map[string]string) string { return "" }, func(string, string, []string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge first timing set: %v", err)
	}

	healthyLast := traceFixture("trace-healthy", def, nil)
	faultyASecond := traceFixture("trace-faulty-a", def, nil)
	faultyBSecond := traceFixture("trace-faulty-b", def, nil)
	healthyLast.Start = base.Add(1 * time.Second)
	faultyASecond.Start = base.Add(3 * time.Second)
	faultyBSecond.Start = base.Add(2 * time.Second)

	challengeB, err := buildPreparedChallenge(def, traceGroups{
		Healthy: []traceRecord{healthyLast},
		Faulty:  []traceRecord{faultyASecond, faultyBSecond},
	}, func(id string) string { return "/trace/" + id }, func(string, string, int, map[string]string) string { return "" }, func(string, string, []string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge second timing set: %v", err)
	}

	gotA := publicTraceChoiceIDs(challengeA.Public.TraceChoices)
	gotB := publicTraceChoiceIDs(challengeB.Public.TraceChoices)
	if !sameOrderedStrings(gotA, gotB) {
		t.Fatalf("expected stable mixed trace order independent of timing, got %v vs %v", gotA, gotB)
	}
	if !sameStringSet(gotA, []string{"trace-healthy", "trace-faulty-a", "trace-faulty-b"}) {
		t.Fatalf("expected the same mixed trace ids, got %v", gotA)
	}
}

func TestGradeSubmissionRequiresTraceAndSpanEvidence(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 1)
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty: []traceRecord{
			traceFixture("trace-1", def, nil),
		},
		Healthy: []traceRecord{
			traceFixture("trace-healthy", def, nil),
		},
	}, func(string) string { return "" }, func(string, string, int, map[string]string) string { return "" }, func(string, string, []string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	result := gradeSubmission(def, challenge, gradeRequest{
		SelectedTraceID: "trace-1",
		SelectedSpan:    spanChoiceID("edge-api", "GET /api/search"),
	})
	if result.Pass {
		t.Fatal("expected wrong span to fail")
	}

	result = gradeSubmission(def, challenge, gradeRequest{
		SelectedTraceID: "trace-healthy",
		SelectedSpan:    challenge.ExpectedSpanID,
	})
	if result.Pass {
		t.Fatal("expected wrong trace to fail")
	}

	result = gradeSubmission(def, challenge, gradeRequest{
		SelectedTraceID: "trace-1",
		SelectedSpan:    challenge.ExpectedSpanID,
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

func TestBuildPreparedChallengeCreatesBeforeAfterCompareAssessment(t *testing.T) {
	def := firstScenarioByLevel(t, testScenarioSet(), 3)
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Before: []traceRecord{traceFixture("before-1", def, nil)},
		After:  []traceRecord{traceFixture("after-1", def, nil)},
	}, func(id string) string { return "/trace/" + id }, func(string, string, int, map[string]string) string { return "" }, func(beforeID, afterID string, cohort []string) string {
		return "/trace/" + beforeID + "..." + afterID + "?cohort=" + strings.Join(cohort, ",")
	})
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	if len(challenge.Public.TraceChoices) != 2 {
		t.Fatalf("expected merged trace choices, got %+v", challenge.Public.TraceChoices)
	}
	if len(challenge.Public.BeforeTraceChoices) != 1 {
		t.Fatalf("expected one before trace choice, got %+v", challenge.Public.BeforeTraceChoices)
	}
	if len(challenge.Public.AfterTraceChoices) != 1 {
		t.Fatalf("expected one after trace choice, got %+v", challenge.Public.AfterTraceChoices)
	}
	if challenge.Public.CompareLink == nil || !strings.Contains(challenge.Public.CompareLink.URL, "/trace/before-1...after-1") {
		t.Fatalf("expected compare link with before/after pair, got %+v", challenge.Public.CompareLink)
	}
	if !strings.Contains(challenge.Public.CompareLink.URL, "cohort=before-1,after-1") {
		t.Fatalf("expected compare cohort in link, got %+v", challenge.Public.CompareLink)
	}
	if !containsID(challenge.ExpectedBeforeTraceIDs, "before-1") {
		t.Fatalf("expected before trace id to be preserved, got %+v", challenge.ExpectedBeforeTraceIDs)
	}
	if !containsID(challenge.ExpectedAfterTraceIDs, "after-1") {
		t.Fatalf("expected after trace id to be preserved, got %+v", challenge.ExpectedAfterTraceIDs)
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
	}, func(string) string { return "" }, func(string, string, int, map[string]string) string { return "" }, func(string, string, []string) string { return "" })
	if err != nil {
		t.Fatalf("buildPreparedChallenge: %v", err)
	}

	result := gradeSubmission(def, challenge, gradeRequest{
		SuspectedService:  def.ExpectedService,
		SuspectedIssue:    def.ExpectedIssue,
		SelectedSpan:      challenge.ExpectedSpanID,
		SelectedAttribute: "lab.wait_checkpoint=wrong.value",
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

func TestSnapshotMarksEveryLevelSelectable(t *testing.T) {
	s := newTestCoachServer(t, testScenarioSet())

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := s.snapshotLocked()
	for _, level := range snapshot.Levels {
		if !level.Unlocked {
			t.Fatalf("expected level %d to be selectable", level.Number)
		}
	}
}

func TestSelectLevelAllowsAnyLevelWithoutPriorCorrectCount(t *testing.T) {
	s := newTestCoachServer(t, testScenarioSet())

	s.mu.Lock()
	s.state.Levels[5].Current = s.levels[4].Scenarios[0]
	s.state.Levels[5].Prepared = true
	s.mu.Unlock()

	reqBody, err := json.Marshal(selectLevelRequest{Level: 5})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/levels/select", strings.NewReader(string(reqBody)))
	s.selectLevel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected selecting level 5 to succeed, got status %d body %q", rec.Code, rec.Body.String())
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.SelectedLevel != 5 {
		t.Fatalf("expected selected level 5, got %d", s.state.SelectedLevel)
	}
}

func TestStateSnapshotBootstrapsPreparedChallenge(t *testing.T) {
	defs := testScenarioSet()
	s := newTestCoachServer(t, defs)
	s.jaegerUIURL = "http://jaeger.example"

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer web.Close()

	s.client = web.Client()
	s.webURL = web.URL

	selected := firstScenarioByLevel(t, defs, 1)
	traceCalls := 0
	s.findRecentTraces = func(_ context.Context, _ string, _ string, since time.Time, need, _ int, batchID string) ([]traceRecord, error) {
		traceCalls++

		traces := make([]traceRecord, 0, need)
		for i := 0; i < need; i++ {
			tags := map[string]string{
				app.BatchAttribute: batchID,
			}
			if i == traceSearchSpanFaultyOffset(batchID, need) {
				tags[app.ScenarioAttribute] = selected.ID
			}
			trace := traceFixture(fmt.Sprintf("%s-%d", batchID, i), selected, tags)
			trace.Start = since.Add(time.Duration(i+1) * time.Millisecond)
			traces = append(traces, trace)
		}
		return traces, nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	s.stateSnapshot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected state snapshot to succeed, got %d body %q", rec.Code, rec.Body.String())
	}
	if traceCalls == 0 {
		t.Fatal("expected state snapshot to prepare the selected challenge")
	}

	var snapshot coachSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snapshot.CurrentScenario.Assessment.Ready {
		t.Fatalf("expected prepared assessment in snapshot, got %+v", snapshot.CurrentScenario.Assessment)
	}
	if snapshot.JaegerUIURL != "http://jaeger.example" {
		t.Fatalf("expected jaeger URL in snapshot, got %q", snapshot.JaegerUIURL)
	}
}

func TestRoutesServeCoachUIAssets(t *testing.T) {
	s := newTestCoachServer(t, testScenarioSet())
	handler := s.routes()

	cases := []struct {
		path        string
		contentType string
		wantBody    string
	}{
		{path: "/", contentType: "text/html", wantBody: "<script type=\"module\" src=\"/app.js\"></script>"},
		{path: "/app.css", contentType: "text/css", wantBody: ".progression-panel"},
		{path: "/app.js", contentType: "text/javascript", wantBody: "requestSnapshot(\"/api/state\")"},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d body %q", tc.path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tc.contentType) {
			t.Fatalf("%s: expected content type containing %q, got %q", tc.path, tc.contentType, got)
		}
		if !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Fatalf("%s: expected body to contain %q", tc.path, tc.wantBody)
		}
	}
}

func TestPickRandomForLevelDifferentVariantPrefersOtherVariantGroup(t *testing.T) {
	s := &coachServer{
		levels: []levelDefinition{
			{
				Number: 1,
				Scenarios: []scenario.Definition{
					{ID: "same-a", VariantGroup: "group-a"},
					{ID: "same-b", VariantGroup: "group-a"},
					{ID: "other", VariantGroup: "group-b"},
				},
			},
		},
	}

	got := s.pickRandomForLevelDifferentVariant(1, "same-a", "group-a")
	if got.ID != "other" {
		t.Fatalf("expected different variant group to be preferred, got %+v", got)
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

func TestSearchURLIncludesBatchTags(t *testing.T) {
	s := &coachServer{jaegerUIURL: "http://jaeger.example"}

	got := s.searchURL("shop-web", "GET /search", 12, map[string]string{
		app.BatchAttribute: "batch-42",
	})

	if !strings.Contains(got, "service=shop-web") {
		t.Fatalf("expected service in search URL, got %q", got)
	}
	if !strings.Contains(got, "operation=GET+%2Fsearch") {
		t.Fatalf("expected operation in search URL, got %q", got)
	}
	if !strings.Contains(got, "limit=12") {
		t.Fatalf("expected limit in search URL, got %q", got)
	}
	if !strings.Contains(got, "%22lab.batch_id%22%3A%22batch-42%22") {
		t.Fatalf("expected encoded batch tag in search URL, got %q", got)
	}
}

func TestCompareURLIncludesPreparedPairAndCohort(t *testing.T) {
	s := &coachServer{jaegerUIURL: "http://jaeger.example"}

	got := s.compareURL("before-1", "after-1", []string{"before-1", "before-2", "after-1", "after-2", "before-1"})
	if !strings.Contains(got, "/trace/before-1...after-1") {
		t.Fatalf("expected compare route in URL, got %q", got)
	}
	if strings.Count(got, "cohort=") != 4 {
		t.Fatalf("expected four cohort entries, got %q", got)
	}
	for _, expected := range []string{"cohort=before-1", "cohort=before-2", "cohort=after-1", "cohort=after-2"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in compare URL, got %q", expected, got)
		}
	}
}

func TestTraceDisplayIDUsesJaegerPrefix(t *testing.T) {
	if got := traceDisplayID("9aaa72201234567876ea06a4"); got != "9aaa722" {
		t.Fatalf("expected Jaeger-aligned trace display, got %q", got)
	}
	if got := traceDisplayID("9aaa72"); got != "9aaa72" {
		t.Fatalf("expected shorter trace ids to remain intact, got %q", got)
	}
}

func TestTraceSearchSpanScenarioOrderRotatesFaultyPosition(t *testing.T) {
	batches := []string{"batch-4", "batch-8", "batch-12", "batch-16"}
	seenPositions := map[int]struct{}{}

	for _, batchID := range batches {
		order := traceSearchSpanScenarioOrder("scenario-a", batchID, 1, 3)
		if len(order) != 4 {
			t.Fatalf("expected four generated requests for %s, got %d", batchID, len(order))
		}
		if countScenarioID(order, "scenario-a") != 1 {
			t.Fatalf("expected one faulty slot for %s, got %v", batchID, order)
		}

		position := strings.Index(strings.Join(order, ","), "scenario-a")
		if order[0] == "scenario-a" {
			t.Fatalf("expected faulty trace to avoid the oldest slot for %s, got %v", batchID, order)
		}
		for i, value := range order {
			if value == "scenario-a" {
				seenPositions[i] = struct{}{}
			}
		}
		if position < 0 {
			t.Fatalf("expected faulty slot for %s, got %v", batchID, order)
		}
	}

	if len(seenPositions) < 2 {
		t.Fatalf("expected faulty slot to vary across batches, got positions %+v", seenPositions)
	}
}

func TestGradeRotatesChallengeAfterSecondIncorrectSubmission(t *testing.T) {
	s := newTestCoachServer(t, testScenarioSet())
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer web.Close()

	s.client = web.Client()
	s.webURL = web.URL
	s.findRecentTraces = func(_ context.Context, _ string, _ string, since time.Time, need, _ int, batchID string) ([]traceRecord, error) {
		s.mu.RLock()
		def := s.state.Levels[s.state.SelectedLevel].Current
		s.mu.RUnlock()

		traces := make([]traceRecord, 0, need)
		for i := 0; i < need; i++ {
			tags := map[string]string{
				app.BatchAttribute: batchID,
			}
			if i == traceSearchSpanFaultyOffset(batchID, need) {
				tags[app.ScenarioAttribute] = def.ID
			}
			trace := traceFixture(fmt.Sprintf("%s-%d", batchID, i), def, tags)
			trace.Start = since.Add(time.Duration(i+1) * time.Millisecond)
			traces = append(traces, trace)
		}
		return traces, nil
	}

	s.mu.Lock()
	def := s.state.Levels[1].Current
	challenge, err := buildPreparedChallenge(def, traceGroups{
		Faulty: []traceRecord{traceFixture("trace-1", def, map[string]string{app.BatchAttribute: "batch-initial"})},
	}, func(string) string { return "" }, func(string, string, int, map[string]string) string { return "" }, func(string, string, []string) string { return "" })
	if err != nil {
		s.mu.Unlock()
		t.Fatalf("buildPreparedChallenge: %v", err)
	}
	s.state.Levels[1].Prepared = true
	s.state.Levels[1].Challenge = challenge
	s.mu.Unlock()

	reqBody, err := json.Marshal(gradeRequest{
		ScenarioID:      def.ID,
		SelectedTraceID: "trace-1",
		SelectedSpan:    "wrong|span",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/grade", strings.NewReader(string(reqBody)))
	s.grade(rec, req)

	s.mu.RLock()
	if got := s.state.Levels[1].IncorrectAttempts; got != 1 {
		s.mu.RUnlock()
		t.Fatalf("expected first wrong submission to record one incorrect attempt, got %d", got)
	}
	if got := s.state.Levels[1].Current.ID; got != def.ID {
		s.mu.RUnlock()
		t.Fatalf("expected challenge to stay on first wrong submission, got %q", got)
	}
	if !strings.Contains(s.state.Feedback, "1 attempt remain") {
		s.mu.RUnlock()
		t.Fatalf("expected first wrong feedback to mention one attempt remaining, got %q", s.state.Feedback)
	}
	s.mu.RUnlock()

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/grade", strings.NewReader(string(reqBody)))
	s.grade(rec, req)

	s.mu.RLock()
	defer s.mu.RUnlock()
	if got := s.state.Levels[1].IncorrectAttempts; got != 0 {
		t.Fatalf("expected incorrect attempts to reset after rotation, got %d", got)
	}
	if got := s.state.Levels[1].Current.ID; got == def.ID {
		t.Fatalf("expected a new challenge after the second wrong submission, still on %q", got)
	}
	if !s.state.Levels[1].Prepared || s.state.Levels[1].Challenge == nil {
		t.Fatalf("expected the new challenge to be prepared, got prepared=%t challenge=%+v", s.state.Levels[1].Prepared, s.state.Levels[1].Challenge)
	}
	if s.state.HasFeedback || s.state.Feedback != "" {
		t.Fatalf("expected rotation to clear feedback after second wrong submission, got has_feedback=%t feedback=%q", s.state.HasFeedback, s.state.Feedback)
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

func publicTraceChoiceIDs(options []publicTraceOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	return ids
}

func sameOrderedStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
		{level: 1, assessmentType: assessmentTraceSearchSpan, route: "/search", op: "GET /search", service: "catalog-api", issue: "expensive_search_query", span: "catalog.search.fetch_results"},
		{level: 2, assessmentType: assessmentHealthyFaulty, route: "/checkout", op: "GET /checkout", service: "inventory-api", issue: "n_plus_one_queries", span: "inventory.reserve.check_stock"},
		{level: 3, assessmentType: assessmentBeforeAfter, route: "/account/orders", op: "GET /account/orders", service: "orders-api", issue: "expensive_sort", span: "orders.history.load_page"},
		{level: 4, assessmentType: assessmentSpanAttribute, route: "/checkout", op: "GET /checkout", service: "payments-api", issue: "lock_wait_timeout", span: "payments.idempotency.ensure_guard", attrKey: "lab.wait_checkpoint", attrValue: "payments.idempotency.guard"},
		{level: 5, assessmentType: assessmentIntermittent, route: "/search", op: "GET /search", service: "catalog-api", issue: "expensive_search_query", span: "catalog.search.fetch_results"},
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
	case assessmentTraceSearchSpan:
		return scenario.BatchPlan{FaultyCount: 1, HealthyCount: 1, BackgroundCount: 1, CandidateCount: 2}
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

func countScenarioID(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
