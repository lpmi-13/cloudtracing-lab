package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
)

type coachServer struct {
	client      *http.Client
	jaegerURL   string
	webURL      string
	scenarios   map[string]scenario.Definition
	scenarioSet []scenario.Definition
	page        *template.Template
	mu          sync.RWMutex
	current     scenario.Definition
	prepared    bool
}

type publicScenario struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Objective      string `json:"objective"`
	Prompt         string `json:"prompt"`
	Hint1          string `json:"hint_1"`
	Hint2          string `json:"hint_2"`
	Route          string `json:"route"`
	TrafficPath    string `json:"traffic_path"`
	FocusService   string `json:"focus_service"`
	FocusOperation string `json:"focus_operation"`
}

type trafficRequest struct {
	ScenarioID string `json:"scenario_id"`
	Count      int    `json:"count"`
}

type gradeRequest struct {
	ScenarioID       string `json:"scenario_id"`
	SuspectedService string `json:"suspected_service"`
	SuspectedIssue   string `json:"suspected_issue"`
}

// Seed every distinct learner-facing route on each iteration so the active scenario
// is not obvious from a single operation appearing in Jaeger.
const defaultTraceBatchSize = 5

//go:embed favicon.ico
var coachFavicon []byte

func main() {
	scenarios, err := app.LoadScenarios()
	if err != nil {
		log.Fatalf("load scenarios: %v", err)
	}

	scenarioSet := make([]scenario.Definition, 0, len(scenarios))
	for _, def := range scenarios {
		scenarioSet = append(scenarioSet, def)
	}

	s := &coachServer{
		client:      &http.Client{Timeout: 10 * time.Second},
		jaegerURL:   strings.TrimRight(defaultEnv("JAEGER_UI_URL", ""), "/"),
		webURL:      strings.TrimRight(defaultEnv("WEB_URL", "http://shop-web:8080"), "/"),
		scenarios:   scenarios,
		scenarioSet: scenarioSet,
		page:        template.Must(template.New("page").Parse(pageTemplate)),
		current:     scenarioSet[0],
	}
	s.current = s.pickRandom("")

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(s.index))
	mux.Handle("/api/scenarios/random", http.HandlerFunc(s.randomScenario))
	mux.Handle("/api/traffic", http.HandlerFunc(s.generateTraffic))
	mux.Handle("/api/grade", http.HandlerFunc(s.grade))
	mux.Handle("/favicon.ico", http.HandlerFunc(serveFavicon))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	addr := ":8080"
	log.Printf("coach listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *coachServer) index(w http.ResponseWriter, r *http.Request) {
	selected, generated, err := s.prepareCurrentScenario(r.Context(), defaultTraceBatchSize)
	payload, _ := json.Marshal(s.toPublic(selected))
	feedback := focusTraceFeedback(selected)
	if err != nil {
		feedback = "The current scenario is loaded, but automatic trace generation failed. Refresh the page and try again."
	} else if generated > 0 {
		feedback = fmt.Sprintf("Generated %s across the shop endpoints. %s", freshTraceText(generated), focusTraceFeedback(selected))
	}

	data := map[string]any{
		"InitialScenario": template.JS(payload),
		"InitialFeedback": feedback,
		"JaegerURL":       s.jaegerURL,
	}
	if err := s.page.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *coachServer) randomScenario(w http.ResponseWriter, r *http.Request) {
	def, _, err := s.advanceScenario(r.Context(), r.URL.Query().Get("exclude"), defaultTraceBatchSize)
	if err != nil {
		http.Error(w, "failed to prepare the next scenario", http.StatusServiceUnavailable)
		return
	}

	app.WriteJSON(w, http.StatusOK, s.toPublic(def))
}

func (s *coachServer) generateTraffic(w http.ResponseWriter, r *http.Request) {
	var req trafficRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	def, ok := s.scenarios[req.ScenarioID]
	if !ok {
		http.Error(w, "unknown scenario", http.StatusNotFound)
		return
	}

	successes, err := s.generateScenarioTraffic(r.Context(), def, req.Count)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.markPreparedIfCurrent(def.ID)

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"generated": successes,
		"target":    s.webURL + def.TrafficPath,
	})
}

