/* GitHub plugin frontend.
 *
 * The host mounts this file at /plugins/github/plugin.js and exposes
 * window.folio.api for authenticated requests. This script owns the GitHub
 * card and the searchable repository picker; the shell has no repository form
 * of its own and only carries the connect transaction.
 */
(() => {
  "use strict";

  const API = "/api/plugins/github";
  const card = document.getElementById("plugin-github");
  const slot = document.getElementById("repo-picker-slot");
  // The list is rendered as DOM, so it is capped; the count line says when
  // more matches exist and the search narrows them.
  const MAX_OPTIONS = 60;
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
    repoMeta: null,
    busy: false,
    pollTimer: null,
    // Picker state survives a re-render: the status card and the picker are
    // rebuilt on every status change, but the typed query and choice are not.
    query: "",
    selected: "",
    open: false,
    active: 0,
  };

  // The picker nodes exist only while signed in and repositories are mounted.
  let picker = null;

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
    const status = state.status || { status: "loading" };
    // The card is the sign-in detail surface: it appears while authorization is
    // pending, when signed in, or when something needs attention. Signed out,
    // the repository card's slot carries the single sign-in entry point.
    card.hidden = status.status === "signed_out" || status.status === "loading";
    const head = element("div", "gh-head");
    const heading = element("h2", "", text("GitHub", "GitHub"));
    heading.id = "plugin-github-heading";
    head.append(heading);
    const badge = element("span", "badge", badgeText(status));
    badge.dataset.state = status.status || "loading";
    head.append(badge);
    card.append(head);

    const body = element("div", "gh-body");
    const note = element("p", "gh-hint");
    card.append(body, note);
    renderBody(body, note, status);
    renderSlot();
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
      if (status.mode === "token") {
        // A deployment token is fixed by the environment. Offering a sign-in
        // button here could not change the credential, so the card explains
        // the boundary instead of showing a dead button.
        body.append(element("p", "gh-hint", text(
          "账号由部署环境决定，页面无法切换；仓库搜索只覆盖该令牌可访问的范围。",
          "The account is fixed by the deployment and cannot change here; search covers only repositories that token can access.")));
        return;
      }
      row.append(button(text("退出登录", "Sign out"), () => signOut()));
      body.append(row);
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
      "登录 GitHub 后可以直接选择仓库，不需要手动填写。",
      "Sign in to GitHub to pick a repository from a list; no manual typing.")));
    row.append(button(text("登录 GitHub", "Sign in to GitHub"), () => startSignIn(), "btn primary"));
    body.append(row);
    message(note, status.message || "", status.status === "error");
  }

  /* --------------------------------------------------------- repo picker */

  // The picker lives in the repository card, not in this card: it is the input
  // method for the same connect action. The shell has no owner/name form, so
  // this searchable list is the only way to choose a repository.
  function renderSlot() {
    if (!slot) return;
    const status = state.status || { status: "loading" };
    slot.replaceChildren();
    slot.hidden = false;
    picker = null;
    if (status.status === "authenticated") {
      renderPicker(status);
      return;
    }
    const note = element("p", "gh-hint gh-slot-note");
    note.id = "gh-slot-note";
    const row = element("div", "gh-row");
    if (status.status === "pending") {
      note.textContent = text(
        "等待 GitHub 授权，授权后这里会出现仓库搜索。",
        "Waiting for GitHub authorization; repository search appears here after approval.");
    } else if (status.status === "loading") {
      note.textContent = text("正在检查 GitHub 登录状态…", "Checking GitHub sign-in…");
    } else {
      row.append(button(text("登录 GitHub", "Sign in to GitHub"), () => startSignIn(), "btn primary"));
      note.textContent = text(
        "一键登录 GitHub 后可直接搜索并选择仓库。",
        "Sign in to GitHub once, then search and pick the repository.");
    }
    slot.append(note, row);
  }

  function renderPicker(status) {
    const loaded = state.repositories !== null;
    const wrap = element("div", "gh-picker");

    const note = element("p", "gh-hint gh-slot-note");
    note.id = "gh-slot-note";
    note.textContent = pickerNote(loaded, status);

    const search = element("input", "gh-search");
    search.type = "search";
    search.id = "gh-repo-search";
    search.autocomplete = "off";
    search.spellcheck = false;
    search.value = state.query;
    search.disabled = !loaded;
    search.placeholder = loaded
      ? text("搜索仓库：owner、名称或模糊片段", "Search repositories by owner, name, or fragment")
      : text("正在加载仓库…", "Loading repositories…");
    search.setAttribute("role", "combobox");
    search.setAttribute("aria-autocomplete", "list");
    search.setAttribute("aria-expanded", "false");
    search.setAttribute("aria-controls", "gh-repo-options");
    search.setAttribute("aria-label", text("搜索仓库", "Search repositories"));

    const refresh = button(text("刷新列表", "Refresh list"), (event) => refreshRepositories(event.currentTarget), "btn secondary");
    refresh.id = "gh-refresh";

    const searchRow = element("div", "gh-search-row");
    searchRow.append(search, refresh);

    const list = element("div", "gh-options");
    list.id = "gh-repo-options";
    list.setAttribute("role", "listbox");
    list.setAttribute("aria-label", text("仓库", "Repositories"));
    list.hidden = true;

    const connect = button(text("连接仓库", "Connect repository"), () => connectSelected(), "btn primary");
    connect.id = "gh-connect";
    connect.disabled = !state.selected;

    const selection = element("p", "gh-hint gh-selection");
    selection.id = "gh-selection";

    const actions = element("div", "gh-row");
    actions.append(connect, selection);

    wrap.append(note, searchRow, list, actions);
    slot.append(wrap);

    picker = { root: wrap, search, list, connect, selection, refresh, matches: [] };

    search.addEventListener("input", () => {
      state.query = search.value;
      state.selected = exactRepository(search.value);
      state.open = true;
      state.active = 0;
      updatePicker();
    });
    search.addEventListener("focus", () => {
      state.open = true;
      updatePicker();
    });
    // Focus does not fire again while the field already has focus, so a click
    // after choosing must reopen the list explicitly.
    search.addEventListener("click", () => {
      if (state.open) return;
      state.open = true;
      updatePicker();
    });
    search.addEventListener("keydown", (event) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        if (!state.open) {
          state.open = true;
          state.active = 0;
        } else if (picker.matches.length) {
          state.active = (state.active + step + picker.matches.length) % picker.matches.length;
        }
        updatePicker();
        return;
      }
      if (event.key === "Enter") {
        const match = state.open ? picker.matches[state.active] : null;
        if (!match) return;
        event.preventDefault();
        chooseRepository(match.full_name);
        return;
      }
      if (event.key === "Escape" && state.open) {
        event.preventDefault();
        state.open = false;
        updatePicker();
      }
    });

    updatePicker();
  }

  function pickerNote(loaded, status) {
    if (!loaded) {
      return text("正在加载仓库列表…", "Loading the repository list…");
    }
    const meta = state.repoMeta || {};
    if (meta.error) {
      return text("仓库列表加载失败，请点刷新重试。", "The repository list could not load; use Refresh to retry.");
    }
    const count = Number(meta.count) || 0;
    const privateCount = Number(meta.private) || 0;
    const parts = [count
      ? text("共 " + count + " 个仓库，其中 " + privateCount + " 个私有", count + " repositories, " + privateCount + " private")
      : text("当前凭据看不到任何仓库", "The current credential sees no repositories")];
    if (meta.truncated) {
      parts.push(text("仅加载最近更新的 " + count + " 个", "only the " + count + " most recently updated are loaded"));
    }
    parts.push(text("输入关键词模糊搜索", "type to fuzzy-search"));
    if (status.mode === "token") {
      parts.push(text("部署令牌仅能访问其授权范围内的仓库", "the deployment token reaches only its authorized repositories"));
    }
    return parts.join(" · ");
  }

  function updatePicker() {
    if (!picker) return;
    picker.connect.disabled = !state.selected;
    picker.selection.textContent = "";
    picker.selection.classList.remove("gh-error");
    if (state.selected) {
      picker.selection.textContent = text("已选择 " + state.selected, "Selected " + state.selected);
    }
    renderOptions();
  }

  function exactRepository(value) {
    const wanted = value.trim().toLowerCase();
    if (!wanted) return "";
    for (const repo of state.repositories || []) {
      if (repo.full_name.toLowerCase() === wanted) return repo.full_name;
    }
    return "";
  }

  function searchTerms() {
    return state.query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  }

  // scoreRepository matches every term, in any order, against owner/name. A
  // contiguous hit scores above a scattered subsequence so "folio" puts
  // rwdai-labs/folio first while "rwfol" still finds it.
  function scoreRepository(fullName, terms) {
    const haystack = fullName.toLowerCase();
    let total = 0;
    for (const term of terms) {
      const found = haystack.indexOf(term);
      if (found === 0) {
        total += 1000;
      } else if (found > 0 && "/-_.".includes(haystack[found - 1])) {
        total += 800;
      } else if (found > 0) {
        total += 500 - Math.min(found, 400);
      } else {
        const scattered = subsequenceScore(haystack, term);
        if (scattered < 0) return -1;
        total += 100 + scattered;
      }
    }
    return total;
  }

  function subsequenceScore(haystack, needle) {
    let score = 0;
    let cursor = 0;
    let streak = 0;
    for (const character of needle) {
      const found = haystack.indexOf(character, cursor);
      if (found < 0) return -1;
      if (found === cursor) {
        streak += 1;
        score += 2 + streak;
      } else {
        streak = 0;
        score += 1;
      }
      if (found === 0 || "/-_.".includes(haystack[found - 1])) score += 3;
      cursor = found + 1;
    }
    return score;
  }

  function filteredRepositories() {
    const repositories = state.repositories || [];
    const terms = searchTerms();
    if (!terms.length) return repositories;
    return repositories
      .map((repo, index) => ({ repo, index, score: scoreRepository(repo.full_name, terms) }))
      .filter((item) => item.score >= 0)
      .sort((left, right) => right.score - left.score || left.index - right.index)
      .map((item) => item.repo);
  }

  function renderOptions() {
    if (!picker) return;
    const loaded = state.repositories !== null;
    const matches = loaded ? filteredRepositories() : [];
    picker.matches = matches.slice(0, MAX_OPTIONS);
    if (state.active >= picker.matches.length) state.active = 0;
    picker.list.replaceChildren();
    picker.list.hidden = !state.open || !loaded;
    picker.search.setAttribute("aria-expanded", state.open && loaded ? "true" : "false");
    if (picker.list.hidden) {
      picker.search.removeAttribute("aria-activedescendant");
      return;
    }
    if (!matches.length) {
      const empty = element("p", "gh-options-empty", text("没有匹配的仓库，换个关键词试试。", "No repository matches; try another keyword."));
      picker.list.append(empty);
      picker.search.removeAttribute("aria-activedescendant");
      return;
    }
    picker.matches.forEach((repo, index) => {
      const option = element("button", "gh-option");
      option.type = "button";
      option.id = "gh-repo-option-" + index;
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", repo.full_name === state.selected ? "true" : "false");
      option.classList.toggle("is-active", index === state.active);
      option.classList.toggle("is-selected", repo.full_name === state.selected);
      option.append(element("span", "gh-option-name", repo.full_name));
      if (repo.private) option.append(element("span", "gh-tag", text("私有", "private")));
      option.addEventListener("mousedown", (event) => {
        event.preventDefault();
        chooseRepository(repo.full_name);
      });
      picker.list.append(option);
    });
    if (matches.length > picker.matches.length) {
      picker.list.append(element("p", "gh-options-more", text(
        "还有 " + (matches.length - picker.matches.length) + " 个匹配，继续输入可缩小范围。",
        (matches.length - picker.matches.length) + " more matches; keep typing to narrow them.")));
    }
    picker.search.setAttribute("aria-activedescendant", "gh-repo-option-" + state.active);
    const active = picker.list.querySelector(".gh-option.is-active");
    if (active) active.scrollIntoView({ block: "nearest" });
  }

  function chooseRepository(fullName) {
    state.selected = fullName;
    state.query = fullName;
    state.open = false;
    state.active = 0;
    if (picker) picker.search.value = fullName;
    updatePicker();
  }

  // connectSelected hands the chosen owner/name to the host. The host owns the
  // connect transaction and the refreshed state; this picker only reports it.
  async function connectSelected() {
    if (!state.selected || !picker) return;
    const [owner, name] = state.selected.split("/");
    const host = window.folio;
    const report = (value, isError) => {
      if (!picker) return;
      picker.selection.textContent = value;
      picker.selection.classList.toggle("gh-error", Boolean(isError));
    };
    if (!host || typeof host.connectRepository !== "function") {
      report(text("宿主不支持连接操作，请刷新页面。", "The host cannot connect; reload the page."), true);
      return;
    }
    report(text("正在连接 " + state.selected + "…", "Connecting " + state.selected + "…"), false);
    picker.connect.disabled = true;
    const outcome = await host.connectRepository(owner, name);
    if (!outcome || !outcome.ok) {
      const reason = outcome && outcome.reason;
      report(reason === "invalid"
        ? text("仓库名无效。", "That repository name is not valid.")
        : reason === "busy"
          ? text("已有连接操作正在进行。", "Another connection is already running.")
          : text("连接失败，请检查仓库名与访问权限。", "Could not connect. Check the repository name and access."), true);
      picker.connect.disabled = !state.selected;
      return;
    }
    report(text("已连接 " + state.selected, "Connected " + state.selected), false);
    picker.connect.disabled = false;
  }

  async function refreshRepositories(node) {
    if (node.disabled) return;
    node.disabled = true;
    node.textContent = text("刷新中…", "Refreshing…");
    await loadRepositories();
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
        render();
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
      state.repoMeta = null;
      state.query = "";
      state.selected = "";
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
      state.repoMeta = {
        count: typeof response.count === "number" ? response.count : state.repositories.length,
        private: typeof response.private === "number"
          ? response.private
          : state.repositories.filter((repo) => repo.private).length,
        truncated: Boolean(response.truncated),
        error: false,
      };
    } catch (error) {
      state.repositories = [];
      state.repoMeta = { count: 0, private: 0, truncated: false, error: true };
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

  async function loadStatus() {
    // Paint the loading state before the first request: on a slow GitHub or
    // proxy the repository list can take seconds, and an empty card would look
    // like a broken plugin.
    render();
    try {
      state.status = await api(API + "/status");
      // Show the signed-in shell with a loading picker before the list arrives.
      render();
      if (state.status.status === "authenticated") await loadRepositories();
    } catch (error) {
      state.status = { status: "signed_out" };
      render();
    }
  }

  document.addEventListener("folio:languagechange", () => {
    if (!state.status) return;
    render();
  });

  document.addEventListener("mousedown", (event) => {
    if (!picker || !state.open) return;
    if (picker.root.contains(event.target)) return;
    state.open = false;
    renderOptions();
  });

  // The host signals a completed sign-in so a plugin loaded before login
  // refreshes its status instead of staying on the signed-out view. The
  // signed-out signal drops the picker with the host session.
  document.addEventListener("folio:session", (event) => {
    const authenticated = Boolean(event.detail && event.detail.authenticated);
    if (authenticated) {
      loadStatus();
      return;
    }
    stopPolling();
    state.repositories = null;
    state.repoMeta = null;
    state.query = "";
    state.selected = "";
    state.open = false;
    state.status = { status: "signed_out" };
    render();
  });

  // The card stays hidden until the first status answer: the slot is the
  // visible sign-in entry, and an empty card would flash above it.
  loadStatus();
})();
