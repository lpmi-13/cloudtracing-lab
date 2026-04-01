package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"cloudtracing/internal/scenario"
)

func TestTrafficPathsForScenarioUsesDistinctRoutes(t *testing.T) {
	s := &coachServer{
		scenarioSet: []scenario.Definition{
			{ID: "checkout-lock", Route: "/checkout", TrafficPath: "/checkout?sku=sku-28"},
			{ID: "search", Route: "/search", TrafficPath: "/search?q=trail"},
			{ID: "orders", Route: "/account/orders", TrafficPath: "/account/orders?user_id=user-4"},
			{ID: "checkout-n-plus-one", Route: "/checkout", TrafficPath: "/checkout?sku=sku-14"},
		},
	}

	paths := s.trafficPathsForScenario(scenario.Definition{
		ID:          "checkout-lock",
		Route:       "/checkout",
		TrafficPath: "/checkout?sku=sku-28",
	})

	want := []string{
		"/account/orders?user_id=user-4",
		"/search?q=trail",
		"/checkout?sku=sku-28",
	}
	if len(paths) != len(want) {
		t.Fatalf("expected %d paths, got %d: %v", len(want), len(paths), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("path %d: expected %q, got %q", i, want[i], paths[i])
		}
	}
}

func TestPrepareCurrentScenarioSeedsFiveTracesPerEndpoint(t *testing.T) {
	var (
		mu     sync.Mutex
		counts = map[string]int{}
	)

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()

		if got := r.URL.Query().Get("scenario"); got != "checkout-lock" {
			t.Errorf("expected scenario query %q, got %q", "checkout-lock", got)
		}
		if got := r.Header.Get("X-Trace-Lab-Scenario"); got != "checkout-lock" {
			t.Errorf("expected scenario header %q, got %q", "checkout-lock", got)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer web.Close()

	current := scenario.Definition{
		ID:          "checkout-lock",
		Route:       "/checkout",
		TrafficPath: "/checkout?sku=sku-28",
	}

	s := &coachServer{
		client: web.Client(),
		webURL: web.URL,
		scenarioSet: []scenario.Definition{
			current,
			{ID: "search", Route: "/search", TrafficPath: "/search?q=trail"},
			{ID: "orders", Route: "/account/orders", TrafficPath: "/account/orders?user_id=user-4"},
		},
		current: current,
	}

	def, generated, err := s.prepareCurrentScenario(context.Background(), defaultTraceBatchSize)
	if err != nil {
		t.Fatalf("prepareCurrentScenario returned error: %v", err)
	}
	if def.ID != current.ID {
		t.Fatalf("expected current scenario %q, got %q", current.ID, def.ID)
	}

	expectedTotal := defaultTraceBatchSize * 3
	if generated != expectedTotal {
		t.Fatalf("expected %d generated traces, got %d", expectedTotal, generated)
	}

	wantCounts := map[string]int{
		"/checkout":       defaultTraceBatchSize,
		"/search":         defaultTraceBatchSize,
		"/account/orders": defaultTraceBatchSize,
	}

	mu.Lock()
	defer mu.Unlock()
	if len(counts) != len(wantCounts) {
		t.Fatalf("expected %d paths, got %d: %v", len(wantCounts), len(counts), counts)
	}
	for path, want := range wantCounts {
		if got := counts[path]; got != want {
			t.Fatalf("path %s: expected %d requests, got %d", path, want, got)
		}
	}
}
