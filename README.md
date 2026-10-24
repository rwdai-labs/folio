# Folio Service

A self-hosted maintenance service for a private Markdown context repository.
It provides a small Web UI for GitHub device authorization, repository cloning,
and automatic scheduled maintenance: pull the latest checkout, ask a model
runner for bounded Markdown edits, validate them, commit, and push. Every run is
recorded with its changes, reasons, and diffs for later inspection.

The service never runs code from the cloned context repository. Personal
context, credentials, run audits, and runtime state stay in the private data
volume. This repository contains only generic service code and Web assets.

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
internal/notify outbound push channels (Bark today); no inbound traffic
scripts/        external watcher and hook client; not part of the service
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
Codex by default. An operator can trigger a run with Codex or DSH from the Web
UI when its headless profile is installed; both accept a selected model, and
the field remains free-form for ids not in the suggestions. Codex suggestions
come from the installed CLI catalog; DSH suggestions come from the actual
mounted profile patch. See `deploy/README.md` for DSH and host-config setup.

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
Folio never polls — `scripts/folio-watch-git.sh` runs outside the service,
compares `git ls-remote` HEAD, and calls `scripts/folio-hook.sh` only when the
commit actually changes, using the commit SHA as the event id. The interval
scheduler still exists but is no longer the intended trigger; runs started by a
hook are recorded with `trigger: hook`. Hook callers pass the same host
allowlist as the browser UI, so `TRUSTED_HOSTS` must include the address the
watcher connects to (for example `127.0.0.1` when it runs on the same host).

Events create tasks, not runs. A task is durable intent with a priority and a
status; a run is one attempt at it. P0 and P1 start immediately, P2 and P3 wait
for the off-peak window from `policies.json` (default `00:30–07:30` local), and
the dispatcher starts one task at a time so the checkout and commit stay
single-writer. The owner can pause the whole queue, hold one task, run it now,
or cancel it; pausing never discards work. A task interrupted by a restart is
marked failed rather than left phantom-running.

Token usage is recorded per run from what the runner itself reported: Codex via
its `turn.completed` JSON event, DSH via its per-request `usage` chunks. The
record separates uncached, cached, and output tokens, and a run that never
called a model carries no usage at all — the dashboard shows how many runs
reported and how many did not instead of estimating the difference. Cost is not
shown yet: it needs a price table the owner maintains, and a wrong price is
worse than no price. The window is `GET /api/usage?days=7|30|90`.

Outbound push is optional and strictly one-way. The operator configures a Bark
server and device key in the Web UI; the key is stored in the private volume
with mode 0600 and is never returned to the browser or written to the delivery
ledger. A run that pushes or fails sends at most one bounded message, and the
ledger records only kind, channel, status, title, and time — never the message
body. Plain HTTP is accepted only for loopback or private-network servers,
because a device key sent over the public internet is a credential leak.
Additional channels reuse the same outbound contract.

A maintenance runtime sandbox is not an OS boundary against every readable file. Keep
unrelated secrets off the service account and volume, and expose the Web UI only
through loopback, an SSH tunnel, or an authenticated HTTPS reverse proxy.

On the first start that has no administrator password, the service generates a
random 256-bit password, stores it in the private volume with mode 0600, and
prints it once to stderr so the operator can sign in without exec access. Later
starts reuse the file and print nothing. Container logs therefore hold a
credential from the first start onward: keep them private, and provide the
password file yourself (or mount it via `ADMIN_PASSWORD_FILE`) when logs leave
the host.

## Related repository

The canonical context, local Python CLI, and agent integration live in a
separate private repository. This service consumes that repository's documented
layout at runtime; it does not vendor it.
