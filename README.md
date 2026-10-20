# Folio Service

A self-hosted maintenance service for a private Markdown context repository.
It provides a small Web UI for GitHub device authorization, repository cloning,
scheduled proposal generation, diff review, and explicitly confirmed
commit/push.

The service never runs code from the cloned context repository. Personal
context, credentials, proposals, and runtime state stay in the private data
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
internal/       context router, proposal worker, Git/GitHub store, Web server
web/            static Web UI served by the Go binary
deploy/         Dockerfile, entrypoint and deployment notes
compose.yaml    NAS Compose example
```

## Security boundary

The image is public and must never contain canonical context, inbox contents,
Git history, generated packets, reports, credentials, or runtime volumes. The
service clones a private repository only after owner authorization; the
checkout, proposals, and secrets live in separate private volume directories.
Scheduled jobs produce pending-review proposals. Only an authenticated owner
may preview and confirm a small canonical diff before commit and push.

The Codex sandbox is not an OS boundary against every readable file. Keep
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
