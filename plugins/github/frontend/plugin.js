/* GitHub plugin frontend.
 *
 * The host mounts this file at /plugins/github/plugin.js and exposes
 * window.folio.api for authenticated requests. This script owns the GitHub
 * card and the repository picker; the base UI keeps a manual owner/name form
 * as a fallback when no plugin is installed.
 */
(() => {
  "use strict";

  const API = "/api/plugins/github";
  const card = document.getElementById("plugin-github");
  const slot = document.getElementById("repo-picker-slot");
  if (!card) return;

  // The host serves this plugin's assets from the plugin module; the stylesheet
  // is injected here so the shell does not need to know plugin ids.
  if (!document.querySelector('link[data-plugin-style="github"]')) {
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = "/plugins/github/plugin.css";
    link.dataset.pluginStyle = "github";
    document.head.append(link);
  }

  const state = {
    status: null,
    repositories: null,
    busy: false,
    pollTimer: null,
  };

  const lang = () => {
    const value = document.documentElement.lang;
    return typeof value === "string" && value.startsWith("zh") ? "zh" : "en";
  };
  const text = (zh, en) => (lang() === "zh" ? zh : en);
  const api = (path, options) => (window.folio && window.folio.api ? window.folio.api(path, options) : Promise.reject(new Error("host api unavailable")));

  function element(tag, className, content) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (content !== undefined) node.textContent = content;
    return node;
  }

  function button(label, onClick, className) {
    const node = element("button", className || "btn secondary", label);
    node.type = "button";
    node.addEventListener("click", onClick);
    return node;
  }

  function message(node, value, isError) {
    node.textContent = value || "";
    node.classList.toggle("gh-error", Boolean(isError));
    node.hidden = !value;
  }

  /* ------------------------------------------------------------- rendering */

  function render() {
    card.replaceChildren();
    card.hidden = false;
    const head = element("div", "gh-head");
    const heading = element("h2", "", text("GitHub", "GitHub"));
    heading.id = "plugin-github-heading";
    head.append(heading);
    const status = state.status || { status: "loading" };
    const badge = element("span", "badge", badgeText(status));
    badge.dataset.state = status.status || "loading";
    head.append(badge);
    card.append(head);

    const body = element("div", "gh-body");
    const note = element("p", "gh-hint");
    card.append(body, note);
    renderBody(body, note, status);
  }

  function badgeText(status) {
    if (!status || status.status === "loading") return text("加载中", "Loading");
    if (status.status === "authenticated") {
      return status.mode === "token" ? text("部署令牌", "Deployment token") : text("已登录", "Signed in");
    }
    if (status.status === "pending") return text("等待授权", "Waiting for authorization");
    if (status.status === "error") return text("需要处理", "Needs attention");
    return text("未登录", "Signed out");
  }

  function renderBody(body, note, status) {
    const row = element("div", "gh-row");
    if (status.status === "authenticated") {
      body.append(element("p", "gh-hint", status.mode === "token"
        ? text("已通过 GIT_AUTH_TOKEN 认证，仓库操作使用部署令牌。", "Authenticated with GIT_AUTH_TOKEN; repository operations use the deployment token.")
        : text("已通过 gh 登录，git 与 API 共用这份凭据。", "Signed in through gh; git and the API share this credential.")));
      if (status.mode !== "token") {
        row.append(button(text("退出登录", "Sign out"), () => signOut()));
      }
      row.append(button(text("重新授权", "Sign in again"), () => startSignIn()));
      body.append(row);
      renderPicker();
      return;
    }
    if (status.status === "pending") {
      const device = element("div", "gh-device");
      device.append(element("p", "gh-hint", text("在 GitHub 中输入下面的代码：", "Enter this code on GitHub:")));
      device.append(element("div", "gh-code", status.user_code || ""));
      const link = element("a", "btn secondary gh-link", text("打开 GitHub", "Open GitHub"));
      link.href = "https://github.com/login/device";
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      device.append(link);
      device.append(element("p", "gh-hint", text("此页面会自动检查授权状态。", "This page checks for authorization automatically.")));
      body.append(device);
      row.append(button(text("立即检查", "Check now"), () => poll()));
      row.append(button(text("取消", "Cancel"), () => cancelSignIn()));
      body.append(row);
      startPolling();
      return;
    }
    body.append(element("p", "gh-hint", text(
      "登录 GitHub 后可以直接选择仓库；也可以使用下方的 owner/name 手动填写。",
      "Sign in to GitHub to pick a repository, or use the manual owner/name fields below.")));
    row.append(button(text("登录 GitHub", "Sign in to GitHub"), () => startSignIn(), "btn"));
    body.append(row);
    message(note, status.message || "", status.status === "error");
  }

  // The picker lives in the repository card, not in this card: it is an input
  // method for the same connect action. It fills the core inputs, so the
  // existing connect button keeps working unchanged.
  function renderPicker() {
    if (!slot) return;
    slot.replaceChildren();
    hideManualInputs(true);
    slot.hidden = false;
    const wrapper = element("div", "gh-row");
    const select = element("select");
    select.id = "gh-repo-select";
    select.setAttribute("aria-label", text("选择仓库", "Choose repository"));
    const placeholder = element("option", "", text("选择仓库…", "Choose repository…"));
    placeholder.value = "";
    select.append(placeholder);
    for (const repo of state.repositories || []) {
      const option = element("option", "", repo.full_name + (repo.private ? " · " + text("私有", "private") : ""));
      option.value = repo.full_name;
      select.append(option);
    }
    if (!state.repositories) {
      select.disabled = true;
      placeholder.textContent = text("正在加载仓库…", "Loading repositories…");
    }
    select.addEventListener("change", () => {
      const [owner, name] = select.value.split("/");
      const ownerInput = document.getElementById("repo-owner");
      const nameInput = document.getElementById("repo-name");
      if (ownerInput) ownerInput.value = owner || "";
      if (nameInput) nameInput.value = name || "";
    });
    wrapper.append(select);
    wrapper.append(button(text("刷新列表", "Refresh list"), () => loadRepositories()));
    slot.append(wrapper);
  }

  /* ---------------------------------------------------------------- actions */

  async function startSignIn() {
    if (state.busy) return;
    state.busy = true;
    try {
      state.status = await api(API + "/auth/start", { method: "POST", body: {} });
      render();
    } catch (error) {
      state.status = { status: "error", message: text("无法开始登录，请检查网络与 GH_TOKEN 配置。", "Could not start sign-in. Check the network and configuration.") };
      render();
    } finally {
      state.busy = false;
    }
  }

  async function poll() {
    if (state.busy) return;
    state.busy = true;
    try {
      state.status = await api(API + "/auth/poll", { method: "POST", body: {} });
      if (state.status.status === "authenticated") {
        stopPolling();
        await loadRepositories();
        refreshHost();
      }
      render();
    } catch (error) {
      stopPolling();
      state.status = { status: "error", message: text("检查授权失败。", "Could not check authorization.") };
      render();
    } finally {
      state.busy = false;
    }
  }

  function cancelSignIn() {
    stopPolling();
    state.status = { status: "signed_out" };
    render();
  }

  async function signOut() {
    if (state.busy) return;
    state.busy = true;
    try {
      state.status = await api(API + "/auth/logout", { method: "POST", body: {} });
      state.repositories = null;
      clearPicker();
    } catch (error) {
      state.status = { status: "error", message: text("退出登录失败。", "Sign-out failed.") };
    } finally {
      state.busy = false;
      render();
      refreshHost();
    }
  }

  async function loadRepositories() {
    try {
      const response = await api(API + "/repositories");
      state.repositories = Array.isArray(response.repositories) ? response.repositories : [];
    } catch (error) {
      state.repositories = [];
    }
    if (state.status && state.status.status === "authenticated") render();
  }

  function startPolling() {
    stopPolling();
    const seconds = Math.max(5, Number(state.status.interval) || 5);
    state.pollTimer = window.setInterval(() => {
      if (document.hidden) return;
      poll();
    }, seconds * 1000);
  }

  function stopPolling() {
    if (state.pollTimer) window.clearInterval(state.pollTimer);
    state.pollTimer = null;
  }

  function refreshHost() {
    if (window.folio && typeof window.folio.refreshState === "function") {
      window.folio.refreshState().catch(() => {});
    }
  }

  /* ------------------------------------------------------------------ setup */

  function hideManualInputs(hidden) {
    const holder = document.querySelector("#repo-manual .form-inline");
    if (holder) holder.hidden = hidden;
  }

  function clearPicker() {
    if (slot) {
      slot.replaceChildren();
      slot.hidden = true;
    }
    hideManualInputs(false);
  }

  async function loadStatus() {
    try {
      state.status = await api(API + "/status");
      if (state.status.status === "authenticated") await loadRepositories();
    } catch (error) {
      state.status = { status: "signed_out" };
    }
    render();
    if (state.status.status !== "authenticated") clearPicker();
  }

  document.addEventListener("folio:languagechange", () => {
    if (!state.status) return;
    render();
  });

  // The host signals a completed sign-in so a plugin loaded before login
  // refreshes its status instead of staying on the signed-out view.
  document.addEventListener("folio:session", (event) => {
    if (!event.detail || !event.detail.authenticated) return;
    loadStatus();
  });

  card.hidden = false;
  loadStatus();
})();
