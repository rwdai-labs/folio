# Folio Service container

Image: `ghcr.io/rwdai-labs/folio-service:main` (AMD64 and ARM64).
The public image contains the compiled service, Web assets, and runtime tools
only. Personal context, Git history, GitHub credentials and model credentials
are downloaded or created only at runtime in the private `/data` volume. A neutral name is not an access
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

The service needs one credential for the private repository: a GitHub HTTPS git
token. Put it in `.env` next to `compose.yaml`:

```bash
GIT_AUTH_TOKEN=github_pat_...
```

Use a fine-grained token limited to the context repository with contents
read/write. The service writes it to a private credential file (mode `0600`) and
never prints it. An existing `gh` session inside the data volume still works as
a compatibility fallback, so an upgraded deployment keeps pushing without a
change, but a fresh deployment should use the token.

Sign in to the local Web page and connect the private repository by typing
`owner/name`. Folio no longer lists repositories or signs in to GitHub: that is
an integration plugin's job. The service runs installed image tools, never
scripts from the cloned repo.

GitHub login and model login are separate. To configure Codex through its
official device login, run locally:

```bash
docker compose exec -u context -e CODEX_HOME=/data/codex folio codex login --device-auth
```

Alternatively provision the supported provider configuration and credentials
inside `/data/codex` through your secret manager. Do not put secrets in Compose,
the context repository or chat. Inspect the Web model status after setup.

## Maintenance runners

Scheduled maintenance uses Codex. The Web UI can also start a run immediately
with either runner; Codex remains the default.

- **Codex** is the existing path: use the device login or provider configuration
  described above. The Web UI suggests models from the installed Codex catalog
  and also accepts a typed model id.
- **DSH** runs the image's `dsh` CLI with its `headless` profile. It can use
  either a provider credential file at `/data/dsh/.env` (mode `0600`, for
  example `DEEPSEEK_API_KEY=...`) or the credentials saved by the host DSH
  installation. The model selector reads the actual `llm-pi-ai` providers and
  models declared by a mounted DSH profile; it has no built-in model list. A
  selected model is applied through a temporary one-process profile patch that
  also carries the selected provider's declaration, so the host profile is
  never modified. The field remains free-form for a model id not in the
  mounted profile. Set `DSH_MODEL_PROVIDER` to override the provider used for a
  typed id. Leave the field blank to keep the profile default.

  The runner stays read-only; model credentials never enter the request, a
  maintenance report or the repository. The first DSH run initializes
  `/data/dsh/profiles/headless`, so allow network access to the provider. Do
  not put real credentials in Compose, the context repository or chat; create
  the file through your secret manager or a private shell, never through a
  shared log.

### Reuse the host Codex/DSH/GitHub configuration

The optional [`compose.host-config.yaml`](compose.host-config.yaml) override
mounts `~/.codex`, `~/.dsh`, and `~/.config/gh` read-only at `/host/codex`,
`/host/dsh`, and `/host/gh`:

```bash
docker compose -f compose.yaml -f deploy/compose.host-config.yaml up -d
```

The entrypoint imports only a bounded configuration subset into the private
`/data` volume before dropping privileges:

| Host source | Imported into | Purpose |
| --- | --- | --- |
| `~/.codex/auth.json`, `config.toml`, `models_cache.json` | `/data/codex/` | Existing Codex sign-in and provider routing |
| `~/.dsh/.credentials.yaml` | `/data/dsh/.credentials.yaml` | Stored DSH provider keys |
| `~/.dsh/.env` | `/data/dsh/.env` | DSH provider environment fallback |
| `~/.dsh/profiles/*/cordis.patch.yml` | `/data/dsh/imported/profiles/` | Actual DSH model/provider catalog |
| `~/.config/gh/hosts.yml` | `/data/secrets/gh/hosts.yml` | Existing GitHub CLI login, including private-repository access |

History databases, sessions, plugins, and `node_modules` are not copied. The
host directories are mounted read-only and are never chowned; only `/data`
changes ownership. The service discovers imported profile patches
automatically (preferring `web`, then `headless`, then another profile).

To enable the imported stored DSH credentials, the override sets
`DSH_CREDENTIALS: stored`. This restores the headless profile's credential,
authorization, and DeepSeek-account rows for the one-process overlay. Without
that setting, headless mode keeps its normal behavior and uses environment /
`.env` credentials only. Remove the setting if you provide credentials through
a secret manager instead.

For a custom location, set `CODEX_CONFIG_HOME` / `DSH_CONFIG_HOME` /
`GH_CONFIG_HOME` to the mounted directory, or set `DSH_CONFIG_PATCH` to one
specific `cordis.patch.yml`. An explicit `DSH_CONFIG_PATCH` wins over profile
discovery. If `~/.config/gh/hosts.yml` is imported, the GitHub card detects the
existing login on the next state refresh and the repository selector includes
private repositories visible to that login.

The scheduler runs every 86400 seconds after startup. Each tick pulls the
latest checkout, asks the runner for bounded Markdown edits, validates and
pushes them, and records the run. Set `MAINTENANCE_SCHEDULER_ENABLED` to
`"false"` to disable automatic runs; the Web UI still allows an immediate run.
Use only one scheduler for this checkout. The service drops to UID/GID 10001
after initializing `/data`.

Run audits are private: `/data/runtime/runs` holds at most 40 JSON records with
status, pull/commit/push evidence, summary, and per-file diffs. The Web UI shows
the latest run, history, and exact diffs to the authenticated owner. Tune
`MAINTENANCE_INTERVAL_SECONDS` to change the schedule.

`folio-data` persists private data across container/image updates. Back it up
privately; do not run `docker compose down -v` when preserving data. To update:

```bash
docker compose pull
docker compose up -d
```

### Optional outbound proxy

Compose optionally passes `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` into the
container. It accepts either uppercase or lowercase input and supplies both
spellings for tools that expect Git's lowercase names. When no proxy is
configured, the HTTP and HTTPS proxy values are empty and outbound clients
connect directly. The current custom GitHub transport does not evaluate
`NO_PROXY`, and the `gh`/`git` child environment does not pass a standard
comma-separated bypass list, so this setting is not a complete bypass for
GitHub or Git operations. The health check targets `http://127.0.0.1:8080/` and
stays local to the container loopback interface.

For a temporary proxy configuration, create the local `.env.proxy` file. It is
ignored by Git and must not be committed:

```dotenv
HTTP_PROXY=http://proxy.example.invalid:8080
HTTPS_PROXY=http://proxy.example.invalid:8080
NO_PROXY=localhost,127.0.0.1
```

Replace the example with a proxy address reachable from the container. A
container's `127.0.0.1` is the container itself, so it cannot reach a proxy
running on the NAS host through that address; use a host name or LAN address
that the container can route to. Recreate the service after changing the env
file:

```bash
docker compose --env-file .env.proxy pull
docker compose --env-file .env.proxy up -d --force-recreate
```

For an HTTPS reverse proxy, configure explicit `TRUSTED_HOSTS` and
`TRUSTED_ORIGINS` and enable `COOKIE_SECURE=true`. Do not expose the plain HTTP
port on the public internet. The base Compose file mounts no host home
directory or Docker socket. The optional host-config override mounts only
`~/.codex`, `~/.dsh`, and `~/.config/gh` read-only and imports a bounded subset
into `/data`.

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
