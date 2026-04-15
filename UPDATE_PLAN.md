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

We evaluated several directions and narrowed the focus to approaches that keep the UI interaction simple and deterministic while making the *investigation* in Jaeger substantially more complex and realistic. For now, learner submissions should be limited to selecting known entities in the UI: service IDs, issue types, trace IDs, span IDs, or a small coordinated set of those values.

### 1. Denser Trace Landscape

**Learning objective focus**: Make the learner distinguish signal from noise in a Jaeger result set that looks more like a real production system.

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

**Learning objective focus**: Teach differential analysis — the core skill of incident response.

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

**Learning objective focus**: Prevent the learner from jumping to the first slow span they see.

- Inject occasional slow-but-normal spans in healthy services (e.g., a legitimate cache miss that takes 200ms)
- Add background traffic from other "users" creating unrelated traces
- Make some spans slow because they're *waiting on* the actual culprit (symptom, not cause)

### 4. Partial / Intermittent Failures

**Learning objective focus**: Teach multi-trace investigation and statistical thinking.

Make faults probabilistic (e.g., 30% of requests hit lock contention). The learner must look at multiple traces to identify the pattern. A single trace might look healthy; only comparing several reveals the intermittent fault.

### 5. Error Propagation (Beyond Latency)

**Learning objective focus**: Diversify what "problematic" means.

Currently all scenarios are latency-based. Add:
- HTTP 500 errors that propagate up the call chain (learner traces back to the originating service)
- Partial success (checkout succeeds but payment is "pending" with a warning span)
- Retries that succeed on the 2nd attempt (visible as duplicate spans)

This teaches: not all problems are slow. Some are broken. Jaeger marks error spans with red indicators that the learner must learn to scan for.

---

## Assessment and Learning Objective Model

The current document is strongest as a scenario roadmap. To make it a stronger learning plan, each level should also define a deterministic assessment contract that the coach UI can score without interpreting free text.

For now, the plan should satisfy the **specific**, **measurable**, **achievable**, and **relevant** parts of SMART. Time-bounded mastery can be added later once the UI and instrumentation are mature enough to support it cleanly.

### Assessment Principles

| Principle | Implication for the coach UI |
|---|---|
| Deterministic evidence only | Every graded input must be a known value or known set: service, issue type, trace ID, span ID, attribute key, attribute identifier, or a fixed multi-select set |
| Assess process, not just the final answer | Each level should require one or more intermediate selections that prove the learner used the intended Jaeger skill |
| Immediate targeted feedback | The coach should distinguish partial success states such as "correct service, wrong span" or "correct after-trace, wrong before-trace" |
| Repetition with variation | Mastery should require repeated success across multiple seeded variants of the same skill, not a single lucky attempt |
| Stable answer keys | Scenario generation should emit an answer key containing acceptable trace IDs, span IDs, service IDs, and any acceptable supporting identifiers |
| No free-text grading | If a scenario needs supporting evidence, the learner should select it from constrained UI affordances rather than type an explanation |

### Deterministic Evidence Types

- Service ID plus issue type
- Trace ID
- Span ID
- Attribute key selected from a constrained list
- Attribute identifier selected from a constrained list or represented by a stable semantic tag such as a query fingerprint ID
- Coordinated multi-select sets such as "all failing traces in this candidate list" or "one healthy trace and one degraded trace"

### Feedback and Mastery Model

- Each level should expose a narrow target skill, then score the learner on both the final diagnosis and the evidence used to support it
- A learner should not advance on a single success; every level should require **5 correct attempts** before the next level unlocks
- The coach UI should always show a visible multi-level progression, with each level displaying its current mastery count out of 5 and its lock/unlock state
- Hints and deep-linked Jaeger URLs should be treated as scaffolding that can be reduced over time without changing the deterministic scoring model

### Progress Tracking Implementation

- Learner progress should be tracked in **server-side in-memory state** owned by the coach process
- The server should be the single source of truth for the active level, current mastery counts, and unlock state; the browser should not hold authoritative progress
- Progress does **not** need to survive a process restart; restarting the coach can reset the learner state to the initial level
- Because all browsers and tabs should show the **exact same state**, every client should subscribe to the same shared server-side session state and refresh when that state changes
- Prefer a push-based update mechanism such as server-sent events or websockets over per-tab local storage so scenario state and progression stay synchronized everywhere

