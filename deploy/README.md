# Folio Service container

Image: `ghcr.io/rwdai-labs/folio-service:main` (AMD64 and ARM64).
The public image contains the compiled service, Web assets, and runtime tools only. Personal context,
Git history, GitHub credentials and model credentials are downloaded or created
only at runtime in the private `/data` volume. A neutral name is not an access
control; keep Web authentication and the private network boundary.

## NAS deployment

Copy [`compose.yaml`](../compose.yaml) into a dedicated NAS directory.
The private source repository is not needed on the NAS. Then run:

```bash
docker compose pull
docker compose up -d
docker compose ps
```

Compose binds only `127.0.0.1:8080` on the NAS. From your workstation:

```bash
ssh -L 8080:127.0.0.1:8080 <nas>
```

Open `http://localhost:8080`. On the first start, when no administrator password
exists, the service generates one and prints it once to its log. Read it in your
own NAS terminal, never through chat or model tools:

```bash
docker compose logs folio
```

Look for the `administrator password:` line. It is emitted only on the start that
creates the password and is not repeated on later starts, but it stays in the
existing container log. The password is also stored in the private volume:

```bash
docker compose exec -u context folio cat /data/secrets/admin-password
```

Container logs are a credential source from that first start onward. Do not ship
them to a shared aggregator, and keep `docker compose logs` output private. To
keep the credential out of the log entirely, provide the password yourself
before the first start, for example with a secret manager file mounted and
`ADMIN_PASSWORD_FILE` set; the service then reuses that file and prints nothing.

Sign in to the local Web page, click GitHub sign-in, and complete the device
authorization on GitHub. Connect the private repository by `owner/name`.
The service runs installed image tools, never scripts from the cloned repo.

GitHub login and model login are separate. To configure Codex through its
official device login, run locally:

```bash
docker compose exec -u context -e CODEX_HOME=/data/codex folio codex login --device-auth
```

Alternatively provision the supported provider configuration and credentials
inside `/data/codex` through your secret manager. Do not put secrets in Compose,
the context repository or chat. Inspect the Web model status after setup.

The scheduler runs every 86400 seconds after startup and produces proposals
only. Review the exact diff in Web before confirming commit/push. Set
`MAINTENANCE_SCHEDULER_ENABLED` to `"false"` to disable it. Use only one scheduler
for this checkout. The service drops to UID/GID 10001 after initializing `/data`.

`folio-data` persists private data across container/image updates. Back it up
privately; do not run `docker compose down -v` when preserving data. To update:

```bash
docker compose pull
docker compose up -d
```

For an HTTPS reverse proxy, configure explicit `TRUSTED_HOSTS` and
`TRUSTED_ORIGINS` and enable `COOKIE_SECURE=true`. Do not expose the plain HTTP
port on the public internet. No host home directory or Docker socket is mounted.

## Package and build boundary

GHCR creates a new package for a new image name. Its owner must set that package
to **Public** once; package visibility does not carry over from an older image
name. The source repository is public; runtime data remains private. Existing
data volumes do not need to be renamed.

Verify actual anonymous download with an empty Docker configuration:

```bash
docker_config_dir=$(mktemp -d)
docker --config "$docker_config_dir" pull ghcr.io/rwdai-labs/folio-service:main
```

CI builds from the repository root with an explicit `.dockerignore` allowlist
and runs a runtime smoke test before publishing. Context, inbox, `.git`,
reports, runtime state, and secrets may not enter any image layer or public
artifact. BuildKit publishes SBOM and provenance attestations for release builds.
