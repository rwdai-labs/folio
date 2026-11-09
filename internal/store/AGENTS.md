# Durable service state and transactions

Inherit `../../AGENTS.md` and `../AGENTS.md`.

| Files | Responsibility |
| --- | --- |
| `store.go`, `storage.go`, `executor.go`, `git.go`, `lifecycle.go` | Layout, remote integration, safe processes and repository lifecycle |
| `automation.go`, `review.go` | Validated automatic edits and owner-reviewed changes |
| `maintenance.go` | Incremental fingerprints, persistent review findings and continuation tasks |
| `tasks.go`, `kinds.go`, `hooks.go`, `actions.go`, `events.go`, `routes.go`, `action_config.go`, `action_pipeline.go` | Durable queue, data-defined actions, internal action plans, signed events, event routes and the runtime action registry/pipeline |
| `action_catalog.go` | Owner-facing catalog and validated action launch contract; never executes steps inline |
| `feedback.go` | Private feedback inbox, deterministic validation, image normalization and the report sufficiency gate |
| `agent.go`, `agent_config.go`, `agent_openai.go`, `agent_tools.go` | Main-agent runtime boundary, validated owner configuration, OpenAI-compatible and command adapters, bounded sessions, owner/observer tool grants and agent run audit |
| `model_services.go`, `model_execution.go` | Reusable model connections, task bindings, secret files, bounded catalog tests and tool-free report calls |
| `report.go`, `report_result.go`, `readiness.go`, `usage.go`, `channels.go`, `models.go`, `quota.go`, `pricing.go` | Reports and bounded result reads, capability readiness, usage and estimated cost, notifications, runner catalogs, the optional quota snapshot and the cached model price catalog |
| `inbound.go`, `weixin_push.go` | Explicit Weixin owner authorization, bounded read-only answers and the optional outbound push |

- A task is durable intent; a run is one attempt. Preserve single-writer
  checkout access, priority/off-peak policies, pause/hold/cancel/run-now
  behaviour, deduplication, and recovery of interrupted tasks after restart.
  Queue maintenance uses runner `codex`; absent a model-service assignment it
  inherits legacy routing. An assigned maintenance connection/model uses only a
  trusted per-call child environment, preserving structural validation/rollback
  and never mutating runner profiles. Its run records carry `task_id` from the
  first persisted running snapshot, just
  like report runs; each retry keeps the task identity and creates a new run.
- State exposes independent assistant/report/maintenance/context readiness as
  local configuration only (`verification: unknown`), never a credential or
  inference check. Standalone chat/report need no repository; maintenance and
  context do. `workshop` is always unavailable for now (`workshop_unavailable`)
  and must not affect setup/default-route decisions. State reads atomic snapshots
  without the repository writer lock.
- Generic `report` is a P1 kind, additive in memory for old kinds files; preserve
  owner-defined kinds and explicit report configuration. Carry the owner request
  in `payload.request`, with an explicit 4000-rune bound for manual/agent report
  requests; reject excess rather than dropping intent. Hook/other scalar payload
  projection retains its 512-rune bound. Report deduplication includes title and payload,
  while maintenance event coalescing keeps its existing semantics. All sources,
  including assistant tasks, use the ordinary independent queue dispatcher.
- Feedback is private owner input under `data/feedback/<id>/feedback.json` plus an
  optional re-encoded `screenshot.png`; IDs are `f-<12 hex>`, dirs 0700/files
  0600, and callers never supply paths. Validate title/area/text/priority with
  fixed reasons before persistence; malformed input writes no record. Accept only
  PNG/JPEG by content, reject dimensions outside 64..4096 or over 8MP, decode
  fully, strip metadata by re-encoding PNG, and bound input/output to 8 MiB plus
  total HTTP body to 8 MiB. When a report binding exists, call the trusted
  tool-free adapter with a strict JSON-only sufficiency prompt (30s, <=512
  output), extract the first balanced object, reject extra/missing/wrong-typed
  fields, accept ready=true only with reason `ok`, map unknown reasons to
  `ambiguous`, record only normalized bounded `gate_usage` counters (also on
  parse failure), and record `gate_unavailable` on any call/parse failure; never
  persist raw gate output. No report binding skips the gate without inventing a
  model. Rejections (`too_broad`, `ambiguous`, `not_actionable`, `unsafe_scope`,
  `gate_unavailable`, defensive `too_long`) are recorded with status rejected
  and returned as normal accepted=false outcomes. Acceptance uses the reserved
  internal `feedback-plan` report kind (never listed or configurable, so a
  configured `report` action cannot intercept it) to build a bounded
  plan-only report task (`Plan: ` title, payload request, no edits/deploy, no
  clarifying questions) and stores task_id. Listing returns at most 50 newest
  summaries; detail/image are ID-only.