func (s *coachServer) grade(w http.ResponseWriter, r *http.Request) {
	var req gradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	def, ok := s.scenarios[req.ScenarioID]
	if !ok {
		http.Error(w, "unknown scenario", http.StatusNotFound)
		return
	}

	correct := req.SuspectedService == def.ExpectedService && req.SuspectedIssue == def.ExpectedIssue
	if !correct {
		app.WriteJSON(w, http.StatusOK, map[string]any{
			"correct":  false,
			"feedback": "Incorrect answer, please try again",
		})
		return
	}

	next, generated, err := s.advanceScenario(r.Context(), def.ID, defaultTraceBatchSize)
	if err != nil {
		app.WriteJSON(w, http.StatusOK, map[string]any{
			"correct":  true,
			"feedback": fmt.Sprintf("Correct. The culprit was %s. The next scenario could not be prepared yet, so the current activity stayed in place.", def.Answer),
		})
		return
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"correct":       true,
		"feedback":      fmt.Sprintf("Correct.\n\nThe culprit was %s.\n\nLoaded the next scenario and prepared %s across the shop endpoints.", def.Answer, freshTraceText(generated)),
		"next_scenario": s.toPublic(next),
	})
}

func (s *coachServer) currentScenario() (scenario.Definition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.prepared
}

func (s *coachServer) setCurrentScenario(def scenario.Definition, prepared bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = def
	s.prepared = prepared
}

func (s *coachServer) markPreparedIfCurrent(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.ID == id {
		s.prepared = true
	}
}

func (s *coachServer) prepareCurrentScenario(ctx context.Context, count int) (scenario.Definition, int, error) {
	def, prepared := s.currentScenario()
	if prepared {
		return def, 0, nil
	}

	generated, err := s.generateScenarioTraffic(ctx, def, count)
	if err != nil {
		return def, 0, err
	}
	s.markPreparedIfCurrent(def.ID)
	return def, generated, nil
}

func (s *coachServer) advanceScenario(ctx context.Context, exclude string, count int) (scenario.Definition, int, error) {
	next := s.pickRandom(exclude)
	generated, err := s.generateScenarioTraffic(ctx, next, count)
	if err != nil {
		return scenario.Definition{}, 0, err
	}

	s.setCurrentScenario(next, true)
	return next, generated, nil
}

func (s *coachServer) generateScenarioTraffic(ctx context.Context, def scenario.Definition, count int) (int, error) {
	if count <= 0 {
		count = defaultTraceBatchSize
	}

	var firstErr error
	var successes int
	for _, trafficPath := range s.trafficPathsForScenario(def) {
		pathSuccesses, err := s.generateTrafficBatch(ctx, def, trafficPath, count)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		successes += pathSuccesses
	}

	if successes == 0 && firstErr != nil {
		return 0, firstErr
	}

	return successes, nil
}

func (s *coachServer) generateTrafficBatch(ctx context.Context, def scenario.Definition, trafficPath string, count int) (int, error) {
	target := s.webURL + trafficPath
	separator := "&"
	if !strings.Contains(trafficPath, "?") {
		separator = "?"
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		successes int
	)

	wg.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target+separator+"scenario="+def.ID, nil)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			httpReq.Header.Set(app.ScenarioHeader, def.ID)

			resp, err := s.client.Do(httpReq)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			resp.Body.Close()

			mu.Lock()
			successes++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if successes == 0 && firstErr != nil {
		return 0, firstErr
	}
	return successes, nil
}

func (s *coachServer) trafficPathsForScenario(def scenario.Definition) []string {
	pathsByRoute := map[string]string{}
	otherRoutes := make([]string, 0, len(s.scenarioSet))

	for _, candidate := range s.scenarioSet {
		if candidate.Route == "" || candidate.TrafficPath == "" {
			continue
		}
		if candidate.Route == def.Route {
			pathsByRoute[candidate.Route] = def.TrafficPath
			continue
		}

		existing, ok := pathsByRoute[candidate.Route]
		if !ok {
			otherRoutes = append(otherRoutes, candidate.Route)
			pathsByRoute[candidate.Route] = candidate.TrafficPath
			continue
		}
		if candidate.TrafficPath < existing {
			pathsByRoute[candidate.Route] = candidate.TrafficPath
		}
	}

	sort.Strings(otherRoutes)

	paths := make([]string, 0, len(otherRoutes)+1)
	for _, route := range otherRoutes {
		paths = append(paths, pathsByRoute[route])
	}
	paths = append(paths, def.TrafficPath)
	return paths
}

