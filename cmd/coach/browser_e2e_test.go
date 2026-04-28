//go:build e2e

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"

	"github.com/chromedp/chromedp"
)

const skillPlacementStorageKey = "cloudtracing.skillPlacement.v1"

func TestCoachBrowserAssessmentModes(t *testing.T) {
	t.Run("level_1_trace_search_span", func(t *testing.T) {
		h := newCoachE2EHarness(t, coachSessionSetup{SelectedLevel: 1})
		tab, closeTab := newBrowserRoot(t)
		defer closeTab()

		navigateCoach(t, tab, h.coach.URL)
		waitForLevelUI(t, tab, assessmentTraceSearchSpan)
		waitForReferenceTraceLink(t, tab)
		assertAssessmentContract(t, tab)
		waitForCondition(t, tab, `document.getElementById("title").textContent.trim() === "Find the slow trace and span"`)
		waitForCondition(t, tab, `document.getElementById("objective").textContent.trim() === ""`)
		waitForCondition(t, tab, `document.getElementById("open-jaeger").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("assessment-prompt").classList.contains("hidden")`)

		answer := h.waitForSelectedAnswer(t)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "Select the slow trace you inspected before submitting.")

		setSelectValue(t, tab, "#selected-trace", answer.SelectedTraceID, false)
		waitForCondition(t, tab, `document.querySelector('label[for="selected-span"]').textContent.trim() === "Slow span"`)
		setSelectValue(t, tab, "#selected-span", wrongSelectOptionValue(t, tab, "#selected-span", answer.SpanID), false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "slow span is wrong")

		if answer.InvalidTraceID == "" {
			t.Fatal("expected a distractor trace for level 1")
		}
		setSelectValue(t, tab, "#selected-trace", answer.InvalidTraceID, false)
		setSelectValue(t, tab, "#selected-span", answer.SpanID, false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "trace used is wrong")
	})

	t.Run("level_2_healthy_faulty", func(t *testing.T) {
		h := newCoachE2EHarness(t, coachSessionSetup{SelectedLevel: 2, CorrectCounts: map[int]int{1: correctTarget}})
		tab, closeTab := newBrowserRoot(t)
		defer closeTab()

		navigateCoach(t, tab, h.coach.URL)
		waitForLevelUI(t, tab, assessmentHealthyFaulty)
		assertAssessmentContract(t, tab)
		waitForCondition(t, tab, `document.getElementById("title").textContent.trim() === "Classify the traces as slow or healthy, then name the responsible service and failure mode."`)
		waitForCondition(t, tab, `document.getElementById("objective").textContent.trim() === ""`)
		waitForCondition(t, tab, `document.querySelector('#levels .level-button:nth-child(1) .level-complete') !== null`)
		waitForCondition(t, tab, `!document.getElementById("levels").textContent.includes("OPEN") && !document.getElementById("levels").textContent.includes("READY")`)
		waitForCondition(t, tab, `document.getElementById("open-jaeger").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("reference-trace").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("assessment-prompt").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("issue-help").textContent.includes("load_page")`)
		waitForCondition(t, tab, `document.querySelector('#issue option[value="expensive_sort"]').textContent.includes("load_page")`)

		answer := h.waitForSelectedAnswer(t)
		setSelectValue(t, tab, "#service", answer.Service, false)
		setSelectValue(t, tab, "#issue", answer.Issue, false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "Classify every trace before submitting.")

		setTraceRole(t, tab, answer.FaultyTraceIDs[0], "slow")
		setTraceRole(t, tab, answer.FaultyTraceIDs[1], "healthy")
		setTraceRole(t, tab, answer.HealthyTraceID, "slow")
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "slow trace and healthy trace selections are wrong")
	})

	t.Run("level_3_before_after", func(t *testing.T) {
		h := newCoachE2EHarness(t, coachSessionSetup{
			SelectedLevel: 3,
			CorrectCounts: map[int]int{1: correctTarget, 2: correctTarget},
		})
		tab, closeTab := newBrowserRoot(t)
		defer closeTab()

		navigateCoach(t, tab, h.coach.URL)
		waitForLevelUI(t, tab, assessmentBeforeAfter)
		assertAssessmentContract(t, tab)
		waitForCondition(t, tab, `(() => {
			const link = document.querySelector("#reference-trace a");
			return !!link && link.textContent.trim() === "Compare traces" && link.href.includes("/trace/") && link.href.includes("...");
		})()`)
		waitForCondition(t, tab, `document.getElementById("open-jaeger").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("assessment-prompt").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("selected-level-title").textContent.includes("Respond to an Elevated Latency Alert")`)
		waitForCondition(t, tab, `document.getElementById("title").textContent.toLowerCase().includes("elevated")`)
		waitForCondition(t, tab, `document.getElementById("before-trace") === null && document.getElementById("after-trace") === null`)

		answer := h.waitForSelectedAnswer(t)
		setSelectValue(t, tab, "#service", answer.Service, false)
		setSelectValue(t, tab, "#issue", wrongSelectOptionValue(t, tab, "#issue", answer.Issue), false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "failure mode is wrong")
		setSelectValue(t, tab, "#issue", answer.Issue, false)
		click(t, tab, "#submit")
		waitForProgress(t, tab, "1/5 correct")
	})

	t.Run("level_4_span_attribute", func(t *testing.T) {
		h := newCoachE2EHarness(t, coachSessionSetup{
			SelectedLevel: 4,
			CorrectCounts: map[int]int{1: correctTarget, 2: correctTarget, 3: correctTarget},
		})
		tab, closeTab := newBrowserRoot(t)
		defer closeTab()

		navigateCoach(t, tab, h.coach.URL)
		waitForLevelUI(t, tab, assessmentSpanAttribute)
		waitForReferenceTraceLink(t, tab)
		assertAssessmentContract(t, tab)

		answer := h.waitForSelectedAnswer(t)
		setSelectValue(t, tab, "#service", answer.Service, false)
		setSelectValue(t, tab, "#issue", answer.Issue, false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "Select the culprit span before submitting.")

		setSelectValue(t, tab, "#selected-span", answer.SpanID, false)
		waitForCondition(t, tab, `document.querySelector('label[for="selected-attribute"]').textContent.trim() === "Proof tag on culprit span"`)
		setSelectValue(t, tab, "#selected-attribute", wrongSelectOptionValue(t, tab, "#selected-attribute", answer.AttributeID), false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "proof tag is wrong")
	})

	t.Run("level_5_intermittent_failure", func(t *testing.T) {
		h := newCoachE2EHarness(t, coachSessionSetup{
			SelectedLevel: 5,
			CorrectCounts: map[int]int{1: correctTarget, 2: correctTarget, 3: correctTarget, 4: correctTarget},
		})
		tab, closeTab := newBrowserRoot(t)
		defer closeTab()

		navigateCoach(t, tab, h.coach.URL)
		waitForLevelUI(t, tab, assessmentIntermittent)
		assertAssessmentContract(t, tab)
		waitForCondition(t, tab, `document.getElementById("objective").textContent.trim() === ""`)
		waitForCondition(t, tab, `document.getElementById("assessment-prompt").classList.contains("hidden")`)
		waitForCondition(t, tab, `document.getElementById("reference-trace").textContent.includes("to the right")`)

		answer := h.waitForSelectedAnswer(t)
		setSelectValue(t, tab, "#service", answer.Service, false)
		setSelectValue(t, tab, "#issue", answer.Issue, false)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "Select every failing trace before submitting.")

		setChecked(t, tab, "failing-trace", answer.FailingTraceIDs[0], true)
		click(t, tab, "#submit")
		waitForFeedbackContains(t, tab, "failing traces selection is wrong")
	})
}

