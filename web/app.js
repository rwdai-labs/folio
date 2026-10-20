(() => {
  "use strict";

  const state = { csrf: null, preview: null, proposal: null };
  const byId = (id) => document.getElementById(id);

  const connectionState = byId("connection-state");
  const loginPanel = byId("login-panel");
  const workspace = byId("workspace");
  const loginMessage = byId("login-message");

  function setMessage(element, message, error = false) {
    element.textContent = message || "";
    element.dataset.error = error ? "true" : "false";
  }

  function setOnline(online) {
    connectionState.textContent = online ? "Authenticated" : "Offline";
    connectionState.dataset.state = online ? "online" : "offline";
    byId("logout-button").hidden = !online;
    loginPanel.hidden = online;
    workspace.hidden = !online;
  }

  async function request(path, options = {}) {
    const requestOptions = { ...options, headers: { ...(options.headers || {}) } };
    if (requestOptions.body !== undefined && typeof requestOptions.body !== "string") {
      requestOptions.headers["Content-Type"] = "application/json";
      requestOptions.body = JSON.stringify(requestOptions.body);
    }
    if (state.csrf && requestOptions.method && requestOptions.method !== "GET") {
      requestOptions.headers["X-CSRF-Token"] = state.csrf;
    }
    const response = await fetch(path, requestOptions);
    let payload = null;
    try { payload = await response.json(); } catch (_) { payload = {}; }
    if (!response.ok) {
      const error = new Error(typeof payload.error === "string" ? payload.error : `Request failed (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return payload;
  }

  function showError(element, error) {
    if (error && error.status === 401) {
      state.csrf = null;
      setOnline(false);
      setMessage(loginMessage, "Session expired. Sign in again.", true);
      return;
    }
    setMessage(element, error && error.message ? error.message : "Request failed.", true);
  }

  function renderJson(element, value) {
    element.textContent = JSON.stringify(value, null, 2);
  }

  function clearElement(element) {
    while (element.firstChild) element.removeChild(element.firstChild);
  }

  async function loadSession() {
    try {
      const session = await request("/api/session");
      state.csrf = session.csrf_token || null;
      setOnline(Boolean(session.authenticated));
      if (session.authenticated) await refreshAll();
    } catch (error) {
      setOnline(false);
      setMessage(loginMessage, "Service unavailable.", true);
    }
  }

  async function refreshState() {
    const value = await request("/api/state");
    renderJson(byId("state-output"), value);
  }

  async function refreshProposals() {
    const value = await request("/api/proposals");
    const list = [];
    if (Array.isArray(value.proposals)) list.push(...value.proposals);
    if (Array.isArray(value.reports)) {
      value.reports.forEach((report) => {
        if (!Array.isArray(report.proposals)) return;
        report.proposals.forEach((proposal) => {
          list.push({ ...proposal, report_id: report.report_id, report_summary: report.summary });
        });
      });
    }
    const target = byId("proposals-list");
    clearElement(target);
    if (!list.length) {
      const empty = document.createElement("p");
      empty.className = "output";
      empty.textContent = "No pending proposals.";
      target.appendChild(empty);
      return;
    }
    list.forEach((proposal, index) => {
      const item = document.createElement("article");
      item.className = "proposal-item";
      const header = document.createElement("header");
      const title = document.createElement("h3");
      title.textContent = typeof proposal.source === "string" ? proposal.source : `Proposal ${index + 1}`;
      header.appendChild(title);
      item.appendChild(header);
      const details = document.createElement("pre");
      details.className = "output pre-output";
      details.textContent = JSON.stringify(proposal, null, 2);
      item.appendChild(details);
      const useButton = document.createElement("button");
      useButton.type = "button";
      useButton.className = "button secondary";
      useButton.textContent = "Edit this proposal";
      useButton.addEventListener("click", () => useProposal(proposal));
      item.appendChild(useButton);
      target.appendChild(item);
    });
  }

  function useProposal(proposal) {
    state.proposal = proposal;
    const target = typeof proposal.target === "string" ? proposal.target : (typeof proposal.source === "string" ? proposal.source : "");
    let content = proposal.content;
    if (typeof content !== "string") content = proposal.proposed_content;
    if (typeof content !== "string") content = "";
    byId("change-target").value = target;
    byId("change-content").value = content;
    byId("change-target").focus();
    window.scrollTo({ top: byId("editor-heading").getBoundingClientRect().top + window.scrollY - 24, behavior: "smooth" });
  }

  async function refreshAll() {
    try {
      await Promise.all([refreshState(), refreshProposals()]);
    } catch (error) {
      showError(byId("maintenance-message"), error);
    }
  }

  async function pollJob(jobId, output, message) {
    try {
      const job = await request(`/api/jobs/${encodeURIComponent(jobId)}`);
      if (job.status === "running") {
        setMessage(message, "Operation running…");
        window.setTimeout(() => pollJob(jobId, output, message), 500);
        return;
      }
      if (job.status === "succeeded") {
        if (output) renderJson(output, job.result === undefined ? job : job.result);
        setMessage(message, "Operation complete.");
        await refreshAll();
      } else {
        setMessage(message, "Operation failed.", true);
      }
    } catch (error) {
      showError(message, error);
    }
  }

  byId("login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const input = byId("password");
    const password = input.value;
    input.value = "";
    try {
      const result = await request("/api/login", { method: "POST", body: { password } });
      state.csrf = result.csrf_token || null;
      setOnline(true);
      setMessage(loginMessage, "");
      await refreshAll();
    } catch (error) {
      setMessage(loginMessage, error.status === 429 ? "Too many attempts. Try again later." : "Sign in failed.", true);
    }
  });

  byId("logout-button").addEventListener("click", async () => {
    try {
      await request("/api/logout", { method: "POST", body: {} });
    } catch (_) {
      // Clear the local view even when the server session already expired.
    }
    state.csrf = null;
    state.preview = null;
    setOnline(false);
  });

  byId("github-start").addEventListener("click", async () => {
    try {
      const result = await request("/api/github/start", { method: "POST", body: {} });
      renderJson(byId("github-output"), result);
    } catch (error) { showError(byId("github-output"), error); }
  });

  byId("github-poll").addEventListener("click", async () => {
    try {
      const result = await request("/api/github/poll", { method: "POST", body: {} });
      renderJson(byId("github-output"), result);
    } catch (error) { showError(byId("github-output"), error); }
  });

  byId("github-logout").addEventListener("click", async () => {
    try {
      const result = await request("/api/github/logout", { method: "POST", body: {} });
      renderJson(byId("github-output"), result);
    } catch (error) { showError(byId("github-output"), error); }
  });

  byId("repository-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    try {
      const result = await request("/api/repository/connect", {
        method: "POST",
        body: { owner: byId("repo-owner").value, name: byId("repo-name").value },
      });
      renderJson(byId("repository-output"), result);
      await refreshState();
    } catch (error) { showError(byId("repository-output"), error); }
  });

  byId("sync-button").addEventListener("click", async () => {
    try {
      const result = await request("/api/repository/sync", { method: "POST", body: {} });
      await pollJob(result.job_id, byId("repository-output"), byId("repository-output"));
    } catch (error) { showError(byId("repository-output"), error); }
  });

  byId("refresh-button").addEventListener("click", refreshAll);
  byId("proposals-refresh").addEventListener("click", refreshProposals);

  async function runMaintenance(mode) {
    try {
      const body = { mode };
      const model = byId("model-input").value.trim();
      if (model) body.model = model;
      const result = await request("/api/maintenance", { method: "POST", body });
      await pollJob(result.job_id, byId("report-output"), byId("maintenance-message"));
    } catch (error) { showError(byId("maintenance-message"), error); }
  }

  byId("run-button").addEventListener("click", () => runMaintenance("report"));
  byId("run-agent-button").addEventListener("click", () => runMaintenance("agent"));

  byId("editor-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    state.preview = null;
    byId("confirm-row").hidden = true;
    byId("confirm-change").checked = false;
    byId("apply-button").disabled = true;
    try {
      const body = {
        target: byId("change-target").value,
        content: byId("change-content").value,
      };
      if (state.proposal && typeof state.proposal.report_id === "string") body.report_id = state.proposal.report_id;
      if (state.proposal && Number.isInteger(state.proposal.index)) body.proposal_index = state.proposal.index;
      const result = await request("/api/proposals/preview", {
        method: "POST",
        body,
      });
      state.preview = result;
      renderJson(byId("preview-output"), result.preview === undefined ? result : result.preview);
      byId("confirm-row").hidden = false;
      setMessage(byId("editor-message"), "Preview ready. Review it before authorizing commit and push.");
    } catch (error) { showError(byId("editor-message"), error); }
  });

  byId("confirm-change").addEventListener("change", (event) => {
    byId("apply-button").disabled = !state.preview || !event.target.checked;
  });

  byId("apply-button").addEventListener("click", async () => {
    if (!state.preview) return;
    try {
      const result = await request("/api/proposals/apply", {
        method: "POST",
        body: {
          confirm: true,
          preview_id: state.preview.preview_id,
          target: byId("change-target").value,
          content: byId("change-content").value,
          message: byId("commit-message").value,
        },
      });
      renderJson(byId("preview-output"), result);
      state.preview = null;
      byId("confirm-row").hidden = true;
      byId("confirm-change").checked = false;
      byId("apply-button").disabled = true;
      setMessage(byId("editor-message"), "Reviewed change committed and pushed.");
      await refreshAll();
    } catch (error) { showError(byId("editor-message"), error); }
  });

  loadSession();
})();
