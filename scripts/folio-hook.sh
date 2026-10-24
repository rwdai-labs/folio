#!/bin/sh
# Deliver one signed event to a Folio hook endpoint.
#
# Folio does not poll. A watcher anywhere else (cron, systemd timer, CI, another
# host) calls this script when something happens; Folio starts one bounded run.
#
# Usage:
#   FOLIO_URL=http://127.0.0.1:8080 \
#   FOLIO_HOOK_ID=context-repo-updated \
#   FOLIO_HOOK_SECRET=<secret from the Web UI> \
#     ./scripts/folio-hook.sh ['{"optional":"json"}']
#
# Environment:
#   FOLIO_URL          base URL of the service, no trailing slash
#   FOLIO_HOOK_ID      hook id shown in the Web UI
#   FOLIO_HOOK_SECRET  signing secret, shown once when the hook is created
#   FOLIO_EVENT_ID     optional; pin it to make a retry idempotent
#
# Requires openssl for the HMAC. Exit status is non-zero on any 4xx/5xx.
set -eu

: "${FOLIO_URL:?set FOLIO_URL}"
: "${FOLIO_HOOK_ID:?set FOLIO_HOOK_ID}"
: "${FOLIO_HOOK_SECRET:?set FOLIO_HOOK_SECRET}"

body=${1:-'{}'}
timestamp=$(date +%s)
event_id=${FOLIO_EVENT_ID:-"${timestamp}-$$"}
signature=$(printf '%s.%s' "$timestamp" "$body" | openssl dgst -sha256 -hmac "$FOLIO_HOOK_SECRET" -r | cut -d' ' -f1)

response=$(curl -sS -X POST "${FOLIO_URL%/}/api/hooks/${FOLIO_HOOK_ID}" \
  -H 'Content-Type: application/json' \
  -H "X-Folio-Timestamp: ${timestamp}" \
  -H "X-Folio-Event-Id: ${event_id}" \
  -H "X-Folio-Signature: ${signature}" \
  --data-binary "$body" \
  -w '\n%{http_code}')

status=$(printf '%s' "$response" | tail -n 1)
printf '%s\n' "$response" | sed '$d'

case "$status" in
  2*) exit 0 ;;
  *) exit 1 ;;
esac
