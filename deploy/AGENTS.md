# Container and deployment

Inherit `../AGENTS.md`; `README.md` documents operator setup. The root
`compose.yaml` is the base example; this directory contains the Dockerfile,
entrypoint and optional read-only host-config mounts.

- Preserve the public-image/private-volume boundary. Root `.dockerignore` is
  deny-by-default; allow source explicitly and never include private context,
  Git history, runtime data, reports or credentials in any image layer.
- Keep the multi-stage build and runtime tool pins/checks aligned with CI.
  Changes to source directories must still pass the build-context guard in
  `cmd/folio/dockerfile_test.go`; local Go compilation alone is insufficient.
- Entrypoint initialization may prepare `/data`, then must drop privileges.
  Import only the documented bounded host-config subset. Host mounts stay
  read-only and must never be recursively copied or chowned.
- Preserve persistent private data, loopback port exposure, health checks and
  secret-file permissions. Test with disposable volumes, never remove an
  operator's volume or print real first-start credentials into shared output.
- Weixin is console-managed by default: leave `WEIXIN_ENABLED`/`WEIXIN_OWNER_ID`
  unset so the settings page drives QR login and runtime start/stop. `false` is a
  hard kill switch; `true` plus an owner pins a headless deployment and fails
  startup without credentials. Keep the examples aligned with the CLI, which
  runs interactively as the service user against the same private volume while
  the poller is stopped. The runtime must include the Weixin/QR dependency
  license notice; no Weixin sidecar is needed.
- Model-service and Main-agent configuration described below belongs to the
  current legacy Go/HTTP/command execution paths. The approved SDK/ACP target is
  in `../docs/agent-architecture.md`, and packaging plans are in `README.md`.
  Main will be a built-in pi SDK host on Node.js or Bun with thin custom-tool IPC
  to Go; it is not an interchangeable ACP Task runner. Task model/agent execution
  will use ACP harnesses or verified, versioned adapters; business HTTP steps remain.
  Do not describe the current Node.js/Codex/DSH image as shipping that host or as
  proving ACP compatibility. Future packaging must pin host/SDK/harness/adapter
  versions, preserve license notices, isolate credentials and granted workspaces
  for file/shell/skill use, and smoke-test the actual runtime/protocol combination.
  Main workspace grants do not authorize production source/deployment edits or
  private Context scripts. Do not invent environment variables or install commands
  before the host/configuration contract exists.
- Generic model connections/task bindings live at `/data/runtime/model-services.json`
  and keys at `/data/secrets/model-services/<id>` (0600), not Compose or images.
  Settings assigns exact model/group IDs for assistant/report/maintenance and
  report/action overrides; gateway account/fallback/cost preference stays external.
  HTTPS and private-IP/loopback HTTP are allowed. Catalog tests do not prove
  inference support. Unbound uses preserve legacy runner/agent configuration.
  Maintenance uses a trusted per-call worker connection override and still
  requires Codex; never mutate or replace imported profiles for a binding.
- Main-agent configuration is owner data in `/data`, not Compose state. The
  settings form may write `/data/agent/config.json` and
  `/data/secrets/agent-api-key`; headless deployments may set
  `FOLIO_AGENT_KIND`, `FOLIO_AGENT_PROVIDER`, `FOLIO_AGENT_BASE_URL`,
  `FOLIO_AGENT_MODEL` and `FOLIO_AGENT_API_KEY_FILE`. Never put the key itself
  in Compose, the image or logs. The price catalog (`FOLIO_PRICING_URL`) is an
  optional external read and must not block startup or runs.
- Changes to environment/defaults, paths, dependencies or security settings
  require this file, deployment README, Compose examples, CLI wiring and CI
  expectations to be reviewed and updated together where affected.

From the repository root validate relevant Compose configurations with
`docker compose config --quiet` and, for the override,
`docker compose -f compose.yaml -f deploy/compose.host-config.yaml config --quiet`.
Run `go test ./cmd/folio`, build with
`docker build -f deploy/Dockerfile -t folio-service:local .`, then follow the
runtime smoke checks in `../.github/workflows/container.yml` using synthetic
configuration. State separately whether the image was built, started and tested;
local success does not prove a published image or deployed instance works.
