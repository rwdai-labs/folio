# From a nightly job to an event-driven assistant

Status: design note. Snapshot date: 2026-10-23. Supersedes the sequencing in
[personal-assistant-research.md](personal-assistant-research.md) §6.
No secrets or private context belong in this file.

## 1. What actually changes

Folio today is **one nightly job**. The target is **a small automation platform**
where work is caused by something happening, costs money you can watch, and
runs at the cheapest acceptable moment.

| Today | Target |
| --- | --- |
| one fixed job (context maintenance) | named job templates anyone can trigger |
| one interval scheduler | event sources: manual, schedule, watch, webhook, hook API |
| always Codex | priority + trust + price + peak/off-peak routing |
| no cost visibility | per-run token and cost ledger with a dashboard |
| no output | Bark push now; DingTalk / Feishu / WeCom later |
| one global lock | resource-scoped locks |

The five primitives to build. Everything else is configuration of these:

1. **Event source** — *when*: manual, schedule, watch, webhook, hook API.
2. **Job template** — *what*: a named, bounded unit of work with declared tools
   and a declared output class.
3. **Policy** — *how*: priority, provider trust, price, time window,
   concurrency, approval requirement.
4. **Surface** — *out*: Web UI always; push channels optionally.
5. **Ledger** — *evidence*: tokens, cost, latency, status, trigger lineage.

## 2. Event sources

| Source | Cost to run | Latency | Reachability needed | Attack surface |
| --- | --- | --- | --- | --- |
| Manual (Web) | free | immediate | none | authenticated session |
| Schedule (cron/at/every) | free | scheduled | none | none |
| Watch (cheap change probe) | free / one API call | one probe interval | outbound only | none |
| GitHub webhook | free | seconds | inbound HTTPS | signed webhook |
| Generic hook API | free | seconds | inbound HTTPS | **HMAC + replay + budget** |

**Folio owns only the last row.** It never polls: the watcher is someone else's
job (a shell script, a CI step, another host). Folio's inbound surface is one
signed endpoint, and everything else — how a change is noticed — stays outside
the service. That keeps a moving part out of the service and keeps the security
question small: one authenticated door.

**Keep schedule, but demote it.** A schedule is correct only when the thing you
want has no observable event (e.g. "summarize the week"). When an event exists,
schedule is a worse approximation that either wastes model calls or is late.

### External watch is the answer to "don't use dumb cron"

`git ls-remote origin HEAD` costs no API quota and no model tokens. Compare the
SHA with the last seen value. If it changed, the context repository moved: that
*is* the event. The watcher then posts it to a hook, Folio pulls and maintains,
and the SHA doubles as the event id so a retry is idempotent.

This is not polling for work; it is checking whether a change occurred. Empty
probes cost nothing, so a 5-minute interval is cheap; the expensive part (a
model call) only happens on a real change.

`scripts/folio-watch-git.sh` implements exactly that and runs from cron, a
systemd timer, or a NAS scheduled task. `scripts/folio-hook.sh` sends one signed
event by hand. Neither is part of the service.

The same shape covers GitHub without webhooks: `GET /repos/{r}/issues?state=open`
with `If-None-Match` and the stored ETag. A 304 costs one rate-limit unit and no
response body. At personal scale this is effectively free.

### Plugins: one class owns the remote, one class reports events

Folio is not a GitHub client, and it is not a Git-only service either. The code
host sits behind `internal/storage.Remote`, implemented by the built-in
`plugins/github`. That plugin owns the remote (clone, fast-forward, push,
ls-remote), the credential (`gh` device sign-in or `GIT_AUTH_TOKEN`), the sign-in
card, and the repository picker. Folio owns the checkout, the clean-check gate,
the model run, the commit, the rollback, and the audit trail; it calls the
plugin through one interface and never sees the token. A network drive or an S3
bucket is a new implementation of six methods, not a change to the worker.

External triggers are the other class: a separate program with its own
credentials and state that notices an event and calls the signed hook API. The
context watcher, a CI job, and `plugins/github-bridge` (issues) are that class.
The dependency direction is the defining difference:

- built-in capability plugin: Folio → plugin (compiled in, host mounts it);
- external event plugin: plugin → Folio (hook API only).

