# Update Plan: Enhancing the Cloud Tracing Lab

This document captures our analysis and discussion of potential enhancements to the cloud tracing lab, with the goal of making it a more authentic simulation that reflects the needs and learning objectives of teaching distributed trace analysis in complex production systems.

## Current State Assessment

The lab currently provides a solid MVP:
- 4 fault scenarios across 6 services (catalog, inventory, orders, payments, edge, shop-web)
- A coach UI with hints, grading, and randomised scenario cycling
- Header-based fault injection (`X-Trace-Lab-Scenario`)
- Tight feedback loop: generate traffic, inspect in Jaeger, submit answer via dropdowns

### Limitations

- **Low signal-to-noise ratio**: The coach generates ~5 traces, all faulty in the same way. The broken span is immediately obvious.
- **Flat difficulty**: All 4 scenarios have the same cognitive shape — find the one slow service.
- **Underuse of Jaeger UI**: Learners only use the service/operation filter and the waterfall view. The majority of Jaeger's capabilities (tag filtering, duration filtering, trace comparison, dependency graph, span attribute inspection) go untouched.
- **Recognition-only assessment**: Dropdowns test whether the learner can *recognise* the answer from a list, not whether they can *find* it through investigation.
- **Artificial trace volume**: Only 15 traces are stored (configurable, not a hard limit — see below), and all look similar.

---

## Enhancement Directions

We evaluated several directions and narrowed the focus to approaches that keep the UI interaction simple (identification via dropdowns) while making the *investigation* in Jaeger substantially more complex and realistic.

### 1. Denser Trace Landscape

**Goal**: Make the Jaeger search results look more like a real production system.

#### More Services in the Call Graph

Add services that are always present in traces but never the root cause — pure distractors that the learner must consciously rule out:

- **auth-api**: Called on every request. Always completes in ~60-80ms. Adds a span to every trace that looks plausibly suspicious to a novice.
- **notifications-api**: Fires asynchronously after checkout. Adds spans *after* the problematic span, tempting investigation of downstream effects.
- **pricing-api**: Called by catalog-api for dynamic pricing. Deepens the call chain (catalog -> pricing -> redis), adding indirection.

Each additional service means more plausible suspects the learner must rule out by reading span details, not just durations.

#### Concurrent Baseline Traffic

Currently the coach only generates scenario-specific (faulty) traffic. If it also generated clean, non-scenario traffic alongside the faulty traffic, the learner would see a mix of healthy and unhealthy traces in Jaeger's list view. They'd need to:

1. Scan multiple traces to notice only *some* are slow
2. Figure out what the slow ones have in common
3. Drill into the right one

#### Realistic Variance in Healthy Spans

Currently healthy spans are all near-identical in timing. Real services have natural jitter. Adding minor random variance (e.g., 50-200ms on cache lookups) prevents the learner from relying on "anything above X ms is the bug."

### 2. Trace Group Comparison

**Goal**: Teach differential analysis — the core skill of incident response.

#### "Before/After" Scenarios

The coach generates two labelled batches:

- **Group A** ("30 minutes ago"): 5-8 healthy traces, normal latency
- **Group B** ("now"): 5-8 traces showing the fault

The prompt becomes: *"Users started reporting slow checkouts at 14:05. Traffic before 14:00 was normal. Compare the two groups and identify what changed."*

The learner uses Jaeger's built-in trace comparison view (select two traces, view in split pane) or manually compares traces from different time ranges.

#### Subtle Differential Scenarios

Make the comparison non-obvious:
- Group A: checkout total 400ms (catalog=80ms, inventory=150ms, payments=120ms)
- Group B: checkout total 1100ms (catalog=180ms, inventory=580ms, payments=130ms)

Multiple services got slower, but only one is the root cause. The learner must understand baselines and proportional change.

#### "Good Request / Bad Request" Variant

