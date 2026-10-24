# Plugin boundaries

Inherit `../AGENTS.md`; read `../docs/integration-plugins.md` for the contract.

| Area | Integration direction | Local instructions |
| --- | --- | --- |
| `github/` | Built into Folio; host calls capability interfaces | `github/AGENTS.md` |
| `github-bridge/` | Separate program calls Folio's signed hooks | `github-bridge/AGENTS.md` |

- Built-in plugins implement `internal/plugin` and optionally
  `internal/storage.Remote`. Main wires them in; the host owns authentication,
  response encoding and process execution through `storage.Executor`.
- External event plugins own their credentials, state, cadence and retries.
  Folio must not depend on their process, import their code or share their
  private state. A signed hook accepts durable intent, not successful work.
- Keep external events as bounded data. The owner chooses the hook's kind;
  event senders do not acquire repository-write or action-selection privileges.
- Add new capability packages to the root build/module and packaging boundary
  as needed. External modules have independent checks; root tests miss them.
- A new plugin or changed interface requires parent navigation, local
  instructions, host/consumer instructions and the integration contract to be
  updated together. Keep detailed plugin behaviour in its own directory.
