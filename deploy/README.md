# Folio container

Folio is a self-hosted personal assistant, task center, and owner-controlled
digital counterpart. The private Markdown context repository is its memory
foundation; context maintenance is one capability within the wider task and
assistant control plane. See the [product definition](../docs/product-definition.md)
for the shared product language.

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

Open `http://localhost:8080`. On the first start with no owner account, the
console shows a setup form. It is accepted only from loopback or a private
network; choose a username and a password (8–200 characters, at least three of
uppercase, lowercase, digits and symbols). Only the PBKDF2 hash is stored in
`/data/folio.db` (mode 0600); there is no default password,
generated password, or printed setup code.

If you need unattended bootstrap, mount an `ADMIN_PASSWORD_FILE` before the
first start; it is imported once as the `admin` account and then removed. The
same one-time import applies when upgrading a volume that already contains
`secrets/admin-password` or `secrets/admin-password.hash`.

The database is the auth source of truth. Back it up privately with SQLite's
backup command, for example `sqlite3 /data/folio.db "VACUUM INTO '/data/backup.db'"`,
instead of copying the WAL files while the service is writing. Keep the SQLite
file on a local volume, not on NFS/SMB. Sessions last 30 days of activity with a
90-day absolute limit and survive restarts.

The service needs one credential for the private repository. There are two
first-class paths:

**Sign in on the Web page.** Open the GitHub card and enter the device code at
`github.com/login/device`. The plugin stores the token through `gh`, so git and
the API share it, and the repository picker lists your repositories. When
GitHub issues an expiring OAuth token, the plugin keeps the rotating refresh
token in the private volume and renews it automatically; the owner signs in
again only after a sign-out or a rejected refresh.

**Or set a deployment token**, for an unattended deployment. Put it in `.env`
next to `compose.yaml`:

```bash
GIT_AUTH_TOKEN=github_pat_...
```

Use a fine-grained token limited to the context repository with contents
read/write. The plugin writes it to a private credential file (mode `0600`) and
never prints it. `gh` stays in the image because the device flow and the git
credential helper use it.

Sign in to the local Web page and connect the private repository through the
searchable picker. The GitHub card and the picker are served by the built-in
GitHub plugin; the service itself runs installed image tools, never scripts
from the cloned repo.

GitHub login and model login are separate. To configure Codex through its
official device login, run locally:

```bash
docker compose exec -u context -e CODEX_HOME=/data/codex folio codex login --device-auth
```

Alternatively provision the supported provider configuration and credentials
inside `/data/codex` through your secret manager. Do not put secrets in Compose,
the context repository or chat. Inspect the Web model status after setup.

## First useful task

After creating the owner account, configure the assistant and/or report model
under **Settings → Model services**. A private repository and Codex login are
not prerequisites for API-backed chat or reports. The dashboard and settings
show separate capability readiness; these checks describe local configuration,
not verified provider credentials.

Use **Background report** on the dashboard, enter a concrete request and submit
it. Open the accepted task to see its queue state and eventual report. A paused
queue or an off-peak priority can delay execution; acceptance is not completion.
Read the result and use its follow-up entry to ask about the same run. This small
report is also an inference check that **Test connection** cannot provide.

To ask about personal context, connect the private repository. To organize it,
finish the Codex worker setup and maintenance model assignment as well. Optional
Weixin/Bark setup can follow once the Web flow works. If a task fails, inspect
its fixed failure status and configuration before retrying; preserve the private
data volume when upgrading or restarting.

## Model services and task assignments

Open **Settings → Model services** to add a named OpenAI-compatible connection,
then assign its connection and exact model/group ID separately to assistant chat,
reports and maintenance. A report kind or runtime action may override the report
default. For a gateway, enter its advertised ID (for example `group/reports`);
configure account selection, cost preferences and fallback at that gateway.
Folio's existing task priorities and time windows still control execution order.

The base URL normally ends in `/v1`. HTTPS is supported; plain HTTP is accepted
only for loopback or private IP addresses. Use a private IP URL for a local gateway
that has no HTTPS endpoint. Keep public HTTP endpoints out of configuration.
The write-only key field stays blank on reload; leaving it blank retains the
saved key. Configuration lives at `/data/runtime/model-services.json` and keys at
`/data/secrets/model-services/<connection-id>`, all with mode 0600. Keep these
paths in the persistent private data volume and out of images and source control.

**Test connection** performs a bounded `GET /models` with no inference request.
It checks connectivity/authentication and can offer model/group suggestions; it
does not prove that a selected route supports tools, Responses, or a particular
task. Verify a noncritical report before switching production uses. Maintenance
retains the Codex execution and structural-validation/rollback boundaries while
using its assigned connection; reports use a bounded tool-free API call.

