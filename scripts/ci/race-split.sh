#!/usr/bin/env bash
# Splits one package's race suite across two CI jobs by test name.
# Usage: race-split.sh <package> heavy|rest|verify, where <package> is a
# directory name under internal/contextfabric with a
# scripts/ci/<package>-heavy-tests.txt list.
#   heavy  prints the anchored regex of the tests listed in
#          <package>-heavy-tests.txt (job: container-heavy half).
#   rest   prints the same regex; the other job passes it as -skip, so any
#          test not listed (new tests included) runs there. Nothing can drop.
#   partition fails unless every top-level test of the package lands in exactly
#          one of the two halves (-run regex vs its -skip complement), and
#          ci.yml runs both halves of this package.
#   verify fails unless every listed name is a real top-level test, so a
#          rename cannot silently move a heavy test into the rest half.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
name="${1:-}"
case "$name" in
  devhealthfacts|devhealthsource) ;;
  *) printf 'usage: %s devhealthfacts|devhealthsource heavy|rest|verify|partition\n' "${0##*/}" >&2; exit 2 ;;
esac
list="$root/scripts/ci/$name-heavy-tests.txt"
pkg="./internal/contextfabric/$name"

names() { grep -v '^[[:space:]]*$' "$list" | LC_ALL=C sort -u; }

regex() {
  local joined
  joined="$(names | paste -sd'|' -)"
  test -n "$joined" || { printf 'empty heavy list: %s\n' "$list" >&2; exit 1; }
  printf '^(%s)$' "$joined"
}

case "${2:-}" in
  heavy|rest) regex ;;
  partition)
    cd "$root"
    wf="$root/.github/workflows/ci.yml"
    for want in "race-split.sh $name rest)" "race-split.sh $name heavy)"; do
      grep -qF -- "$want" "$wf" || { printf 'ci.yml never uses "%s"\n' "$want" >&2; exit 1; }
    done
    all="$(go test -list '.' "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    a="$(go test -list "$(regex)" "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    c="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$a"))"
    both="$(LC_ALL=C comm -12 <(printf '%s\n' "$a") <(printf '%s\n' "$c"))"
    lost="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n%s\n' "$a" "$c" | grep -v '^$' | LC_ALL=C sort -u))"
    if [ -n "$both" ] || [ -n "$lost" ] || [ -z "$a" ] || [ -z "$c" ]; then
      printf 'split is not a partition.\nin both halves:\n%s\nin no half:\n%s\n' "$both" "$lost" >&2
      exit 1
    fi
    printf 'split ok: %s tests = %s heavy + %s rest\n' "$(printf '%s\n' "$all" | wc -l | tr -d ' ')" \
      "$(printf '%s\n' "$a" | wc -l | tr -d ' ')" "$(printf '%s\n' "$c" | wc -l | tr -d ' ')"
    ;;
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
  *) printf 'usage: %s <package> heavy|rest|verify|partition\n' "${0##*/}" >&2; exit 2 ;;
esac
