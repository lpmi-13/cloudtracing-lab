import os
import time

import psycopg
from flask import Flask, jsonify, request

from python.common.scenarios import fault_for_headers, load_scenarios
from python.common.telemetry import client_span, init_telemetry, server_span


app = Flask(__name__)
tracer = init_telemetry("payments-api")
scenarios = load_scenarios()
dsn = os.getenv("POSTGRES_DSN")


def connection():
    if not dsn:
        raise RuntimeError("POSTGRES_DSN is required")
    return psycopg.connect(dsn)


@app.route("/healthz")
def healthz():
    return "ok", 200


@app.route("/internal/charge")
def charge():
    with server_span(tracer, "GET /internal/charge", request.headers):
        sku = request.args.get("sku", "sku-14")
        amount = float(request.args.get("amount", "49.95"))
        order_ref = f"pay-{sku}-{int(time.time() * 1000)}"
        fault = fault_for_headers(request.headers, scenarios, "payments-api")

        try:
            with connection() as conn:
                with conn.cursor() as cursor:
                    if fault.get("mode") == "lock_wait_timeout":
                        with client_span(
                            tracer,
                            fault.get("query_label", "payments.idempotency.lock_wait"),
                            {
                                "db.system": "postgresql",
                                "db.statement": fault.get("query_text", "select pg_sleep(1.2)"),
                                "lab.query_label": fault.get("query_label", "payments.idempotency.lock_wait"),
                                "lab.statement_signature": "select pg_sleep(%s)",
                            },
                        ) as span:
                            cursor.execute("select pg_sleep(%s)", (fault.get("latency_ms", 1200) / 1000.0,))
                            span.set_attribute("lab.failure_mode", "lock_wait_timeout")
                        return jsonify(
                            {
                                "status": "failed",
                                "reference": order_ref,
                                "error": "payment authorization timed out while waiting on a database lock",
                            }
                        ), 504

                    with client_span(
                        tracer,
                        "payments.capture.insert",
                        {
                            "db.system": "postgresql",
                            "db.statement": "insert into payment_attempts (order_ref, amount, status) values ($1, $2, $3)",
                            "lab.query_label": "payments.capture.insert",
                            "lab.statement_signature": "insert into payment_attempts (order_ref, amount, status) values ($1, $2, $3)",
                        },
                    ):
                        cursor.execute(
                            "insert into payment_attempts (order_ref, amount, status) values (%s, %s, %s)",
                            (order_ref, amount, "captured"),
                        )
                    conn.commit()
        except Exception as exc:
            return jsonify({"status": "failed", "reference": order_ref, "error": str(exc)}), 500

        return jsonify({"status": "captured", "reference": order_ref, "sku": sku})


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8080)
