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
    sessionEpoch: 0,
    chatLoadGeneration: 0,
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
    quotas: null,
    quotasLoading: false,
    weixin: null,
    weixinBusy: false,
    weixinPoll: null,
    tasks: null,
    kinds: null,
    tasksBusy: "",
    agent: null,
    agentBusy: false,
    modelServices: null,
    modelServicesBusy: false,
    modelValues: { codex: "", dsh: "" },
    modelMenuOpen: false,
    modelActive: -1,
    modelCatalogSignature: "",
    selectedRunId: "",
    lastState: null,
    view: "dashboard",
    viewInitialized: false,
    rechecking: false,
    selectedRun: null,
    setupRequired: false,
    chat: [],
    chatBusy: false,
    chatController: null,
    reportRunId: "",
    reportResults: new Map(),
    chatTasks: [],
    actions: [],
    actionVersions: [],
    actionSelected: "",
    actionsBusy: false,
    catalog: [],
    catalogLoaded: false,
    catalogError: false,
    catalogBusy: Object.create(null),
    catalogResults: new Map(),
    catalogNotices: new Map(),
    feedbackBusy: false,
    feedbackNotice: null,
    feedback: {
      image: null, imageName: "", dataURL: "", loadGeneration: 0,
      cssWidth: 0, cssHeight: 0, dpr: 1,
      strokes: [], current: null, drawing: false, pointerId: null,
      tool: "pen", color: "#d64545", width: 3,
      imageError: "",
    },
  };

  const messageRegions = {
    login: { element: "login-message", data: null },
    setup: { element: "setup-message", data: null },
    repository: { element: "repository-message", data: null },
    automation: { element: "automation-message", data: null },
    channels: { element: "channels-message", data: null },
    hooks: { element: "hooks-message", data: null },
    usage: { element: "usage-message", data: null },
    quotas: { element: "quotas-message", data: null },
    weixin: { element: "weixin-message", data: null },
    tasks: { element: "tasks-message", data: null },
    chat: { element: "chat-message", data: null },
    agent: { element: "agent-message", data: null },
    "model-services": { element: "model-services-message", data: null },
    actions: { element: "actions-message", data: null },
    review: { element: "review-message", data: null },
    app: { element: "app-message", data: null },
  };

  let busyButtons = [];

  /* ------------------------------------------------------------- transport */

  async function api(path, options = {}) {
    const epoch = state.sessionEpoch;
    const method = options.method || "GET";
    const headers = { Accept: "application/json" };
    if (options.body !== undefined && !options.formData) headers["Content-Type"] = "application/json";
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
        body: options.formData || (options.body === undefined ? undefined : JSON.stringify(options.body)),
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

    if (response.status === 401 && epoch === state.sessionEpoch && path !== "/api/session" && path !== "/api/login") {
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
      case "model_services_invalid":
      case "model_services_unavailable":
      case "model_service_key_invalid":
      case "model_service_key_unavailable":
      case "model_service_rate_limited":
      case "model_service_unavailable":
      case "model_service_unauthorized":
      case "model_service_unreachable":
      case "model_service_invalid_response":
        return "model_services.failed";
      case "quota_invalid":
        return "quota.error.quota_invalid";
      case "quota_key_unavailable":
        return "quota.error.quota_key_unavailable";
      case "quota_unauthorized":
        return "quota.error.quota_unauthorized";
      case "quota_unavailable":
        return "quota.error.quota_unavailable";
      case "quota_invalid_response":
        return "quota.error.quota_invalid_response";
      case "quota_rate_limited":
        return "quota.error.quota_rate_limited";
      case "quota_refresh_limited":
        return "quota.error.quota_refresh_limited";
      case "weixin_unavailable":
        return "weixin.error.push_unavailable";
      case "weixin_busy":
        return "weixin.error.busy";
      case "weixin_deployment_off":
        return "weixin.error.deployment_off";
      case "weixin_not_configured":
        return "weixin.error.not_configured";
      case "weixin_owner_required":
        return "weixin.error.owner_required";
      case "weixin_owner_mismatch":
        return "weixin.error.owner_mismatch";
      case "weixin_no_login":
        return "weixin.error.no_login";
      case "weixin_no_prompt":
        return "weixin.error.no_prompt";
      case "weixin_state_error":
        return "weixin.error.state";
      case "weixin_invalid_code":
        return "weixin.code_invalid";
      case "weixin_invalid_owner":
      case "weixin_invalid_request":
        return "weixin.error.invalid";
      case "weixin_unavailable":
        return "weixin.error.push_unavailable";
      case "task_not_found":
        return "tasks.not_found";
      case "task_busy":
        return "tasks.busy";
      case "task_canceled":
        return "tasks.canceled_error";
      case "action_unknown":
        return "catalog.action_unknown";
      case "action_disabled":
        return "catalog.action_disabled";
      case "action_input_invalid":
        return "catalog.action_input_invalid";
      case "feedback_invalid":
        return "feedback.failed";
      case "feedback_too_large":
        return "feedback.image_too_large";
      case "feedback_unavailable":
        return "feedback.failed";
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
    showAuthMode();
  }

  function showAuthMode() {
    const setup = Boolean(state.setupRequired);
    const loginForm = $("login-form");
    const setupForm = $("setup-form");
    if (loginForm) loginForm.hidden = setup;
    if (setupForm) setupForm.hidden = !setup;
    const heading = $("login-heading");
    const lead = $("login-lead");
    if (heading) heading.textContent = t(setup ? "setup.title" : "login.title");
    if (lead) lead.textContent = t(setup ? "setup.title_lead" : "login.lead");
    const focus = setup ? $("setup-username") : $("login-username");
    if (focus) focus.focus();
  }

  function finishAuthentication(result) {
    state.csrf = typeof result.csrf_token === "string" ? result.csrf_token : "";
    state.authenticated = true;
    state.sessionEpoch += 1;
    state.setupRequired = false;
    showApp();
    return Promise.resolve();
  }

  function showApp() {
    $("login-view").hidden = true;
    $("app-view").hidden = false;
    $("logout-button").hidden = false;
    setConnection(true);
    if (!state.timers.automation) {
      state.timers.automation = window.setInterval(() => {
        if (state.authenticated && !document.hidden) {
          refreshAutomation({ silent: true });
          refreshTasks();
          if (state.view === "dashboard") refreshUsage();
        }
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
    if ($("model-service-key")) $("model-service-key").value = "";
    state.modelServices = null;
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
    const usernameInput = $("login-username");
    const passwordInput = $("password");
    const username = usernameInput ? usernameInput.value.trim() : "";
    const password = passwordInput ? passwordInput.value : "";
    if (!username || !password) {
      setMessage("login", "login.required", { error: true });
      (username ? passwordInput : usernameInput).focus();
      return;
    }
    state.loggingIn = true;
    const button = $("login-submit");
    setBusy(button, "login.submitting");
    setMessage("login", null);
    try {
      const result = await api("/api/login", { method: "POST", body: { username, password } });
      if (passwordInput) passwordInput.value = "";
      await finishAuthentication(result);
      await refreshAll();
      await enterSession();
    } catch (error) {
      if (error.code === "setup_required") {
        state.setupRequired = true;
        showLogin();
        return;
      }
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

  async function setup(event) {
    event.preventDefault();
    if (state.loggingIn) return;
    const username = $("setup-username").value.trim();
    const password = $("setup-password").value;
    const confirm = $("setup-confirm").value;
    if (!username || !password) {
      setMessage("setup", "setup.fields_required", { error: true });
      return;
    }
    if ([...password].length < 8) {
      setMessage("setup", "setup.password_short", { error: true });
      return;
    }
    const classes = [
      /\p{Ll}/u.test(password),
      /\p{Lu}/u.test(password),
      /\p{Nd}/u.test(password),
      /[\p{P}\p{S}]/u.test(password),
    ].filter(Boolean).length;
    if (classes < 3) {
      setMessage("setup", "setup.password_weak", { error: true });
      return;
    }
    if (password !== confirm) {
      setMessage("setup", "setup.password_mismatch", { error: true });
      return;
    }
    state.loggingIn = true;
    const button = $("setup-submit");
    setBusy(button, "setup.submitting");
    setMessage("setup", null);
    try {
      const result = await api("/api/setup", { method: "POST", body: { username, password } });
      $("setup-password").value = "";
      $("setup-confirm").value = "";
      await finishAuthentication(result);
      await refreshAll();
      await enterSession();
    } catch (error) {
      const key =
        error.code === "setup_private_only"
          ? "setup.private_only"
          : error.code === "auth_setup_complete"
            ? "setup.complete"
            : error.code === "auth_username_invalid" || error.code === "auth_password_invalid"
              ? "setup.invalid"
              : "setup.failed";
      setMessage("setup", key, { error: true });
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
      if ($("model-service-key")) $("model-service-key").value = "";
      state.modelServices = null;
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
    renderSetup();
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
    if (state.automationRunning || !setupReady()) return;
    state.automationRunning = true;
    const button = $("run-now-button");
    setBusy(button, "automation.running");
    setMessage("automation", null);
    // Daily runs inherit the task binding configured in Settings. Legacy runner/model
    // fields remain available only when Advanced legacy runtime is explicitly opened.
    const body = {};
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
      return run;
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("automation", errorKey(error, "automation.failed"), { error: true });
        renderErrorRaw("automation-output", error);
      }
    } finally {
      state.automationRunning = false;
      clearBusy(button);
      renderSetup();
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

  const views = ["dashboard", "chat", "feedback", "actions", "runs", "review", "settings"];
  const sectionViews = {
    "repository-section": "settings", "runner-settings": "settings",
    "channels-section": "settings", "hooks-section": "settings", "agent-settings": "settings", "model-services-settings": "settings",
    "automation-section": "runs", "tasks-section": "dashboard", "usage-section": "dashboard", "run-options": "dashboard",
  };

  function capability(name) {
    const data = state.lastState || {};
    const caps = data.capabilities || {};
    const supplied = caps[name === "chat" ? "assistant" : name] || (name === "maintenance" ? caps.context_maintenance : null);
    if (supplied && typeof supplied.available === "boolean") return { ...supplied, ready: supplied.available, missing: supplied.reason ? [supplied.reason] : [] };
    if (supplied && typeof supplied.ready === "boolean") return supplied;
    const repository = Boolean(data.repository && data.repository.connected && data.storage && data.storage.auth && data.storage.auth !== "missing");
    const defaults = state.modelServices && state.modelServices.defaults || {};
    const binding = defaults[name === "chat" ? "assistant" : name];
    const model = Boolean(binding && binding.connection && binding.model);
    const runner = runnerAvailable("codex", data) || runnerAvailable("dsh", data);
    if (name === "chat") return { ready: Boolean(state.agent && state.agent.available) || model, missing: [] };
    if (name === "report") return { ready: model || runner, missing: [] };
    if (name === "maintenance") return { ready: repository && (model || runner), missing: [] };
    if (name === "context") return { ready: repository, missing: [] };
    return { ready: false, missing: [] };
  }

  function setupReady() {
    return ["chat", "report", "maintenance"].some((name) => capability(name).ready);
  }

  function capabilityText(name) {
    const cap = capability(name);
    if (cap.ready) return t("daily.ready");
    const missing = Array.isArray(cap.missing) ? cap.missing : [];
    const reasons = missing.map((item) => typeof item === "string" ? item : item && item.code).filter(Boolean);
    return reasons.length ? reasons.map((code) => translateKnown(`daily.missing.${code}`, t(`daily.missing.${name}`))).join(" · ") : t(`daily.missing.${name}`);
  }

  function navigate(focus = false) {
    if (!state.authenticated) return;
    if ($("task-detail").open) $("task-detail").close();
    const fragment = window.location.hash.slice(1);
    const target = views.includes(fragment) ? fragment : sectionViews[fragment];
    state.view = target || (setupReady() ? "dashboard" : "settings");
    views.forEach((view) => { $(`${view}-view`).hidden = view !== state.view; });
    if (state.view === "chat") loadChat();
    if (state.view === "actions") loadActions();
    document.querySelectorAll("[data-view]").forEach((link) => {
      if (link.dataset.view === state.view) link.setAttribute("aria-current", "page");
      else link.removeAttribute("aria-current");
    });
    if (focus) {
      const section = sectionViews[fragment] ? $(fragment) : null;
      const heading = $(`${state.view === "runs" ? "runs-page" : state.view === "settings" ? "settings" : state.view}-heading`);
      if (section) {
        if (section.tagName === "DETAILS") section.open = true;
        section.scrollIntoView({ block: "start" });
      }
      else if (heading) heading.focus();
    }
    state.viewInitialized = true;
    renderSetup();
  }

  function renderSetup() {
    const data = state.lastState || {};
    const repository = data.repository || {};
    const authorized = data.storage && data.storage.auth && data.storage.auth !== "missing";
    const available = runnerAvailable("codex", data) || runnerAvailable("dsh", data);
    $("setup-repository-state").textContent = t(!authorized ? "setup.authorize" : repository.connected ? "setup.selected" : "setup.select");
    $("setup-runner-state").textContent = t(available ? "setup.installed" : "setup.required");
    $("setup-runner-detail").textContent = t(available ? "setup.runner_available" : "setup.runner_missing");
    $("setup-alert").hidden = capability("maintenance").ready || !["dashboard", "review", "runs"].includes(state.view);
    $("dashboard-repository").textContent = repository.connected ? repository.full_name || "" : t("repo.none");
    ["chat", "report", "maintenance"].forEach((name) => { $("settings-" + name + "-ready").textContent = capabilityText(name); });
    updateChatAgentHint();
    if (state.catalogLoaded) updateCatalogAvailability();
    const unavailable = !capability("maintenance").ready;
    if (!state.automationRunning) $("run-now-button").disabled = unavailable;
    if (!state.rechecking) $("review-recheck").disabled = unavailable;
    $("run-options-summary").textContent = t("board.run_options", {
      runner: activeRunner(), model: $("model-input").value.trim() || t("automation.model_default"),
    });
    $("review-runner").textContent = t("review.binding");
  }

  // Only fixed GitHub URLs; model-provided paths never become arbitrary links.
  function findingURL(run, source, current = false) {
    const repository = current ? (state.lastState || {}).repository : run && run.repository;
    const storage = (state.lastState || {}).storage || {};
    if ((current ? storage.plugin : repository && repository.plugin) !== "github") return "";
    const name = repository && repository.full_name;
    if (current && run && run.repository && run.repository.full_name && run.repository.full_name !== name) return "";
    if (typeof name !== "string" || name.split("/").length !== 2 ||
        !name.split("/").every((part) => repositoryPart.test(part))) return "";
    const ref = current ? repository.branch : run && run.pull && run.pull.head;
    if (typeof ref !== "string" || !ref || (!current && !/^[a-f0-9]{40}$/i.test(ref))) return "";
    if (typeof source !== "string" || source.length > 512 || !source.endsWith(".md") ||
        /[\\\x00-\x1f\x7f]/.test(source) || source.split("/").some((part) => !part || part === "." || part === "..")) return "";
    return `https://github.com/${name.split("/").map(encodeURIComponent).join("/")}/blob/${encodeURIComponent(ref)}/${source.split("/").map(encodeURIComponent).join("/")}`;
  }

  function findingCards(run, current = false) {
    const list = document.createElement("div");
    list.className = "review-findings";
    const issues = Array.isArray(run && run.needs_review) ? run.needs_review : [];
    issues.forEach((issue) => {
      if (!issue || typeof issue.source !== "string" || typeof issue.reason !== "string") return;
      const card = document.createElement("article");
      card.className = "review-finding";
      const source = document.createElement("h3");
      source.textContent = issue.source;
      const reason = document.createElement("p");
      reason.textContent = translateKnown(`automation.finding.${issue.reason}`, issue.reason);
      const next = document.createElement("p");
      next.className = "hint";
      next.textContent = t(issue.reason === "candidate_requires_owner_review" ? "review.candidate_next" : "review.finding_next");
      const guidance = document.createElement("details");
      const guidanceTitle = document.createElement("summary");
      guidanceTitle.textContent = t("review.what_next");
      guidance.append(guidanceTitle, next);
      card.append(source, reason, guidance);
      if (current && !(run.repository && run.repository.full_name)) {
        const warning = document.createElement("p");
        warning.className = "hint";
        warning.textContent = t("review.current_only");
        card.appendChild(warning);
      }
      const actions = document.createElement("div");
      actions.className = "actions";
      [false, ...(current ? [true] : [])].forEach((inCurrentRepository) => {
        const url = findingURL(run, issue.source, inCurrentRepository);
        if (!url) return;
        const link = document.createElement("a");
        link.className = "btn ghost small";
        link.href = url;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.textContent = t(inCurrentRepository ? "review.open_current" : "review.open_input");
        actions.appendChild(link);
      });
      if (!actions.childNodes.length) {
        const hint = document.createElement("p");
        hint.className = "hint";
        hint.textContent = t("review.no_link");
        card.appendChild(hint);
      }
      card.appendChild(actions);
      list.appendChild(card);
    });
    return list;
  }

  function renderReview() {
    const run = state.automation && state.automation.review_run;
    const issues = Array.isArray(run && run.needs_review) ? run.needs_review : [];
    const notice = $("dashboard-review");
    notice.hidden = !issues.length;
    notice.textContent = t("review.notice", { count: issues.length });
    $("review-snapshot").textContent = state.automationError ? t("review.snapshot_failed") : run
      ? t("review.as_of", { time: formatTime(run.finished_at || run.started_at), count: issues.length })
      : t("review.no_snapshot");
    const list = $("review-list");
    list.replaceChildren();
    if (issues.length) list.appendChild(findingCards(run, true));
    else {
      const empty = document.createElement("p");
      empty.className = "empty-state";
      empty.textContent = t(run ? "review.empty" : "review.no_snapshot_hint");
      list.appendChild(empty);
    }
    renderSetup();
  }

  async function recheckReview() {
    if (state.rechecking || state.automationRunning || !capability("maintenance").ready) return;
    state.rechecking = true;
    const button = $("review-recheck");
    setBusy(button, "automation.running");
    setMessage("review", "review.checking");
    try {
      // Maintenance already pulls before inspecting files; a separate sync would
      // introduce a second job and a race without improving freshness.
      const run = await runAutomationNow();
      const successful = run && ["pushed", "no_changes"].includes(run.status);
      setMessage("review", successful ? state.automationError ? "review.snapshot_failed" : "review.checked" : "review.check_failed", {
        error: !successful || state.automationError,
      });
    } finally {
      state.rechecking = false;
      clearBusy(button);
      renderSetup();
    }
  }

  function renderState(data) {
    if (!data) return;
    const storage = data.storage || {};
    const credential = typeof storage.auth === "string" ? storage.auth : "missing";

    const repository = data.repository || {};
    const connected = Boolean(repository.connected);
    const authorizationRequired = connected && credential === "missing";
    const repositoryBadge = $("repository-badge");
    repositoryBadge.dataset.state = authorizationRequired ? "pending" : connected ? "connected" : "not_connected";
    repositoryBadge.textContent = t(authorizationRequired ? "repo.auth_required" : connected ? "repo.connected" : "repo.not_connected");
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
    if (!state.viewInitialized) navigate();
    renderSetup();
  }

  async function refreshState() {
    const data = await api("/api/state");
    state.lastState = data;
    renderState(data);
    return data;
  }

  async function refreshAll() {
    const initialNavigation = !state.viewInitialized;
    try {
      await refreshState();
      setMessage("app", null);
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("app", errorKey(error, "error.generic"), { error: true });
      }
    }
    if (!state.authenticated) return;
    if (!state.viewInitialized) navigate();
    // The quota source is optional and may take its full timeout; do not hold
    // the rest of the dashboard refresh behind a slow external gateway.
    refreshQuotas();
    await Promise.allSettled([
      loadCatalog(), loadTaskKinds(), refreshAutomation({ silent: true }), refreshChannels(),
      refreshHooks(), refreshUsage(), refreshTasks(), loadWeixin(), loadAgentConfig(), loadModelServices(),
    ]);
    renderHooks({ hooks: state.hooks });
    if (initialNavigation && !window.location.hash && state.authenticated) navigate();
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
    // The push switch lives in the channel payload but is shown beside the
    // WeChat card, so a late channel load must refresh that card too.
    if (state.weixin) renderWeixin(state.weixin);
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
  const RESERVED_KIND_LABELS = Object.freeze({ "feedback-plan": "tasks.kind.feedback_plan" });

  function kindLabel(id) {
    const kind = (state.kinds || []).find((item) => item && item.id === id);
    if (kind && kind.label) return kind.label;
    if (typeof id === "string" && Object.prototype.hasOwnProperty.call(RESERVED_KIND_LABELS, id)) return t(RESERVED_KIND_LABELS[id]);
    return id || "";
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
    if (state.modelServices) renderModelOverrides(state.modelServices.overrides);
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

  /* ----------------------------------------------------------------- cost */

  function costValue(value) {
    const number = Number(value);
    if (!Number.isFinite(number) || number < 0) return "";
    try {
      return new Intl.NumberFormat(usageLocale(), {
        style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 6,
      }).format(number);
    } catch (_) {
      return `$${number.toFixed(6)}`;
    }
  }

  function costText(cost) {
    if (!cost) return t("cost.unavailable");
    const known = cost.estimated === true || (cost.partial === true && Number(cost.total_cost) > 0);
    if (!known) return t("cost.unavailable");
    const value = costValue(cost.total_cost);
    if (!value) return t("cost.unavailable");
    return cost.partial ? t("cost.partial", { value }) : value;
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
        Number(model.priced_runs) > 0 ? costText({ estimated: true, total_cost: model.estimated_cost_usd, partial: Number(model.unpriced_cost_runs) > 0 }) : "—",
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
      cell.colSpan = 7;
      cell.className = "hint";
      cell.textContent = t("usage.empty");
      row.appendChild(cell);
      body.appendChild(row);
    }
  }

  function renderUsage(data) {
    state.usage = data || null;
    const totals = (data && data.totals) || {};
    setUsageText("usage-total", data ? usageNumber(totals.total_tokens) : "—");
    setUsageText("usage-input", data ? usageNumber(totals.input_tokens) : "—");
    setUsageText("usage-output", data ? usageNumber(totals.output_tokens) : "—");
    setUsageText("usage-runs", data ? usageNumber(totals.runs) : "—");
    setUsageText("work-status-tokens", data ? usageNumber(totals.total_tokens) : "—");
    setUsageText("work-status-cost", data && Number(totals.priced_runs) > 0 ? costText({ estimated: true, total_cost: totals.estimated_cost_usd, partial: Number(totals.unpriced_cost_runs) > 0 }) : "—");
    setUsageText("usage-cost", data && Number(totals.priced_runs) > 0
      ? costText({ estimated: true, total_cost: totals.estimated_cost_usd, partial: Number(totals.unpriced_cost_runs) > 0 })
      : "—");
    const note = $("usage-note");
    if (note) {
      note.textContent = data
        ? t("usage.note", {
            reported: usageNumber(totals.reported_runs),
            runs: usageNumber(totals.runs),
            missing: usageNumber(totals.unreported_runs),
          }) + " " + t("usage.cost_note")
        : "";
    }
    renderUsageDays(Array.isArray(data && data.days) ? data.days : []);
    renderUsageModels(Array.isArray(data && data.models) ? data.models : []);
  }

  async function refreshUsage() {
    try {
      const data = await api(`/api/usage?days=${encodeURIComponent(state.usageWindow)}`);
      renderUsage(data);
      setMessage("usage", null);
    } catch (error) {
      if (error.code === "authentication_required") return;
      setMessage("usage", errorKey(error, "usage.failed"), { error: true });
    }
  }

  /* ---------------------------------------------------------------- quotas */

  function quotaLocale() {
    const lang = document.documentElement.lang;
    return typeof lang === "string" && lang ? lang : "en";
  }

  function quotaNumeric(value) {
    if (value === null || value === undefined || value === "") return null;
    const number = Number(value);
    return Number.isFinite(number) ? number : null;
  }

  function quotaNumberText(value) {
    const number = quotaNumeric(value);
    if (number === null) return "";
    try {
      return new Intl.NumberFormat(quotaLocale(), { maximumFractionDigits: 1 }).format(number);
    } catch (_) {
      return String(number);
    }
  }

  function quotaKindLabel(kind) {
    if (typeof kind !== "string" || !kind) return "";
    const key = `quota.kind.${kind}`;
    return hasI18n() && window.FolioI18n.has(key) ? t(key) : kind;
  }

  function quotaErrorText(code) {
    if (typeof code !== "string" || !code) return t("quota.failed");
    const key = `quota.error.${code}`;
    return hasI18n() && window.FolioI18n.has(key) ? t(key) : t("quota.failed");
  }

  // Relative time is best effort: without Intl.RelativeTimeFormat the caller
  // falls back to an absolute Intl.DateTimeFormat timestamp.
  function quotaRelativePhrase(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "";
    if (typeof Intl === "undefined" || typeof Intl.RelativeTimeFormat !== "function") return "";
    const deltaSeconds = (date.getTime() - Date.now()) / 1000;
    const magnitude = Math.abs(deltaSeconds);
    let unit = "second";
    let amount = deltaSeconds;
    if (magnitude >= 86400) {
      unit = "day";
      amount = deltaSeconds / 86400;
    } else if (magnitude >= 3600) {
      unit = "hour";
      amount = deltaSeconds / 3600;
    } else if (magnitude >= 60) {
      unit = "minute";
      amount = deltaSeconds / 60;
    }
    amount = Math.round(amount);
    if (amount === 0) amount = 0;
    try {
      return new Intl.RelativeTimeFormat(quotaLocale(), { numeric: "auto" }).format(amount, unit);
    } catch (_) {
      return "";
    }
  }

  function quotaReadLabel(value) {
    if (value === null || value === undefined || value === "") return t("quota.read_unknown");
    const relative = quotaRelativePhrase(value);
    if (relative) return t("quota.read_at", { time: relative });
    const absolute = formatTime(value);
    if (absolute) return t("quota.read_at", { time: absolute });
    return t("quota.read_unknown");
  }

  function quotaResetLabel(value) {
    if (value === null || value === undefined || value === "") return t("quota.window.no_reset");
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return t("quota.window.no_reset");
    if (date.getTime() <= Date.now()) return t("quota.window.reset_passed");
    const relative = quotaRelativePhrase(value);
    return t("quota.window.reset_in", { time: relative || formatTime(value) || String(value) });
  }

  function quotaAmountText(value, unit) {
    const text = quotaNumberText(value);
    if (!text) return "";
    return unit ? `${text} ${unit}` : text;
  }

  function renderQuotaWindow(reading) {
    const item = document.createElement("li");
    item.className = "quota-window";

    const unlimited = reading.unlimited === true;
    const textOnly = reading.text === true;
    const used = quotaNumeric(reading.used);
    const remaining = quotaNumeric(reading.remaining);
    const name =
      typeof reading.name === "string" && reading.name.trim() ? reading.name.trim() : "—";
    const display = typeof reading.display === "string" ? reading.display.trim() : "";
    const usedText = used === null ? "" : t("quota.window.used", { percent: quotaNumberText(used) });

    const head = document.createElement("div");
    head.className = "quota-window-head";
    const nameNode = document.createElement("span");
    nameNode.className = "quota-window-name";
    nameNode.textContent = name;
    const displayNode = document.createElement("span");
    displayNode.className = "quota-window-display";
    if (display) displayNode.textContent = display;
    else if (unlimited) displayNode.textContent = t("quota.window.unlimited");
    else if (textOnly || used === null) displayNode.textContent = t("quota.window.no_reading");
    else displayNode.textContent = usedText;
    head.appendChild(nameNode);
    head.appendChild(displayNode);
    item.appendChild(head);

    if (!unlimited && !textOnly && used !== null) {
      const clamped = Math.min(100, Math.max(0, used));
      const bar = document.createElement("div");
      bar.className = "quota-bar";
      bar.setAttribute("role", "progressbar");
      bar.setAttribute("aria-valuemin", "0");
      bar.setAttribute("aria-valuemax", "100");
      bar.setAttribute("aria-valuenow", String(clamped));
      bar.setAttribute("aria-valuetext", usedText || t("quota.window.no_reading"));
      bar.setAttribute("aria-label", `${name}: ${usedText || t("quota.window.no_reading")}`);
      const fill = document.createElement("span");
      fill.className = "quota-bar-fill";
      fill.style.width = `${clamped}%`;
      bar.appendChild(fill);
      item.appendChild(bar);
    }
    if (unlimited) item.dataset.unlimited = "true";
    if (textOnly) item.dataset.text = "true";

    const meta = document.createElement("p");
    meta.className = "quota-window-meta";
    const parts = [];
    if (!unlimited && !textOnly && remaining !== null) {
      parts.push(t("quota.window.remaining", { percent: quotaNumberText(remaining) }));
    }
    parts.push(quotaResetLabel(reading.resets_at));
    const balance = quotaNumeric(reading.balance);
    const unit = typeof reading.unit === "string" ? reading.unit.trim() : "";
    if (balance !== null) {
      parts.push(t("quota.window.balance", { amount: quotaAmountText(balance, unit) }));
    } else {
      const amount = quotaNumeric(reading.amount);
      if (amount !== null && amount !== 0) {
        parts.push(t("quota.window.amount", { amount: quotaAmountText(amount, unit) }));
      }
    }
    meta.textContent = parts.join(" · ");
    item.appendChild(meta);
    return item;
  }

  function renderQuotaCard(card) {
    const article = document.createElement("article");
    article.className = "quota-card";
    if (card.stale) article.dataset.state = "stale";
    if (card.error) article.dataset.state = "error";

    const providerLabel =
      typeof card.provider_label === "string" ? card.provider_label.trim() : "";
    const provider = typeof card.provider === "string" ? card.provider.trim() : "";
    const alias = typeof card.alias === "string" ? card.alias.trim() : "";
    const account = typeof card.account === "string" ? card.account.trim() : "";
    const identity = alias || account || providerLabel || provider || "—";

    const head = document.createElement("div");
    head.className = "quota-card-head";
    const heading = document.createElement("div");
    heading.className = "quota-card-heading";
    const providerNode = document.createElement("h3");
    providerNode.className = "quota-card-provider";
    providerNode.textContent = providerLabel || provider || "—";
    const identityNode = document.createElement("p");
    identityNode.className = "quota-card-identity";
    identityNode.textContent = identity;
    heading.appendChild(providerNode);
    heading.appendChild(identityNode);
    head.appendChild(heading);

    const badges = document.createElement("div");
    badges.className = "quota-card-badges";
    const plan = typeof card.plan === "string" ? card.plan.trim() : "";
    const kindText = quotaKindLabel(card.kind);
    if (plan || kindText) {
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.dataset.state = "neutral";
      badge.textContent = [plan, kindText].filter(Boolean).join(" · ");
      badges.appendChild(badge);
    }
    if (card.stale) {
      const stale = document.createElement("span");
      stale.className = "badge";
      stale.dataset.state = "skipped";
      stale.textContent = t("quota.stale_badge");
      badges.appendChild(stale);
    }
    if (badges.childNodes.length) head.appendChild(badges);
    article.appendChild(head);

    const metaParts = [quotaReadLabel(card.read_at)];
    const balance = typeof card.balance === "string" ? card.balance.trim() : "";
    if (balance) metaParts.push(t("quota.card_balance", { amount: balance }));
    const meta = document.createElement("p");
    meta.className = "quota-card-meta";
    meta.textContent = metaParts.join(" · ");
    article.appendChild(meta);

    if (typeof card.error === "string" && card.error.trim()) {
      const errorNode = document.createElement("p");
      errorNode.className = "quota-card-error";
      errorNode.textContent = t("quota.card_error", { detail: card.error.trim() });
      article.appendChild(errorNode);
    }

    const readings = Array.isArray(card.windows) ? card.windows : [];
    if (readings.length) {
      const list = document.createElement("ul");
      list.className = "quota-windows";
      readings.forEach((reading) => {
        if (reading && typeof reading === "object") list.appendChild(renderQuotaWindow(reading));
      });
      if (list.childNodes.length) article.appendChild(list);
    }
    return article;
  }

  function quotaSourceNote(data, cardCount) {
    if (data.error) {
      let text = quotaErrorText(data.error);
      const retry = quotaNumeric(data.retry_after_seconds);
      if (retry !== null && retry > 0) {
        text += ` · ${t("quota.retry_after", { seconds: Math.round(retry) })}`;
      }
      if (data.stale) text += ` · ${t("quota.stale")}`;
      return { text, state: "error" };
    }
    const stale = data.stale === true;
    if (!cardCount) {
      return {
        text: stale ? `${t("quota.empty")} · ${t("quota.stale")}` : t("quota.empty"),
        state: stale ? "stale" : "",
      };
    }
    const parts = [];
    if (typeof data.source === "string" && data.source.trim()) {
      parts.push(t("quota.source", { source: data.source.trim() }));
    }
    const fetchedAt = typeof data.fetched_at === "string" ? data.fetched_at : "";
    const updated = fetchedAt ? formatTime(fetchedAt) : "";
    if (updated) parts.push(t("quota.updated", { time: updated }));
    if (stale) parts.push(t("quota.stale"));
    return { text: parts.join(" · "), state: stale ? "stale" : "" };
  }

  function renderQuotas(data) {
    if (data !== undefined) state.quotas = data || null;
    const source = state.quotas;
    const allowance = $("work-status-allowance");
    if (allowance) allowance.textContent = source && Array.isArray(source.cards) && source.cards.length ? (source.stale || source.cards.some((card) => card.stale || card.error) ? t("quota.stale_badge") : String(source.cards.length)) : "—";
    const note = $("quotas-note");
    const container = $("quotas-cards");
    if (container) {
      while (container.firstChild) container.removeChild(container.firstChild);
      container.setAttribute("aria-busy", state.quotasLoading ? "true" : "false");
    }
    if (!source) {
      if (note) {
        note.textContent = state.quotasLoading ? t("quota.loading") : t("quota.empty");
        delete note.dataset.state;
      }
      return;
    }
    const cards = Array.isArray(source.cards) ? source.cards : [];
    const configured = source.configured !== false;
    if (note) {
      if (!configured) {
        note.textContent = t("quota.unconfigured");
        delete note.dataset.state;
      } else {
        const summary = quotaSourceNote(source, cards.length);
        note.textContent = summary.text;
        if (summary.state) note.dataset.state = summary.state;
        else delete note.dataset.state;
      }
    }
    if (!configured || !container) return;
    cards.forEach((card) => {
      if (card && typeof card === "object") container.appendChild(renderQuotaCard(card));
    });
  }

  async function refreshQuotas({ refresh = false } = {}) {
    if (state.quotasLoading) return;
    state.quotasLoading = true;
    const button = $("quotas-refresh");
    const container = $("quotas-cards");
    setBusy(button, refresh ? "quota.refreshing" : "quota.loading");
    setMessage("quotas", null);
    if (!state.quotas) renderQuotas(state.quotas);
    if (container) container.setAttribute("aria-busy", "true");
    try {
      const data = refresh
        ? await api("/api/quotas/refresh", { method: "POST", body: {} })
        : await api("/api/quotas");
      renderQuotas(data);
    } catch (error) {
      if (error.code === "authentication_required") return;
      const retry = quotaNumeric(error.payload && error.payload.retry_after_seconds);
      let key = errorKey(error, "quota.failed");
      if (error.code === "quota_refresh_limited" && retry !== null && retry > 0) {
        key = "quota.refresh_limited";
      }
      setMessage("quotas", key, {
        error: true,
        vars: retry !== null && retry > 0 ? { seconds: Math.round(retry) } : null,
      });
    } finally {
      state.quotasLoading = false;
      if (container) container.setAttribute("aria-busy", "false");
      if (!state.quotas) renderQuotas(state.quotas);
      clearBusy(button);
    }
  }

  /* ---------------------------------------------------------------- weixin */

  const WEIXIN_POLL_MS = 1500;
  // The transport gives a QR five minutes. One poll per 1.5s keeps a bounded
  // timer count well inside that window and stops on any terminal state.
  const WEIXIN_POLL_LIMIT = 200;
  const WEIXIN_LOGIN_ERRORS = {
    expired: "weixin.error.login_expired",
    protocol_error: "weixin.error.protocol",
    state_error: "weixin.error.state",
    unavailable: "weixin.error.unavailable",
    login_failed: "weixin.failed",
  };

  function weixinLoginActive(login) {
    const status = login && login.status;
    return status === "waiting" || status === "need_code" || status === "verifying";
  }

  function stopWeixinPoll() {
    if (state.weixinPoll) {
      window.clearTimeout(state.weixinPoll);
      state.weixinPoll = null;
    }
  }

  // Only a server-rendered PNG may reach the image element. The raw QR payload
  // is never part of an API response, so there is nothing here to encode.
  function weixinQRSrc(value) {
    return typeof value === "string" && value.startsWith("data:image/png;base64,") ? value : "";
  }

  function weixinBadgeText(data) {
    const deployment = (data && data.deployment) || {};
    if (deployment.mode === "off") return ["disabled", t("weixin.badge.deployment_off")];
    if (data.running) return ["enabled", t("weixin.badge.running")];
    if (data.enabled) return ["disabled", t("weixin.badge.stopped")];
    if (data.owner_id) return ["disabled", t("weixin.badge.ready")];
    if (data.configured) return ["disabled", t("weixin.badge.unconfirmed")];
    return ["disabled", t("weixin.badge.unconfigured")];
  }

  function weixinNoteText(data) {
    const deployment = (data && data.deployment) || {};
    if (deployment.mode === "off") return t("weixin.note.deployment_off");
    if (deployment.owner_pinned) return t("weixin.note.owner_pinned");
    if (!data.configured) return t("weixin.note.unconfigured");
    if (!data.owner_id) return t("weixin.note.pending");
    return "";
  }

  function weixinPushNote(push) {
    if (!push) return "";
    if (push.last_test_status === "failed") {
      return t("weixin.push.failed", { detail: push.last_error || "failed" });
    }
    if (push.last_test_status === "delivered" && push.last_test_at) {
      return t("weixin.push.delivered", { time: formatTime(push.last_test_at) || push.last_test_at });
    }
    return push.enabled ? t("weixin.push.on") : t("weixin.push.off");
  }

  async function toggleWeixinPush(event) {
    const enabled = Boolean(event.target.checked);
    setMessage("weixin", null);
    try {
      state.channels = await api("/api/channels/weixin", { method: "POST", body: { enabled } });
      renderChannelState(state.channels);
      setMessage("weixin", enabled ? "weixin.push.enabled" : "weixin.push.disabled");
    } catch (error) {
      if (event.target) event.target.checked = !enabled;
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    }
  }

  async function testWeixinPush() {
    if (state.weixinPushBusy) return;
    state.weixinPushBusy = true;
    const button = $("weixin-push-test");
    setBusy(button, "weixin.push.testing");
    setMessage("weixin", null);
    try {
      const result = await api("/api/channels/weixin/test", { method: "POST", body: {} });
      if (result.state) renderChannelState(result.state);
      const receipt = result.receipt || {};
      if (receipt.status === "delivered") setMessage("weixin", "weixin.push.test_sent");
      else setMessage("weixin", "weixin.push.test_failed", { vars: { detail: receipt.detail || "failed" }, error: true });
    } catch (error) {
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    } finally {
      state.weixinPushBusy = false;
      clearBusy(button);
    }
  }

  function weixinHealthText(data) {
    const info = data && data.diagnostics;
    if (!info) return "";
    if (info.last_error) {
      return t("weixin.health.error", {
        reason: translateKnown(`weixin.reason.${info.last_error}`, info.last_error),
        time: formatTime(info.last_error_at) || info.last_error_at || "",
      });
    }
    if (info.last_ok_at) return t("weixin.health.ok", { time: formatTime(info.last_ok_at) || info.last_ok_at });
    return "";
  }

  function renderWeixin(data) {
    const section = $("weixin-section");
    if (!section) return;
    state.weixin = data || null;
    if (!data) {
      section.hidden = true;
      stopWeixinPoll();
      return;
    }
    section.hidden = false;

    const badge = $("weixin-badge");
    if (badge) {
      const [stateName, text] = weixinBadgeText(data);
      badge.dataset.state = stateName;
      badge.textContent = text;
    }
    const note = $("weixin-note");
    if (note) note.textContent = weixinNoteText(data);
    const health = $("weixin-health");
    if (health) {
      const text = weixinHealthText(data);
      health.hidden = !text;
      health.textContent = text;
    }

    const deployment = data.deployment || {};
    const login = data.login || null;
    const active = weixinLoginActive(login);
    const awaiting = Boolean(login && login.status === "awaiting_confirmation");
    const disabled = deployment.mode === "off";

    const connect = $("weixin-connect");
    if (connect) {
      connect.hidden = active || awaiting;
      connect.disabled = disabled;
    }
    const cancel = $("weixin-cancel");
    if (cancel) cancel.hidden = !active;

    const toggle = $("weixin-toggle");
    if (toggle) {
      const canToggle = Boolean(data.owner_id) && Boolean(data.configured) && !active && !awaiting && !disabled;
      toggle.hidden = !canToggle;
      toggle.disabled = !canToggle;
      const label = toggle.querySelector(".btn-label");
      if (label) label.textContent = t(data.running ? "weixin.disable" : "weixin.enable");
    }

    const loginBox = $("weixin-login");
    if (loginBox) loginBox.hidden = !login || (!active && !awaiting);

    const qrWrap = $("weixin-qr-wrap");
    const qr = $("weixin-qr");
    const src = login ? weixinQRSrc(login.qr_image) : "";
    if (qrWrap) qrWrap.hidden = !src;
    if (qr) {
      if (src && qr.getAttribute("src") !== src) qr.setAttribute("src", src);
      if (!src && qr.hasAttribute("src")) qr.removeAttribute("src");
      qr.alt = src ? t("weixin.qr_alt") : "";
    }

    const codeForm = $("weixin-code-form");
    if (codeForm) codeForm.hidden = !(login && login.status === "need_code");
    const verifyHint = $("weixin-verify-hint");
    if (verifyHint) verifyHint.hidden = !(login && login.status === "verifying");

    const confirm = $("weixin-confirm");
    if (confirm) confirm.hidden = !awaiting;
    const scanner = $("weixin-scanner");
    if (scanner) {
      scanner.textContent = awaiting && typeof login.scanner_id === "string" ? login.scanner_id : "";
    }

    const push = (state.channels && state.channels.weixin) || null;
    const pushBox = $("weixin-push");
    if (pushBox) {
      const ready = Boolean(push && push.available && data.configured && data.owner_id);
      pushBox.hidden = !ready;
      const toggle = $("weixin-push-toggle");
      if (toggle) toggle.checked = Boolean(push && push.enabled);
      const pushNote = $("weixin-push-note");
      if (pushNote) pushNote.textContent = weixinPushNote(push);
    }

    if (login && login.error) {
      const key = WEIXIN_LOGIN_ERRORS[login.error] || "weixin.failed";
      setMessage("weixin", key, { error: true });
    }
  }

  async function loadWeixin() {
    try {
      renderWeixin(await api("/api/weixin"));
    } catch (error) {
      if (error.code === "authentication_required") return;
      if (error.code === "weixin_unavailable") {
        renderWeixin(null);
        return;
      }
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    }
  }

  function scheduleWeixinPoll(remaining) {
    stopWeixinPoll();
    state.weixinPoll = window.setTimeout(() => pollWeixinLogin(remaining), WEIXIN_POLL_MS);
  }

  async function pollWeixinLogin(remaining = WEIXIN_POLL_LIMIT) {
    if (!state.authenticated || remaining <= 0) {
      stopWeixinPoll();
      return;
    }
    try {
      const data = await api("/api/weixin/login");
      const login = data.login || null;
      if (state.weixin) renderWeixin({ ...state.weixin, login });
      else await loadWeixin();
      if (login && weixinLoginActive(login)) {
        scheduleWeixinPoll(remaining - 1);
        return;
      }
      stopWeixinPoll();
      if (login && login.status === "awaiting_confirmation") await loadWeixin();
    } catch (error) {
      stopWeixinPoll();
      if (error.code !== "authentication_required") {
        setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
      }
    }
  }

  async function startWeixinLogin() {
    if (state.weixinBusy) return;
    state.weixinBusy = true;
    const button = $("weixin-connect");
    setBusy(button, "weixin.connecting");
    setMessage("weixin", null);
    try {
      renderWeixin(await api("/api/weixin/login", { method: "POST", body: {} }));
      scheduleWeixinPoll(WEIXIN_POLL_LIMIT);
    } catch (error) {
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
      await loadWeixin();
    } finally {
      state.weixinBusy = false;
      clearBusy(button);
    }
  }

  async function cancelWeixinLogin() {
    stopWeixinPoll();
    const button = $("weixin-cancel");
    setBusy(button, "weixin.canceling");
    try {
      renderWeixin(await api("/api/weixin/login/cancel", { method: "POST", body: {} }));
      setMessage("weixin", null);
    } catch (error) {
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    } finally {
      clearBusy(button);
    }
  }

  async function submitWeixinCode(event) {
    if (event) event.preventDefault();
    const input = $("weixin-code");
    const code = input ? input.value.trim() : "";
    if (!/^[0-9]{1,12}$/.test(code)) {
      setMessage("weixin", "weixin.code_invalid", { error: true });
      return;
    }
    const button = $("weixin-code-submit");
    setBusy(button, "weixin.sending");
    setMessage("weixin", null);
    try {
      const data = await api("/api/weixin/login/code", { method: "POST", body: { code } });
      if (state.weixin) renderWeixin({ ...state.weixin, login: data.login || null });
      if (input) input.value = "";
      scheduleWeixinPoll(WEIXIN_POLL_LIMIT);
    } catch (error) {
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    } finally {
      clearBusy(button);
    }
  }

  async function confirmWeixinOwner() {
    const scanner = $("weixin-scanner");
    const owner = scanner ? scanner.textContent.trim() : "";
    if (!owner) {
      setMessage("weixin", "weixin.error.no_login", { error: true });
      return;
    }
    const button = $("weixin-confirm-button");
    setBusy(button, "weixin.confirming");
    try {
      renderWeixin(await api("/api/weixin/confirm", { method: "POST", body: { owner_id: owner } }));
      stopWeixinPoll();
      setMessage("weixin", "weixin.confirmed");
    } catch (error) {
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    } finally {
      clearBusy(button);
    }
  }

  async function toggleWeixin() {
    const data = state.weixin || {};
    const enable = !data.running;
    const button = $("weixin-toggle");
    setBusy(button, enable ? "weixin.enabling" : "weixin.disabling");
    try {
      renderWeixin(await api("/api/weixin/enabled", { method: "POST", body: { enabled: enable } }));
      setMessage("weixin", enable ? "weixin.enabled" : "weixin.disabled");
    } catch (error) {
      setMessage("weixin", errorKey(error, "weixin.failed"), { error: true });
    } finally {
      clearBusy(button);
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

  function taskWaitText(task, data) {
    if (task.status === "queued") {
      if (!task.enabled) return t("board.held");
      if (data && data.paused) return t("board.queue_paused");
      if (task.not_before && !task.due) return t("tasks.not_before", { time: formatTime(task.not_before) });
      const policy = data && data.policy;
      if (policy && policy.defer && policy.defer[task.priority] && !policy.in_window) return t("board.off_peak");
      return t("board.ready");
    }
    if (task.status === "failed") return reasonText(task.reason) || t("board.failed_hint");
    if (task.status === "running") {
      const latest = state.automation && state.automation.latest;
      return latest && latest.run_id === task.run_id && latest.stage
        ? translateKnown(`automation.stage.${latest.stage}`, latest.stage) : t("automation.status.running");
    }
    return t(task.status === "canceled" ? "tasks.status.canceled" : "board.finished");
  }

  function taskSourceText(source) {
    if (source === "maintenance:continuation") return t("board.continuation");
    if (source === "manual") return t("automation.trigger.manual");
    if (source === "schedule" || source === "scheduler") return t("automation.trigger.scheduler");
    return source || "";
  }

  function boardTasks() {
    const tasks = Array.isArray(state.tasks && state.tasks.tasks) ? state.tasks.tasks.map((task) => ({ ...task })) : [];
    const run = state.automation && state.automation.latest;
    tasks.filter((task) => task.status === "running").forEach((task) => {
      // Queue records receive their run_id at completion. A live run's explicit
      // task_id identifies the current attempt; never present an old retry as live.
      task.run_id = run && run.task_id === task.id ? run.run_id : "";
    });
    // Immediate/scheduled runs do not necessarily have a durable queue task.
    if (run && run.run_id && !tasks.some((task) => task.run_id === run.run_id || task.id === run.task_id)) {
      tasks.unshift({
        id: `run:${run.run_id}`, run_id: run.run_id, boardRun: true,
        title: t("board.direct_run"), kind: run.kind || "maintenance",
        source: run.trigger, status: ["pushed", "no_changes", "reported", "completed"].includes(run.status) ? "done" : run.status === "skipped" ? "failed" : run.status,
        reason: run.reason, created_at: run.started_at, finished_at: run.finished_at,
        runner: run.runner, model: run.model,
      });
    }
    return tasks;
  }

  function taskCard(task, data) {
    const row = document.createElement("article");
    row.className = "task-item";
    const head = document.createElement("div");
    head.className = "task-head";
    const title = document.createElement("button");
    title.type = "button";
    title.className = "task-title task-open";
    title.dataset.taskInspect = task.id;
    title.textContent = task.source === "maintenance:continuation" ? t("board.continuation") : task.title || task.kind || task.id;
    head.appendChild(title);
    if (task.priority) {
      const chip = document.createElement("span");
      chip.className = "task-priority";
      chip.dataset.priority = task.priority;
      chip.textContent = task.priority;
      head.appendChild(chip);
    }
    const meta = document.createElement("p");
    meta.className = "task-meta";
    const parts = [kindLabel(task.kind), taskSourceText(task.source)];
    if (task.runner) parts.push(`${task.runner}${task.model ? " · " + task.model : ""}`);
    const when = task.finished_at || task.created_at;
    if (when) parts.push(formatTime(when));
    if (task.attempts > 0) parts.push(t("tasks.attempts", { count: task.attempts }));
    meta.textContent = parts.filter(Boolean).join(" · ");
    const progress = document.createElement("p");
    progress.className = "task-progress";
    progress.dataset.state = task.status;
    progress.textContent = taskWaitText(task, data);
    row.append(head, meta, progress);
    const actions = document.createElement("div");
    actions.className = "task-actions";
    if (!task.boardRun) {
      if (task.status === "queued") {
        actions.appendChild(taskButton(task, "toggle", task.enabled ? "tasks.action.pause" : "tasks.action.resume"));
        actions.appendChild(taskButton(task, "run", "tasks.action.run"));
        actions.appendChild(taskButton(task, "cancel", "tasks.action.cancel"));
      } else if (["failed", "done"].includes(task.status)) actions.appendChild(taskButton(task, "run", "tasks.action.run"));
    }
    if (actions.childNodes.length) row.appendChild(actions);
    return row;
  }

  function renderTasks(data) {
    state.tasks = data || null;
    const board = $("tasks-list");
    if (!board) return;
    const query = $("task-search").value.trim().toLocaleLowerCase();
    const priority = $("task-priority-filter").value;
    const all = boardTasks();
    const filtered = all.filter((task) => (!priority || task.priority === priority) &&
      (!query || [task.title, task.kind, task.source, task.reason, task.runner, task.model, taskSourceText(task.source)].some((value) => typeof value === "string" && value.toLocaleLowerCase().includes(query))));
    const pending = all.filter((task) => ["queued", "running"].includes(task.status)).length;
    $("tasks-count").dataset.state = pending ? "enabled" : "disabled";
    $("tasks-count").textContent = String(pending);
    $("tasks-pause").querySelector(".btn-label").textContent = t(data && data.paused ? "tasks.resume" : "tasks.pause");
    $("tasks-pause").dataset.paused = data && data.paused ? "true" : "false";
    const policy = data && data.policy;
    $("tasks-policy").textContent = policy ? t(policy.in_window ? "tasks.policy_inside" : "tasks.policy_outside", {
      start: policy.window_start || "", end: policy.window_end || "",
    }) : "";
    $("board-summary").textContent = t("board.summary", { shown: filtered.length, total: all.length });
    const active = document.activeElement;
    const focus = active && board.contains(active) ? {
      inspect: active.dataset.taskInspect, action: active.dataset.taskAction, id: active.dataset.taskId,
    } : null;
    const scrollLeft = board.scrollLeft;
    const columnScroll = new Map(Array.from(board.querySelectorAll(".board-column")).map((column) => [column.dataset.column, column.querySelector(".board-column-body").scrollTop]));
    board.replaceChildren();
    const groups = [
      ["current", filtered.filter((task) => ["queued", "running"].includes(task.status))],
      ["attention", filtered.filter((task) => task.status === "failed").sort((a, b) => String(b.finished_at || b.updated_at || b.created_at || "").localeCompare(String(a.finished_at || a.updated_at || a.created_at || "")))],
      ["recent", filtered.filter((task) => ["done", "canceled"].includes(task.status)).sort((a, b) => String(b.finished_at || b.updated_at || b.created_at || "").localeCompare(String(a.finished_at || a.updated_at || a.created_at || "")))],
    ];
    const reviewRun = state.automation && state.automation.review_run;
    const findings = Array.isArray(reviewRun && reviewRun.needs_review) ? reviewRun.needs_review : [];
    const showReview = findings.length && !priority && (!query || findings.some((issue) => [issue.source, issue.reason, t("nav.review")].some((value) => typeof value === "string" && value.toLocaleLowerCase().includes(query))));
    groups.sort((a, b) => ["attention", "current", "recent"].indexOf(a[0]) - ["attention", "current", "recent"].indexOf(b[0]));
    groups.forEach(([key, tasks]) => {
      const column = document.createElement("section");
      column.className = "board-column";
      column.dataset.column = key;
      const heading = document.createElement("h3");
      heading.className = "board-column-heading";
      const name = document.createElement("span");
      name.textContent = t(`board.column.${key}`);
      const count = document.createElement("span");
      count.className = "board-column-count";
      count.textContent = String(tasks.length + (key === "attention" && showReview ? 1 : 0));
      heading.append(name, count);
      const body = document.createElement("div");
      body.className = "board-column-body";
      if (key === "attention" && showReview) {
        const card = document.createElement("article");
        card.className = "task-item review-task";
        const link = document.createElement("a");
        link.href = "#review";
        link.className = "task-title";
        link.textContent = t("board.review_title", { count: findings.length });
        const note = document.createElement("p");
        note.className = "task-meta";
        note.textContent = t("board.review_note", { time: formatTime(reviewRun.finished_at || reviewRun.started_at) });
        card.append(link, note);
        body.appendChild(card);
      }
      tasks.forEach((task) => body.appendChild(taskCard(task, data)));
      if (!body.childNodes.length) {
        const empty = document.createElement("p");
        empty.className = "board-empty";
        empty.textContent = t(data ? `board.empty.${key}` : "board.loading");
        body.appendChild(empty);
      }
      column.append(heading, body);
      board.appendChild(column);
      body.scrollTop = columnScroll.get(key) || 0;
    });
    board.scrollLeft = scrollLeft;
    if (focus) {
      const replacement = Array.from(board.querySelectorAll("button")).find((button) => focus.inspect
        ? button.dataset.taskInspect === focus.inspect
        : focus.action && button.dataset.taskAction === focus.action && button.dataset.taskId === focus.id);
      if (replacement) replacement.focus({ preventScroll: true });
    }
    syncInspectedTask();
  }

  let inspectedTask = "";
  let inspectGeneration = 0;
  let inspectedRun = null;
  let inspectedRunId = "";
  function renderTaskContext(task) {
    $("task-detail-context").textContent = `${task.title || task.kind} · ${taskWaitText(task, state.tasks)}${task.status === "queued" && task.run_id ? " · " + t("board.previous_attempt") : ""}`;
  }

  function syncInspectedTask() {
    if (!$("task-detail").open) return;
    const task = boardTasks().find((item) => item.id === inspectedTask);
    if (!task) return;
    renderTaskContext(task);
    const latest = state.automation && state.automation.latest;
    if (task.run_id && latest && latest.run_id === task.run_id) {
      if (inspectedRun && inspectedRun.run_id === latest.run_id && inspectedRun.status !== "running" && latest.status === "running") return;
      if (JSON.stringify(inspectedRun) !== JSON.stringify(latest)) {
        inspectGeneration += 1;
        inspectedRunId = task.run_id;
        inspectedRun = latest;
        renderLatestRun(latest, "task-detail-body");
      }
    } else if ((task.run_id || "") !== inspectedRunId) {
      inspectTask(task.id);
    }
  }

  async function inspectTask(id) {
    const task = boardTasks().find((item) => item.id === id);
    if (!task) return;
    inspectedTask = id;
    const generation = ++inspectGeneration;
    inspectedRun = null;
    inspectedRunId = task.run_id || "";
    const dialog = $("task-detail");
    const body = $("task-detail-body");
    renderTaskContext(task);
    body.replaceChildren();
    const title = document.createElement("h3");
    title.textContent = task.title || task.kind;
    body.appendChild(title);
    const status = document.createElement("p");
    status.className = "notice";
    status.textContent = taskWaitText(task, state.tasks);
    body.appendChild(status);
    if (!dialog.open) dialog.showModal();
    if (!task.run_id) {
      const note = document.createElement("p");
      note.textContent = t(task.status === "running" ? "board.starting_attempt" : "board.no_attempt");
      body.appendChild(note);
      return;
    }
    const loading = document.createElement("p");
    loading.textContent = t("automation.loading");
    body.appendChild(loading);
    try {
      const run = await api(`/api/automation/runs/${encodeURIComponent(task.run_id)}`);
      if (inspectGeneration === generation && dialog.open && state.authenticated) {
        inspectedRun = run;
        renderLatestRun(run, "task-detail-body");
      }
    } catch (error) {
      if (inspectGeneration === generation && dialog.open) loading.textContent = t(errorKey(error, "automation.load_failed"));
    }
  }

  async function refreshTasks() {
    try {
      const data = await api("/api/tasks");
      renderTasks(data);
      setMessage("tasks", null);
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



  /* --------------------------------------------------------------- catalog */

  let catalogFieldSerial = 0;

  function catalogLabel(action) {
    return translateKnown(action.label_key, action.label || action.id);
  }

  function catalogDescription(action) {
    return translateKnown(action.description_key, action.description || "");
  }

  function catalogSubmitLabel(action) {
    return translateKnown(action.submit_label_key, action.submit_label || t("catalog.submit"));
  }

  function catalogGroupName(action) {
    const presentation = action.presentation && typeof action.presentation === "object" ? action.presentation : null;
    if (presentation && typeof presentation.group === "string" && presentation.group.trim()) return presentation.group.trim();
    if (typeof action.group === "string" && action.group.trim()) return action.group.trim();
    return "actions";
  }

  function catalogRequirementReason(name) {
    if (name === "workshop") return t("catalog.requires.workshop");
    const known = { assistant: "chat", report: "report", maintenance: "maintenance", context: "context" };
    if (!Object.prototype.hasOwnProperty.call(known, name)) {
      return t("catalog.requires_unavailable", { name });
    }
    const cap = capability(known[name]);
    if (cap.ready) return "";
    const missing = Array.isArray(cap.missing) ? cap.missing : [];
    const reasons = missing.map((item) => (typeof item === "string" ? item : item && item.code)).filter(Boolean);
    const translated = reasons.map((code) => translateKnown(`daily.missing.${code}`, "")).filter(Boolean);
    if (translated.length) return translated.join(" · ");
    return t(`daily.missing.${known[name]}`);
  }

  function catalogBlockedReasons(action) {
    const reasons = [];
    if (action.enabled === false) reasons.push(t("catalog.disabled"));
    const requires = Array.isArray(action.requires) ? action.requires : typeof action.requires === "string" ? [action.requires] : [];
    requires.forEach((name) => {
      if (typeof name !== "string" || !name) return;
      const reason = catalogRequirementReason(name);
      if (reason && !reasons.includes(reason)) reasons.push(reason);
    });
    return reasons;
  }

  function catalogFieldLabel(field) {
    return field && typeof field.label === "string" && field.label.trim() ? field.label.trim() : field && field.name ? field.name : "";
  }

  function catalogFieldNode(field) {
    if (!field || typeof field.name !== "string" || !field.name) return null;
    const type = field.type;
    if (!["text", "textarea", "select", "boolean"].includes(type)) return null;
    catalogFieldSerial += 1;
    const id = `catalog-field-${catalogFieldSerial}`;
    const wrapper = document.createElement("div");
    wrapper.className = "field";
    const label = document.createElement("label");
    label.htmlFor = id;
    label.textContent = catalogFieldLabel(field);
    let control;
    if (type === "textarea") {
      control = document.createElement("textarea");
      control.rows = 3;
    } else if (type === "select") {
      control = document.createElement("select");
      const options = Array.isArray(field.options) ? field.options : [];
      const placeholder = typeof field.placeholder === "string" ? field.placeholder : "";
      if (field.required || placeholder) {
        const blank = document.createElement("option");
        blank.value = "";
        blank.textContent = placeholder || t("catalog.select_placeholder");
        if (placeholder) blank.disabled = true;
        blank.selected = true;
        control.appendChild(blank);
      }
      options.forEach((option) => {
        if (!option || typeof option.value !== "string") return;
        const item = document.createElement("option");
        item.value = option.value;
        item.textContent = typeof option.label === "string" && option.label ? option.label : option.value;
        control.appendChild(item);
      });
    } else if (type === "boolean") {
      control = document.createElement("input");
      control.type = "checkbox";
    } else {
      control = document.createElement("input");
      control.type = "text";
    }
    control.id = id;
    control.name = field.name;
    control.dataset.catalogField = field.name;
    control.dataset.catalogType = type;
    if (field.required === true) control.required = true;
    if (typeof field.placeholder === "string" && field.placeholder && type !== "select") control.placeholder = field.placeholder;
    if (Number.isInteger(field.max_length) && field.max_length > 0 && (type === "text" || type === "textarea")) {
      control.maxLength = field.max_length;
    }
    if (type === "boolean") {
      label.className = "checkbox";
      label.append(control, document.createTextNode(" " + catalogFieldLabel(field)));
      wrapper.appendChild(label);
      return wrapper;
    }
    wrapper.append(label, control);
    return wrapper;
  }

  function catalogActionCard(action) {
    const article = document.createElement("article");
    article.className = "catalog-action";
    article.dataset.catalogAction = action.id;

    const heading = document.createElement("h4");
    heading.textContent = catalogLabel(action);
    article.appendChild(heading);
    const description = catalogDescription(action);
    if (description) {
      const text = document.createElement("p");
      text.className = "hint";
      text.textContent = description;
      article.appendChild(text);
    }
    const blocked = catalogBlockedReasons(action);

    const form = document.createElement("form");
    form.className = "form catalog-action-form";
    form.noValidate = true;
    form.dataset.catalogForm = action.id;
    const fieldset = document.createElement("fieldset");
    fieldset.className = "catalog-fields";
    if (blocked.length) fieldset.disabled = true;
    const fields = Array.isArray(action.fields) ? action.fields : [];
    fields.forEach((field) => {
      const node = catalogFieldNode(field);
      if (node) fieldset.appendChild(node);
    });
    const priorityControl = fieldset.querySelector('select[data-catalog-field="priority"]');
    if (priorityControl && typeof action.priority === "string") {
      const option = Array.from(priorityControl.options).find((item) => item.value === action.priority);
      if (option) {
        // Keep the catalog default across form.reset() after a submitted action.
        Array.from(priorityControl.options).forEach((item) => { item.defaultSelected = item === option; });
        priorityControl.value = action.priority;
      }
    }
    const actions = document.createElement("div");
    actions.className = "actions";
    const submit = document.createElement("button");
    submit.type = "submit";
    submit.className = "btn primary";
    submit.disabled = blocked.length > 0 || Boolean(state.catalogBusy[action.id]);
    const submitLabel = document.createElement("span");
    submitLabel.className = "btn-label";
    submitLabel.textContent = catalogSubmitLabel(action);
    submit.appendChild(submitLabel);
    actions.appendChild(submit);
    fieldset.appendChild(actions);
    form.appendChild(fieldset);
    article.appendChild(form);

    if (blocked.length) {
      const reason = document.createElement("p");
      reason.className = "hint catalog-requirement";
      reason.dataset.catalogRequirement = "true";
      reason.textContent = blocked.join(" · ");
      article.appendChild(reason);
    }

    const message = document.createElement("p");
    message.className = "form-message";
    message.dataset.catalogMessage = "true";
    message.setAttribute("role", "status");
    message.setAttribute("aria-live", "polite");
    const notice = state.catalogNotices.get(action.id);
    if (notice) {
      message.textContent = t(notice.key, notice.vars);
      if (notice.error) message.dataset.error = "true";
    }
    article.appendChild(message);

    const result = document.createElement("div");
    result.dataset.catalogResult = "true";
    const task = state.catalogResults.get(action.id);
    if (task) result.appendChild(chatTaskCard(task));
    article.appendChild(result);
    return article;
  }

  function renderCatalog() {
    const list = $("catalog-list");
    const hint = $("catalog-hint");
    if (!list || !hint) return;
    const drafts = new Map();
    let focused = null;
    document.querySelectorAll("[data-catalog-action]").forEach((article) => {
      const values = {};
      article.querySelectorAll("[data-catalog-field]").forEach((control) => {
        values[control.dataset.catalogField] = control.type === "checkbox" ? control.checked : control.value;
        if (document.activeElement === control) focused = { action: article.dataset.catalogAction, field: control.dataset.catalogField };
      });
      drafts.set(article.dataset.catalogAction, values);
    });
    list.replaceChildren();
    if (state.catalogError) {
      hint.hidden = false;
      hint.textContent = t("catalog.unavailable");
      return;
    }
    if (!state.catalogLoaded) {
      hint.hidden = false;
      hint.textContent = t("catalog.loading");
      return;
    }
    const actions = Array.isArray(state.catalog) ? state.catalog : [];
    if (!actions.length) {
      hint.hidden = false;
      hint.textContent = t("catalog.empty");
      return;
    }
    hint.hidden = true;
    const groups = new Map();
    actions.forEach((action, index) => {
      if (!action || typeof action.id !== "string" || !action.id || action.id.length > 128) return;
      const name = catalogGroupName(action);
      if (!groups.has(name)) groups.set(name, []);
      groups.get(name).push({ action, index });
    });
    groups.forEach((entries, name) => {
      entries.sort((a, b) => {
        const left = Number.isFinite(Number(a.action.order)) ? Number(a.action.order) : a.index;
        const right = Number.isFinite(Number(b.action.order)) ? Number(b.action.order) : b.index;
        return left - right || a.index - b.index;
      });
      const group = document.createElement("section");
      group.className = "catalog-group";
      const heading = document.createElement("h3");
      heading.textContent = translateKnown(`catalog.group.${name}`, name);
      group.appendChild(heading);
      entries.forEach(({ action }) => {
        const card = catalogActionCard(action);
        const draft = drafts.get(action.id);
        if (draft) {
          card.querySelectorAll("[data-catalog-field]").forEach((control) => {
            const name = control.dataset.catalogField;
            if (!Object.prototype.hasOwnProperty.call(draft, name)) return;
            if (control.type === "checkbox") control.checked = Boolean(draft[name]);
            else control.value = String(draft[name]);
          });
        }
        group.appendChild(card);
      });
      list.appendChild(group);
    });
    if (focused) {
      const control = Array.from(list.querySelectorAll("[data-catalog-field]")).find((item) =>
        item.closest("[data-catalog-action]")?.dataset.catalogAction === focused.action && item.dataset.catalogField === focused.field);
      if (control) control.focus({ preventScroll: true });
    }
  }

  function updateCatalogAvailability() {
    const actions = Array.isArray(state.catalog) ? state.catalog : [];
    document.querySelectorAll("[data-catalog-action]").forEach((article) => {
      const action = actions.find((item) => item && item.id === article.dataset.catalogAction);
      if (!action) return;
      const blocked = catalogBlockedReasons(action);
      const fieldset = article.querySelector("fieldset.catalog-fields");
      if (fieldset) fieldset.disabled = blocked.length > 0;
      const submit = article.querySelector("button[type=submit]");
      if (submit) submit.disabled = blocked.length > 0 || Boolean(state.catalogBusy[action.id]);
      let reason = article.querySelector("[data-catalog-requirement]");
      if (blocked.length) {
        if (!reason) {
          reason = document.createElement("p");
          reason.className = "hint catalog-requirement";
          reason.dataset.catalogRequirement = "true";
          const form = article.querySelector("form");
          if (form) article.insertBefore(reason, form.nextSibling);
          else article.appendChild(reason);
        }
        reason.textContent = blocked.join(" · ");
      } else if (reason) {
        reason.remove();
      }
    });
  }

  async function loadCatalog() {
    const epoch = state.sessionEpoch;
    try {
      const data = await api("/api/catalog");
      if (!state.authenticated || epoch !== state.sessionEpoch) return;
      state.catalog = Array.isArray(data.actions) ? data.actions : [];
      state.catalogLoaded = true;
      state.catalogError = false;
      renderCatalog();
    } catch (error) {
      if (error.code === "authentication_required") return;
      if (!state.authenticated || epoch !== state.sessionEpoch) return;
      state.catalog = [];
      state.catalogLoaded = true;
      state.catalogError = true;
      renderCatalog();
    }
  }

  function setCatalogNotice(id, key, options = {}) {
    state.catalogNotices.set(id, { key, vars: options.vars || null, error: Boolean(options.error) });
    document.querySelectorAll("[data-catalog-action]").forEach((article) => {
      if (article.dataset.catalogAction !== id) return;
      const message = article.querySelector("[data-catalog-message]");
      if (!message) return;
      message.textContent = t(key, options.vars);
      message.dataset.error = options.error ? "true" : "false";
    });
  }

  function catalogFieldValues(form, action) {
    const fields = {};
    let focus = null;
    const controls = form.querySelectorAll("[data-catalog-field]");
    for (const control of controls) {
      const name = control.dataset.catalogField;
      const definition = (Array.isArray(action.fields) ? action.fields : []).find((field) => field && field.name === name);
      if (!definition) continue;
      const type = control.dataset.catalogType;
      if (type === "boolean") {
        fields[name] = control.checked;
        if (definition.required === true && !control.checked) {
          focus = control;
          return { fields, focus };
        }
        continue;
      }
      const value = control.value;
      if (definition.required === true && !value.trim()) {
        focus = control;
        return { fields, focus };
      }
      if (Number.isInteger(definition.max_length) && definition.max_length > 0 && Array.from(value).length > definition.max_length) {
        focus = control;
        return { fields, focus };
      }
      if (type === "select" && !value && definition.required !== true) continue;
      fields[name] = type === "select" ? value : value.trim();
    }
    return { fields, focus };
  }

  async function runCatalogAction(event) {
    event.preventDefault();
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    const id = form.dataset.catalogForm;
    const action = (Array.isArray(state.catalog) ? state.catalog : []).find((item) => item && item.id === id);
    if (!action || state.catalogBusy[id]) return;
    if (catalogBlockedReasons(action).length) {
      setCatalogNotice(id, "catalog.action_disabled", { error: true });
      return;
    }
    const { fields, focus } = catalogFieldValues(form, action);
    if (focus) {
      const definition = (action.fields || []).find((field) => field && field.name === focus.dataset.catalogField) || {};
      setCatalogNotice(id, "catalog.invalid_field", { error: true, vars: { field: catalogFieldLabel(definition) } });
      focus.focus();
      return;
    }
    const epoch = state.sessionEpoch;
    state.catalogBusy[id] = true;
    const button = form.querySelector("button[type=submit]");
    setBusy(button, "catalog.submitting");
    try {
      const result = await api(`/api/actions/${encodeURIComponent(id)}/run`, { method: "POST", body: { fields } });
      if (!state.authenticated || epoch !== state.sessionEpoch) return;
      const task = result.task || result;
      if (task && typeof task === "object") {
        state.catalogResults.set(id, task);
        const article = form.closest("article[data-catalog-action]");
        const container = article ? article.querySelector("[data-catalog-result]") : null;
        if (container) container.replaceChildren(chatTaskCard(task));
      }
      setCatalogNotice(id, "catalog.accepted");
      form.reset();
      await refreshTasks();
    } catch (error) {
      if (state.authenticated && epoch === state.sessionEpoch) {
        setCatalogNotice(id, errorKey(error, "catalog.failed"), { error: true });
      }
    } finally {
      delete state.catalogBusy[id];
      clearBusy(button);
    }
  }

  /* --------------------------------------------------------------- actions */

  function renderActions() {
    const list = $("actions-list");
    if (!list) return;
    while (list.firstChild) list.removeChild(list.firstChild);
    if (!state.actions.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("actions.empty");
      list.appendChild(empty);
      return;
    }
    state.actions.forEach((action) => {
      if (!action || typeof action.id !== "string") return;
      const button = document.createElement("button");
      button.type = "button";
      button.className = "btn ghost small action-row";
      button.dataset.actionId = action.id;
      button.textContent = `${action.label || action.id} · ${action.id}${action.enabled === false ? " · " + t("actions.off") : ""}`;
      list.appendChild(button);
    });
  }

  function selectAction(id) {
    const action = state.actions.find((item) => item && item.id === id);
    if (!action) return;
    state.actionSelected = id;
    const editor = $("action-editor");
    if (editor) editor.value = JSON.stringify(action, null, 2);
    setMessage("actions", "actions.selected", { vars: { id } });
  }

  function parseActionEditor() {
    const editor = $("action-editor");
    if (!editor || !editor.value.trim()) {
      setMessage("actions", "actions.select", { error: true });
      return null;
    }
    try {
      const value = JSON.parse(editor.value);
      if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("not an object");
      return value;
    } catch (error) {
      setMessage("actions", "actions.invalid_json", { error: true });
      return null;
    }
  }

  async function loadActions() {
    try {
      const data = await api("/api/actions");
      state.actions = Array.isArray(data.actions) ? data.actions : [];
      const hosts = $("action-hosts");
      if (hosts) hosts.value = Array.isArray(data.http_hosts) ? data.http_hosts.join("\n") : "";
      renderActions();
      await loadActionVersions();
    } catch (error) {
      if (error.code !== "authentication_required") setMessage("actions", "actions.failed", { error: true });
    }
  }

  async function validateAction() {
    if (state.actionsBusy) return;
    const action = parseActionEditor();
    if (!action) return;
    state.actionsBusy = true;
    const button = $("action-validate");
    setBusy(button, "common.running");
    try {
      await api("/api/actions/validate", { method: "POST", body: { action } });
      setMessage("actions", "actions.valid");
    } catch (error) {
      setMessage("actions", "actions.invalid", { error: true });
    } finally {
      state.actionsBusy = false;
      clearBusy(button);
    }
  }

  async function saveAction() {
    if (state.actionsBusy) return;
    const action = parseActionEditor();
    if (!action) return;
    state.actionsBusy = true;
    const button = $("action-save");
    setBusy(button, "common.running");
    try {
      const data = await api("/api/actions", { method: "POST", body: { action } });
      state.actions = Array.isArray(data.actions) ? data.actions : state.actions;
      renderActions();
      await loadActionVersions();
      setMessage("actions", "actions.saved");
    } catch (error) {
      setMessage("actions", "actions.invalid", { error: true });
    } finally {
      state.actionsBusy = false;
      clearBusy(button);
    }
  }

  async function disableAction() {
    if (state.actionsBusy || !state.actionSelected) {
      setMessage("actions", "actions.select", { error: true });
      return;
    }
    state.actionsBusy = true;
    const button = $("action-disable");
    setBusy(button, "common.running");
    try {
      const data = await api(`/api/actions/${encodeURIComponent(state.actionSelected)}/disable`, { method: "POST", body: {} });
      state.actions = Array.isArray(data.actions) ? data.actions : state.actions;
      renderActions();
      await loadActionVersions();
      setMessage("actions", "actions.disabled");
    } catch (error) {
      setMessage("actions", "actions.failed", { error: true });
    } finally {
      state.actionsBusy = false;
      clearBusy(button);
    }
  }

  async function saveActionHosts() {
    if (state.actionsBusy) return;
    const field = $("action-hosts");
    const hosts = field ? field.value.split(/[\n,]/).map((host) => host.trim()).filter(Boolean) : [];
    state.actionsBusy = true;
    const button = $("action-hosts-save");
    setBusy(button, "common.running");
    try {
      await api("/api/actions/hosts", { method: "POST", body: { hosts } });
      await loadActions();
      setMessage("actions", "actions.hosts_saved");
    } catch (error) {
      setMessage("actions", "actions.hosts_invalid", { error: true });
    } finally {
      state.actionsBusy = false;
      clearBusy(button);
    }
  }

  async function loadActionVersions() {
    const list = $("action-versions");
    if (!list) return;
    try {
      const data = await api("/api/actions/versions");
      state.actionVersions = Array.isArray(data.versions) ? data.versions : [];
    } catch (error) {
      state.actionVersions = [];
    }
    while (list.firstChild) list.removeChild(list.firstChild);
    if (!state.actionVersions.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("actions.no_versions");
      list.appendChild(empty);
      return;
    }
    state.actionVersions.forEach((version) => {
      if (!version || typeof version.name !== "string") return;
      const button = document.createElement("button");
      button.type = "button";
      button.className = "btn ghost small action-row";
      button.dataset.actionVersion = version.name;
      button.textContent = `${version.name} · ${t("actions.restore")}`;
      list.appendChild(button);
    });
  }

  async function restoreActionVersion(name) {
    if (state.actionsBusy || !name) return;
    state.actionsBusy = true;
    try {
      const data = await api("/api/actions/restore", { method: "POST", body: { version: name } });
      state.actions = Array.isArray(data.actions) ? data.actions : state.actions;
      renderActions();
      await loadActionVersions();
      setMessage("actions", "actions.restored");
    } catch (error) {
      setMessage("actions", "actions.failed", { error: true });
    } finally {
      state.actionsBusy = false;
    }
  }

  /* ----------------------------------------------------------- main agent */

  const AGENT_PRESETS = {
    deepseek: "https://api.deepseek.com/v1",
    openai: "https://api.openai.com/v1",
    openrouter: "https://openrouter.ai/api/v1",
  };

  function renderAgentBadge(data) {
    const badge = $("agent-badge");
    if (!badge) return;
    if (data && data.available) {
      badge.dataset.state = "enabled";
      badge.textContent = t("agent.badge.available");
    } else if (data && data.configured && data.kind === "openai" && !data.key_present) {
      badge.dataset.state = "disabled";
      badge.textContent = t("agent.badge.key_missing");
    } else if (data && data.configured && data.error) {
      badge.dataset.state = "failed";
      badge.textContent = t("agent.badge.invalid");
    } else {
      badge.dataset.state = "disabled";
      badge.textContent = t("agent.badge.unconfigured");
    }
  }

  function renderAgentText(data) {
    renderAgentBadge(data);
    const status = $("agent-status");
    const detail = $("agent-detail");
    if (status) {
      if (!data || !data.configured) status.textContent = t("agent.status.unconfigured");
      else if (data.available) status.textContent = t("agent.status.available", {
        kind: data.kind === "openai" ? (data.model || "openai") : (data.command || "command"),
        provider: data.provider || "custom",
      });
      else if (data.kind === "openai" && !data.key_present) status.textContent = t("agent.status.key_missing");
      else if (data.error) status.textContent = t("agent.status.invalid");
      else status.textContent = t("agent.status.not_available");
    }
    if (detail) {
      const parts = [];
      if (data && data.source) parts.push(t("agent.source", { source: data.source }));
      if (data && data.kind === "openai" && data.base_url) parts.push(data.base_url);
      if (data && data.kind === "command" && data.command) parts.push(data.command);
      detail.textContent = parts.filter(Boolean).join(" · ");
    }
    const keyHint = $("agent-key-hint");
    if (keyHint) keyHint.textContent = t(data && data.key_present ? "agent.api_key_saved" : "agent.api_key_hint");
    updateChatAgentHint();
  }

  function agentKindChanged() {
    const kind = $("agent-kind") ? $("agent-kind").value : "openai";
    if ($("agent-openai-fields")) $("agent-openai-fields").hidden = kind !== "openai";
    if ($("agent-command-fields")) $("agent-command-fields").hidden = kind !== "command";
  }

  function agentProviderChanged() {
    const provider = $("agent-provider") ? $("agent-provider").value : "";
    const input = $("agent-base-url");
    if (!input) return;
    const preset = AGENT_PRESETS[provider] || "";
    const current = input.value.trim();
    if (!current || Object.values(AGENT_PRESETS).includes(current)) input.value = preset;
  }

  function renderAgentConfig(data, { fill = false } = {}) {
    state.agent = data || null;
    renderAgentText(state.agent);
    renderSetup();
    if (!fill || !data) return;
    if ($("agent-kind")) $("agent-kind").value = data.kind === "command" ? "command" : "openai";
    if ($("agent-provider")) $("agent-provider").value = AGENT_PRESETS[data.provider] ? data.provider : "";
    if ($("agent-base-url")) $("agent-base-url").value = typeof data.base_url === "string" ? data.base_url : "";
    if ($("agent-model")) $("agent-model").value = typeof data.model === "string" ? data.model : "";
    if ($("agent-timeout")) $("agent-timeout").value = String(data.timeout_seconds || 120);
    if ($("agent-command")) $("agent-command").value = typeof data.command === "string" ? data.command : "";
    if ($("agent-args")) $("agent-args").value = Array.isArray(data.args) ? data.args.join("\n") : "";
    if ($("agent-api-key")) $("agent-api-key").value = "";
    agentKindChanged();
  }

  function updateChatAgentHint() {
    const hint = $("chat-config-hint");
    if (!hint) return;
    hint.hidden = capability("chat").ready;
    $("chat-source-hint").textContent = t(state.lastState && (state.lastState.capabilities && state.lastState.capabilities.context ? state.lastState.capabilities.context.available : state.lastState.repository && state.lastState.repository.connected) ? "daily.source_connected" : "daily.source_absent");
  }

  async function loadAgentConfig(options = {}) {
    try {
      const data = await api("/api/agent/config");
      renderAgentConfig(data, { fill: options.fill === true || !state.agent });
    } catch (error) {
      if (error.code === "authentication_required") return;
      renderAgentConfig(null);
      setMessage("agent", "agent.failed", { error: true });
    }
  }

  async function saveAgentConfig(event) {
    if (event) event.preventDefault();
    if (state.agentBusy) return;
    const kind = $("agent-kind") ? $("agent-kind").value : "openai";
    const body = {
      kind,
      timeout_seconds: Number.parseInt($("agent-timeout").value, 10) || 120,
    };
    if (kind === "openai") {
      body.provider = $("agent-provider").value;
      body.base_url = $("agent-base-url").value.trim();
      body.model = $("agent-model").value.trim();
      const key = $("agent-api-key").value.trim();
      if (key) body.api_key = key;
    } else {
      body.command = $("agent-command").value.trim();
      body.args = $("agent-args").value.split("\n").map((item) => item.trim()).filter(Boolean);
    }
    state.agentBusy = true;
    const button = $("agent-save");
    setBusy(button, "agent.saving");
    setMessage("agent", null);
    try {
      const data = await api("/api/agent/config", { method: "POST", body });
      renderAgentConfig(data, { fill: true });
      setMessage("agent", "agent.saved");
      await refreshState();
      if (state.agent && !state.agent.available && state.agent.error) {
        setMessage("agent", "agent.saved_unavailable", { error: true });
      }
    } catch (error) {
      const key = error.code === "agent_key_invalid" ? "agent.key_invalid"
        : error.code === "agent_config_invalid" ? "agent.invalid" : "agent.failed";
      setMessage("agent", key, { error: true });
    } finally {
      state.agentBusy = false;
      clearBusy(button);
    }
  }

  /* ------------------------------------------------------- model services */

  function modelServiceParts(value) {
    const text = typeof value === "string" ? value.trim() : "";
    const slash = text.indexOf("/");
    return slash > 0 ? { connection: text.slice(0, slash), model: text.slice(slash + 1) } : { connection: text, model: "" };
  }

  function fillConnectionSelect(select, value = "") {
    select.replaceChildren(); const blank = document.createElement("option"); blank.value = ""; blank.textContent = t("model_services.empty"); select.appendChild(blank);
    Object.entries(state.modelServices && state.modelServices.connections || {}).forEach(([id, entry]) => { const option = document.createElement("option"); option.value = id; option.textContent = entry.name || id; select.appendChild(option); }); select.value = value;
  }
  function selectModelConnection(id) {
    const entry = state.modelServices && state.modelServices.connections && state.modelServices.connections[id] || {};
    $("model-service-id").value = id || "service-" + Math.random().toString(36).slice(2, 12); $("model-service-name").value = entry.name || ""; $("model-service-url").value = entry.base_url || ""; $("model-service-key").value = "";
    $("model-service-key-hint").textContent = t(entry.key_present ? "model_services.key_saved" : "model_services.key_hint");
  }

  function renderModelOverrides(overrides = {}) {
    const list = $("model-overrides-list"); if (!list) return;
    const datalist = $("model-task-kind-options"); if (datalist) { datalist.replaceChildren(); const kinds = Array.isArray(state.kinds) ? state.kinds : []; kinds.forEach((kind) => { const option = document.createElement("option"); option.value = kind.id || ""; option.label = kind.label || kind.id || ""; datalist.appendChild(option); }); }
    list.replaceChildren();
    Object.entries(overrides && overrides.report && typeof overrides.report === "object" ? overrides.report : {}).forEach(([actionId, value]) => {
      const row = modelOverrideRow(actionId, value || {}); list.appendChild(row);
    });
  }

  function modelOverrideRow(actionId = "", value = {}) {
    const row = document.createElement("div"); row.className = "model-override-row";
    const id = document.createElement("input"); id.className = "model-override-id"; id.type = "text"; id.maxLength = 200; id.placeholder = t("model_services.override_id"); id.setAttribute("list", "model-task-kind-options"); id.value = actionId;
    const connection = document.createElement("select"); connection.className = "model-override-connection"; connection.setAttribute("aria-label", t("model_services.connection_name")); fillConnectionSelect(connection, value.connection || "");
    const model = document.createElement("input"); model.className = "model-override-model"; model.type = "text"; model.maxLength = 200; model.placeholder = "model or group/name"; model.setAttribute("aria-label", t("agent.model")); model.value = value.model || "";
    const remove = document.createElement("button"); remove.type = "button"; remove.className = "btn ghost small model-override-remove"; remove.textContent = t("model_services.override_remove");
    row.append(id, connection, model, remove); return row;
  }

  function addModelOverride() { const list = $("model-overrides-list"); if (list) { const row = modelOverrideRow(); list.appendChild(row); row.querySelector(".model-override-id").focus(); } }

  function renderModelServices(data, { fill = false } = {}) {
    state.modelServices = data || null;
    renderSetup();
    const connections = data && data.connections && typeof data.connections === "object" ? data.connections : {};
    const ids = Object.keys(connections);
    const badge = $("model-services-badge");
    if (badge) { badge.textContent = ids.length ? t("model_services.configured") : t("model_services.empty"); badge.dataset.state = ids.length ? "enabled" : "unknown"; }
    if (!fill) return;
    fillConnectionSelect($("model-service-select")); renderModelOverrides(data && data.overrides);
    const first = ids[0] || "";
    const connection = first ? connections[first] || {} : {};
    const defaults = data && data.defaults && typeof data.defaults === "object" ? data.defaults : {};
    const set = (id, value) => { const node = $(id); if (node) node.value = value || ""; };
    $("model-service-select").value = first; selectModelConnection(first);
    ["assistant", "report", "maintenance"].forEach((task) => { const assignment = defaults[task] || {}; fillConnectionSelect($("model-task-" + task + "-connection"), assignment.connection || ""); set(`model-task-${task}`, assignment.model || ""); });
    const hint = $("model-service-key-hint"); if (hint) hint.textContent = connection.key_present ? t("model_services.key_saved") : t("model_services.key_hint");
  }

  async function loadModelServices(options = {}) {
    try { renderModelServices(await api("/api/model-services"), { fill: options.fill === true || !state.modelServices }); }
    catch (error) { if (error.code !== "authentication_required") setMessage("model-services", "model_services.failed", { error: true }); }
  }

  function modelServiceBody() {
    const id = $("model-service-id").value.trim();
    const existing = state.modelServices && state.modelServices.connections && typeof state.modelServices.connections === "object" ? state.modelServices.connections : {};
    const connection = { name: $("model-service-name").value.trim(), base_url: $("model-service-url").value.trim() };
    const key = $("model-service-key").value.trim(); if (key) connection.api_key = key;
    const defaults = {};
    ["assistant", "report", "maintenance"].forEach((task) => { const connection = $("model-task-" + task + "-connection").value; const model = $("model-task-" + task).value.trim(); defaults[task] = connection && model ? { connection, model } : {}; });
    const overrides = { report: {} };
    document.querySelectorAll("#model-overrides-list .model-override-row").forEach((row) => {
      const actionId = row.querySelector(".model-override-id").value.trim(); const connectionId = row.querySelector(".model-override-connection").value.trim(); const model = row.querySelector(".model-override-model").value.trim();
      if (actionId && connectionId && model) overrides.report[actionId] = { connection: connectionId, model };
    });
    const connections = Object.fromEntries(Object.entries(existing).map(([id, entry]) => [id, { name: entry.name || "", base_url: entry.base_url || "" }])); if (id) connections[id] = connection; return { version: 1, connections, defaults, overrides };
  }

  async function saveModelServices(event) {
    if (event) event.preventDefault(); if (state.modelServicesBusy) return;
    state.modelServicesBusy = true; const button = $("model-services-save"); setBusy(button, "model_services.saving"); setMessage("model-services", null);
    try { renderModelServices(await api("/api/model-services", { method: "POST", body: modelServiceBody() }), { fill: true }); setMessage("model-services", "model_services.saved"); await loadAgentConfig(); await refreshState(); }
    catch (error) { setMessage("model-services", errorKey(error, "model_services.failed"), { error: true }); }
    finally { state.modelServicesBusy = false; clearBusy(button); }
  }

  function renderServiceModelOptions(data, connection) {
    const list = $("model-service-model-options"); list.replaceChildren();
    (Array.isArray(data.models) ? data.models : []).slice(0, 500).forEach((entry) => { if (!entry || typeof entry.id !== "string") return; const option = document.createElement("option"); option.value = entry.id; list.appendChild(option); });
    ["assistant", "report", "maintenance"].forEach((task) => { const input = $("model-task-" + task); if ($("model-task-" + task + "-connection").value === connection) input.setAttribute("list", "model-service-model-options"); else input.removeAttribute("list"); });
  }

  async function testModelService() {
    if (state.modelServicesBusy) return; state.modelServicesBusy = true; const button = $("model-services-test"); setBusy(button, "model_services.testing");
    const result = $("model-services-test-result"); if (result) result.textContent = t("model_services.testing");
    try { const body = { connection: $("model-service-id").value.trim(), base_url: $("model-service-url").value.trim() }; const key = $("model-service-key").value.trim(); if (key) body.api_key = key; const data = await api("/api/model-services/test", { method: "POST", body }); renderServiceModelOptions(data, body.connection); if (result) result.textContent = data.reachable ? t(data.inference_tested ? "model_services.test_inference" : "model_services.test_reachable", { count: Array.isArray(data.models) ? data.models.length : 0 }) : t("model_services.test_unreachable"); }
    catch (error) { if (result) result.textContent = t(errorKey(error, "model_services.test_failed")); }
    finally { state.modelServicesBusy = false; clearBusy(button); }
  }

  /* ------------------------------------------------------------- assistant */

  function prefillChat(text, runId = "") {
    state.reportRunId = runId;
    $("chat-input").value = text;
    $("chat-followup").hidden = !runId;
    $("chat-followup").textContent = runId ? t("daily.followup_id", { id: runId }) : "";
    window.location.hash = "chat";
    $("chat-input").focus();
    window.setTimeout(() => { if (state.authenticated && state.view === "chat") $("chat-input").focus(); }, 0);
  }

  function chatTaskCard(task) {
    const card = document.createElement("div");
    card.className = "chat-task-card notice";
    const title = document.createElement("strong"); title.textContent = task.title || t("catalog.task_fallback");
    const status = document.createElement("p"); status.textContent = t(task.status === "done" ? "daily.completed" : task.status === "failed" ? "tasks.status.failed" : "daily.accepted");
    const waiting = task.status === "queued" && typeof task.enabled === "boolean" ? taskWaitText(task, state.tasks) : "";
    const detail = document.createElement("p"); detail.className = "hint";
    detail.textContent = task.status === "failed" ? reasonText(task.reason) : waiting;
    const button = document.createElement("button"); button.type = "button"; button.className = "btn ghost small";
    button.dataset.chatTask = task.id || task.task_id || ""; button.textContent = t("daily.inspect");
    card.append(title, status, detail, button); return card;
  }

  function renderChat() {
    const log = $("chat-log");
    if (!log) return;
    while (log.firstChild) log.removeChild(log.firstChild);
    if (!state.chat.length) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = t("chat.empty");
      log.appendChild(empty);
      if (!state.chatTasks.length) return;
    }
    state.chat.forEach((message) => {
      if (!message || (message.role !== "user" && message.role !== "assistant")) return;
      const bubble = document.createElement("div");
      bubble.className = `chat-bubble ${message.role === "user" ? "chat-user" : "chat-assistant"}`;
      const label = document.createElement("p");
      label.className = "chat-role";
      label.textContent = t(message.role === "user" ? "chat.you" : "chat.assistant");
      const body = document.createElement("p");
      body.textContent = message.content || "";
      bubble.appendChild(label);
      bubble.appendChild(body);
      log.appendChild(bubble);
      (Array.isArray(message.tasks) ? message.tasks : []).forEach((task) => log.appendChild(chatTaskCard(task)));
    });
    state.chatTasks.forEach((task) => log.appendChild(chatTaskCard(task)));
    log.scrollTop = log.scrollHeight;
  }

  function appendChatMessage(role, text) {
    if (!text) return;
    state.chat.push({ role, content: text });
    renderChat();
  }

  function appendChatTool(text) {
    const log = $("chat-log");
    if (!log || !text) return;
    const row = document.createElement("p");
    row.className = "chat-tool";
    row.textContent = text;
    log.appendChild(row);
    log.scrollTop = log.scrollHeight;
  }

  async function loadChat() {
    const epoch = state.sessionEpoch;
    const generation = ++state.chatLoadGeneration;
    updateChatAgentHint();
    try {
      const data = await api("/api/agent/sessions/console-main");
      if (!state.authenticated || epoch !== state.sessionEpoch || generation !== state.chatLoadGeneration) return;
      state.chat = Array.isArray(data.messages) ? data.messages : [];
      state.chatTasks = Array.isArray(data.tasks) ? data.tasks : [];
      renderChat();
    } catch (error) {
      if (!state.authenticated || epoch !== state.sessionEpoch || generation !== state.chatLoadGeneration) return;
      if (error.code === "agent_unavailable") {
        setMessage("chat", "chat.unavailable", { error: true });
      } else if (error.code !== "authentication_required") {
        setMessage("chat", "chat.failed", { error: true });
      }
    }
  }

  function handleChatEvent(event) {
    if (!event || typeof event.type !== "string") return;
    if (event.type === "task.created") {
      const task = event.task || event;
      let message = state.chat[state.chat.length - 1];
      if (!message || message.role !== "assistant") { message = { role: "assistant", content: "" }; state.chat.push(message); }
      if (!Array.isArray(message.tasks)) message.tasks = [];
      message.tasks.push(task);
      renderChat(); refreshTasks();
    } else if (event.type === "turn.started") {
      setMessage("chat", "chat.thinking");
    } else if (event.type === "text.delta") {
      if (typeof event.text === "string") appendChatMessage("assistant", event.text);
    } else if (event.type === "tool.call") {
      appendChatTool(t("chat.tool_running", { tool: event.tool || "" }));
    } else if (event.type === "tool.result") {
      appendChatTool(t("chat.tool_done", { tool: event.tool || "", status: event.status || "" }));
    } else if (event.type === "turn.finished") {
      setMessage("chat", event.status === "failed" ? "chat.failed" : null, { error: event.status === "failed" });
    } else if (event.type === "error") {
      setMessage("chat", "chat.failed", { error: true });
    }
  }

  async function sendChat(event) {
    event.preventDefault();
    if (state.chatBusy) return;
    const input = $("chat-input");
    const text = input ? input.value.trim() : "";
    if (!text) return;
    state.chatLoadGeneration += 1;
    appendChatMessage("user", text);
    if (input) input.value = "";
    state.chatBusy = true;
    const button = $("chat-send");
    setBusy(button, "chat.sending");
    setMessage("chat", "chat.thinking");
    const epoch = state.sessionEpoch;
    const controller = new AbortController();
    state.chatController = controller;
    const timeout = window.setTimeout(() => controller.abort(), 180000);
    try {
      const response = await fetch("/api/agent/turns", {
        signal: controller.signal,
        credentials: "same-origin",
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          "X-CSRF-Token": state.csrf,
        },
        body: JSON.stringify({ session: "console-main", text }),
      });
      if (!state.authenticated || epoch !== state.sessionEpoch) return;
      if (response.status === 401) { expireSession(); return; }
      if (!response.ok) {
        let code = "chat.failed";
        try {
          const body = await response.json();
          if (body && (body.error === "agent_unavailable" || body.error === "agent_route_disabled")) code = "chat.unavailable";
        } catch (error) {
          // The status is enough; do not surface a raw response.
        }
        setMessage("chat", code, { error: true });
        return;
      }
      if (!response.body) {
        setMessage("chat", "chat.failed", { error: true });
        return;
      }
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        if (!state.authenticated || epoch !== state.sessionEpoch) { await reader.cancel(); break; }
        buffer += decoder.decode(value, { stream: true });
        if (buffer.length > 262144) { await reader.cancel(); throw new Error("stream_limit"); }
        let boundary = buffer.indexOf("\n\n");
        while (boundary >= 0) {
          const chunk = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          const line = chunk.split("\n").find((item) => item.startsWith("data: "));
          if (line) {
            try {
              handleChatEvent(JSON.parse(line.slice(6)));
            } catch (error) {
              // Ignore one malformed event; the stream may continue.
            }
          }
          boundary = buffer.indexOf("\n\n");
        }
      }
    } catch (error) {
      if (state.authenticated && epoch === state.sessionEpoch) setMessage("chat", "chat.failed", { error: true });
    } finally {
      window.clearTimeout(timeout);
      state.chatController = null;
      state.chatBusy = false;
      clearBusy(button);
    }
  }

  /* --------------------------------------------------------------- feedback */

  const FEEDBACK_IMAGE_MAX = 8 * 1024 * 1024;
  const FEEDBACK_AREAS = ["dashboard", "assistant", "actions", "runs", "review", "settings", "feedback", "other"];
  const FEEDBACK_REASONS = {
    title_short: "feedback.reason.title_short",
    area_invalid: "feedback.reason.area_invalid",
    current_short: "feedback.reason.current_short",
    expected_short: "feedback.reason.expected_short",
    acceptance_short: "feedback.reason.acceptance_short",
    image_invalid: "feedback.reason.image_invalid",
    too_large: "feedback.reason.too_large",
    too_long: "feedback.reason.too_long",
    too_broad: "feedback.reason.too_broad",
    ambiguous: "feedback.reason.ambiguous",
    not_actionable: "feedback.reason.not_actionable",
    unsafe_scope: "feedback.reason.unsafe_scope",
    gate_unavailable: "feedback.reason.gate_unavailable",
    priority_invalid: "feedback.reason.priority_invalid",
    invalid: "feedback.reason.invalid",
    title_long: "feedback.reason.title_long",
  };
  const FEEDBACK_TOOL_IDS = { pen: "feedback-tool-pen", arrow: "feedback-tool-arrow", rect: "feedback-tool-rect" };

  function feedbackReasonText(code) {
    if (typeof code !== "string" || !code) return t("feedback.reason.unknown");
    const key = FEEDBACK_REASONS[code];
    return key ? translateKnown(key, code) : code;
  }

  function renderFeedbackNotice() {
    const element = $("feedback-message");
    if (!element) return;
    const notice = state.feedbackNotice;
    if (!notice) {
      element.textContent = "";
      element.dataset.error = "false";
      return;
    }
    if (Object.prototype.hasOwnProperty.call(notice, "reason")) {
      element.textContent = t("feedback.rejected", { reason: feedbackReasonText(notice.reason) });
    } else {
      element.textContent = t(notice.key, notice.vars);
    }
    element.dataset.error = notice.error ? "true" : "false";
  }

  function setFeedbackNotice(notice) {
    state.feedbackNotice = notice || null;
    renderFeedbackNotice();
  }

  function renderFeedbackImageState() {
    const element = $("feedback-image-state");
    if (!element) return;
    const name = state.feedback.imageName;
    element.textContent = name ? t("feedback.image_selected", { name }) : t("feedback.image_none");
  }

  function renderFeedbackImageError() {
    const element = $("feedback-image-error");
    if (!element) return;
    const key = state.feedback.imageError;
    element.textContent = key ? t(key) : "";
    element.dataset.error = key ? "true" : "false";
  }

  function setFeedbackImageError(key) {
    state.feedback.imageError = key || "";
    renderFeedbackImageError();
  }

  function clearFeedbackCanvas() {
    const canvas = $("feedback-canvas");
    if (!canvas) return;
    try {
      const context = canvas.getContext("2d");
      if (context) {
        context.setTransform(1, 0, 0, 1, 0, 0);
        context.clearRect(0, 0, canvas.width, canvas.height);
      }
    } catch (_) {
      // A canvas can be unavailable in an old engine; the rest of the form still works.
    }
    canvas.width = 1;
    canvas.height = 1;
    canvas.style.removeProperty("width");
    canvas.style.removeProperty("height");
    const editor = $("feedback-editor");
    if (editor) editor.hidden = true;
  }

  function clearFeedbackImage() {
    const feedback = state.feedback;
    feedback.loadGeneration += 1;
    feedback.image = null;
    feedback.imageName = "";
    feedback.dataURL = "";
    feedback.cssWidth = 0;
    feedback.cssHeight = 0;
    feedback.dpr = 1;
    feedback.strokes = [];
    feedback.current = null;
    feedback.drawing = false;
    feedback.pointerId = null;
    feedback.imageError = "";
    clearFeedbackCanvas();
    const input = $("feedback-image");
    if (input) input.value = "";
    renderFeedbackImageState();
    renderFeedbackImageError();
  }

  function resetFeedbackFields() {
    ["feedback-title", "feedback-current", "feedback-expected", "feedback-acceptance"].forEach((id) => {
      const node = $(id);
      if (node) node.value = "";
    });
    if ($("feedback-area")) $("feedback-area").value = "dashboard";
    if ($("feedback-priority")) $("feedback-priority").value = "P2";
    setFeedbackImageError("");
  }

  function resetFeedbackDraft() {
    resetFeedbackFields();
    clearFeedbackImage();
  }

  function resetFeedbackSession() {
    state.feedbackBusy = false;
    state.feedbackNotice = null;
    resetFeedbackDraft();
    const created = $("feedback-created");
    if (created) created.replaceChildren();
    if ($("feedback-color")) $("feedback-color").value = "#d64545";
    if ($("feedback-width")) $("feedback-width").value = "3";
    if ($("feedback-width-value")) $("feedback-width-value").textContent = "3";
    state.feedback.tool = "pen";
    state.feedback.color = "#d64545";
    state.feedback.width = 3;
    feedbackSetTool("pen");
    renderFeedbackNotice();
  }

  function feedbackSetTool(tool) {
    if (!Object.prototype.hasOwnProperty.call(FEEDBACK_TOOL_IDS, tool)) return;
    state.feedback.tool = tool;
    Object.entries(FEEDBACK_TOOL_IDS).forEach(([name, id]) => {
      const button = $(id);
      if (button) button.setAttribute("aria-pressed", name === tool ? "true" : "false");
    });
  }

  function feedbackDrawStroke(context, stroke) {
    const points = Array.isArray(stroke.points) ? stroke.points : [];
    if (!points.length) return;
    const color = /^#[0-9a-f]{6}$/i.test(stroke.color) ? stroke.color : "#d64545";
    const width = Math.max(1, Math.min(12, Number(stroke.width) || 3));
    context.save();
    context.strokeStyle = color;
    context.fillStyle = color;
    context.lineWidth = width;
    context.lineCap = "round";
    context.lineJoin = "round";
    if (stroke.tool === "rect" && points.length > 1) {
      const start = points[0];
      const end = points[points.length - 1];
      context.strokeRect(start.x, start.y, end.x - start.x, end.y - start.y);
    } else if (stroke.tool === "arrow" && points.length > 1) {
      const start = points[0];
      const end = points[points.length - 1];
      context.beginPath();
      context.moveTo(start.x, start.y);
      context.lineTo(end.x, end.y);
      context.stroke();
      const angle = Math.atan2(end.y - start.y, end.x - start.x);
      const head = Math.max(8, width * 3.5);
      context.beginPath();
      context.moveTo(end.x, end.y);
      context.lineTo(end.x - head * Math.cos(angle - Math.PI / 6), end.y - head * Math.sin(angle - Math.PI / 6));
      context.moveTo(end.x, end.y);
      context.lineTo(end.x - head * Math.cos(angle + Math.PI / 6), end.y - head * Math.sin(angle + Math.PI / 6));
      context.stroke();
    } else {
      context.beginPath();
      context.moveTo(points[0].x, points[0].y);
      for (let index = 1; index < points.length; index += 1) context.lineTo(points[index].x, points[index].y);
      if (points.length === 1) context.lineTo(points[0].x + 0.01, points[0].y + 0.01);
      context.stroke();
    }
    context.restore();
  }

  function redrawFeedback() {
    const canvas = $("feedback-canvas");
    const feedback = state.feedback;
    if (!canvas || !feedback.image || !feedback.cssWidth || !feedback.cssHeight) return;
    let context;
    try {
      context = canvas.getContext("2d");
    } catch (_) {
      context = null;
    }
    if (!context) return;
    context.setTransform(1, 0, 0, 1, 0, 0);
    context.clearRect(0, 0, canvas.width, canvas.height);
    context.setTransform(feedback.dpr, 0, 0, feedback.dpr, 0, 0);
    context.drawImage(feedback.image, 0, 0, feedback.cssWidth, feedback.cssHeight);
    feedback.strokes.forEach((stroke) => feedbackDrawStroke(context, stroke));
    if (feedback.current) feedbackDrawStroke(context, feedback.current);
  }

  function fitFeedbackCanvas(image) {
    const editor = $("feedback-editor");
    if (editor) editor.hidden = false;
    const wrap = $("feedback-canvas-wrap");
    const naturalWidth = image.naturalWidth || image.width || 1;
    const naturalHeight = image.naturalHeight || image.height || 1;
    const available = Math.max(1, Math.min(760, (wrap && wrap.clientWidth) || 760));
    const scale = Math.min(available / naturalWidth, 560 / naturalHeight, 1);
    const cssWidth = Math.max(1, Math.round(naturalWidth * scale));
    const cssHeight = Math.max(1, Math.round(naturalHeight * scale));
    const dpr = Math.max(1, window.devicePixelRatio || 1);
    const canvas = $("feedback-canvas");
    if (!canvas) return;
    canvas.width = Math.max(1, Math.round(cssWidth * dpr));
    canvas.height = Math.max(1, Math.round(cssHeight * dpr));
    canvas.style.width = `${cssWidth}px`;
    canvas.style.height = `${cssHeight}px`;
    state.feedback.cssWidth = cssWidth;
    state.feedback.cssHeight = cssHeight;
    state.feedback.dpr = dpr;
    state.feedback.strokes = [];
    state.feedback.current = null;
    redrawFeedback();
  }

  function feedbackSelectFile(file) {
    if (!file) return;
    const type = typeof file.type === "string" ? file.type : "";
    if (type !== "image/png" && type !== "image/jpeg") {
      clearFeedbackImage();
      setFeedbackImageError("feedback.image_invalid");
      return;
    }
    if (Number(file.size) > FEEDBACK_IMAGE_MAX) {
      clearFeedbackImage();
      setFeedbackImageError("feedback.image_too_large");
      return;
    }
    clearFeedbackImage();
    setFeedbackImageError("");
    const feedback = state.feedback;
    const generation = ++feedback.loadGeneration;
    const reader = new FileReader();
    reader.onload = () => {
      if (generation !== feedback.loadGeneration || !state.authenticated) return;
      const dataURL = typeof reader.result === "string" ? reader.result : "";
      if (!/^data:image\/(png|jpeg);base64,/.test(dataURL)) {
        setFeedbackImageError("feedback.image_invalid");
        return;
      }
      feedback.dataURL = dataURL;
      feedback.imageName = typeof file.name === "string" && file.name ? file.name : "feedback.png";
      const image = new Image();
      image.onload = () => {
        if (generation !== feedback.loadGeneration || !state.authenticated) return;
        const width = image.naturalWidth || 0;
        const height = image.naturalHeight || 0;
        if (width < 64 || height < 64 || width > 4096 || height > 4096 || width * height > 8_000_000) {
          clearFeedbackImage();
          setFeedbackImageError("feedback.image_dimensions");
          return;
        }
        feedback.image = image;
        fitFeedbackCanvas(image);
        renderFeedbackImageState();
      };
      image.onerror = () => {
        if (generation !== feedback.loadGeneration) return;
        feedback.image = null;
        feedback.imageName = "";
        setFeedbackImageError("feedback.image_invalid");
        renderFeedbackImageState();
      };
      image.src = dataURL;
    };
    reader.onerror = () => {
      if (generation !== feedback.loadGeneration) return;
      setFeedbackImageError("feedback.image_invalid");
    };
    try {
      reader.readAsDataURL(file);
    } catch (_) {
      setFeedbackImageError("feedback.image_invalid");
    }
  }

  function feedbackPoint(event) {
    const canvas = $("feedback-canvas");
    const feedback = state.feedback;
    if (!canvas || !feedback.cssWidth || !feedback.cssHeight) return null;
    const rect = canvas.getBoundingClientRect();
    if (!rect.width || !rect.height) return null;
    const x = (event.clientX - rect.left) * (feedback.cssWidth / rect.width);
    const y = (event.clientY - rect.top) * (feedback.cssHeight / rect.height);
    return {
      x: Math.max(0, Math.min(feedback.cssWidth, x)),
      y: Math.max(0, Math.min(feedback.cssHeight, y)),
    };
  }

  function startFeedbackStroke(event) {
    const feedback = state.feedback;
    if (!feedback.image || feedback.drawing || event.isPrimary === false) return;
    if (typeof event.button === "number" && event.button !== 0) return;
    const point = feedbackPoint(event);
    if (!point) return;
    event.preventDefault();
    feedback.drawing = true;
    feedback.pointerId = event.pointerId;
    feedback.current = {
      tool: feedback.tool,
      color: feedback.color,
      width: feedback.width,
      points: [point],
    };
    const canvas = $("feedback-canvas");
    if (canvas && typeof canvas.setPointerCapture === "function") {
      try {
        canvas.setPointerCapture(event.pointerId);
      } catch (_) {
        // Pointer capture is an enhancement; pointer move still works in most engines.
      }
    }
    redrawFeedback();
  }

  function moveFeedbackStroke(event) {
    const feedback = state.feedback;
    if (!feedback.drawing || !feedback.current) return;
    if (feedback.pointerId !== null && event.pointerId !== feedback.pointerId) return;
    const point = feedbackPoint(event);
    if (!point) return;
    event.preventDefault();
    if (feedback.current.tool === "pen") feedback.current.points.push(point);
    else feedback.current.points[1] = point;
    redrawFeedback();
  }

  function endFeedbackStroke(event) {
    const feedback = state.feedback;
    if (!feedback.drawing) return;
    if (feedback.pointerId !== null && event.pointerId !== undefined && event.pointerId !== feedback.pointerId) return;
    if (feedback.current) feedback.strokes.push(feedback.current);
    feedback.current = null;
    feedback.drawing = false;
    const canvas = $("feedback-canvas");
    if (canvas && feedback.pointerId !== null && typeof canvas.releasePointerCapture === "function") {
      try {
        canvas.releasePointerCapture(feedback.pointerId);
      } catch (_) {
        // The pointer may already have been released by the browser.
      }
    }
    feedback.pointerId = null;
    redrawFeedback();
  }

  function feedbackExportBlob() {
    return new Promise((resolve, reject) => {
      const feedback = state.feedback;
      const image = feedback.image;
      if (!image) {
        resolve(null);
        return;
      }
      const width = image.naturalWidth || image.width || 0;
      const height = image.naturalHeight || image.height || 0;
      if (!width || !height || !feedback.cssWidth || !feedback.cssHeight) {
        reject(new Error("unavailable"));
        return;
      }
      // Export the original image dimensions, not the fitted preview, so text
      // and annotations keep their detail. Strokes are stored in preview
      // coordinates and scale uniformly with the same aspect ratio.
      const output = document.createElement("canvas");
      output.width = width;
      output.height = height;
      const context = output.getContext("2d");
      if (!context || typeof output.toBlob !== "function") {
        reject(new Error("unsupported"));
        return;
      }
      const scaleX = width / feedback.cssWidth;
      const scaleY = height / feedback.cssHeight;
      try {
        context.setTransform(scaleX, 0, 0, scaleY, 0, 0);
        context.drawImage(image, 0, 0, feedback.cssWidth, feedback.cssHeight);
        feedback.strokes.forEach((stroke) => feedbackDrawStroke(context, stroke));
        output.toBlob((blob) => {
          if (blob) resolve(blob);
          else reject(new Error("empty"));
        }, "image/png");
      } catch (_) {
        reject(new Error("failed"));
      }
    });
  }

  async function submitFeedback(event) {
    event.preventDefault();
    if (state.feedbackBusy) return;
    const created = $("feedback-created");
    if (created) created.replaceChildren();
    const titleNode = $("feedback-title");
    const areaNode = $("feedback-area");
    const currentNode = $("feedback-current");
    const expectedNode = $("feedback-expected");
    const acceptanceNode = $("feedback-acceptance");
    const priorityNode = $("feedback-priority");
    const title = titleNode ? titleNode.value.trim() : "";
    const current = currentNode ? currentNode.value.trim() : "";
    const expected = expectedNode ? expectedNode.value.trim() : "";
    const acceptance = acceptanceNode ? acceptanceNode.value.trim() : "";
    const area = areaNode ? areaNode.value : "";
    const priority = priorityNode ? priorityNode.value : "";
    const required = [
      [titleNode, title, 120],
      [currentNode, current, 1000],
      [expectedNode, expected, 1000],
      [acceptanceNode, acceptance, 1000],
    ];
    for (const [node, value, limit] of required) {
      if (!value) {
        setFeedbackNotice({ key: "feedback.required", error: true });
        if (node) node.focus();
        return;
      }
      if (Array.from(value).length > limit) {
        setFeedbackNotice({ key: "feedback.too_long", error: true });
        if (node) node.focus();
        return;
      }
    }
    if (!FEEDBACK_AREAS.includes(area) || (priority !== "P1" && priority !== "P2")) {
      setFeedbackNotice({ key: "feedback.invalid", error: true });
      return;
    }
    let imageBlob = null;
    if (state.feedback.image) {
      try {
        imageBlob = await feedbackExportBlob();
      } catch (_) {
        setFeedbackImageError("feedback.export_failed");
        return;
      }
      if (!imageBlob || imageBlob.type !== "image/png") {
        setFeedbackImageError("feedback.export_failed");
        return;
      }
      if (imageBlob.size > FEEDBACK_IMAGE_MAX) {
        setFeedbackImageError("feedback.image_too_large");
        return;
      }
    }
    if (!state.authenticated) return;
    const epoch = state.sessionEpoch;
    state.feedbackBusy = true;
    setFeedbackImageError("");
    const button = $("feedback-submit");
    setBusy(button, "feedback.submitting");
    setFeedbackNotice(null);
    const data = new FormData();
    data.append("title", title);
    data.append("area", area);
    data.append("current", current);
    data.append("expected", expected);
    data.append("acceptance", acceptance);
    data.append("priority", priority);
    if (imageBlob) data.append("image", imageBlob, "feedback.png");
    try {
      const result = await api("/api/feedback", { method: "POST", formData: data });
      if (!state.authenticated || epoch !== state.sessionEpoch) return;
      if (result && result.accepted === false) {
        const reason = result.feedback && typeof result.feedback.reason === "string" ? result.feedback.reason : "";
        setFeedbackNotice({ reason, error: true });
        return;
      }
      setFeedbackNotice({ key: "feedback.accepted" });
      const task = result && (result.task || (result.feedback && result.feedback.task));
      const resultBox = $("feedback-created");
      if (resultBox && task && typeof task === "object") resultBox.replaceChildren(chatTaskCard(task));
      resetFeedbackDraft();
      await refreshTasks();
    } catch (error) {
      if (state.authenticated && epoch === state.sessionEpoch) {
        const detail = error && (error.payload || error.data) && typeof (error.payload || error.data) === "object" ? (error.payload || error.data) : {};
        const reason = typeof detail.reason === "string" ? detail.reason : "";
        const code = reason || (error && typeof error.code === "string" ? error.code : "");
        if (FEEDBACK_REASONS[code]) setFeedbackNotice({ reason: code, error: true });
        else setFeedbackNotice({ key: errorKey(error, "feedback.failed"), error: true });
      }
    } finally {
      state.feedbackBusy = false;
      clearBusy(button);
    }
  }

  function feedbackChooseImage() {
    const input = $("feedback-image");
    if (input) input.click();
  }

  function feedbackFileInputChanged(event) {
    const file = event.target && event.target.files ? event.target.files[0] : null;
    feedbackSelectFile(file);
  }

  function feedbackDrop(event) {
    event.preventDefault();
    const zone = $("feedback-dropzone");
    if (zone) zone.dataset.active = "false";
    const files = event.dataTransfer && event.dataTransfer.files;
    if (files && files.length) feedbackSelectFile(files[0]);
  }

  function feedbackPaste(event) {
    if (!state.authenticated || state.view !== "feedback") return;
    const items = event.clipboardData && event.clipboardData.items ? Array.from(event.clipboardData.items) : [];
    const image = items.find((item) => item && (item.type === "image/png" || item.type === "image/jpeg"));
    if (!image) return;
    const file = image.getAsFile();
    if (!file) return;
    event.preventDefault();
    feedbackSelectFile(file);
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
      case "reported":
        return "automation.status.reported";
      case "completed":
        return "automation.status.completed";
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

  function renderLatestRun(run, containerId = "latest-run") {
    const container = $(containerId);
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
      const details = document.createElement("details");
      const title = document.createElement("summary");
      title.textContent = t("run.model_summary");
      details.append(title, summary);
      container.appendChild(details);
    }
    if (["pushed", "no_changes"].includes(run.status)) {
      const outcome = document.createElement("p");
      outcome.className = "run-summary";
      outcome.textContent = t("run.outcome", { count: Array.isArray(run.edits) ? run.edits.length : 0 });
      container.appendChild(outcome);
    }
    if (run.status === "reported" || typeof run.report_text === "string" || run.report_file || run.report_path) {
      const heading = document.createElement("h3"); heading.textContent = t("automation.report_title");
      const report = document.createElement("pre"); report.className = "run-report"; report.dataset.reportRun = run.run_id;
      const saved = state.reportResults.get(run.run_id);
      report.textContent = saved && !saved.loading ? saved.text || t("daily.result_empty") : t("automation.loading");
      const provenance = document.createElement("p"); provenance.className = "hint report-provenance";
      provenance.dataset.reportNotice = run.run_id;
      provenance.hidden = !(saved && (saved.historical_snapshot || saved.truncated));
      provenance.textContent = t("daily.result_partial");
      container.append(heading, provenance, report);
      const follow = document.createElement("button"); follow.type = "button"; follow.className = "btn ghost";
      follow.textContent = t("daily.followup"); follow.dataset.reportFollowup = run.run_id;
      container.appendChild(follow);
      if (!saved) {
        const epoch = state.sessionEpoch;
        state.reportResults.set(run.run_id, { loading: true });
        api(`/api/automation/runs/${encodeURIComponent(run.run_id)}/result`).then((result) => {
          if (!state.authenticated || epoch !== state.sessionEpoch) return;
          const text = typeof result.text === "string" ? result.text : "";
          state.reportResults.set(run.run_id, { text, historical_snapshot: result.historical_snapshot === true, truncated: result.truncated === true });
          document.querySelectorAll("[data-report-notice]").forEach((node) => { if (node.dataset.reportNotice === run.run_id) node.hidden = !(result.historical_snapshot || result.truncated); });
          document.querySelectorAll("[data-report-run]").forEach((node) => { if (node.dataset.reportRun === run.run_id) node.textContent = text || t("daily.result_empty"); });
        }).catch(() => {
          if (!state.authenticated || epoch !== state.sessionEpoch) return;
          state.reportResults.delete(run.run_id);
          document.querySelectorAll("[data-report-run]").forEach((node) => { if (node.dataset.reportRun === run.run_id) node.textContent = t("daily.result_failed") + (typeof run.report_text === "string" && run.report_text ? "\n\n" + t("daily.preview") + "\n" + run.report_text : ""); });
        });
      }
    }
    if (Array.isArray(run.needs_review) && run.needs_review.length) {
      const heading = document.createElement("h3");
      heading.textContent = t("automation.needs_review");
      container.appendChild(heading);
      const guidance = document.createElement("a");
      guidance.href = "#review";
      guidance.textContent = t("review.open_guide");
      container.appendChild(guidance);
      container.appendChild(findingCards(run));
    }
    if (Number.isInteger(run.remaining) && run.remaining > 0) {
      const pending = document.createElement("p");
      pending.className = "notice";
      pending.textContent = t(run.continuation_queued === true ? "review.continuation_queued" : "review.continuation_unqueued", { count: run.remaining });
      container.appendChild(pending);
      const board = document.createElement("a");
      board.href = "#dashboard";
      board.textContent = t("review.queue_link");
      container.appendChild(board);
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
    addRunMeta(meta, t("board.run_tokens"), run.usage && Number.isFinite(run.usage.total_tokens)
      ? usageNumber(run.usage.total_tokens) : t("board.no_usage"));
    addRunMeta(meta, t("board.run_cost"), run.cost ? costText(run.cost) : "—");
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
    else if (state.selectedRun && state.selectedRun.run_id === state.selectedRunId) renderLatestRun(state.selectedRun);
    renderRunHistory(state.automation ? state.automation.runs : []);
    renderRaw("automation-output", { service: service, automation: state.automation });
    renderReview();
    renderTasks(state.tasks);
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
      if (state.selectedRunId !== runID) return;
      state.selectedRun = detail;
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
  on("setup-form", "submit", setup);
  window.addEventListener("hashchange", () => navigate(true));
  on("review-refresh", "click", async () => {
    const button = $("review-refresh");
    setBusy(button, "common.refreshing");
    try { await refreshAll(); } finally { clearBusy(button); }
  });
  on("review-recheck", "click", recheckReview);
  on("task-search", "input", () => renderTasks(state.tasks));
  on("task-priority-filter", "change", () => renderTasks(state.tasks));
  on("task-detail-close", "click", () => $("task-detail").close());
  on("task-detail", "close", () => {
    if ($("task-detail").open) return;
    inspectGeneration += 1;
    inspectedRun = null;
    inspectedRunId = "";
    if (state.authenticated && state.view === "dashboard") {
      const opener = Array.from($("tasks-list").querySelectorAll("[data-task-inspect]")).find((button) => button.dataset.taskInspect === inspectedTask);
      (opener || $("tasks-list")).focus({ preventScroll: true });
    }
  });
  on("tasks-list", "click", (event) => {
    const target = event.target instanceof Element ? event.target.closest("button[data-task-inspect]") : null;
    if (target) inspectTask(target.dataset.taskInspect);
  });
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
  on("runner-select", "change", () => {
    updateRunnerUi(state.lastState);
    renderSetup();
  });
  on("model-input", "click", () => {
    if (!state.modelMenuOpen) openModelMenu();
  });
  on("model-input", "input", () => {
    state.modelValues[activeRunner()] = $("model-input").value;
    if (state.modelMenuOpen) renderModelMenu();
    renderSetup();
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

  on("model-service-select", "change", (event) => selectModelConnection(event.target.value));
  on("model-services-form", "submit", saveModelServices);
  on("model-services-test", "click", testModelService);
  on("model-override-add", "click", addModelOverride);
  on("model-overrides-list", "click", (event) => { if (event.target.closest(".model-override-remove")) event.target.closest(".model-override-row").remove(); });
  on("work-allowance-open", "click", () => { $("work-usage-details").open = true; $("work-usage-details").querySelector("summary").focus(); });
  on("usage-window", "change", (event) => {
    const next = Number.parseInt(event.target.value, 10);
    state.usageWindow = Number.isFinite(next) && next > 0 ? next : 30;
    refreshUsage();
  });

  on("quotas-refresh", "click", () => refreshQuotas({ refresh: true }));

  on("hook-form", "submit", createHook);
  ["latest-run", "task-detail-body"].forEach((id) => on(id, "click", (event) => {
    const button = event.target instanceof Element ? event.target.closest("button[data-report-followup]") : null;
    if (!button) return;
    if ($("task-detail").open) $("task-detail").close();
    prefillChat(t("daily.followup_prompt", { id: button.dataset.reportFollowup }), button.dataset.reportFollowup);
  }));
  on("chat-form", "submit", sendChat);
  on("catalog-list", "submit", runCatalogAction);
  const inspectChatTask = async (event) => {
    const button = event.target instanceof Element ? event.target.closest("button[data-chat-task]") : null;
    if (!button || !button.dataset.chatTask) return;
    await refreshTasks();
    if (state.authenticated) inspectTask(button.dataset.chatTask);
  };
  on("chat-log", "click", inspectChatTask);
  on("catalog-list", "click", inspectChatTask);
  on("feedback-created", "click", inspectChatTask);
  on("feedback-form", "submit", submitFeedback);
  on("feedback-image", "change", feedbackFileInputChanged);
  on("feedback-choose-image", "click", feedbackChooseImage);
  on("feedback-dropzone", "click", (event) => {
    const target = event.target instanceof Element ? event.target.closest("button, input") : null;
    if (!target) feedbackChooseImage();
  });
  on("feedback-dropzone", "keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    feedbackChooseImage();
  });
  on("feedback-dropzone", "dragover", (event) => {
    event.preventDefault();
    $("feedback-dropzone").dataset.active = "true";
  });
  on("feedback-dropzone", "dragenter", (event) => {
    event.preventDefault();
    $("feedback-dropzone").dataset.active = "true";
  });
  on("feedback-dropzone", "dragleave", (event) => {
    if (event.target === event.currentTarget) $("feedback-dropzone").dataset.active = "false";
  });
  on("feedback-dropzone", "drop", feedbackDrop);
  on("feedback-tool-pen", "click", () => feedbackSetTool("pen"));
  on("feedback-tool-arrow", "click", () => feedbackSetTool("arrow"));
  on("feedback-tool-rect", "click", () => feedbackSetTool("rect"));
  on("feedback-color", "input", (event) => {
    state.feedback.color = /^#[0-9a-f]{6}$/i.test(event.target.value) ? event.target.value : "#d64545";
  });
  on("feedback-width", "input", (event) => {
    const value = Math.max(1, Math.min(12, Number.parseInt(event.target.value, 10) || 3));
    state.feedback.width = value;
    const output = $("feedback-width-value");
    if (output) output.textContent = String(value);
  });
  on("feedback-undo", "click", () => {
    state.feedback.strokes.pop();
    redrawFeedback();
  });
  on("feedback-clear", "click", () => {
    state.feedback.strokes = [];
    state.feedback.current = null;
    state.feedback.drawing = false;
    state.feedback.pointerId = null;
    redrawFeedback();
  });
  on("feedback-remove-image", "click", () => {
    clearFeedbackImage();
    const choose = $("feedback-choose-image");
    if (choose) choose.focus();
  });
  on("feedback-canvas", "pointerdown", startFeedbackStroke);
  on("feedback-canvas", "pointermove", moveFeedbackStroke);
  on("feedback-canvas", "pointerup", endFeedbackStroke);
  on("feedback-canvas", "pointercancel", endFeedbackStroke);
  document.addEventListener("paste", feedbackPaste);
  on("agent-kind", "change", agentKindChanged);
  on("agent-provider", "change", agentProviderChanged);
  on("agent-form", "submit", saveAgentConfig);
  on("actions-refresh", "click", loadActions);
  on("action-validate", "click", validateAction);
  on("action-save", "click", saveAction);
  on("action-disable", "click", disableAction);
  on("action-hosts-save", "click", saveActionHosts);
  on("actions-list", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-action-id]") : null;
    if (button) selectAction(button.dataset.actionId);
  });
  on("action-versions", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-action-version]") : null;
    if (button) restoreActionVersion(button.dataset.actionVersion);
  });
  on("hook-secret-copy", "click", copyHookSecret);
  on("hooks-list", "click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-hook-action]") : null;
    if (button) hookAction(button);
  });

  on("weixin-connect", "click", startWeixinLogin);
  on("weixin-cancel", "click", cancelWeixinLogin);
  on("weixin-code-form", "submit", submitWeixinCode);
  on("weixin-confirm-button", "click", confirmWeixinOwner);
  on("weixin-toggle", "click", toggleWeixin);
  on("weixin-push-toggle", "change", toggleWeixinPush);
  on("weixin-push-test", "click", testWeixinPush);

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
    if (!state.authenticated) showAuthMode();
    renderMessages();
    setConnection(state.authenticated);
    if (state.lastState) renderState(state.lastState);
    if (state.modelMenuOpen) renderModelMenu();
    renderChannelTexts(state.channels);
    renderHooks({ hooks: state.hooks });
    renderUsage(state.usage);
    renderQuotas(state.quotas);
    if (state.weixin) renderWeixin(state.weixin);
    renderTasks(state.tasks);
    if ($("task-detail").open) {
      if (inspectedRun) renderLatestRun(inspectedRun, "task-detail-body");
      else inspectTask(inspectedTask);
    }
    renderChat();
    renderCatalog();
    renderFeedbackImageState();
    renderFeedbackImageError();
    renderFeedbackNotice();
    rerenderBusyLabels();
  });

  /* ------------------------------------------------------------------ boot */

  async function boot() {
    try {
      const session = await api("/api/session");
      state.setupRequired = Boolean(session.setup_required);
      if (session.authenticated) {
        state.authenticated = true;
    state.sessionEpoch += 1;
        state.csrf = typeof session.csrf_token === "string" ? session.csrf_token : "";
        showApp();
        await refreshAll();
        await enterSession();
      } else {
        showLogin();
        showAuthMode();
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
    if ($("task-detail").open) $("task-detail").close();
    state.viewInitialized = false;
    state.sessionEpoch += 1;
    state.chatLoadGeneration += 1;
    if (state.chatController) state.chatController.abort();
    state.chat = []; state.chatTasks = []; state.chatBusy = false; state.reportRunId = ""; state.reportResults.clear();
    $("chat-log").replaceChildren(); $("chat-input").value = "";
    $("chat-followup").hidden = true;
    $("task-detail-body").replaceChildren();
    $("runs-list").replaceChildren(); $("tasks-list").replaceChildren();
    setMessage("chat", null);
    state.catalog = [];
    state.catalogLoaded = false;
    state.catalogError = false;
    state.catalogBusy = Object.create(null);
    state.catalogResults.clear();
    state.catalogNotices.clear();
    $("catalog-list").replaceChildren();
    $("catalog-hint").hidden = true;
    resetFeedbackSession();
    state.lastState = null;
    state.selectedRun = null;
    state.selectedRunId = "";
    state.usage = null;
    state.tasks = null;
    state.channels = null;
    state.agent = null;
    state.weixin = null;
    stopWeixinPoll();
    if ($("weixin-section")) $("weixin-section").hidden = true;
    $("review-list").replaceChildren();
    $("latest-run").replaceChildren();
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
