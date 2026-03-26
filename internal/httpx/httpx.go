package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func DoJSON(ctx context.Context, client *http.Client, method, target string, body io.Reader, scenarioID string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if scenarioID != "" {
		req.Header.Set("X-Trace-Lab-Scenario", scenarioID)
	}

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, strings.TrimSpace(target), err)
	}

	return resp.StatusCode, nil
}