Instead of temporal groups, concurrent traffic where requests for certain inputs succeed and others fail:

*"Some users can check out fine, others time out. What's different about the failing requests?"*

### 3. Red Herrings and Noise

**Goal**: Prevent the learner from jumping to the first slow span they see.

- Inject occasional slow-but-normal spans in healthy services (e.g., a legitimate cache miss that takes 200ms)
- Add background traffic from other "users" creating unrelated traces
- Make some spans slow because they're *waiting on* the actual culprit (symptom, not cause)

### 4. Partial / Intermittent Failures

**Goal**: Teach multi-trace investigation and statistical thinking.

Make faults probabilistic (e.g., 30% of requests hit lock contention). The learner must look at multiple traces to identify the pattern. A single trace might look healthy; only comparing several reveals the intermittent fault.

### 5. Error Propagation (Beyond Latency)

**Goal**: Diversify what "problematic" means.

Currently all scenarios are latency-based. Add:
- HTTP 500 errors that propagate up the call chain (learner traces back to the originating service)
- Partial success (checkout succeeds but payment is "pending" with a warning span)
- Retries that succeed on the 2nd attempt (visible as duplicate spans)

This teaches: not all problems are slow. Some are broken. Jaeger marks error spans with red indicators that the learner must learn to scan for.

---

## Leveraging Untapped Jaeger UI Capabilities

A major finding: the current scenarios exercise only a small fraction of Jaeger's features. Each untapped feature represents a skill we could assess.

### Currently Used

| Feature | Usage |
|---|---|
| Service name filter | Coach tells learner which service to start with |
| Operation name filter | Coach gives focus_operation |
| Waterfall view (span hierarchy) | Learner reads parent-child span tree |
| Span duration bars | Learner visually spots slow spans |

### Currently Unused but Valuable

| Feature | What It Does | Scenario Potential |
|---|---|---|
| **Tag/attribute filter** | Free-text filter, e.g. `db.system=postgresql` | Filter traces by data store type, error status, or custom attributes |
| **Duration filter** (min/max) | Show only traces above/below a threshold | Isolate p99 outliers from a large trace set |
| **Trace comparison view** | Side-by-side structural + timing diff of two traces | Before/after, good request/bad request scenarios |
| **Span detail panel** (attributes) | Shows `db.system`, `db.statement`, span kind, status | Scenarios where two spans have similar duration but only one has a bad query |
| **Span count per service** | Trace header shows "N spans" for each service | N+1 detection via structural analysis, not just timing |
| **Error span indicators** | Red icon on spans with error status | Scenarios where latency is normal but data is wrong |
| **Service dependency graph** (DAG) | Visual topology of service-to-service calls | Orientation task before diving into traces |
| **Lookback / time range** | Select custom time windows | Temporal comparison scenarios |
| **Limit results** | Control how many traces are returned | Required when trace volume exceeds default 20 |

### Jaeger UI Deep Linking

Jaeger supports URL query parameters for pre-filling the search form. The coach can construct links that control what the learner sees on arrival:

| Parameter | Example | Effect |
|---|---|---|
| `service` | `?service=catalog-api` | Pre-selects service |
| `operation` | `&operation=GET%20/internal/search` | Pre-selects operation |
| `limit` | `&limit=100` | Sets result count |
| `lookback` | `&lookback=1h` | Sets time range |
| `minDuration` / `maxDuration` | `&minDuration=500ms` | Filters by span duration |
| `tags` | `&tags=db.system%3Dpostgresql` | Pre-fills tag filter |
| `traceID` | `&traceID=abc123` | Direct trace lookup |
| `start` / `end` | `&lookback=custom&start=...&end=...` | Custom time window (microseconds since epoch) |

This enables a **scaffolding progression**:
- **Early levels**: Coach provides a fully pre-filled Jaeger link (service, operation, limit set) — the learner just reads the waterfall
- **Mid levels**: Coach provides only `?limit=100` — the learner must choose which service and operation to filter on
- **Later levels**: Coach provides a bare `/search` link — the learner must figure out all search parameters themselves

