#!/usr/bin/env bash
# Offline test of run-queue-report.sh: a four-job chain with known queue waits.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
cat > "$tmp/jobs.json" <<'JSON'
{"jobs":[
 {"name":"mirror-preflight","created_at":"2026-10-15T20:07:41Z","started_at":"2026-10-15T20:21:51Z","completed_at":"2026-10-15T20:22:23Z"},
 {"name":"docs-mermaid","created_at":"2026-10-15T20:07:41Z","started_at":"2026-10-15T20:07:45Z","completed_at":"2026-10-15T20:08:45Z"},
 {"name":"unit (shard 1 of 9)","created_at":"2026-10-15T20:22:24Z","started_at":"2026-10-15T20:30:24Z","completed_at":"2026-10-15T20:53:58Z"},
 {"name":"verify","created_at":"2026-10-15T20:53:58.5Z","started_at":"2026-10-15T21:01:22Z","completed_at":"2026-10-15T21:02:22Z"},
 {"name":"never-started","created_at":"2026-10-15T20:22:24Z","started_at":null,"completed_at":null}
]}
JSON
out="$("$here/run-queue-report.sh" --jobs-json "$tmp/jobs.json")"
fail=0
want() { grep -qF -- "$1" <<<"$out" || { printf 'missing: %s\n--- output:\n%s\n' "$1" "$out" >&2; fail=1; }; }
want 'critical path: 3 hop(s), queue 29.6m of 54.7m wall'
want '20:07:41 queue 14.2m  mirror-preflight'
want '20:22:24 queue 8m  unit (shard 1 of 9)'
want '20:53:58 queue 7.4m  verify'
want 'queue -m  run -m  never-started'
if grep -q 'docs-mermaid' <<<"$(sed -n '/^critical path/,$p' <<<"$out")"; then
  printf 'docs-mermaid finished first and released nothing; it must not be on the critical path\n' >&2; fail=1
fi
[ "$fail" -eq 0 ] && printf 'PASS: run-queue-report.sh\n'
exit "$fail"
