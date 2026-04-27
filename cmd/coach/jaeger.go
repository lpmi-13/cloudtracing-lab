package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloudtracing/internal/app"
)

const defaultTraceSearchLimit = 40
const defaultJaegerQueryMaxLimit = 79

type jaegerTraceSearchResponse struct {
	Data []jaegerTrace `json:"data"`
}

type jaegerTrace struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerSpan struct {
	SpanID        string      `json:"spanID"`
	OperationName string      `json:"operationName"`
	StartTime     int64       `json:"startTime"`
	Duration      int64       `json:"duration"`
	ProcessID     string      `json:"processID"`
	Tags          []jaegerTag `json:"tags"`
}

type jaegerProcess struct {
	ServiceName string `json:"serviceName"`
}

type jaegerTag struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func (s *coachServer) traceURL(traceID string) string {
	if s.jaegerUIURL == "" || traceID == "" {
		return ""
	}
	return s.jaegerUIURL + "/trace/" + traceID
}

func (s *coachServer) searchURL(service, operation string, limit int, tags map[string]string) string {
	if s.jaegerUIURL == "" {
		return ""
	}

	values := url.Values{}
	if service != "" {
		values.Set("service", service)
	}
	if operation != "" {
		values.Set("operation", operation)
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if len(tags) > 0 {
		encoded, err := json.Marshal(tags)
		if err == nil {
			values.Set("tags", string(encoded))
		}
	}
	values.Set("lookback", "1h")

	return s.jaegerUIURL + "/search?" + values.Encode()
}

func (s *coachServer) compareURL(traceA, traceB string, cohort []string) string {
	if s.jaegerUIURL == "" || traceA == "" || traceB == "" {
		return ""
	}

	orderedCohort := orderedUniqueTraceIDs(append([]string{traceA, traceB}, cohort...))
	values := url.Values{}
	for _, id := range orderedCohort {
		values.Add("cohort", id)
	}

	encoded := values.Encode()
	if encoded == "" {
		return s.jaegerUIURL + "/trace/" + traceA + "..." + traceB
	}
	return s.jaegerUIURL + "/trace/" + traceA + "..." + traceB + "?" + encoded
}

func (s *coachServer) recentTraces(ctx context.Context, service, operation string, since time.Time, need, limit int, batchID string) ([]traceRecord, error) {
	if s.findRecentTraces != nil {
		return s.findRecentTraces(ctx, service, operation, since, need, limit, batchID)
	}
	return s.fetchRecentTraces(ctx, service, operation, since, need, limit, batchID)
}

func (s *coachServer) fetchRecentTraces(ctx context.Context, service, operation string, since time.Time, need, limit int, batchID string) ([]traceRecord, error) {
	if s.jaegerQueryURL == "" {
		return nil, fmt.Errorf("JAEGER_QUERY_URL is not configured")
	}
	if need <= 0 {
		return nil, nil
	}
	limit = s.effectiveTraceSearchLimit(need, limit)

	deadline := time.Now().Add(8 * time.Second)
	var last []traceRecord
	var lastErr error

	for {
		records, err := s.searchTraces(ctx, service, operation, limit)
		if err == nil {
			filtered := filterRecentTraces(records, since, batchID)
			if len(filtered) >= need {
				return filtered[:need], nil
			}
			last = filtered
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, lastErr
			}
			if len(last) > 0 {
				return last, fmt.Errorf("found %d recent traces for %s %s, need %d", len(last), service, operation, need)
			}
			return nil, fmt.Errorf("no recent traces found for %s %s", service, operation)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(350 * time.Millisecond):
		}
	}
}

func (s *coachServer) effectiveTraceSearchLimit(need, requested int) int {
	if need <= 0 {
		return 0
	}
	if requested <= 0 {
		requested = defaultTraceSearchLimit
	}
	if requested < need*2 {
		requested = need * 2
	}

	maxLimit := s.jaegerQueryMaxLimit
	if maxLimit <= 0 {
		maxLimit = defaultJaegerQueryMaxLimit
	}
	if requested > maxLimit {
		requested = maxLimit
	}
	if requested < need && maxLimit >= need {
		requested = need
	}
	return requested
}

