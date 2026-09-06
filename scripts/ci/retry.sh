#!/usr/bin/env bash
# retry.sh -- run a command, retrying it on failure with backoff.
#
# It exists for one measured failure mode: a build step that fetches a tool
# over the network fails outright when the module proxy hiccups.
#
#   2026-09-05T23:39:39Z  main Release run 33997012296, build job, step
#   "Build deterministic matrix and final checksum manifest":
#     go: github.com/segmentio/encoding@v0.5.4: Get
#       "https://storage.googleapis.com/proxy-golang-org-prod/…encoding-v0.5.4.zip?…"
#       : read tcp 10.1.0.66:50148->104.154.124.27:443: read: connection reset by peer
#     ##[error]Process completed with exit code 1.
#
# That reset arrived while `go run github.com/anchore/syft/cmd/syft@v1.46.0`
# was resolving its dependency graph, and it discarded a build that had already
# compiled and verified a deterministic matrix. Nothing about it is specific to
# syft: every `go run <module>@<version>` and `go install` in a build step
# reaches the proxy, and none of them had a retry.
#
# ATTEMPTS: four, not three. The escalation is 5s/15s/45s, and three attempts
# consume only TWO waits -- the 45 would be a configured value no code path can
# reach. Four makes every named delay reachable, worst case 65s of waiting
# against a build that costs minutes to redo. This matches the wrapper landed
# for the Release publish retry, deliberately: one escalation, one shape.
#
# It does NOT retry forever and it does NOT hide what it retried past. Every
# failed attempt prints the command, the attempt number and the exit status, so
# a run that needed retries says so in its log and a pattern of second-attempt
# successes is visible rather than silent. After the last attempt it fails with
# the command's own exit status, so callers see the real failure, not a
# synthetic one.
set -uo pipefail

if [ "$#" -eq 0 ]; then
  printf 'retry.sh: usage: retry.sh <command> [args...]\n' >&2
  exit 2
fi

# Overridable for the behavioural test, which must not spend 65s to prove the
# logic. Defaults are the production values.
RETRY_MAX_ATTEMPTS="${RETRY_MAX_ATTEMPTS:-4}"
RETRY_DELAYS="${RETRY_DELAYS:-5 15 45}"
read -r -a _delays <<<"$RETRY_DELAYS"

label="$1"
attempt=1
while :; do
  "$@"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    if [ "$attempt" -gt 1 ]; then
      printf 'retry.sh: %s succeeded on attempt %d of %d\n' "$label" "$attempt" "$RETRY_MAX_ATTEMPTS" >&2
    fi
    exit 0
  fi
  printf 'retry.sh: %s FAILED attempt %d of %d (exit %d)\n' \
    "$label" "$attempt" "$RETRY_MAX_ATTEMPTS" "$rc" >&2
  if [ "$attempt" -ge "$RETRY_MAX_ATTEMPTS" ]; then
    printf 'retry.sh: %s failed after %d attempts; exiting with its own status %d\n' \
      "$label" "$RETRY_MAX_ATTEMPTS" "$rc" >&2
    exit "$rc"
  fi
  delay="${_delays[attempt-1]:-${_delays[${#_delays[@]}-1]}}"
  printf 'retry.sh: retrying %s in %ss\n' "$label" "$delay" >&2
  sleep "$delay"
  attempt=$((attempt + 1))
done