func TestCoachBrowserSkillPlacementModal(t *testing.T) {
	h := newCoachE2EHarness(t, coachSessionSetup{})
	tab, closeTab := newBrowserRoot(t)
	defer closeTab()

	runChromedp(t, tab,
		chromedp.Navigate(h.coach.URL),
		chromedp.WaitVisible("#title", chromedp.ByID),
	)
	waitForCondition(t, tab, `document.getElementById("title").textContent.trim().length > 0`)
	waitForCondition(t, tab, `document.getElementById("busy-overlay").classList.contains("hidden")`)
	waitForCondition(t, tab, `!document.getElementById("skill-modal").classList.contains("hidden")`)
	waitForCondition(t, tab, `document.getElementById("skill-modal-title").textContent.includes("distributed tracing")`)

	click(t, tab, `[data-skill-choice="familiar"]`)
	waitForSelectedLevel(t, tab, 3)
	waitForCondition(t, tab, `!document.getElementById("skill-step-explainer").classList.contains("hidden")`)
	waitForCondition(t, tab, `document.getElementById("skill-step-explainer").textContent.includes("Move between levels any time")`)

	click(t, tab, "#skill-modal-close")
	waitForCondition(t, tab, `document.getElementById("skill-modal").classList.contains("hidden")`)

	reloadPage(t, tab)
	waitForSelectedLevel(t, tab, 3)
	waitForCondition(t, tab, `document.getElementById("skill-modal").classList.contains("hidden")`)
}

