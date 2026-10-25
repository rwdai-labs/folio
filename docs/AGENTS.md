# Design and contract documentation

Inherit `../AGENTS.md`.

- `integration-plugins.md` documents implemented plugin boundaries and signed
  event contracts. `event-driven-assistant.md` and
  `personal-assistant-research.md` contain design/research context; verify any
  proposal against code before treating it as shipped behaviour.
- `context-maintenance.md` is the automatic organization contract: complete
  incremental input, structural edit limits, checkpoint/backlog semantics,
  persistent owner-review findings and focused acceptance checks.
- Keep protocol details consistent with service validation and every affected
  caller: headers, raw-body signatures, event IDs, limits, response semantics,
  retries, payload projection and owner-selected task kinds.
- Use synthetic examples. Never paste private context, runtime reports,
  credentials or private host/deployment details into this public directory.
- Code changes that alter a documented contract must update that document and
  the affected directory instructions in the same change. New or moved docs
  also update this map and parent links; avoid duplicating implementation
  inventories or storing a task log in `AGENTS.md`.

For documentation-only edits, verify relative links, commands and factual
claims against current source. Mark planned behaviour explicitly; do not claim
runtime/deployment acceptance from documentation or static checks.