func (s *coachServer) pickRandom(exclude string) scenario.Definition {
	filtered := make([]scenario.Definition, 0, len(s.scenarioSet))
	for _, def := range s.scenarioSet {
		if def.ID == exclude && len(s.scenarioSet) > 1 {
			continue
		}
		filtered = append(filtered, def)
	}
	if len(filtered) == 0 {
		return s.scenarioSet[0]
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return filtered[rng.Intn(len(filtered))]
}

func (s *coachServer) toPublic(def scenario.Definition) publicScenario {
	return publicScenario{
		ID:             def.ID,
		Title:          def.Title,
		Objective:      def.Objective,
		Prompt:         def.Prompt,
		Hint1:          def.Hint1,
		Hint2:          def.Hint2,
		Route:          def.Route,
		TrafficPath:    def.TrafficPath,
		FocusService:   def.FocusService,
		FocusOperation: def.FocusOperation,
	}
}

func defaultEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func freshTraceText(count int) string {
	if count == 1 {
		return "1 fresh trace"
	}
	return fmt.Sprintf("%d fresh traces", count)
}

func focusTraceFeedback(def scenario.Definition) string {
	return fmt.Sprintf("In Jaeger, filter to service %s and operation %s, then open the newest trace.", def.FocusService, def.FocusOperation)
}

func serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
	http.ServeContent(w, r, "favicon.ico", time.Time{}, bytes.NewReader(coachFavicon))
}