func TestCoachBrowserSharedStateAndProgression(t *testing.T) {
	h := newCoachE2EHarness(t, coachSessionSetup{})

	tabA, closeA := newBrowserRoot(t)
	defer closeA()
	tabB, closeB := newBrowserRoot(t)
	defer closeB()
	tabC, closeCtab := newBrowserRoot(t)
	defer closeCtab()

	navigateCoach(t, tabA, h.coach.URL)
	navigateCoach(t, tabB, h.coach.URL)
	navigateCoach(t, tabC, h.coach.URL)

	waitForLevelUI(t, tabA, assessmentTraceSearchSpan)
	waitForLevelUI(t, tabB, assessmentTraceSearchSpan)
	waitForLevelUI(t, tabC, assessmentTraceSearchSpan)
	waitForCondition(t, tabA, levelUnlockedExpression(2, true))
	waitForCondition(t, tabA, levelUnlockedExpression(5, true))

	for attempt := 1; attempt <= correctTarget; attempt++ {
		titleBefore := textContent(t, tabA, "#title")
		solveSelectedLevel(t, h, tabA)
		waitForCondition(t, tabA, levelUnlockedExpression(2, true))
		waitForProgress(t, tabA, fmt.Sprintf("%d/%d correct", attempt, correctTarget))
		if attempt < correctTarget {
			waitForCondition(t, tabA, fmt.Sprintf(`document.getElementById("title").textContent.trim() !== %q`, titleBefore))
			waitForCondition(t, tabB, fmt.Sprintf(`document.getElementById("selected-level-progress").textContent.trim() === %q`, fmt.Sprintf("%d/%d correct", attempt, correctTarget)))
			waitForCondition(t, tabC, fmt.Sprintf(`document.getElementById("selected-level-progress").textContent.trim() === %q`, fmt.Sprintf("%d/%d correct", attempt, correctTarget)))
		}
	}
	waitForLevelReadyModal(t, tabA, 1)
	closeLevelReadyModalIfVisible(t, tabA)
	closeLevelReadyModalIfVisible(t, tabB)
	closeLevelReadyModalIfVisible(t, tabC)

	waitForCondition(t, tabA, levelUnlockedExpression(2, true))
	waitForCondition(t, tabA, levelUnlockedExpression(3, true))

	click(t, tabA, "#levels .level-button:nth-child(2)")
	waitForSelectedLevel(t, tabA, 2)
	waitForSelectedLevel(t, tabB, 2)
	waitForSelectedLevel(t, tabC, 2)
	waitForLevelUI(t, tabB, assessmentHealthyFaulty)
	waitForLevelUI(t, tabC, assessmentHealthyFaulty)

	solveSelectedLevel(t, h, tabB)
	waitForProgress(t, tabA, "1/5 correct")
	waitForProgress(t, tabC, "1/5 correct")

	feedbackA := textContent(t, tabA, "#feedback")
	feedbackC := textContent(t, tabC, "#feedback")
	if feedbackA == "" || feedbackA != feedbackC {
		t.Fatalf("expected shared feedback across tabs, got A=%q C=%q", feedbackA, feedbackC)
	}

	titleBefore := textContent(t, tabA, "#title")
	answer := h.waitForSelectedAnswer(t)
	setSelectValue(t, tabA, "#service", answer.Service, false)
	setSelectValue(t, tabA, "#issue", answer.Issue, false)
	setTraceRole(t, tabA, answer.FaultyTraceIDs[0], "slow")
	click(t, tabA, "#next-challenge")

	waitForCondition(t, tabA, fmt.Sprintf(`document.getElementById("title").textContent.trim() !== %q`, titleBefore))
	waitForCondition(t, tabB, fmt.Sprintf(`document.getElementById("title").textContent.trim() === %q`, textContent(t, tabA, "#title")))
	waitForCondition(t, tabC, fmt.Sprintf(`document.getElementById("title").textContent.trim() === %q`, textContent(t, tabA, "#title")))
	waitForInputValue(t, tabA, "#service", "")
	waitForInputValue(t, tabA, "#issue", "")
	waitForCondition(t, tabA, `document.querySelectorAll('select[data-trace-role]').length === 0 || [...document.querySelectorAll('select[data-trace-role]')].every((select) => !select.value)`)
	waitForCondition(t, tabB, `document.querySelectorAll('select[data-trace-role]').length === 0 || [...document.querySelectorAll('select[data-trace-role]')].every((select) => !select.value)`)
	waitForCondition(t, tabC, `document.querySelectorAll('select[data-trace-role]').length === 0 || [...document.querySelectorAll('select[data-trace-role]')].every((select) => !select.value)`)
	waitForFeedbackHidden(t, tabA)
	waitForFeedbackHidden(t, tabB)
	waitForFeedbackHidden(t, tabC)
}

func TestCoachBrowserLevelReadyModalShowsOncePerLevel(t *testing.T) {
	h := newCoachE2EHarness(t, coachSessionSetup{})
	tab, closeTab := newBrowserRoot(t)
	defer closeTab()

	navigateCoach(t, tab, h.coach.URL)
	waitForLevelUI(t, tab, assessmentTraceSearchSpan)

	for attempt := 1; attempt <= correctTarget; attempt++ {
		solveSelectedLevel(t, h, tab)
		waitForProgress(t, tab, fmt.Sprintf("%d/%d correct", attempt, correctTarget))
	}

	waitForLevelReadyModal(t, tab, 1)
	closeLevelReadyModalIfVisible(t, tab)
	reloadPage(t, tab)
	waitForCondition(t, tab, `document.getElementById("level-ready-modal").classList.contains("hidden")`)
}

