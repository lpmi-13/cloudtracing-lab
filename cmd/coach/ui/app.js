const minimumSubmitBusyMs = 700;
const skillPlacementKey = "cloudtracing.skillPlacement.v1";
const mobileProgressionQuery = window.matchMedia("(max-width: 840px)");

let coachState = {};
let hintLevel = 0;
let lastScenarioID = "";
let lastSelectedLevel = 0;
let levelsCollapsed = mobileProgressionQuery.matches;
let levelsCollapseTouched = false;

function currentScenario() {
  return coachState.current_scenario || {};
}

function currentAssessment() {
  return currentScenario().assessment || {};
}

function requiresDiagnosis(assessment) {
  return assessment.type !== "trace_search_span";
}

function selectedLevel() {
  return (coachState.levels || []).find((level) => level.selected) || null;
}

function renderJaegerLink() {
  const link = document.getElementById("open-jaeger");
  const href = coachState.jaeger_ui_url || "";
  const hidden = !href || currentAssessment().type === "trace_search_span";

  link.href = href || "#";
  link.classList.toggle("hidden", hidden);
}

function setFeedback(message, ok = false, visible = false) {
  const panel = document.getElementById("feedback-panel");
  const box = document.getElementById("feedback");
  box.textContent = message || "";
  box.classList.toggle("ok", ok);
  panel.classList.toggle("hidden", !visible);
}

function setLevelsCollapsed(collapsed) {
  levelsCollapsed = collapsed;
  document.getElementById("levels-wrap").classList.toggle("collapsed", collapsed);
  document.getElementById("toggle-levels").setAttribute("aria-expanded", String(!collapsed));
  document.getElementById("toggle-levels").textContent = collapsed ? "Show Levels" : "Hide Levels";
}

function syncLevelsCollapsed() {
  if (levelsCollapseTouched) {
    return;
  }
  setLevelsCollapsed(mobileProgressionQuery.matches);
}

function toggleLevels() {
  levelsCollapseTouched = true;
  setLevelsCollapsed(!levelsCollapsed);
}

function delay(ms) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function hintsForCurrent() {
  const current = currentScenario();
  return [current.prompt, current.hint_1, current.hint_2].filter(Boolean);
}

function traceLinksPlacementCopy() {
  return mobileProgressionQuery.matches ? "below" : "to the right";
}

function renderAssessmentFieldVisibility(assessment) {
  const hidden = !requiresDiagnosis(assessment);
  document.getElementById("service-field").classList.toggle("hidden", hidden);
  document.getElementById("issue-field").classList.toggle("hidden", hidden);
}

function renderHints() {
  const hints = hintsForCurrent();
  const panel = document.getElementById("hint-panel");
  const shell = document.getElementById("hint-shell");
  const box = document.getElementById("hint-box");
  const button = document.getElementById("hint");
  const isOpen = hints.length > 0 && hintLevel > 0;

  panel.classList.toggle("hidden", hints.length === 0);
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
  box.textContent = hints[level - 1];
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

function toggleInputs(disabled) {
  document.getElementById("submit").disabled = disabled;
  document.getElementById("next-challenge").disabled = disabled;
  document.getElementById("hint").disabled = disabled;
  document.getElementById("service").disabled = disabled;
  document.getElementById("issue").disabled = disabled;
  document.querySelectorAll("#assessment-fields select, #assessment-fields input").forEach((element) => {
    element.disabled = disabled;
  });
  document.querySelectorAll(".level-button").forEach((button) => {
    button.disabled = disabled;
  });
}

function setBusy(message) {
  document.getElementById("busy-title").textContent = message;
  document.getElementById("busy-overlay").classList.remove("hidden");
  toggleInputs(true);
}

function clearBusy() {
  document.getElementById("busy-overlay").classList.add("hidden");
  toggleInputs(false);
  renderLevels();
}

function renderLevels() {
  const root = document.getElementById("levels");
  root.innerHTML = "";

  (coachState.levels || []).forEach((level) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "level-button";
    button.dataset.unlocked = "true";
    if (level.selected) {
      button.classList.add("selected");
    }
    if (level.mastered) {
      button.classList.add("mastered");
    }
    button.innerHTML =
      "<div class=\"level-topline\">" +
        "<strong>" + level.title + "</strong>" +
        "<span class=\"level-state\">" + (level.mastered ? "Mastered" : "Open") + "</span>" +
      "</div>" +
      "<div class=\"level-summary\">" + level.summary + "</div>" +
      "<div class=\"level-progress\">" + level.mastery_count + "/" + level.mastery_target + " correct</div>";
    button.addEventListener("click", () => selectLevel(level.number));
    root.appendChild(button);
  });
}

