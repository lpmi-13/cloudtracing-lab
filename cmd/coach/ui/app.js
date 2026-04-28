const minimumSubmitBusyMs = 700;
const skillPlacementKey = "cloudtracing.skillPlacement.v1";
const levelIntroKeyPrefix = "cloudtracing.levelIntro.v1.";
const levelReadyKeyPrefix = "cloudtracing.levelReady.v1.";
const mobileProgressionQuery = window.matchMedia("(max-width: 840px)");
const levelIntroContent = {
  1: {
    scene: "You have just joined the team and want to understand how requests actually move through the system before you change anything. You open recent traces to learn the landscape, get a feel for normal request timing, and spot any obvious bottlenecks worth investigating.",
    objective: "Practice spotting one slow request and the span that best explains the delay."
  },
  2: {
    scene: "A teammate says the service feels inconsistent: some requests seem fine, others feel sluggish. Before you jump to a conclusion, you compare several traces to separate healthy requests from regressed ones and look for the pattern the slow traces share.",
    objective: "Practice classifying mixed traces and identifying the responsible service behind the slow ones."
  },
  3: {
    scene: "An alert says latency has climbed after a recent change. You compare a healthy before trace against a slower after trace so you can explain what regressed before the team chases the wrong service.",
    objective: "Practice responding to an elevated latency alert by using Jaeger Compare to explain what changed."
  },
  4: {
    scene: "You think you know which service is responsible, but now you need evidence strong enough to convince the rest of the team in a review or incident thread. That means finding the exact span and the proof tag on that span that most specifically names the slow work or wait condition.",
    objective: "Practice proving the root cause with the culprit span and its proof tag instead of relying on a hunch."
  },
  5: {
    scene: "You are helping during an on-call incident where the failure is intermittent. Not every request is broken, and rushing to a conclusion could send the team in the wrong direction. You need to isolate the traces that actually show the failure and identify what they have in common.",
    objective: "Practice isolating intermittent failures under noisy, ambiguous conditions."
  }
};

let coachState = {};
let hintLevel = 0;
let lastScenarioID = "";
let lastSelectedLevel = 0;
let lastCorrectCountsByLevel = {};
let levelsCollapsed = mobileProgressionQuery.matches;
let levelsCollapseTouched = false;
let pendingLevelReady = 0;

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

function nextLevel(levelNumber) {
  return (coachState.levels || []).find((level) => level.number === levelNumber + 1) || null;
}

