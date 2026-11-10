# Folio

Folio is a self-hosted personal assistant, task center, and owner-controlled
digital counterpart. It turns owner intent and trusted events into bounded
tasks, uses private personal context when needed, executes configured runtime
actions, and returns evidence through the Web console or optional channels.
See the [product definition](docs/product-definition.md) for the shared product
language and boundaries.

Automatic personal-context organization is Folio's first shipped assistant
scenario: section cleanup preserving existing text, exact deduplication,
extractive summaries and local navigation, with conflicts and candidate
decisions left for owner review. See [the maintenance contract](docs/context-maintenance.md)
for its scope, incremental checkpoints and acceptance checks.

It provides a Web UI for repository setup, task management, assistant chat,
configured actions, usage and owner review. The maintenance worker can pull
the latest checkout, ask a model
runner for bounded Markdown edits, validate them, commit, and push. Every run is
recorded with its changes, reasons, and diffs for later inspection.

The service never runs code from the cloned context repository. Personal
context, credentials, run audits, and runtime state stay in the private data
volume. This repository contains only generic service code and Web assets.

## Start using Folio

1. Create the owner account, then open **Settings → Model services**. Add a
   connection and assign a model to the assistant and/or reports. Chat and
   API-backed reports can work before a context repository or CLI runner is
   configured. A connection test lists models; complete an actual conversation
   or small report to check inference support.
2. The dashboard is an **action catalog**, not a fixed set of daily buttons.
   It reads `GET /api/catalog`; the built-in report and maintenance actions and
   any owner-defined `actions.json` entries appear there. Submitting a card
   creates durable task intent. Its task card shows queue state, waiting rules,
   attempts and results; creation is not completion.
3. Use **Assistant** for conversation. Open **Feedback** from the fixed launcher
   to report an improvement request about Folio itself. The screenshot-first
   floating window can capture the current tab in one click, or accept a pasted,
   dropped or selected PNG/JPEG; it attaches the current view and a bounded set
   of visible page elements automatically. Annotate with pen, arrow or rectangle
   tools, describe what happened and what you want, and open **More** only if
   the derived title, priority or acceptance needs editing. Closing the window
   keeps the draft. Vague or incomplete requests are rejected with a fixed
   reason; an accepted request currently creates a bounded planning task, not a
   source edit or deployment.
4. For personal-context answers and organization, authorize GitHub and connect
   the private Markdown repository. Organization also needs the Codex worker
   and its configured model route. Missing optional channels do not block chat
   or the task board.
5. Open a completed report from its task or run, then use the follow-up entry
   to ask the assistant about that run. New reports retain a separate artifact
   for each attempt. Older reports may expose only a marked, potentially
   truncated audit snapshot; an overwritten task file is never presented as an
   exact historical result. Context answers should cite the source paths
   returned by search; unavailable context must stay explicit.

Action presentation is owner data. An `actions.json` entry may add a
`description`, `requires` capability names, and a `presentation` object with
`group`, `order`, `submit_label` and validated form fields; see
[docs/self-bootstrap.md](docs/self-bootstrap.md) for the shape and the
not-yet-shipped source workshop boundary.

The settings and dashboard show separate readiness for assistant, reports and
maintenance. Readiness describes local configuration, not verified credentials
or a successful model call. Queue pause and off-peak policies still apply to
background tasks. Deployment and optional notification setup are described in
[deploy/README.md](deploy/README.md).

## Image

`ghcr.io/rwdai-labs/folio-service:main` (AMD64 and ARM64).

For NAS deployment, see [deploy/README.md](deploy/README.md).

## Build and test

Requirements: Go 1.24+; Docker is optional for the container build.

```bash
go test -race ./...
go vet ./...
go build -o /tmp/folio ./cmd/folio
docker build -f deploy/Dockerfile -t folio-service .
```

## Layout

```text
cmd/folio/      service entrypoint and CLI
internal/       context router, maintenance worker, Git/GitHub store, Web server
internal/quota  optional bounded subscription-allowance source for the dashboard
internal/notify outbound push channels (Bark today); no inbound traffic
internal/channel native inbound/outbound communication (Weixin text MVP)
scripts/        external watcher and hook client; not part of the service
plugins/        built-in capability plugins compiled into the binary (GitHub),
                plus a separate external bridge module for issue events
web/            static Web UI served by the Go binary
deploy/         Dockerfile, entrypoint and deployment notes
docs/           design and research notes; no runtime behaviour
compose.yaml    NAS Compose example
```

## Security boundary

