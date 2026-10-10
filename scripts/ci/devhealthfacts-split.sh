#!/usr/bin/env bash
# Splits the devhealthfacts race suite across two CI jobs by test name.
#   heavy  prints the anchored regex of the tests listed in
#          devhealthfacts-heavy-tests.txt (job: container-heavy half).
#   rest   prints the same regex; the other job passes it as -skip, so any
#          test not listed (new tests included) runs there. Nothing can drop.
#   verify fails unless every listed name is a real top-level test, so a
#          rename cannot silently move a heavy test into the rest half.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
list="$root/scripts/ci/devhealthfacts-heavy-tests.txt"
pkg=./internal/contextfabric/devhealthfacts

names() { grep -v '^[[:space:]]*$' "$list" | LC_ALL=C sort -u; }

regex() {
  local joined
  joined="$(names | paste -sd'|' -)"
  test -n "$joined" || { printf 'empty heavy list: %s\n' "$list" >&2; exit 1; }
  printf '^(%s)$' "$joined"
}

case "${1:-}" in
  heavy|rest) regex ;;
  verify)
    cd "$root"
    existing="$(go test -list '.' "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    missing="$(LC_ALL=C comm -23 <(names) <(printf '%s\n' "$existing"))"
    if [ -n "$missing" ]; then
      printf 'heavy list names no such test:\n%s\n' "$missing" >&2
      exit 1
    fi
    printf 'heavy list ok: %s tests\n' "$(names | wc -l | tr -d ' ')"
    ;;
  *) printf 'usage: %s heavy|rest|verify\n' "${0##*/}" >&2; exit 2 ;;
esac
