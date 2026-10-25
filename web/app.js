(() => {
  "use strict";

  /* --------------------------------------------------------------- helpers */

  const $ = (id) => document.getElementById(id);
  // Wiring goes through on() so one renamed or optional element cannot abort
  // the rest of the page. Missing ids are reported once, after wiring.
  const missingElements = [];
  function on(id, type, handler, options) {
    const node = $(id);
    if (!node) {
      missingElements.push(id);
      return null;
    }
    node.addEventListener(type, handler, options);
    return node;
  }
  const hasI18n = () => Boolean(window.FolioI18n);
  const t = (key, vars) => (hasI18n() ? window.FolioI18n.t(key, vars) : key);
  const formatTime = (value) => (hasI18n() ? window.FolioI18n.formatTime(value) : "");
  const translateKnown = (key, fallback) => (hasI18n() && window.FolioI18n.has(key) ? t(key) : fallback);

  class ApiError extends Error {
    constructor(code, status, payload) {
      super(code);
      this.name = "ApiError";
      this.code = code;
      this.status = status;
      this.payload = payload || null;
      this.network = code === "network";
    }
  }

  const state = {
    csrf: "",
    authenticated: false,
    loggingIn: false,
    loggingOut: false,
    connecting: false,
    syncing: false,
    refreshing: false,
    automationRunning: false,
    automationLoading: false,
    recovering: false,
    device: null,
    timers: { poll: null, countdown: null, job: null, copy: null, automation: null },
    automation: null,
    automationRuns: [],
    automationError: false,
    channels: null,
    channelsSaving: false,
    channelsTesting: false,
    hooks: [],
    hookSecret: "",
    hookSecretFor: "",
    hookBusy: "",
    usage: null,
    usageWindow: 30,
    tasks: null,
    kinds: null,
    tasksBusy: "",
    modelValues: { codex: "", dsh: "" },
    modelMenuOpen: false,
    modelActive: -1,
    modelCatalogSignature: "",
    selectedRunId: "",
    lastState: null,
  };

  const messageRegions = {
    login: { element: "login-message", data: null },
    repository: { element: "repository-message", data: null },
    automation: { element: "automation-message", data: null },
    channels: { element: "channels-message", data: null },
    hooks: { element: "hooks-message", data: null },
    usage: { element: "usage-message", data: null },
    tasks: { element: "tasks-message", data: null },
  };

  let busyButtons = [];

  /* ------------------------------------------------------------- transport */

  async function api(path, options = {}) {
    const method = options.method || "GET";
    const headers = { Accept: "application/json" };
    if (options.body !== undefined) headers["Content-Type"] = "application/json";
    if (state.csrf && method !== "GET") headers["X-CSRF-Token"] = state.csrf;

    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 60000);
    let response;
    try {
      response = await fetch(path, {
        method,
        headers,
        credentials: "same-origin",
        cache: "no-store",
        signal: controller.signal,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
      });
    } catch (_) {
      throw new ApiError("network", 0, null);
    } finally {
      window.clearTimeout(timeout);
    }

    let payload = null;
    try {
      payload = await response.json();
    } catch (_) {
      payload = null;
    }

    if (response.status === 401 && path !== "/api/session" && path !== "/api/login") {
      expireSession();
    }
    if (!response.ok) {
      const code = payload && typeof payload.error === "string" ? payload.error : "operation_failed";
      throw new ApiError(code, response.status, payload);
    }
    return payload || {};
  }

  function errorKey(error, fallback) {
    if (!error) return fallback;
    if (error.network) return "error.network";
    switch (error.code) {
      case "invalid_credentials":
        return "login.failed";
      case "login_rate_limited":
        return "login.rate_limited";
      case "authentication_required":
        return "login.expired";
      case "origin_forbidden":
      case "csrf_failed":
        return "error.forbidden";
      case "not_found":
        return "error.not_found";
      case "invalid_change":
      case "invalid_request":
        return "automation.invalid";
      case "invalid_model":
        return "error.invalid";
      case "invalid_runner":
        return "runner.unavailable";
      case "device_key_required":
      case "channel_not_configured":
        return "channels.device_key_required";
      case "invalid_channel":
      case "invalid_level":
      case "invalid_language":
        return "channels.invalid";
      case "invalid_hook_name":
        return "hooks.invalid_name";
      case "hook_limit_reached":
        return "hooks.limit";
      case "hook_not_found":
        return "hooks.not_found";
      case "invalid_hook_id":
      case "invalid_hook_action":
      case "hook_invalid":
        return "hooks.failed";
      case "usage_unavailable":
        return "usage.failed";
      case "task_not_found":
        return "tasks.not_found";
      case "task_busy":
        return "tasks.busy";
      case "task_canceled":
        return "tasks.canceled_error";
      case "job_unavailable":
        return "error.job_busy";
      case "session_unavailable":
        return "error.unavailable";
      default:
        return fallback || "error.generic";
    }
  }

  /* -------------------------------------------------------------- messages */

  function renderMessage(region) {
    const record = messageRegions[region];
    if (!record) return;
    const element = $(record.element);
    if (!element) return;
    const data = record.data;
    element.textContent = data ? t(data.key, data.vars) : "";
    element.dataset.error = data && data.error ? "true" : "false";
  }

  function setMessage(region, key, options = {}) {
    const record = messageRegions[region];
    if (!record) return;
    record.data = key ? { key, vars: options.vars || null, error: Boolean(options.error) } : null;
    renderMessage(region);
  }

  function renderMessages() {
    Object.keys(messageRegions).forEach(renderMessage);
  }

  function renderRaw(elementId, value) {
    const element = $(elementId);
    if (!element) return;
    try {
      element.textContent = JSON.stringify(value, null, 2);
    } catch (_) {
      element.textContent = String(value);
    }
  }

  function renderErrorRaw(elementId, error) {
    renderRaw(elementId, {
      error: error && error.code ? error.code : null,
      status: error && error.status ? error.status : null,
    });
  }

  /* ----------------------------------------------------------------- views */

  function setConnection(online) {
    const pill = $("connection-state");
    if (!pill) return;
    pill.dataset.state = online ? "online" : "offline";
    $("connection-label").textContent = t(online ? "status.online" : "status.offline");
  }

  function showLogin() {
    $("login-view").hidden = false;
    $("app-view").hidden = true;
    $("logout-button").hidden = true;
    setConnection(false);
  }

  function showApp() {
    $("login-view").hidden = true;
    $("app-view").hidden = false;
    $("logout-button").hidden = false;
    setConnection(true);
    if (!state.timers.automation) {
      state.timers.automation = window.setInterval(() => {
        if (state.authenticated && !document.hidden) refreshAutomation({ silent: true });
      }, 15000);
    }
  }

  /* ------------------------------------------------------------------ busy */

  function setBusy(button, key) {
    if (!button) return;
    if (!busyButtons.includes(button)) {
      busyButtons.push(button);
      button.dataset.busyDisabled = button.disabled ? "true" : "false";
    }
    button.dataset.busyKey = key;
    button.setAttribute("aria-busy", "true");
    button.disabled = true;
    const label = button.querySelector(".btn-label");
    if (label) label.textContent = t(key);
  }

  function clearBusy(button) {
    if (!button) return;
    const wasDisabled = button.dataset.busyDisabled === "true";
    button.removeAttribute("aria-busy");
    button.disabled = wasDisabled;
    delete button.dataset.busyKey;
    delete button.dataset.busyDisabled;
    busyButtons = busyButtons.filter((item) => item !== button);
    if (hasI18n()) window.FolioI18n.apply(button);
    else {
      const label = button.querySelector(".btn-label");
      if (label) label.textContent = t("common.refresh");
    }
  }

  function rerenderBusyLabels() {
    busyButtons.forEach((button) => {
      const key = button.dataset.busyKey;
      const label = button.querySelector(".btn-label");
      if (key && label) label.textContent = t(key);
    });
  }

  /* ------------------------------------------------------------------ auth */

  function expireSession() {
    if (!state.authenticated && !state.csrf) return;
    state.authenticated = false;
    state.csrf = "";
    stopAllTimers();
    state.automation = null;
    state.automationRuns = [];
    state.automationError = false;
    state.modelCatalogSignature = "";
    state.modelValues = { codex: "", dsh: "" };
    state.hooks = [];
    showHookSecret("", "");
    if ($("model-input")) $("model-input").value = "";
    closeModelMenu();
    showLogin();
    leaveSession();
    setMessage("login", "login.expired", { error: true });
  }

  async function login(event) {
    event.preventDefault();
    if (state.loggingIn) return;
    const input = $("password");
    const password = input.value;
    if (!password) {
      setMessage("login", "login.required", { error: true });
      input.focus();
      return;
    }
    state.loggingIn = true;
    const button = $("login-submit");
    setBusy(button, "login.submitting");
    setMessage("login", null);
    try {
      const result = await api("/api/login", { method: "POST", body: { password } });
      state.csrf = typeof result.csrf_token === "string" ? result.csrf_token : "";
      state.authenticated = true;
      input.value = "";
      showApp();
      await refreshAll();
      await enterSession();
    } catch (error) {
      const key =
        error.code === "login_rate_limited" || error.status === 429
          ? "login.rate_limited"
          : error.code === "invalid_credentials"
            ? "login.failed"
            : error.network
              ? "error.network"
              : "login.failed";
      setMessage("login", key, { error: true });
    } finally {
      state.loggingIn = false;
      clearBusy(button);
    }
  }

  async function logout() {
    if (state.loggingOut) return;
    state.loggingOut = true;
    const button = $("logout-button");
    setBusy(button, "common.running");
    try {
      await api("/api/logout", { method: "POST", body: {} });
    } catch (_) {
      // Clear the local view even when the remote session has already expired.
    } finally {
      state.authenticated = false;
      state.csrf = "";
      state.automation = null;
      state.automationRuns = [];
      state.automationError = false;
      state.modelCatalogSignature = "";
      state.modelValues = { codex: "", dsh: "" };
      state.hooks = [];
      showHookSecret("", "");
      if ($("model-input")) $("model-input").value = "";
      closeModelMenu();
      stopAllTimers();
      showLogin();
      leaveSession();
      setMessage("login", null);
      state.loggingOut = false;
      clearBusy(button);
    }
  }

  /* ------------------------------------------------------------- repository */

  const repositoryPart = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$/;

  // connectRemote is the one connect transaction. The storage plugin's picker
  // calls it; the shell has no repository form of its own. It reports a reason,
  // not a translation key, so the calling plugin can phrase the failure.
  async function connectRemote(owner, name) {
    const cleanOwner = typeof owner === "string" ? owner.trim() : "";
    const cleanName = typeof name === "string" ? name.trim() : "";
    if (!repositoryPart.test(cleanOwner) || !repositoryPart.test(cleanName)) {
      return { ok: false, reason: "invalid" };
    }
    if (state.connecting) return { ok: false, reason: "busy" };
    state.connecting = true;
    try {
      const result = await api("/api/repository/connect", {
        method: "POST",
        body: { owner: cleanOwner, name: cleanName },
      });
      renderRaw("repository-output", result);
      await refreshAll();
      return { ok: true, result };
    } catch (error) {
      if (error.code !== "authentication_required") renderErrorRaw("repository-output", error);
      return { ok: false, reason: "failed" };
    } finally {
      state.connecting = false;
    }
  }

  async function syncRepository() {
    if (state.syncing) return;
    state.syncing = true;
    const button = $("sync-button");
    setBusy(button, "repo.syncing");
    setMessage("repository", null);
    try {
      const accepted = await api("/api/repository/sync", { method: "POST", body: {} });
      await pollJob(accepted.job_id, {
        region: "repository",
        output: "repository-output",
        successKey: "repo.synced",
        failureKey: "repo.sync_failed",
      });
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("repository", errorKey(error, "repo.sync_failed"), { error: true });
        renderErrorRaw("repository-output", error);
      }
    } finally {
      state.syncing = false;
      clearBusy(button);
    }
  }

  /* ------------------------------------------------------------- automation */

  function runnerAvailable(runner, data) {
    if (!data) return runner === "codex";
    if (data.runners && data.runners[runner]) return Boolean(data.runners[runner].available);
    if (runner === "codex" && data.model) return Boolean(data.model.available);
    return false;
  }

  function activeRunner() {
    const select = $("runner-select");
    return select && select.value === "dsh" ? "dsh" : "codex";
  }

  function modelOptionsFor(runner) {
    const catalog = state.automation && state.automation.models;
    const raw = catalog && Array.isArray(catalog[runner]) ? catalog[runner] : [];
    const seen = new Set();
    const options = [];
    raw.forEach((item) => {
      if (!item || typeof item.id !== "string" || !item.id || seen.has(item.id)) return;
      seen.add(item.id);
      const label = typeof item.label === "string" && item.label.trim() ? item.label.trim() : item.id;
      const provider = typeof item.provider === "string" ? item.provider.trim() : "";
      options.push({ id: item.id, label, provider });
    });
    return options;
  }

  function closeModelMenu() {
    const menu = $("model-options");
    const input = $("model-input");
    const toggle = $("model-toggle");
    if (!menu) return;
    menu.hidden = true;
    state.modelMenuOpen = false;
    state.modelActive = -1;
    if (input) {
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
    }
    if (toggle) toggle.setAttribute("aria-expanded", "false");
  }

  function modelMatch(option, query) {
    if (!query) return true;
    const needle = query.toLowerCase();
    return option.id.toLowerCase().includes(needle) || option.label.toLowerCase().includes(needle);
  }

  function setActiveModel(index) {
    const menu = $("model-options");
    const input = $("model-input");
    if (!menu || !input) return;
    const items = Array.from(menu.querySelectorAll(".model-option"));
    if (!items.length) {
      state.modelActive = -1;
      input.removeAttribute("aria-activedescendant");
      return;
    }
    state.modelActive = Math.max(0, Math.min(index, items.length - 1));
    items.forEach((item, itemIndex) => {
      const active = itemIndex === state.modelActive;
      item.dataset.active = active ? "true" : "false";
      if (active) {
        input.setAttribute("aria-activedescendant", item.id);
        item.scrollIntoView({ block: "nearest" });
      }
    });
  }

  function moveModelActive(delta) {
    const menu = $("model-options");
    if (!menu) return;
    const total = menu.querySelectorAll(".model-option").length;
    if (!total) return;
    const current = state.modelActive < 0 ? 0 : state.modelActive;
    setActiveModel((current + delta + total) % total);
  }

  function renderModelMenu() {
    const menu = $("model-options");
    const input = $("model-input");
    if (!menu || !input || menu.hidden) return;
    while (menu.firstChild) menu.removeChild(menu.firstChild);

    const query = input.value.trim();
    const suggestions = modelOptionsFor(activeRunner()).filter((option) => modelMatch(option, query));
    const items = [];
    if (!query) items.push({ id: "", label: t("automation.model_default") });
    items.push(...suggestions);

    items.forEach((entry, index) => {
      const option = document.createElement("div");
      option.className = "model-option";
      option.id = `model-option-${index}`;
      option.setAttribute("role", "option");
      option.dataset.modelValue = entry.id;
      option.setAttribute("aria-selected", entry.id === input.value.trim() ? "true" : "false");
      const detail = entry.provider ? `${entry.provider} · ${entry.id}` : entry.id;
      option.setAttribute("aria-label", entry.id ? `${entry.label} (${detail})` : entry.label);
      const label = document.createElement("span");
      label.className = "model-option-label";
      label.textContent = entry.label;
      option.appendChild(label);
      if (entry.id) {
        const id = document.createElement("span");
        id.className = "model-option-id";
        id.textContent = detail;
        option.appendChild(id);
      }
      menu.appendChild(option);
    });

    if (!suggestions.length) {
      const empty = document.createElement("p");
      empty.className = "model-option-empty";
      empty.setAttribute("role", "presentation");
      empty.textContent = t("automation.model_none");
      menu.appendChild(empty);
    }

    const current = items.findIndex((entry) => entry.id === input.value.trim());
    setActiveModel(current >= 0 ? current : 0);
  }

  function openModelMenu() {
    const menu = $("model-options");
    const input = $("model-input");
    const toggle = $("model-toggle");
    if (!menu || !input || !toggle) return;
    state.modelMenuOpen = true;
    menu.hidden = false;
    input.setAttribute("aria-expanded", "true");
    toggle.setAttribute("aria-expanded", "true");
    renderModelMenu();
  }

  function chooseModel(value) {
    const input = $("model-input");
    if (!input) return;
    input.value = value;
    state.modelValues[activeRunner()] = value;
    closeModelMenu();
    input.focus();
  }

  function updateRunnerUi(data) {
    const select = $("runner-select");
    const hint = $("runner-hint");
    if (!select) return;

    const codex = runnerAvailable("codex", data);
    const dsh = runnerAvailable("dsh", data);
    Array.from(select.options).forEach((option) => {
      const available = option.value === "codex" ? codex : option.value === "dsh" ? dsh : false;
      option.disabled = !available;
    });

    let selected = select.value;
    if ((selected === "codex" && !codex) || (selected === "dsh" && !dsh)) {
      selected = codex ? "codex" : dsh ? "dsh" : "codex";
      select.value = selected;
    }
    const available = selected === "codex" ? codex : selected === "dsh" ? dsh : false;
    if (hint) {
      hint.hidden = available;
      hint.textContent = available ? "" : t("runner.unavailable");
    }

    const previous = select.dataset.activeRunner;
    const model = $("model-input");
    if (model) {
      if (previous && previous !== selected) {
        state.modelValues[previous] = model.value;
        model.value = state.modelValues[selected] || "";
        closeModelMenu();
      }
      model.disabled = false;
      model.placeholder = t("automation.model_placeholder");
    }
    select.dataset.activeRunner = selected;
  }

  async function runAutomationNow() {
    if (state.automationRunning) return;
    state.automationRunning = true;
    const button = $("run-now-button");
    setBusy(button, "automation.running");
    setMessage("automation", null);
    const runner = activeRunner();
    const body = { runner };
    const model = $("model-input").value.trim();
    if (model) body.model = model;
    try {
      const accepted = await api("/api/automation/run", { method: "POST", body });
      const job = await pollJob(accepted.job_id, {
        region: "automation",
        output: "automation-output",
        successKey: "automation.completed",
        failureKey: "automation.failed",
        maxAttempts: 1800,
      });
      const run = job && job.result ? job.result : {};
      if (run.status === "pushed") setMessage("automation", "automation.pushed");
      else if (run.status === "no_changes") setMessage("automation", "automation.no_changes");
      else if (run.status === "skipped") setMessage("automation", "automation.skipped");
      else if (run.status === "failed") setMessage("automation", "automation.failed", { error: true });
      else setMessage("automation", "automation.completed");
      await refreshAll();
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("automation", errorKey(error, "automation.failed"), { error: true });
        renderErrorRaw("automation-output", error);
      }
    } finally {
      state.automationRunning = false;
      clearBusy(button);
    }
  }

  /* ------------------------------------------------------------------ jobs */

  function pollJob(jobId, options) {
    let attempts = 0;
    const maxAttempts = Number(options.maxAttempts) || 600;
    return new Promise((resolve, reject) => {
      const tick = async () => {
        attempts += 1;
        if (attempts > maxAttempts) {
          setMessage(options.region, "error.generic", { error: true });
          reject(new ApiError("timeout", 0, null));
          return;
        }
        try {
          const job = await api(`/api/jobs/${encodeURIComponent(jobId)}`);
          if (job.status === "queued" || job.status === "running") {
            setMessage(options.region, "common.running");
            state.timers.job = window.setTimeout(tick, 1000);
            return;
          }
          if (job.status === "succeeded") {
            if (options.output) renderRaw(options.output, job.result === undefined ? job : job.result);
            setMessage(options.region, options.successKey);
            await refreshAll();
            resolve(job);
            return;
          }
          if (options.output) renderRaw(options.output, { status: job.status, error: job.error || null });
          setMessage(options.region, options.failureKey, { error: true });
          reject(new ApiError(job.error || "operation_failed", 0, job));
        } catch (error) {
          if (error.code === "authentication_required") {
            reject(error);
            return;
          }
          setMessage(options.region, errorKey(error, "error.generic"), { error: true });
          if (options.output) renderErrorRaw(options.output, error);
          reject(error);
        }
      };
      tick();
    });
  }

  /* --------------------------------------------------------------- state */

  function renderState(data) {
    if (!data) return;
    const storage = data.storage || {};
    const credential = typeof storage.auth === "string" ? storage.auth : "missing";

    const repository = data.repository || {};
    const connected = Boolean(repository.connected);
    const repositoryBadge = $("repository-badge");
    repositoryBadge.dataset.state = connected ? "connected" : "not_connected";
    repositoryBadge.textContent = t(connected ? "repo.connected" : "repo.not_connected");
    $("repository-summary").hidden = !connected;
    $("repo-current-name").textContent = connected ? repository.full_name || "—" : "—";
    $("repo-current-branch").textContent = connected ? repository.branch || "—" : "—";
    $("repo-current-head").textContent = connected && repository.head ? String(repository.head).slice(0, 8) : "—";

    $("fact-git").textContent = t(`state.credential_${credential}`);
    $("fact-repository").textContent = connected ? repository.full_name || "—" : t("repo.none");
    $("fact-branch").textContent = repository.branch || "—";
    $("fact-head").textContent = repository.head ? String(repository.head).slice(0, 8) : "—";

    const model = data.model || {};
    $("fact-model").textContent = t(model.available ? "state.available" : "state.unavailable");

    const service = data.service || {};
    $("fact-scheduler").textContent = t(service.scheduler_enabled ? "state.enabled" : "state.disabled");
    const scheduler = data.scheduler || {};
    const latest = state.automation && state.automation.latest && state.automation.latest.run_id ? state.automation.latest : null;
    const lastRun = latest ? latest.finished_at || latest.started_at : scheduler.finished_at;
    $("fact-last-run").textContent = lastRun ? formatTime(lastRun) || lastRun : t("automation.never");

    updateRunnerUi(data);
    renderAutomationView();
  }

  async function refreshState() {
    const data = await api("/api/state");
    state.lastState = data;
    renderState(data);
    return data;
  }

  async function refreshAll() {
    try {
      await refreshState();
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("automation", errorKey(error, "error.generic"), { error: true });
      }
    }
    await loadTaskKinds();
    await refreshAutomation({ silent: true });
    await refreshChannels();
    await refreshHooks();
    await refreshUsage();
    await refreshTasks();
  }

  /* -------------------------------------------------------------- channels */

  const BARK_LEVELS = ["passive", "active", "timeSensitive"];

  function barkBadge(bark) {
    const badge = $("bark-badge");
    if (!badge || !bark) return;
    if (!bark.configured) {
      badge.dataset.state = "disabled";
      badge.textContent = t("channels.badge.unconfigured");
      return;
    }
    badge.dataset.state = bark.enabled ? "enabled" : "disabled";
    badge.textContent = t(bark.enabled ? "channels.badge.enabled" : "channels.badge.disabled");
  }

  function barkKeyHint(bark) {
    const hint = $("bark-key-hint");
    if (!hint || !bark) return;
    hint.textContent = t(bark.configured ? "channels.device_key_stored" : "channels.device_key_missing");
  }

  // Channel texts follow the language, while the inputs keep whatever the
  // owner has typed. Language changes therefore only re-render the texts.
  function renderChannelTexts(data) {
    if (!data) return;
    barkBadge(data.bark);
    barkKeyHint(data.bark);
    renderDeliveries(data.deliveries);
  }

  function renderChannelValues(data) {
    if (!data) return;
    const bark = data.bark || {};
    if ($("bark-enabled")) $("bark-enabled").checked = Boolean(bark.enabled);
    if ($("bark-server")) $("bark-server").value = typeof bark.server === "string" ? bark.server : "";
    if ($("bark-group")) $("bark-group").value = typeof bark.group === "string" ? bark.group : "";
    if ($("bark-level")) $("bark-level").value = BARK_LEVELS.includes(bark.level) ? bark.level : "active";
    if ($("bark-language")) $("bark-language").value = data.language === "zh" ? "zh" : "en";
    if ($("bark-key")) $("bark-key").value = "";
  }

  function renderChannelState(data) {
    state.channels = data;
    renderChannelValues(data);
    renderChannelTexts(data);
  }

  function deliveryKindKey(kind) {
    switch (kind) {
      case "test":
        return "channels.kind.test";
      case "run:pushed":
        return "channels.kind.run.pushed";
      case "run:failed":
        return "channels.kind.run.failed";
      default:
        return "";
    }
  }

  function renderDeliveries(deliveries) {
    const list = $("deliveries-list");
    if (!list) return;
    while (list.firstChild) list.removeChild(list.firstChild);
    const items = Array.isArray(deliveries) ? deliveries : [];
    if (!items.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("channels.none");
      list.appendChild(empty);
      return;
    }
    items.forEach((item) => {
      if (!item || typeof item !== "object") return;
      const row = document.createElement("div");
      row.className = "delivery-item";
      const head = document.createElement("div");
      head.className = "delivery-head";
      const kind = document.createElement("span");
      kind.className = "delivery-kind";
      const kindKey = deliveryKindKey(item.kind);
      kind.textContent = kindKey ? t(kindKey) : item.kind || "—";
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.dataset.state = item.status === "delivered" ? "enabled" : "failed";
      badge.textContent = t(item.status === "delivered" ? "channels.status.delivered" : "channels.status.failed");
      head.appendChild(kind);
      head.appendChild(badge);
      const meta = document.createElement("p");
      meta.className = "delivery-meta";
      const parts = [];
      if (typeof item.title === "string" && item.title) parts.push(item.title);
      if (typeof item.detail === "string" && item.detail) parts.push(item.detail);
      const at = formatTime(item.at);
      if (at) parts.push(at);
      meta.textContent = parts.join(" · ");
      row.appendChild(head);
      row.appendChild(meta);
      list.appendChild(row);
    });
  }

  async function refreshChannels() {
    try {
      const data = await api("/api/channels");
      renderChannelState(data);
    } catch (error) {
      if (error.code === "authentication_required") return;
      setMessage("channels", errorKey(error, "error.generic"), { error: true });
    }
  }

  function barkPayload() {
    return {
      enabled: Boolean($("bark-enabled") && $("bark-enabled").checked),
      server: $("bark-server") ? $("bark-server").value.trim() : "",
      device_key: $("bark-key") ? $("bark-key").value : "",
      group: $("bark-group") ? $("bark-group").value.trim() : "",
      level: $("bark-level") ? $("bark-level").value : "active",
      language: $("bark-language") ? $("bark-language").value : "en",
    };
  }

  async function saveBark(event) {
    event.preventDefault();
    if (state.channelsSaving) return;
    state.channelsSaving = true;
    const button = $("bark-save");
    setBusy(button, "channels.saving");
    setMessage("channels", null);
    try {
      const result = await api("/api/channels/bark", { method: "POST", body: barkPayload() });
      renderChannelState(result);
      setMessage("channels", "channels.saved");
    } catch (error) {
      setMessage("channels", errorKey(error, "channels.save_failed"), { error: true });
    } finally {
      state.channelsSaving = false;
      clearBusy(button);
    }
  }

  async function testBark() {
    if (state.channelsTesting) return;
    state.channelsTesting = true;
    const button = $("bark-test");
    setBusy(button, "channels.testing");
    setMessage("channels", null);
    try {
      // Test the values on screen, so a key can be verified before it is
      // persisted. The backend falls back to the stored key when blank.
      const result = await api("/api/channels/bark/test", {
        method: "POST",
        body: {
          server: $("bark-server") ? $("bark-server").value.trim() : "",
          device_key: $("bark-key") ? $("bark-key").value : "",
        },
      });
      if (result.state) renderChannelTexts(result.state);
      const receipt = result.receipt || {};
      if (receipt.status === "delivered") {
        setMessage("channels", "channels.test_sent");
      } else {
        setMessage("channels", "channels.test_failed", {
          vars: { detail: receipt.detail || "failed" },
          error: true,
        });
      }
    } catch (error) {
      setMessage("channels", errorKey(error, "channels.save_failed"), { error: true });
    } finally {
      state.channelsTesting = false;
      clearBusy(button);
    }
  }

  /* ----------------------------------------------------------------- hooks */

  function renderHookCount(hooks) {
    const badge = $("hooks-count");
    if (!badge) return;
    const items = Array.isArray(hooks) ? hooks : [];
    const active = items.filter((item) => item && item.enabled).length;
    badge.dataset.state = active > 0 ? "enabled" : "disabled";
    badge.textContent = `${active}/${items.length}`;
  }

  // Task kinds are data. The hook form lists whatever data/task-kinds.json
  // defines, so the owner adds a kind without touching this file.
  function kindLabel(id) {
    const kind = (state.kinds || []).find((item) => item && item.id === id);
    return (kind && kind.label) || id || "";
  }

  async function loadTaskKinds() {
    try {
      const data = await api("/api/task-kinds");
      state.kinds = Array.isArray(data.kinds) ? data.kinds : [];
    } catch (error) {
      state.kinds = state.kinds || [];
      return;
    }
    const select = $("hook-action");
    if (!select) return;
    const current = select.value;
    while (select.firstChild) select.removeChild(select.firstChild);
    state.kinds.forEach((kind) => {
      if (!kind || typeof kind.id !== "string") return;
      const option = document.createElement("option");
      option.value = kind.id;
      option.textContent = kind.label || kind.id;
      select.appendChild(option);
    });
    if (state.kinds.some((kind) => kind.id === current)) select.value = current;
  }

  function hookButton(hook, action, labelKey) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = action === "delete" ? "btn ghost small danger" : "btn ghost small";
    button.dataset.hookAction = action;
    button.dataset.hookId = hook.id;
    const label = document.createElement("span");
    label.className = "btn-label";
    label.textContent = t(labelKey);
    button.appendChild(label);
    return button;
  }

  function hookPrioritySelect(hook) {
    const select = document.createElement("select");
    select.className = "hook-priority";
    select.setAttribute("aria-label", t("hooks.priority"));
    ["P0", "P1", "P2", "P3"].forEach((priority) => {
      const option = document.createElement("option");
      option.value = priority;
      option.textContent = priority;
      select.appendChild(option);
    });
    select.value = hook.priority || "P2";
    select.addEventListener("change", () => updateHookPriority(hook, select.value));
    return select;
  }

  async function updateHookPriority(hook, priority) {
    if (state.hookBusy) return;
    state.hookBusy = `priority:${hook.id}`;
    try {
      const result = await api(`/api/hooks/${encodeURIComponent(hook.id)}/update`, {
        method: "POST",
        body: { enabled: !!hook.enabled, name: hook.name || "", priority },
      });
      if (Array.isArray(result.hooks)) renderHooks({ hooks: result.hooks });
      setMessage("hooks", "hooks.updated");
    } catch (error) {
      setMessage("hooks", errorKey(error, "hooks.failed"), { error: true });
    } finally {
      state.hookBusy = "";
    }
  }

  function renderHooks(data) {
    const list = $("hooks-list");
    if (!list) return;
    const hooks = Array.isArray(data && data.hooks) ? data.hooks : [];
    state.hooks = hooks;
    renderHookCount(hooks);
    while (list.firstChild) list.removeChild(list.firstChild);
    if (!hooks.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("hooks.empty");
      list.appendChild(empty);
      return;
    }
    hooks.forEach((hook) => {
      if (!hook || typeof hook.id !== "string") return;
      const row = document.createElement("div");
      row.className = "hook-item";
      row.dataset.hookId = hook.id;

      const head = document.createElement("div");
      head.className = "hook-head";
      const name = document.createElement("span");
      name.className = "hook-name";
      name.textContent = hook.name || hook.id;
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.dataset.state = hook.enabled ? "enabled" : "disabled";
      badge.textContent = t(hook.enabled ? "hooks.enabled" : "hooks.disabled");
      head.appendChild(name);
      head.appendChild(badge);

      const meta = document.createElement("p");
      meta.className = "hook-meta";
      const parts = [`/api/hooks/${hook.id}`, kindLabel(hook.action)];
      if (hook.calls > 0) parts.push(t("hooks.calls", { count: hook.calls }));
      parts.push(
        hook.last_called_at
          ? `${t("hooks.last_called")} ${formatTime(hook.last_called_at)}`
          : t("hooks.never"),
      );
      meta.textContent = parts.join(" · ");

      const actions = document.createElement("div");
      actions.className = "hook-actions";
      actions.appendChild(hookPrioritySelect(hook));
      actions.appendChild(hookButton(hook, "toggle", hook.enabled ? "hooks.disable" : "hooks.enable"));
      actions.appendChild(hookButton(hook, "rotate", "hooks.rotate"));
      actions.appendChild(hookButton(hook, "delete", "hooks.delete"));

      row.appendChild(head);
      row.appendChild(meta);
      row.appendChild(actions);
      list.appendChild(row);
    });
  }

  async function refreshHooks() {
    try {
      const data = await api("/api/hooks");
      renderHooks(data);
    } catch (error) {
      if (error.code === "authentication_required") return;
      setMessage("hooks", errorKey(error, "hooks.failed"), { error: true });
    }
  }

  function showHookSecret(secret, hookID) {
    state.hookSecret = typeof secret === "string" ? secret : "";
    state.hookSecretFor = state.hookSecret ? hookID || "" : "";
    const panel = $("hook-secret-panel");
    const value = $("hook-secret-value");
    if (!panel || !value) return;
    value.textContent = state.hookSecret;
    panel.hidden = !state.hookSecret;
  }

  async function createHook(event) {
    event.preventDefault();
    if (state.hookBusy) return;
    const input = $("hook-name");
    const name = input ? input.value.trim() : "";
    if (!name) {
      setMessage("hooks", "hooks.invalid_name", { error: true });
      if (input) input.focus();
      return;
    }
    state.hookBusy = "create";
    const button = $("hook-create");
    setBusy(button, "hooks.creating");
    setMessage("hooks", null);
    const priorityInput = $("hook-priority");
    const priority = priorityInput ? priorityInput.value : "P2";
    const actionInput = $("hook-action");
    const action = actionInput && actionInput.value ? actionInput.value : "maintenance";
    try {
      const result = await api("/api/hooks", { method: "POST", body: { name, action, priority } });
      if (input) input.value = "";
      if (Array.isArray(result.hooks)) renderHooks({ hooks: result.hooks });
      const hook = result.hook && typeof result.hook.id === "string" ? result.hook.id : "";
      showHookSecret(result.secret, hook);
      setMessage("hooks", "hooks.created");
    } catch (error) {
      setMessage("hooks", errorKey(error, "hooks.failed"), { error: true });
    } finally {
      state.hookBusy = "";
      clearBusy(button);
    }
  }

  async function hookAction(button) {
    const action = button.dataset.hookAction;
    const id = button.dataset.hookId;
    if (!action || !id || state.hookBusy) return;
    const hook = (state.hooks || []).find((item) => item && item.id === id) || null;
    if (action === "delete" && !window.confirm(t("hooks.confirm_delete"))) return;
    state.hookBusy = `${action}:${id}`;
    setBusy(button, action === "rotate" ? "hooks.rotating" : "common.running");
    setMessage("hooks", null);
    const path = `/api/hooks/${encodeURIComponent(id)}`;
    try {
      if (action === "toggle") {
        const result = await api(`${path}/update`, {
          method: "POST",
          body: { enabled: !(hook && hook.enabled), name: hook ? hook.name : "" },
        });
        renderHooks(result);
        setMessage("hooks", "hooks.updated");
      } else if (action === "rotate") {
        const result = await api(`${path}/rotate`, { method: "POST", body: {} });
        if (Array.isArray(result.hooks)) renderHooks({ hooks: result.hooks });
        showHookSecret(result.secret, id);
        setMessage("hooks", "hooks.rotated");
      } else if (action === "delete") {
        const result = await api(`${path}/delete`, { method: "POST", body: {} });
        renderHooks(result);
        if (state.hookSecretFor === id) showHookSecret("", "");
        setMessage("hooks", "hooks.deleted");
      }
    } catch (error) {
      setMessage("hooks", errorKey(error, "hooks.failed"), { error: true });
    } finally {
      state.hookBusy = "";
      clearBusy(button);
    }
  }

  async function copyHookSecret() {
    if (!state.hookSecret) return;
    let copied = false;
    try {
      if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
        await navigator.clipboard.writeText(state.hookSecret);
        copied = true;
      }
    } catch (_) {
      copied = false;
    }
    if (!copied) copied = fallbackCopy($("hook-secret-value"));
    const button = $("hook-secret-copy");
    const label = button ? button.querySelector(".btn-label") : null;
    if (!label) return;
    label.textContent = t(copied ? "common.copied" : "common.copy_failed");
    clearTimer("copy");
    state.timers.copy = window.setTimeout(() => {
      label.textContent = t("common.copy");
    }, 2000);
  }

  /* ----------------------------------------------------------------- usage */

  function usageLocale() {
    const lang = document.documentElement.lang;
    return typeof lang === "string" && lang ? lang : "en";
  }

  function usageNumber(value) {
    const number = Number(value);
    return new Intl.NumberFormat(usageLocale()).format(Number.isFinite(number) ? number : 0);
  }

  function usageCompact(value) {
    const number = Number(value);
    return new Intl.NumberFormat(usageLocale(), {
      notation: "compact",
      maximumFractionDigits: 1,
    }).format(Number.isFinite(number) ? number : 0);
  }

  function setUsageText(id, text) {
    const node = $(id);
    if (node) node.textContent = text;
  }

  function renderUsageDays(days) {
    const list = $("usage-days");
    if (!list) return;
    while (list.firstChild) list.removeChild(list.firstChild);
    if (!days.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("usage.empty");
      list.appendChild(empty);
      return;
    }
    let peak = 0;
    days.forEach((day) => {
      const total = Number(day && day.total_tokens) || 0;
      if (total > peak) peak = total;
    });
    days.forEach((day) => {
      const total = Number(day && day.total_tokens) || 0;
      const row = document.createElement("div");
      row.className = "usage-day";

      const label = document.createElement("span");
      label.className = "usage-day-label";
      const date = typeof day.date === "string" ? day.date : "";
      label.textContent = date.length === 10 ? date.slice(5) : date;

      const track = document.createElement("span");
      track.className = "usage-bar";
      const fill = document.createElement("span");
      fill.className = "usage-bar-fill";
      fill.style.width = `${peak > 0 ? Math.max(2, Math.round((total / peak) * 100)) : 0}%`;
      track.appendChild(fill);

      const value = document.createElement("span");
      value.className = "usage-day-value";
      value.textContent = usageCompact(total);

      row.appendChild(label);
      row.appendChild(track);
      row.appendChild(value);
      list.appendChild(row);
    });
  }

  function renderUsageModels(models) {
    const body = $("usage-models");
    if (!body) return;
    while (body.firstChild) body.removeChild(body.firstChild);
    models.forEach((model) => {
      const row = document.createElement("tr");
      const runner = typeof model.runner === "string" ? model.runner : "";
      const name = typeof model.model === "string" ? model.model : "";
      const cells = [
        name ? `${runner} / ${name}` : runner,
        usageNumber(model.total_tokens),
        usageNumber(model.input_tokens),
        usageNumber(model.cached_tokens),
        usageNumber(model.output_tokens),
        usageNumber(model.runs),
      ];
      cells.forEach((text, index) => {
        const cell = document.createElement(index === 0 ? "th" : "td");
        if (index === 0) cell.setAttribute("scope", "row");
        cell.textContent = text;
        row.appendChild(cell);
      });
      body.appendChild(row);
    });
    if (!models.length) {
      const row = document.createElement("tr");
      const cell = document.createElement("td");
      cell.colSpan = 6;
      cell.className = "hint";
      cell.textContent = t("usage.empty");
      row.appendChild(cell);
      body.appendChild(row);
    }
  }

  function renderUsage(data) {
    state.usage = data || null;
    const totals = (data && data.totals) || {};
    setUsageText("usage-total", usageNumber(totals.total_tokens));
    setUsageText("usage-input", usageNumber(totals.input_tokens));
    setUsageText("usage-output", usageNumber(totals.output_tokens));
    setUsageText("usage-runs", usageNumber(totals.runs));
    const note = $("usage-note");
    if (note) {
      note.textContent = data
        ? t("usage.note", {
            reported: usageNumber(totals.reported_runs),
            runs: usageNumber(totals.runs),
            missing: usageNumber(totals.unreported_runs),
          })
        : "";
    }
    renderUsageDays(Array.isArray(data && data.days) ? data.days : []);
    renderUsageModels(Array.isArray(data && data.models) ? data.models : []);
  }

  async function refreshUsage() {
    try {
      const data = await api(`/api/usage?days=${encodeURIComponent(state.usageWindow)}`);
      renderUsage(data);
    } catch (error) {
      if (error.code === "authentication_required") return;
      setMessage("usage", errorKey(error, "usage.failed"), { error: true });
    }
  }

  /* ----------------------------------------------------------------- tasks */

  function taskStatusKey(status) {
    switch (status) {
      case "running":
        return "tasks.status.running";
      case "done":
        return "tasks.status.done";
      case "failed":
        return "tasks.status.failed";
      case "canceled":
        return "tasks.status.canceled";
      default:
        return "tasks.status.queued";
    }
  }

  function taskButton(task, action, labelKey) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = action === "cancel" ? "btn ghost small danger" : "btn ghost small";
    button.dataset.taskAction = action;
    button.dataset.taskId = task.id;
    const label = document.createElement("span");
    label.className = "btn-label";
    label.textContent = t(labelKey);
    button.appendChild(label);
    return button;
  }

  function renderTasks(data) {
    state.tasks = data || null;
    const list = $("tasks-list");
    if (!list) return;
    const tasks = Array.isArray(data && data.tasks) ? data.tasks : [];
    const counts = (data && data.counts) || {};
    const pending = (counts.queued || 0) + (counts.running || 0);
    const badge = $("tasks-count");
    if (badge) {
      badge.dataset.state = pending > 0 ? "enabled" : "disabled";
      badge.textContent = String(pending);
    }
    const pauseButton = $("tasks-pause");
    if (pauseButton) {
      const label = pauseButton.querySelector(".btn-label");
      if (label) label.textContent = t(data && data.paused ? "tasks.resume" : "tasks.pause");
      pauseButton.dataset.paused = data && data.paused ? "true" : "false";
    }
    const policy = (data && data.policy) || {};
    const policyLine = $("tasks-policy");
    if (policyLine) {
      policyLine.textContent = data
        ? t(policy.in_window ? "tasks.policy_inside" : "tasks.policy_outside", {
            start: policy.window_start || "",
            end: policy.window_end || "",
          })
        : "";
    }
    while (list.firstChild) list.removeChild(list.firstChild);
    if (!tasks.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("tasks.empty");
      list.appendChild(empty);
      return;
    }
    tasks.forEach((task) => {
      if (!task || typeof task.id !== "string") return;
      const row = document.createElement("div");
      row.className = "task-item";

      const head = document.createElement("div");
      head.className = "task-head";
      const chip = document.createElement("span");
      chip.className = "task-priority";
      chip.dataset.priority = task.priority || "P2";
      chip.textContent = task.priority || "P2";
      const title = document.createElement("span");
      title.className = "task-title";
      title.textContent = task.title || task.kind || task.id;
      const status = document.createElement("span");
      status.className = "badge";
      status.dataset.state =
        task.status === "running" ? "enabled" : task.status === "failed" ? "disabled" : "neutral";
      status.textContent = t(taskStatusKey(task.status));
      head.appendChild(chip);
      head.appendChild(title);
      head.appendChild(status);

      const meta = document.createElement("p");
      meta.className = "task-meta";
      const parts = [kindLabel(task.kind), task.source];
      if (task.created_at) {
        parts.push(t("tasks.created", { time: formatTime(task.created_at) }));
      }
      if (task.not_before && !task.due && task.status === "queued") {
        parts.push(t("tasks.not_before", { time: formatTime(task.not_before) }));
      }
      if (task.attempts > 0) {
        parts.push(t("tasks.attempts", { count: task.attempts }));
      }
      if (task.payload && typeof task.payload === "object") {
        Object.entries(task.payload)
          .filter(([key, value]) => key !== "title" && ["string", "number", "boolean"].includes(typeof value))
          .slice(0, 3)
          .forEach(([key, value]) => {
            const text = String(value);
            parts.push(`${key}=${text.length > 40 ? `${text.slice(0, 40)}…` : text}`);
          });
      }
      if (task.reason) {
        parts.push(task.reason);
      }
      meta.textContent = parts.filter(Boolean).join(" · ");

      const actions = document.createElement("div");
      actions.className = "task-actions";
      if (task.status === "queued") {
        actions.appendChild(taskButton(task, "toggle", task.enabled ? "tasks.action.pause" : "tasks.action.resume"));
        actions.appendChild(taskButton(task, "run", "tasks.action.run"));
        actions.appendChild(taskButton(task, "cancel", "tasks.action.cancel"));
      } else if (task.status !== "running") {
        actions.appendChild(taskButton(task, "run", "tasks.action.run"));
      }

      row.appendChild(head);
      row.appendChild(meta);
      if (actions.childNodes.length) row.appendChild(actions);
      list.appendChild(row);
    });
  }

  async function refreshTasks() {
    try {
      const data = await api("/api/tasks");
      renderTasks(data);
    } catch (error) {
      if (error.code === "authentication_required") return;
      setMessage("tasks", errorKey(error, "tasks.failed"), { error: true });
    }
  }

  async function toggleTasksPaused() {
    if (state.tasksBusy) return;
    const paused = !!((state.tasks && state.tasks.paused) || false);
    state.tasksBusy = "pause";
    try {
      const data = await api("/api/tasks/pause", { method: "POST", body: { paused: !paused } });
      renderTasks(data);
      setMessage("tasks", null);
    } catch (error) {
      setMessage("tasks", errorKey(error, "tasks.failed"), { error: true });
    } finally {
      state.tasksBusy = "";
    }
  }

  async function taskAction(button) {
    const action = button.dataset.taskAction;
    const id = button.dataset.taskId;
    if (!action || !id || state.tasksBusy) return;
    const task = (state.tasks && state.tasks.tasks ? state.tasks.tasks : []).find(
      (item) => item && item.id === id,
    ) || null;
    if (action === "cancel" && !window.confirm(t("tasks.confirm_cancel"))) return;
    state.tasksBusy = `${action}:${id}`;
    setBusy(button, action === "run" ? "tasks.action.run" : "common.running");
    setMessage("tasks", null);
    try {
      let body = {};
      if (action === "toggle") {
        body = { enabled: !(task && task.enabled) };
      }
      const data = await api(`/api/tasks/${encodeURIComponent(id)}/${action === "toggle" ? "update" : action}`, {
        method: "POST",
        body,
      });
      renderTasks(data);
    } catch (error) {
      setMessage("tasks", errorKey(error, "tasks.failed"), { error: true });
    } finally {
      state.tasksBusy = "";
      clearBusy(button);
    }
  }

  /* ----------------------------------------------------------- run display */

  function statusKey(status) {
    switch (status) {
      case "pushed":
        return "automation.status.pushed";
      case "no_changes":
        return "automation.status.no_changes";
      case "skipped":
        return "automation.status.skipped";
      case "failed":
        return "automation.status.failed";
      case "running":
        return "automation.status.running";
      default:
        return "automation.status.unknown";
    }
  }

  function triggerKey(trigger) {
    if (trigger === "scheduler") return "automation.trigger.scheduler";
    if (trigger === "hook") return "automation.trigger.hook";
    if (trigger === "task") return "automation.trigger.task";
    return "automation.trigger.manual";
  }

  function formatInterval(seconds) {
    const value = Number(seconds);
    if (!Number.isFinite(value) || value <= 0) return "—";
    if (value % 86400 === 0) return `${value / 86400}d`;
    if (value % 3600 === 0) return `${value / 3600}h`;
    if (value % 60 === 0) return `${value / 60}m`;
    return `${value}s`;
  }

  function formatDuration(milliseconds) {
    const value = Number(milliseconds);
    if (!Number.isFinite(value) || value < 0) return "—";
    if (value < 1000) return `${Math.round(value)} ms`;
    if (value < 60000) return `${(value / 1000).toFixed(1)} s`;
    return `${Math.floor(value / 60000)}m ${Math.round((value % 60000) / 1000)}s`;
  }

  function shortHash(value) {
    return typeof value === "string" && value ? value.slice(0, 8) : "—";
  }

  function reasonText(reason) {
    if (!reason) return "";
    return translateKnown(`automation.reason.${reason}`, reason);
  }

  function addRunMeta(parent, label, value) {
    const item = document.createElement("div");
    item.className = "run-meta-item";
    const term = document.createElement("dt");
    term.textContent = label;
    const description = document.createElement("dd");
    description.textContent = value || "—";
    item.appendChild(term);
    item.appendChild(description);
    parent.appendChild(item);
  }

  function renderDiffText(output, text) {
    if (!output) return;
    output.textContent = "";
    const lines = typeof text === "string" ? text.split("\n") : [];
    const limit = 2000;
    const shown = lines.slice(0, limit);
    shown.forEach((line) => {
      const span = document.createElement("span");
      span.className = "diff-line";
      if (line.startsWith("@@")) span.classList.add("diff-hunk");
      else if (line.startsWith("+++") || line.startsWith("---")) span.classList.add("diff-meta");
      else if (line.startsWith("+")) span.classList.add("diff-add");
      else if (line.startsWith("-")) span.classList.add("diff-del");
      span.textContent = line + "\n";
      output.appendChild(span);
    });
    if (lines.length > limit) {
      const note = document.createElement("span");
      note.className = "diff-note";
      note.textContent = t("automation.diff_truncated", { lines: limit });
      output.appendChild(note);
    }
  }

  function renderLatestRun(run) {
    const container = $("latest-run");
    if (!container) return;
    while (container.firstChild) container.removeChild(container.firstChild);
    if (!run || !run.run_id) {
      const empty = document.createElement("p");
      empty.className = "empty-state";
      empty.textContent = t("automation.no_runs");
      container.appendChild(empty);
      return;
    }

    const head = document.createElement("div");
    head.className = "run-head";
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.dataset.state = typeof run.status === "string" ? run.status : "unknown";
    badge.textContent = t(statusKey(run.status));
    head.appendChild(badge);
    const trigger = document.createElement("span");
    trigger.className = "run-trigger";
    const when = run.finished_at || run.started_at;
    trigger.textContent = `${t(triggerKey(run.trigger))}${when ? " · " + (formatTime(when) || when) : ""}`;
    head.appendChild(trigger);
    container.appendChild(head);

    if (run.summary) {
      const summary = document.createElement("p");
      summary.className = "run-summary";
      summary.textContent = run.summary;
      container.appendChild(summary);
    }
    if (typeof run.report_text === "string" && run.report_text.trim()) {
      const heading = document.createElement("h3");
      heading.className = "run-report-heading";
      heading.textContent = t("automation.report_title");
      const report = document.createElement("pre");
      report.className = "run-report";
      report.textContent = run.report_text;
      container.appendChild(heading);
      container.appendChild(report);
    }
    if (Array.isArray(run.needs_review) && run.needs_review.length) {
      const heading = document.createElement("h3");
      heading.textContent = t("automation.needs_review");
      const list = document.createElement("ul");
      run.needs_review.forEach((issue) => {
        if (!issue || typeof issue.source !== "string" || typeof issue.reason !== "string") return;
        const item = document.createElement("li");
        item.textContent = `${issue.source}: ${translateKnown(`automation.finding.${issue.reason}`, issue.reason)}`;
        list.appendChild(item);
      });
      container.appendChild(heading);
      container.appendChild(list);
    }
    if (Number.isInteger(run.remaining) && run.remaining > 0) {
      const pending = document.createElement("p");
      pending.textContent = t("automation.remaining", { count: run.remaining });
      container.appendChild(pending);
    }
    if ((run.status === "failed" || run.status === "skipped") && run.reason) {
      const reason = document.createElement("p");
      reason.className = "run-reason";
      reason.textContent = reasonText(run.reason);
      container.appendChild(reason);
    }

    const meta = document.createElement("dl");
    meta.className = "run-meta";
    const pull = run.pull || {};
    if (run.stage) addRunMeta(meta, t("automation.stage"), translateKnown(`automation.stage.${run.stage}`, run.stage));
    addRunMeta(meta, t("automation.runner"), `${run.runner || "—"}${run.model ? " · " + run.model : ""}`);
    addRunMeta(meta, t("automation.pull"), pull.status ? `${pull.status} · ${shortHash(pull.head)}` : "—");
    addRunMeta(meta, t("automation.commit"), shortHash(run.commit));
    const push = run.push || {};
    addRunMeta(meta, t("automation.push"), push.status || "—");
    const edits = Array.isArray(run.edits) ? run.edits : [];
    addRunMeta(meta, t("automation.files"), String(edits.length));
    addRunMeta(meta, t("automation.duration"), run.status === "running" ? "—" : formatDuration(run.duration_ms));
    addRunMeta(meta, t("automation.input"), shortHash(run.input_digest));
    container.appendChild(meta);

    if (!edits.length) {
      const note = document.createElement("p");
      note.className = "hint";
      note.textContent = t("automation.no_files");
      container.appendChild(note);
      return;
    }

    const list = document.createElement("div");
    list.className = "run-edit-list";
    edits.forEach((edit) => {
      const card = document.createElement("article");
      card.className = "run-edit";

      const cardHead = document.createElement("div");
      cardHead.className = "run-edit-head";
      const target = document.createElement("code");
      target.textContent = edit.target || "—";
      cardHead.appendChild(target);
      const action = document.createElement("span");
      action.className = "badge";
      action.dataset.state = edit.action || "update";
      action.textContent = t(edit.action === "create" ? "automation.action.create" : "automation.action.update");
      cardHead.appendChild(action);
      if (edit.claim_type) {
        const claim = document.createElement("span");
        claim.className = "run-claim";
        claim.textContent = edit.claim_type;
        cardHead.appendChild(claim);
      }
      card.appendChild(cardHead);

      if (edit.reason) {
        const reason = document.createElement("p");
        reason.className = "run-edit-reason";
        reason.textContent = edit.reason;
        card.appendChild(reason);
      }
      if (edit.source) {
        const source = document.createElement("p");
        source.className = "run-edit-source";
        source.textContent = `${t("automation.source")}: ${edit.source}`;
        card.appendChild(source);
      }
      if (edit.diff) {
        const details = document.createElement("details");
        details.className = "run-diff";
        const summary = document.createElement("summary");
        summary.textContent = t("automation.diff");
        details.appendChild(summary);
        const output = document.createElement("pre");
        output.className = "output diff";
        renderDiffText(output, edit.diff);
        details.appendChild(output);
        card.appendChild(details);
      }
      list.appendChild(card);
    });
    container.appendChild(list);
  }

  function renderRunHistory(runs) {
    const list = $("runs-list");
    if (!list) return;
    while (list.firstChild) list.removeChild(list.firstChild);
    if (!Array.isArray(runs) || !runs.length) {
      const empty = document.createElement("p");
      empty.className = "empty-state";
      empty.textContent = t("automation.no_runs");
      list.appendChild(empty);
      return;
    }
    runs.forEach((run) => {
      const item = document.createElement("button");
      item.type = "button";
      item.className = "run-history-item";
      item.dataset.runId = run.run_id;
      if (state.selectedRunId === run.run_id) item.dataset.selected = "true";

      const head = document.createElement("span");
      head.className = "run-history-head";
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.dataset.state = run.status || "unknown";
      badge.textContent = t(statusKey(run.status));
      head.appendChild(badge);
      const when = document.createElement("span");
      when.className = "run-history-time";
      const time = run.finished_at || run.started_at;
      when.textContent = time ? formatTime(time) || time : "";
      head.appendChild(when);
      item.appendChild(head);

      const summary = document.createElement("span");
      summary.className = "run-history-summary";
      summary.textContent = run.summary || t(statusKey(run.status));
      item.appendChild(summary);

      const meta = document.createElement("span");
      meta.className = "run-history-meta";
      const runner = run.runner ? `${run.runner}${run.model ? " · " + run.model : ""}` : "—";
      meta.textContent = `${t(triggerKey(run.trigger))} · ${runner} · ${t("automation.files")}: ${run.files_changed || 0}`;
      item.appendChild(meta);

      list.appendChild(item);
    });
  }

  function renderAutomationView() {
    const service = (state.lastState && state.lastState.service) || {};
    const enabled = Boolean(service.scheduler_enabled);
    const badge = $("automation-badge");
    const latest = state.automation && state.automation.latest && state.automation.latest.run_id ? state.automation.latest : null;
    const jobs = Array.isArray(service.jobs) ? service.jobs : [];
    const running = Boolean(latest && latest.status === "running") || jobs.some((job) => job && job.status === "running" && (job.name === "scheduler:automation" || String(job.name).startsWith("automation:")));
    if (badge) {
      if (running) {
        badge.dataset.state = "running";
        badge.textContent = t("automation.status.running");
      } else {
        badge.dataset.state = enabled ? "enabled" : "disabled";
        badge.textContent = t(enabled ? "automation.on" : "automation.off");
      }
    }
    const schedule = $("automation-schedule");
    if (schedule) schedule.textContent = enabled ? t("automation.every", { value: formatInterval(service.scheduler_interval_seconds) }) : t("automation.disabled");
    const next = $("automation-next");
    if (next) next.textContent = enabled && service.scheduler_next_at ? formatTime(service.scheduler_next_at) || service.scheduler_next_at : t("automation.none");
    const scheduler = (state.lastState && state.lastState.scheduler) || {};
    const lastTime = latest ? latest.finished_at || latest.started_at : scheduler.finished_at;
    const lastText = lastTime ? formatTime(lastTime) || lastTime : t("automation.never");
    const last = $("automation-last");
    if (last) last.textContent = lastText;
    const sidebarLast = $("fact-last-run");
    if (sidebarLast) sidebarLast.textContent = lastText;

    const showingSelected = Boolean(latest && state.selectedRunId && latest.run_id !== state.selectedRunId);
    if (!showingSelected) renderLatestRun(latest);
    renderRunHistory(state.automation ? state.automation.runs : []);
    renderRaw("automation-output", { service: service, automation: state.automation });
  }

  async function refreshAutomation(options = {}) {
    if (state.automationLoading) return;
    state.automationLoading = true;
    try {
      const data = await api("/api/automation");
      state.automation = data;
      state.automationRuns = Array.isArray(data.runs) ? data.runs : [];
      state.automationError = false;
      const signature = JSON.stringify(data.models || {});
      if (signature !== state.modelCatalogSignature) {
        state.modelCatalogSignature = signature;
        if (state.lastState) updateRunnerUi(state.lastState);
        if (state.modelMenuOpen) renderModelMenu();
      }
      renderAutomationView();
    } catch (error) {
      if (error.code === "authentication_required") return;
      state.automationError = true;
      if (!options.silent) setMessage("automation", errorKey(error, "automation.load_failed"), { error: true });
      renderAutomationView();
    } finally {
      state.automationLoading = false;
    }
  }

  async function selectAutomationRun(runID) {
    if (!runID) return;
    state.selectedRunId = runID;
    const latest = state.automation && state.automation.latest && state.automation.latest.run_id ? state.automation.latest : null;
    if (latest && latest.run_id === runID) {
      renderLatestRun(latest);
      renderRunHistory(state.automation ? state.automation.runs : []);
      return;
    }
    const container = $("latest-run");
    if (container) {
      while (container.firstChild) container.removeChild(container.firstChild);
      const loading = document.createElement("p");
      loading.className = "empty-state";
      loading.textContent = t("automation.loading");
      container.appendChild(loading);
    }
    try {
      const detail = await api(`/api/automation/runs/${encodeURIComponent(runID)}`);
      renderLatestRun(detail);
      renderRunHistory(state.automation ? state.automation.runs : []);
    } catch (error) {
      if (error.code !== "authentication_required") setMessage("automation", errorKey(error, "automation.load_failed"), { error: true });
    }
  }

  /* ---------------------------------------------------------------- timers */

  function clearTimer(name) {
    const id = state.timers[name];
    if (!id) return;
    window.clearTimeout(id);
    window.clearInterval(id);
    state.timers[name] = null;
  }

  function stopAllTimers() {
    Object.keys(state.timers).forEach(clearTimer);
  }

  /* --------------------------------------------------------------- events */

  on("login-form", "submit", login);
  on("logout-button", "click", logout);
  on("sync-button", "click", syncRepository);
  on("automation-form", "submit", (event) => {
    event.preventDefault();
    runAutomationNow();
  });
  on("run-now-button", "click", runAutomationNow);
  on("automation-refresh", "click", async () => {
    const button = $("automation-refresh");
    setBusy(button, "common.refreshing");
    try {
      await refreshAutomation();
    } finally {
      clearBusy(button);
    }
  });
  on("runs-list", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-run-id]") : null;
    if (button) selectAutomationRun(button.dataset.runId);
  });
  on("runner-select", "change", () => updateRunnerUi(state.lastState));
  on("model-input", "click", () => {
    if (!state.modelMenuOpen) openModelMenu();
  });
  on("model-input", "input", () => {
    state.modelValues[activeRunner()] = $("model-input").value;
    if (state.modelMenuOpen) renderModelMenu();
  });
  on("model-input", "keydown", (event) => {
    const menu = $("model-options");
    if (!menu) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (!state.modelMenuOpen) openModelMenu();
      else moveModelActive(1);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      if (!state.modelMenuOpen) {
        openModelMenu();
        const total = menu.querySelectorAll(".model-option").length;
        if (total) setActiveModel(total - 1);
      } else {
        moveModelActive(-1);
      }
      return;
    }
    if (event.key === "Enter" && state.modelMenuOpen) {
      const options = menu.querySelectorAll(".model-option");
      if (state.modelActive >= 0 && state.modelActive < options.length) {
        event.preventDefault();
        chooseModel(options[state.modelActive].dataset.modelValue || "");
      }
      return;
    }
    if (event.key === "Escape" && state.modelMenuOpen) {
      event.preventDefault();
      closeModelMenu();
      return;
    }
    if (event.key === "Tab") closeModelMenu();
  });
  on("model-toggle", "click", () => {
    if (state.modelMenuOpen) closeModelMenu();
    else openModelMenu();
    $("model-input").focus();
  });
  on("model-options", "mousedown", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (target && target.closest(".model-option")) event.preventDefault();
  });
  on("model-options", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const option = target ? target.closest(".model-option") : null;
    if (option) chooseModel(option.dataset.modelValue || "");
  });
  document.addEventListener("click", (event) => {
    if (!state.modelMenuOpen) return;
    const combo = $("model-combo");
    const target = event.target instanceof Node ? event.target : null;
    if (combo && target && !combo.contains(target)) closeModelMenu();
  });

  on("tasks-pause", "click", toggleTasksPaused);
  on("tasks-list", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-task-action]") : null;
    if (button) taskAction(button);
  });

  on("usage-window", "change", (event) => {
    const next = Number.parseInt(event.target.value, 10);
    state.usageWindow = Number.isFinite(next) && next > 0 ? next : 30;
    refreshUsage();
  });

  on("hook-form", "submit", createHook);
  on("hook-secret-copy", "click", copyHookSecret);
  on("hooks-list", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-hook-action]") : null;
    if (button) hookAction(button);
  });

  on("bark-form", "submit", saveBark);
  on("bark-test", "click", testBark);

  on("refresh-button", "click", async () => {
    if (state.refreshing) return;
    state.refreshing = true;
    const button = $("refresh-button");
    setBusy(button, "common.refreshing");
    try {
      await refreshAll();
    } finally {
      state.refreshing = false;
      clearBusy(button);
    }
  });

  document.addEventListener("folio:languagechange", () => {
    renderMessages();
    setConnection(state.authenticated);
    if (state.lastState) renderState(state.lastState);
    if (state.modelMenuOpen) renderModelMenu();
    renderChannelTexts(state.channels);
    renderHooks({ hooks: state.hooks });
    renderUsage(state.usage);
    renderTasks(state.tasks);
    rerenderBusyLabels();
  });

  /* ------------------------------------------------------------------ boot */

  async function boot() {
    try {
      const session = await api("/api/session");
      if (session.authenticated) {
        state.authenticated = true;
        state.csrf = typeof session.csrf_token === "string" ? session.csrf_token : "";
        showApp();
        await refreshAll();
        await enterSession();
      } else {
        showLogin();
      }
    } catch (error) {
      showLogin();
      setMessage("login", error.network ? "error.network" : "login.unavailable", { error: true });
    }
  }

  // enterSession runs whenever the shell gains a session: from boot with a
  // restored cookie and from a fresh login. A plugin that is already loaded
  // hears folio:session and re-reads its status instead of staying signed out.
  async function enterSession() {
    await loadPlugins();
    document.dispatchEvent(new CustomEvent("folio:session", { detail: { authenticated: true } }));
  }

  // leaveSession is the other half: a plugin must drop its signed-in state
  // instead of leaving a picker on screen after the owner logs out.
  function leaveSession() {
    document.dispatchEvent(new CustomEvent("folio:session", { detail: { authenticated: false } }));
  }

  // Built-in plugins are compiled into the binary and served from their own
  // module. The shell asks which ones exist and loads their frontend, so a
  // plugin that is not compiled in simply never appears.
  async function loadPlugins() {
    try {
      const response = await api("/api/plugins");
      for (const item of response.plugins || []) {
        if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(item.id)) continue;
        if (document.querySelector(`script[data-plugin="${item.id}"]`)) continue;
        const script = document.createElement("script");
        script.src = `/plugins/${item.id}/plugin.js`;
        script.defer = true;
        script.dataset.plugin = item.id;
        document.head.append(script);
      }
    } catch (_) {
      // Discovery is best effort: without a plugin there is no repository
      // picker, and the rest of the console still works.
    }
  }

  // The host surface a plugin may use: authenticated requests, the one connect
  // action, a single refresh entry point, and the session signal. Plugins never
  // reach into the store or the DOM state.
  window.folio = {
    api,
    connectRepository: connectRemote,
    refreshState: () => refreshAll(),
    refreshTasks,
    formatTime,
    t,
  };

  if (missingElements.length) {
    window.console.warn("folio: missing elements: " + Array.from(new Set(missingElements)).join(", "));
  }
  boot();
})();