func TestCoachBrowserLevelReadyModalCanAdvanceToNextLevel(t *testing.T) {
	h := newCoachE2EHarness(t, coachSessionSetup{})
	tab, closeTab := newBrowserRoot(t)
	defer closeTab()

	navigateCoach(t, tab, h.coach.URL)
	waitForLevelUI(t, tab, assessmentTraceSearchSpan)

	for attempt := 1; attempt <= correctTarget; attempt++ {
		solveSelectedLevel(t, h, tab)
		waitForProgress(t, tab, fmt.Sprintf("%d/%d correct", attempt, correctTarget))
	}

	waitForLevelReadyModal(t, tab, 1)
	click(t, tab, "#level-ready-next")
	waitForSelectedLevel(t, tab, 2)
	waitForLevelUI(t, tab, assessmentHealthyFaulty)
}

func TestCoachBrowserFinalLevelReadyModalOmitsNextLevel(t *testing.T) {
	h := newCoachE2EHarness(t, coachSessionSetup{
		SelectedLevel: 5,
		CorrectCounts: map[int]int{1: correctTarget, 2: correctTarget, 3: correctTarget, 4: correctTarget},
	})
	tab, closeTab := newBrowserRoot(t)
	defer closeTab()

	navigateCoach(t, tab, h.coach.URL)
	waitForLevelUI(t, tab, assessmentIntermittent)

	for attempt := 1; attempt <= correctTarget; attempt++ {
		solveSelectedLevel(t, h, tab)
		waitForProgress(t, tab, fmt.Sprintf("%d/%d correct", attempt, correctTarget))
	}

	waitForLevelReadyModal(t, tab, 5)
}

func TestCoachBrowserRestartReset(t *testing.T) {
	h := newCoachE2EHarness(t, coachSessionSetup{
		SelectedLevel: 2,
		CorrectCounts: map[int]int{1: correctTarget, 2: 2},
	})

	tabA, closeA := newBrowserRoot(t)
	defer closeA()
	tabB, closeB := newBrowserRoot(t)
	defer closeB()
	tabC, closeCtab := newBrowserRoot(t)
	defer closeCtab()

	navigateCoach(t, tabA, h.coach.URL)
	navigateCoach(t, tabB, h.coach.URL)
	navigateCoach(t, tabC, h.coach.URL)

	waitForSelectedLevel(t, tabA, 2)
	waitForSelectedLevel(t, tabB, 2)
	waitForSelectedLevel(t, tabC, 2)
	waitForProgress(t, tabA, "2/5 correct")

	h.restart(t, coachSessionSetup{})
	reloadPage(t, tabA)
	reloadPage(t, tabB)
	reloadPage(t, tabC)

	waitForSelectedLevel(t, tabA, 1)
	waitForSelectedLevel(t, tabB, 1)
	waitForSelectedLevel(t, tabC, 1)
	waitForProgress(t, tabA, "0/5 correct")
	waitForCondition(t, tabA, levelUnlockedExpression(2, true))

	solveSelectedLevel(t, h, tabA)
	waitForProgress(t, tabB, "1/5 correct")
	waitForProgress(t, tabC, "1/5 correct")
}

type coachSessionSetup struct {
	SelectedLevel int
	CorrectCounts map[int]int
}

type seededTrafficRequest struct {
	At         time.Time
	Route      string
	ScenarioID string
	BatchID    string
	Seq        int
}

type expectedAnswer struct {
	Type            string
	Service         string
	Issue           string
	SelectedTraceID string
	InvalidTraceID  string
	SpanID          string
	AttributeID     string
	FaultyTraceIDs  []string
	HealthyTraceID  string
	BeforeTraceID   string
	AfterTraceID    string
	FailingTraceIDs []string
}

type coachE2EHarness struct {
	defs            []scenario.Definition
	defsByID        map[string]scenario.Definition
	defaultsByRoute map[string]scenario.Definition
	web             *httptest.Server
	coach           *httptest.Server
	swap            *swapHandler

	mu         sync.Mutex
	current    *coachServer
	requests   []seededTrafficRequest
	requestSeq int
}

type swapHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func newCoachE2EHarness(t *testing.T, setup coachSessionSetup) *coachE2EHarness {
	t.Helper()

	defs := testScenarioSet()
	h := &coachE2EHarness{
		defs:            defs,
		defsByID:        scenario.Index(defs),
		defaultsByRoute: defaultDefinitionsByRoute(defs),
	}
	h.web = httptest.NewServer(http.HandlerFunc(h.recordTraffic))
	h.swap = newSwapHandler(http.NotFoundHandler())
	h.coach = httptest.NewServer(h.swap)
	t.Cleanup(func() {
		h.coach.Close()
		h.web.Close()
	})

	h.restart(t, setup)
	return h
}

func newSwapHandler(initial http.Handler) *swapHandler {
	h := &swapHandler{}
	h.Store(initial)
	return h
}

func (h *swapHandler) Store(handler http.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handler = handler
}

func (h *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	handler := h.handler
	h.mu.RUnlock()
	handler.ServeHTTP(w, r)
}

