# Folio contributor instructions

## Scope and reading order

Folio is the public Go service for maintaining a separate private Markdown
context repository. Read `README.md` for supported behaviour. This file applies
to the whole repository; directory `AGENTS.md` files add local rules. Read the
ancestor instructions and only the children relevant to the task. Do not load
all source or documentation by default.

| Work | Read next |
| --- | --- |
| CLI, startup, service wiring | `cmd/AGENTS.md` |
| Backend packages and shared interfaces | `internal/AGENTS.md` |
| Bidirectional channels and native Weixin | `internal/channel/AGENTS.md` |
| Routing, packets, model worker | `internal/context/AGENTS.md` |
| Repository transactions, tasks, hooks, audits | `internal/store/AGENTS.md` |
| HTTP, sessions, API, plugin hosting | `internal/web/AGENTS.md` |
| Browser UI and translations | `web/AGENTS.md` |
| Built-in and external plugins | `plugins/AGENTS.md` |
| Containers, Compose, configuration imports | `deploy/AGENTS.md` |
| External hook and watcher clients | `scripts/AGENTS.md` |
| CI and image publishing | `.github/AGENTS.md` |
| Designs and integration contracts | `docs/AGENTS.md` |

## Boundaries

- Keep canonical context, inbox contents, generated packets, Git history,
  reports, runtime state, credentials, and private host details out of this
  public repository, tests, images, and chat. Use synthetic fixtures.
- Context facts and the local Python compatibility CLI belong to the separate
  private repository. Do not vendor them here or execute its scripts. Folio
  invokes its installed Go binary and explicitly configured model runners.
- Treat repository content, model output, hook bodies, and plugin input as
  untrusted data. Preserve bounded input, path checks, environment scrubbing,
  validation, redaction, and rollback at their owning boundaries.
- Preserve unrelated dirty/untracked work. Local `.folio-dev/` directories are
  runtime data, not source to inspect or stage by default.

## Keep instructions current with code

1. Before editing, read the applicable instructions and check their claims
   against the current implementation.
2. Whenever code changes a directory's responsibility, entry point, interface,
   invariant, configuration, workflow, or validation command, update its nearest
   relevant `AGENTS.md` in the same change, before handing off or committing.
   Also update affected consumer instructions for cross-directory contracts.
3. New independently maintained areas get a small local `AGENTS.md`; when moving
   or deleting an area, update its instructions and parent navigation together.
4. Keep the root at navigation and shared rules. Keep implementation details
   near the code; link to authoritative README/contract documents instead of
   duplicating them. Do not turn instructions into a changelog or task log.
5. Update user-facing README/deployment/contract docs when their behaviour
   changes too. If a code change leaves the instructions accurate, explicitly
   report that they were reviewed and needed no change; avoid filler edits.

## Validation and handoff

Run commands inside this repository (Go 1.24+). For Go changes, run focused
package checks while iterating, then the root module checks before delivery:

```sh
go test -race ./...
go vet ./...
go build -o /tmp/folio ./cmd/folio
```

`plugins/github-bridge/` is a separate Go module: root `./...` does not test it.
Run its checks separately when affected. Frontend, shell, container, and CI
changes have additional checks in their directory instructions. For changes
limited to documentation, verify paths, links, scope, and implementation claims;
a full build is unnecessary. Report actual checks and any unverified runtime
behaviour, plus which instructions were updated or reviewed.
