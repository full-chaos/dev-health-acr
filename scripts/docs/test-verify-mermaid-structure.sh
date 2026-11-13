#!/usr/bin/env bash
# Proves the structure counts ignore a label edit and catch a dropped edge, and that
# the real docs keep their counts against the current HEAD.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
tool="$root/scripts/docs/mermaid-structure.py"
fx="$root/testdata/docs-mermaid-structure"
b="$(python3 "$tool" --dir "$fx/before")"
l="$(python3 "$tool" --dir "$fx/after-label")"
d="$(python3 "$tool" --dir "$fx/after-dropped-edge")"
[ -n "$b" ] || { printf 'FAIL: fixture counted nothing\n' >&2; exit 1; }
[ "$b" = "$l" ] || { printf 'FAIL: a label edit changed the counts\n' >&2; exit 1; }
[ "$b" != "$d" ] || { printf 'FAIL: a dropped edge kept the counts\n' >&2; exit 1; }
"$root/scripts/docs/verify-mermaid-structure.sh" HEAD >/dev/null
printf 'ok: mermaid-structure ignores a label edit and rejects a dropped edge\n'