The trigger plugin cannot choose what Folio does: the hook's action decides the
task kind, and the owner sets it in Folio. That keeps the dangerous half of an
integration — the action, not the trigger — behind the owner's configuration.

### The hook API is the only inbound trigger

`POST /api/hooks/{hook_id}` is how work starts. This is the "开放 API"
requirement, and it is also the most dangerous surface in the product: an
inbound request can cause an agent to run.

Implemented in `internal/store/hooks.go` and `internal/web/web.go`. The
identity is a hook id plus a 256-bit secret shown once at creation; only the
signature proves the caller may start work. Management verbs
(`/update`, `/rotate`, `/delete`) stay behind the browser session.

Contract:

```text
POST /api/hooks/{trigger_id}
X-Folio-Timestamp: 1761000000
X-Folio-Event-Id: <unique string, ≤128 bytes>
X-Folio-Signature: sha256=<hex hmac of "{timestamp}.{raw body}">
Content-Type: application/json
```

Rules, all enforced in code:

1. **Disabled until armed.** A trigger exists as configuration; it accepts
   nothing until the owner enables it. The secret is generated server-side and
   shown exactly once.
2. **Signature over raw body plus timestamp**, constant-time compare.
3. **Replay window** ±300 s and an event-id dedupe store (last 512 ids).
4. **Payload projection.** The trigger names a job template and a whitelist of
   payload fields with type and length bounds. Raw payload never becomes
   instructions; the template renders a fixed prompt with projected values
   quoted as untrusted data.
5. **Budget.** Per-trigger: max invocations/hour, max concurrent jobs, daily
   token cap. Exceeding returns `429`, never a partial run.
6. **Least privilege.** A hook-triggered job gets the template's declared tool
   set only. Default templates are read-only or candidate-only.

Response: `202 {"job_id", "trigger_id", "event_id"}` or a 4xx code. Never
echo the payload.

## 3. Job templates

| Template | Input | Tools | Output class | Approval |
| --- | --- | --- | --- | --- |
| `context-maintain` | repo HEAD | pull, edit Markdown, commit, push | canonical write | diff review before push |
| `context-consolidate` | inbox candidates | read canonical, write candidates | candidate only | owner review |
| `issue-triage` | issue number | read repo, read issue, propose labels/comment | proposal | required before any GitHub write |
| `pr-review-draft` | PR number | read diff, read issue, draft comments | proposal | required |
| `digest` | period | read ledgers | message | none (read-only) |

Two rules that keep this safe as it grows:

- A template declares its **output class** (`read_only`, `candidate`,
  `canonical`, `external_write`). Policy derives approval and trust rules from
  the class, not from the template name.
- GitHub writes are *external* writes: visible to other people, hard to undo.
  They always require explicit approval, regardless of provider trust.

## 4. Priority, trust, and cost routing

Three independent axes that people conflate:

- **Priority** — how soon the result is worth anything.
- **Trust** — whether the provider's output may influence durable state.
- **Price** — what a token costs *now*.

### Priority classes

| Class | Meaning | Deferrable | Default route |
| --- | --- | --- | --- |
| P0 interactive | owner is waiting (<30 s) | no | fastest trusted provider |
| P1 timely | needs action within the hour (triage) | ≤ 15 min | trusted, cheapest within deadline |
| P2 normal | daily value (maintenance, digest) | until deadline | cheapest trusted, prefer off-peak |
| P3 batch | no deadline (consolidation, replay) | yes | cheapest acceptable, **off-peak only** |

### Task queue (implemented)

`internal/store/tasks.go` separates **intent** from **attempt**:

- `task` — durable intent: kind, priority, source, status, deferral time.
- `run` — one attempt: the immutable record `RunAutomation` already produces.

Events no longer start runs. A hook creates a task; the dispatcher starts at
most one due task per tick, highest priority first.

| Concern | Rule |
| --- | --- |
| Priority | P0/P1 start immediately; P2/P3 defer to the off-peak window. |
| Window | `00:30–07:30` local by default, from `policies.json`; may cross midnight. |
| Pause | Global switch and per-task hold. Pausing keeps the queue; it never drops work. |
| Run now | Owner override clears the deferral and re-arms a finished task. |
| Dedupe | An active task with the same kind and source is returned, not duplicated. |
| Serialization | One task at a time, deliberately: checkout, commit, and audit assume a single writer. |
| Crash | A task left `running` at startup becomes `failed/interrupted`, never a phantom. |
| Unknown kind | Fails with `unsupported_kind` instead of retrying forever. |