func defaultDefinitionsByRoute(defs []scenario.Definition) map[string]scenario.Definition {
	byRoute := make(map[string]scenario.Definition)
	for _, def := range defs {
		current, ok := byRoute[def.Route]
		if !ok || def.Level < current.Level {
			byRoute[def.Route] = def
		}
	}
	return byRoute
}

func (h *coachE2EHarness) restart(t *testing.T, setup coachSessionSetup) {
	t.Helper()

	if setup.SelectedLevel <= 0 {
		setup.SelectedLevel = 1
	}

	levels, err := buildLevels(h.defs)
	if err != nil {
		t.Fatalf("build levels: %v", err)
	}

	server := &coachServer{
		client:              h.web.Client(),
		jaegerUIURL:         "http://jaeger.example",
		jaegerQueryURL:      "http://jaeger.example",
		jaegerQueryMaxLimit: defaultJaegerQueryMaxLimit,
		webURL:              h.web.URL,
		scenarios:           h.defsByID,
		scenarioSet:         h.defs,
		levels:              levels,
		findRecentTraces:    h.findRecentTraces,
		state:               newLearnerSession(levels),
		subscribers:         map[int]chan coachSnapshot{},
	}

	server.state.SelectedLevel = setup.SelectedLevel
	for _, level := range levels {
		state := server.state.Levels[level.Number]
		state.Current = level.Scenarios[0]
		if correctCount, ok := setup.CorrectCounts[level.Number]; ok {
			state.CorrectCount = correctCount
		}
	}
	server.setFeedbackLocked(fmt.Sprintf("%s selected. Fresh traces will be prepared for the current challenge.", server.levelLabel(server.state.SelectedLevel)), false)

	h.mu.Lock()
	h.current = server
	h.requests = nil
	h.requestSeq = 0
	h.mu.Unlock()

	h.swap.Store(server.routes())
}

