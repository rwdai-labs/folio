# External event clients

Inherit `../AGENTS.md`; use `../docs/integration-plugins.md` as the hook contract.

- `folio-hook.sh` signs and sends one event; `folio-watch-git.sh` probes a
  remote HEAD and calls the hook client on changes. These run outside Folio;
  keep polling out of the service.
- Keep POSIX `sh` compatibility and explicit dependencies. Sign the exact body
  transmitted, preserve timestamp/event headers, and return failure for an
  unsuccessful HTTP request. A retry must reuse a stable event ID.
- Advance watcher state only after successful hook acceptance. Do not conflate
  bridge baseline behaviour with this watcher's initial HEAD delivery.
- Keep secrets in private caller configuration. Do not add shell tracing or
  logging that exposes hook secrets, credential-bearing remotes or bodies.
- Changes to inputs, retry/state semantics or protocol require this file,
  script usage comments and integration documentation to be updated together;
  coordinate protocol changes with `internal/store` and `internal/web`.

From the repository root run `sh -n scripts/folio-hook.sh` and
`sh -n scripts/folio-watch-git.sh`. For behaviour changes exercise synthetic
events with a local fake server/remote, including unchanged HEAD, rejected
delivery and retry. Do not call a real configured hook during verification.
