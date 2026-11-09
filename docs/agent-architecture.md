# Runtime actions and the main agent

Status: target architecture. Snapshot date: 2026-11-01. Phases 1–4 are
implemented at the adapter boundary: runtime actions, event routes, the main
agent loop with an in-process tool gateway, console chat, Weixin owner turns,
and the action editor/version restore. The real pi wrapper, lane admission and
live push/remote acceptance remain unverified; see §10. No private context,
credentials, or host details belong here.

Scope decision: the main agent may change runtime configuration; editing Go
source, running a shell, and deploying a binary are out of scope.

## 1. Summary

Folio's product shape is a personal assistant and task center built around an
owner-controlled digital counterpart; see [the product definition](product-definition.md).
The runtime architecture keeps the Go control plane: durable tasks, audit, Git transactions,
channels, and the console. Two things change:

1. **Actions become runtime data.** An action is an ordered list of bounded
   steps over a fixed primitive set. Adding a task is a JSON edit; adding a new
   primitive is still a code change. Scripts are allowed as an owner-authored
   primitive, never as event-supplied code.
2. **A main agent becomes the control-plane interface.** It runs behind a
   narrow runtime adapter (a pi wrapper is the intended deployment), receives
   owner messages from Weixin and the console chat, and calls Folio tools to
   create tasks, edit actions and routes, push messages, and inspect runs.
   "Modifying Folio" means runtime configuration only; source code and deploy
   stay owner/CI work. Integration events never reach write tools.

Non-goals: multi-tenant access, executing code from the context repository or
an integration payload, a general-purpose workflow language, and editing or
deploying Folio's source code from an agent turn.

## 2. Historical starting point

The following list describes the baseline before phases 1–4, not the current
service. Runtime actions, event routing, console sessions and owner tools now
exist. The daily-use interface adds independent capability readiness, durable
report creation, task associations in assistant events/history, and bounded
run-owned report retrieval through the console and `report.get`. These remain
subject to the runtime acceptance limits in the status block; configuration
readiness does not verify model credentials or inference support.

- `data/task-kinds.json` maps one kind to one of two hardcoded actions.
- `DispatchDue` has two branches: `report`, and everything else (maintenance).
- Weixin inbound is a one-shot read-only answer; it creates no task and has no
  tools.
- There is no console chat or agent session.
- One `modelMu` serializes every model call; a long maintenance run blocks an
  interactive reply.
- A hook binds one action at creation; there is no general event router.
- DSH vendors pi's model layer (`@earendil-works/pi-ai`) but owns its agent
  loop. Moving the main agent to pi is a runtime choice, not a model-provider
  change.

## 3. Target architecture

```text
events: console / weixin / integration / system
              │
              ▼
        ┌───────────┐
        │  router   │  by authenticated source, never by body
        └──┬─────┬──┘
   owner   │     │   integration / system
           ▼     ▼
   ┌───────────┐  ┌────────────────┐
   │ main agent│  │   task queue   │
   │ (pi)      │  │ priority·lane  │
   └─────┬─────┘  └───────┬────────┘
         │ tools          │
         ▼                ▼
   ┌───────────┐  ┌────────────────┐
   │tool gateway│ │ action runner  │
   │per-run grant│ │    steps       │
   └─────┬─────┘  └───────┬────────┘
         │                │
         ▼                ▼
  actions.json /     context / channels
  task queue / push  / reports / audit
```

Three planes:

- **Control plane** — owner intent, action definitions, agent tools. Fast,
  authenticated, directly effective.
- **Data plane** — durable tasks, priority/lane admission, step execution,
  audit. Single-writer where the checkout is involved.
- **Channel** — Weixin is both an owner-event source and a push sink. It is not
  a second control plane.

## 4. Core objects

| Object | Meaning |
| --- | --- |
| event | One authenticated occurrence, with a trust class. |
| route | `(source, type) → target`, configured by the owner. |
| action | Runtime definition: id, priority, lane, ordered steps. |
| task | Durable instance of an action with a payload. |
| run | One attempt at a task, with per-step results and audit. |
| lane | Admission class: `repo`, `interactive`, `batch`. |
| agent turn | One main-agent run. Audited, but not a durable task. |
| tool | Host capability granted to an agent turn or invoked by a step. |

### 4.1 Events and routes

```json
{
  "version": 1,
  "id": "evt-01...",
  "source": "weixin",
  "type": "owner.message",
  "subject": "owner",
  "occurred_at": "2026-11-01T09:00:00+08:00",
  "trust": "owner",
  "payload": { "text": "..." }
}
```

