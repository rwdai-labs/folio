# CLI and service composition

Applies to `cmd/`; inherit `../AGENTS.md`.

- `folio/main.go` owns command dispatch, environment/flag defaults, built-in
  plugin wiring, HTTP server lifecycle, health checks, scheduler and dispatcher
  startup. Context commands delegate to `internal/context.Run`.
- `folio/weixin.go` owns operator-only `weixin login|status`, terminal QR
  display and optional `WEIXIN_ENABLED` service wiring. Require an explicit
  `WEIXIN_OWNER_ID` before polling. Keep QR and scanner identity out of logs;
  credentials/state live under the external data directory. Login/status and
  the poller share a lifetime process lock; disabled installations make no
  Weixin requests. See `../docs/weixin-channel.md`.
- Keep business rules in the owning `internal/` package. Wire capabilities
  through the plugin/storage interfaces rather than importing plugin internals
  into the maintenance worker.
- Preserve graceful cancellation, HTTP timeouts, embedded timezone data, and
  bounded configuration. Ordinary failures must not print subprocess payloads
  or secrets. The first-start password notice is a deliberate exception:
  generate/announce once, reuse silently, and keep its tests and deployment
  documentation aligned.
- Context CLI `agent --auto` accepts `--maintenance-state` only in automatic
  mode and only within the external runtime. The service owns checkpoint writes;
  CLI workers emit validated structural proposals and optional review findings.
- Changes to flags, environment variables, startup or tool paths require
  corresponding updates to `../deploy/README.md` and configuration examples.
  Synchronize this file whenever these responsibilities or checks change.

Validate with `go test -race ./cmd/folio` from the repository root, then the
root Go checks. `dockerfile_test.go` guards build-context coverage; source or
packaging changes also follow `../deploy/AGENTS.md`. `ui_test.go` guards the
shell contract: every id `web/app.js` wires must exist in `web/index.html`, and
plugins load on both session paths.
