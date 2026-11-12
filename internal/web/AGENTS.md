# HTTP API and plugin host

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- The approved Main pi SDK / ACP task architecture in
  `../../docs/agent-architecture.md` is not implemented. Future API migration must
  separate Main model/session configuration from ACP task harness profiles and
  expose real task receipts, waiting/approval revisions and artifact/delivery
  evidence. Keep current APIs below accurate; do not invent Host/ACP endpoints.
  Normal Main file/shell/skill access does not widen feedback/observer grants.
- `web.go` owns HTTP routing, authentication, sessions, CSRF/host/origin checks,
  request/response limits and background dispatch integration. Depend on the
  `Backend` contract; repository and model operations remain in store/worker.
- `plugins.go` mounts compiled plugin routes and assets under their plugin ID.
  The host owns session authentication, CSRF, JSON encoding, error mapping,
  redaction, and the safe executor supplied to plugin handlers.
- Keep browser mutations behind session/CSRF checks. Signed inbound hooks are
  a separate authentication path; preserve host checks, raw-body signature
  semantics, body bounds and rejection before side effects.
- Auth is a single owner account (username + PBKDF2 password) in
  `/data/folio.db`. First-run setup is available only from loopback/private
  addresses; there is no default or generated password and no setup code. The
  DB stores only salt/digest; legacy plaintext/hash files are imported once and
  removed. Sessions are durable rows (30-day idle, 90-day absolute, sliding),
  with hashed tokens and CSRF checks; logout removes the row. Keep tokens,
  passwords and the SQLite file out of responses and logs.
- Never expose raw process errors, credential files or arbitrary runtime files.
  Static assets must come only from their configured bounded asset roots.
- API changes require matching `Backend` implementations, fake backends, UI
  consumers, and contract documentation. Task acceptance is not run completion.
  Update these instructions whenever API ownership or security rules change.
- The optional `AgentBackend` contract serves session/CSRF-protected
  `POST /api/agent/turns` (`text/event-stream`) and session-protected
  `GET /api/agent/sessions/{id}`. The store checks the event route and runtime
  availability first; events carry bounded text/tool status, never raw runtime
  errors or secrets. A disconnected request cancels the turn. The optional
  `AgentConfigBackend` adds session-protected `GET /api/agent/config` and
  CSRF-protected `POST /api/agent/config`; responses may expose availability,
  kind, provider, base URL, model and key presence, never the key or key path.
  Map validation failures to fixed `agent_config_invalid`/`agent_key_invalid`
  codes.
- The optional `ModelServicesBackend` serves session-protected
  `GET /api/model-services` and CSRF-protected `POST /api/model-services` and
  `POST /api/model-services/test`. Store owns reusable connections, write-only
  keys, default assistant/report/maintenance bindings and per-report/action
  overrides. Return safe metadata and key presence only. Test checks a bounded
  model catalog without paid inference; never report it as inference acceptance.
  Map configuration and upstream failures to fixed `model_services_*` or
  `model_service_*` codes, never upstream text. Legacy agent/runner APIs remain
  compatible; the daily UI uses task bindings instead of model overrides.
- The optional `PricingBackend` serves session-protected `GET /api/pricing` and
  CSRF-protected `POST /api/pricing/refresh`. Status is bounded metadata;
  refresh is rate limited and returns 429 with `Retry-After`, otherwise 200 with
  a fixed error code and the last good catalog.
- The optional `ActionBackend` contract serves session-protected
  `GET /api/actions` and `GET /api/actions/versions`, and CSRF-protected
  `POST /api/actions`, `POST /api/actions/validate`, `POST /api/actions/restore`,
  `POST /api/actions/hosts` and `POST /api/actions/{id}/disable`. Definitions
  are owner configuration:
  validate the whole definition, snapshot before writing, and map failures to
  fixed `action_*` codes. Responses may carry action definitions, never action
  secrets or arbitrary runtime files.