### Level Navigation and Unlocking

- Any level that is already unlocked should remain selectable at any time; learners should be free to jump between all unlocked levels without losing prior mastery on those levels
- Selecting an unlocked level should load that level's current challenge state and allow the learner to generate fresh scenario variants in that level for as long as they want
- Unlocked levels should support effectively unlimited replay; the system should keep generating new challenges within the selected level rather than exhausting the level after mastery is reached
- Locked levels should remain visible in the progression UI but disabled and not directly selectable
- Unlocking should stay strictly sequential: level `N+1` becomes selectable only when level `N` is the selected level and the UI is showing that level as mastered at `5/5`
- Mastery on earlier unlocked levels must not skip over an intervening locked level; the learner has to select the level immediately before the locked one to unlock the next step in the sequence

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

Combining the above into a levelled structure. The core investigation happens in Jaeger, but each level also defines deterministic evidence that the learner must submit through the coach UI. The investigation complexity scales while the scoring model remains simple and predictable.

### Level 1: "Find the Slow Service" (current scenarios, refined)

- **Objective**: Given a pre-filtered faulty trace set, identify the culprit service and the specific span that demonstrates the issue
- Coach provides a pre-filled Jaeger link (service, operation, limit)
- 5 traces, all faulty, one clear culprit
- **Learner submits**: service ID, issue type, one trace ID, one span ID
- **Pass condition**: selected service, issue, trace, and span all match the scenario answer key
- **Mastery gate**: **5 correct attempts** across multiple variants of the same scenario shape
- **Jaeger skills tested**: waterfall reading, span hierarchy navigation

### Level 2: "Find the Slow Service With Noise"

- **Objective**: Distinguish faulty traces from healthy noise and identify the real culprit rather than a distractor
- Coach provides a Jaeger link with `?limit=50` only
- 30-50 traces: mix of healthy baseline and faulty traffic
- Additional distractor services (auth-api, pricing-api) present in all traces
- Healthy spans have realistic variance
- **Learner submits**: culprit service ID, issue type, two trace IDs believed to be faulty, and one trace ID believed to be healthy
- **Pass condition**: the selected faulty traces belong to the degraded set, the selected healthy trace belongs to the baseline set, and the diagnosis is correct
- **Mastery gate**: **5 correct attempts** across variants with different distractor patterns and different healthy/faulty mixes
- **Jaeger skills tested**: service/operation filtering, scanning trace list for outliers, ruling out distractors

### Level 3: "What Changed?" (Trace Comparison)

- **Objective**: Compare healthy and degraded traffic to identify which service actually regressed
- Coach provides two Jaeger links: one for "before" time window, one for "after"
- Healthy group vs degraded group, multiple services showing changed timing
- **Learner submits**: one "before" trace ID, one "after" trace ID, culprit service ID, issue type
- **Pass condition**: both trace IDs come from the correct groups and the culprit service and issue match the answer key
- **Mastery gate**: **5 correct attempts** across variants where symptom services differ from the true root cause
- **Jaeger skills tested**: time range filtering, trace comparison view, differential analysis

### Level 4: "Dig Into the Details" (Attribute-Based Diagnosis)

- **Objective**: Use span attributes to disambiguate two plausible culprits that look similar in the waterfall
- Coach provides a bare Jaeger link (`/search`)
- Two services show similar elevated latency on DB spans
- Only one is running a pathological query or issuing a distinct failure pattern
- **Learner submits**: culprit service ID, culprit span ID, supporting attribute key, supporting attribute identifier
- **Pass condition**: the selected span is correct and the supporting attribute selection matches one of the accepted identifiers in the scenario answer key
- **Implementation note**: prefer stable semantic identifiers such as query fingerprints or retry-cause IDs over raw free-text values so the UI can grade deterministically
- **Mastery gate**: **5 correct attempts** across variants that require different attribute-based clues
- **Jaeger skills tested**: tag filtering (`db.system=postgresql`), span detail panel, reading attributes, duration filtering

### Level 5: "Find the Intermittent Failure"