const pageTemplate = `
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Trace Coach</title>
    <meta name="description" content="Fresh traces are generated automatically for every scenario, so the learner can go straight into Jaeger, diagnose the real culprit, and keep looping with immediate feedback.">
    <link rel="icon" href="/favicon.ico">
    <style>
      :root {
        --bg: #efe7d4;
        --panel: #fff9ef;
        --ink: #1f2120;
        --muted: #5d635d;
        --accent: #a53d24;
        --accent-soft: #f5d8c6;
        --success: #215f3c;
        --border: #d3c2ae;
      }
      html {
        min-height: 100%;
        background: #e8dcc6;
      }
      body {
        margin: 0;
        min-height: 100vh;
        font-family: "Iowan Old Style", Georgia, serif;
        background-color: var(--bg);
        background-image:
          radial-gradient(circle at top left, rgba(165, 61, 36, 0.12), transparent 28%),
          linear-gradient(180deg, #f5ecdd 0%, #e8dcc6 100%);
        background-repeat: no-repeat;
        background-size: 100% 100%;
        color: var(--ink);
      }
      main {
        max-width: 1100px;
        margin: 0 auto;
        padding: 28px 20px 48px;
      }
      .stack {
        display: grid;
        gap: 18px;
      }
      .panel {
        background: var(--panel);
        border: 1px solid var(--border);
        border-radius: 20px;
        padding: 20px;
        box-shadow: 0 12px 28px rgba(0, 0, 0, 0.06);
      }
      .grid {
        display: grid;
        grid-template-columns: 1.4fr 1fr;
        gap: 18px;
      }
      @media (max-width: 840px) {
        .grid { grid-template-columns: 1fr; }
      }
      h1, h2, h3 { margin-top: 0; }
      p { line-height: 1.45; }
      .muted { color: var(--muted); }
      .badge {
        display: inline-block;
        padding: 6px 10px;
        border-radius: 999px;
        background: var(--accent-soft);
        margin-right: 8px;
        margin-bottom: 8px;
      }
      button, select, a.button {
        font: inherit;
      }
      button, a.button {
        background: var(--accent);
        color: white;
        border: none;
        border-radius: 999px;
        padding: 10px 14px;
        cursor: pointer;
        text-decoration: none;
      }
      button:disabled {
        opacity: 0.6;
        cursor: default;
      }
      select {
        width: 100%;
        padding: 10px 12px;
        border-radius: 12px;
        border: 1px solid var(--border);
        margin-bottom: 10px;
        background: white;
      }
      #feedback {
        min-height: 48px;
        padding: 12px 14px;
        border-radius: 14px;
        background: #f3ede3;
        white-space: pre-line;
      }
      #feedback.ok {
        background: #dceedd;
        color: var(--success);
      }
      .hint-shell {
        display: grid;
        grid-template-rows: 0fr;
        opacity: 0;
        overflow: hidden;
        transform: translateY(-8px);
        transition:
          grid-template-rows 280ms cubic-bezier(0.2, 0.8, 0.2, 1),
          opacity 220ms ease,
          transform 280ms cubic-bezier(0.2, 0.8, 0.2, 1);
      }
      .hint-shell.open {
        grid-template-rows: 1fr;
        opacity: 1;
        transform: translateY(0);
        transition-delay: 90ms;
      }
      .hint-shell > * {
        min-height: 0;
      }
      #hint-box {
        min-height: 72px;
        padding: 12px 14px;
        border-radius: 14px;
        background: #f3ede3;
        white-space: pre-line;
      }
      .actions {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
      }
      .hint-actions {
        margin-top: 10px;
      }
      code {
        background: #f3ead8;
        padding: 2px 6px;
        border-radius: 8px;
      }
      #busy-overlay {
        position: fixed;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        background: rgba(31, 33, 32, 0.34);
        backdrop-filter: blur(3px);
        z-index: 1000;
      }
      #busy-overlay.hidden {
        display: none;
      }
      .modal {
        min-width: min(320px, calc(100vw - 40px));
        max-width: 420px;
        text-align: center;
      }
      .spinner {
        width: 34px;
        height: 34px;
        margin: 0 auto 14px;
        border-radius: 50%;
        border: 3px solid rgba(165, 61, 36, 0.18);
        border-top-color: var(--accent);
        animation: spin 0.85s linear infinite;
      }
      @keyframes spin {
        to { transform: rotate(360deg); }
      }
    </style>
  </head>
  <body>
    <main class="stack">
      <section class="grid">
        <article class="panel stack">
          <div>
            <div class="badge">Localhost Ports / Ingress</div>
            <div class="badge">Python Web Tier</div>
            <div class="badge">Go + Python App Tier</div>
            <div class="badge">PostgreSQL + Redis + Meilisearch</div>
          </div>
          <div>
            <h2 id="title"></h2>
            <p id="objective"></p>
            <p id="prompt" class="muted"></p>
            <p class="muted">The coach seeds fresh traces across all shop endpoints when the scenario starts, when you randomize, and when the next scenario loads after a correct answer.</p>
          </div>
          <div class="actions">
            {{if .JaegerURL}}<a class="button" target="_blank" rel="noreferrer" href="{{.JaegerURL}}">Open Jaeger</a>{{end}}
            <button id="skip">New Scenario</button>
          </div>
          <div>
            <strong>Trace entry point</strong>
            <p class="muted">Start with service <code id="focus-service"></code> and operation <code id="focus-operation"></code>.</p>
          </div>
          <div>
            <strong>Need a hint?</strong>
            <p class="muted">Use hints only if you are stuck moving from the entry span to the next service layer.</p>
            <div id="hint-shell" class="hint-shell" aria-hidden="true">
              <div id="hint-box" class="muted"></div>
            </div>
            <div class="actions hint-actions">
              <button id="hint" type="button">Show Hint</button>
            </div>
          </div>
        </article>
        <aside class="panel stack">
          <div>
            <h3>Submit Diagnosis</h3>
            <select id="service">
              <option value="">Select service</option>
              <option value="catalog-api">catalog-api</option>
              <option value="inventory-api">inventory-api</option>
              <option value="orders-api">orders-api</option>
              <option value="payments-api">payments-api</option>
            </select>
            <select id="issue">
              <option value="">Select issue type</option>
              <option value="expensive_search_query">expensive search query</option>
              <option value="n_plus_one_queries">n plus one queries</option>
              <option value="lock_wait_timeout">lock wait timeout</option>
              <option value="expensive_sort">expensive sort</option>
            </select>
            <div class="actions">
              <button id="submit">Check Answer</button>
            </div>
          </div>
          <div id="feedback">{{.InitialFeedback}}</div>
        </aside>
      </section>
    </main>
    <div id="busy-overlay" class="hidden" aria-live="polite" aria-busy="true">
      <div class="panel modal">
        <div class="spinner" aria-hidden="true"></div>
        <strong id="busy-title">Loading...</strong>
      </div>
    </div>
    <script>
      const initialScenario = {{.InitialScenario}};
      const learnerLoopHint = "1. Read the scenario.\n2. Open Jaeger.\n3. Inspect the newest trace for the focus operation.";
      // Keep submit loading feedback visible briefly so instant wrong answers do
      // not cause a distracting busy-overlay flash.
      const minimumSubmitBusyMs = 700;
      let current = initialScenario;
      let hintLevel = 0;

      function setFeedback(message, ok = false) {
        const box = document.getElementById("feedback");
        box.textContent = message;
        box.className = ok ? "ok" : "";
      }

      function delay(ms) {
        return new Promise((resolve) => window.setTimeout(resolve, ms));
      }

      function hintsForCurrent() {
        return [learnerLoopHint, current.hint_1, current.hint_2].filter(Boolean);
      }

      function renderHints() {
        const hints = hintsForCurrent();
        const shell = document.getElementById("hint-shell");
        const box = document.getElementById("hint-box");
        const button = document.getElementById("hint");
        const isOpen = hints.length > 0 && hintLevel > 0;

        shell.classList.toggle("open", isOpen);
        shell.setAttribute("aria-hidden", String(!isOpen));

        if (hints.length === 0) {
          box.textContent = "";
          button.disabled = true;
          button.textContent = "Hints Unavailable";
          return;
        }

        if (hintLevel === 0) {
          box.textContent = "";
          button.disabled = false;
          button.textContent = "Show Hint";
          return;
        }

        const level = Math.min(hintLevel, hints.length);
        box.textContent = hints[level-1];
        button.disabled = level >= hints.length;
        button.textContent = level >= hints.length ? "No More Hints" : "Show Another Hint";
      }

      function showHint() {
        const hints = hintsForCurrent();
        if (hintLevel < hints.length) {
          hintLevel++;
          renderHints();
        }
      }

      function setBusy(message) {
        document.getElementById("busy-title").textContent = message;
        document.getElementById("busy-overlay").classList.remove("hidden");
        document.getElementById("submit").disabled = true;
        document.getElementById("skip").disabled = true;
        document.getElementById("hint").disabled = true;
        document.getElementById("service").disabled = true;
        document.getElementById("issue").disabled = true;
      }

      function clearBusy() {
        document.getElementById("busy-overlay").classList.add("hidden");
        document.getElementById("submit").disabled = false;
        document.getElementById("skip").disabled = false;
        document.getElementById("service").disabled = false;
        document.getElementById("issue").disabled = false;
        renderHints();
      }

      function render() {
        document.getElementById("title").textContent = current.title;
        document.getElementById("objective").textContent = current.objective;
        document.getElementById("prompt").textContent = current.prompt;
        document.getElementById("focus-service").textContent = current.focus_service;
        document.getElementById("focus-operation").textContent = current.focus_operation;
        document.getElementById("service").value = "";
        document.getElementById("issue").value = "";
        hintLevel = 0;
        renderHints();
      }

      async function randomize(exclude = "") {
        setBusy("Loading a new scenario...");
        try {
          const response = await fetch("/api/scenarios/random?exclude=" + encodeURIComponent(exclude));
          if (!response.ok) {
            throw new Error("randomize request failed with status " + response.status);
          }

          current = await response.json();
          render();
          setFeedback("New scenario loaded. In Jaeger, filter to service " + current.focus_service + " and operation " + current.focus_operation + ", then open the newest trace.");
        } catch (error) {
          setFeedback("Loading a new scenario failed. Refresh the page and try again.");
        } finally {
          clearBusy();
        }
      }

      async function submit() {
        const suspectedService = document.getElementById("service").value;
        const suspectedIssue = document.getElementById("issue").value;
        if (!suspectedService || !suspectedIssue) {
          setFeedback("Select both a suspected service and an issue type before submitting.");
          return;
        }

        const minimumBusy = delay(minimumSubmitBusyMs);
        setBusy("Checking your answer...");
        try {
          const response = await fetch("/api/grade", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify({
              scenario_id: current.id,
              suspected_service: suspectedService,
              suspected_issue: suspectedIssue
            })
          });

          if (!response.ok) {
            throw new Error("grade request failed with status " + response.status);
          }

          const payload = await response.json();
          if (payload.correct && payload.next_scenario) {
            current = payload.next_scenario;
            render();
          }
          setFeedback(payload.feedback, payload.correct);
        } catch (error) {
          setFeedback("Submitting the diagnosis failed. Refresh the page and try again.");
        } finally {
          await minimumBusy;
          clearBusy();
        }
      }

      document.getElementById("skip").addEventListener("click", () => randomize(current.id));
      document.getElementById("hint").addEventListener("click", showHint);
      document.getElementById("submit").addEventListener("click", submit);
      render();
    </script>
  </body>
</html>
`
