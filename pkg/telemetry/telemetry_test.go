package telemetry

import "testing"

func TestResolveOTLPTraceEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		tracesEndpoint string
		baseEndpoint   string
		wantEndpoint   string
		wantUseURL     bool
	}{
		{
			name:         "default",
			wantEndpoint: "localhost:4318",
		},
		{
			name:           "traces endpoint url",
			tracesEndpoint: "http://collector:4318/v1/traces",
			wantEndpoint:   "http://collector:4318/v1/traces",
			wantUseURL:     true,
		},
		{
			name:           "traces endpoint bare host",
			tracesEndpoint: "collector:4318",
			wantEndpoint:   "collector:4318",
		},
		{
			name:         "base endpoint bare host",
			baseEndpoint: "collector:4318",
			wantEndpoint: "collector:4318",
		},
		{
			name:         "base endpoint url adds trace path",
			baseEndpoint: "http://collector:4318",
			wantEndpoint: "http://collector:4318/v1/traces",
			wantUseURL:   true,
		},
		{
			name:         "base endpoint url preserves prefix",
			baseEndpoint: "http://collector:4318/prefix",
			wantEndpoint: "http://collector:4318/prefix/v1/traces",
			wantUseURL:   true,
		},
		{
			name:         "base endpoint url preserves trace path",
			baseEndpoint: "http://collector:4318/prefix/v1/traces",
			wantEndpoint: "http://collector:4318/prefix/v1/traces",
			wantUseURL:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEndpoint, gotUseURL := resolveOTLPTraceEndpoint(tt.tracesEndpoint, tt.baseEndpoint)
			if gotEndpoint != tt.wantEndpoint {
				t.Fatalf("endpoint: want %q, got %q", tt.wantEndpoint, gotEndpoint)
			}
			if gotUseURL != tt.wantUseURL {
				t.Fatalf("useURL: want %v, got %v", tt.wantUseURL, gotUseURL)
			}
		})
	}
}