The image is public and must never contain canonical context, inbox contents,
Git history, generated packets, reports, credentials, or runtime volumes. The
service clones a private repository only after owner authorization; the
checkout, run audits, and secrets live in separate private volume directories.
Scheduled jobs pull first, then accept at most eight Markdown files from the
runner, reject sensitive or non-canonical paths, verify that the checkout
contains only those edits, commit, and push. A failed validation or push is
rolled back and recorded; it never leaves a partial commit. Automatic runs use
Codex by default. The daily UI runs maintenance using its saved task binding;
model/group IDs remain free-form in Settings → Model connections. Existing
Codex/DSH API and CLI selections remain available for legacy configurations;
DSH needs an installed headless profile. See `deploy/README.md` for model-service,
DSH and host-config setup.

Each run stores status (`pushed`, `no_changes`, `skipped`, `failed`), runner and
model, pull and push evidence, commit, summary, per-file reasons and diffs under
the private runtime directory. The Web UI exposes the latest run, history, and
exact diffs.

Work starts from events, not a clock. `POST /api/hooks/{id}` accepts a signed
event and starts one maintenance run: HMAC-SHA256 over the timestamp, a dot, and
the exact raw body, within a five-minute window, with event-id replay protection
and a per-hook rate limit. Each hook has its own 256-bit secret, shown once and
never returned again; unsigned, stale, or replayed calls are refused before any
state changes, and only a signature check can consume replay or rate budget.
Folio never polls Git for changes — `scripts/folio-watch-git.sh` runs outside the service,
compares `git ls-remote` HEAD, and calls `scripts/folio-hook.sh` only when the
commit actually changes, using the commit SHA as the event id. Folio compares
document fingerprints after pulling, so its own maintenance commit does not
cause another model call. The interval scheduler is also available; queued
hook attempts are recorded with `trigger: task`. Hook callers pass the same host
allowlist as the browser UI, so `TRUSTED_HOSTS` must include the address the
watcher connects to (for example `127.0.0.1` when it runs on the same host).

Events create tasks, not runs. A task is durable intent with a priority and a
status; a run is one attempt at it. P0 and P1 start immediately, P2 and P3 wait
for the off-peak window from `policies.json` (default `00:30–07:30` local), and
the dispatcher starts one task at a time so the checkout and commit stay
single-writer. The owner can pause the whole queue, hold one task, run it now,
or cancel it; pausing never discards work. A task interrupted by a restart is
marked failed rather than left phantom-running.
Large maintenance backlogs automatically queue bounded continuation tasks.
Changes received during a running maintenance task coalesce into one queued
follow-up. Run details show the original input fingerprints, remaining
documents and persistent findings that need owner review. Unchanged inputs
need no model call; validation and push failures do not advance progress.

Task kinds are data, not code. `data/task-kinds.json` lists them; the hook form
and the task board read the same file. A kind names an `action` Folio implements
and, for `report`, a prompt and an optional runner/model:

```json
{"id": "triage", "label": "Issue triage", "action": "report", "priority": "P2",
 "prompt": "Triage this issue: {{title}} - {{payload.body}}"}
```

The built-in actions are `maintenance` (pull, organize, commit, push) and
`report` (one read-only model call, a Markdown report in `runtime/reports/`,
usage in the run ledger, a notification, and no repository write). A new kind is
a JSON edit and a Web UI reload; only a genuinely new capability needs Go. An
unknown kind is rejected at hook creation and again at dispatch, never guessed.

`data/actions.json` v2 adds owner-defined runtime actions: an ordered chain of
`context`, `agent`, `script`, `push`, `http` and `task` steps. The registry is
validated and versioned before a write; scripts run as argv under
`/data/scripts` with no shell, and HTTP steps require the configured HTTPS host
allowlist. The Advanced action editor can validate, publish, disable, restore
versions and
edit the host allowlist; the Assistant can edit the same registry when an owner
runtime is configured.
Source code and deployment remain outside the agent's write surface.

