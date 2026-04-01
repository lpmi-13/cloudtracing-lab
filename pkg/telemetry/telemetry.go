package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	exporter, err := otlptracehttp.New(ctx, otlpTraceExporterOptions()...)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String("0.1.0"),
			attribute.String("deployment.environment.name", "lab"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return provider.Shutdown, nil
}

func otlpTraceExporterOptions() []otlptracehttp.Option {
	endpoint, useURL := resolveOTLPTraceEndpoint(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	)
	if useURL {
		return []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint)}
	}
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	}
}

func resolveOTLPTraceEndpoint(tracesEndpoint, baseEndpoint string) (string, bool) {
	if endpoint := strings.TrimSpace(tracesEndpoint); endpoint != "" {
		return endpoint, strings.Contains(endpoint, "://")
	}

	if endpoint := strings.TrimSpace(baseEndpoint); endpoint != "" {
		if strings.Contains(endpoint, "://") {
			return appendTracePath(endpoint), true
		}
		return endpoint, false
	}

	return "localhost:4318", false
}

func appendTracePath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case path == "":
		parsed.Path = "/v1/traces"
	case strings.HasSuffix(path, "/v1/traces"):
		parsed.Path = path
	default:
		parsed.Path = path + "/v1/traces"
	}

	return parsed.String()
}
