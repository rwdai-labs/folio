# HTTP API and plugin host

Inherit `../../AGENTS.md` and `../AGENTS.md`.

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
- The optional `FeedbackBackend` serves session-protected `GET /api/feedback`,
  `GET /api/feedback/{id}` and `GET /api/feedback/{id}/image`, plus CSRF-protected
  multipart `POST /api/feedback`. The multipart branch must run after
  `requireSession`/CSRF but before `requestBody` JSON parsing; bound the total
  body with `MaxBytesReader` at 8 MiB. Create returns 201
  `{accepted:true,feedback,task}` or 200 `{accepted:false,feedback}` for a
  recorded gate rejection. Records may carry bounded normalized `gate_usage`
  counters from the gate call, never raw gate text. Malformed input returns 400
  `feedback_invalid`, with a fixed `reason` such as `title_short`/`image_invalid`
  when available. Image responses are ID-only PNG bytes with `no-store`,
  `nosniff` and inline disposition; only `has_image` records serve them. Never
  accept caller paths.
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
  caller source values never select trust. Return `{task, created}` with 201 for
  new intent or 200 for deduplication. A report's owner `payload.request` permits
  at most 4000 runes; excess/type errors return 400 `report_request_invalid`.
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
