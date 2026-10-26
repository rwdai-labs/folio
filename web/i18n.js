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
    "status.online": "Signed in to Folio",
    "status.offline": "Signed out of Folio",
    "status.checking": "Checking…",
    "status.unknown": "Unknown",
    "lang.aria": "Language",
    "auth.logout": "Log out of Folio",

    // Login
    "login.eyebrow": "Local owner",
    "login.title": "Sign in to Folio",
    "login.lead": "This console is bound to this host. Use the administrator password from the private data volume.",
    "login.password": "Administrator password",
    "login.placeholder": "Enter the administrator password",
    "login.submit": "Sign in to Folio",
    "login.submitting": "Signing in…",
    "login.hint": "Stored at secrets/admin-password in the service data volume.",
    "login.required": "Enter the administrator password.",
    "login.failed": "Sign-in failed. Check the password and try again.",
    "login.rate_limited": "Too many attempts. Wait a minute and try again.",
    "login.unavailable": "The service is unavailable.",
    "login.expired": "Your session expired. Sign in again.",

    // Navigation / state
    "nav.aria": "Sections",
    "nav.repository": "Repository",
    "nav.automation": "Automation",
    "state.title": "Current state",
    "state.repository": "Repository",
    "state.branch": "Branch",
    "state.head": "HEAD",
    "state.model": "Codex",
    "state.scheduler": "Scheduler",
    "state.git_credential": "Repository authorization",
    "state.credential_token": "Deployment token configured",
    "state.credential_gh": "Signed in to GitHub",
    "state.credential_missing": "Authorization required",
    "state.available": "Available",
    "state.unavailable": "Unavailable",
    "state.enabled": "Enabled",
    "state.disabled": "Disabled",
    "state.last_run": "Last run",

    // GitHub

    // Repository
    "repo.kicker": "Remote",
    "repo.title": "Repository",
    "repo.lead": "Connect the private Markdown repository. The checkout stays in the private data volume.",
    "repo.current": "Selected repository",
    "repo.private": "private",
    "repo.connected": "Repository selected",
    "repo.not_connected": "No repository selected",
    "repo.auth_required": "Repository authorization required",
    "repo.branch": "Branch",
    "repo.head": "HEAD",
    "repo.sync": "Sync repository",
    "repo.syncing": "Syncing…",
    "repo.synced": "Repository synced.",
    "repo.sync_failed": "Sync failed. Check the technical details.",
    "repo.none": "No repository selected",

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
    "automation.model": "Model",
    "automation.model_placeholder": "Runner default",
    "automation.model_default": "Runner default",
    "automation.model_toggle": "Show model suggestions",
    "automation.model_none": "No suggestions. Type a model ID.",
    "automation.run_now": "Run now",
    "automation.runner_hint": "Pick a suggestion or type any model ID; Codex suggestions come from its installed catalog and DSH from its active profile. Blank uses the runner default. Scheduled runs use Codex; Run now can use Codex or DSH.",
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
    "automation.input": "Input digest",
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
    "automation.reason.checkout_remote_mismatch": "Local and remote commits differ. Review and reconcile unpublished commits before retrying.",
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
    "automation.status.reported": "Report ready",
    "automation.report_title": "Report",
    "automation.needs_review": "Needs your review",
    "automation.remaining": "{count} changed documents remain for a following task.",
    "automation.finding.document_too_large": "Too large for a complete maintenance input; review or split this document.",
    "automation.finding.sensitive_content_excluded": "Excluded from model input because it may contain sensitive material.",
    "automation.finding.candidate_requires_owner_review": "Candidate remains pending; organization does not approve it.",
    "automation.finding.stale_context_requires_evidence": "Verify the facts before renewing the review date.",
    "automation.finding.broken_link_requires_review": "A document link is broken; repair needs an unambiguous supplied destination.",
    "automation.finding.parent_unavailable_requires_review": "The declared parent is missing or unavailable to maintenance.",
    "automation.finding.hierarchy_cycle_requires_review": "The hierarchy contains a cycle; review its parent metadata.",
    "automation.finding.related_context_over_budget": "Related context did not fit; parent summary synchronization needs review.",
    "automation.reason.maintenance_input_invalid": "The context input or maintenance checkpoint could not be read safely.",
    "automation.status.unknown": "Unknown",
    "automation.trigger.scheduler": "Scheduled",
    "automation.trigger.manual": "Manual",
    "automation.trigger.hook": "Event hook",
    "automation.trigger.task": "Task queue",

    // Push notifications
    "nav.channels": "Notifications",
    "channels.kicker": "Delivery",
    "channels.title": "Push notifications",
    "channels.lead": "Folio only speaks outward: when maintenance finishes or fails it pushes one short message to the channel you configure here. Nothing is ever sent back.",
    "channels.enabled": "Send a push when a run finishes",
    "channels.server": "Bark server",
    "channels.server_hint": "Leave blank for the public server. Plain HTTP is accepted only for a device on your own network.",
    "channels.device_key": "Device key",
    "channels.device_key_hint": "Stored in the private volume and never returned to the browser.",
    "channels.device_key_stored": "A device key is stored. Leave this blank to keep it.",
    "channels.device_key_missing": "No device key stored yet.",
    "channels.group": "Group",
    "channels.level": "Urgency",
    "channels.level.passive": "Passive",
    "channels.level.active": "Active",
    "channels.level.time_sensitive": "Time sensitive",
    "channels.language": "Message language",
    "channels.save": "Save channel",
    "channels.test": "Send test",
    "channels.deliveries": "Recent deliveries",
    "channels.deliveries_hint": "Newest first. Only the status is kept, never the message body.",
    "channels.none": "No deliveries yet.",
    "channels.badge.enabled": "Enabled",
    "channels.badge.disabled": "Disabled",
    "channels.badge.unconfigured": "Not configured",
    "channels.saved": "Channel saved.",
    "channels.test_sent": "Test push delivered.",
    "channels.test_failed": "Test push failed: {detail}",
    "channels.save_failed": "The channel could not be saved.",
    "channels.device_key_required": "Enter a device key before enabling notifications.",
    "channels.invalid": "Check the server URL, device key, and urgency.",
    "channels.testing": "Sending test…",
    "channels.saving": "Saving…",
    "channels.status.delivered": "Delivered",
    "channels.status.failed": "Failed",
    "channels.kind.test": "Test",
    "channels.kind.run.pushed": "Run pushed",
    "channels.kind.run.failed": "Run failed",

    // Event hooks
    "nav.hooks": "Event hooks",
    "hooks.kicker": "Triggers",
    "hooks.title": "Event hooks",
    "hooks.lead": "Folio does not poll. Any external watcher posts a signed event here and Folio starts one maintenance run. Each hook has its own secret; unsigned, stale, or replayed requests are refused.",
    "hooks.name": "Hook name",
    "hooks.name_placeholder": "Context repository updated",
    "hooks.create": "Create hook",
    "hooks.priority": "Priority",
    "hooks.action": "Task kind",
    "hooks.creating": "Creating…",
    "hooks.list": "Configured hooks",
    "hooks.list_hint": "Disable a hook to refuse its calls without deleting it.",
    "hooks.empty": "No hooks yet. Create one to get a signing secret.",
    "hooks.enabled": "Enabled",
    "hooks.disabled": "Disabled",
    "hooks.enable": "Enable",
    "hooks.disable": "Disable",
    "hooks.rotate": "Rotate secret",
    "hooks.delete": "Delete",
    "hooks.rotating": "Rotating…",
    "hooks.never": "Never called",
    "hooks.calls": "{count} calls",
    "hooks.last_called": "Last call",
    "hooks.secret_title": "Signing secret",
    "hooks.secret_once": "Copy it now: it is shown once and never returned again.",
    "hooks.secret_hint": "Sign the timestamp, a dot, and the exact raw body with HMAC-SHA256 and this secret; send the hex digest as X-Folio-Signature.",
    "hooks.created": "Hook created. Copy its secret now.",
    "hooks.rotated": "Secret rotated. The old secret no longer works.",
    "hooks.updated": "Hook updated.",
    "hooks.deleted": "Hook deleted.",
    "hooks.confirm_delete": "Delete this hook? Its secret stops working immediately.",
    "hooks.invalid_name": "Give the hook a name of 64 characters or fewer.",
    "hooks.limit": "Hook limit reached. Delete one first.",
    "hooks.not_found": "That hook no longer exists.",
    "hooks.failed": "The hook operation failed.",
    "common.copy": "Copy",
    "nav.tasks": "Tasks",
    "tasks.kicker": "Queue",
    "tasks.title": "Task board",
    "tasks.lead": "An event creates a task, not a run. Pending tasks start one at a time, highest priority first, and a deferrable priority waits for the off-peak window instead of spending tokens when you are working. Pausing never drops work.",
    "tasks.pause": "Pause queue",
    "tasks.resume": "Resume queue",
    "tasks.policy_inside": "Off-peak window {start}–{end} · currently open, so deferred work may start.",
    "tasks.policy_outside": "Off-peak window {start}–{end} · currently closed, so deferred work waits.",
    "tasks.empty": "No tasks yet. A hook event or a manual run creates one.",
    "tasks.created": "Created {time}",
    "tasks.not_before": "starts after {time}",
    "tasks.attempts": "attempt {count}",
    "tasks.action.pause": "Hold",
    "tasks.action.resume": "Release",
    "tasks.action.run": "Run now",
    "tasks.action.cancel": "Cancel",
    "tasks.confirm_cancel": "Cancel this task? It will not run.",
    "tasks.status.queued": "Waiting",
    "tasks.status.running": "Running",
    "tasks.status.done": "Done",
    "tasks.status.failed": "Failed",
    "tasks.status.canceled": "Canceled",
    "tasks.priority.defer": "off-peak",
    "tasks.priority.now": "immediate",
    "tasks.failed": "The task operation failed.",
    "tasks.not_found": "That task no longer exists.",
    "tasks.busy": "A task that is running cannot be changed.",
    "tasks.canceled_error": "That task was canceled.",
    "nav.usage": "Usage",
    "usage.kicker": "Usage",
    "usage.title": "Token usage",
    "usage.lead": "Every number below was reported by the runner itself. A run that called no model has no usage at all, and a run whose runner reported nothing stays unknown — it is never estimated.",
    "usage.window": "Window",
    "usage.days7": "Last 7 days",
    "usage.days30": "Last 30 days",
    "usage.days90": "Last 90 days",
    "usage.total": "Total tokens",
    "usage.input": "Input (uncached)",
    "usage.output": "Output",
    "usage.runs": "Runs",
    "usage.note": "{reported} of {runs} runs reported usage; {missing} did not.",
    "usage.empty": "No runs in this window.",
    "usage.model": "Runner / model",
    "usage.col_total": "Total",
    "usage.col_input": "Input",
    "usage.col_cached": "Cached",
    "usage.col_output": "Output",
    "usage.col_runs": "Runs",
    "usage.failed": "Usage data is unavailable.",

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
    "status.online": "Folio 已登录",
    "status.offline": "Folio 未登录",
    "status.checking": "检查中…",
    "status.unknown": "未知",
    "lang.aria": "语言",
    "auth.logout": "退出 Folio",

    // Login
    "login.eyebrow": "本地所有者",
    "login.title": "登录 Folio",
    "login.lead": "此控制台仅在本机开放，请使用私有数据卷中的管理员密码登录。",
    "login.password": "管理员密码",
    "login.placeholder": "输入管理员密码",
    "login.submit": "登录 Folio",
    "login.submitting": "正在登录…",
    "login.hint": "密码保存在服务数据卷的 secrets/admin-password。",
    "login.required": "请输入管理员密码。",
    "login.failed": "登录失败，请检查密码后重试。",
    "login.rate_limited": "尝试次数过多，请稍后再试。",
    "login.unavailable": "服务暂时不可用。",
    "login.expired": "会话已过期，请重新登录。",

    // Navigation / state
    "nav.aria": "页面分区",
    "nav.repository": "仓库",
    "nav.automation": "自动化",
    "state.title": "当前状态",
    "state.repository": "仓库",
    "state.branch": "分支",
    "state.head": "提交",
    "state.model": "Codex",
    "state.scheduler": "调度器",
    "state.git_credential": "仓库授权",
    "state.credential_token": "已配置部署令牌",
    "state.credential_gh": "GitHub 已登录",
    "state.credential_missing": "需要授权",
    "state.available": "可用",
    "state.unavailable": "不可用",
    "state.enabled": "已启用",
    "state.disabled": "已禁用",
    "state.last_run": "上次运行",

    // GitHub

    // Repository
    "repo.kicker": "远端",
    "repo.title": "仓库",
    "repo.lead": "连接私有 Markdown 仓库，检出内容只保存在私有数据卷中。",
    "repo.current": "已选仓库",
    "repo.private": "私有",
    "repo.connected": "已选择仓库",
    "repo.not_connected": "未选择仓库",
    "repo.auth_required": "仓库访问待授权",
    "repo.branch": "分支",
    "repo.head": "提交",
    "repo.sync": "同步仓库",
    "repo.syncing": "正在同步…",
    "repo.synced": "仓库已同步。",
    "repo.sync_failed": "同步失败，请查看技术详情。",
    "repo.none": "未选择仓库",

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
    "automation.model": "模型",
    "automation.model_placeholder": "运行器默认模型",
    "automation.model_default": "运行器默认",
    "automation.model_toggle": "显示模型建议",
    "automation.model_none": "暂无建议，可直接输入模型 ID。",
    "automation.run_now": "立即运行",
    "automation.runner_hint": "可选择建议或直接输入任意模型 ID：Codex 建议来自已安装目录，DSH 建议来自当前配置。留空使用运行器默认模型；定时运行使用 Codex，立即运行可使用 Codex 或 DSH。",
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
    "automation.input": "输入摘要",
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
    "automation.reason.checkout_remote_mismatch": "本地与远端提交不一致。请先审查并处理未推送的提交，再重新运行。",
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
    "automation.status.reported": "报告已生成",
    "automation.report_title": "报告",
    "automation.needs_review": "需要你确认",
    "automation.remaining": "还有 {count} 份变更文档等待后续任务处理。",
    "automation.finding.document_too_large": "文档超出完整输入上限，需要人工检查或拆分。",
    "automation.finding.sensitive_content_excluded": "可能包含敏感信息，已从模型输入中排除。",
    "automation.finding.candidate_requires_owner_review": "候选保持待审，自动整理不会批准或转正。",
    "automation.finding.stale_context_requires_evidence": "需要核实事实，不能仅因整理就刷新复核日期。",
    "automation.finding.broken_link_requires_review": "文档链接失效，需要有明确依据的目标才能修复。",
    "automation.finding.parent_unavailable_requires_review": "声明的父级文档不存在或无法参与整理。",
    "automation.finding.hierarchy_cycle_requires_review": "文档层级存在循环，需要检查父级元数据。",
    "automation.finding.related_context_over_budget": "相关上下文超出本批容量，需要检查父级摘要是否同步。",
    "automation.reason.maintenance_input_invalid": "无法安全读取 context 输入或整理进度。",
    "automation.status.unknown": "未知",
    "automation.trigger.scheduler": "定时",
    "automation.trigger.manual": "手动",
    "automation.trigger.hook": "事件触发",
    "automation.trigger.task": "任务队列",

    // 推送通知
    "nav.channels": "通知",
    "channels.kicker": "投递",
    "channels.title": "推送通知",
    "channels.lead": "Folio 只向外发送：维护完成或失败时，会向你在这里配置的渠道推送一条短消息。不会接收任何回传。",
    "channels.enabled": "运行结束后推送",
    "channels.server": "Bark 服务器",
    "channels.server_hint": "留空使用公共服务。只有你自己的内网设备才允许使用明文 HTTP。",
    "channels.device_key": "设备密钥",
    "channels.device_key_hint": "保存在私有卷中，永远不会返回给浏览器。",
    "channels.device_key_stored": "已保存设备密钥。留空表示保持不变。",
    "channels.device_key_missing": "尚未保存设备密钥。",
    "channels.group": "分组",
    "channels.level": "提醒级别",
    "channels.level.passive": "静默",
    "channels.level.active": "正常",
    "channels.level.time_sensitive": "时效",
    "channels.language": "消息语言",
    "channels.save": "保存渠道",
    "channels.test": "发送测试",
    "channels.deliveries": "最近投递",
    "channels.deliveries_hint": "按时间倒序。只记录状态，不保存消息正文。",
    "channels.none": "暂无投递记录。",
    "channels.badge.enabled": "已启用",
    "channels.badge.disabled": "已停用",
    "channels.badge.unconfigured": "未配置",
    "channels.saved": "渠道已保存。",
    "channels.test_sent": "测试推送已送达。",
    "channels.test_failed": "测试推送失败：{detail}",
    "channels.save_failed": "渠道保存失败。",
    "channels.device_key_required": "启用通知前请先填写设备密钥。",
    "channels.invalid": "请检查服务器地址、设备密钥和提醒级别。",
    "channels.testing": "正在发送测试…",
    "channels.saving": "正在保存…",
    "channels.status.delivered": "已送达",
    "channels.status.failed": "失败",
    "channels.kind.test": "测试",
    "channels.kind.run.pushed": "运行已推送",
    "channels.kind.run.failed": "运行失败",

    // 事件触发
    "nav.hooks": "事件触发",
    "hooks.kicker": "触发",
    "hooks.title": "事件触发",
    "hooks.lead": "Folio 不做轮询。任何外部巡检——脚本、CI、另一台机器——向这里投递一条签名事件，Folio 才开始一次维护运行。每个 hook 有独立密钥；未签名、过期或重放的请求一律拒绝。",
    "hooks.name": "触发名称",
    "hooks.name_placeholder": "上下文仓库已更新",
    "hooks.create": "创建触发",
    "hooks.priority": "优先级",
    "hooks.action": "任务类型",
    "hooks.creating": "正在创建…",
    "hooks.list": "已配置触发",
    "hooks.list_hint": "停用后拒绝调用，但不会删除。",
    "hooks.empty": "还没有触发。创建一个即可获得签名密钥。",
    "hooks.enabled": "已启用",
    "hooks.disabled": "已停用",
    "hooks.enable": "启用",
    "hooks.disable": "停用",
    "hooks.rotate": "轮换密钥",
    "hooks.delete": "删除",
    "hooks.rotating": "正在轮换…",
    "hooks.never": "从未调用",
    "hooks.calls": "调用 {count} 次",
    "hooks.last_called": "最近调用",
    "hooks.secret_title": "签名密钥",
    "hooks.secret_once": "请立即复制：只显示这一次，之后不再返回。",
    "hooks.secret_hint": "用该密钥对「时间戳 + 点号 + 原始请求体」做 HMAC-SHA256，把十六进制摘要放进 X-Folio-Signature。",
    "hooks.created": "触发已创建，请立即复制密钥。",
    "hooks.rotated": "密钥已轮换，旧密钥立即失效。",
    "hooks.updated": "触发已更新。",
    "hooks.deleted": "触发已删除。",
    "hooks.confirm_delete": "确定删除该触发？其密钥会立即失效。",
    "hooks.invalid_name": "名称不能为空且不超过 64 个字符。",
    "hooks.limit": "触发数量已达上限，请先删除一个。",
    "hooks.not_found": "该触发已不存在。",
    "hooks.failed": "触发操作失败。",
    "common.copy": "复制",
    "nav.tasks": "任务",
    "tasks.kicker": "队列",
    "tasks.title": "任务看板",
    "tasks.lead": "事件创建的是任务，不是运行。待办任务按优先级一次只跑一个；可延后的优先级会等到闲时窗口，而不是在你干活时消耗 token。暂停不会丢任务。",
    "tasks.pause": "暂停队列",
    "tasks.resume": "恢复队列",
    "tasks.policy_inside": "闲时窗口 {start}–{end} · 当前已开启，延后任务可以启动。",
    "tasks.policy_outside": "闲时窗口 {start}–{end} · 当前关闭，延后任务在等待。",
    "tasks.empty": "还没有任务。hook 事件或手动运行会创建任务。",
    "tasks.created": "创建于 {time}",
    "tasks.not_before": "{time} 之后可执行",
    "tasks.attempts": "第 {count} 次尝试",
    "tasks.action.pause": "挂起",
    "tasks.action.resume": "恢复",
    "tasks.action.run": "立即执行",
    "tasks.action.cancel": "取消",
    "tasks.confirm_cancel": "确定取消该任务？它不会被执行。",
    "tasks.status.queued": "等待中",
    "tasks.status.running": "执行中",
    "tasks.status.done": "已完成",
    "tasks.status.failed": "失败",
    "tasks.status.canceled": "已取消",
    "tasks.priority.defer": "闲时",
    "tasks.priority.now": "立即",
    "tasks.failed": "任务操作失败。",
    "tasks.not_found": "该任务已不存在。",
    "tasks.busy": "正在执行的任务不能修改。",
    "tasks.canceled_error": "该任务已取消。",
    "nav.usage": "用量",
    "usage.kicker": "用量",
    "usage.title": "Token 用量",
    "usage.lead": "这里每个数字都来自运行器的自报。没调用模型的运行没有用量；运行器未上报的保持未知——绝不用估算填成 0。",
    "usage.window": "时间范围",
    "usage.days7": "近 7 天",
    "usage.days30": "近 30 天",
    "usage.days90": "近 90 天",
    "usage.total": "总 token",
    "usage.input": "输入（未缓存）",
    "usage.output": "输出",
    "usage.runs": "运行次数",
    "usage.note": "{runs} 次运行中有 {reported} 次上报了用量，{missing} 次没有。",
    "usage.empty": "该时间范围内没有运行记录。",
    "usage.model": "运行器 / 模型",
    "usage.col_total": "合计",
    "usage.col_input": "输入",
    "usage.col_cached": "缓存",
    "usage.col_output": "输出",
    "usage.col_runs": "次数",
    "usage.failed": "用量数据不可用。",

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
