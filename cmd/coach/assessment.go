package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
)

const (
	assessmentTraceSearchSpan = "trace_search_span"
	assessmentCulpritSpan     = "culprit_span"
	assessmentCompareCulprit  = "compare_culprit_span"
	assessmentHealthyFaulty   = "healthy_faulty"
	assessmentBeforeAfter     = "before_after"
	assessmentCompareConfig   = "compare_config_change"
	assessmentSpanAttribute   = "span_attribute"
	assessmentIntermittent    = "intermittent_failure"
)

type traceRecord struct {
	ID         string
	Start      time.Time
	DurationMS int
	Spans      []traceSpan
}

type traceSpan struct {
	ID         string
	Service    string
	Operation  string
	Start      time.Time
	DurationMS int
	Tags       map[string]string
	Error      bool
}

type publicTraceOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
}

type publicLinkOption struct {
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
}

type publicSpanOption struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
}

type publicAttributeOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type publicAssessment struct {
	Ready                bool                               `json:"ready"`
	Type                 string                             `json:"type"`
	Prompt               string                             `json:"prompt"`
	RequiredEvidence     []string                           `json:"required_evidence"`
	PassCondition        string                             `json:"pass_condition"`
	StartGuide           string                             `json:"start_guide"`
	InvestigationLink    *publicLinkOption                  `json:"investigation_link,omitempty"`
	CompareLink          *publicLinkOption                  `json:"compare_link,omitempty"`
	ReferenceTrace       *publicTraceOption                 `json:"reference_trace,omitempty"`
	TraceChoices         []publicTraceOption                `json:"trace_choices,omitempty"`
	BeforeTraceChoices   []publicTraceOption                `json:"before_trace_choices,omitempty"`
	AfterTraceChoices    []publicTraceOption                `json:"after_trace_choices,omitempty"`
	FaultyTraceChoices   []publicTraceOption                `json:"faulty_trace_choices,omitempty"`
	HealthyTraceChoices  []publicTraceOption                `json:"healthy_trace_choices,omitempty"`
	FailingTraceChoices  []publicTraceOption                `json:"failing_trace_choices,omitempty"`
	TraceSpanChoices     map[string][]publicSpanOption      `json:"trace_span_choices,omitempty"`
	SpanChoices          []publicSpanOption                 `json:"span_choices,omitempty"`
	SpanAttributeChoices map[string][]publicAttributeOption `json:"span_attribute_choices,omitempty"`
	AttributeChoices     []publicAttributeOption            `json:"attribute_choices,omitempty"`
	UnavailableReason    string                             `json:"unavailable_reason,omitempty"`
}

type preparedChallenge struct {
	Public                  publicAssessment
	ExpectedTraceIDs        []string
	ExpectedSpanID          string
	ExpectedAttributeID     string
	ExpectedFaultyTraceIDs  []string
	ExpectedHealthyTraceIDs []string
	ExpectedBeforeTraceIDs  []string
	ExpectedAfterTraceIDs   []string
	ExpectedFailingTraceIDs []string
}

type gradeResult struct {
	Pass    bool
	Message string
}

type traceGroups struct {
	Faulty  []traceRecord
	Healthy []traceRecord
	Before  []traceRecord
	After   []traceRecord
}

var compareConfigCatalog = []publicAttributeOption{
	{ID: "lab.config.inventory_reserve_strategy", Label: "Inventory reserve strategy"},
	{ID: "lab.config.inventory_batch_size", Label: "Inventory batch size"},
	{ID: "lab.config.inventory_read_consistency", Label: "Inventory read consistency"},
	{ID: "lab.config.orders_sort_strategy", Label: "Orders sort strategy"},
	{ID: "lab.config.orders_history_window_days", Label: "Orders history window"},
	{ID: "lab.config.orders_page_size", Label: "Orders page size"},
	{ID: "lab.config.payments_lock_strategy", Label: "Payments lock strategy"},
	{ID: "lab.config.payments_retry_budget", Label: "Payments retry budget"},
}

func assessmentShell(def scenario.Definition) publicAssessment {
	return publicAssessment{
		Ready:            false,
		Type:             def.AssessmentType,
		Prompt:           firstNonEmpty(def.AssessmentPrompt, def.Prompt),
		RequiredEvidence: requiredEvidenceFor(def),
		PassCondition:    passConditionFor(def),
		StartGuide:       startGuideFor(def),
	}
}

func requiredEvidenceFor(def scenario.Definition) []string {
	switch def.AssessmentType {
	case assessmentTraceSearchSpan:
		return []string{
			"Select the slow trace you inspected from the prepared search results.",
			"Select the slow span inside that trace.",
		}
	case assessmentCulpritSpan:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
			"Select the responsible span inside the reference trace.",
		}
	case assessmentCompareCulprit:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
			"Select the responsible span inside the slower after trace.",
		}
	case assessmentHealthyFaulty:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
			"Select every slow trace from the mixed candidate set.",
			"Select the one healthy trace from the same candidate set.",
		}
	case assessmentBeforeAfter:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
		}
	case assessmentSpanAttribute:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
			"Select the responsible span inside the reference trace.",
			"Select the proof tag on that responsible span that proves the root cause.",
		}
	case assessmentCompareConfig:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
			"Select the responsible span inside the slower after trace.",
			"Select the changed setting on that span that you would revert.",
		}
	case assessmentIntermittent:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
			"Select every failing trace from the intermittent candidate set.",
		}
	default:
		return []string{
			"Select the responsible service.",
			"Select the failure mode.",
		}
	}
}

