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
    "review.what_next": "How to handle this finding",
    "automation.reason.runner_network": "Could not reach the model provider. Check the network and retry.",
    "automation.reason.runner_authentication": "Model provider authentication failed. Check runner and provider credentials on the host.",
    "automation.reason.runner_timeout": "The model request timed out. Inspect details and retry.",
    "automation.reason.runner_rate_limited": "The model provider rate limited this request. Retry later.",
    "automation.reason.runner_output_schema": "The model output did not match the required format.",
    "automation.reason.runner_runner_exit": "The runner exited unexpectedly. Check its host configuration.",
    "automation.reason.runner_model_unavailable": "The selected model is unavailable. Check the model ID and provider setup.",
    "automation.reason.dispatch_failed": "Task dispatch failed. Retry or inspect service logs.",
    "automation.reason.interrupted": "A service restart interrupted this task. Retry it.",
    "automation.reason.kind_unknown": "This task kind is unavailable. Check event trigger settings.",
    "board.previous_attempt": "The previous attempt is shown below",
    "board.starting_attempt": "The task is starting and its current run record is not ready. Reopen shortly for progress.",
    "review.snapshot_failed": "Could not refresh findings. Any findings shown are the last successfully loaded snapshot.",
    "board.run_options": "Runner / model: {runner} · {model}",
    "board.search": "Search tasks",
    "board.search_placeholder": "Title, source or reason",
    "board.priority": "Priority",
    "board.all_priorities": "All priorities",
    "board.usage_details": "Usage by day and model",
    "board.detail": "Task run details",
    "board.close": "Close",
    "board.held": "This task is on hold. Resume it to allow execution.",
    "board.queue_paused": "The queue is paused. Resume it to allow execution.",
    "board.off_peak": "Waiting for the off-peak window. You can run it now.",
    "board.ready": "Queued, waiting for the dispatcher and preceding work.",
    "board.failed_hint": "Run failed. Open details for the reason.",
    "board.finished": "Finished. Open details for the result.",
    "board.continuation": "Continue organizing remaining documents",
    "board.direct_run": "Most recent run",
    "board.summary": "Showing {shown} of {total} task/run records; human review is identified separately.",
    "board.column.queued": "Queued",
    "board.column.running": "Running",
    "board.column.attention": "Needs attention",
    "board.column.done": "Finished",
    "board.empty.queued": "No queued tasks",
    "board.empty.running": "Nothing running now",
    "board.empty.attention": "No recorded failures or review findings",
    "board.empty.done": "Finished tasks appear here",
    "board.loading": "Loading tasks…",
    "board.review_title": "{count} findings need human review",
    "board.review_note": "Snapshot from successful maintenance at {time}. Open sources and next steps.",
    "board.no_attempt": "This task has no run record yet. Once it starts, details show the runner, token usage and changes.",
    "board.run_tokens": "Tokens this run",
    "board.no_usage": "No usage recorded",
    "review.current_only": "This older record has no repository identity. The link below opens the currently selected repository; verify the file belongs to this finding.",
    "nav.dashboard": "Dashboard",
    "nav.runs": "Run history",
    "nav.review": "Owner review",
    "nav.settings": "Settings",
    "dashboard.kicker": "Overview",
    "dashboard.title": "Tasks and usage",
    "dashboard.results": "View run results",
    "dashboard.back": "Back to dashboard",
    "setup.kicker": "Connections and preferences",
    "setup.lead": "Connect a repository and check your runner before starting. Notifications and event triggers are optional and can be changed later.",
    "setup.steps": "Setup steps",
    "setup.repository": "1 · GitHub and repository",
    "setup.runner": "2 · Model runner",
    "setup.notifications": "3 · Notifications",
    "setup.triggers": "4 · Event triggers",
    "setup.optional": "Optional",
    "setup.authorize": "Authorization needed",
    "setup.selected": "Selected",
    "setup.select": "Select a repository",
    "setup.installed": "Installed",
    "setup.required": "Setup required",
    "setup.runner_available": "An installed runner is available. Choose your runner and model on the dashboard.",
    "setup.runner_missing": "No runner is available. Install and configure one on the service host first.",
    "setup.runner_hint": "Configure runners and provider credentials on the host. Installed does not mean signed in to the provider; the first run checks the connection.",
    "setup.finish_title": "Start daily use",
    "setup.finish_hint": "Set up notifications and triggers later. Open the dashboard for tasks and token usage.",
    "setup.finish": "Go to dashboard",
    "setup.alert": "Authorize and select a repository and configure a runner before starting maintenance.",
    "setup.open": "Open settings",
    "review.kicker": "Human decisions",
    "review.how_title": "How to handle a finding",
    "review.step1": "Open the source. Read the observation, proposed claim and evidence; decide whether it is accurate and worth keeping.",
    "review.step2": "Record your decision and reason in the repository. If accepted, explicitly update the canonical document; changing a candidate status alone does not publish it. Record a reason when declining or deferring.",
    "review.step3": "Commit and push your changes, then recheck here. The check pulls the latest content and may call the selected model and use tokens.",
    "review.boundary": "Review happens in your repository. Folio currently has no in-page approval or promotion; unhandled candidates remain pending.",
    "review.recheck": "Pull latest and recheck",
    "review.model": "Choose runner / model",
    "review.runner": "Check with: {runner} · {model}",
    "review.snapshot": "Findings at the last successful maintenance",
    "review.as_of": "Recorded {time} · {count} findings. This is a snapshot; recheck to reflect later repository changes.",
    "review.no_snapshot": "No maintenance review snapshot is available.",
    "review.no_snapshot_hint": "Run maintenance first. Report tasks and failed runs do not clear previous findings.",
    "review.empty": "This maintenance recorded no review findings.",
    "review.notice": "Last successful maintenance recorded {count} findings · Review sources and next steps →",
    "review.candidate_next": "After checking the evidence, set frontmatter status to approved or rejected and record your reason in Review. Keep pending-review when deferring. Adoption still requires an explicit canonical edit by you.",
    "review.finding_next": "Next: verify and fix this issue in the repository, commit, then recheck.",
    "review.open_current": "View / handle in current repository",
    "review.open_input": "View source at run time",
    "review.no_link": "This older record has no repository identity. Use the file path to locate it in its repository.",
    "review.open_guide": "View review steps (this run remains a historical record)",
    "review.continuation_queued": "{count} changed documents await automatic maintenance in a follow-up task. They need no approval; queue pause and off-peak policies affect when it starts.",
    "review.continuation_unqueued": "{count} changed documents remain. A follow-up task was not confirmed for this run. Check the board and run maintenance manually if needed.",
    "review.queue_link": "View task queue",
    "review.checking": "Pulling and checking. Please wait.",
    "review.checked": "Check finished. Findings below reflect the last successful maintenance.",
    "review.check_failed": "Check did not succeed. Previous findings are retained. Open run history for the reason.",
    "run.model_summary": "Original model summary",
    "run.outcome": "This run changed {count} documents. Review findings and remaining work are shown below.",
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
    "automation.title": "Run results",
    "automation.lead": "Inspect what changed, why a run failed, and what needs your decision.",
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
    "automation.run_now": "Start maintenance",
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
    "automation.finding.candidate_requires_owner_review": "You have not yet decided whether to adopt this observation.",
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
    "tasks.lead": "Start maintenance or follow queued work here. Tasks run one at a time; pause the queue or run a waiting task now.",
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
    "usage.lead": "Recorded model usage for the selected period. Missing reports stay unknown.",
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

    // Subscription quota
    "nav.quotas": "Subscriptions",
    "quota.kicker": "Subscriptions",
    "quota.title": "Subscription quota",
    "quota.lead": "Read-only readings from the configured quota source. Folio shows what the source last reported and never estimates a cap or treats a missing reading as zero.",
    "quota.refresh": "Refresh quota",
    "quota.refreshing": "Refreshing quota…",
    "quota.loading": "Loading subscription quota…",
    "quota.failed": "Subscription quota is unavailable.",
    "quota.unconfigured": "No quota source is configured.",
    "quota.empty": "No subscription quota readings are available.",
    "quota.stale": "Showing the last successful reading.",
    "quota.stale_badge": "Stale",
    "quota.source": "Source: {source}",
    "quota.updated": "Updated {time}",
    "quota.kind.subscription": "Subscription",
    "quota.kind.plan": "Plan",
    "quota.kind.balance": "Balance",
    "quota.window.used": "Used {percent}%",
    "quota.window.remaining": "Remaining {percent}%",
    "quota.window.reset_in": "Resets {time}",
    "quota.window.reset_passed": "Reset time passed",
    "quota.window.no_reset": "No reset time",
    "quota.window.unlimited": "Unlimited",
    "quota.window.no_reading": "No numeric reading",
    "quota.window.balance": "Balance: {amount}",
    "quota.window.amount": "Amount: {amount}",
    "quota.read_at": "Read {time}",
    "quota.read_unknown": "Read time unknown",
    "quota.card_error": "Quota error: {detail}",
    "quota.card_balance": "Balance: {amount}",
    "quota.refresh_limited": "Refresh is limited. Try again in {seconds}s.",
    "quota.retry_after": "Try again in {seconds}s.",
    "quota.error.quota_invalid": "The quota response was invalid.",
    "quota.error.quota_key_unavailable": "The quota key is not available to Folio.",
    "quota.error.quota_unauthorized": "The quota source rejected the request.",
    "quota.error.quota_unavailable": "The quota source is unreachable.",
    "quota.error.quota_invalid_response": "The quota source returned an invalid response.",
    "quota.error.quota_rate_limited": "The quota source is rate limiting requests.",
    "quota.error.quota_refresh_limited": "Refresh is temporarily limited.",

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
    "review.what_next": "这项具体怎么处理？",
    "automation.reason.runner_network": "模型服务网络连接失败，请检查网络后重试。",
    "automation.reason.runner_authentication": "模型服务授权失败，请检查主机上的运行器与服务凭据。",
    "automation.reason.runner_timeout": "模型调用超时，请查看详情后重试。",
    "automation.reason.runner_rate_limited": "模型服务限制了请求频率，请稍后重试。",
    "automation.reason.runner_output_schema": "模型输出格式不符合要求，本次修改未通过。",
    "automation.reason.runner_runner_exit": "运行器异常退出，请检查主机上的运行器配置。",
    "automation.reason.runner_model_unavailable": "所选模型不可用，请检查模型名称与服务配置。",
    "automation.reason.dispatch_failed": "任务未能完成调度，请重试或检查服务日志。",
    "automation.reason.interrupted": "服务重启中断了本次任务，请重试。",
    "automation.reason.kind_unknown": "任务类型已不可用，请检查事件触发设置。",
    "board.previous_attempt": "下方显示的是上一次运行记录",
    "board.starting_attempt": "任务正在启动，当前运行记录尚未就绪。稍后重新打开查看进度。",
    "review.snapshot_failed": "待审记录刷新失败；若有记录，仍显示上次成功读取的快照。",
    "board.run_options": "运行选项：{runner} · {model}",
    "board.search": "搜索任务",
    "board.search_placeholder": "标题、来源或原因",
    "board.priority": "优先级",
    "board.all_priorities": "全部优先级",
    "board.usage_details": "按日期 / 模型查看明细",
    "board.detail": "任务运行详情",
    "board.close": "关闭",
    "board.held": "此任务已暂停，恢复后才会排队执行。",
    "board.queue_paused": "队列已暂停，恢复队列后才能执行。",
    "board.off_peak": "等待空闲时段，可选择立即运行。",
    "board.ready": "已排队，等待前序任务完成。",
    "board.failed_hint": "运行失败，打开详情查看原因。",
    "board.finished": "已完成，打开详情查看结果。",
    "board.continuation": "继续整理剩余文档",
    "board.direct_run": "最近一次运行",
    "board.summary": "显示 {shown} / {total} 条任务与运行记录；人工复核单独标注。",
    "board.column.queued": "待执行",
    "board.column.running": "运行中",
    "board.column.attention": "需要关注",
    "board.column.done": "已结束",
    "board.empty.queued": "没有排队中的任务",
    "board.empty.running": "当前没有任务在运行",
    "board.empty.attention": "没有已记录的失败或人工复核项",
    "board.empty.done": "完成的任务会出现在这里",
    "board.loading": "正在加载任务…",
    "board.review_title": "{count} 项内容待人工复核",
    "board.review_note": "来自 {time} 的成功整理快照。点击查看原文与处理步骤。",
    "board.no_attempt": "此任务尚无运行记录。开始执行后，这里会显示运行器、Token 用量和修改结果。",
    "board.run_tokens": "本次 Token",
    "board.no_usage": "无用量记录",
    "review.current_only": "这条旧记录没有仓库归属信息。下方仅打开当前所选仓库，请核对文件是否对应这条待审项。",
    "nav.dashboard": "任务看板",
    "nav.runs": "运行记录",
    "nav.review": "人工复核",
    "nav.settings": "设置",
    "dashboard.kicker": "工作概览",
    "dashboard.title": "任务与用量",
    "dashboard.results": "查看运行结果",
    "dashboard.back": "返回任务看板",
    "setup.kicker": "连接与偏好",
    "setup.lead": "先连接仓库、检查模型运行器，再开始整理。推送通知和事件触发可选，之后随时能改。",
    "setup.steps": "设置步骤",
    "setup.repository": "1 · GitHub 与仓库",
    "setup.runner": "2 · 模型运行器",
    "setup.notifications": "3 · 推送通知",
    "setup.triggers": "4 · 事件触发",
    "setup.optional": "可跳过",
    "setup.authorize": "待授权",
    "setup.selected": "已选择",
    "setup.select": "待选仓库",
    "setup.installed": "已安装",
    "setup.required": "待配置",
    "setup.runner_available": "已检测到可用的运行器。可在任务看板选择运行器和模型。",
    "setup.runner_missing": "尚未检测到可用运行器，请先在服务主机完成安装与配置。",
    "setup.runner_hint": "运行器与模型服务凭据在主机上配置。「已安装」不代表模型服务已登录；首次运行会实际检查连接。",
    "setup.finish_title": "开始日常使用",
    "setup.finish_hint": "通知和触发器可以以后再设。进入看板即可查看任务与 Token 用量。",
    "setup.finish": "进入任务看板",
    "setup.alert": "运行整理前，请先完成仓库授权、选择仓库并配置运行器。",
    "setup.open": "前往设置",
    "review.kicker": "需要人工判断的内容",
    "review.how_title": "怎样处理待审内容？",
    "review.step1": "打开原文，阅读观察、拟议结论与证据，判断内容是否准确、是否值得长期保留。",
    "review.step2": "在仓库中记录审核决定和理由。采纳时，请明确修改对应的正式文档；只改候选状态不会自动转正。不采纳或暂缓时，记录原因。",
    "review.step3": "提交并推送修改后，回到这里重新检查。检查会拉取最新内容，并可能调用所选模型、消耗 Token。",
    "review.boundary": "审核在仓库中完成。Folio 目前不提供页面内批准或转正；没有处理的候选会继续保留。",
    "review.recheck": "拉取最新内容并重新检查",
    "review.model": "选择运行器 / 模型",
    "review.runner": "检查使用：{runner} · {model}",
    "review.snapshot": "最近成功整理时的待审记录",
    "review.as_of": "记录时间：{time} · {count} 项。这是当时的快照；仓库里的后续修改需重新检查才能反映。",
    "review.no_snapshot": "尚无可用的整理复核记录。",
    "review.no_snapshot_hint": "先运行一次整理。报告任务和失败的运行不会清空既有待审记录。",
    "review.empty": "这次整理没有记录待审项。",
    "review.notice": "最近成功整理记录了 {count} 项人工复核内容 · 查看原文与处理步骤 →",
    "review.candidate_next": "核对证据后，在文件头设置 status: approved（采纳）或 rejected（不采纳），并在 Review 区记录决定与理由。暂缓则保留 pending-review。采纳的结论还需你明确更新正式文档。",
    "review.finding_next": "下一步：在仓库中核对并修正这项问题，提交后重新检查。",
    "review.open_current": "在当前仓库查看 / 处理",
    "review.open_input": "查看运行时原文",
    "review.no_link": "这条旧记录没有仓库归属信息，请根据文件路径在对应仓库中查阅。",
    "review.open_guide": "查看人工复核流程（本页记录保留为历史）",
    "review.continuation_queued": "还有 {count} 份变更文档尚未整理，已加入后续任务。它们无需人工确认；开始时间受队列暂停和空闲时段设置影响。",
    "review.continuation_unqueued": "还有 {count} 份变更文档尚未整理，本次未确认排入后续任务。可到看板检查队列，必要时手动运行。",
    "review.queue_link": "查看任务队列",
    "review.checking": "正在拉取并检查，请稍候。",
    "review.checked": "检查完成，下面已更新为最近一次成功整理的记录。",
    "review.check_failed": "检查未成功，保留上次待审记录。请到运行记录查看原因。",
    "run.model_summary": "模型原始说明",
    "run.outcome": "本次自动修改 {count} 份文档。人工复核与剩余任务见下方。",
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
    "automation.title": "运行结果",
    "automation.lead": "查看本次改了什么、失败原因，以及哪些内容仍需你判断。",
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
    "automation.run_now": "开始整理",
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
    "automation.finding.candidate_requires_owner_review": "这条观察尚未由你决定是否采纳。",
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
    "tasks.lead": "在这里开始整理或查看排队进度。任务一次执行一个；你可以暂停队列，也可以让等待中的任务立即运行。",
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
    "usage.lead": "查看所选时段内模型上报的实际用量。未上报的运行单独计数，不按零计算。",
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

    // 订阅额度
    "nav.quotas": "订阅额度",
    "quota.kicker": "订阅",
    "quota.title": "订阅额度",
    "quota.lead": "这里只读展示已配置额度源的上报数据。Folio 只显示额度源最近一次读数，不会估算上限，也不会把缺失的读数当成 0。",
    "quota.refresh": "刷新额度",
    "quota.refreshing": "正在刷新额度…",
    "quota.loading": "正在加载订阅额度…",
    "quota.failed": "订阅额度数据不可用。",
    "quota.unconfigured": "尚未配置额度源。",
    "quota.empty": "暂无订阅额度读数。",
    "quota.stale": "当前显示上一次成功读取的数据。",
    "quota.stale_badge": "旧读数",
    "quota.source": "来源：{source}",
    "quota.updated": "更新于 {time}",
    "quota.kind.subscription": "订阅",
    "quota.kind.plan": "套餐",
    "quota.kind.balance": "余额",
    "quota.window.used": "已用 {percent}%",
    "quota.window.remaining": "剩余 {percent}%",
    "quota.window.reset_in": "重置：{time}",
    "quota.window.reset_passed": "重置时间已过",
    "quota.window.no_reset": "无重置时间",
    "quota.window.unlimited": "不限量",
    "quota.window.no_reading": "无数值读数",
    "quota.window.balance": "余额：{amount}",
    "quota.window.amount": "额度：{amount}",
    "quota.read_at": "读取于 {time}",
    "quota.read_unknown": "读取时间未知",
    "quota.card_error": "额度错误：{detail}",
    "quota.card_balance": "余额：{amount}",
    "quota.refresh_limited": "刷新受限，请在 {seconds} 秒后重试。",
    "quota.retry_after": "请在 {seconds} 秒后重试。",
    "quota.error.quota_invalid": "额度响应格式不合法。",
    "quota.error.quota_key_unavailable": "Folio 无法使用额度密钥。",
    "quota.error.quota_unauthorized": "额度源拒绝了请求。",
    "quota.error.quota_unavailable": "额度源不可达。",
    "quota.error.quota_invalid_response": "额度源返回了不合法的响应。",
    "quota.error.quota_rate_limited": "额度源正在限流。",
    "quota.error.quota_refresh_limited": "刷新暂时受限。",

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