| Trust class | Ingress | May reach | Capability |
| --- | --- | --- | --- |
| `owner` | Weixin owner, console session | main agent | config + control tools |
| `integration` | signed hook, GitHub bridge, watcher | task queue, or agent in observer mode | no write tools |
| `system` | task/run lifecycle, scheduler | notify/push or observer | notify; observer read-only |

Concrete event catalog (target names; current hook payloads map to them at
ingress):

| source | type | trust | Real example | Default target |
| --- | --- | --- | --- | --- |
| `console` | `owner.message` | owner | Chat: "把早报改成每天 8 点" | `agent:main` |
| `weixin` | `owner.message` | owner | "上周的笔记帮我整理一下" | `agent:main` |
| `hook` | `repo.changed` | integration | Watcher sees HEAD move `abc123 → def456` | `action:maintenance` |
| `github` | `issue.opened` | integration | Issue #42 "Crash on save" opened | `action:triage` |
| `github` | `issue.updated` | integration | Label `bug` added to #42 | `action:triage` |
| `scheduler` | `tick` | system | 00:30 daily maintenance | `action:maintenance` |
| `task` | `completed` | system | Maintenance pushed 3 files | `notify:owner` |
| `task` | `failed` | system | Runner timeout | `notify:owner` |
| `action` | `published` | system | `morning-brief` v3 published | audit only |
| `action` | `disabled` | system | Owner disables `nightly-backup` | audit only |
| `channel` | `delivery.failed` | system | Weixin push returned 500 | `notify:owner` |

These names are new; the hook wire format does not carry a `type` today. At
ingress, the hook configuration supplies `source` and `type`. The GitHub
bridge's `event: opened|updated` becomes `github.issue.opened|updated`; the
watcher becomes `hook.repo.changed`; the scheduler currently runs maintenance
directly and will instead emit `scheduler.tick`; Weixin inbound is currently a
one-shot answer and becomes `weixin.owner.message` in Phase 3.

Run-level detail stays in the run ledger. Task completion currently uses the
existing notification path; routing `task.completed` and `task.failed` through
`routes.json` is planned.

Console chat example:

```json
{
  "source": "console",
  "type": "owner.message",
  "trust": "owner",
  "subject": "chat:main",
  "payload": {
    "session": "chat:main",
    "text": "把早报改成每天 8 点"
  }
}
```

Task failure example:

```json
{
  "source": "task",
  "type": "failed",
  "trust": "system",
  "subject": "task:t-01...",
  "payload": {
    "task_id": "t-01...",
    "action": "maintenance",
    "reason": "runner_timeout"
  }
}
```

Routes are data:

```json
{
  "version": 1,
  "routes": [
    { "source": "console", "type": "owner.message", "target": "agent:main" },
    { "source": "weixin", "type": "owner.message", "target": "agent:main" },
    { "source": "hook", "type": "repo.changed", "target": "action:maintenance" },
    { "source": "github", "type": "issue.opened", "target": "action:triage" },
    { "source": "task", "type": "failed", "target": "notify:owner" },
    { "source": "action", "type": "published", "target": "audit:only" }
  ]
}
```

Rules:

- The ingress assigns `trust`; the body cannot choose it.
- A route matches `source` and `type` only. Payload cannot select a target,
  tool, or trust class.
- An integration event routed to `agent:main` runs in **observer** mode: read
  tools only, never `action.put`, `task.create`, or `push.send`.
- An unknown event is recorded and ignored. It is never guessed into an action.

### 4.2 Action definition

```json
{
  "version": 2,
  "actions": [
    {
      "id": "morning-brief",
      "label": "Morning brief",
      "priority": "P1",
      "lane": "batch",
      "steps": [
        { "id": "ctx", "type": "context", "query": "{{payload.focus}}", "max_bytes": 65536 },
        { "id": "brief", "type": "agent", "runner": "codex", "timeout_seconds": 900,
          "prompt": "Write a brief for me.\n\n<context>{{steps.ctx.output}}</context>" },
        { "id": "send", "type": "push", "channel": "weixin", "title": "Morning brief",
          "body": "{{steps.brief.output}}" }
      ]
    },
    {
      "id": "nightly-backup",
      "label": "Nightly backup",
      "priority": "P2",
      "lane": "batch",
      "steps": [
        { "id": "backup", "type": "script",
          "command": ["/data/scripts/backup.sh", "--json"],
          "cwd": "/data", "stdin": "{{payload}}", "timeout_seconds": 600,
          "max_output_bytes": 65536 },
        { "id": "notify", "type": "push", "channel": "weixin",
          "title": "Backup", "body": "{{steps.backup.output}}" }
      ]
    }
  ]
}
```

