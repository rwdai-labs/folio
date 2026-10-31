# Backend package map

Applies to `internal/`; inherit `../AGENTS.md` and read local children as needed.

| Package | Responsibility / next instructions |
| --- | --- |
| `context/` | Deterministic routing and model worker; `context/AGENTS.md` |
| `store/` | Durable state, Git transactions, dispatch, audits; `store/AGENTS.md` |
| `web/` | Authenticated HTTP and plugin host; `web/AGENTS.md` |
| `storage/` | `Remote` and hardened process `Executor` contracts |
| `plugin/` | Built-in capability interfaces and registry |
| `dshconfig/` | Bounded extraction of model/provider declarations from profiles |
| `quota/` | Optional bounded subscription-allowance source; `quota/AGENTS.md` |
| `notify/` | Bounded outbound push messages and credential-free receipts |
| `channel/` | Inbound identity/text boundary and native Weixin transport; `channel/AGENTS.md` |

- Keep HTTP dependent on its `Backend` interface. Store owns local checkout
  validation, commits, rollback, and auditing; a storage plugin owns the remote
  and its authentication. Process calls go through the host executor contract.
- `dshconfig` reads a restricted profile shape; never evaluate executable YAML
  tags or mutate a mounted host profile. Model suggestions come from actual
  runner/profile data; preserve typed model IDs.
- Notifications are outbound only. Keep device keys out of API responses and
  delivery receipts; restrict plain HTTP to approved local/private targets.
- Bidirectional channels are separate from notifications. Channel adapters own
  protocol and recovery state; store owns authorization and application calls.
- Update interface producers and consumers together, including their nearest
  instructions and `../docs/integration-plugins.md` when relevant. Add a child
  instruction file when an area needs substantial independent guidance.

Run affected package tests (for example `go test -race ./internal/dshconfig
./internal/notify`) and the root Go checks. Use temporary synthetic data and
local fake services; never read a real private volume to populate fixtures.
