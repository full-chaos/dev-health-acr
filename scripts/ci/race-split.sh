#!/usr/bin/env bash
# Splits one package's race suite across two CI jobs by test name.
# Usage: race-split.sh <package> heavy|rest|verify, where <package> is a
# directory name under internal/contextfabric with a
# scripts/ci/<package>-heavy-tests.txt list.
#   heavy  prints the anchored regex of the tests listed in
#          <package>-heavy-tests.txt (job: container-heavy half).
#   rest   prints the same regex; the other job passes it as -skip, so any
#          test not listed (new tests included) runs there. Nothing can drop.
#   verify fails unless every listed name is a real top-level test, so a
#          rename cannot silently move a heavy test into the rest half.
#
# devhealthfacts only: the race jobs split three ways. race-mid-tests.txt names
# tests moved out of both halves into a third job.
#   race-heavy  anchored regex of heavy-tests.txt minus race-mid-tests.txt (-run)
#   race-mid    anchored regex of race-mid-tests.txt (-run)
#   race-rest   anchored regex of heavy plus mid names (-skip); unlisted tests run there
#   race-partition  lists each job's tests with `go test -list` (-run regexes; -skip is the complement) and
#          fails unless every test lands in exactly one job.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
name="${1:-}"
case "$name" in
  devhealthfacts|devhealthsource) ;;
  *) printf 'usage: %s devhealthfacts|devhealthsource heavy|rest|verify\n' "${0##*/}" >&2; exit 2 ;;
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

mid_list="$root/scripts/ci/$name-race-mid-tests.txt"
mid_names() { grep -v '^[[:space:]]*$' "$mid_list" | LC_ALL=C sort -u; }
join_regex() {
  local joined
  joined="$(paste -sd'|' -)"
  test -n "$joined" || { printf 'empty test list for %s\n' "$name" >&2; exit 1; }
  printf '^(%s)$' "$joined"
}
race_heavy() { LC_ALL=C comm -23 <(names) <(mid_names) | join_regex; }
race_mid() { mid_names | join_regex; }
race_rest() { { names; mid_names; } | LC_ALL=C sort -u | join_regex; }

case "${2:-}" in
  heavy|rest) regex ;;
  race-heavy) race_heavy ;;
  race-mid) race_mid ;;
  race-rest) race_rest ;;
  race-partition)
    [ "$name" = devhealthfacts ] || { printf 'race-partition: devhealthfacts only\n' >&2; exit 2; }
    cd "$root"
    all="$(go test -list '.' "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    a="$(go test -list "$(race_heavy)" "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    b="$(go test -list "$(race_mid)" "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    skipped="$(go test -list "$(race_rest)" "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    c="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$skipped"))"
    counted="$(printf '%s\n%s\n%s\n' "$a" "$b" "$c" | grep -v '^$' | LC_ALL=C sort | uniq -c)"
    bad="$(printf '%s\n' "$counted" | awk '$1 != 1 {print $1, $2}')"
    lost="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$counted" | awk '{print $2}' | LC_ALL=C sort -u))"
    if [ -n "$bad" ] || [ -n "$lost" ]; then
      printf 'race split is not a partition.\nrun more or less than once (count name):\n%s\nrun in no job:\n%s\n' "$bad" "$lost" >&2
      exit 1
    fi
    printf 'race split ok: %s tests = %s heavy + %s mid + %s rest\n' \
      "$(printf '%s\n' "$all" | wc -l | tr -d ' ')" "$(printf '%s\n' "$a" | wc -l | tr -d ' ')" \
      "$(printf '%s\n' "$b" | wc -l | tr -d ' ')" "$(printf '%s\n' "$c" | wc -l | tr -d ' ')"
    ;;
  verify)
    cd "$root"
    existing="$(go test -list '.' "$pkg" | grep '^Test' | LC_ALL=C sort -u)"
    missing="$(LC_ALL=C comm -23 <({ names; [ -f "$mid_list" ] && mid_names; } | LC_ALL=C sort -u) <(printf '%s\n' "$existing"))"
    if [ -n "$missing" ]; then
      printf 'heavy list names no such test:\n%s\n' "$missing" >&2
      exit 1
    fi
    printf 'heavy list ok: %s tests\n' "$(names | wc -l | tr -d ' ')"
    ;;
  *) printf 'usage: %s <package> heavy|rest|verify\n' "${0##*/}" >&2; exit 2 ;;
esac