func (s *coachServer) searchTraces(ctx context.Context, service, operation string, limit int) ([]traceRecord, error) {
	values := url.Values{}
	values.Set("service", service)
	if operation != "" {
		values.Set("operation", operation)
	}
	values.Set("limit", strconv.Itoa(limit))
	values.Set("lookback", "1h")

	endpoint := s.jaegerQueryURL + "/api/traces?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("jaeger returned status %d", resp.StatusCode)
	}

	var payload jaegerTraceSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	records := make([]traceRecord, 0, len(payload.Data))
	for _, trace := range payload.Data {
		record, ok := convertJaegerTrace(trace, service, operation)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	sortTraceRecords(records)
	return records, nil
}

func convertJaegerTrace(raw jaegerTrace, focusService, focusOperation string) (traceRecord, bool) {
	if raw.TraceID == "" || len(raw.Spans) == 0 {
		return traceRecord{}, false
	}

	spans := make([]traceSpan, 0, len(raw.Spans))
	var (
		rootFound bool
		rootStart time.Time
		rootDurMS int
		earliest  time.Time
	)

	for _, span := range raw.Spans {
		service := raw.Processes[span.ProcessID].ServiceName
		start := time.Unix(0, span.StartTime*int64(time.Microsecond))
		durationMS := int(span.Duration / 1000)
		tags := jaegerTagMap(span.Tags)
		record := traceSpan{
			ID:         span.SpanID,
			Service:    service,
			Operation:  span.OperationName,
			Start:      start,
			DurationMS: durationMS,
			Tags:       tags,
			Error:      isErrorSpan(tags),
		}
		spans = append(spans, record)

		if earliest.IsZero() || start.Before(earliest) {
			earliest = start
		}
		if service == focusService && span.OperationName == focusOperation {
			if !rootFound || start.Before(rootStart) {
				rootFound = true
				rootStart = start
				rootDurMS = durationMS
			}
		}
	}

	if !rootFound {
		rootStart = earliest
		for _, span := range spans {
			if span.Start.Equal(earliest) {
				rootDurMS = span.DurationMS
				break
			}
		}
	}

	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start.Equal(spans[j].Start) {
			if spans[i].Service == spans[j].Service {
				return spans[i].Operation < spans[j].Operation
			}
			return spans[i].Service < spans[j].Service
		}
		return spans[i].Start.Before(spans[j].Start)
	})

	return traceRecord{
		ID:         raw.TraceID,
		Start:      rootStart,
		DurationMS: rootDurMS,
		Spans:      spans,
	}, true
}

func orderedUniqueTraceIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func jaegerTagMap(tags []jaegerTag) map[string]string {
	parsed := make(map[string]string, len(tags))
	for _, tag := range tags {
		switch value := tag.Value.(type) {
		case string:
			parsed[tag.Key] = value
		case bool:
			parsed[tag.Key] = strconv.FormatBool(value)
		case float64:
			if value == float64(int64(value)) {
				parsed[tag.Key] = strconv.FormatInt(int64(value), 10)
			} else {
				parsed[tag.Key] = strconv.FormatFloat(value, 'f', -1, 64)
			}
		default:
			parsed[tag.Key] = strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return parsed
}

func isErrorSpan(tags map[string]string) bool {
	if strings.EqualFold(tags["error"], "true") {
		return true
	}
	if strings.EqualFold(tags["otel.status_code"], "error") {
		return true
	}
	if status := tags["http.status_code"]; status != "" {
		code, err := strconv.Atoi(status)
		return err == nil && code >= http.StatusInternalServerError
	}
	return false
}

func filterRecentTraces(records []traceRecord, since time.Time, batchID string) []traceRecord {
	cutoff := since.Add(-250 * time.Millisecond)
	filtered := make([]traceRecord, 0, len(records))
	seen := map[string]struct{}{}

	for _, record := range records {
		if record.Start.Before(cutoff) {
			continue
		}
		if batchID != "" && !traceHasAttribute(record, app.BatchAttribute, batchID) {
			continue
		}
		if _, ok := seen[record.ID]; ok {
			continue
		}
		seen[record.ID] = struct{}{}
		filtered = append(filtered, record)
	}
	sortTraceRecords(filtered)
	return filtered
}

func traceHasAttribute(record traceRecord, key, want string) bool {
	for _, span := range record.Spans {
		if span.Tags[key] == want {
			return true
		}
	}
	return false
}
