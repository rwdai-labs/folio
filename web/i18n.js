/*
 * Folio i18n runtime.
 *
 * Dependency-free classic script. Defines window.FolioI18n: language detection
 * and persistence, dictionary lookup with interpolation, DOM binding through
 * data-i18n* attributes, and locale-aware time formatting.
 *
 * Loaded with defer before app.js, so the document is already parsed.
 */
(function () {
  "use strict";

  var STORAGE_KEY = "folio.lang";
  var FALLBACK_LANG = "en";
  var APPLIED_VALUE_ATTR = "data-i18n-applied";

  // Current language state, exposed through getters on the public API.
  var currentLang = FALLBACK_LANG;
  var currentLocale = "en";

  // --------------------------------------------------------------- dictionary
  // English copy is authoritative and mirrors the frozen contract exactly.

  var EN = {
    // App / global
    "app.title": "Folio · Context maintainer",
    "app.tagline": "Context maintenance console",
    "app.skip": "Skip to content",
    "app.footer": "Runs locally. Automatic maintenance pulls, organizes, validates, and pushes on its own schedule.",

    // Status / language
    "status.online": "Signed in",
    "status.offline": "Signed out",
    "status.checking": "Checking…",
    "status.unknown": "Unknown",
    "lang.aria": "Language",
    "auth.logout": "Log out",

    // Login
    "login.eyebrow": "Local owner",
    "login.title": "Sign in",
    "login.lead": "This console is bound to this host. Use the administrator password from the private data volume.",
    "login.password": "Administrator password",
    "login.placeholder": "Enter the administrator password",
    "login.submit": "Sign in",
    "login.submitting": "Signing in…",
    "login.hint": "Stored at secrets/admin-password in the service data volume.",
    "login.required": "Enter the administrator password.",
    "login.failed": "Sign-in failed. Check the password and try again.",
    "login.rate_limited": "Too many attempts. Wait a minute and try again.",
    "login.unavailable": "The service is unavailable.",
    "login.expired": "Your session expired. Sign in again.",

    // Navigation / state
    "nav.aria": "Sections",
    "nav.github": "GitHub",
    "nav.repository": "Repository",
    "nav.automation": "Automation",
    "state.title": "Current state",
    "state.github": "GitHub",
    "state.repository": "Repository",
    "state.branch": "Branch",
    "state.head": "HEAD",
    "state.model": "Codex",
    "state.scheduler": "Scheduler",
    "state.available": "Available",
    "state.unavailable": "Unavailable",
    "state.enabled": "Enabled",
    "state.disabled": "Disabled",
    "state.last_run": "Last run",

    // GitHub
    "github.kicker": "Identity",
    "github.title": "GitHub",
    "github.lead": "Authorize Folio to read and push to your private context repository. The token stays in the private data volume.",
    "github.connected": "Connected",
    "github.pending": "Waiting for approval",
    "github.signed_out": "Not connected",
    "github.error": "Connection error",
    "github.unknown": "Not checked",
    "github.start": "Start device login",
    "github.restart": "Start a new device login",
    "github.poll": "Check status",
    "github.logout": "Disconnect",
    "github.code_label": "One-time code",
    "github.copy_code": "Copy code",
    "github.open": "Open GitHub device page",
    "github.instructions": "Open the GitHub page, enter this code, and approve access. This page checks automatically.",
    "github.expires": "Expires in {time}",
    "github.expired": "This code has expired. Start a new device login.",
    "github.waiting": "Waiting for approval on GitHub…",
    "github.starting": "Contacting GitHub…",
    "github.polling": "Checking authorization…",
    "github.authorized": "GitHub connected. Connect a repository next.",
    "github.disconnected": "GitHub disconnected.",
    "github.start_failed": "Could not start device login. Check the service network and try again.",
    "github.poll_failed": "Could not check authorization. Try again.",
    "github.denied": "Authorization was not completed. Start again.",

    // Repository
    "repo.kicker": "Remote",
    "repo.title": "Repository",
    "repo.lead": "Connect the private Markdown repository. The checkout stays in the private data volume.",
    "repo.current": "Current repository",
    "repo.owner": "Owner",
    "repo.name": "Name",
    "repo.connect": "Connect",
    "repo.connecting": "Connecting…",
    "repo.connected": "Connected",
    "repo.not_connected": "Not connected",
    "repo.branch": "Branch",
    "repo.head": "HEAD",
    "repo.sync": "Sync repository",
    "repo.syncing": "Syncing…",
    "repo.synced": "Repository synced.",
    "repo.connect_hint": "Use the owner and repository name, for example owner/context.",
    "repo.connect_failed": "Could not connect the repository.",
    "repo.sync_failed": "Sync failed. Check the technical details.",
    "repo.invalid": "Enter a valid owner and repository name.",
    "repo.connected_message": "Repository connected.",
    "repo.none": "Not connected",

    // Automatic maintenance
    "automation.kicker": "Autopilot",
    "automation.title": "Automatic maintenance",
    "automation.lead": "Folio pulls the latest context, asks the runner for bounded Markdown edits, validates them, and pushes the result. Every run is recorded with its edits and diffs.",
    "automation.schedule": "Schedule",
    "automation.next_run": "Next run",
    "automation.last_run": "Last run",
    "automation.runner": "Runner",
    "runner.codex": "Codex",
    "runner.dsh": "DSH",
    "runner.unavailable": "This runner is not installed in the service image.",
    "automation.model": "Model (optional)",
    "automation.model_placeholder": "Provider default",
    "automation.model_dsh": "The DSH runner uses its profile default model.",
    "automation.run_now": "Run now",
    "automation.runner_hint": "Scheduled runs always use Codex. Run now can use Codex or the installed DSH runner.",
    "automation.running": "Maintenance running…",
    "automation.refresh": "Refresh runs",
    "automation.latest": "Latest run",
    "automation.history": "Run history",
    "automation.history_hint": "Newest first. Select a run to inspect its edits.",
    "automation.never": "Never",
    "automation.none": "None",
    "automation.disabled": "Disabled",
    "automation.on": "On",
    "automation.off": "Off",
    "automation.every": "Every {value}",
    "automation.pull": "Pull",
    "automation.commit": "Commit",
    "automation.push": "Push",
    "automation.input": "Model input",
    "automation.files": "Files",
    "automation.duration": "Duration",
    "automation.stage": "Stage",
    "automation.stage.repository": "Repository",
    "automation.stage.pull": "Pull",
    "automation.stage.organize": "Organize",
    "automation.stage.validate": "Validate",
    "automation.stage.commit": "Commit",
    "automation.stage.push": "Push",
    "automation.stage.done": "Done",
    "automation.no_files": "No file changes.",
    "automation.no_runs": "No automatic runs yet. Folio will run on schedule, or use Run now.",
    "automation.loading": "Loading run…",
    "automation.load_failed": "Could not load automatic runs.",
    "automation.source": "Source",
    "automation.diff": "Diff",
    "automation.diff_truncated": "Diff truncated after {lines} lines.",
    "automation.action.create": "Create",
    "automation.action.update": "Update",
    "automation.completed": "Automatic maintenance finished.",
    "automation.pushed": "Changes committed and pushed.",
    "automation.no_changes": "No context changes were needed.",
    "automation.skipped": "Automatic maintenance was skipped.",
    "automation.failed": "Automatic maintenance failed.",
    "automation.invalid": "The automatic change set was rejected by validation.",
    "automation.reason.repository_not_connected": "Connect the repository before automatic maintenance can run.",
    "automation.reason.repository_sync_failed": "The checkout could not be pulled to the latest remote commit.",
    "automation.reason.model_unavailable": "The selected model runner is not installed or configured.",
    "automation.reason.maintenance_operation_failed": "The model runner did not produce a valid maintenance report.",
    "automation.reason.checkout_head_unavailable": "The checkout HEAD could not be read.",
    "automation.reason.content_rejected": "The proposed content failed validation, contained sensitive material, or lost its frontmatter.",
    "automation.reason.target_invalid": "The model proposed a path outside the canonical Markdown tree.",
    "automation.reason.diff_rejected": "The proposed diff was too large.",
    "automation.reason.checkout_rejected": "The checkout contained changes outside the proposed edit set.",
    "automation.reason.commit_failed": "The validated changes could not be committed.",
    "automation.reason.push_failed": "The commit could not be pushed; the local change was rolled back.",
    "automation.reason.validation_failed": "The proposed change set failed validation.",
    "automation.status.pushed": "Pushed",
    "automation.status.no_changes": "No changes",
    "automation.status.skipped": "Skipped",
    "automation.status.failed": "Failed",
    "automation.status.running": "Running",
    "automation.status.unknown": "Unknown",
    "automation.trigger.scheduler": "Scheduled",
    "automation.trigger.manual": "Manual",

    // Common
    "common.details": "Technical details",
    "common.refresh": "Refresh",
    "common.refreshing": "Refreshing…",
    "common.none": "None",
    "common.copied": "Copied",
    "common.copy_failed": "Copy failed",
    "common.running": "Running…",

    // Errors
    "error.generic": "Something went wrong. Try again.",
    "error.network": "Cannot reach the service. Check that it is running.",
    "error.forbidden": "The request was rejected by the local security check.",
    "error.not_found": "The requested item no longer exists.",
    "error.invalid": "The request was not valid.",
    "error.too_many": "Too many requests. Wait a moment.",
    "error.unavailable": "The service is temporarily unavailable.",
    "error.job_busy": "Another job is already running. Wait for it to finish.",
  };

  // ------------------------------------------------------- Chinese dictionary
  // Same keys, polished natural Chinese; never machine-literal.

  var ZH = {
    // App / global
    "app.title": "Folio · 上下文维护台",
    "app.tagline": "上下文维护控制台",
    "app.skip": "跳到正文",
    "app.footer": "本地运行。自动维护会按计划拉取、整理、校验并推送上下文。",

    // Status / language
    "status.online": "已登录",
    "status.offline": "未登录",
    "status.checking": "检查中…",
    "status.unknown": "未知",
    "lang.aria": "语言",
    "auth.logout": "退出登录",

    // Login
    "login.eyebrow": "本地所有者",
    "login.title": "登录",
    "login.lead": "此控制台仅在本机开放，请使用私有数据卷中的管理员密码登录。",
    "login.password": "管理员密码",
    "login.placeholder": "输入管理员密码",
    "login.submit": "登录",
    "login.submitting": "正在登录…",
    "login.hint": "密码保存在服务数据卷的 secrets/admin-password。",
    "login.required": "请输入管理员密码。",
    "login.failed": "登录失败，请检查密码后重试。",
    "login.rate_limited": "尝试次数过多，请稍后再试。",
    "login.unavailable": "服务暂时不可用。",
    "login.expired": "会话已过期，请重新登录。",

    // Navigation / state
    "nav.aria": "页面分区",
    "nav.github": "GitHub",
    "nav.repository": "仓库",
    "nav.automation": "自动化",
    "state.title": "当前状态",
    "state.github": "GitHub",
    "state.repository": "仓库",
    "state.branch": "分支",
    "state.head": "提交",
    "state.model": "Codex",
    "state.scheduler": "调度器",
    "state.available": "可用",
    "state.unavailable": "不可用",
    "state.enabled": "已启用",
    "state.disabled": "已禁用",
    "state.last_run": "上次运行",

    // GitHub
    "github.kicker": "身份",
    "github.title": "GitHub",
    "github.lead": "授权 Folio 读写你的私有上下文仓库，凭据只保存在私有数据卷中。",
    "github.connected": "已连接",
    "github.pending": "等待授权",
    "github.signed_out": "未连接",
    "github.error": "连接异常",
    "github.unknown": "未检查",
    "github.start": "开始设备登录",
    "github.restart": "重新开始设备登录",
    "github.poll": "检查状态",
    "github.logout": "断开连接",
    "github.code_label": "一次性验证码",
    "github.copy_code": "复制验证码",
    "github.open": "打开 GitHub 设备授权页",
    "github.instructions": "打开 GitHub 页面，输入此验证码并确认授权。本页会自动检查状态。",
    "github.expires": "{time} 后过期",
    "github.expired": "验证码已过期，请重新开始设备登录。",
    "github.waiting": "正在等待 GitHub 授权…",
    "github.starting": "正在连接 GitHub…",
    "github.polling": "正在检查授权…",
    "github.authorized": "GitHub 已连接，接下来可以连接仓库。",
    "github.disconnected": "已断开 GitHub。",
    "github.start_failed": "无法开始设备登录，请检查服务网络后重试。",
    "github.poll_failed": "无法检查授权状态，请重试。",
    "github.denied": "授权未完成，请重新开始。",

    // Repository
    "repo.kicker": "远端",
    "repo.title": "仓库",
    "repo.lead": "连接私有 Markdown 仓库，检出内容只保存在私有数据卷中。",
    "repo.current": "当前仓库",
    "repo.owner": "所有者",
    "repo.name": "仓库名",
    "repo.connect": "连接",
    "repo.connecting": "正在连接…",
    "repo.connected": "已连接",
    "repo.not_connected": "未连接",
    "repo.branch": "分支",
    "repo.head": "提交",
    "repo.sync": "同步仓库",
    "repo.syncing": "正在同步…",
    "repo.synced": "仓库已同步。",
    "repo.connect_hint": "填写所有者和仓库名，例如 owner/context。",
    "repo.connect_failed": "连接仓库失败。",
    "repo.sync_failed": "同步失败，请查看技术详情。",
    "repo.invalid": "请输入有效的所有者和仓库名。",
    "repo.connected_message": "仓库已连接。",
    "repo.none": "未连接",

    // 自动维护
    "automation.kicker": "自动维护",
    "automation.title": "自动维护",
    "automation.lead": "Folio 会拉取最新上下文，让运行器生成有界的 Markdown 修改，校验后推送结果。每次运行都会记录修改内容和差异。",
    "automation.schedule": "计划",
    "automation.next_run": "下次运行",
    "automation.last_run": "上次运行",
    "automation.runner": "运行器",
    "runner.codex": "Codex",
    "runner.dsh": "DSH",
    "runner.unavailable": "服务镜像中未安装此运行器。",
    "automation.model": "模型（可选）",
    "automation.model_placeholder": "使用默认模型",
    "automation.model_dsh": "DSH 运行器使用其配置中的默认模型。",
    "automation.run_now": "立即运行",
    "automation.runner_hint": "定时运行始终使用 Codex；立即运行可以使用 Codex 或已安装的 DSH。",
    "automation.running": "维护运行中…",
    "automation.refresh": "刷新运行记录",
    "automation.latest": "最近一次运行",
    "automation.history": "运行历史",
    "automation.history_hint": "按时间倒序。选择一次运行以查看修改。",
    "automation.never": "从未",
    "automation.none": "无",
    "automation.disabled": "已停用",
    "automation.on": "已启用",
    "automation.off": "已停用",
    "automation.every": "每 {value}",
    "automation.pull": "拉取",
    "automation.commit": "提交",
    "automation.push": "推送",
    "automation.input": "模型输入",
    "automation.files": "文件",
    "automation.duration": "耗时",
    "automation.stage": "阶段",
    "automation.stage.repository": "仓库",
    "automation.stage.pull": "拉取",
    "automation.stage.organize": "整理",
    "automation.stage.validate": "校验",
    "automation.stage.commit": "提交",
    "automation.stage.push": "推送",
    "automation.stage.done": "完成",
    "automation.no_files": "没有文件修改。",
    "automation.no_runs": "还没有自动运行记录。Folio 会按计划运行，也可以立即运行。",
    "automation.loading": "正在加载运行…",
    "automation.load_failed": "加载自动运行记录失败。",
    "automation.source": "来源",
    "automation.diff": "差异",
    "automation.diff_truncated": "差异过长，仅显示前 {lines} 行。",
    "automation.action.create": "新建",
    "automation.action.update": "更新",
    "automation.completed": "自动维护已完成。",
    "automation.pushed": "修改已提交并推送。",
    "automation.no_changes": "无需修改上下文。",
    "automation.skipped": "自动维护已跳过。",
    "automation.failed": "自动维护失败。",
    "automation.invalid": "自动修改未通过校验。",
    "automation.reason.repository_not_connected": "请先连接仓库，自动维护才能运行。",
    "automation.reason.repository_sync_failed": "无法将检出拉取到远端最新提交。",
    "automation.reason.model_unavailable": "所选模型运行器未安装或未配置。",
    "automation.reason.maintenance_operation_failed": "模型运行器没有生成有效的维护报告。",
    "automation.reason.checkout_head_unavailable": "无法读取检出 HEAD。",
    "automation.reason.content_rejected": "候选内容未通过校验、包含敏感信息或丢失了 frontmatter。",
    "automation.reason.target_invalid": "模型提出了 canonical Markdown 树之外的路径。",
    "automation.reason.diff_rejected": "候选差异过大。",
    "automation.reason.checkout_rejected": "检出中出现了候选修改之外的变更。",
    "automation.reason.commit_failed": "校验后的修改无法提交。",
    "automation.reason.push_failed": "提交无法推送；本地修改已回滚。",
    "automation.reason.validation_failed": "候选修改未通过校验。",
    "automation.status.pushed": "已推送",
    "automation.status.no_changes": "无变化",
    "automation.status.skipped": "已跳过",
    "automation.status.failed": "失败",
    "automation.status.running": "运行中",
    "automation.status.unknown": "未知",
    "automation.trigger.scheduler": "定时",
    "automation.trigger.manual": "手动",

    // Common
    "common.details": "技术详情",
    "common.refresh": "刷新",
    "common.refreshing": "正在刷新…",
    "common.none": "无",
    "common.copied": "已复制",
    "common.copy_failed": "复制失败",
    "common.running": "正在运行…",

    // Errors
    "error.generic": "操作失败，请重试。",
    "error.network": "无法连接服务，请确认服务正在运行。",
    "error.forbidden": "请求被本地安全检查拒绝。",
    "error.not_found": "请求的对象已不存在。",
    "error.invalid": "请求无效。",
    "error.too_many": "请求过于频繁，请稍后再试。",
    "error.unavailable": "服务暂时不可用。",
    "error.job_busy": "已有任务在运行，请等待其完成。",
  };

  var DICT = { en: EN, zh: ZH };

  // ------------------------------------------------------- lookup / formatting

  function owns(table, key) {
    return !!table && Object.prototype.hasOwnProperty.call(table, key);
  }

  function lookup(lang, key) {
    if (typeof key !== "string") return undefined;
    var table = DICT[lang];
    return owns(table, key) ? table[key] : undefined;
  }

  function interpolate(text, vars) {
    if (!vars || typeof text !== "string") return text;
    return text.replace(/\{([A-Za-z0-9_.-]+)\}/g, function (match, name) {
      if (!owns(vars, name)) return match;
      var value = vars[name];
      if (value === null || value === undefined) return match;
      return String(value);
    });
  }

  // Current language first, then English, then the key itself.
  function t(key, vars) {
    var text = lookup(currentLang, key);
    if (text === undefined) text = lookup(FALLBACK_LANG, key);
    if (text === undefined) return key === undefined || key === null ? "" : String(key);
    return interpolate(text, vars);
  }

  function has(key) {
    return owns(DICT[currentLang], key) || owns(EN, key);
  }

  // --------------------------------------------------------------- DOM binding

  function scopeOf(root) {
    return root && typeof root.querySelectorAll === "function" ? root : document;
  }

  // Collect descendants, and the root itself when it matches the selector.
  function collect(root, selector) {
    var nodes = [];
    if (!root || typeof root.querySelectorAll !== "function") return nodes;
    if (root.nodeType === 1 && typeof root.matches === "function") {
      try {
        if (root.matches(selector)) nodes.push(root);
      } catch (err) {
        // Ignore unsupported selectors on unusual roots.
      }
    }
    try {
      var found = root.querySelectorAll(selector);
      for (var i = 0; i < found.length; i += 1) nodes.push(found[i]);
    } catch (err) {
      // Keep whatever was collected before the failure.
    }
    return nodes;
  }

  function applyText(scope) {
    var nodes = collect(scope, "[data-i18n]");
    for (var i = 0; i < nodes.length; i += 1) {
      var key = nodes[i].getAttribute("data-i18n");
      if (key) nodes[i].textContent = t(key);
    }
  }

  function applyAttribute(scope, selector, keyAttr, targetAttr) {
    var nodes = collect(scope, selector);
    for (var i = 0; i < nodes.length; i += 1) {
      var key = nodes[i].getAttribute(keyAttr);
      if (key) nodes[i].setAttribute(targetAttr, t(key));
    }
  }

  // A field is untouched when it is empty, still shows the last translation we
  // applied, or still carries the initial English default from the HTML.
  function isUntouchedValue(node, key, current) {
    if (current === "") return true;
    var previous = node.getAttribute(APPLIED_VALUE_ATTR);
    if (previous !== null) return current === previous;
    var initial = lookup(FALLBACK_LANG, key);
    return typeof initial === "string" && current === initial;
  }

  function applyValue(scope) {
    var nodes = collect(scope, "[data-i18n-value]");
    for (var i = 0; i < nodes.length; i += 1) {
      var node = nodes[i];
      var key = node.getAttribute("data-i18n-value");
      if (!key) continue;
      var current = typeof node.value === "string" ? node.value : "";
      if (!isUntouchedValue(node, key, current)) continue;
      var next = t(key);
      node.value = next;
      node.setAttribute(APPLIED_VALUE_ATTR, next);
    }
  }

  function isDocumentScope(scope) {
    if (scope === document) return true;
    return !!(document.documentElement && scope === document.documentElement);
  }

  function updateDocumentMetadata() {
    if (document.documentElement) document.documentElement.lang = currentLocale;
    document.title = t("app.title");
  }

  // Translate every data-i18n* binding under root (default: document).
  function apply(root) {
    var scope = scopeOf(root);
    applyText(scope);
    applyAttribute(scope, "[data-i18n-placeholder]", "data-i18n-placeholder", "placeholder");
    applyAttribute(scope, "[data-i18n-aria-label]", "data-i18n-aria-label", "aria-label");
    applyAttribute(scope, "[data-i18n-title]", "data-i18n-title", "title");
    applyValue(scope);
    if (isDocumentScope(scope)) updateDocumentMetadata();
  }

  // ---------------------------------------------------------- language buttons

  function updateLanguageButtons(root) {
    var nodes = collect(scopeOf(root), "[data-lang]");
    for (var i = 0; i < nodes.length; i += 1) {
      var lang = nodes[i].getAttribute("data-lang");
      if (lang !== "zh" && lang !== "en") continue;
      var active = lang === currentLang;
      nodes[i].setAttribute("aria-pressed", active ? "true" : "false");
      if (nodes[i].classList) {
        if (active) nodes[i].classList.add("is-active");
        else nodes[i].classList.remove("is-active");
      }
    }
  }

  function findLangButton(node) {
    while (node && node !== document) {
      if (node.nodeType === 1 && typeof node.getAttribute === "function") {
        var lang = node.getAttribute("data-lang");
        if (lang === "zh" || lang === "en") return node;
      }
      node = node.parentNode;
    }
    return null;
  }

  function handleDocumentClick(event) {
    var button = findLangButton(event && event.target);
    if (!button) return;
    setLang(button.getAttribute("data-lang"));
  }

  // ------------------------------------------------------- language lifecycle

  function localeFor(lang) {
    return lang === "zh" ? "zh-CN" : "en";
  }

  // In a browser these are the same object; keep both lookups so the module
  // still works in restricted hosts and test harnesses.
  function storageRef() {
    try {
      if (typeof localStorage !== "undefined" && localStorage) return localStorage;
    } catch (err) {
      // Ignore blocked storage and try window.localStorage next.
    }
    try {
      if (typeof window !== "undefined" && window.localStorage) return window.localStorage;
    } catch (err) {
      // Ignore blocked storage.
    }
    return null;
  }

  function readStoredLang() {
    try {
      var storage = storageRef();
      if (!storage || typeof storage.getItem !== "function") return null;
      var raw = storage.getItem(STORAGE_KEY);
      if (raw === "zh" || raw === "en") return raw;
    } catch (err) {
      // Private mode or blocked storage: fall back to the browser preference.
    }
    return null;
  }

  function browserLang() {
    var nav = null;
    if (typeof navigator !== "undefined" && navigator) nav = navigator;
    else if (typeof window !== "undefined" && window.navigator) nav = window.navigator;
    if (!nav) return FALLBACK_LANG;
    var candidates = [];
    if (nav.language) candidates.push(nav.language);
    if (nav.languages && nav.languages.length) candidates.push(nav.languages[0]);
    for (var i = 0; i < candidates.length; i += 1) {
      var value = candidates[i];
      if (typeof value === "string" && value.toLowerCase().indexOf("zh") === 0) return "zh";
    }
    return FALLBACK_LANG;
  }

  function detectLang() {
    return readStoredLang() || browserLang();
  }

  function persistLang(lang) {
    try {
      var storage = storageRef();
      if (storage && typeof storage.setItem === "function") {
        storage.setItem(STORAGE_KEY, lang);
      }
    } catch (err) {
      // Persistence is best-effort; the in-memory language still applies.
    }
  }

  function dispatchLanguageChange(lang) {
    if (!document || typeof document.dispatchEvent !== "function") return;
    var detail = { lang: lang };
    var event = null;
    var Ctor = null;
    if (typeof window !== "undefined" && typeof window.CustomEvent === "function") {
      Ctor = window.CustomEvent;
    } else if (typeof CustomEvent === "function") {
      Ctor = CustomEvent;
    }
    if (Ctor) {
      try {
        event = new Ctor("folio:languagechange", { detail: detail, bubbles: true });
      } catch (err) {
        event = null;
      }
    }
    if (!event && typeof document.createEvent === "function") {
      try {
        event = document.createEvent("CustomEvent");
        event.initCustomEvent("folio:languagechange", true, false, detail);
      } catch (err) {
        event = null;
      }
    }
    if (event) document.dispatchEvent(event);
  }

  // Apply a supported language everywhere and notify listeners.
  function setLang(lang) {
    if (lang !== "zh" && lang !== "en") return currentLang;
    currentLang = lang;
    currentLocale = localeFor(lang);
    persistLang(lang);
    apply(document);
    updateLanguageButtons(document);
    dispatchLanguageChange(lang);
    return currentLang;
  }

  // ------------------------------------------------------------- time format

  function isoFallback(date) {
    try {
      return date.toISOString().slice(0, 16).replace("T", " ");
    } catch (err) {
      return "";
    }
  }

  function formatTime(value) {
    if (value === "" || value === null || value === undefined) return "";
    var date = new Date(value);
    if (isNaN(date.getTime())) return "";
    try {
      if (typeof Intl !== "undefined" && Intl && typeof Intl.DateTimeFormat === "function") {
        return new Intl.DateTimeFormat(currentLocale, {
          dateStyle: "medium",
          timeStyle: "short",
        }).format(date);
      }
    } catch (err) {
      // Older engines without dateStyle/timeStyle support use the fallback.
    }
    return isoFallback(date);
  }

  // -------------------------------------------------------------------- boot

  function init() {
    currentLang = detectLang();
    currentLocale = localeFor(currentLang);
    apply(document);
    updateLanguageButtons(document);
    if (document && typeof document.addEventListener === "function") {
      document.addEventListener("click", handleDocumentClick, false);
    }
  }

  var api = {
    t: t,
    has: has,
    setLang: setLang,
    apply: apply,
    formatTime: formatTime,
  };

  Object.defineProperty(api, "lang", {
    enumerable: true,
    get: function () {
      return currentLang;
    },
    set: function (value) {
      setLang(value);
    },
  });

  Object.defineProperty(api, "locale", {
    enumerable: true,
    get: function () {
      return currentLocale;
    },
  });

  window.FolioI18n = api;
  init();
})();
