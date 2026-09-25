#!/bin/sh
# `go test -exec` wrapper: one testcontainers session, so one ryuk reaper, per
# test binary (CHAOS-6777). testcontainers-go keys its session on the parent
# `go test` pid, so every package binary of one `go test pkg1 pkg2 ...` call
# shares a reaper, and a binary that starts while an earlier package's reaper
# is shutting down waits 60 s on it and fails. Reaping stays on; only the
# sharing goes. A session id already set by the caller is kept.
set -eu
if [ -z "${TESTCONTAINERS_SESSION_ID:-}" ]; then
	TESTCONTAINERS_SESSION_ID="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
	export TESTCONTAINERS_SESSION_ID
fi
exec "$@"
