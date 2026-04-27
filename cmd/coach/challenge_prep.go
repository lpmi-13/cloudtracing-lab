package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
)

func (s *coachServer) prepareChallenge(ctx context.Context, def scenario.Definition, fallbackCount int) (*preparedChallenge, int, error) {
	plan := normalizeBatchPlan(def, fallbackCount)
	searchLimit := def.SearchLimit
	if searchLimit <= 0 {
		searchLimit = defaultTraceSearchLimit
	}

	backgroundGenerated, backgroundErr := s.generateBackgroundTraffic(ctx, def, plan.BackgroundCount)
	groups := traceGroups{}
	generated := backgroundGenerated

	if def.AssessmentType == assessmentTraceSearchSpan && plan.FaultyCount > 0 && plan.HealthyCount > 0 {
		mixedGroups, count, err := s.seedTraceSearchSpanGroups(ctx, def, plan.FaultyCount, plan.HealthyCount, searchLimit)
		generated += count
		if err != nil {
			return nil, generated, err
		}
		groups = mixedGroups
	} else {
		if plan.FaultyCount > 0 {
			traces, count, err := s.seedTraceGroup(ctx, def, def.TrafficPath, def.ID, plan.FaultyCount, searchLimit)
			generated += count
			if err != nil {
				return nil, generated, err
			}
			groups.Faulty = traces
		}
		if plan.HealthyCount > 0 {
			traces, count, err := s.seedTraceGroup(ctx, def, def.TrafficPath, "", plan.HealthyCount, searchLimit)
			generated += count
			if err != nil {
				return nil, generated, err
			}
			groups.Healthy = traces
		}
	}
	if plan.BeforeCount > 0 {
		traces, count, err := s.seedTraceGroup(ctx, def, def.TrafficPath, "", plan.BeforeCount, searchLimit)
		generated += count
		if err != nil {
			return nil, generated, err
		}
		groups.Before = traces
	}
	if plan.AfterCount > 0 {
		traces, count, err := s.seedTraceGroup(ctx, def, def.TrafficPath, def.ID, plan.AfterCount, searchLimit)
		generated += count
		if err != nil {
			return nil, generated, err
		}
		groups.After = traces
	}

	challenge, err := buildPreparedChallenge(def, groups, s.traceURL, s.searchURL, s.compareURL)
	if err != nil {
		return nil, generated, err
	}
	if backgroundErr != nil {
		return challenge, generated, backgroundErr
	}
	return challenge, generated, nil
}

func (s *coachServer) seedTraceSearchSpanGroups(ctx context.Context, def scenario.Definition, faultyCount, healthyCount, searchLimit int) (traceGroups, int, error) {
	total := faultyCount + healthyCount
	if total <= 0 {
		return traceGroups{}, 0, nil
	}

	batchID := newTraceBatchID()
	since := time.Now()
	generated := 0

	for _, scenarioID := range traceSearchSpanScenarioOrder(def.ID, batchID, faultyCount, healthyCount) {
		count, err := s.generateTrafficRequests(ctx, def.TrafficPath, scenarioID, batchID, 1)
		generated += count
		if err != nil {
			return traceGroups{}, generated, err
		}
	}
	if generated < total {
		return traceGroups{}, generated, fmt.Errorf("generated %d/%d requests for %s", generated, total, def.ID)
	}

	traces, err := s.recentTraces(ctx, def.FocusService, def.FocusOperation, since, total, searchLimit, batchID)
	if err != nil {
		return traceGroups{}, generated, err
	}
	if len(traces) < total {
		return traceGroups{}, generated, fmt.Errorf("loaded %d/%d traces for %s", len(traces), total, def.ID)
	}

	groups := traceGroups{}
	for _, trace := range traces {
		if traceHasAttribute(trace, app.ScenarioAttribute, def.ID) {
			groups.Faulty = append(groups.Faulty, trace)
			continue
		}
		groups.Healthy = append(groups.Healthy, trace)
	}
	if len(groups.Faulty) < faultyCount || len(groups.Healthy) < healthyCount {
		return traceGroups{}, generated, fmt.Errorf("partitioned %d faulty and %d healthy traces for %s", len(groups.Faulty), len(groups.Healthy), def.ID)
	}

	groups.Faulty = groups.Faulty[:faultyCount]
	groups.Healthy = groups.Healthy[:healthyCount]
	return groups, generated, nil
}