- The optional `ActionCatalogBackend` serves session-protected
  `GET /api/catalog` (`{"actions":[...]}` sorted by group/order/label) and the
  separate optional `ActionRunBackend` serves CSRF-protected
  `POST /api/actions/{id}/run` with `{"fields":{...}}`. Catalog entries carry
  only ID, label/optional i18n keys, description, priority, enabled, group,
  order, submit label, validated field descriptors, requires and legacy flag —
  never steps, prompts, hosts or secrets. Run creates durable task intent and
  returns `{task,created}` with 201/200; unknown/disabled/invalid map to 404
  `action_unknown`, 409 `action_disabled`, 400 `action_input_invalid`. Route the
  run verb through `actionAdmin` before the generic `/api/actions` POST switch.
  A configured action requiring the unavailable `workshop` capability is also
  rejected for task creation and hook creation (`invalid_hook_action`); an event
  target resolving to it fails closed. Presentation fields are validated for
  owner launches and for hook/event actions that declare fields.
- The optional `FeedbackBackend` serves session-protected `GET /api/feedback`,
  `GET /api/feedback/{id}` and `GET /api/feedback/{id}/image`, CSRF-protected
  multipart `POST /api/feedback`, and CSRF-protected JSON
  `POST /api/feedback/{id}/answers` (`{"answers":[{"question_id","option_id","text"}]}`).
  The multipart branch must run after
  `requireSession`/CSRF but before `requestBody` JSON parsing; bound the total
  body with `MaxBytesReader` at 8 MiB plus a bounded 1 MiB multipart-envelope
  allowance, while the image part itself stays capped at 8 MiB. Create returns 201
  `{accepted:true,feedback}` with no model call on the submission path; feedback
  is `intake_pending`; the assessment is always asynchronous. `accepted` appears
  only after a successful main-agent verdict and plan creation. A backend's `{accepted:false,feedback}` is still passed
  through unchanged. Answers return the updated feedback (200); invalid answer
  sets are 400 `feedback_invalid`/`answers_invalid`, a non-pending record is 409
  `feedback_not_awaiting_answers`, and an unknown id is 404 `feedback_unknown`. Old
  records may still carry legacy `gate_usage`/`gate_advisory` fields, never raw
  model text. Malformed input returns 400
  `feedback_invalid`, with a fixed `reason` such as `title_short`/`image_invalid`
  when available. An optional multipart `context` field carries the store's
  bounded page-context JSON (<=24 KiB UTF-8 object; no HTML, input values or
  arbitrary DOM). The store strictly decodes it and maps invalid, duplicate,
  unknown-key or oversized JSON to 400 `feedback_invalid` with reason
  `context_invalid`; authenticated detail includes the sanitized context, list
  summaries expose only `element_count`, and submissions without context remain
  valid. Context bounds (<=24 elements, fixed view enum, route <=200 runes,
  viewport 1..10000 with dpr 0.5..4, bounded element labels/selector/section and
  rect values 0..100000) live in store validation. Only validated context is
  summarized into the bounded intake/plan prompt.
  Image responses are ID-only PNG bytes with `no-store`,
  `nosniff` and inline disposition; only `has_image` records serve them. Never
  accept caller paths. Acceptance queues the reserved `feedback-intake` task
  asynchronously. Only the scoped main-agent verdict may create the reserved
  `feedback-plan` task; both use the dedicated `feedback` store source, and
  manual, agent, hook, event and pipeline callers cannot create either kind
  directly. A failed assessment returns `intake_failed` with a fixed reason and
  no task; `POST /api/feedback/{id}/retry` re-queues it (409 when not
  retryable).
- The optional `QuotaBackend` contract exposes session-protected
  `GET /api/quotas` and CSRF-protected `POST /api/quotas/refresh`. Responses are
  bounded maps, never raw magpie bodies, key paths, or credentials. A
  `quota_refresh_limited` refresh returns 429 with `Retry-After`; other source
  failures stay 200 with a fixed error code and stale cards when available.