---

## Jaeger In-Memory Storage: No Hard Limit

The current `max_traces: 15` in `k8s/base/jaeger/config.yaml` is an arbitrary configuration value, not a technical limitation. The `MaxTraces` field is a plain Go `int` with a default of 0 (unbounded). Jaeger's own documentation uses 100,000 as an example value. The only constraint is available pod memory.

**Note**: Jaeger UI's search defaults to returning 20 results with no pagination. If `max_traces` is increased beyond 20, learners must manually increase the "Limit Results" field — or the coach can handle this via URL deep linking (`?limit=100`).

**Recommendation**: Increase `max_traces` to at least 50-80 to support mixed healthy/faulty traffic batches with enough volume for filtering exercises to be meaningful.

---

## Proposed Difficulty Progression

Combining the above into a levelled structure. The UI interaction (dropdowns for answer) stays the same across all levels. The investigation complexity scales.

### Level 1: "Find the Slow Service" (current scenarios, refined)

- Coach provides a pre-filled Jaeger link (service, operation, limit)
- 5 traces, all faulty, one clear culprit
- Learner reads waterfall, picks service + issue from dropdowns
- **Jaeger skills tested**: waterfall reading, span hierarchy navigation

### Level 2: "Find the Slow Service With Noise"

- Coach provides a Jaeger link with `?limit=50` only
- 30-50 traces: mix of healthy baseline and faulty traffic
- Additional distractor services (auth-api, pricing-api) present in all traces
- Healthy spans have realistic variance
- **Jaeger skills tested**: service/operation filtering, scanning trace list for outliers, ruling out distractors

### Level 3: "What Changed?" (Trace Comparison)

- Coach provides two Jaeger links: one for "before" time window, one for "after"
- Healthy group vs degraded group, multiple services showing changed timing
- Learner must compare traces to isolate which service actually regressed
- **Jaeger skills tested**: time range filtering, trace comparison view, differential analysis

### Level 4: "Dig Into the Details" (Attribute-Based Diagnosis)

- Coach provides a bare Jaeger link (`/search`)
- Two services show similar elevated latency on DB spans
- Only one is running a pathological query (visible in `db.statement` attribute)
- **Jaeger skills tested**: tag filtering (`db.system=postgresql`), span detail panel, reading `db.statement`, duration filtering

### Level 5: "Find the Intermittent Failure"

- Coach provides `?limit=100`
- 50+ traces, ~30% showing the fault, rest healthy
- Some error spans (not just latency) buried in child services
- **Jaeger skills tested**: duration filtering to isolate outliers, error span identification, multi-trace pattern recognition, span count analysis (N+1 visible as extra spans)

---

## Required Technical Changes (Summary)

| Change | Purpose | Effort |
|---|---|---|
| Increase `max_traces` to 50-80 | Support larger trace volumes | Trivial (config change) |
| Coach generates mixed traffic batches (healthy + faulty) | Create noise and enable comparison | Medium |
| Add 2-3 distractor services (auth, pricing, notifications) | Deepen call graph, add plausible suspects | Medium |
| Add realistic latency variance to healthy spans | Prevent trivial outlier detection | Low |
| Add error-status fault scenarios | Diversify beyond latency-only | Low-Medium |
| Enrich span attributes (`db.rows_affected`, `cache.hit`, `http.status_code`) | Enable attribute-based scenarios | Low |
| Coach constructs deep-linked Jaeger URLs per level | Scaffold Jaeger skill development | Low |
| Add probabilistic fault mode to fault injection | Support intermittent failure scenarios | Low |
| Level/progression tracking in coach UI | Visible progress, unlockable levels | Medium |
| Post-answer explanation panel in coach UI | Contextualise each diagnosis with production relevance | Medium |
