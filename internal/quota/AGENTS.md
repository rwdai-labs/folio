# Quota source adapter

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- Reads the optional `data/quotas.json` source and normalizes magpie's
  `/v1/magpie/quotas` cards for the authenticated dashboard. It never talks to
  a subscription vendor, never stores an account credential, and never changes
  task scheduling.
- The only secret is a dedicated gateway key at a relative `key_file`; read it
  per request, never return it in a snapshot, log it, or put it in a URL.
- Keep the source bounded: config and key size, HTTP timeout, response body,
  card/window/string counts. Reject invalid URLs, userinfo, query, fragment and
  non-local key paths. Do not follow redirects.
- Error codes are the browser contract. Return fixed codes such as
  `quota_unauthorized`; never a vendor's raw error or a filesystem path.
- Cache successful snapshots for `max_age_seconds`; on failure return the last
  successful cards with `Stale=true`. An absent config is `Configured=false`,
  not an error. A local refresh limit is `quota_refresh_limited`; a magpie 429
  is `quota_rate_limited`.
- Tests use `httptest` and synthetic fixtures only; never a live magpie or a
  private volume.

Run `go test -race ./internal/quota` from the repository root.
