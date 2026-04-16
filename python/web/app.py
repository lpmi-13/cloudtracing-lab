import json
import os

import requests
from flask import Flask, render_template_string, request

from python.common.telemetry import init_telemetry, inject_headers, server_span


app = Flask(__name__)
tracer = init_telemetry("shop-web")
EDGE_URL = os.getenv("EDGE_URL", "http://edge-api:8080").rstrip("/")

PAGE = """
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Cloud Tracing Shop</title>
    <style>
      :root {
        --bg: #f5efe2;
        --panel: #fffaf0;
        --ink: #1b1e1f;
        --muted: #5b625c;
        --accent: #0a6b73;
        --border: #d7ccb9;
      }
      body {
        margin: 0;
        font-family: Georgia, "Times New Roman", serif;
        background:
          radial-gradient(circle at top right, rgba(10, 107, 115, 0.12), transparent 35%),
          linear-gradient(180deg, #fbf6eb 0%, #f0e7d8 100%);
        color: var(--ink);
      }
      main {
        max-width: 1040px;
        margin: 0 auto;
        padding: 32px 20px 56px;
      }
      .hero, .panel {
        background: var(--panel);
        border: 1px solid var(--border);
        border-radius: 18px;
        padding: 20px;
        box-shadow: 0 12px 32px rgba(0, 0, 0, 0.06);
      }
      .hero {
        margin-bottom: 18px;
      }
      .grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
        gap: 18px;
      }
      h1, h2 {
        margin-top: 0;
      }
      a, button {
        color: white;
        background: var(--accent);
        border: none;
        padding: 10px 14px;
        border-radius: 999px;
        text-decoration: none;
        cursor: pointer;
        display: inline-block;
      }
      form {
        display: grid;
        gap: 10px;
      }
      input {
        padding: 10px 12px;
        border-radius: 10px;
        border: 1px solid var(--border);
        font: inherit;
      }
      .muted {
        color: var(--muted);
      }
      pre {
        white-space: pre;
        overflow: auto;
        background: #f1eadb;
        padding: 14px;
        border-radius: 12px;
        border: 1px solid var(--border);
        font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
        font-size: 13px;
        line-height: 1.45;
        max-height: 280px;
      }
    </style>
  </head>
  <body>
    <main>
      <section class="hero">
        <h1>Cloud Tracing Shop</h1>
        <p class="muted">A realistic traffic source for the trace lab. The learner UI generates traffic here, and each page fans out into the application tier.</p>
      </section>
      <section class="grid">
        <article class="panel">
          <h2>Search</h2>
          <form method="get" action="/search">
            <input type="text" name="q" value="{{ q or 'trail' }}">
            <input type="hidden" name="scenario" value="{{ scenario }}">
            <button type="submit">Run Search</button>
          </form>
          {% if search_result %}
          <pre>{{ search_result }}</pre>
          {% endif %}
        </article>
        <article class="panel">
          <h2>Checkout</h2>
          <form method="get" action="/checkout">
            <input type="text" name="sku" value="{{ sku or 'sku-14' }}">
            <input type="hidden" name="scenario" value="{{ scenario }}">
            <button type="submit">Checkout Item</button>
          </form>
          {% if checkout_result %}
          <pre>{{ checkout_result }}</pre>
          {% endif %}
        </article>
        <article class="panel">
          <h2>Order History</h2>
          <form method="get" action="/account/orders">
            <input type="text" name="user_id" value="{{ user_id or 'user-4' }}">
            <input type="hidden" name="scenario" value="{{ scenario }}">
            <button type="submit">Load History</button>
          </form>
          {% if history_result %}
          <pre>{{ history_result }}</pre>
          {% endif %}
        </article>
      </section>
    </main>
  </body>
</html>
"""


def edge_headers(scenario_id: str, batch_id: str):
    headers = {}
    if scenario_id:
        headers["X-Trace-Lab-Scenario"] = scenario_id
    if batch_id:
        headers["X-Trace-Lab-Batch"] = batch_id
    return inject_headers(headers)


def format_response_payload(response: requests.Response) -> str:
    try:
        return json.dumps(response.json(), indent=2)
    except ValueError:
        return response.text


@app.route("/")
def home():
    return render_template_string(PAGE, scenario=request.args.get("scenario", ""), q="", sku="", user_id="")


@app.route("/healthz")
def healthz():
    return "ok", 200


@app.route("/search")
def search():
    with server_span(tracer, "GET /search", request.headers):
        scenario_id = request.args.get("scenario", "")
        batch_id = request.headers.get("X-Trace-Lab-Batch", "")
        q = request.args.get("q", "trail")
        response = requests.get(
            f"{EDGE_URL}/api/search",
            params={"q": q},
            headers=edge_headers(scenario_id, batch_id),
            timeout=8,
        )
        return render_template_string(
            PAGE,
            scenario=scenario_id,
            q=q,
            search_result=format_response_payload(response),
            sku="",
            user_id="",
        )


@app.route("/checkout")
def checkout():
    with server_span(tracer, "GET /checkout", request.headers):
        scenario_id = request.args.get("scenario", "")
        batch_id = request.headers.get("X-Trace-Lab-Batch", "")
        sku = request.args.get("sku", "sku-14")
        response = requests.get(
            f"{EDGE_URL}/api/checkout",
            params={"sku": sku},
            headers=edge_headers(scenario_id, batch_id),
            timeout=8,
        )
        return render_template_string(
            PAGE,
            scenario=scenario_id,
            q="",
            sku=sku,
            checkout_result=format_response_payload(response),
            user_id="",
        )


@app.route("/account/orders")
def order_history():
    with server_span(tracer, "GET /account/orders", request.headers):
        scenario_id = request.args.get("scenario", "")
        batch_id = request.headers.get("X-Trace-Lab-Batch", "")
        user_id = request.args.get("user_id", "user-4")
        response = requests.get(
            f"{EDGE_URL}/api/orders/history",
            params={"user_id": user_id},
            headers=edge_headers(scenario_id, batch_id),
            timeout=8,
        )
        return render_template_string(
            PAGE,
            scenario=scenario_id,
            q="",
            sku="",
            user_id=user_id,
            history_result=format_response_payload(response),
        )


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8080)