function hasSeenSkillPlacement() {
  try {
    return window.localStorage.getItem(skillPlacementKey) === "done";
  } catch (error) {
    return false;
  }
}

function markSkillPlacementSeen() {
  try {
    window.localStorage.setItem(skillPlacementKey, "done");
  } catch (error) {
  }
}

function setSkillModalVisible(visible) {
  document.getElementById("skill-modal").classList.toggle("hidden", !visible);
}

function showSkillExplanation() {
  document.getElementById("skill-step-choice").classList.add("hidden");
  document.getElementById("skill-step-explainer").classList.remove("hidden");
  document.getElementById("skill-modal-close").focus();
}

function setSkillChoiceDisabled(disabled) {
  document.querySelectorAll("[data-skill-choice]").forEach((button) => {
    button.disabled = disabled;
  });
}

async function chooseSkillPlacement(event) {
  const button = event.currentTarget;
  const level = Number(button.dataset.level);
  const error = document.getElementById("skill-modal-error");
  error.textContent = "";
  error.classList.add("hidden");
  setSkillChoiceDisabled(true);

  try {
    await requestSnapshot("/api/levels/select", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({level})
    });
    markSkillPlacementSeen();
    showSkillExplanation();
  } catch (failure) {
    error.textContent = "Could not set your starting level. Try again.";
    error.classList.remove("hidden");
    setSkillChoiceDisabled(false);
  }
}

function closeSkillPlacement() {
  setSkillModalVisible(false);
}

function maybeShowSkillPlacement() {
  if (!hasSeenSkillPlacement()) {
    setSkillModalVisible(true);
  }
}

function renderReferenceTrace(assessment) {
  const shell = document.getElementById("reference-trace");
  shell.innerHTML = "";

  if (assessment.investigation_link) {
    const intro = document.createElement("span");
    intro.textContent = "Open the prepared trace search:";
    shell.appendChild(intro);

    if (assessment.investigation_link.url) {
      const link = document.createElement("a");
      link.href = assessment.investigation_link.url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = assessment.investigation_link.label;
      shell.appendChild(link);
    } else {
      shell.appendChild(document.createTextNode(assessment.investigation_link.label));
    }
    return;
  }

  if (assessment.reference_trace) {
    const intro = document.createElement("span");
    intro.textContent = "Open the reference trace:";
    shell.appendChild(intro);

    if (assessment.reference_trace.url) {
      const link = document.createElement("a");
      link.href = assessment.reference_trace.url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = assessment.reference_trace.label;
      shell.appendChild(link);
    } else {
      shell.appendChild(document.createTextNode(assessment.reference_trace.label));
    }
    return;
  }

  if (assessment.unavailable_reason) {
    shell.textContent = assessment.unavailable_reason;
    return;
  }

  switch (assessment.type) {
    case "healthy_faulty":
      shell.textContent = "Open each trace link " + traceLinksPlacementCopy() + ", decide which ones are slow or healthy, then classify each trace once.";
      break;
    case "before_after":
      shell.textContent = "Pick one baseline trace and one slow trace from the shared candidate list below.";
      break;
    case "intermittent_failure":
      shell.textContent = "Open the trace links below and select the failing ones.";
      break;
    default:
      shell.textContent = "Open Jaeger to inspect the prepared traces below.";
  }
}

function appendNote(container, text) {
  const note = document.createElement("p");
  note.className = "muted";
  note.textContent = text;
  container.appendChild(note);
}

function appendSelect(container, id, labelText, options, placeholder) {
  const label = document.createElement("label");
  label.className = "field-label";
  label.htmlFor = id;
  label.textContent = labelText;
  container.appendChild(label);

  const select = document.createElement("select");
  select.id = id;

  const empty = document.createElement("option");
  empty.value = "";
  empty.textContent = placeholder;
  select.appendChild(empty);

  (options || []).forEach((option) => {
    const item = document.createElement("option");
    item.value = option.id;
    item.textContent = option.label;
    select.appendChild(item);
  });
  container.appendChild(select);
}