func passConditionFor(def scenario.Definition) string {
	switch def.AssessmentType {
	case assessmentTraceSearchSpan:
		return "Full credit requires the intended slow trace from the prepared search and the correct slow span from that trace."
	case assessmentCulpritSpan:
		return "Full credit requires the correct responsible service, failure mode, and responsible span from the reference trace."
	case assessmentCompareCulprit:
		return "Full credit requires the correct responsible service, failure mode, and responsible span from the compare investigation."
	case assessmentHealthyFaulty:
		return "Full credit requires the correct responsible service, failure mode, all slow traces, and the healthy trace."
	case assessmentBeforeAfter:
		return "Full credit requires the correct responsible service and failure mode based on the Jaeger Compare investigation."
	case assessmentSpanAttribute:
		return "Full credit requires the correct responsible service, failure mode, responsible span, and proof tag."
	case assessmentCompareConfig:
		return "Full credit requires the correct responsible service, failure mode, responsible span, and changed setting from the compare investigation."
	case assessmentIntermittent:
		return "Full credit requires the correct responsible service, failure mode, and every failing trace from the intermittent set."
	default:
		return "Full credit requires the correct responsible service, failure mode, and supporting evidence."
	}
}

func startGuideFor(def scenario.Definition) string {
	switch def.Level {
	case 1:
		return "Open the prepared trace search, inspect one slow trace, and pick the slow span."
	case 2:
		if def.AssessmentType == assessmentCompareCulprit {
			return fmt.Sprintf("Open Jaeger Compare for %s, inspect the slower trace, and identify the responsible span.", def.Route)
		}
		return "Open the trace links, pick the slow ones, and choose the one healthy trace."
	case 3:
		if def.AssessmentType == assessmentCompareConfig {
			return fmt.Sprintf("Open Jaeger Compare for %s, inspect the slower span, and identify the changed setting to revert.", def.Route)
		}
		return fmt.Sprintf("Open Jaeger Compare with one before trace and one after trace for %s.", def.Route)
	case 4:
		return fmt.Sprintf("Open the reference trace for %s, find the responsible span, then prove it with one proof tag.", def.Route)
	case 5:
		return fmt.Sprintf("Open the trace links for %s and select the requests that actually fail.", def.Route)
	default:
		return fmt.Sprintf("Inspect the newest traces for %s.", def.Route)
	}
}

func normalizeBatchPlan(def scenario.Definition, fallbackCount int) scenario.BatchPlan {
	plan := def.BatchPlan
	if fallbackCount <= 0 {
		fallbackCount = defaultTraceBatchSize
	}
	if plan.BackgroundCount <= 0 {
		plan.BackgroundCount = 3
	}

	switch def.AssessmentType {
	case assessmentTraceSearchSpan:
		if plan.FaultyCount <= 0 {
			plan.FaultyCount = 1
		}
		if plan.HealthyCount <= 0 {
			if plan.CandidateCount > plan.FaultyCount {
				plan.HealthyCount = plan.CandidateCount - plan.FaultyCount
			} else if plan.BackgroundCount > 0 {
				plan.HealthyCount = plan.BackgroundCount
			} else {
				plan.HealthyCount = 3
			}
		}
	case assessmentCulpritSpan:
		if plan.FaultyCount <= 0 {
			plan.FaultyCount = max(2, fallbackCount-1)
		}
	case assessmentCompareCulprit:
		if plan.BeforeCount <= 0 {
			plan.BeforeCount = 2
		}
		if plan.AfterCount <= 0 {
			plan.AfterCount = 2
		}
	case assessmentHealthyFaulty:
		if plan.FaultyCount <= 0 {
			plan.FaultyCount = 2
		}
		if plan.HealthyCount <= 0 {
			plan.HealthyCount = 1
		}
	case assessmentBeforeAfter:
		if plan.BeforeCount <= 0 {
			plan.BeforeCount = 2
		}
		if plan.AfterCount <= 0 {
			plan.AfterCount = 2
		}
	case assessmentCompareConfig:
		if plan.BeforeCount <= 0 {
			plan.BeforeCount = 2
		}
		if plan.AfterCount <= 0 {
			plan.AfterCount = 2
		}
	case assessmentSpanAttribute:
		if plan.FaultyCount <= 0 {
			plan.FaultyCount = max(2, fallbackCount-2)
		}
	case assessmentIntermittent:
		if plan.FaultyCount <= 0 {
			plan.FaultyCount = 2
		}
		if plan.HealthyCount <= 0 {
			plan.HealthyCount = 3
		}
	}

	if plan.CandidateCount <= 0 {
		plan.CandidateCount = plan.FaultyCount + plan.HealthyCount + plan.BeforeCount + plan.AfterCount
		if plan.CandidateCount == 0 {
			plan.CandidateCount = fallbackCount
		}
	}
	return plan
}