The window is the **owner's** schedule, not a provider price window. "When I am
asleep" is a human fact expressed in local time; provider peak hours belong to
the price table and will be a second, independent gate once that exists.

### Provider profile

```yaml
id: ds-official
kind: official          # official | reseller | local
trust: verified         # verified | unknown | untrusted
models: [deepseek-v4.1, deepseek-v4.1-flash]
price_in_per_mtok: 2.0
price_out_per_mtok: 8.0
currency: CNY
peak_windows:           # local-time windows that bill at full price
  - {tz: Asia/Shanghai, from: "08:30", to: "00:30"}
context_limit: 128000
max_concurrency: 2
```

`peak_windows` are **price windows, not availability**. A job is eligible
when the provider is either outside a peak window or the job cannot wait.

### Policy invariants (never configurable)

1. `sensitivity: sensitive` context **never** goes to `trust != verified`.
   Rationale: reseller channels may log or replay prompts.
2. `untrusted` output never applies directly. It may produce candidates or
   drafts, and always passes a human or a verified-model check.
3. `external_write` always requires approval.
4. Every routed decision records *why*: class, provider, window, price used.

### Routing algorithm

```text
eligible = providers where trust ≥ required(output_class, sensitivity)
                    and model supports template
                    and concurrency available
if job.deferrable and now < job.earliest_at: wait
cost(p, now) = tokens_estimate(p, template) × price(p, now)
pick = argmin cost over eligible, subject to:
         P0: latency first, then cost
         P1: eligible within deadline; else escalate trust
         P2/P3: if outside all off-peak windows and wait_ok: defer to next window
```

Deferral is durable: the job row stores `earliest_at`, `deadline`, and
`deferred_until`, so a restart does not lose the decision. The scheduler wakes
at the earliest deferred time — still a timer, but a *decision* timer rather
than "do work every 24 h".

**Failure escalation:** if the chosen provider errors or returns a low-quality
result (schema-invalid, empty, or a reviewer rejects it), retry once with the
next eligible provider and record both attempts. Never silently downgrade
trust to save money.

## 5. Token ledger and dashboard

**Implemented** as a per-run `usage` block inside the run record, not a
separate ledger. The run record is already immutable, pruned, and read without
the repository lock, so it is a sufficient store for a single-user deployment.

```json
{"run_id":"…","trigger":"hook","runner":"codex","model":"gpt-5.1-codex",
 "status":"pushed","duration_ms":41230,
 "usage":{"input_tokens":6621,"cached_tokens":8064,"output_tokens":15,
          "total_tokens":14700,"requests":1,
          "source":"reported","origin":"stdout"},
 "started_at":"…","finished_at":"…"}
```

Semantics, chosen so the numbers mean the same thing across runners:

- `input_tokens` — uncached prompt tokens actually billed.
- `cached_tokens` — the cached part of the prompt, billed at a lower rate.
- `output_tokens` — completion tokens.
- `total_tokens` — the sum the provider bills: uncached + cached + output.

Capture points (the worker, not the service, because that is where the CLI runs):

- **Codex**: `codex exec --json` prints a `turn.completed` event carrying
  `usage`; the values are cumulative for the turn, so the last event wins.
  Verified against codex-cli 0.162.1 with the exact production flag set.
- **DSH**: the JSONL stream carries `chunk.type = "usage"` per request; totals
  are sums, not a last-wins value. `requests` counts them.

**Honesty rule, enforced by absence.** A run that never called a model has no
`usage` key at all. A run that called a model but got no report also has none.
The dashboard prints `reported_runs` and `unreported_runs` side by side instead
of turning a missing number into a zero. There is no character-based estimate:
the design note allowed one, but an estimate nobody validates is worse than an
admitted gap, so it is dropped until a real need appears.

Dashboard (`GET /api/usage?days=N`, 1–365, default 30): total in/cached/out,
per-day series, per runner/model table, and the reported/unreported split. The
window only counts runs whose `started_at` falls inside it.

**Deferred, deliberately:**

- **Cost.** Requires a price table and currency; the provider catalog changes
  too fast to hard-code, and the owner asked for it to be runtime-editable
  data. Until that data exists, the dashboard shows tokens, not money.