- **Objective**: Identify an intermittent pattern across multiple traces and connect it to the underlying service and failure mode
- Coach provides `?limit=100`
- 50+ traces, ~30% showing the fault, rest healthy
- Some error spans (not just latency) buried in child services
- **Learner submits**: the full set of failing trace IDs from a fixed candidate subset, culprit service ID, issue type
- **Pass condition**: the selected trace set matches the expected failing subset and the diagnosis is correct
- **Mastery gate**: **5 correct attempts** across variants with different failure rates and different intermittent mechanisms
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
| Add stable semantic identifiers for graded evidence (`query_fingerprint`, retry-cause ID, failure-mode ID) | Allow deterministic grading without free-text parsing | Low-Medium |
| Coach constructs deep-linked Jaeger URLs per level | Scaffold Jaeger skill development | Low |
| Add probabilistic fault mode to fault injection | Support intermittent failure scenarios | Low |
| Scenario generator emits a deterministic answer key per variant | Map each run to acceptable service IDs, trace IDs, span IDs, and supporting identifiers | Medium |
| Coach UI adds constrained selectors for trace IDs, span IDs, and coordinated multi-select tasks | Let learners submit evidence, not just final diagnoses | Medium |
| Partial-credit feedback states in coach UI | Distinguish incorrect diagnosis from incorrect evidence selection | Medium |
| Level/progression tracking in coach UI | Visible five-level progression, per-level `x/5` mastery counts, and unlockable levels | Medium |
| Mastery tracking based on repeated correct completions per level | Enforce the `5 correct attempts` gate without adding time-based scoring | Medium |
| Shared in-memory learner session in coach | Keep the exact same scenario and progression state across browsers and tabs without persisting across restarts | Medium |
| Level navigation across unlocked levels | Let learners revisit any unlocked level, replay it indefinitely, and unlock only the next sequential level from the currently selected mastered level | Medium |
| Post-answer explanation panel in coach UI | Contextualise each diagnosis with production relevance and explain which evidence mattered | Medium |

---

## Implementation Backlog

The current implementation now has the **progression shell** in place, but it still falls short of the learning-design contract in several ways:

- grading still evaluates only the final diagnosis rather than the evidence used to reach it
- most levels do not yet have multiple true variants of the same skill
- later levels are not yet differentiated enough in scaffolding, trace landscape, or assessment type
- feedback is still mostly binary rather than targeted and diagnostic

The backlog below is ordered to bring the implementation into line with both **deliberate-practice principles** and **SMART learning-objective assessment**, excluding time-bounded scoring for now.

### Phase 1: Level Contracts and Assessment Schema

**Goal**: make each level a concrete learning contract rather than just a label in the UI.

Deliverables:

- Define a level-spec record for each level containing:
  - target skill
  - learner-facing objective
  - required evidence fields
  - pass condition
  - partial-credit states
  - mastery rule
- Extend the scenario schema to support deterministic assessment:
  - `variant_group`
  - `assessment_type`
  - `answer_key`
  - accepted trace IDs
  - accepted span IDs
  - accepted attribute keys
  - accepted attribute identifiers
  - fixed candidate trace sets where needed
  - feedback mappings for partial-credit responses
- Validate at load time that every scenario is assigned to a level and contains the fields required by that level's assessment type

Acceptance criteria:

- Every level has a written objective that is specific, measurable, achievable, and relevant
- Every level's scoring inputs are explicit and machine-checkable
- The coach can reject malformed or incomplete scenario definitions at startup

### Phase 2: Real Within-Level Variation

**Goal**: turn the `5 correct attempts` gate into repeated deliberate practice instead of repetition of the exact same activity.

Deliverables:

- Add at least `3-5` real variants per level before using the mastery gate as evidence of skill
- Ensure variants within a level preserve the same target skill while changing:
  - route or scenario framing
  - distractor pattern
  - trace IDs and span IDs
  - healthy/faulty mix
  - supporting attribute identifiers
- Update challenge selection so "new challenge" picks a different variant group whenever possible, not just the same scenario reseeded

Acceptance criteria:

- A learner can complete five correct attempts in a level without seeing the same exact assessment contract every time
- Mastery reflects repeated success across varied but equivalent tasks

### Phase 3: Dynamic Assessment UI

**Goal**: capture the evidence required by each level instead of relying on a single fixed two-dropdown form.

Deliverables:

