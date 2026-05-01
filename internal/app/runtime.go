package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"cloudtracing/internal/scenario"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const ScenarioHeader = "X-Trace-Lab-Scenario"
const BatchHeader = "X-Trace-Lab-Batch"
const ScenarioAttribute = "lab.scenario_id"
const BatchAttribute = "lab.batch_id"
const startupDependencyTimeout = 2 * time.Minute
const startupDependencyRetryInterval = 2 * time.Second

func ScenarioFile() string {
	if path := os.Getenv("SCENARIO_FILE"); path != "" {
		return path
	}
	return "scenarios/scenarios.json"
}

func LoadScenarios() (map[string]scenario.Definition, error) {
	defs, err := scenario.Load(ScenarioFile())
	if err != nil {
		return nil, err
	}
	return scenario.Index(defs), nil
}

func ScenarioIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(ScenarioHeader))
}

func BatchIDFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(BatchHeader))
}

func AnnotateRequestSpanFromHeaders(ctx context.Context, r *http.Request) {
	AnnotateRequestSpan(ctx, ScenarioIDFromRequest(r), BatchIDFromRequest(r))
}

func AnnotateRequestSpan(ctx context.Context, scenarioID, batchID string) {
	span := trace.SpanFromContext(ctx)
	if scenarioID != "" {
		span.SetAttributes(attribute.String(ScenarioAttribute, scenarioID))
	}
	if batchID != "" {
		span.SetAttributes(attribute.String(BatchAttribute, batchID))
	}
}

func FaultForRequest(index map[string]scenario.Definition, r *http.Request, serviceName string) scenario.FaultSpec {
	if def, ok := scenario.Lookup(index, ScenarioIDFromRequest(r)); ok {
		if fault, found := def.Services[serviceName]; found {
			return fault
		}
	}
	return scenario.FaultSpec{Mode: "baseline", Repeat: 1}
}

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func QuerySpan(ctx context.Context, tracer trace.Tracer, label, stmt string, latency time.Duration, exec func(context.Context) error) error {
	return QuerySpanWithTags(ctx, tracer, label, stmt, latency, nil, exec)
}

func WorkSpan(ctx context.Context, tracer trace.Tracer, label string, tags map[string]string, fn func(context.Context) error) error {
	attrs := StringAttributes(tags)
	ctx, span := tracer.Start(ctx, label, trace.WithAttributes(attrs...))
	defer span.End()

	if err := fn(ctx); err != nil {
		span.RecordError(err)
		return err
	}

	return nil
}

func QuerySpanWithTags(ctx context.Context, tracer trace.Tracer, label, stmt string, latency time.Duration, tags map[string]string, exec func(context.Context) error) error {
	attrs := []attribute.KeyValue{
		attribute.String("db.system", "postgresql"),
		attribute.String("db.statement", stmt),
		attribute.String("lab.query_label", label),
		attribute.String("lab.statement_signature", statementSignature(stmt)),
	}
	attrs = append(attrs, StringAttributes(tags)...)

	ctx, span := tracer.Start(ctx, label, trace.WithAttributes(attrs...))
	defer span.End()

	if latency > 0 {
		time.Sleep(latency)
	}

	if err := exec(ctx); err != nil {
		span.RecordError(err)
		return err
	}

	return nil
}

func StringAttributes(tags map[string]string) []attribute.KeyValue {
	if len(tags) == 0 {
		return nil
	}

	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, attribute.String(key, tags[key]))
	}
	return attrs
}

func statementSignature(stmt string) string {
	fields := strings.Fields(strings.TrimSpace(stmt))
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func OpenPostgres(driverName, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := WaitForDependency(context.Background(), "postgres", startupDependencyTimeout, startupDependencyRetryInterval, func(ctx context.Context) error {
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return db.PingContext(attemptCtx)
	}); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return db, nil
}

func WaitForDependency(ctx context.Context, name string, timeout, interval time.Duration, check func(context.Context) error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		if err := check(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s: %w", name, timeout, lastErr)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s wait canceled: %w", name, ctx.Err())
		case <-time.After(interval):
		}
	}
}