func buildPreparedChallenge(
	def scenario.Definition,
	groups traceGroups,
	traceURL func(string) string,
	searchURL func(string, string, int, map[string]string) string,
	compareURL func(string, string, []string) string,
) (*preparedChallenge, error) {
	public := assessmentShell(def)

	switch def.AssessmentType {
	case assessmentTraceSearchSpan:
		if len(groups.Faulty) == 0 {
			return nil, fmt.Errorf("no faulty traces available for %s", def.ID)
		}
		choices := append([]traceRecord{}, groups.Faulty...)
		choices = append(choices, groups.Healthy...)
		sortTraceRecords(choices)

		expectedTraceIDs := traceIDs(groups.Faulty)
		expectedSpanID := spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation)
		traceSpanChoices, err := traceSearchSpanChoices(def, choices, expectedTraceIDs, expectedSpanID)
		if err != nil {
			return nil, err
		}

		searchTags := map[string]string{}
		if batchID := sharedTraceAttributeValue(choices, app.BatchAttribute); batchID != "" {
			searchTags[app.BatchAttribute] = batchID
		}

		public.Ready = true
		public.InvestigationLink = searchLinkOption(traceSearchLabel(def), searchURL(def.FocusService, def.FocusOperation, max(def.SearchLimit, len(choices)*2), searchTags))
		public.TraceChoices = traceOptions(choices, traceURL)
		public.TraceSpanChoices = traceSpanChoices
		return &preparedChallenge{
			Public:           public,
			ExpectedTraceIDs: expectedTraceIDs,
			ExpectedSpanID:   expectedSpanID,
		}, nil

	case assessmentCulpritSpan:
		if len(groups.Faulty) == 0 {
			return nil, fmt.Errorf("no faulty traces available for %s", def.ID)
		}
		reference := groups.Faulty[0]
		spanChoices := spanChoicesForTrace(def, reference)
		expectedSpanID := spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation)
		if !containsSpanChoice(spanChoices, expectedSpanID) {
			return nil, fmt.Errorf("reference trace for %s does not include span %q", def.ID, expectedSpanID)
		}

		public.Ready = true
		public.ReferenceTrace = traceOption(reference, traceURL)
		public.SpanChoices = spanChoices
		return &preparedChallenge{
			Public:         public,
			ExpectedSpanID: expectedSpanID,
		}, nil

	case assessmentCompareCulprit:
		if len(groups.Before) == 0 || len(groups.After) == 0 {
			return nil, fmt.Errorf("compare trace set incomplete for %s", def.ID)
		}
		beforeChoices := traceOptions(groups.Before, traceURL)
		afterChoices := traceOptions(groups.After, traceURL)
		choices := append(append([]publicTraceOption{}, beforeChoices...), afterChoices...)
		cohortIDs := append(append([]string{}, traceIDs(groups.Before)...), traceIDs(groups.After)...)

		reference := groups.After[0]
		spanChoices := spanChoicesForTrace(def, reference)
		expectedSpanID := spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation)
		if !containsSpanChoice(spanChoices, expectedSpanID) {
			return nil, fmt.Errorf("after trace for %s does not include span %q", def.ID, expectedSpanID)
		}

		public.Ready = true
		public.TraceChoices = choices
		public.BeforeTraceChoices = beforeChoices
		public.AfterTraceChoices = afterChoices
		public.CompareLink = compareLinkOption(beforeChoices[0].ID, afterChoices[0].ID, cohortIDs, compareURL)
		public.SpanChoices = spanChoices
		return &preparedChallenge{
			Public:                 public,
			ExpectedSpanID:         expectedSpanID,
			ExpectedBeforeTraceIDs: traceIDs(groups.Before),
			ExpectedAfterTraceIDs:  traceIDs(groups.After),
		}, nil

	case assessmentHealthyFaulty:
		if len(groups.Faulty) < 2 || len(groups.Healthy) == 0 {
			return nil, fmt.Errorf("mixed trace set incomplete for %s", def.ID)
		}
		choices := append([]traceRecord{}, groups.Healthy...)
		choices = append(choices, groups.Faulty...)

		public.Ready = true
		public.TraceChoices = shuffledTraceOptions(choices, traceURL)
		public.FaultyTraceChoices = public.TraceChoices
		public.HealthyTraceChoices = public.TraceChoices
		return &preparedChallenge{
			Public:                  public,
			ExpectedFaultyTraceIDs:  traceIDs(groups.Faulty),
			ExpectedHealthyTraceIDs: traceIDs(groups.Healthy),
		}, nil

	case assessmentBeforeAfter:
		if len(groups.Before) == 0 || len(groups.After) == 0 {
			return nil, fmt.Errorf("before/after trace set incomplete for %s", def.ID)
		}
		beforeChoices := traceOptions(groups.Before, traceURL)
		afterChoices := traceOptions(groups.After, traceURL)
		choices := append(append([]publicTraceOption{}, beforeChoices...), afterChoices...)
		cohortIDs := append(append([]string{}, traceIDs(groups.Before)...), traceIDs(groups.After)...)
		public.Ready = true
		public.TraceChoices = choices
		public.BeforeTraceChoices = beforeChoices
		public.AfterTraceChoices = afterChoices
		public.CompareLink = compareLinkOption(beforeChoices[0].ID, afterChoices[0].ID, cohortIDs, compareURL)
		return &preparedChallenge{
			Public:                 public,
			ExpectedBeforeTraceIDs: traceIDs(groups.Before),
			ExpectedAfterTraceIDs:  traceIDs(groups.After),
		}, nil

	case assessmentCompareConfig:
		if len(groups.Before) == 0 || len(groups.After) == 0 {
			return nil, fmt.Errorf("compare trace set incomplete for %s", def.ID)
		}
		beforeChoices := traceOptions(groups.Before, traceURL)
		afterChoices := traceOptions(groups.After, traceURL)
		choices := append(append([]publicTraceOption{}, beforeChoices...), afterChoices...)
		cohortIDs := append(append([]string{}, traceIDs(groups.Before)...), traceIDs(groups.After)...)

		reference := groups.After[0]
		beforeReference := groups.Before[0]
		spanChoices := spanChoicesForTrace(def, reference)
		expectedSpanID := spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation)
		if !containsSpanChoice(spanChoices, expectedSpanID) {
			return nil, fmt.Errorf("after trace for %s does not include span %q", def.ID, expectedSpanID)
		}
		if countSpanChoicesForService(spanChoices, def.ExpectedService) < 4 {
			return nil, fmt.Errorf("after trace for %s exposes fewer than four responsible-span choices for %s", def.ID, def.ExpectedService)
		}
		expectedAttributeID := def.AnswerKey.AttributeKey
		if expectedAttributeID == "" {
			return nil, fmt.Errorf("no changed setting answer key configured for %s", def.ID)
		}

		beforeSpan, ok := findSpan(beforeReference, def.ExpectedService, def.AnswerKey.SpanOperation)
		if !ok {
			return nil, fmt.Errorf("before trace for %s does not include responsible span %q", def.ID, expectedSpanID)
		}
		afterSpan, ok := findSpan(reference, def.ExpectedService, def.AnswerKey.SpanOperation)
		if !ok {
			return nil, fmt.Errorf("after trace for %s does not include responsible span %q", def.ID, expectedSpanID)
		}

		beforeValue := beforeSpan.Tags[expectedAttributeID]
		afterValue := afterSpan.Tags[expectedAttributeID]
		if afterValue == "" {
			return nil, fmt.Errorf("after responsible span for %s does not include changed setting %q", def.ID, expectedAttributeID)
		}
		if beforeValue == "" {
			return nil, fmt.Errorf("before responsible span for %s does not include changed setting %q", def.ID, expectedAttributeID)
		}
		if beforeValue == afterValue {
			return nil, fmt.Errorf("changed setting %q does not differ across the compare boundary for %s", expectedAttributeID, def.ID)
		}

		public.Ready = true
		public.TraceChoices = choices
		public.BeforeTraceChoices = beforeChoices
		public.AfterTraceChoices = afterChoices
		public.CompareLink = compareLinkOption(beforeChoices[0].ID, afterChoices[0].ID, cohortIDs, compareURL)
		public.SpanChoices = spanChoices
		public.AttributeChoices = compareConfigChoices(expectedAttributeID)
		return &preparedChallenge{
			Public:                 public,
			ExpectedSpanID:         expectedSpanID,
			ExpectedAttributeID:    expectedAttributeID,
			ExpectedBeforeTraceIDs: traceIDs(groups.Before),
			ExpectedAfterTraceIDs:  traceIDs(groups.After),
		}, nil

	case assessmentSpanAttribute:
		if len(groups.Faulty) == 0 {
			return nil, fmt.Errorf("no faulty traces available for %s", def.ID)
		}
		reference := groups.Faulty[0]
		spanChoices := spanChoicesForTrace(def, reference)
		expectedSpanID := spanChoiceID(def.ExpectedService, def.AnswerKey.SpanOperation)
		if !containsSpanChoice(spanChoices, expectedSpanID) {
			return nil, fmt.Errorf("reference trace for %s does not include span %q", def.ID, expectedSpanID)
		}

		culpritSpan, ok := findSpan(reference, def.ExpectedService, def.AnswerKey.SpanOperation)
		if !ok {
			return nil, fmt.Errorf("responsible span missing from reference trace for %s", def.ID)
		}
		expectedAttributeID := attributeChoiceID(def.AnswerKey.AttributeKey, def.AnswerKey.AttributeValue)
		if expectedAttributeID == "=" {
			expectedAttributeID = attributeChoiceID(def.AnswerKey.SpanAttributeKey, def.AnswerKey.SpanAttributeValue)
		}
		if expectedAttributeID == "=" {
			return nil, fmt.Errorf("no attribute answer key configured for %s", def.ID)
		}
		attributeChoicesBySpan := attributeChoicesBySpanForTrace(reference, expectedAttributeID)
		attributeChoices := attributeChoicesBySpan[expectedSpanID]
		if _, ok := culpritSpan.Tags[def.AnswerKey.AttributeKey]; def.AnswerKey.AttributeKey != "" && !ok {
			return nil, fmt.Errorf("responsible span for %s does not include attribute %q", def.ID, def.AnswerKey.AttributeKey)
		}
		if !containsAttributeChoice(attributeChoices, expectedAttributeID) {
			return nil, fmt.Errorf("reference trace for %s does not include attribute %q", def.ID, expectedAttributeID)
		}

		public.Ready = true
		public.ReferenceTrace = traceOption(reference, traceURL)
		public.SpanChoices = spanChoices
		public.SpanAttributeChoices = attributeChoicesBySpan
		public.AttributeChoices = attributeChoices
		return &preparedChallenge{
			Public:              public,
			ExpectedSpanID:      expectedSpanID,
			ExpectedAttributeID: expectedAttributeID,
		}, nil

	case assessmentIntermittent:
		if len(groups.Faulty) == 0 || len(groups.Healthy) == 0 {
			return nil, fmt.Errorf("intermittent trace set incomplete for %s", def.ID)
		}
		choices := append([]traceRecord{}, groups.Healthy...)
		choices = append(choices, groups.Faulty...)
		sortTraceRecords(choices)

		public.Ready = true
		public.TraceChoices = traceOptions(choices, traceURL)
		public.FailingTraceChoices = public.TraceChoices
		return &preparedChallenge{
			Public:                  public,
			ExpectedFailingTraceIDs: traceIDs(groups.Faulty),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported assessment type %q", def.AssessmentType)
	}
}