- Reports use immutable `run-<run_id>.md` names. Result reads accept a run ID only,
  validate successful run ownership of its exact report filename, reject paths,
  symlinks and secret-shaped content, and bound regular-file reads to 64 KiB.
  Historical `task-<task_id>.md`/`action-<task_id>.md` artifacts were mutable:
  serve only the run's saved `report_text` with `historical_snapshot: true` and
  conservative `truncated: true`, or fail unavailable if absent. Never attribute
  a later attempt's file bytes to an older run. Owner-only `report.get` further
  bounds tool text and marks truncation.
- Kinds are configuration mapped to implemented actions. Reject unknown kinds
  both at hook setup and dispatch. A `report` action has no repository writes.
  The built-in maintenance plan stays runnable and catalog-visible when its
  task-kinds.json entry is removed; other unknown kinds still fail closed.
  The dispatcher must go through `resolveTaskAction` and `runAction`; legacy
  kinds stay one-step, runtime actions use the validated pipeline. Event route
  targets come from trusted ingress configuration or the built-in route table,
  never a request body; unknown targets fail closed.
- Runtime actions (`actions.json`) are owner-only configuration: validate the
  whole file, snapshot versions before writing, reject unknown fields, and fail
  closed when an action is disabled or malformed. Pipeline steps are bounded;
  scripts execute argv (no shell) under `data/scripts`, HTTP requires the
  configured host allowlist and HTTPS, and secrets resolve only in script env or
  HTTP headers/body. Audit step metadata and digests, never raw step output or
  stderr. File schema stays version 2: optional `description`, `requires`
  (assistant/report/maintenance/context/workshop, unique) and `presentation`
  (group <=32 runes, order 0..9999, submit label <=64 runes, <=8 validated
  fields) are additive; a missing presentation is valid. Configured text and
  textarea fields keep the 1..4000-rune bound. Owner catalog tasks use a separate
  explicit `maxActionPayloadBytes` (32 KiB) owner-payload path, so 4000-rune
  field values reach the durable task and dispatch snapshot instead of being
  silently truncated by the generic 512-rune hook projection. Sensitive field
  names are rejected, and a combined configured payload over that bound fails
  validation. Non-maintenance plans deduplicate only identical title/payload;
  maintenance plans retain event coalescing, so distinct configured field values
  create distinct queued tasks.
- The action catalog is the only decoupled action surface. It merges configured
  actions with legacy kinds (configured IDs shadow legacy ones), sorts by
  group/order/label, and exposes only ID, labels/optional i18n keys, description,
  priority, enabled, group, order, submit label, validated fields, requires and
  legacy flag. Never include steps, prompts, hosts, scripts or secrets. Built-in
  report kinds require `report`; maintenance requires `maintenance`; other legacy
  kinds require nothing. `title` and `priority` are reserved launch controls and
  may not be declared presentation fields; selects need at least one option.
  Catalog-run validates unknown/required fields, types, select options, rune
  limits and title/priority server-side, then creates durable task intent: legacy
  report maps `{request}`, legacy maintenance has no payload or priority override,
  configured actions pass validated fields. A `workshop` requirement is rejected
  server-side while that capability is unavailable; other `requires` values are
  UI gating and execution still fails closed if the capability is missing. Disabled
  or unknown actions fail closed; no step executes inline.
- The main agent is a bounded host loop, not an ambient process. The runtime
  receives one request and proposes text/tool calls; the store re-checks every
  tool against the trust class, executes it, and audits names/status without
  arguments or secrets. Owner sessions get control tools; integration observers
  get read-only tools. Sessions are bounded and keyed by authenticated identity.
  Runtime configuration is owner data at `data/agent/config.json`; validate the
  whole shape, reject plain HTTP or malformed URLs, store a supplied API key
  only at `data/secrets/agent-api-key` with mode 0600, and never return it.
  The built-in OpenAI-compatible adapter and the optional command adapter at
  `/data/agent/runtime` share the same gateway and bounds. Tool-call ids must
  pair the assistant message with each tool result; a real pi wrapper remains
  unverified, so tests use a synthetic endpoint or executable. Successful
  `task.create` emits bounded `task.created` task ID/title/status metadata;
  session history restores associations from retained durable task sources.
  Context answers must cite returned source paths and distinguish missing
  context; report follow-ups cite the inspected report run ID.