Step types (v1):

| Type | Does | Notes |
| --- | --- | --- |
| `context` | read-only context packet | bounded; no repo write |
| `agent` | one model call | fixed prompt; runner/model/timeout |
| `script` | owner-authored executable | argv only; no shell string |
| `push` | one channel message | Weixin/Bark; rate-limited |
| `http` | one HTTP request | host allowlist, no redirects |
| `task` | enqueue another action | bounded fan-out |

Rules:

- Steps run in order. There are no loops, branches, or conditional
  expressions; anything more is a new primitive, not a new JSON feature.
- Templates are variable substitution only:
  `{{event.*}}`, `{{payload.*}}`, `{{steps.<id>.output}}`, `{{secrets.<name>}}`.
  A missing key fails the step. Unknown functions fail validation.
  `{{secrets.<name>}}` is valid only in `script.env` and `http` headers or
  body; a template that would put a secret into a prompt, report, or push body
  fails validation.
- Each step has a timeout and an output bound; the action has a total budget.
- A failed step stops the action by default (`on_error: continue` is opt-in).
  A retry reruns the action, not a half-finished plan. Side-effecting steps get
  `FOLIO_RUN_ID` and `FOLIO_STEP_ID` for idempotency.
- The built-in `report` action becomes an `agent` step plus an action-level
  `report_from` field that names the step whose output is persisted as the
  report and summary. The `report` kind remains a compatibility alias during
  migration; it is not a second execution path.

### 4.3 Scripts

A `script` step is the owner's code, not a sandbox:

- `command` is an argv array; the first element is an absolute path under the
  configured scripts root (`/data/scripts` by default). No shell string and no
  word splitting.
- Payload data arrives on `stdin` (objects render as JSON) or in environment
  variables, not interpolated into a shell command. `argv` elements may be
  templates, but each element is one literal value.
- `cwd` must stay under the private data directory.
- stdout and stderr are bounded. Raw stderr is not persisted; the run stores a
  fixed failure class unless the owner opts into a private bounded log.
- The process runs as the service user and can read the private volume. That is
  equivalent to owner shell access. The owner binding and the private volume
  are the security boundary; the step itself is not a sandbox.

The main agent may wire an existing script into an action, because that is
configuration, but it cannot create or edit the script file; scripts are
owner/CI artifacts.

### 4.4 Main agent

Runtime: an injected adapter; a pi wrapper is the intended deployment. Folio
keeps a narrow Go interface so the runtime can be replaced:

```go
type AgentRuntime interface {
    Available() bool
    Turn(context.Context, AgentRequest) (AgentResponse, error)
}
```

DSH remains a worker runner. Its SDK/ACP is not the control-plane contract: its
agent loop is downstream of pi and may change independently. The
`AgentRuntime` boundary keeps that swap cheap.

`AgentRequest` carries the session, input, bounded history, the granted tool
specs, and the event trust class. Folio executes the tools; the runtime never
receives ambient authority.

The owner session has one write surface: runtime configuration and queue
control. An observer session has read tools only.

| Class | Tools | Effect |
| --- | --- | --- |
| control | `task.*`, `action.*`, `route.*`, `policy.get` / `policy.put`, `push.send`, `context.search`, `run.*`, `status` | queue, definitions, routes, policy, channel, context, audit |
| observer | `context.search`, `run.get`, `status` | read-only |

"Modify Folio" means:

- create, update, disable, and version actions (`action.put`, `action.disable`);
- change event routes (`route.put`) and the queue policy (`policy.put`).

Writes are read from the private files on every resolution, so no reload tool is
required.

It does not mean editing Go source, running a shell, or replacing the binary.
Those remain owner/CI work and need a separate design.

Rules:

- Owner sessions get control tools. Observer sessions get read tools only.
  Integration events never receive write tools.
- All writes go through the tool gateway, which validates, authorizes, and
  audits them. The agent never writes `actions.json`, `routes.json`, or the
  policy file directly.
- `action.put` takes effect immediately after schema validation. It is
  versioned, attributed, and audited; `action.disable` is the kill switch.
  There is no confirmation step.
