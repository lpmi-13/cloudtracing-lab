from contextlib import contextmanager
import os

from opentelemetry import propagate, trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.trace import SpanKind

SCENARIO_HEADER = "X-Trace-Lab-Scenario"
BATCH_HEADER = "X-Trace-Lab-Batch"


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
    provider.add_span_processor(SimpleSpanProcessor(OTLPSpanExporter(endpoint=endpoint)))
    trace.set_tracer_provider(provider)
    return trace.get_tracer(service_name)


@contextmanager
def server_span(tracer, name: str, headers):
    ctx = propagate.extract(headers)
    with tracer.start_as_current_span(name, context=ctx, kind=SpanKind.SERVER) as span:
        scenario_id = (headers.get(SCENARIO_HEADER) or "").strip()
        if scenario_id:
            span.set_attribute("lab.scenario_id", scenario_id)
        batch_id = (headers.get(BATCH_HEADER) or "").strip()
        if batch_id:
            span.set_attribute("lab.batch_id", batch_id)
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


@contextmanager
def work_span(tracer, name: str, attrs: dict | None = None):
    with tracer.start_as_current_span(name) as span:
        for key, value in (attrs or {}).items():
            span.set_attribute(key, value)
        yield span
