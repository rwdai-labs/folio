# Inbound communication boundary

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- `channel.go` carries transport identity, bounded text and future attachment
  kinds into a Folio-owned handler. It is not an Agent or plugin framework.
- Identity comes from the authenticated channel, never text. Deduplication and
  session keys must include account, sender and conversation; transport session
  IDs cannot grant authority or select a model session.
- Handlers must authorize processing and cached reply delivery. Recheck policy
  after restart; prepared replies cannot preserve revoked privileges.
- `weixin/` owns iLink communication and private recovery state; read its
  `AGENTS.md`. Store owns authorization and application responses.

Run `go test -race ./internal/channel/... ./internal/store` and root Go checks.