- `route.put` and `policy.put` follow the same pattern; invalid routes or
  policy values are rejected before they are saved.
- A turn is a run (`trigger: agent`), not a durable task. Work that outlives the
  turn becomes `task.create`.
- Session keys are `(channel, account, conversation)`. Transcripts are bounded
  and live under the private runtime directory; the audit keeps the tool calls,
  not the model's hidden reasoning.
- The channel reply is the turn result. Proactive messages use `push.send` and
  the delivery ledger.

The primary Settings → Model connections form manages reusable connections
and independent assistant/report/maintenance assignments, with per-report/action
overrides. An assistant assignment constructs the bounded HTTP adapter for that
turn; an unassigned assistant uses the legacy configuration below. Assignments
select exact model/group IDs; account/fallback/cost-preference scheduling stays
in the gateway. Reports use a separate tool-free HTTP call, while assigned
maintenance retains the Codex worker and transaction boundary. Runtime actions,
queue priorities and tool authorization are unchanged. See the
[deployment contract](../deploy/README.md#model-services-and-task-assignments).

Two adapters implement the main-agent boundary:

- **OpenAI-compatible HTTP (built in).** Settings → Main agent runtime stores
  `/data/agent/config.json` and a write-only key at
  `/data/secrets/agent-api-key`. The adapter sends bounded chat-completions
  requests with the same tool schemas and `tool_choice: auto`, parses text,
  `tool_calls` and `usage`, and never exposes the key or base URL to a tool.
  HTTPS only; redirects are rejected. A saved file wins over the environment
  fallback (`FOLIO_AGENT_KIND`, `FOLIO_AGENT_PROVIDER`,
  `FOLIO_AGENT_BASE_URL`, `FOLIO_AGENT_MODEL`,
  `FOLIO_AGENT_API_KEY_FILE`, `FOLIO_AGENT_TIMEOUT_SECONDS`).
- **Command adapter.** An absolute executable (legacy path
  `/data/agent/runtime`) receives one JSON `AgentRequest` on stdin and returns
  one JSON `AgentResponse` on stdout: `text`, optional `tool_calls` (`name`,
  `arguments`) and optional `usage`. A pi-backed wrapper belongs here; the real
  pi package is still not installed or verified in this environment.

Folio validates names, arguments, output bounds and grants, executes the tools,
appends tool results, and calls the runtime again. Assistant messages carry the
tool-call ids so an OpenAI-compatible second request can pair each result.

### 4.5 Lanes (target; admission not enforced yet)

| Lane | Work | Concurrency | Window |
| --- | --- | --- | --- |
| `repo` | maintenance | 1 | owner policy |
| `interactive` | owner chat | 1 reserved model slot | immediate |
| `batch` | reports, scheduled actions | remaining model slots | owner policy |

Lanes are the target admission model, not OS threads. The current build still
uses the existing single dispatcher and one model mutex; lane admission is not
enforced yet. Until it is, an interactive turn can wait behind a maintenance
run, and the queue remains single-writer.

### 4.6 Console chat (implemented)

The console has an `Assistant` view. It is an owner event source, not a second
agent:

- `#chat` view in the existing hash navigation; owner-only like the rest of the
  console. No plugin mount is needed.
- `POST /api/agent/turns` with `{session, text}` and the existing CSRF header;
  the response is `text/event-stream`, which the same-origin CSP permits.
- Stream events: `turn.started`, `text.delta`, `tool.call`, `tool.result`,
  `turn.finished`, `error`. Tool rows show name, a short argument summary,
  status, and a link to the run/task; no secrets or raw model reasoning.
- `GET /api/agent/sessions/main` returns the bounded transcript for reload.
- A disconnect cancels the turn; a partial run is audited as canceled.
- The chat and Weixin use the same main agent; only the session key differs.

## 5. Security model

- **The owner is the trust root.** Single-owner binding is the authorization
  boundary. A compromised owner channel can change actions and routes and
  invoke installed scripts; treat it as owner-level access, not as a sandbox.
- **Direct effect is accepted.** Validation, versioning, audit, rate limits,
  and a disable switch are the mitigations, not an approval dialog.
- **Integration events never get write tools.** They can trigger actions; they
  cannot define actions, create tasks, push, or edit routes.
- **No source or deploy tools.** The agent cannot edit Go source, run a shell,
  or replace the binary. Runtime configuration is the whole write surface;
  source and deploy remain owner/CI work.
- **No body-selected authority.** Routes, trust classes, tools, and scripts are
  configuration; payload is data.
- **No shell interpolation.** Scripts receive argv and stdin; HTTP gets a
  bounded body; prompts frame payload as untrusted data.
- **Secrets stay by reference.** `{{secrets.name}}` resolves at execution and is
  never written to a run, report, log, or notification.
- **Public/private split.** The public repository and image contain no private
  actions, scripts, context, credentials, or host details.

## 6. Storage and APIs

Planned private layout:

```text
/data/actions.json              action definitions (0600)
/data/routes.json               event routes (0600)
/data/agent/config.json         owner-selected agent runtime (0600)
/data/secrets/agent-api-key     write-only provider key (0600)
/data/agent/runtime             optional command adapter (unverified pi wrapper)
/data/agent/sessions/           bounded session state
/data/runtime/model-prices.json cached models.dev price projection (0600)
/data/scripts/                  owner scripts (0700)
/data/policies.json             queue priority/window policy
/data/runtime/action-versions/  published versions and diffs
/data/runtime/runs/             existing run ledger
/data/runtime/reports/          existing reports
```

Session-protected endpoints:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/actions`, `POST /api/actions/validate` | inspect and dry-run |
| `POST /api/actions` | publish (CSRF, audited, direct) |
| `POST /api/actions/{id}/disable` | kill switch |
| `GET /api/actions/versions`, `POST /api/actions/restore` | registry history and rollback |
| `POST /api/actions/hosts` | HTTP host allowlist |
| `GET /api/agent/config`, `POST /api/agent/config` | safe runtime status and validated write-only configuration |
| `POST /api/agent/turns` | owner chat turn, SSE |
| `GET /api/agent/sessions/{id}` | bounded transcript |
| `GET /api/pricing`, `POST /api/pricing/refresh` | cached model price catalog and bounded refresh |

Routes and policy are edited through the main-agent tools; a dedicated Web
route editor is not implemented. A chat turn is canceled by closing the SSE
connection.

Existing task, run, usage, channel, and repository APIs keep their contracts.

### 4.7 Token prices

`internal/store/pricing.go` downloads the public
[`models.dev`](https://models.dev) catalog from `https://models.dev/api.json`,
projects provider/model/cost entries into a compact bounded cache, and computes
US-dollar list-price estimates per run. The catalog is refreshed in the
background at startup when missing or older than 24 hours, then checks again on
a bounded timer. `FOLIO_PRICING_URL` can point
at an internal mirror with the same shape.

The estimate uses uncached input, cached input and output rates per million
tokens. Cache writes, context-tier overrides, subscriptions and taxes are not
modeled. A model/provider pair absent from the catalog keeps its token counts
and returns `estimated: false` with `price_unavailable`; it is never guessed.
Agent, maintenance, report and pipeline runs carry the model/provider needed for
the lookup. A pipeline that calls several models records `model_usage` and
aggregates a partial total.

## 7. Migration

Phase 0 — runtime spike. Verify pi's SDK/RPC custom-tool API, the ability to
disable built-in shell/filesystem tools, session persistence, and a Node
sidecar lifecycle. Pin the pi package and Node version. Acceptance: one pi turn
calls a fake `push` tool through the gateway and returns; only the granted
tools are available.

Phase 1 — event envelope, router, action registry (implemented 2026-11-01).
Represent `maintenance` and `report` as internal built-in actions (not yet
exposed as `actions.json` v2); keep behaviour identical. Acceptance: existing
hook and task tests pass; a route can target the same actions.

Phase 2 — action v2 (implemented 2026-11-01). Add `script`, `http`, `push`,
`task` steps; validation, versioning, and the console dry-run. Acceptance: a
script action runs from `/data/scripts` with argv/stdin and bounded output; a
failed step is audited. The registry API is implemented; the visual editor is
deferred to Phase 4.

Phase 3 — main agent and chat (implemented at the adapter boundary,
2026-11-01). Add the runtime boundary, in-process tool gateway, sessions,
console chat, Weixin owner turns, and `routes.json`. Acceptance: owner asks for
a task in Weixin or chat, the agent creates it, and the run is audited; the chat
streams tool calls; an integration event cannot reach write tools. The real pi
wrapper is not installed or verified; tests use a fake and the command adapter.
Lane admission remains planned.

Phase 4 — editor and operations (core implemented 2026-11-01). Action editor,
rollback, and version restore are implemented. Metrics, a second channel, and a
retention policy remain planned.

## 8. Open questions

- The real pi adapter: SDK versus RPC, and whether a persistent Node sidecar or
  a per-turn child is simpler to operate. This is the unverified Phase 0 work.
- Lane admission and the reserved interactive model slot; the current build
  still uses one dispatcher and one model mutex.
- Can a real pi wrapper speak the stdin/stdout adapter protocol and avoid
  built-in shell/filesystem tools? If not, use a minimal pi SDK session rather
  than the coding-agent profile.
- Scripts root: only `/data/scripts`, or an owner-configured allowlist?
- Session compaction and retention: who owns the canonical transcript, pi or
  Folio?
- Action-version retention and whether rollback re-runs or only re-arms.
- Whether maintenance stays a built-in action or becomes a normal action with a
  privileged repo primitive.

## 9. Decisions

2026-11-01:

- Runtime actions are step chains, and **scripts are allowed** as an
  owner-authored primitive.
- `action.put` takes effect **immediately**; no confirmation step.
- The main agent runs behind an **OpenAI-compatible or command adapter**; a pi
  wrapper remains supported through the command protocol. DSH remains a
  maintenance/report runner, not the main-agent contract.
- The console gets a first-class **chat window** to the main agent; Weixin is
  the remote surface.
- The owner session may **modify Folio's runtime configuration only**: actions,
  routes, prompts, and policy. Source code and deploy are out of scope, and
  integration events never get write tools.
- The deployment remains **single-owner**; the owner binding is the trust root.
- The queue gets **lanes** so interactive replies are not blocked by
  maintenance.

## 10. Implementation status

2026-11-01 — Phases 1–4 landed at the adapter boundary:

- `internal/store/actions.go` defines the internal `actionPlan`/`actionStep`
  shape and routes both legacy kinds and runtime actions through `runAction`.
  Legacy kinds remain one-step and keep their previous behavior.
- `internal/store/events.go` and `routes.go` define the event envelope, trust
  classes, defaults and `/data/routes.json`. `SchedulerTick` enters through
  `scheduler.tick`; console and Weixin owner messages resolve through the same
  route table.
- `internal/store/action_config.go` loads and validates `/data/actions.json`
  (version 2), snapshots versions before writes, and exposes list/validate/
  put/disable/restore operations. Unknown fields and unsafe definitions fail
  closed.
- `internal/store/action_pipeline.go` executes ordered `context`, `agent`,
  `script`, `push`, `http` and `task` steps with bounded output, templates,
  per-step audit records, usage aggregation and optional report persistence.
- `internal/store/agent.go` and `agent_tools.go` implement the runtime adapter,
  bounded sessions, the tool loop, owner/observer grants and an agent run audit.
  The built-in OpenAI-compatible adapter and the command adapter at
  `/data/agent/runtime` are both tested with synthetic endpoints/executables;
  a real pi installation remains unverified.
- `internal/store/pricing.go` caches the models.dev catalog, estimates run cost
  from reported usage, and leaves unknown prices explicitly unavailable.
- `internal/store/inbound.go` sends authorized Weixin owner text through the
  agent when the route and runtime are available, and keeps the bounded
  read-only answer as a fallback.
- `internal/web` exposes the action registry API, agent configuration, the SSE
  chat endpoint, session history and the price catalog API. The console has
  `#chat` and `#actions` views; Settings includes the agent runtime card; task
  details and usage show estimated cost.
- Hooks, task creation and dispatch accept runtime action IDs; the legacy
  `task-kinds.json` path still works.
- Not verified / not implemented: a real pi package wrapper, lane admission and
  the reserved interactive model slot, task lifecycle events routed through
  `routes.json`, live Weixin/push delivery, metrics, a second channel, and the
  git remote push. `lane` is validated but not enforced.
- Tests cover action validation/versioning, pipeline steps, runtime dispatch,
  routes, the agent turn/tool loop, Weixin routing, the chat SSE API, the action
  registry API, the editor contracts and the unchanged legacy paths.

## References

- pi coding agent SDK:
  <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/sdk.md>
- pi coding agent RPC:
  <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md>
- DSH repository: <https://github.com/deepseek-ai/deepseek-harness>
- Current task model: [event-driven-assistant.md](event-driven-assistant.md) §4
- Hook contract: [integration-plugins.md](integration-plugins.md)
- Weixin boundary: [weixin-channel.md](weixin-channel.md)