Unassigned uses preserve the existing agent/runner configuration below. Clearing
an assignment restores that fallback; malformed saved configuration fails closed.
Do not remove existing credentials until assigned uses have passed acceptance.
Reported tokens and actual served provider/model, when supplied by the gateway,
feed the existing estimated-cost view. Requested group IDs are not prices, and
unknown attribution remains unpriced.

## Legacy main-agent runtime and price estimates

The Assistant view also supports the existing configurable main-agent runtime
when no assistant model-service assignment is present. Under **Advanced →
Main agent runtime**, choose one of two modes:

- **OpenAI-compatible API (recommended).** Pick a provider preset (DeepSeek,
  OpenAI or OpenRouter), or enter a custom HTTPS base URL. Folio
  appends `/chat/completions`. Set the model id and API key. The key is written
  to `/data/secrets/agent-api-key` with mode `0600` and is never returned to the
  browser. A common DeepSeek setup is:
  `https://api.deepseek.com/v1` + the exact model id from your provider
  (for example `deepseek-chat`, or the id shown in your provider console).
- **Installed command adapter.** Point at an absolute executable. It receives
  one JSON `AgentRequest` on stdin and returns one JSON `AgentResponse` on
  stdout; this is the boundary for a pi wrapper. The legacy path is
  `/data/agent/runtime`.

Saving the form swaps the runtime immediately; no restart is needed. The
Assistant reports unavailable until the adapter is complete and its key file
exists. A key is write-only in the UI: leave the field blank to keep the saved
key.

For a headless deployment, set the same values in the private `.env` next to
`compose.yaml` instead of using the form:

```bash
FOLIO_AGENT_KIND=openai
FOLIO_AGENT_PROVIDER=deepseek
FOLIO_AGENT_BASE_URL=https://api.deepseek.com/v1
FOLIO_AGENT_MODEL=deepseek-chat
# Write the key to this file with mode 0600, never to Compose or chat.
FOLIO_AGENT_API_KEY_FILE=/data/secrets/agent-api-key
```

The environment is read only when `/data/agent/config.json` does not exist; a
saved Settings configuration wins. A malformed config file fails closed. The
OpenAI-compatible adapter has no shell, filesystem, or source-edit tools; it can
only call the bounded Folio tools granted to the owner session. Integration
events still receive observer tools only.