- Replace the fixed service/issue form with a level-driven evidence form
- Implement level-specific submission affordances:
  - Level 1: service ID, issue type, trace ID, span ID
  - Level 2: service ID, issue type, two faulty traces, one healthy trace
  - Level 3: before trace, after trace, culprit service, issue type
  - Level 4: culprit service, culprit span, supporting attribute key, supporting attribute identifier
  - Level 5: failing trace set from a fixed candidate subset, culprit service, issue type
- Ensure the UI presents only constrained selections; no free-text grading paths
- Show the learner exactly what evidence is required for the currently selected level

Acceptance criteria:

- The UI fields change by level according to the assessment contract
- The learner can only submit deterministic, known values
- The assessment inputs prove process, not just the final conclusion

### Phase 4: Level-Specific Grading and Partial Credit

**Goal**: make feedback immediate, targeted, and instructionally useful.

Deliverables:

- Replace binary grading with level-specific grading logic
- Add partial-credit states such as:
  - correct service, wrong issue
  - correct diagnosis, wrong trace
  - correct trace group, wrong culprit
  - correct span, wrong supporting attribute
  - partially correct intermittent set
- Return structured grading results that include:
  - pass/fail
  - mastery increment or not
  - targeted feedback message
  - explanation of which evidence was right or wrong
- Unlock the next level only when a submission satisfies the full pass condition for the selected level

Acceptance criteria:

- Wrong answers tell the learner what part of the investigation was incorrect
- Correct answers confirm both the diagnosis and the supporting evidence that mattered
- Mastery advances only on full, level-appropriate success

### Phase 5: Scaffolding Progression

**Goal**: make later levels more independent and cognitively demanding while keeping the grading deterministic.

Deliverables:

- Reduce deep-linking and coach guidance by level:
  - Level 1: fully scaffolded Jaeger entry point
  - Level 2: lighter filtering guidance
  - Level 3: paired comparison links or windows
  - Level 4: bare search with constrained evidence capture
  - Level 5: minimal coach guidance
- Make hints level-aware rather than globally uniform
- Ensure level descriptions, hint strategy, and grading all point at the same target skill

Acceptance criteria:

- Earlier levels provide more direction than later levels
- Later levels require more independent Jaeger navigation without reverting to free-text assessment

### Phase 6: Trace-Landscape and Fault-Injection Support

**Goal**: generate trace environments that actually test the named skill for each level.

Deliverables:

- Add mixed healthy/faulty traffic generation for noise-based levels
- Add fixed candidate subsets for trace-selection tasks
- Add before/after time-window generation for comparison tasks
- Add stable semantic identifiers for attribute-based diagnosis
- Add intermittent or probabilistic fault generation for pattern-recognition tasks
- Increase or tune trace counts so later levels expose enough data to make filtering and comparison meaningful

Acceptance criteria:

- Each level's trace landscape matches its stated learning objective
- The learner is forced to use the intended Jaeger skill, not just spot an obvious slow span

### Phase 7: Pedagogical Guardrails in Tests

**Goal**: make the learning-design contract enforceable in code review and CI.

Deliverables:

- Add tests that verify:
  - every level has multiple variants
  - every level has a non-empty assessment contract
  - every level renders the correct evidence fields
  - grading returns partial-credit states where expected
  - unlocking requires a full pass, not a partial pass
  - later levels expose less scaffolding than earlier levels
- Add schema-validation tests for scenario files so missing answer-key fields fail fast

Acceptance criteria:

- Future changes cannot silently collapse a level back to binary diagnosis-only grading
- The learning-design assumptions are encoded as tests, not just prose

### Recommended Build Order

1. Implement level contracts and the extended assessment schema.
2. Add true within-level variation.
3. Ship Level 1 end to end with evidence capture and partial-credit grading.
4. Ship Level 2 and Level 3 with their distinct trace landscapes and assessment forms.
5. Ship Level 4 and Level 5 with attribute-based and intermittent-pattern evidence models.
6. Tighten scaffolding progression and add pedagogical guardrail tests.

### Definition of Done

The implementation should be considered aligned with deliberate-practice and SMART assessment principles when all of the following are true:

- each level has a narrow, explicit learning objective that is specific, measurable, achievable, and relevant
- each objective is assessed through deterministic evidence rather than coach inference
- mastery requires repeated success across multiple variants of the same skill
- feedback tells the learner what part of the investigation was right or wrong
- the level label, trace landscape, scaffolding, UI, and grader all describe the same target skill
- the next level unlocks only after full mastery of the selected preceding level
