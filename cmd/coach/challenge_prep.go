package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

	challenge, err := buildPreparedChallenge(def, groups, s.traceURL)
	if err != nil {
		return nil, generated, err
	}
	if backgroundErr != nil {
		return challenge, generated, backgroundErr
	}
	return challenge, generated, nil
}

func (s *coachServer) seedTraceGroup(ctx context.Context, def scenario.Definition, trafficPath, scenarioID string, count, searchLimit int) ([]traceRecord, int, error) {
	if count <= 0 {
		return nil, 0, nil
	}

	since := time.Now()
	generated, err := s.generateTrafficRequests(ctx, trafficPath, scenarioID, count)
	if err != nil && generated == 0 {
		return nil, 0, err
	}
	if generated < count {
		return nil, generated, fmt.Errorf("generated %d/%d requests for %s", generated, count, def.ID)
	}

	traces, traceErr := s.recentTraces(ctx, def.FocusService, def.FocusOperation, since, count, searchLimit)
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
		count, err := s.generateTrafficRequests(ctx, path, "", 1)
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

func (s *coachServer) generateTrafficRequests(ctx context.Context, trafficPath, scenarioID string, count int) (int, error) {
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

			req, err := httpRequestWithScenario(ctx, target+separator+"scenario="+scenarioID, scenarioID)
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

func httpRequestWithScenario(ctx context.Context, target, scenarioID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if scenarioID != "" {
		req.Header.Set(app.ScenarioHeader, scenarioID)
	}
	return req, nil
}