function appendChoiceGroup(container, name, type, labelText, options) {
  const label = document.createElement("div");
  label.className = "field-label";
  label.textContent = labelText;
  container.appendChild(label);

  const group = document.createElement("div");
  group.className = "checkbox-group";

  (options || []).forEach((option) => {
    const row = document.createElement("label");
    row.className = "choice";

    const input = document.createElement("input");
    input.type = type;
    input.name = name;
    input.value = option.id;
    row.appendChild(input);

    const text = document.createElement("div");
    const title = document.createElement("div");
    title.textContent = option.label;
    text.appendChild(title);

    if (option.url) {
      const link = document.createElement("a");
      link.href = option.url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = "Open trace";
      text.appendChild(link);
    }

    row.appendChild(text);
    group.appendChild(row);
  });

  container.appendChild(group);
}

function appendTraceClassifier(container, options, previousRoles) {
  const label = document.createElement("div");
  label.className = "field-label";
  label.textContent = "Trace classification";
  container.appendChild(label);

  appendNote(container, "Each trace appears once. Mark the slow traces, mark the one healthy trace, then name the shared culprit above.");

  const group = document.createElement("div");
  group.className = "checkbox-group";

  (options || []).forEach((option) => {
    const row = document.createElement("div");
    row.className = "choice trace-classifier-row";

    const text = document.createElement("div");
    text.className = "trace-choice-copy";

    const title = document.createElement("div");
    title.textContent = option.label;
    text.appendChild(title);

    if (option.url) {
      const link = document.createElement("a");
      link.href = option.url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = "Open trace";
      text.appendChild(link);
    }

    const select = document.createElement("select");
    select.className = "trace-role-select";
    select.dataset.traceRole = option.id;

    [
      {value: "", label: "Classify trace"},
      {value: "slow", label: "Slow"},
      {value: "healthy", label: "Healthy"}
    ].forEach((entry) => {
      const item = document.createElement("option");
      item.value = entry.value;
      item.textContent = entry.label;
      select.appendChild(item);
    });

    if (previousRoles && previousRoles[option.id]) {
      select.value = previousRoles[option.id];
    }

    row.appendChild(text);
    row.appendChild(select);
    group.appendChild(row);
  });

  container.appendChild(group);
}

function selectedServiceValue() {
  return document.getElementById("service")?.value || "";
}

function selectedTraceValue() {
  return document.getElementById("selected-trace")?.value || "";
}

function selectedSpanValue() {
  return document.getElementById("selected-span")?.value || "";
}

function traceSpanChoicesForAssessment(assessment) {
  const traceID = selectedTraceValue();
  if (!traceID) {
    return [];
  }
  return (assessment.trace_span_choices || {})[traceID] || [];
}

function spanChoicesForAssessment(assessment) {
  const service = selectedServiceValue();
  if (!service) {
    return [];
  }
  return (assessment.span_choices || []).filter((option) => option.service === service);
}

function attributeChoicesForAssessment(assessment) {
  const spanID = selectedSpanValue();
  if (!spanID) {
    return [];
  }
  return (assessment.span_attribute_choices || {})[spanID] || [];
}

function restoreSelectValue(id, value) {
  const select = document.getElementById(id);
  if (!select || !value) {
    return;
  }
  if ([...select.options].some((option) => option.value === value)) {
    select.value = value;
  }
}

function restoreCheckedValues(name, values) {
  const wanted = new Set(values || []);
  document.querySelectorAll("input[name=\"" + name + "\"]").forEach((input) => {
    input.checked = wanted.has(input.value);
  });
}

function traceRoleSelections() {
  const roles = {};
  document.querySelectorAll("select[data-trace-role]").forEach((select) => {
    if (select.value) {
      roles[select.dataset.traceRole] = select.value;
    }
  });
  return roles;
}