func (s *coachServer) seedTraceGroup(ctx context.Context, def scenario.Definition, trafficPath, scenarioID string, count, searchLimit int) ([]traceRecord, int, error) {
	return s.seedTraceGroupWithBatchID(ctx, def, trafficPath, scenarioID, count, searchLimit, "")
}

func (s *coachServer) seedTraceGroupWithBatchID(ctx context.Context, def scenario.Definition, trafficPath, scenarioID string, count, searchLimit int, batchID string) ([]traceRecord, int, error) {
	if count <= 0 {
		return nil, 0, nil
	}

	since := time.Now()
	if batchID == "" {
		batchID = newTraceBatchID()
	}
	generated, err := s.generateTrafficRequests(ctx, trafficPath, scenarioID, batchID, count)
	if err != nil && generated == 0 {
		return nil, 0, err
	}
	if generated < count {
		return nil, generated, fmt.Errorf("generated %d/%d requests for %s", generated, count, def.ID)
	}

	traces, traceErr := s.recentTraces(ctx, def.FocusService, def.FocusOperation, since, count, searchLimit, batchID)
	if traceErr != nil {
		return nil, generated, traceErr
	}
	if len(traces) < count {
		return nil, generated, fmt.Errorf("loaded %d/%d traces for %s", len(traces), count, def.ID)
	}
	return traces[:count], generated, err
}

func (s *coachServer) generateBackgroundTraffic(ctx context.Context, def scenario.Definition, total int) (int, error) {
	if total <= 0 {
		return 0, nil
	}

	paths := s.backgroundPathsForScenario(def)
	if len(paths) == 0 {
		return 0, nil
	}

	var (
		firstErr  error
		generated int
	)
	for i := 0; i < total; i++ {
		path := paths[i%len(paths)]
		count, err := s.generateTrafficRequests(ctx, path, "", newTraceBatchID(), 1)
		generated += count
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return generated, firstErr
}

func (s *coachServer) backgroundPathsForScenario(def scenario.Definition) []string {
	paths := make([]string, 0, len(s.scenarioSet))
	seen := map[string]struct{}{}
	for _, candidate := range s.scenarioSet {
		if candidate.Route == "" || candidate.TrafficPath == "" || candidate.Route == def.Route {
			continue
		}
		if _, ok := seen[candidate.Route]; ok {
			continue
		}
		seen[candidate.Route] = struct{}{}
		paths = append(paths, candidate.TrafficPath)
	}
	return paths
}

var traceBatchSeq atomic.Uint64

func newTraceBatchID() string {
	return fmt.Sprintf("batch-%d", traceBatchSeq.Add(1))
}

func traceSearchSpanScenarioOrder(scenarioID, batchID string, faultyCount, healthyCount int) []string {
	total := faultyCount + healthyCount
	order := make([]string, total)
	if total == 0 || faultyCount <= 0 {
		return order
	}

	offset := traceSearchSpanFaultyOffset(batchID, total)
	for i := 0; i < faultyCount; i++ {
		order[(offset+i)%total] = scenarioID
	}
	return order
}

func traceSearchSpanFaultyOffset(batchID string, total int) int {
	if total <= 1 {
		return 0
	}

	// Jaeger search sorts these batches by recency, so slot 0 becomes the last row.
	// Keep the faulty trace out of that oldest slot while still varying its position.
	return 1 + stableChoiceOffset(batchID, total-1)
}

func (s *coachServer) generateTrafficRequests(ctx context.Context, trafficPath, scenarioID, batchID string, count int) (int, error) {
	target := s.webURL + trafficPath
	separator := "&"
	if !strings.Contains(trafficPath, "?") {
		separator = "?"
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		successes int
	)

	wg.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()

			req, err := httpRequestWithScenario(ctx, target+separator+"scenario="+scenarioID, scenarioID, batchID)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			resp, err := s.client.Do(req)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			resp.Body.Close()

			mu.Lock()
			successes++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if successes == 0 && firstErr != nil {
		return 0, firstErr
	}
	return successes, firstErr
}

func httpRequestWithScenario(ctx context.Context, target, scenarioID, batchID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if scenarioID != "" {
		req.Header.Set(app.ScenarioHeader, scenarioID)
	}
	if batchID != "" {
		req.Header.Set(app.BatchHeader, batchID)
	}
	return req, nil
}
