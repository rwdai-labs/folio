#!/bin/sh
# Watch a Git remote and fire a Folio hook only when its HEAD changes.
#
# This is the "context repository was updated" event. The probe is
# `git ls-remote`, which consumes no API quota and no model tokens; the
# expensive work happens only on a real change, and the commit SHA doubles as
# the event id so a retry is idempotent.
#
# Run it from cron, a systemd timer, or a NAS scheduled task. It lives outside
# Folio on purpose: Folio never polls.
#
# Usage:
#   WATCH_REMOTE=git@github.com:owner/context.git \
#   WATCH_STATE=/var/lib/folio-watch/context.head \
#   FOLIO_URL=http://127.0.0.1:8080 \
#   FOLIO_HOOK_ID=context-repo-updated \
#   FOLIO_HOOK_SECRET=<secret> \
#     ./scripts/folio-watch-git.sh
set -eu

: "${WATCH_REMOTE:?set WATCH_REMOTE}"
: "${WATCH_STATE:?set WATCH_STATE}"

current=$(git ls-remote "$WATCH_REMOTE" HEAD | cut -f1)
[ -n "$current" ] || exit 0

previous=""
if [ -f "$WATCH_STATE" ]; then
  previous=$(cat "$WATCH_STATE")
fi
[ "$current" = "$previous" ] && exit 0

# Record the SHA only after the hook is accepted: a failed call is retried on
# the next run with the same event id, which Folio treats as a duplicate.
FOLIO_EVENT_ID="git-${current}"
export FOLIO_EVENT_ID
"$(dirname "$0")/folio-hook.sh" '{}'
mkdir -p "$(dirname "$WATCH_STATE")"
printf '%s\n' "$current" > "$WATCH_STATE"