- **Priority, peak windows, budget policy.** They need the same policy data
  plus a router; nothing consumes them yet, so building the matrix first would
  be speculative.
- **Job/trigger breakdown.** Every run today is the same job. The dimension
  becomes meaningful when a second action exists.

## 6. Surfaces

`notify.Channel` contract: `Send(ctx, Message) Receipt`; outbound only for the
first channels. Adding a channel must not touch job code.

| Channel | Transport | Inbound | Notes |
| --- | --- | --- | --- |
| Bark | `POST {server}/push` JSON | no | iOS push, self-hostable; **implemented first** |
| DingTalk | bot webhook + HMAC sign | no | custom robot; markdown/actionCard |
| Feishu | bot webhook + sign | optional | outbound first |
| WeCom | group robot webhook | no | corporate WeChat |
| Telegram | Bot API | yes | pairs with the chat surface |

**WeChat reality check.** Personal WeChat has no official bot API. Third-party
bridges (wechaty-class) violate the terms of service and risk account bans;
they are not a supported integration. Only 公众号 and 企业微信 have sanctioned
APIs. Plan accordingly: WeCom for work, 公众号 for public-facing, Bark/DingTalk
for personal push.

Bark specifics that matter: device key is a credential (private volume, never
returned by the API, never logged); level `critical` needs an entitlement, so
default to `active`; a 200 response with `code != 200` is still a failure;
truncate bodies rather than letting the server reject them.

## 7. Concurrency prerequisite

`Store.withLock` serializes *everything*. With multiple job kinds that becomes
the bottleneck and a deadlock risk (a long agent run blocks a sync request).

Replace with resource locks acquired in a fixed order:

- `repo:context` — pull, checkout, commit, push
- `github:api` — rate-limit aware client
- `provider:<id>` — per-provider concurrency limit
- `job:<template>` — per-template single-flight where required

A job takes only the locks its template declares. Notifications run outside all
locks.

## 8. Sequencing

1. **Bark channel** (implemented): config, masked state, test send, delivery
   on pushed/failed runs, bounded delivery ledger, Web UI card.
2. **Hook API** (implemented): per-hook secret, HMAC over timestamp and raw
   body, replay window, rate limit, `maintenance` action, masked state, Web UI.
3. **External watcher** (implemented as scripts): `ls-remote` probe, SHA as
   event id. Replaces the nightly schedule for the context repository.
4. **Usage capture and dashboard** (implemented): runner-reported per-run
   usage, `/api/usage`, Web UI over 7/30/90 days. Cost waits for a maintained
   price table.
5. **Task queue** (implemented): durable tasks, priorities, off-peak window,
   global and per-task pause, run-now, single-task dispatcher.
6. **Integration plugins** (implemented): the hook contract in
   `docs/integration-plugins.md`, payload projection, and the GitHub bridge as
   the reference plugin. Folio no longer logs in to GitHub or lists
   repositories; it holds only the context-repository git credential.
7. **GitHub actions**: issue triage and PR review draft, approval-gated. This
   is also the first task kind with a payload, so the projection rule in §9
   must be settled first.
8. **Price-aware routing**: provider price data, peak windows, trust rules.
9. **More channels**: DingTalk → Feishu → WeCom.

Each slice is independently useful and testable; none requires the next.

## 9. Owner decisions (2026-10-23)

- **Inbound reachability**: an external watcher is acceptable, so hook calls may
  originate outside Folio. The hook API is the primary trigger; Folio itself
  never polls.
- **Provider catalog**: the list changes too fast to freeze in code. Prices,
  trust, and peak windows must be runtime data the owner edits, and cost output
  must stay explicitly optional — see §5. Do not build routing before the data
  exists.
- **Unattended policy**: too abstract to decide up front. Start with one
  concrete action (`maintenance`) and add actions one at a time; no template
  registry, no approval matrix until a second real case appears.
- **Scope**: single-user deployment. No multi-tenant features, no per-channel
  locale matrix. Skip work that only pays off at scale.

One thing the hook API deliberately does not do yet: carry data into the run.
The body is signed and bounded, but only its authenticity is used; the action is
fixed per hook. When an event needs a payload (an issue number, a PR id), the
template must project whitelisted fields instead of interpolating raw input.
