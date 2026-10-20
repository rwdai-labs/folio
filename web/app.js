(() => {
  "use strict";

  /* --------------------------------------------------------------- helpers */

  const $ = (id) => document.getElementById(id);
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
    startingGithub: false,
    pollingGithub: false,
    loggingOutGithub: false,
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
    modelValues: { codex: "", dsh: "" },
    modelMenuOpen: false,
    modelActive: -1,
    modelCatalogSignature: "",
    selectedRunId: "",
    lastState: null,
    githubStatus: "unknown",
    githubEpoch: 0,
  };

  const messageRegions = {
    login: { element: "login-message", data: null },
    github: { element: "github-message", data: null },
    repository: { element: "repository-message", data: null },
    automation: { element: "automation-message", data: null },
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
      case "invalid_repository":
        return "repo.invalid";
      case "invalid_change":
      case "invalid_request":
        return "automation.invalid";
      case "invalid_model":
        return "error.invalid";
      case "invalid_runner":
        return "runner.unavailable";
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
    state.githubEpoch += 1;
    stopAllTimers();
    state.device = null;
    state.automation = null;
    state.automationRuns = [];
    state.automationError = false;
    state.modelCatalogSignature = "";
    state.modelValues = { codex: "", dsh: "" };
    if ($("model-input")) $("model-input").value = "";
    closeModelMenu();
    showLogin();
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
      state.githubEpoch += 1;
      state.automation = null;
      state.automationRuns = [];
      state.automationError = false;
      state.modelCatalogSignature = "";
      state.modelValues = { codex: "", dsh: "" };
      if ($("model-input")) $("model-input").value = "";
      closeModelMenu();
      stopAllTimers();
      state.device = null;
      showLogin();
      setMessage("login", null);
      state.loggingOut = false;
      clearBusy(button);
    }
  }

  /* ---------------------------------------------------------- github flow */

  function githubKey(status) {
    switch (status) {
      case "authenticated":
        return "github.connected";
      case "pending":
        return "github.pending";
      case "signed_out":
        return "github.signed_out";
      case "error":
        return "github.error";
      default:
        return "github.unknown";
    }
  }

  function renderGithubBadge(status) {
    const badge = $("github-badge");
    if (!badge) return;
    badge.dataset.state = status || "unknown";
    badge.textContent = t(githubKey(status));
  }

  function syncGithubSnapshot(status) {
    if (state.lastState) {
      const previous = state.lastState.github || {};
      state.lastState.github = { ...previous, status, authenticated: status === "authenticated" };
    }
    const fact = $("fact-github");
    if (fact) fact.textContent = t(githubKey(status));
  }

  function updateGithubButtons() {
    const authenticated = state.githubStatus === "authenticated";
    $("github-start").hidden = authenticated;
    $("github-poll").hidden = authenticated;
    $("github-logout").hidden = !authenticated;

    if (!authenticated) {
      const label = $("github-start").querySelector(".btn-label");
      if (label) label.textContent = t(state.githubStatus === "pending" ? "github.restart" : "github.start");
      $("github-poll").disabled = !state.device || state.device.expired || state.pollingGithub;
    }
  }

  function formatRemaining(seconds) {
    const safe = Math.max(0, Math.ceil(seconds));
    const minutes = Math.floor(safe / 60);
    const rest = safe % 60;
    return `${minutes}:${rest < 10 ? "0" : ""}${rest}`;
  }

  function updateExpiryText() {
    if (!state.device) return;
    const expiry = $("github-expiry");
    if (!expiry) return;
    if (state.device.expired) {
      expiry.textContent = t("github.expired");
      return;
    }
    const remaining = (state.device.deadline - Date.now()) / 1000;
    expiry.textContent = t("github.expires", { time: formatRemaining(remaining) });
  }

  function startCountdown() {
    clearTimer("countdown");
    if (!state.device) return;
    const tick = () => {
      if (!state.device) return;
      const remaining = (state.device.deadline - Date.now()) / 1000;
      if (remaining <= 0) {
        state.device.expired = true;
        clearTimer("poll");
        updateExpiryText();
        updateGithubButtons();
        setMessage("github", "github.expired", { error: true });
        return;
      }
      updateExpiryText();
    };
    tick();
    state.timers.countdown = window.setInterval(tick, 1000);
  }

  function schedulePoll(seconds) {
    clearTimer("poll");
    if (!state.device || state.device.expired) return;
    const delay = Math.max(5, Number(seconds) || 5) * 1000;
    state.timers.poll = window.setTimeout(() => pollGithub({ auto: true }), delay);
  }

  function showDevice(result) {
    const code = typeof result.user_code === "string" ? result.user_code.trim() : "";
    if (!code) return;
    $("github-device").hidden = false;
    $("github-user-code").textContent = code;

    const open = $("github-open");
    if (open) {
      const uri = result.verification_uri;
      open.href = uri === "https://github.com/login/device" ? uri : "https://github.com/login/device";
    }

    const expires = Number(result.expires_in);
    const deadline = Number.isFinite(expires) && expires > 0 ? Date.now() + expires * 1000 : Date.now() + 15 * 60 * 1000;
    const interval = Math.max(5, Number(result.interval) || 5);
    if (state.device && state.device.code === code) {
      state.device.deadline = deadline;
      state.device.interval = interval;
      state.device.expired = false;
    } else {
      state.device = { code, deadline, interval, expired: false };
    }
    startCountdown();
    schedulePoll(interval);
    updateGithubButtons();
  }

  function hideDevice() {
    $("github-device").hidden = true;
    clearTimer("poll");
    clearTimer("countdown");
    state.device = null;
    state.pollingGithub = false;
    updateGithubButtons();
  }

  function applyGithub(result, action) {
    renderRaw("github-output", result);
    if (!result || typeof result.status !== "string") {
      setMessage("github", "error.generic", { error: true });
      return;
    }
    state.githubStatus = result.status;
    syncGithubSnapshot(result.status);
    switch (result.status) {
      case "authenticated":
        state.githubStatus = "authenticated";
        renderGithubBadge("authenticated");
        hideDevice();
        setMessage("github", "github.authorized");
        refreshAll();
        return;
      case "pending":
        state.githubStatus = "pending";
        renderGithubBadge("pending");
        if (result.user_code) {
          showDevice(result);
        } else if (state.device) {
          const remaining = Number(result.expires_in);
          if (Number.isFinite(remaining) && remaining > 0) {
            state.device.deadline = Date.now() + remaining * 1000;
            state.device.expired = false;
            startCountdown();
          }
          const interval = Number(result.interval);
          if (Number.isFinite(interval) && interval >= 5) state.device.interval = interval;
        }
        setMessage("github", "github.waiting");
        updateGithubButtons();
        if (state.device && !state.device.expired) schedulePoll(state.device.interval);
        return;
      case "signed_out":
        state.githubStatus = "signed_out";
        renderGithubBadge("signed_out");
        hideDevice();
        setMessage("github", action === "logout" ? "github.disconnected" : "github.signed_out");
        return;
      case "error":
        state.githubStatus = "error";
        renderGithubBadge("error");
        clearTimer("poll");
        clearTimer("countdown");
        if (state.device) state.device.expired = true;
        updateGithubButtons();
        setMessage(
          "github",
          action === "poll" ? "github.denied" : action === "start" ? "github.start_failed" : "github.error",
          { error: true },
        );
        return;
      default:
        setMessage("github", "github.unknown");
    }
  }

  async function startGithub() {
    if (state.startingGithub) return;
    const epoch = state.githubEpoch + 1;
    state.githubEpoch = epoch;
    state.startingGithub = true;
    clearTimer("poll");
    const button = $("github-start");
    setBusy(button, "github.starting");
    setMessage("github", null);
    try {
      const result = await api("/api/github/start", { method: "POST", body: {} });
      if (epoch !== state.githubEpoch) return;
      applyGithub(result, "start");
    } catch (error) {
      if (epoch !== state.githubEpoch) return;
      if (error.code !== "authentication_required") {
        setMessage("github", error.network ? "error.network" : "github.start_failed", { error: true });
        renderErrorRaw("github-output", error);
        if (state.device && !state.device.expired) schedulePoll(state.device.interval);
      }
    } finally {
      state.startingGithub = false;
      clearBusy(button);
      updateGithubButtons();
    }
  }

  async function pollGithub(options = {}) {
    const manual = Boolean(options.manual);
    if (!state.device || state.device.expired) return;
    if (state.pollingGithub || state.startingGithub) {
      if (!manual) schedulePoll(2);
      return;
    }
    state.pollingGithub = true;
    const epoch = state.githubEpoch;
    clearTimer("poll");
    const button = $("github-poll");
    if (manual) setBusy(button, "github.polling");
    try {
      const result = await api("/api/github/poll", { method: "POST", body: {} });
      if (epoch !== state.githubEpoch) return;
      applyGithub(result, manual ? "poll" : "auto");
    } catch (error) {
      if (epoch !== state.githubEpoch) return;
      if (error.code !== "authentication_required") {
        setMessage("github", error.network ? "error.network" : "github.poll_failed", { error: true });
        renderErrorRaw("github-output", error);
        if (state.device && !state.device.expired) schedulePoll(state.device.interval);
      }
    } finally {
      state.pollingGithub = false;
      if (manual) clearBusy(button);
      updateGithubButtons();
    }
  }

  async function recoverGithubFlow() {
    if (state.recovering || state.device || state.startingGithub) return;
    if (!state.lastState || !state.lastState.github || state.lastState.github.status !== "pending") return;
    state.recovering = true;
    const epoch = state.githubEpoch;
    try {
      const result = await api("/api/github/poll", { method: "POST", body: {} });
      if (epoch !== state.githubEpoch) return;
      applyGithub(result, "recover");
    } catch (error) {
      if (epoch !== state.githubEpoch) return;
      if (error.code !== "authentication_required") renderErrorRaw("github-output", error);
    } finally {
      state.recovering = false;
    }
  }

  async function logoutGithub() {
    if (state.loggingOutGithub) return;
    const epoch = state.githubEpoch + 1;
    state.githubEpoch = epoch;
    state.loggingOutGithub = true;
    const button = $("github-logout");
    setBusy(button, "common.running");
    try {
      const result = await api("/api/github/logout", { method: "POST", body: {} });
      if (epoch !== state.githubEpoch) return;
      applyGithub(result, "logout");
      await refreshAll();
    } catch (error) {
      if (epoch !== state.githubEpoch) return;
      if (error.code !== "authentication_required") {
        setMessage("github", errorKey(error, "error.generic"), { error: true });
        renderErrorRaw("github-output", error);
      }
    } finally {
      state.loggingOutGithub = false;
      clearBusy(button);
    }
  }

  function fallbackCopy(node) {
    try {
      const range = document.createRange();
      range.selectNodeContents(node);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      const copied = document.execCommand("copy");
      selection.removeAllRanges();
      return copied;
    } catch (_) {
      return false;
    }
  }

  async function copyDeviceCode() {
    const code = $("github-user-code").textContent.trim();
    if (!code) return;
    let copied = false;
    try {
      if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
        await navigator.clipboard.writeText(code);
        copied = true;
      }
    } catch (_) {
      copied = false;
    }
    if (!copied) copied = fallbackCopy($("github-user-code"));

    const button = $("github-copy");
    const label = button.querySelector(".btn-label");
    if (!label) return;
    label.textContent = t(copied ? "common.copied" : "common.copy_failed");
    clearTimer("copy");
    state.timers.copy = window.setTimeout(() => {
      if (hasI18n()) window.FolioI18n.apply(button);
      else label.textContent = t("github.copy_code");
    }, 1600);
  }

  /* ------------------------------------------------------------- repository */

  async function connectRepository(event) {
    event.preventDefault();
    if (state.connecting) return;
    const owner = $("repo-owner").value.trim();
    const name = $("repo-name").value.trim();
    if (!owner || !name) {
      setMessage("repository", "repo.invalid", { error: true });
      return;
    }
    state.connecting = true;
    const button = $("repo-connect");
    setBusy(button, "repo.connecting");
    setMessage("repository", null);
    try {
      const result = await api("/api/repository/connect", {
        method: "POST",
        body: { owner, name },
      });
      renderRaw("repository-output", result);
      setMessage("repository", "repo.connected_message");
      await refreshAll();
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("repository", errorKey(error, "repo.connect_failed"), { error: true });
        renderErrorRaw("repository-output", error);
      }
    } finally {
      state.connecting = false;
      clearBusy(button);
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
      options.push({ id: item.id, label });
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
      option.setAttribute("aria-label", entry.id ? `${entry.label} (${entry.id})` : entry.label);
      const label = document.createElement("span");
      label.className = "model-option-label";
      label.textContent = entry.label;
      option.appendChild(label);
      if (entry.id) {
        const id = document.createElement("span");
        id.className = "model-option-id";
        id.textContent = entry.id;
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
    const github = data.github || {};
    const status = typeof github.status === "string" ? github.status : "unknown";
    state.githubStatus = status;
    renderGithubBadge(status);
    updateGithubButtons();
    if (status === "authenticated" || status === "signed_out" || status === "error") hideDevice();

    const repository = data.repository || {};
    const connected = Boolean(repository.connected);
    const repositoryBadge = $("repository-badge");
    repositoryBadge.dataset.state = connected ? "connected" : "not_connected";
    repositoryBadge.textContent = t(connected ? "repo.connected" : "repo.not_connected");
    $("repository-summary").hidden = !connected;
    $("repo-current-name").textContent = connected ? repository.full_name || "—" : "—";
    $("repo-current-branch").textContent = connected ? repository.branch || "—" : "—";
    $("repo-current-head").textContent = connected && repository.head ? String(repository.head).slice(0, 8) : "—";

    $("fact-github").textContent = t(githubKey(status));
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

    if (connected && repository.full_name && !$("repo-owner").value.trim() && !$("repo-name").value.trim()) {
      const separator = repository.full_name.indexOf("/");
      if (separator > 0) {
        $("repo-owner").value = repository.full_name.slice(0, separator);
        $("repo-name").value = repository.full_name.slice(separator + 1);
      }
    }

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
      await recoverGithubFlow();
    } catch (error) {
      if (error.code !== "authentication_required") {
        setMessage("automation", errorKey(error, "error.generic"), { error: true });
      }
    }
    await refreshAutomation({ silent: true });
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
    return trigger === "scheduler" ? "automation.trigger.scheduler" : "automation.trigger.manual";
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

  $("login-form").addEventListener("submit", login);
  $("logout-button").addEventListener("click", logout);
  $("github-start").addEventListener("click", startGithub);
  $("github-poll").addEventListener("click", () => pollGithub({ manual: true }));
  $("github-logout").addEventListener("click", logoutGithub);
  $("github-copy").addEventListener("click", copyDeviceCode);
  $("repository-form").addEventListener("submit", connectRepository);
  $("sync-button").addEventListener("click", syncRepository);
  $("automation-form").addEventListener("submit", (event) => {
    event.preventDefault();
    runAutomationNow();
  });
  $("run-now-button").addEventListener("click", runAutomationNow);
  $("automation-refresh").addEventListener("click", async () => {
    const button = $("automation-refresh");
    setBusy(button, "common.refreshing");
    try {
      await refreshAutomation();
    } finally {
      clearBusy(button);
    }
  });
  $("runs-list").addEventListener("click", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    const button = target ? target.closest("button[data-run-id]") : null;
    if (button) selectAutomationRun(button.dataset.runId);
  });
  $("runner-select").addEventListener("change", () => updateRunnerUi(state.lastState));
  $("model-input").addEventListener("click", () => {
    if (!state.modelMenuOpen) openModelMenu();
  });
  $("model-input").addEventListener("input", () => {
    state.modelValues[activeRunner()] = $("model-input").value;
    if (state.modelMenuOpen) renderModelMenu();
  });
  $("model-input").addEventListener("keydown", (event) => {
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
  $("model-toggle").addEventListener("click", () => {
    if (state.modelMenuOpen) closeModelMenu();
    else openModelMenu();
    $("model-input").focus();
  });
  $("model-options").addEventListener("mousedown", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (target && target.closest(".model-option")) event.preventDefault();
  });
  $("model-options").addEventListener("click", (event) => {
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

  $("refresh-button").addEventListener("click", async () => {
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
    renderGithubBadge(state.githubStatus);
    updateGithubButtons();
    if (state.device && !state.device.expired) updateExpiryText();
    if (state.modelMenuOpen) renderModelMenu();
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
      } else {
        showLogin();
      }
    } catch (error) {
      showLogin();
      setMessage("login", error.network ? "error.network" : "login.unavailable", { error: true });
    }
  }

  boot();
})();
