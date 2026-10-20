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
web/            static Web UI served by the Go binary
deploy/         Dockerfile, entrypoint and deployment notes
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
the field remains free-form for ids not in the suggestions. See
`deploy/README.md` for DSH setup.

Each run stores status (`pushed`, `no_changes`, `skipped`, `failed`), runner and
model, pull and push evidence, commit, summary, per-file reasons and diffs under
the private runtime directory. The Web UI exposes the latest run, history, and
exact diffs.

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