- `model_services.go` owns private `runtime/model-services.json` and write-only
  `secrets/model-services/<id>` keys (0600); validate whole configs and reject
  symlinks, public HTTP and malformed bindings. Default assistant/report/maintenance
  assignments and per-report/action overrides select an exact model or group ID;
  Folio does not implement cost-preference or account/fallback scheduling.
  Unbound uses preserve legacy config; malformed saved state fails closed.
  Catalog tests make bounded GET requests without redirects or inference.
  Bound reports/pipeline agents use a tool-free call; the main agent keeps its
  authorized tool loop. A selected assistant route failure must not bypass the
  gateway via the Weixin legacy runner. Keep requested aliases distinct from
  actual served metadata in `model_usage`; CLI routes lacking attribution remain
  unpriced. Retain HTTP reported usage even when content validation rejects it.
- Model prices are an optional external estimate, never billing truth. Fetch the
  models.dev JSON over HTTPS with bounded size/time, cache a compact projection
  under `data/runtime`, and keep the last good catalog on failure. Price only
  reported usage whose provider/model matches; unknown pairs return
  `estimated:false`/`price_unavailable`, not zero or a guess. State that list
  prices exclude cache writes, context tiers and subscriptions.
- `routes.json` is the single source for event targets; an action target must
  resolve through the validated registry and a disabled route fails closed.
- Inbound Weixin uses an explicitly configured account/owner/direct-peer match
  before any capability, including cached reply delivery. Text reuses the
  ephemeral read-only report worker with a fresh per-call directory; never pass
  channel credentials or use transport session IDs to resume a model session.
  A connected repository contributes one `folio build --stdout --no-record`
  packet, clamped and marked as reference data; selection failures degrade to an
  answer without context instead of failing the message. Runner failures cross
  the child boundary as the fixed exit codes in `internal/context`, never as
  stderr, and become a channel failure class.
  External users are denied; future personas require a separate restricted
  context/capability boundary. Channel calls do not create task/run audits.
- `weixin_push.go` owns the optional outbound WeChat switch and test send. The
  transport is injected at startup; enabling without it is rejected, and a push
  failure is only a delivery-ledger entry.
- Automatic organization uses a host-built complete snapshot and the context
  package's structural validator even with an older worker. Checkpoints advance
  only after successful no-change or verified push, acknowledging resulting
  bytes; a failed model/validation/push must not acknowledge input.
  Require local HEAD to match the remote after pull, including no-change runs;
  interrupted unpublished commits need explicit review and reconciliation.
  Review findings persist while the source fingerprint is unchanged. Bounded
  backlogs create maintenance continuation tasks under ordinary pause/window policies.
  A maintenance event received during a running task coalesces into one queued
  follow-up so changes after the pull cannot be lost.
- Verify a hook's signature over timestamp plus exact raw body before consuming
  replay/rate state. Preserve bounded scalar payload projection; expose the
  hook secret only at creation. Bodies cannot select an unauthorized action.
- Before publishing, require the expected clean checkout/remote, pull first,
  validate every edited path and content, stage exactly the approved paths,
  and verify the published head. Preserve rollback and audit evidence on
  failure. Never force-push or bypass a validation gate.
- Keep locks scoped so read-only state/history queries remain responsive during
  model runs. Persist private state with bounded reads/writes and restrictive
  permissions; keep secrets out of responses, diffs and notification ledgers.
- Failed runner audits may store only a fixed diagnostic class such as network,
  authentication, timeout or schema failure; never persist raw stderr or model
  output. Preserve fixed worker diagnostic classes across the service boundary.
- Maintenance runs snapshot only the storage plugin ID, its validated repository
  name and the checkout branch before pulling; never copy remote URLs or private
  repository state into that snapshot. Automation listing keeps `latest` as the
  newest run and separately exposes `review_run`, the newest retained successful
  maintenance record with an explicit `needs_review` field. Reports, failures
  and old records without findings cannot clear that historical review snapshot.
- Update this file for changed state/transaction invariants. Persisted schemas,
  task/run statuses or response fields also require a compatibility decision
  and synchronized HTTP/UI/worker instructions, tests and contract docs.

Run `go test -race ./internal/store` from the repository root and the root Go
checks. Focus regression coverage on failed validation/push, concurrent access,
restart recovery, hook replay/rate limits, and secret redaction when affected.
Tests use temporary repositories and fake runners, never live credentials.