func gradeSubmission(def scenario.Definition, challenge *preparedChallenge, req gradeRequest) gradeResult {
	serviceOK := req.SuspectedService == def.ExpectedService
	issueOK := req.SuspectedIssue == def.ExpectedIssue

	if challenge == nil {
		return gradeResult{
			Pass:    false,
			Message: "The current challenge is not ready yet. Refresh the page or request a new challenge before submitting.",
		}
	}

	switch def.AssessmentType {
	case assessmentTraceSearchSpan:
		traceOK := containsID(challenge.ExpectedTraceIDs, req.SelectedTraceID)
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		if traceOK && spanOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The trace used and slow span both match the intended slow request.",
			}
		}
		return gradeResult{Pass: false, Message: traceSearchSpanFeedback(traceOK, spanOK)}

	case assessmentCulpritSpan:
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		if serviceOK && issueOK && spanOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The responsible service, failure mode, and responsible span all match the reference trace.",
			}
		}
		return gradeResult{Pass: false, Message: culpritSpanFeedback(serviceOK, issueOK, spanOK)}

	case assessmentCompareCulprit:
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		if serviceOK && issueOK && spanOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The responsible service, failure mode, and responsible span match what changed in Jaeger Compare.",
			}
		}
		return gradeResult{Pass: false, Message: compareCulpritFeedback(serviceOK, issueOK, spanOK)}

	case assessmentHealthyFaulty:
		faultyOK := sameStringSet(req.FaultyTraceIDs, challenge.ExpectedFaultyTraceIDs)
		healthyOK := containsID(challenge.ExpectedHealthyTraceIDs, req.HealthyTraceID)
		if serviceOK && issueOK && faultyOK && healthyOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. You separated the slow traces from the healthy trace and chose the right responsible service and failure mode.",
			}
		}
		return gradeResult{Pass: false, Message: mixedTraceFeedback(serviceOK, issueOK, faultyOK, healthyOK)}

	case assessmentBeforeAfter:
		if serviceOK && issueOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The responsible service and failure mode match what changed in Jaeger Compare.",
			}
		}
		return gradeResult{Pass: false, Message: serviceFailureModeFeedback(serviceOK, issueOK) + " Recheck what changed in Jaeger Compare."}

	case assessmentSpanAttribute:
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		attributeOK := req.SelectedAttribute == challenge.ExpectedAttributeID
		if serviceOK && issueOK && spanOK && attributeOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The responsible service, failure mode, responsible span, and proof tag all line up.",
			}
		}
		return gradeResult{Pass: false, Message: spanAttributeFeedback(serviceOK, issueOK, spanOK, attributeOK)}

	case assessmentCompareConfig:
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		attributeOK := req.SelectedAttribute == challenge.ExpectedAttributeID
		if serviceOK && issueOK && spanOK && attributeOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The responsible service, failure mode, responsible span, and changed setting all match the compare evidence.",
			}
		}
		return gradeResult{Pass: false, Message: compareConfigFeedback(serviceOK, issueOK, spanOK, attributeOK)}

	case assessmentIntermittent:
		failingOK := sameStringSet(req.FailingTraceIDs, challenge.ExpectedFailingTraceIDs)
		if serviceOK && issueOK && failingOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. You identified the failing traces and chose the right responsible service and failure mode.",
			}
		}
		return gradeResult{Pass: false, Message: intermittentFeedback(serviceOK, issueOK, failingOK)}

	default:
		return gradeResult{
			Pass:    false,
			Message: "This challenge type is not configured correctly yet.",
		}
	}
}

