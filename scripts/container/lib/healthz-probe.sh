#!/usr/bin/env bash
# Shared, unit-testable readiness probe for verify.sh's /healthz smoke.
#
# CHAOS-12966: `docker run -d -p 127.0.0.1::8080` returns before the process
# listens, and docker's port proxy accepts the connection and then resets it
# until the app is up. curl --retry does not retry a reset (exit 56), and
# --retry-connrefused covers only a refused connection, so the first probe of
# a fresh container failed the job on a start race (acr #2025, #2032, #2037).
# This polls until the endpoint answers {"status":"ok"} or the deadline passes,
# and on failure reports the last curl/jq outcome instead of the first.
#
# wait_for_healthz URL [DEADLINE_SECONDS=60] [INTERVAL_SECONDS=1]
wait_for_healthz() {
  local url="$1" deadline="${2:-60}" interval="${3:-1}"
  local started now body last="no attempt made" attempts=0
  started="$(date +%s)"
  while :; do
    attempts=$((attempts + 1))
    # At most 64 KiB of a reply is read (a faulty handler cannot fill memory or
    # the CI log), and only the first 300 characters of it are ever reported.
    if body="$(set -o pipefail; curl --fail --silent --show-error --max-time 5 "$url" 2>&1 | head -c 65536)"; then
      if printf '%s' "$body" | jq -e -s 'length == 1 and .[0].status == "ok"' >/dev/null 2>&1; then
        return 0
      fi
      last="answered without status ok: ${body:0:300}"
    else
      last="${body:0:300}"
    fi
    now="$(date +%s)"
    if ((now - started >= deadline)); then
      printf 'healthz at %s not ready after %ss and %d attempts; last result: %s\n' "$url" "$deadline" "$attempts" "$last" >&2
      return 1
    fi
    sleep "$interval"
  done
}