func (h *coachE2EHarness) recordTraffic(w http.ResponseWriter, r *http.Request) {
	scenarioID := strings.TrimSpace(r.Header.Get(app.ScenarioHeader))
	if scenarioID == "" {
		scenarioID = strings.TrimSpace(r.URL.Query().Get("scenario"))
	}
	batchID := strings.TrimSpace(r.Header.Get(app.BatchHeader))

	h.mu.Lock()
	h.requestSeq++
	h.requests = append(h.requests, seededTrafficRequest{
		At:         time.Now(),
		Route:      r.URL.Path,
		ScenarioID: scenarioID,
		BatchID:    batchID,
		Seq:        h.requestSeq,
	})
	h.mu.Unlock()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *coachE2EHarness) findRecentTraces(_ context.Context, _ string, operation string, since time.Time, _ int, limit int, batchID string) ([]traceRecord, error) {
	route := routeFromOperation(operation)

	h.mu.Lock()
	defer h.mu.Unlock()

	records := make([]traceRecord, 0, limit)
	for i := len(h.requests) - 1; i >= 0; i-- {
		request := h.requests[i]
		if request.At.Before(since) {
			continue
		}
		if route != "" && request.Route != route {
			continue
		}
		if batchID != "" && request.BatchID != batchID {
			continue
		}

		record, ok := h.traceRecordForRequest(request)
		if !ok {
			continue
		}
		records = append(records, record)
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records, nil
}

func routeFromOperation(operation string) string {
	parts := strings.SplitN(operation, " ", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return operation
}

func (h *coachE2EHarness) traceRecordForRequest(request seededTrafficRequest) (traceRecord, bool) {
	def, ok := h.definitionForRequest(request)
	if !ok {
		return traceRecord{}, false
	}

	extraTags := map[string]string{}
	if key := firstNonEmpty(def.AnswerKey.AttributeKey, def.AnswerKey.SpanAttributeKey); key != "" {
		value := firstNonEmpty(def.AnswerKey.AttributeValue, def.AnswerKey.SpanAttributeValue)
		if value != "" && request.ScenarioID != "" {
			extraTags[key] = value
		}
	}

	record := traceFixture(fmt.Sprintf("trace-%04d", request.Seq), def, extraTags)
	start := request.At.Add(-250 * time.Millisecond)
	record.Start = start
	if request.ScenarioID == "" {
		record.DurationMS = 180
	} else {
		record.DurationMS = 900
	}

	for i := range record.Spans {
		record.Spans[i].Start = start.Add(time.Duration(i+1) * 20 * time.Millisecond)
		if request.ScenarioID == "" {
			record.Spans[i].DurationMS = 110 - (i * 15)
		}
	}
	if request.BatchID != "" && len(record.Spans) > 0 {
		record.Spans[0].Tags[app.BatchAttribute] = request.BatchID
	}

	return record, true
}

func (h *coachE2EHarness) definitionForRequest(request seededTrafficRequest) (scenario.Definition, bool) {
	if request.ScenarioID != "" {
		def, ok := h.defsByID[request.ScenarioID]
		return def, ok
	}

	def, ok := h.defaultsByRoute[request.Route]
	return def, ok
}

func (h *coachE2EHarness) waitForSelectedAnswer(t *testing.T) expectedAnswer {
	t.Helper()

	deadline := time.Now().Add(8 * time.Second)
	for {
		if answer, ok := h.selectedAnswer(); ok {
			return answer
		}
		if time.Now().After(deadline) {
			t.Fatal("selected challenge did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *coachE2EHarness) selectedAnswer() (expectedAnswer, bool) {
	h.mu.Lock()
	server := h.current
	h.mu.Unlock()
	if server == nil {
		return expectedAnswer{}, false
	}

	server.mu.RLock()
	defer server.mu.RUnlock()

	level := server.state.SelectedLevel
	state := server.levelStateLocked(level)
	if state == nil || state.Challenge == nil {
		return expectedAnswer{}, false
	}

	answer := expectedAnswer{
		Type:            state.Current.AssessmentType,
		Service:         state.Current.ExpectedService,
		Issue:           state.Current.ExpectedIssue,
		SpanID:          state.Challenge.ExpectedSpanID,
		AttributeID:     state.Challenge.ExpectedAttributeID,
		SelectedTraceID: "",
	}
	if len(state.Challenge.ExpectedTraceIDs) > 0 {
		answer.SelectedTraceID = state.Challenge.ExpectedTraceIDs[0]
	}
	for _, choice := range state.Challenge.Public.TraceChoices {
		if !containsID(state.Challenge.ExpectedTraceIDs, choice.ID) {
			answer.InvalidTraceID = choice.ID
			break
		}
	}
	answer.FaultyTraceIDs = append(answer.FaultyTraceIDs, state.Challenge.ExpectedFaultyTraceIDs...)
	answer.FailingTraceIDs = append(answer.FailingTraceIDs, state.Challenge.ExpectedFailingTraceIDs...)
	if len(state.Challenge.ExpectedHealthyTraceIDs) > 0 {
		answer.HealthyTraceID = state.Challenge.ExpectedHealthyTraceIDs[0]
	}
	if len(state.Challenge.ExpectedBeforeTraceIDs) > 0 {
		answer.BeforeTraceID = state.Challenge.ExpectedBeforeTraceIDs[0]
	}
	if len(state.Challenge.ExpectedAfterTraceIDs) > 0 {
		answer.AfterTraceID = state.Challenge.ExpectedAfterTraceIDs[0]
	}
	return answer, true
}

func newBrowserRoot(t *testing.T) (context.Context, func()) {
	t.Helper()

	path := browserExecPath(t)
	userDataDir, err := os.MkdirTemp("/tmp", "chromedp-profile-")
	if err != nil {
		t.Fatalf("create temp browser profile: %v", err)
	}
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.UserDataDir(userDataDir),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-crash-reporter", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.WindowSize(1440, 900),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	pageCtx, cancelPage := context.WithTimeout(browserCtx, 2*time.Minute)

	return pageCtx, func() {
		cancelPage()
		cancelBrowser()
		cancelAlloc()
		deadline := time.Now().Add(2 * time.Second)
		for {
			if err := os.RemoveAll(userDataDir); err == nil || time.Now().After(deadline) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func browserExecPath(t *testing.T) string {
	t.Helper()

	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path
		}
	}
	t.Skip("headless Chrome is not available")
	return ""
}

func navigateCoach(t *testing.T, ctx context.Context, url string) {
	t.Helper()

	runChromedp(t, ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible("#title", chromedp.ByID),
	)
	hideSkillPlacementModalForTest(t, ctx)
	waitForCondition(t, ctx, `document.getElementById("title").textContent.trim().length > 0`)
	waitForCondition(t, ctx, `document.getElementById("busy-overlay").classList.contains("hidden")`)
}

func hideSkillPlacementModalForTest(t *testing.T, ctx context.Context) {
	t.Helper()

	var ok bool
	runChromedp(t, ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
		window.localStorage.setItem(%q, "done");
		const modal = document.getElementById("skill-modal");
		if (modal) {
			modal.classList.add("hidden");
		}
		return true;
	})()`, skillPlacementStorageKey), &ok))
}

func reloadPage(t *testing.T, ctx context.Context) {
	t.Helper()

	runChromedp(t, ctx,
		chromedp.Reload(),
		chromedp.WaitVisible("#title", chromedp.ByID),
	)
	waitForCondition(t, ctx, `document.getElementById("title").textContent.trim().length > 0`)
}

func runChromedp(t *testing.T, ctx context.Context, actions ...chromedp.Action) {
	t.Helper()

	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatalf("chromedp run failed: %v", err)
	}
}

func waitForCondition(t *testing.T, ctx context.Context, expression string) {
	t.Helper()

	deadline := time.Now().Add(12 * time.Second)
	for {
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(expression, &ok))
		if err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met: %s (last error: %v)", expression, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func textContent(t *testing.T, ctx context.Context, selector string) string {
	t.Helper()

	var text string
	runChromedp(t, ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
		const element = document.querySelector(%q);
		return element ? element.textContent.trim() : "";
	})()`, selector), &text))
	return text
}

func click(t *testing.T, ctx context.Context, selector string) {
	t.Helper()

	runChromedp(t, ctx, chromedp.Click(selector, chromedp.ByQuery))
}

func setSelectValue(t *testing.T, ctx context.Context, selector, value string, allowUnknown bool) {
	t.Helper()

	expression := fmt.Sprintf(`(() => {
		const element = document.querySelector(%q);
		if (!element) {
			return false;
		}
		if (![...element.options].some((option) => option.value === %q) && %t) {
			element.add(new Option(%q, %q));
		}
		element.value = %q;
		element.dispatchEvent(new Event("input", {bubbles: true}));
		element.dispatchEvent(new Event("change", {bubbles: true}));
		return element.value === %q;
	})()`, selector, value, allowUnknown, value, value, value, value)

	waitForCondition(t, ctx, expression)
}

func setChecked(t *testing.T, ctx context.Context, name, value string, checked bool) {
	t.Helper()

	expression := fmt.Sprintf(`(() => {
		const input = [...document.querySelectorAll(%q)].find((element) => element.value === %q);
		if (!input) {
			return false;
		}
		input.checked = %t;
		input.dispatchEvent(new Event("input", {bubbles: true}));
		input.dispatchEvent(new Event("change", {bubbles: true}));
		return input.checked === %t;
	})()`, fmt.Sprintf(`input[name="%s"]`, name), value, checked, checked)

	waitForCondition(t, ctx, expression)
}

func wrongSelectOptionValue(t *testing.T, ctx context.Context, selector, expected string) string {
	t.Helper()

	var value string
	runChromedp(t, ctx, chromedp.Evaluate(fmt.Sprintf(`(() => {
		const element = document.querySelector(%q);
		if (!element) {
			return "";
		}
		const option = [...element.options].find((candidate) => candidate.value && candidate.value !== %q);
		return option ? option.value : "";
	})()`, selector, expected), &value))
	if value == "" {
		t.Fatalf("no alternative option found for %s", selector)
	}
	return value
}

func waitForLevelUI(t *testing.T, ctx context.Context, assessmentType string) {
	t.Helper()

	assertAssessmentContract(t, ctx)
	waitForFeedbackHidden(t, ctx)
	switch assessmentType {
	case assessmentTraceSearchSpan:
		waitForCondition(t, ctx, `document.querySelector("#selected-trace") !== null`)
	case assessmentCulpritSpan:
		waitForCondition(t, ctx, `document.querySelector("#selected-span") !== null`)
	case assessmentHealthyFaulty:
		waitForCondition(t, ctx, `document.querySelectorAll('select[data-trace-role]').length >= 3`)
	case assessmentBeforeAfter:
		waitForCondition(t, ctx, `document.querySelector("#before-trace") !== null`)
		waitForCondition(t, ctx, `document.querySelector("#after-trace") !== null`)
	case assessmentSpanAttribute:
		waitForCondition(t, ctx, `!document.getElementById("service-field").classList.contains("hidden")`)
	case assessmentIntermittent:
		waitForCondition(t, ctx, `document.querySelectorAll('input[name="failing-trace"]').length >= 2`)
	default:
		t.Fatalf("unsupported assessment type %q", assessmentType)
	}
}

func waitForReferenceTraceLink(t *testing.T, ctx context.Context) {
	t.Helper()
	waitForCondition(t, ctx, `document.querySelector("#reference-trace a") !== null`)
}

func assertAssessmentContract(t *testing.T, ctx context.Context) {
	t.Helper()

	waitForCondition(t, ctx, `document.getElementById("title").textContent.trim().length > 0`)
	waitForCondition(t, ctx, `document.getElementById("assessment-prompt").classList.contains("hidden") || document.getElementById("assessment-prompt").textContent.trim().length > 0`)
	waitForCondition(t, ctx, `document.getElementById("reference-trace").classList.contains("hidden") || document.getElementById("reference-trace").textContent.trim().length > 0`)
}

func waitForFeedbackContains(t *testing.T, ctx context.Context, substring string) {
	t.Helper()
	waitForCondition(t, ctx, fmt.Sprintf(`document.getElementById("feedback").textContent.includes(%q)`, substring))
}

func waitForFeedbackHidden(t *testing.T, ctx context.Context) {
	t.Helper()
	waitForCondition(t, ctx, `document.getElementById("feedback-panel").classList.contains("hidden")`)
}

func waitForLevelReadyModal(t *testing.T, ctx context.Context, level int) {
	t.Helper()
	waitForCondition(t, ctx, `!document.getElementById("level-ready-modal").classList.contains("hidden")`)
	waitForCondition(t, ctx, fmt.Sprintf(`document.getElementById("level-ready-title").textContent.startsWith(%q)`, fmt.Sprintf("Level %d", level)))
	waitForCondition(t, ctx, `document.getElementById("level-ready-summary").textContent.includes("5/5 correct")`)
	if level < 5 {
		waitForCondition(t, ctx, `document.getElementById("level-ready-focus-title").textContent.trim() === "Ready to move on"`)
		waitForCondition(t, ctx, `document.getElementById("level-ready-copy").textContent.includes("ready for the next level")`)
		waitForCondition(t, ctx, `!document.getElementById("level-ready-next").classList.contains("hidden")`)
		return
	}
	waitForCondition(t, ctx, `document.getElementById("level-ready-focus-title").textContent.trim() === "Level complete"`)
	waitForCondition(t, ctx, `document.getElementById("level-ready-copy").textContent.includes("final level")`)
	waitForCondition(t, ctx, `document.getElementById("level-ready-next").classList.contains("hidden")`)
}

func closeLevelReadyModalIfVisible(t *testing.T, ctx context.Context) {
	t.Helper()

	var visible bool
	runChromedp(t, ctx, chromedp.Evaluate(`!document.getElementById("level-ready-modal").classList.contains("hidden")`, &visible))
	if !visible {
		return
	}

	click(t, ctx, "#level-ready-close")
	waitForCondition(t, ctx, `document.getElementById("level-ready-modal").classList.contains("hidden")`)
}

func waitForSelectedLevel(t *testing.T, ctx context.Context, level int) {
	t.Helper()
	waitForCondition(t, ctx, fmt.Sprintf(`document.getElementById("selected-level-title").textContent.startsWith(%q)`, fmt.Sprintf("Level %d", level)))
}

func waitForProgress(t *testing.T, ctx context.Context, value string) {
	t.Helper()
	waitForCondition(t, ctx, fmt.Sprintf(`document.getElementById("selected-level-progress").textContent.trim() === %q`, value))
}

func waitForInputValue(t *testing.T, ctx context.Context, selector, value string) {
	t.Helper()
	waitForCondition(t, ctx, fmt.Sprintf(`(() => {
		const element = document.querySelector(%q);
		return element && element.value === %q;
	})()`, selector, value))
}

func setTraceRole(t *testing.T, ctx context.Context, traceID, role string) {
	t.Helper()
	setSelectValue(t, ctx, fmt.Sprintf(`select[data-trace-role="%s"]`, traceID), role, false)
}

func waitForCheckedCount(t *testing.T, ctx context.Context, selector string, want int) {
	t.Helper()
	waitForCondition(t, ctx, fmt.Sprintf(`document.querySelectorAll(%q).length === %d`, selector, want))
}

func levelUnlockedExpression(level int, unlocked bool) string {
	return fmt.Sprintf(`document.querySelectorAll("#levels .level-button")[%d].dataset.unlocked === %q`, level-1, boolString(unlocked))
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func solveSelectedLevel(t *testing.T, h *coachE2EHarness, ctx context.Context) {
	t.Helper()

	answer := h.waitForSelectedAnswer(t)
	titleBefore := textContent(t, ctx, "#title")
	progressBefore := textContent(t, ctx, "#selected-level-progress")

	if answer.Type != assessmentTraceSearchSpan {
		setSelectValue(t, ctx, "#service", answer.Service, false)
		setSelectValue(t, ctx, "#issue", answer.Issue, false)
	}

	switch answer.Type {
	case assessmentTraceSearchSpan:
		setSelectValue(t, ctx, "#selected-trace", answer.SelectedTraceID, false)
		setSelectValue(t, ctx, "#selected-span", answer.SpanID, false)
	case assessmentCulpritSpan:
		setSelectValue(t, ctx, "#selected-span", answer.SpanID, false)
	case assessmentHealthyFaulty:
		for _, id := range answer.FaultyTraceIDs {
			setTraceRole(t, ctx, id, "slow")
		}
		setTraceRole(t, ctx, answer.HealthyTraceID, "healthy")
	case assessmentBeforeAfter:
		setSelectValue(t, ctx, "#before-trace", answer.BeforeTraceID, false)
		setSelectValue(t, ctx, "#after-trace", answer.AfterTraceID, false)
	case assessmentSpanAttribute:
		setSelectValue(t, ctx, "#selected-span", answer.SpanID, false)
		setSelectValue(t, ctx, "#selected-attribute", answer.AttributeID, false)
	case assessmentIntermittent:
		for _, id := range answer.FailingTraceIDs {
			setChecked(t, ctx, "failing-trace", id, true)
		}
	default:
		t.Fatalf("unsupported assessment type %q", answer.Type)
	}

	click(t, ctx, "#submit")
	waitForFeedbackContains(t, ctx, "Correct.")
	waitForCondition(t, ctx, `document.getElementById("busy-overlay").classList.contains("hidden")`)
	waitForCondition(t, ctx, fmt.Sprintf(`document.getElementById("selected-level-progress").textContent.trim() !== %q || document.getElementById("title").textContent.trim() !== %q`, progressBefore, titleBefore))
}