func traceSearchSpanFeedback(traceOK, spanOK bool) string {
	switch {
	case !traceOK && spanOK:
		return "The slow span is right, but the trace used is wrong. Reopen the prepared search and choose the intended slow trace."
	case traceOK && !spanOK:
		return "The trace used is right, but the slow span is wrong. Reopen that trace and identify the specific slow span."
	default:
		return "The trace used and slow span are both wrong. Reopen the prepared search and inspect the slow branch again."
	}
}

func culpritSpanFeedback(serviceOK, issueOK, spanOK bool) string {
	if serviceOK && issueOK && !spanOK {
		return "The responsible service and failure mode are right, but the responsible span is wrong. Reopen the reference trace and identify the specific slow span."
	}
	if serviceOK && !issueOK && spanOK {
		return "The responsible service and responsible span are right, but the failure mode is wrong. Recheck the failure mode before submitting again."
	}
	return serviceFailureModeFeedback(serviceOK, issueOK) + " The responsible span is also wrong."
}

func compareCulpritFeedback(serviceOK, issueOK, spanOK bool) string {
	if serviceOK && issueOK && !spanOK {
		return "The responsible service and failure mode are right, but the responsible span is wrong. Reopen the slower trace in Jaeger Compare and identify the specific slow span."
	}
	if serviceOK && !issueOK && spanOK {
		return "The responsible service and responsible span are right, but the failure mode is wrong. Recheck the failure mode before submitting again."
	}
	return serviceFailureModeFeedback(serviceOK, issueOK) + " The responsible span is also wrong."
}