function renderAssessmentFields(force) {
  const shell = document.getElementById("assessment-fields");
  const assessment = currentAssessment();
  const previousState = {
    selectedTraceID: selectedTraceValue(),
    selectedSpanID: selectedSpanValue(),
    selectedAttributeID: document.getElementById("selected-attribute")?.value || "",
    beforeTraceID: document.getElementById("before-trace")?.value || "",
    afterTraceID: document.getElementById("after-trace")?.value || "",
    faultyTraceIDs: checkedValues("faulty-trace"),
    healthyTraceID: checkedValue("healthy-trace"),
    failingTraceIDs: checkedValues("failing-trace"),
    traceRoles: traceRoleSelections()
  };
  const signature = [
    assessment.type || "",
    String(assessment.ready),
    assessment.investigation_link ? assessment.investigation_link.label : "",
    assessment.reference_trace ? assessment.reference_trace.id : "",
    (assessment.trace_choices || []).length,
    (assessment.span_choices || []).length,
    (assessment.attribute_choices || []).length
  ].join(":");

  if (!force && shell.dataset.signature === signature) {
    return;
  }

  shell.innerHTML = "";
  shell.dataset.signature = signature;
  if (!assessment.ready) {
    const note = document.createElement("p");
    note.className = "muted";
    note.textContent = assessment.unavailable_reason || "The challenge is still preparing its assessment evidence.";
    shell.appendChild(note);
    return;
  }

  switch (assessment.type) {
    case "trace_search_span":
      appendSelect(shell, "selected-trace", "Trace used", assessment.trace_choices, "Select the trace");
      restoreSelectValue("selected-trace", previousState.selectedTraceID);
      if (!selectedTraceValue()) {
        appendNote(shell, "Select the slow trace you inspected to load its span choices.");
        break;
      }
      appendSelect(shell, "selected-span", "Culprit span", traceSpanChoicesForAssessment(assessment), "Select the span");
      restoreSelectValue("selected-span", previousState.selectedSpanID);
      break;
    case "culprit_span":
      if (!selectedServiceValue()) {
        appendNote(shell, "Select the culprit service to load its span choices.");
        break;
      }
      appendSelect(shell, "selected-span", "Culprit span", spanChoicesForAssessment(assessment), "Select the span");
      restoreSelectValue("selected-span", previousState.selectedSpanID);
      break;
    case "healthy_faulty":
      appendTraceClassifier(shell, assessment.trace_choices, previousState.traceRoles);
      break;
    case "before_after":
      appendSelect(shell, "before-trace", "Before trace", assessment.trace_choices, "Select a baseline trace");
      appendSelect(shell, "after-trace", "After trace", assessment.trace_choices, "Select a slow trace");
      restoreSelectValue("before-trace", previousState.beforeTraceID);
      restoreSelectValue("after-trace", previousState.afterTraceID);
      break;
    case "span_attribute":
      if (!selectedServiceValue()) {
        appendNote(shell, "Select the culprit service to load its span choices.");
        break;
      }
      appendSelect(shell, "selected-span", "Culprit span", spanChoicesForAssessment(assessment), "Select the span");
      restoreSelectValue("selected-span", previousState.selectedSpanID);
      if (!selectedSpanValue()) {
        appendNote(shell, "Select the culprit span to load its supporting attributes.");
        break;
      }
      appendSelect(shell, "selected-attribute", "Supporting attribute", attributeChoicesForAssessment(assessment), "Select the attribute");
      restoreSelectValue("selected-attribute", previousState.selectedAttributeID);
      break;
    case "intermittent_failure":
      appendChoiceGroup(shell, "failing-trace", "checkbox", "Failing traces", assessment.trace_choices);
      restoreCheckedValues("failing-trace", previousState.failingTraceIDs);
      break;
  }
}

function checkedValues(name) {
  return Array.from(document.querySelectorAll("input[name=\"" + name + "\"]:checked")).map((element) => element.value);
}

function checkedValue(name) {
  const selected = document.querySelector("input[name=\"" + name + "\"]:checked");
  return selected ? selected.value : "";
}