- The optional `WeixinBackend` contract serves session-protected
  `GET /api/weixin`, `GET /api/weixin/login` and CSRF-protected
  `POST /api/weixin/{login,login/code,login/cancel,confirm,enabled}`. The process
  supervisor implements it; the store does not. Responses carry fixed state and
  a rendered PNG data URL only, never the raw QR payload, tokens, cursors or
  transport wording. Errors map to stable `weixin_*` codes: 409 for conflicts,
  403 for the deployment kill switch, 500 for private-state faults.
- The optional `WeixinPushBackend` contract serves CSRF-protected
  `POST /api/channels/weixin` (`{"enabled": bool}`) and
  `POST /api/channels/weixin/test`. It is separate from `ChannelBackend` so a
  store without a WeChat transport still satisfies the push-channel contract;
  a missing transport answers 503 `weixin_unavailable`. Responses are bounded
  maps and delivery receipts, never tokens or transport text.
- Maintenance run details add `input_files` (original byte fingerprints),
  `remaining` (unprocessed changed document count) and `needs_review`
  (`source`, `reason`). These are private authenticated audit data. Consumers
  must tolerate their absence on old runs and report runs; findings are not
  approvals and a successful structural edit does not resolve a factual conflict.
- Session/CSRF-protected `POST /api/tasks` accepts `kind`, optional `title`,
  `priority`, and bounded flat `payload`. The server assigns `source: manual`;
  caller source values never select trust. Configured actions use the same
  validated owner-launch path as catalog run (`requires`, unknown/required
  fields, types, select options, rune limits, required-boolean true). Hook/event
  task creation also validates declared fields when the action has any, while
  fieldless actions keep their trusted payload; pipeline `action:*` task steps
  remain exempt. Return `{task, created}` with 201 for new intent or 200 for
  deduplication. A report's owner `payload.request` permits at most 4000 runes;
  excess/type errors return 400 `report_request_invalid`. A configured action's
  `action_disabled` returns 409, `action_input_invalid` returns 400, and other
  invalid task input keeps 400 `task_invalid`. The reserved `feedback-intake`
  and `feedback-plan` kinds are not accepted here; direct creation returns 400
  `task_invalid`.
- `/api/state.capabilities` separates assistant/report/maintenance/context
  configuration readiness (`available`, `reason`, `verification: unknown`) and
  advertises `workshop` as unavailable (`workshop_unavailable`) until a later
  phase. Readiness is never an upstream authentication or inference verification.
  Standalone assistant/report do not require repository connection, and the
  workshop card must not change setup/default-route decisions.
- Optional `ReportResultBackend` serves session-protected
  `GET /api/automation/runs/{run_id}/result`: `run_id`, optional `task_id`,
  `format: markdown`, and bounded `text`. The store owns filename association,
  regular-file, size, symlink and content checks; failures return fixed 404
  `report_result_unavailable`. No caller-supplied path is accepted. Historical
  mutable task-named files use only run audit text, marked
  `historical_snapshot: true`, `truncated: true`; consumers label it as a partial
  historical snapshot rather than an exact artifact.
- Agent SSE passes through response redaction and adds `task.created` events
  with `task_id`, `title`, `status`. History responses add a `tasks` array of
  those associations reconstructed from retained durable task records; clients
  must tolerate empty history/associations. Task acceptance is not completion.
  Assistant `task.create` uses the store's validated owner path, so a rejected
  configured action emits a failed tool result and no `task.created` event.
- Queue-created maintenance and report runs include `task_id` from their first
  running audit record. Consumers can correlate a running task with its current
  attempt before the task's final `run_id` is saved; historical runs may omit it.
- Maintenance run `repository` snapshots contain only `plugin`, `full_name` and
  `branch`. Pass through automation listing's optional `review_run` unchanged
  under the existing authenticated redaction boundary. It is a complete snapshot
  from the most recent retained successful maintenance with known findings,
  independent of `latest`; it is not a live approval queue. Older runs may lack
  repository identity, and an unknown review snapshot must stay unknown.

Run `go test -race ./internal/web` from the repository root and the root Go
checks. Cover affected auth/CSRF/host/origin rejection, limits, redaction and
plugin routes; for changed browser flows also follow `../../web/AGENTS.md`.