func mixedTraceFeedback(serviceOK, issueOK, faultyOK, healthyOK bool) string {
	switch {
	case serviceOK && issueOK && !faultyOK && healthyOK:
		return "The responsible service and failure mode are right, but the slow trace selections are wrong. Compare the candidate traces again and select every slow one."
	case serviceOK && issueOK && faultyOK && !healthyOK:
		return "The responsible service and failure mode are right, but the healthy trace is wrong. Pick the one trace that stayed healthy."
	case serviceOK && issueOK:
		return "The responsible service and failure mode are right, but the slow trace and healthy trace selections are wrong. Separate the healthy trace from the slow ones before resubmitting."
	default:
		return serviceFailureModeFeedback(serviceOK, issueOK) + " Recheck the slow trace and healthy trace selections."
	}
}

func spanAttributeFeedback(serviceOK, issueOK, spanOK, attributeOK bool) string {
	switch {
	case serviceOK && issueOK && !spanOK && attributeOK:
		return "The proof tag is right, but the responsible span is wrong. Reopen the reference trace and identify the exact span carrying that proof."
	case serviceOK && issueOK && spanOK && !attributeOK:
		return "The responsible service, failure mode, and responsible span are right, but the proof tag is wrong. Choose the tag on that span that most specifically proves the root cause."
	case serviceOK && issueOK:
		return "The responsible service and failure mode are right, but the responsible span and proof tag are both wrong. Recheck both fields."
	default:
		return serviceFailureModeFeedback(serviceOK, issueOK) + " Recheck the responsible span and proof tag."
	}
}

func compareConfigFeedback(serviceOK, issueOK, spanOK, attributeOK bool) string {
	switch {
	case serviceOK && issueOK && !spanOK && attributeOK:
		return "The changed setting is right, but the responsible span is wrong. Reopen the slower trace from Compare and identify the exact span that carries that setting."
	case serviceOK && issueOK && spanOK && !attributeOK:
		return "The responsible service, failure mode, and responsible span are right, but the changed setting is wrong. Choose the config tag key on that span whose value changed across the compare boundary."
	case serviceOK && issueOK:
		return "The responsible service and failure mode are right, but the responsible span and changed setting are both wrong. Recheck both fields."
	default:
		return serviceFailureModeFeedback(serviceOK, issueOK) + " Recheck the responsible span and changed setting."
	}
}

func intermittentFeedback(serviceOK, issueOK, failingOK bool) string {
	if serviceOK && issueOK && !failingOK {
		return "The responsible service and failure mode are right, but the failing traces selection is wrong. Select every failing trace and leave the healthy ones unselected."
	}
	return serviceFailureModeFeedback(serviceOK, issueOK) + " Recheck the failing traces selection."
}

func serviceFailureModeFeedback(serviceOK, issueOK bool) string {
	switch {
	case serviceOK && !issueOK:
		return "The responsible service is right, but the failure mode is wrong."
	case !serviceOK && issueOK:
		return "The failure mode is right, but the responsible service is wrong."
	case !serviceOK && !issueOK:
		return "Neither the responsible service nor the failure mode is correct yet."
	default:
		return "The responsible service and failure mode are right."
	}
}

func traceOption(trace traceRecord, traceURL func(string) string) *publicTraceOption {
	return &publicTraceOption{
		ID:    trace.ID,
		Label: traceLabel(trace),
		URL:   traceURL(trace.ID),
	}
}

func traceOptions(records []traceRecord, traceURL func(string) string) []publicTraceOption {
	sorted := append([]traceRecord{}, records...)
	sortTraceRecords(sorted)
	return traceOptionsInOrder(sorted, traceURL)
}

func shuffledTraceOptions(records []traceRecord, traceURL func(string) string) []publicTraceOption {
	return traceOptionsInOrder(stableShuffleTraceRecords(records), traceURL)
}

func traceOptionsInOrder(records []traceRecord, traceURL func(string) string) []publicTraceOption {
	ordered := append([]traceRecord{}, records...)

	options := make([]publicTraceOption, 0, len(ordered))
	for _, record := range ordered {
		options = append(options, publicTraceOption{
			ID:    record.ID,
			Label: traceLabel(record),
			URL:   traceURL(record.ID),
		})
	}
	return options
}

func stableShuffleTraceRecords(records []traceRecord) []traceRecord {
	shuffled := append([]traceRecord{}, records...)
	if len(shuffled) <= 1 {
		return shuffled
	}

	seed := strings.Join(traceIDs(shuffled), "|")
	sort.Slice(shuffled, func(i, j int) bool {
		left := stableHash(seed + "|" + shuffled[i].ID)
		right := stableHash(seed + "|" + shuffled[j].ID)
		if left == right {
			return shuffled[i].ID < shuffled[j].ID
		}
		return left < right
	})
	return shuffled
}