function renderJaegerLink() {
  const link = document.getElementById("open-jaeger");
  const href = coachState.jaeger_ui_url || "";
  const type = currentAssessment().type;
  const hidden = !href || type === "trace_search_span" || type === "healthy_faulty" || type === "before_after";

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
    if (level.ready_to_move_on) {
      button.classList.add("ready");
    }
    const completionMark = level.ready_to_move_on ? "<span class=\"level-complete\" aria-label=\"5 of 5 correct\" title=\"5/5 correct\">&#10003;</span>" : "";
    button.innerHTML =
      "<div class=\"level-topline\">" +
        "<strong>" + level.title + "</strong>" +
        completionMark +
      "</div>" +
      "<div class=\"level-summary\">" + level.summary + "</div>" +
      "<div class=\"level-progress\">" + level.correct_count + "/" + level.correct_target + " correct</div>";
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

function levelIntroKey(level) {
  return levelIntroKeyPrefix + String(level);
}

function levelReadyKey(level) {
  return levelReadyKeyPrefix + String(level);
}

function hasSeenLevelIntro(level) {
  try {
    return window.localStorage.getItem(levelIntroKey(level)) === "done";
  } catch (error) {
    return false;
  }
}

function markLevelIntroSeen(level) {
  try {
    window.localStorage.setItem(levelIntroKey(level), "done");
  } catch (error) {
  }
}

function hasSeenLevelReady(level) {
  try {
    return window.localStorage.getItem(levelReadyKey(level)) === "done";
  } catch (error) {
    return false;
  }
}

function markLevelReadySeen(level) {
  try {
    window.localStorage.setItem(levelReadyKey(level), "done");
  } catch (error) {
  }
}

function setSkillModalVisible(visible) {
  document.getElementById("skill-modal").classList.toggle("hidden", !visible);
}

function setLevelIntroVisible(visible) {
  document.getElementById("level-intro-modal").classList.toggle("hidden", !visible);
}

function setLevelReadyVisible(visible) {
  document.getElementById("level-ready-modal").classList.toggle("hidden", !visible);
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
  maybeShowLevelIntro();
}

function maybeShowSkillPlacement() {
  if (!hasSeenSkillPlacement()) {
    setSkillModalVisible(true);
  }
}

function renderLevelIntro(levelNumber) {
  const intro = levelIntroContent[levelNumber];
  const level = selectedLevel();
  const modal = document.getElementById("level-intro-modal");
  if (!intro || !level) {
    return;
  }

  modal.dataset.level = String(levelNumber);
  document.getElementById("level-intro-title").textContent = level.title + " • " + level.summary;
  document.getElementById("level-intro-scene").textContent = intro.scene;
  document.getElementById("level-intro-objective").textContent = intro.objective;
  document.getElementById("level-intro-close").textContent = "Start " + level.title;
}

function maybeShowLevelIntro() {
  if (!hasSeenSkillPlacement()) {
    return;
  }

  const level = selectedLevel();
  if (!level || !levelIntroContent[level.number] || hasSeenLevelIntro(level.number)) {
    return;
  }

  renderLevelIntro(level.number);
  setLevelIntroVisible(true);
  document.getElementById("level-intro-close").focus();
}

function closeLevelIntro() {
  const modal = document.getElementById("level-intro-modal");
  const levelNumber = Number(modal.dataset.level || coachState.selected_level || 0);
  if (levelNumber > 0) {
    markLevelIntroSeen(levelNumber);
  }
  setLevelIntroVisible(false);
  maybeShowLevelReady();
}

function renderLevelReady(levelNumber) {
  const level = selectedLevel();
  const modal = document.getElementById("level-ready-modal");
  const next = nextLevel(levelNumber);
  const nextButton = document.getElementById("level-ready-next");
  if (!level || level.number !== levelNumber) {
    return;
  }

  modal.dataset.level = String(levelNumber);
  modal.dataset.nextLevel = next ? String(next.number) : "";
  document.getElementById("level-ready-title").textContent = level.title + " • " + level.summary;
  document.getElementById("level-ready-summary").textContent = "You have " + level.correct_count + "/" + level.correct_target + " correct on this level.";
  if (next) {
    document.getElementById("level-ready-focus-title").textContent = "Ready to move on";
    document.getElementById("level-ready-copy").textContent = "You have demonstrated success here and are ready for the next level whenever you want. You can also stay on this level and keep practicing for as long as you like.";
    nextButton.textContent = "Move to " + next.title;
    nextButton.classList.remove("hidden");
    return;
  }

  document.getElementById("level-ready-focus-title").textContent = "Level complete";
  document.getElementById("level-ready-copy").textContent = "You have demonstrated success on the final level. You can stay on this level and keep practicing for as long as you like.";
  nextButton.textContent = "";
  nextButton.classList.add("hidden");
}

function queueLevelReady(level) {
  if (!level || hasSeenLevelReady(level.number)) {
    return;
  }
  const previous = lastCorrectCountsByLevel[level.number];
  if (typeof previous !== "number") {
    return;
  }
  if (previous < level.correct_target && level.correct_count >= level.correct_target) {
    pendingLevelReady = level.number;
  }
}

function maybeShowLevelReady() {
  if (!hasSeenSkillPlacement() || pendingLevelReady <= 0) {
    return;
  }
  if (!document.getElementById("skill-modal").classList.contains("hidden")) {
    return;
  }
  if (!document.getElementById("level-intro-modal").classList.contains("hidden")) {
    return;
  }

  const level = selectedLevel();
  if (!level || level.number !== pendingLevelReady || hasSeenLevelReady(level.number)) {
    return;
  }

  renderLevelReady(level.number);
  markLevelReadySeen(level.number);
  pendingLevelReady = 0;
  setLevelReadyVisible(true);
  if (document.getElementById("level-ready-next").classList.contains("hidden")) {
    document.getElementById("level-ready-close").focus();
  } else {
    document.getElementById("level-ready-next").focus();
  }
}

function closeLevelReady() {
  setLevelReadyVisible(false);
}

async function moveToNextLevel() {
  const next = Number(document.getElementById("level-ready-modal").dataset.nextLevel || 0);
  if (next <= 0) {
    return;
  }

  setLevelReadyVisible(false);
  await selectLevel(next);
}

function updateLastCorrectCounts() {
  const next = {};
  (coachState.levels || []).forEach((level) => {
    next[level.number] = level.correct_count || 0;
  });
  lastCorrectCountsByLevel = next;
}

function renderReferenceTrace(assessment) {
  const shell = document.getElementById("reference-trace");
  shell.innerHTML = "";
  shell.classList.remove("hidden");

  if (assessment.type === "healthy_faulty") {
    shell.classList.add("hidden");
    return;
  }

  if (assessment.compare_link) {
    const intro = document.createElement("span");
    intro.textContent = "Open Jaeger Compare:";
    shell.appendChild(intro);

    if (assessment.compare_link.url) {
      const link = document.createElement("a");
      link.href = assessment.compare_link.url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = assessment.compare_link.label;
      shell.appendChild(link);
    } else {
      shell.appendChild(document.createTextNode(assessment.compare_link.label));
    }
    return;
  }

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
      shell.textContent = "Open Jaeger Compare and inspect what changed between the healthy and slower traces.";
      break;
    case "intermittent_failure":
      shell.textContent = "Open each trace link " + traceLinksPlacementCopy() + ", decide which requests actually fail, then select the failing traces.";
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

  appendNote(container, "Each trace appears once. Mark the slow traces, mark the one healthy trace, then name the responsible service above.");

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
    assessment.compare_link ? assessment.compare_link.label : "",
    assessment.investigation_link ? assessment.investigation_link.label : "",
    assessment.reference_trace ? assessment.reference_trace.id : "",
    (assessment.trace_choices || []).length,
    (assessment.before_trace_choices || []).length,
    (assessment.after_trace_choices || []).length,
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
      appendSelect(shell, "selected-span", "Slow span", traceSpanChoicesForAssessment(assessment), "Select the span");
      restoreSelectValue("selected-span", previousState.selectedSpanID);
      break;
    case "culprit_span":
      if (!selectedServiceValue()) {
        appendNote(shell, "Select the responsible service to load its span choices.");
        break;
      }
      appendSelect(shell, "selected-span", "Culprit span", spanChoicesForAssessment(assessment), "Select the span");
      restoreSelectValue("selected-span", previousState.selectedSpanID);
      break;
    case "healthy_faulty":
      appendTraceClassifier(shell, assessment.trace_choices, previousState.traceRoles);
      break;
    case "before_after":
      break;
    case "span_attribute":
      if (!selectedServiceValue()) {
        appendNote(shell, "Select the responsible service to load its span choices.");
        break;
      }
      appendSelect(shell, "selected-span", "Culprit span", spanChoicesForAssessment(assessment), "Select the span");
      restoreSelectValue("selected-span", previousState.selectedSpanID);
      if (!selectedSpanValue()) {
        appendNote(shell, "Select the culprit span to load its proof tags.");
        break;
      }
      appendSelect(shell, "selected-attribute", "Proof tag on culprit span", attributeChoicesForAssessment(assessment), "Select the proof tag");
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
      return payload.selected_span ? "" : "Select the slow span before submitting.";
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
    case "span_attribute":
      if (!payload.selected_span) {
        return "Select the culprit span before submitting.";
      }
      return payload.selected_attribute ? "" : "Select the proof tag before submitting.";
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
  const levelOneTraceSearch = assessment.type === "trace_search_span";
  const levelTwoHealthyFaulty = assessment.type === "healthy_faulty";
  const levelThreeBeforeAfter = assessment.type === "before_after";
  const levelFiveIntermittent = assessment.type === "intermittent_failure";

  document.getElementById("title").textContent = levelOneTraceSearch ? "Find the slow trace and span" : (levelTwoHealthyFaulty ? (current.objective || current.title || "") : (current.title || ""));
  document.getElementById("objective").textContent = levelOneTraceSearch || levelTwoHealthyFaulty || levelThreeBeforeAfter || levelFiveIntermittent ? "" : (current.objective || "");
  assessmentPrompt.textContent = assessment.prompt || "";
  assessmentPrompt.classList.toggle("hidden", assessment.type === "trace_search_span" || assessment.type === "healthy_faulty" || assessment.type === "before_after" || assessment.type === "intermittent_failure" || !assessment.prompt);
  document.getElementById("selected-level-title").textContent = selected ? (selected.title + " • " + selected.summary) : "Level";
  document.getElementById("selected-level-progress").textContent = selected ? (selected.correct_count + "/" + selected.correct_target + " correct") : "";

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
  queueLevelReady(selected);
  maybeShowLevelIntro();
  maybeShowLevelReady();

  lastScenarioID = current.id || "";
  lastSelectedLevel = coachState.selected_level || 0;
  updateLastCorrectCounts();
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
    setFeedback("Select both a responsible service and a failure mode before submitting.", false, true);
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
    setFeedback("Submitting your answer failed. Refresh the page and try again.", false, true);
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
document.getElementById("level-intro-close").addEventListener("click", closeLevelIntro);
document.getElementById("level-ready-next").addEventListener("click", moveToNextLevel);
document.getElementById("level-ready-close").addEventListener("click", closeLevelReady);
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