Token usage is recorded per run from what the runner itself reported: Codex via
its `turn.completed` JSON event, DSH via its per-request `usage` chunks, and the
OpenAI-compatible agent via the API's `usage` object. The record separates
uncached, cached, and output tokens, and a run that never called a model carries
no usage at all — the dashboard shows how many runs reported and how many did
not instead of estimating the difference. Task details also show an estimated
cost when the model and provider match the public [models.dev](https://models.dev)
catalog (`https://models.dev/api.json`); the catalog is cached under the private
runtime and refreshed automatically in the background. The estimate uses list prices per
million tokens and excludes cache writes, context-length tiers and subscription
discounts, so it is explicitly labelled estimated. Unknown models keep token
counts and show “price unavailable” rather than a guessed number. The window is
`GET /api/usage?days=7|30|90`.

The Web UI can also show subscription allowances from an optional
[magpie](https://github.com/yetone/magpie) gateway. `data/quotas.json` names the
private/local gateway and a dedicated key file under `data/secrets/` (0600).
Folio reads `GET /v1/magpie/quotas?kept=1`, normalizes provider, account,
window, reset, read time, and error cards, and shows them read-only. The panel
never routes a run, changes a task, returns the key or URL, or invents a missing
reading: a failed source keeps the last successful cards visible with a stale
marker, and absent configuration shows a setup hint. `GET /api/quotas` is
session-protected; `POST /api/quotas/refresh` also requires CSRF and is limited
to one refresh every 30 seconds. See
[docs/subscription-quota.md](docs/subscription-quota.md) for the control-plane
design and the phases that are not implemented.

Outbound push is optional and strictly one-way. The operator configures a Bark
server and device key in the Web UI; the key is stored in the private volume
with mode 0600 and is never returned to the browser or written to the delivery
ledger. A run that pushes or fails sends at most one bounded message, and the
ledger records only kind, channel, status, title, and time — never the message
body. Plain HTTP is accepted only for loopback or private-network servers,
because a device key sent over the public internet is a credential leak.
Additional channels reuse the same outbound contract.

Personal Weixin is a separate optional bidirectional channel, implemented in
native Go through Tencent's public iLink protocol. The settings page can run the
QR login, show the scanner's identity for confirmation, and start or stop
polling without a restart; a terminal login and pinned environment variables
remain available for unattended deployments. Credentials and the cursor stay in
the private volume. Only the owner confirmed in the console, or pinned by the
deployment, can reach Folio's read-only text handler; other users are denied.
A connected repository contributes a bounded, read-only context packet to each
answer, a failing model run reports a fixed failure class instead of a generic
apology, and the owner may opt in to receiving finished-run pushes in WeChat.
See [docs/weixin-channel.md](docs/weixin-channel.md) for protocol research,
setup, recovery guarantees and deferred media support.

A maintenance runtime sandbox is not an OS boundary against every readable file. Keep
unrelated secrets off the service account and volume, and expose the Web UI only
through loopback, an SSH tunnel, or an authenticated HTTPS reverse proxy.

On the first start that has no owner account, the console shows a setup form
from the local or private network. The owner chooses a username and password
(8–200 characters, at least three of uppercase, lowercase, digits and symbols);
only the PBKDF2 hash is stored in the private SQLite database at
`/data/folio.db` (mode 0600). There is no default password, generated password,
or printed setup code. Sessions last 30 days of activity with a 90-day absolute
limit, and survive service restarts.

An existing `/data/secrets/admin-password` or `admin-password.hash` from an
older deployment is imported once as the `admin` account (the plaintext file is
removed); `ADMIN_PASSWORD_FILE` can seed that legacy import for an unattended
deployment. After setup the database is authoritative.

## Model services and task assignments

Settings → Model services manages reusable named OpenAI-compatible connections
(base URL and a write-only API key). Assign a connection and an exact model ID or
routing-group ID such as `group/reports` separately to assistant chat, reports,
and context maintenance. Individual report kinds and runtime actions can override
that report default. Folio dispatches to the configured route; account selection,
model fallback and cost preferences belong to the gateway. Task priority still
controls execution order and the owner's off-peak window.

Configuration is private at `data/runtime/model-services.json`; keys live in
`data/secrets/model-services/` with mode 0600 and are never returned. HTTPS is
supported; plain HTTP is limited to loopback or private IP endpoints. Testing a
connection lists its available model IDs without an inference call, so a passing
connection test does not prove that a model or group can complete a task.
Unassigned uses retain the existing runner or agent configuration; malformed
saved model-service configuration fails closed.

Reported usage remains the source of token counts. A gateway may return actual
served provider/model metadata; Folio keeps it distinct from the requested group
for cost estimation. An unresolved group has no invented price. The homepage
work card begins with a compact token, estimated-cost and subscription-status
bar; detailed usage and subscription readings remain expandable.

## Credentials and integrations

Folio keeps the checkout, the model run, the commit, and the audit trail. The
code host is a **built-in plugin**: `plugins/github` owns the remote, the
credential, and the sign-in flow. A storage plugin can be replaced (a network
drive, an S3 bucket) without touching the maintenance worker.

Two ways to authenticate, both first class:

1. Sign in on the Web page (device code → `gh auth login`). This is the
   original flow and the one to use when you want the full experience. When
   GitHub issues an expiring OAuth token, the plugin keeps the rotating refresh
   token in the private volume and renews the access token automatically; only
   a sign-out or a rejected refresh asks for a new device sign-in.
2. Set `GIT_AUTH_TOKEN` for a non-interactive deployment. The token is written
   once to `data/secrets/git-credentials` (0600) and used through
   `git-credential-store`; it never enters a model environment or a command
   line. A fine-grained token only exposes the repositories it was granted, so
   the picker can list fewer private repositories than the account owns; use
   the `gh` sign-in or import an existing `gh` login when the full list is
   required.

The GitHub card and the repository picker are the plugin's own frontend,
mounted at `/plugins/github/` and `/api/plugins/github/`. The repository card
has one way in: sign in to GitHub once, search the repository list (contiguous
or fuzzy fragments, keyboard selection), and connect. Private repositories are
marked and listed from every page the credential can read, and the picker
reports how many were loaded; a truncated list says so instead of silently
hiding entries. There is no owner/name form of its own; the shell keeps the
connect transaction (`/api/repository/connect`) and the plugin drives it.

The console separates daily work from setup. The default page after repository
setup is a dashboard with compact current work, server-driven quick actions and
Token usage; the full task board, run history and owner review live together
under **Activity** with Tasks, Runs and Review tabs. The board groups waiting
work, running attempts, items needing attention, and finished work into columns,
with local search and priority filters. Cards explain waiting or failure and open
run evidence in a side dialog: runner/model, reported tokens, changes and diffs.
Human-review snapshots are labelled separately from failed tasks. Run history is
bounded to a compact recent window with status filters and an explicit Show more;
GitHub authorization, repository selection, model connections, push
notifications, event hooks and advanced action tooling are grouped into Settings
tabs. On an unconfigured installation the console starts in Settings, with a
setup checklist; notifications and triggers are optional. Runner installation is
reported separately from provider authentication, which the first model run
checks.

The console also has an Assistant chat and a server-driven quick-action
catalog. Daily actions open a small form dialog from compact launchers; the raw
registry lives under Settings → Advanced and is not part of normal navigation.
Chat goes to the main-agent runtime configured under Settings → Advanced. The
built-in adapter speaks the OpenAI-compatible chat-completions API (base URL,
model, provider, key), so DeepSeek, OpenAI, OpenRouter and compatible gateways
work without an extra sidecar; an installed command adapter at
`/data/agent/runtime` remains available for a pi wrapper. The key is stored in
the private volume and never returned to the browser. Until a runtime is
configured the view reports unavailable and Weixin keeps its bounded read-only
answer. The agent's tool surface is queue/action/route/policy/channel/context/
audit control, not source or deploy. Actions can be validated, published,
disabled, and restored from retained versions; the same operations are exposed as
agent tools.

Owner review lives in **Activity → Review** and shows the findings from the most
recent retained successful
maintenance, with a timestamp. It is a historical snapshot, not a live approval
queue; reports and failed runs do not replace it. The shared handling workflow is
collapsed behind help; each finding explains the next
step and can link to its input version and current file on GitHub. Older runs
without repository identity cannot supply a historical source link. Review
happens in the private repository: read the evidence, record a decision, and make
any intended canonical edit explicitly, then commit and push. Changing a
candidate's status alone does not promote it. The console offers no in-page
approval. **Pull latest and recheck** runs maintenance using the Activity
Runs/full board task binding and may consume tokens. Remaining changed documents are
shown separately from human findings, including whether a continuation task was
queued; pauses and off-peak policy still apply.

The header identifies the Folio console session. Repository selection and
GitHub authorization have separate statuses: a saved repository still needs
working GitHub credentials to sync or organize. When authorization is missing,
the repository card shows that access needs authorization and offers GitHub
sign-in. A configured deployment token still needs access to the selected
repository.

External triggers (a context watcher, a CI job, the GitHub issue bridge) are a
different class of plugin: they are separate programs that call the signed hook
API. See [docs/integration-plugins.md](docs/integration-plugins.md) and
[plugins/github-bridge](plugins/github-bridge/README.md).

## Related repository

The canonical context, local Python CLI, and agent integration live in a
separate private repository. This service consumes that repository's documented
layout at runtime; it does not vendor it.
