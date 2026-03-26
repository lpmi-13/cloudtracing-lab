from contextlib import contextmanager
import os

from opentelemetry import propagate, trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import SpanKind


def init_telemetry(service_name: str):
    endpoint = os.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://localhost:4318/v1/traces")
    provider = TracerProvider(
        resource=Resource.create(
            {
                "service.name": service_name,
                "service.version": "0.1.0",
                "deployment.environment": "lab",
            }
        )
    )
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=endpoint)))
    trace.set_tracer_provider(provider)
    return trace.get_tracer(service_name)


@contextmanager
def server_span(tracer, name: str, headers):
    ctx = propagate.extract(headers)
    with tracer.start_as_current_span(name, context=ctx, kind=SpanKind.SERVER) as span:
        yield span


def inject_headers(headers):
    propagate.inject(headers)
    return headers


@contextmanager
def client_span(tracer, name: str, attrs: dict | None = None):
    with tracer.start_as_current_span(name, kind=SpanKind.CLIENT) as span:
        for key, value in (attrs or {}).items():
            span.set_attribute(key, value)
        yield span

