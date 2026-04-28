package main

import (
	"strings"
	"testing"
)

func TestFeedbackUsesVisibleFieldNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		message  string
		want     []string
		unwanted []string
	}{
		{
			name:     "trace_search_span",
			message:  traceSearchSpanFeedback(true, false),
			want:     []string{"trace used", "slow span"},
			unwanted: []string{"trace choice", "span evidence"},
		},
		{
			name:     "culprit_span",
			message:  culpritSpanFeedback(true, false, true),
			want:     []string{"responsible service", "failure mode", "culprit span"},
			unwanted: []string{"issue type", "diagnosis"},
		},
		{
			name:     "healthy_faulty",
			message:  mixedTraceFeedback(true, true, false, false),
			want:     []string{"responsible service", "failure mode", "slow trace", "healthy trace"},
			unwanted: []string{"diagnosis", "trace grouping"},
		},
		{
			name:     "span_attribute",
			message:  spanAttributeFeedback(true, true, true, false),
			want:     []string{"responsible service", "failure mode", "culprit span", "proof tag"},
			unwanted: []string{"diagnosis", "supporting evidence"},
		},
		{
			name:     "intermittent",
			message:  intermittentFeedback(true, true, false),
			want:     []string{"responsible service", "failure mode", "failing traces"},
			unwanted: []string{"diagnosis", "intermittent failure set"},
		},
		{
			name:     "service_failure_mode_only",
			message:  serviceFailureModeFeedback(true, false),
			want:     []string{"responsible service", "failure mode"},
			unwanted: []string{"issue type"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lower := strings.ToLower(tc.message)
			for _, want := range tc.want {
				if !strings.Contains(lower, want) {
					t.Fatalf("expected %q to mention %q, got %q", tc.name, want, tc.message)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(lower, unwanted) {
					t.Fatalf("expected %q to avoid %q, got %q", tc.name, unwanted, tc.message)
				}
			}
		})
	}
}