func compareLinkOption(beforeID, afterID string, cohortIDs []string, compareURL func(string, string, []string) string) *publicLinkOption {
	return &publicLinkOption{
		Label: "Compare traces",
		URL:   compareURL(beforeID, afterID, cohortIDs),
	}
}

func spanChoicesForTrace(def scenario.Definition, trace traceRecord) []publicSpanOption {
	type spanKey struct {
		Service   string
		Operation string
	}

	seen := map[spanKey]struct{}{}
	choices := make([]publicSpanOption, 0, len(trace.Spans))
	for _, span := range trace.Spans {
		if span.Service == def.FocusService && span.Operation == def.FocusOperation {
			continue
		}
		key := spanKey{Service: span.Service, Operation: span.Operation}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		choices = append(choices, publicSpanOption{
			ID:        spanChoiceID(span.Service, span.Operation),
			Label:     span.Operation,
			Service:   span.Service,
			Operation: span.Operation,
		})
	}

	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Label < choices[j].Label
	})
	return choices
}

func traceSearchSpanChoices(def scenario.Definition, traces []traceRecord, expectedTraceIDs []string, expectedSpanID string) (map[string][]publicSpanOption, error) {
	traceSpanChoices := make(map[string][]publicSpanOption, len(traces))
	expectedTraceSet := make(map[string]struct{}, len(expectedTraceIDs))
	minChoices := 0

	for _, traceID := range expectedTraceIDs {
		expectedTraceSet[traceID] = struct{}{}
	}

	for _, trace := range traces {
		choices := spanChoicesForTrace(def, trace)
		if len(choices) == 0 {
			return nil, fmt.Errorf("trace %s does not expose span choices for %s", trace.ID, def.ID)
		}
		if _, ok := expectedTraceSet[trace.ID]; ok && !containsSpanChoice(choices, expectedSpanID) {
			return nil, fmt.Errorf("trace %s does not include expected span %q", trace.ID, expectedSpanID)
		}
		traceSpanChoices[trace.ID] = choices
		if minChoices == 0 || len(choices) < minChoices {
			minChoices = len(choices)
		}
	}

	for _, trace := range traces {
		requiredID := ""
		if _, ok := expectedTraceSet[trace.ID]; ok {
			requiredID = expectedSpanID
		}
		traceSpanChoices[trace.ID] = arrangeTraceSearchSpanChoices(trimSpanChoices(traceSpanChoices[trace.ID], minChoices, requiredID), requiredID, trace.ID)
	}

	return traceSpanChoices, nil
}

func trimSpanChoices(options []publicSpanOption, limit int, requiredID string) []publicSpanOption {
	if limit <= 0 || len(options) <= limit {
		return append([]publicSpanOption{}, options...)
	}

	trimmed := append([]publicSpanOption{}, options[:limit]...)
	if requiredID != "" && !containsSpanChoice(trimmed, requiredID) {
		if required, ok := findSpanChoice(options, requiredID); ok {
			trimmed[len(trimmed)-1] = required
			sort.Slice(trimmed, func(i, j int) bool {
				return trimmed[i].Label < trimmed[j].Label
			})
		}
	}
	return trimmed
}

func findSpanChoice(options []publicSpanOption, id string) (publicSpanOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return publicSpanOption{}, false
}

func arrangeTraceSearchSpanChoices(options []publicSpanOption, requiredID, seed string) []publicSpanOption {
	shuffled := stableShuffleSpanChoices(options, seed)
	if len(shuffled) <= 1 || requiredID == "" {
		return shuffled
	}

	required, ok := findSpanChoice(shuffled, requiredID)
	if !ok {
		return shuffled
	}

	distractors := make([]publicSpanOption, 0, len(shuffled)-1)
	for _, option := range shuffled {
		if option.ID == requiredID {
			continue
		}
		distractors = append(distractors, option)
	}

	targetIndex := stableChoiceOffset(seed+"|"+requiredID, len(shuffled))
	arranged := make([]publicSpanOption, 0, len(shuffled))
	arranged = append(arranged, distractors[:targetIndex]...)
	arranged = append(arranged, required)
	arranged = append(arranged, distractors[targetIndex:]...)
	return arranged
}

func rotateSpanChoices(options []publicSpanOption, seed string) []publicSpanOption {
	rotated := append([]publicSpanOption{}, options...)
	if len(rotated) <= 1 {
		return rotated
	}

	offset := stableChoiceOffset(seed, len(rotated))
	if offset == 0 {
		return rotated
	}
	return append(rotated[offset:], rotated[:offset]...)
}

func stableShuffleSpanChoices(options []publicSpanOption, seed string) []publicSpanOption {
	shuffled := append([]publicSpanOption{}, options...)
	if len(shuffled) <= 1 {
		return shuffled
	}

	sort.Slice(shuffled, func(i, j int) bool {
		left := stableHash(seed + "|" + shuffled[i].ID)
		right := stableHash(seed + "|" + shuffled[j].ID)
		if left == right {
			return shuffled[i].ID < shuffled[j].ID
		}
		return left < right
	})
	return shuffled
}

