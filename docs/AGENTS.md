# Design and contract documentation

Inherit `../AGENTS.md`.

- `product-definition.md` is the shared product vocabulary: Folio is a
  personal assistant, task center, and owner-controlled digital counterpart;
  context maintenance is its first shipped scenario.
- `self-bootstrap.md` describes owner feedback capture, the action catalog,
  asynchronous screenshot-aware intake and clarification, the plan-only task,
  and the isolated source workshop
  that remains planned. Do not claim source auto-edit, commit/push, or
  deployment from Phase 1–2.
- `integration-plugins.md` documents implemented plugin boundaries and signed
  event contracts. `event-driven-assistant.md` and
  `personal-assistant-research.md` contain design/research context; verify any
  proposal against code before treating it as shipped behaviour.
- `agent-architecture.md` is the approved target for Main built into pi SDK,
  its file/shell/scripts/skills, the Folio `subagent` tool and ACP task harnesses
  (Codex/DSH/pi via verified profiles). Folio owns task lifecycle and authority;
  direct model HTTP is not a target task runner. The SDK Host/ACP/delegation
  design is not implemented. Keep current Go loop, HTTP/command adapters and
  report calls explicitly labelled as the migration baseline, not the target.
  Do not reopen SDK-vs-wrapper, disable Main shell/files by default, or conflate
  the special Main with interchangeable task runners.
- `subscription-quota.md` covers subscription allowance/quota management: what
  to borrow from magpie, the implemented read-only panel and generic connection/
  task bindings, and the unshipped quota-deferral/budget phases. Its ACP migration
  note keeps model/gateway configuration distinct from harness selection. Deployment README
  owns model-service setup; Folio does not reproduce gateway account/cost routing.
  Keep its status block accurate as phases land.
- `context-maintenance.md` is the automatic organization contract: complete
  incremental input, structural edit limits, checkpoint/backlog semantics,
  persistent owner-review findings and focused acceptance checks.
- `weixin-channel.md` records the native iLink protocol reference, transport
  and owner-handler boundary, the console binding flow (rendered QR over an
  authenticated route, explicit owner confirmation, runtime start and stop),
  private state schema, delivery limits and operator acceptance. Do not claim
  live Weixin acceptance from synthetic HTTP tests.
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
