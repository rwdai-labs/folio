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
