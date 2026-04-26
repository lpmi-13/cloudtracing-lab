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
	assessmentHealthyFaulty   = "healthy_faulty"
	assessmentBeforeAfter     = "before_after"
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
	ReferenceTrace       *publicTraceOption                 `json:"reference_trace,omitempty"`
	TraceChoices         []publicTraceOption                `json:"trace_choices,omitempty"`
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
			"Select the culprit span inside that trace.",
		}
	case assessmentCulpritSpan:
		return []string{
			"Select the culprit service.",
			"Select the failure mode.",
			"Select the slow span inside the reference trace.",
		}
	case assessmentHealthyFaulty:
		return []string{
			"Select the culprit service.",
			"Select the failure mode.",
			"Select every slow trace from the mixed candidate set.",
			"Select the one healthy trace from the same candidate set.",
		}
	case assessmentBeforeAfter:
		return []string{
			"Select the culprit service.",
			"Select the failure mode.",
			"Select one baseline trace from before the regression.",
			"Select one slow trace from after the change.",
		}
	case assessmentSpanAttribute:
		return []string{
			"Select the culprit service.",
			"Select the failure mode.",
			"Select the culprit span inside the reference trace.",
			"Select the supporting attribute that proves the diagnosis.",
		}
	case assessmentIntermittent:
		return []string{
			"Select the culprit service.",
			"Select the failure mode.",
			"Select every failing trace from the intermittent candidate set.",
		}
	default:
		return []string{
			"Select the culprit service.",
			"Select the failure mode.",
		}
	}
}

func passConditionFor(def scenario.Definition) string {
	switch def.AssessmentType {
	case assessmentTraceSearchSpan:
		return "Full credit requires the intended slow trace from the prepared search and the correct culprit span from that trace."
	case assessmentCulpritSpan:
		return "Full credit requires the correct service, issue, and culprit span from the reference trace."
	case assessmentHealthyFaulty:
		return "Full credit requires the correct service, issue, all slow traces, and the healthy trace."
	case assessmentBeforeAfter:
		return "Full credit requires the correct service, issue, one valid before trace, and one valid after trace."
	case assessmentSpanAttribute:
		return "Full credit requires the correct service, issue, culprit span, and supporting attribute."
	case assessmentIntermittent:
		return "Full credit requires the correct service, issue, and every failing trace from the intermittent set."
	default:
		return "Full credit requires the correct diagnosis and evidence."
	}
}