function assessmentPayload(assessment) {
  const payload = {
    selected_trace_id: "",
    selected_span: "",
    selected_attribute: "",
    faulty_trace_ids: [],
    healthy_trace_id: "",
    before_trace_id: "",
    after_trace_id: "",
    failing_trace_ids: []
  };

  switch (assessment.type) {
    case "trace_search_span":
      payload.selected_trace_id = document.getElementById("selected-trace")?.value || "";
      payload.selected_span = document.getElementById("selected-span")?.value || "";
      break;
    case "culprit_span":
      payload.selected_span = document.getElementById("selected-span")?.value || "";
      break;
    case "healthy_faulty":
      payload.trace_roles = traceRoleSelections();
      payload.faulty_trace_ids = Object.keys(payload.trace_roles).filter((traceID) => payload.trace_roles[traceID] === "slow");
      payload.healthy_trace_ids = Object.keys(payload.trace_roles).filter((traceID) => payload.trace_roles[traceID] === "healthy");
      payload.healthy_trace_id = payload.healthy_trace_ids.length === 1 ? payload.healthy_trace_ids[0] : "";
      break;
    case "before_after":
      payload.before_trace_id = document.getElementById("before-trace")?.value || "";
      payload.after_trace_id = document.getElementById("after-trace")?.value || "";
      break;
    case "span_attribute":
      payload.selected_span = document.getElementById("selected-span")?.value || "";
      payload.selected_attribute = document.getElementById("selected-attribute")?.value || "";
      break;
    case "intermittent_failure":
      payload.failing_trace_ids = checkedValues("failing-trace");
      break;
  }
  return payload;
}

function validateAssessment(assessment, payload) {
  if (!assessment.ready) {
    return "The challenge is still preparing. Wait for the evidence fields to load.";
  }

  switch (assessment.type) {
    case "trace_search_span":
      if (!payload.selected_trace_id) {
        return "Select the slow trace you inspected before submitting.";
      }
      return payload.selected_span ? "" : "Select the culprit span before submitting.";
    case "culprit_span":
      return payload.selected_span ? "" : "Select the culprit span before submitting.";
    case "healthy_faulty":
      if (Object.keys(payload.trace_roles || {}).length < (assessment.trace_choices || []).length) {
        return "Classify every trace before submitting.";
      }
      if ((payload.healthy_trace_ids || []).length === 0) {
        return "Select the healthy trace before submitting.";
      }
      if ((payload.healthy_trace_ids || []).length > 1) {
        return "Choose only one healthy trace before submitting.";
      }
      return payload.faulty_trace_ids.length > 0 ? "" : "Select every slow trace before submitting.";
    case "before_after":
      if (!payload.before_trace_id) {
        return "Select a before trace before submitting.";
      }
      if (payload.before_trace_id === payload.after_trace_id) {
        return "Select two different traces before submitting.";
      }
      return payload.after_trace_id ? "" : "Select an after trace before submitting.";
    case "span_attribute":
      if (!payload.selected_span) {
        return "Select the culprit span before submitting.";
      }
      return payload.selected_attribute ? "" : "Select the supporting attribute before submitting.";
    case "intermittent_failure":
      return payload.failing_trace_ids.length > 0 ? "" : "Select every failing trace before submitting.";
    default:
      return "";
  }
}

function render() {
  const current = currentScenario();
  const assessment = currentAssessment();
  const selected = selectedLevel();
  const scenarioChanged = current.id !== lastScenarioID || coachState.selected_level !== lastSelectedLevel;
  const assessmentPrompt = document.getElementById("assessment-prompt");

  document.getElementById("title").textContent = current.title || "";
  document.getElementById("objective").textContent = current.objective || "";
  assessmentPrompt.textContent = assessment.prompt || "";
  assessmentPrompt.classList.toggle("hidden", assessment.type === "trace_search_span" || !assessment.prompt);
  document.getElementById("selected-level-title").textContent = selected ? (selected.title + " • " + selected.summary) : "Level";
  document.getElementById("selected-level-progress").textContent = selected ? (selected.mastery_count + "/" + selected.mastery_target + " correct") : "";

  if (scenarioChanged) {
    document.getElementById("service").value = "";
    document.getElementById("issue").value = "";
    hintLevel = 0;
  }

  renderJaegerLink();
  renderAssessmentFieldVisibility(assessment);
  renderReferenceTrace(assessment);
  renderAssessmentFields(scenarioChanged || document.getElementById("assessment-fields").childElementCount === 0);
  renderLevels();
  renderHints();
  setFeedback(coachState.feedback, coachState.feedback_ok, coachState.has_feedback);

  lastScenarioID = current.id || "";
  lastSelectedLevel = coachState.selected_level || 0;
}

function applySnapshot(snapshot) {
  coachState = snapshot || {};
  render();
}

async function readSnapshot(response) {
  const text = await response.text();
  if (!text) {
    return null;
  }

  try {
    return JSON.parse(text);
  } catch (error) {
    return null;
  }
}