func attributeChoicesBySpanForTrace(trace traceRecord, requiredAttributeID string) map[string][]publicAttributeOption {
	options := make(map[string][]publicAttributeOption, len(trace.Spans))

	for _, span := range trace.Spans {
		spanID := spanChoiceID(span.Service, span.Operation)
		seen := map[string]struct{}{}
		choices := make([]publicAttributeOption, 0, len(span.Tags))
		for key, value := range span.Tags {
			id := attributeChoiceID(key, value)
			if !isVisibleProofTag(key, id, requiredAttributeID) {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			choices = append(choices, publicAttributeOption{
				ID:    id,
				Label: proofTagLabel(key, value),
			})
		}
		sort.Slice(choices, func(i, j int) bool {
			return choices[i].Label < choices[j].Label
		})
		if len(choices) > 0 {
			options[spanID] = choices
		}
	}

	return options
}

func isVisibleProofTag(key, id, requiredAttributeID string) bool {
	return strings.HasPrefix(key, "lab.") || id == requiredAttributeID
}

func proofTagLabel(key, value string) string {
	switch key {
	case "lab.query_label":
		return "Query label: " + value
	case "lab.statement_signature":
		return "Statement signature: " + value
	case "lab.wait_checkpoint":
		return "Wait checkpoint: " + value
	case "db.statement":
		return "Statement text: " + value
	case "db.system":
		return "Database system: " + value
	default:
		return humanizeAttributeKey(key) + ": " + value
	}
}

func compareConfigChoices(expectedID string) []publicAttributeOption {
	options := append([]publicAttributeOption{}, compareConfigCatalog...)
	if expectedID != "" && !containsAttributeChoice(options, expectedID) {
		options = append(options, publicAttributeOption{
			ID:    expectedID,
			Label: compareConfigLabel(expectedID),
		})
	}
	return rotateAttributeChoices(options, expectedID)
}

func compareConfigLabel(id string) string {
	for _, option := range compareConfigCatalog {
		if option.ID == id {
			return option.Label
		}
	}
	label := strings.TrimPrefix(id, "lab.config.")
	label = strings.ReplaceAll(label, ".", " ")
	label = strings.ReplaceAll(label, "_", " ")
	parts := strings.Fields(label)
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	if len(parts) == 0 {
		return "Changed setting"
	}
	return strings.Join(parts, " ")
}

func rotateAttributeChoices(options []publicAttributeOption, seed string) []publicAttributeOption {
	rotated := append([]publicAttributeOption{}, options...)
	if len(rotated) <= 1 {
		return rotated
	}

	offset := stableChoiceOffset(seed, len(rotated))
	if offset == 0 {
		return rotated
	}
	return append(rotated[offset:], rotated[:offset]...)
}

func humanizeAttributeKey(key string) string {
	key = strings.TrimPrefix(key, "lab.")
	key = strings.ReplaceAll(key, ".", " ")
	key = strings.ReplaceAll(key, "_", " ")
	parts := strings.Fields(key)
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	if len(parts) == 0 {
		return "Attribute"
	}
	return strings.Join(parts, " ")
}

func traceIDs(records []traceRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	sort.Strings(ids)
	return ids
}

func sharedTraceAttributeValue(records []traceRecord, key string) string {
	if len(records) == 0 || key == "" {
		return ""
	}

	var shared string
	for _, record := range records {
		value := traceAttributeValue(record, key)
		if value == "" {
			return ""
		}
		if shared == "" {
			shared = value
			continue
		}
		if shared != value {
			return ""
		}
	}
	return shared
}

func traceAttributeValue(record traceRecord, key string) string {
	if key == "" {
		return ""
	}
	for _, span := range record.Spans {
		if value := span.Tags[key]; value != "" {
			return value
		}
	}
	return ""
}

func traceLabel(record traceRecord) string {
	return fmt.Sprintf("trace %s at %s", traceDisplayID(record.ID), record.Start.Local().Format("15:04:05"))
}

func traceDisplayID(id string) string {
	if len(id) <= 7 {
		return id
	}
	return id[:7]
}

func stableChoiceOffset(seed string, size int) int {
	if size <= 1 || seed == "" {
		return 0
	}
	return int(stableHash(seed) % uint32(size))
}

func stableHash(seed string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(seed))
	return hash.Sum32()
}

func spanChoiceID(service, operation string) string {
	return service + "|" + operation
}

func attributeChoiceID(key, value string) string {
	return key + "=" + value
}

func searchLinkOption(label, url string) *publicLinkOption {
	return &publicLinkOption{
		Label: label,
		URL:   url,
	}
}

func traceSearchLabel(def scenario.Definition) string {
	if def.FocusOperation == "" {
		return "Open the prepared trace search"
	}
	return fmt.Sprintf("Open the prepared search for %s", def.FocusOperation)
}

func findSpan(trace traceRecord, service, operation string) (traceSpan, bool) {
	for _, span := range trace.Spans {
		if span.Service == service && span.Operation == operation {
			return span, true
		}
	}
	return traceSpan{}, false
}

func containsSpanChoice(options []publicSpanOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func countSpanChoicesForService(options []publicSpanOption, service string) int {
	count := 0
	for _, option := range options {
		if option.Service == service {
			count++
		}
	}
	return count
}

func containsAttributeChoice(options []publicAttributeOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	leftCopy := append([]string{}, left...)
	rightCopy := append([]string{}, right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)

	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func sortTraceRecords(records []traceRecord) {
	sort.Slice(records, func(i, j int) bool {
		return records[i].Start.After(records[j].Start)
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