func startGuideFor(def scenario.Definition) string {
	switch def.Level {
	case 1:
		return "Open the prepared trace search, inspect one slow trace, and pick the culprit span."
	case 2:
		return "Open the trace links, pick the slow ones, and choose the one healthy trace."
	case 3:
		return fmt.Sprintf("Compare one before trace with one slow after trace for %s.", def.Route)
	case 4:
		return fmt.Sprintf("Open the reference trace for %s, find the culprit span, then prove it with one attribute.", def.Route)
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

func buildPreparedChallenge(def scenario.Definition, groups traceGroups, traceURL func(string) string, searchURL func(string, string, int, map[string]string) string) (*preparedChallenge, error) {
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

	case assessmentHealthyFaulty:
		if len(groups.Faulty) < 2 || len(groups.Healthy) == 0 {
			return nil, fmt.Errorf("mixed trace set incomplete for %s", def.ID)
		}
		choices := append([]traceRecord{}, groups.Healthy...)
		choices = append(choices, groups.Faulty...)
		sortTraceRecords(choices)

		public.Ready = true
		public.TraceChoices = traceOptions(choices, traceURL)
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
		choices := append([]traceRecord{}, groups.Before...)
		choices = append(choices, groups.After...)
		sortTraceRecords(choices)
		public.Ready = true
		public.TraceChoices = traceOptions(choices, traceURL)
		return &preparedChallenge{
			Public:                 public,
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
			return nil, fmt.Errorf("culprit span missing from reference trace for %s", def.ID)
		}
		attributeChoicesBySpan := attributeChoicesBySpanForTrace(reference)
		attributeChoices := attributeChoicesBySpan[expectedSpanID]
		expectedAttributeID := attributeChoiceID(def.AnswerKey.AttributeKey, def.AnswerKey.AttributeValue)
		if expectedAttributeID == "=" {
			expectedAttributeID = attributeChoiceID(def.AnswerKey.SpanAttributeKey, def.AnswerKey.SpanAttributeValue)
		}
		if expectedAttributeID == "=" {
			return nil, fmt.Errorf("no attribute answer key configured for %s", def.ID)
		}
		if _, ok := culpritSpan.Tags[def.AnswerKey.AttributeKey]; def.AnswerKey.AttributeKey != "" && !ok {
			return nil, fmt.Errorf("culprit span for %s does not include attribute %q", def.ID, def.AnswerKey.AttributeKey)
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
				Message: "Correct. You chose the intended slow trace from the prepared search and isolated the culprit span.",
			}
		}
		return gradeResult{Pass: false, Message: traceSearchSpanFeedback(traceOK, spanOK)}

	case assessmentCulpritSpan:
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		if serviceOK && issueOK && spanOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The diagnosis and the culprit span both match the reference trace.",
			}
		}
		return gradeResult{Pass: false, Message: culpritSpanFeedback(serviceOK, issueOK, spanOK)}

	case assessmentHealthyFaulty:
		faultyOK := sameStringSet(req.FaultyTraceIDs, challenge.ExpectedFaultyTraceIDs)
		healthyOK := containsID(challenge.ExpectedHealthyTraceIDs, req.HealthyTraceID)
		if serviceOK && issueOK && faultyOK && healthyOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. You separated the slow traces from the healthy trace and named the right culprit.",
			}
		}
		return gradeResult{Pass: false, Message: mixedTraceFeedback(serviceOK, issueOK, faultyOK, healthyOK)}

	case assessmentBeforeAfter:
		beforeOK := containsID(challenge.ExpectedBeforeTraceIDs, req.BeforeTraceID)
		afterOK := containsID(challenge.ExpectedAfterTraceIDs, req.AfterTraceID)
		if serviceOK && issueOK && beforeOK && afterOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. You matched the right service and issue, and your before/after comparison uses the intended traces.",
			}
		}
		return gradeResult{Pass: false, Message: beforeAfterFeedback(serviceOK, issueOK, beforeOK, afterOK)}

	case assessmentSpanAttribute:
		spanOK := req.SelectedSpan == challenge.ExpectedSpanID
		attributeOK := req.SelectedAttribute == challenge.ExpectedAttributeID
		if serviceOK && issueOK && spanOK && attributeOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. The diagnosis, culprit span, and supporting attribute all line up.",
			}
		}
		return gradeResult{Pass: false, Message: spanAttributeFeedback(serviceOK, issueOK, spanOK, attributeOK)}

	case assessmentIntermittent:
		failingOK := sameStringSet(req.FailingTraceIDs, challenge.ExpectedFailingTraceIDs)
		if serviceOK && issueOK && failingOK {
			return gradeResult{
				Pass:    true,
				Message: "Correct. You identified the intermittent failure set and the owning service.",
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
		return "The culprit span is right, but the trace choice is wrong. Reopen the prepared search and choose the intended slow trace."
	case traceOK && !spanOK:
		return "The trace choice is right, but the span evidence is wrong. Reopen that trace and identify the specific slow span."
	default:
		return "Neither the trace choice nor the span evidence is correct yet. Reopen the prepared search and inspect the slow branch again."
	}
}

func culpritSpanFeedback(serviceOK, issueOK, spanOK bool) string {
	if serviceOK && issueOK && !spanOK {
		return "The diagnosis is right, but the span evidence is wrong. Reopen the reference trace and identify the specific slow span."
	}
	if serviceOK && !issueOK && spanOK {
		return "The span points to the right service, but the issue type is wrong. Recheck the failure mode before submitting again."
	}
	return diagnosisFeedback(serviceOK, issueOK) + " The reference span still needs attention."
}