async function requestSnapshot(url, options = {}) {
  const response = await fetch(url, options);
  const snapshot = await readSnapshot(response);
  if (snapshot) {
    applySnapshot(snapshot);
  }
  if (!response.ok && !snapshot) {
    throw new Error("request failed with status " + response.status);
  }
  return snapshot;
}

async function selectLevel(level) {
  setBusy("Loading that level...");
  try {
    await requestSnapshot("/api/levels/select", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({level})
    });
  } catch (error) {
    setFeedback("Selecting the level failed. Refresh the page and try again.", false, true);
  } finally {
    clearBusy();
  }
}

async function nextChallenge() {
  setBusy("Preparing a new challenge...");
  try {
    await requestSnapshot("/api/challenges/next", {method: "POST"});
  } catch (error) {
    setFeedback("Preparing a new challenge failed. Refresh the page and try again.", false, true);
  } finally {
    clearBusy();
  }
}

async function submit() {
  const suspectedService = document.getElementById("service").value;
  const suspectedIssue = document.getElementById("issue").value;
  const current = currentScenario();
  const assessment = currentAssessment();
  const payload = assessmentPayload(assessment);
  const validationMessage = validateAssessment(assessment, payload);

  if (assessment.type === "healthy_faulty" && validationMessage) {
    setFeedback(validationMessage, false, true);
    return;
  }

  if (requiresDiagnosis(assessment) && (!suspectedService || !suspectedIssue)) {
    setFeedback("Select both a culprit service and a failure mode before submitting.", false, true);
    return;
  }

  if (validationMessage) {
    setFeedback(validationMessage, false, true);
    return;
  }

  const minimumBusy = delay(minimumSubmitBusyMs);
  setBusy("Checking your answer...");
  try {
    await requestSnapshot("/api/grade", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({
        scenario_id: current.id,
        suspected_service: suspectedService,
        suspected_issue: suspectedIssue,
        selected_trace_id: payload.selected_trace_id,
        selected_span: payload.selected_span,
        selected_attribute: payload.selected_attribute,
        faulty_trace_ids: payload.faulty_trace_ids,
        healthy_trace_id: payload.healthy_trace_id,
        before_trace_id: payload.before_trace_id,
        after_trace_id: payload.after_trace_id,
        failing_trace_ids: payload.failing_trace_ids
      })
    });
  } catch (error) {
    setFeedback("Submitting the diagnosis failed. Refresh the page and try again.", false, true);
  } finally {
    await minimumBusy;
    clearBusy();
  }
}

function connectEvents() {
  const stream = new EventSource("/api/events");
  stream.onmessage = (event) => {
    try {
      applySnapshot(JSON.parse(event.data));
    } catch (error) {
    }
  };
}

async function init() {
  setBusy("Loading challenge...");
  syncLevelsCollapsed();

  try {
    const snapshot = await requestSnapshot("/api/state");
    if (!snapshot) {
      throw new Error("missing initial snapshot");
    }
    connectEvents();
    maybeShowSkillPlacement();
  } catch (error) {
    setFeedback("Loading the coach failed. Refresh the page and try again.", false, true);
  } finally {
    clearBusy();
  }
}

document.getElementById("next-challenge").addEventListener("click", nextChallenge);
document.getElementById("hint").addEventListener("click", showHint);
document.getElementById("submit").addEventListener("click", submit);
document.querySelectorAll("[data-skill-choice]").forEach((button) => {
  button.addEventListener("click", chooseSkillPlacement);
});
document.getElementById("skill-modal-close").addEventListener("click", closeSkillPlacement);
document.getElementById("service").addEventListener("change", () => {
  const type = currentAssessment().type;
  if (type === "culprit_span" || type === "span_attribute") {
    renderAssessmentFields(true);
  }
});
document.getElementById("assessment-fields").addEventListener("change", (event) => {
  const type = currentAssessment().type;
  if (event.target.id === "selected-trace" && type === "trace_search_span") {
    renderAssessmentFields(true);
  }
  if (event.target.id === "selected-span" && type === "span_attribute") {
    renderAssessmentFields(true);
  }
});
document.getElementById("toggle-levels").addEventListener("click", toggleLevels);
mobileProgressionQuery.addEventListener("change", () => {
  syncLevelsCollapsed();
  if (currentAssessment().type) {
    render();
  }
});

init();