Token counts come from the runner. Estimated cost additionally needs a price
catalog. Folio downloads the public [models.dev](https://models.dev) catalog from
`https://models.dev/api.json` when the cache is missing or older than 24 hours,
and caches a compact copy at `/data/runtime/model-prices.json` (mode `0600`).
The catalog is refreshed automatically in the background; there is no settings
page to maintain. Set `FOLIO_PRICING_URL` only when the deployment must use an
internal mirror; the same JSON shape is required.

Prices are list prices in USD per million tokens. The estimate covers uncached
input, cached input and output. It does not model cache writes, context-length
tiers, subscription plans, discounts or taxes, and unknown model/provider pairs
show “price unavailable” instead of a guess. Task details include the per-run
estimate; the dashboard includes an estimated total and per-model column.

## Weixin channel

The optional Weixin text channel uses native Go/iLink; it adds no OpenClaw
runtime or sidecar. Leave `WEIXIN_ENABLED` and `WEIXIN_OWNER_ID` unset to manage it
from the settings page: connect, scan the QR rendered in that page, enter the
digits the phone shows, then confirm the scanner identity. Folio starts polling
without a restart, and nothing polls before that confirmation.

For an unattended deployment, log in from a private interactive terminal against
the same volume, then pin the owner. Stop the service first if its poller is
already running:

```bash
docker compose stop folio
docker compose run --rm --no-deps folio /app/folio weixin login --data-dir /data
```

The image entrypoint prepares the volume and drops to the service user. Scan
the terminal QR with Weixin; enter verification digits if requested.
The CLI prints the scanner's opaque ID locally. Verify it, then add
`WEIXIN_ENABLED=true` and `WEIXIN_OWNER_ID=<the verified scanner ID>` to your
private `.env` and run `docker compose up -d`. `WEIXIN_ENABLED=false` is a hard
kill switch that refuses browser binding as well. Never send QR output, token
files, scanner IDs or private messages to chat or shared logs.

Send `/status` to verify the channel, then a short text question after repository
and Codex setup. Replies reuse Folio's read-only report runner, add one bounded
context packet when a repository is connected, and cannot execute maintenance
commands. The reply reports a fixed failure class when the model run fails, and
the console shows the same class beside the channel. The optional push switch
sends finished `pushed`/`failed` runs to the confirmed owner through the reply
context of the owner's last answered message, so the owner must write once first.
Non-owner senders are denied before any Folio capability. Media, group chats and
personas are deferred. Calls are ephemeral and are not currently counted in the
task/run usage dashboard.

Credentials, the confirmed/pending owner decision and the durable
cursor/inbox/outbox stay in `/data/secrets/weixin/` with private permissions.
Restarts reuse them. The CLI and the service take the same exclusive process
lock, so stop an enabled service before `weixin login` or local `weixin status`
(it conflicts with a running poller). Token expiry
requires explicit re-login; do not remove cursor state to repair a token.
`weixin status` reports saved credential presence, not online validity. The Web
health check does not assert Weixin connectivity. See
[the channel contract](../docs/weixin-channel.md) for bounded deduplication,
retry semantics, protocol evidence and live acceptance steps.

## Subscription quota view (optional)

Folio can show a read-only subscription-allowance panel backed by a
[magpie](https://github.com/yetone/magpie) gateway. This does not route model
calls and does not give Folio subscription credentials; it asks magpie for
already-normalized quota cards.

1. Run magpie on the same private network as Folio and create a dedicated
   gateway key with quota visibility enabled. Restrict the key to the models and
   accounts Folio may inspect. It is still a gateway key, not a read-only token:
   keep it out of runners, browsers, Compose and chat.
2. Write the key to `/data/secrets/magpie-quota-key` with mode `0600`.
3. Write `/data/quotas.json` in the service data volume with mode `0600`:

```json
{
  "version": 1,
  "source": {
    "kind": "magpie",
    "url": "http://magpie:3425",
    "key_file": "secrets/magpie-quota-key",
    "timeout_seconds": 5,
    "max_age_seconds": 60
  }
}
```

`url` must be `http` or `https` with no userinfo, query, fragment or path.
`key_file` is relative to `/data` and must be a regular file, not a symlink.
Folio never returns the key or URL to the browser. The refresh button asks
magpie to read the vendors and is limited to once every 30 seconds; normal page
loads use magpie's cached `?kept=1` cards. If magpie is down, Folio keeps the
last successful cards and marks them stale; it never blocks maintenance runs.

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

Codex maintenance preserves the bounded model/provider routing from the imported
`config.toml`, including when no model is selected in the Web UI. An explicit
model selection overrides only the model, preserving the configured provider.
The isolated worker also preserves `model_reasoning_effort` from this file
(`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, or `ultra`); choose
a level supported by the selected model. The Web model field changes only the
model, so it does not replace this effort setting. Invalid effort settings fail
before a model call rather than silently falling back to a different level.
Only the configured provider's named environment credential is forwarded when
present; inline bearer tokens are not forwarded to the isolated invocation.
Without configured routing, the isolated CLI uses its authenticated default.
Prompts are supplied over stdin and are never placed in process arguments.

For a custom provider, prefer `env_key = "FOLIO_PROVIDER_API_KEY"` and provision
that variable in `/data/runtime/worker.env` (mode `0600`, outside the context
checkout). Set `requires_openai_auth = false` when the provider uses its own API
key instead of Codex account authentication. Inline settings such as
`experimental_bearer_token` are deliberately filtered from the runner invocation;
having one in the source profile does not authenticate the isolated runner.
Keep the credential value out of `config.toml` routing overrides, which are
passed as process arguments.

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
existing login on the next state refresh and the searchable repository
selector includes private repositories visible to that login.

`GIT_AUTH_TOKEN` instead fixes the credential to one deployment token. A
fine-grained token only lists and clones the repositories it was granted, so
the picker may show fewer private repositories than the account owns; prefer
the imported `gh` login when the selector must cover all of them. The card and
the picker state which mode is active.

The scheduler runs every 86400 seconds after startup. Each tick pulls the
latest checkout and organizes changed documents; unchanged inputs do not call
the model. It validates bounded structural edits, pushes them and records the
run. See [automatic organization](../docs/context-maintenance.md) for the
edit/review boundary. Incremental fingerprints and review findings are private
files under `/data/runtime`, not repository content. Failed runs do not advance
progress; large backlogs queue continuations under the normal off-peak/pause
policy. Set `MAINTENANCE_SCHEDULER_ENABLED` to
`"false"` to disable automatic runs; the Web UI still allows an immediate run.
Use only one scheduler for this checkout. The service drops to UID/GID 10001
after initializing `/data`.

For change-driven maintenance, create and enable a maintenance hook in the Web
UI, then run `scripts/folio-watch-git.sh` on an external host with private hook
configuration. Include that caller's address in `TRUSTED_HOSTS`. The watcher
delivers changed remote HEADs, including the initial HEAD; Folio's fingerprint
checkpoint prevents its own commit from causing repeated model work. You may
disable the interval scheduler when the watcher is installed. The service
itself does not poll GitHub.

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
and runs a runtime smoke test before publishing. The builder copies the whole
allowed context (`COPY . .`) instead of a hand-maintained list of directories, so
a new Go package cannot be missing from the image; `cmd/folio` has a guard test
that fails if a source directory is not allowlisted or the Dockerfile goes back
to copying directories by hand. Context, inbox, `.git`, reports, runtime state,
and secrets may not enter any image layer or public artifact. BuildKit publishes
SBOM and provenance attestations for release builds.