func mixedTraceFeedback(serviceOK, issueOK, faultyOK, healthyOK bool) string {
	switch {
	case serviceOK && issueOK && !faultyOK && healthyOK:
		return "The diagnosis is right, but the slow trace set is wrong. Compare the candidate traces again and select every slow one."
	case serviceOK && issueOK && faultyOK && !healthyOK:
		return "The diagnosis is right, but the healthy trace is wrong. Pick the one trace that stayed healthy."
	case serviceOK && issueOK:
		return "The diagnosis is right, but the trace grouping is wrong. Separate the healthy trace from the slow ones before resubmitting."
	default:
		return diagnosisFeedback(serviceOK, issueOK) + " The trace grouping still needs work."
	}
}

func beforeAfterFeedback(serviceOK, issueOK, beforeOK, afterOK bool) string {
	switch {
	case serviceOK && issueOK && !beforeOK && afterOK:
		return "The diagnosis is right, but the baseline trace is wrong. Pick a trace from before the regression window."
	case serviceOK && issueOK && beforeOK && !afterOK:
		return "The diagnosis is right, but the after trace is wrong. Pick one of the slow traces from after the change."
	case serviceOK && issueOK:
		return "The diagnosis is right, but the comparison pair is wrong. Recheck which trace belongs to each side of the change."
	default:
		return diagnosisFeedback(serviceOK, issueOK) + " The before/after comparison is still off."
	}
}

func spanAttributeFeedback(serviceOK, issueOK, spanOK, attributeOK bool) string {
	switch {
	case serviceOK && issueOK && !spanOK && attributeOK:
		return "The supporting attribute is right, but the culprit span is wrong. Reopen the reference trace and identify the exact span carrying that attribute."
	case serviceOK && issueOK && spanOK && !attributeOK:
		return "The diagnosis and culprit span are right, but the supporting attribute is wrong. Choose the attribute that proves the root cause."
	case serviceOK && issueOK:
		return "The diagnosis is right, but the supporting evidence is incomplete. Recheck both the culprit span and the attribute."
	default:
		return diagnosisFeedback(serviceOK, issueOK) + " The span evidence still needs work."
	}
}

func intermittentFeedback(serviceOK, issueOK, failingOK bool) string {
	if serviceOK && issueOK && !failingOK {
		return "The diagnosis is right, but the intermittent failure set is wrong. Select every failing trace and leave the healthy ones unselected."
	}
	return diagnosisFeedback(serviceOK, issueOK) + " Recheck which traces actually failed."
}

func diagnosisFeedback(serviceOK, issueOK bool) string {
	switch {
	case serviceOK && !issueOK:
		return "The service is right, but the issue type is wrong."
	case !serviceOK && issueOK:
		return "The issue type is right, but the owning service is wrong."
	case !serviceOK && !issueOK:
		return "Neither the service nor the issue type is correct yet."
	default:
		return "The diagnosis is right."
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

	options := make([]publicTraceOption, 0, len(sorted))
	for _, record := range sorted {
		options = append(options, publicTraceOption{
			ID:    record.ID,
			Label: traceLabel(record),
			URL:   traceURL(record.ID),
		})
	}
	return options
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
		traceSpanChoices[trace.ID] = rotateSpanChoices(trimSpanChoices(traceSpanChoices[trace.ID], minChoices, requiredID), trace.ID)
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

func attributeChoicesBySpanForTrace(trace traceRecord) map[string][]publicAttributeOption {
	options := make(map[string][]publicAttributeOption, len(trace.Spans))

	for _, span := range trace.Spans {
		spanID := spanChoiceID(span.Service, span.Operation)
		seen := map[string]struct{}{}
		choices := make([]publicAttributeOption, 0, len(span.Tags))
		for key, value := range span.Tags {
			if !isAssessmentAttribute(key) {
				continue
			}
			id := attributeChoiceID(key, value)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			choices = append(choices, publicAttributeOption{
				ID:    id,
				Label: fmt.Sprintf("%s = %s", key, value),
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

func isAssessmentAttribute(key string) bool {
	return strings.HasPrefix(key, "lab.") || key == "db.statement" || key == "db.system"
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
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(seed))
	return int(hash.Sum32() % uint32(size))
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
