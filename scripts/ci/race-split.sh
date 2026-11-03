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
#
# factoracle splits two ways (heavy/rest/verify/partition only): its race jobs
# run the heavy list with -run and everything else with -skip.
#
# devhealthfacts and devhealthsource split four ways (CHAOS-12667 added devhealthsource's fourth job; its plain unit legs still run three parts: unit-heavy, unit-mid, unit-rest, checked by `partition`): the race jobs split by these lists. race-mid-tests.txt and
# race-fourth-tests.txt name tests moved out of the heavy and rest halves into
# a third and a fourth job. Lines starting with # are comments in every list.
# <package>-race-rest-tests.txt (devhealthsource, CHAOS-12667) names every test
# the rest job runs. The rest job still runs by -skip, so it selects nothing;
# with the list present, verify and race-partition fail for a test that is in
# no race list, so a new test is placed by its author, not silently in rest.
#   race-heavy  anchored regex of heavy-tests.txt minus the mid and fourth lists (-run)
#   race-mid    anchored regex of race-mid-tests.txt (-run)
#   race-fourth anchored regex of race-fourth-tests.txt (-run)
#   race-rest   anchored regex of heavy, mid and fourth names (-skip); unlisted tests run there
#   race-partition  checks ci.yml wires each race mode once, then lists each job's tests with `go test -list` (-run regexes; -skip is the complement) and
#          fails unless every test lands in exactly one job; a name in both
#          the mid and fourth lists fails by name before any run.
#   verify  also fails for a mid or fourth name that is no real test.
#   unit-heavy / unit-mid  plain unit legs 7 and 10 of devhealthfacts. Optional
#          <package>-unit-heavy-extra-tests.txt names tests of the mid/fourth
#          lists that leg 7 runs instead of leg 10 (race jobs unchanged); a name
#          twice, or in neither list, fails verify and partition.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
name="${1:-}"
case "$name" in
  devhealthfacts|devhealthsource|factoracle) ;;
  *) printf 'usage: %s devhealthfacts|devhealthsource|factoracle heavy|rest|verify|partition\n' "${0##*/}" >&2; exit 2 ;;
esac
list="$root/scripts/ci/$name-heavy-tests.txt"
pkg="./internal/contextfabric/$name"

names() { grep -v '^[[:space:]]*\(#\|$\)' "$list" | LC_ALL=C sort -u; }

regex() {
  local joined
  joined="$(names | paste -sd'|' -)"
  test -n "$joined" || { printf 'empty heavy list: %s\n' "$list" >&2; exit 1; }
  printf '^(%s)$' "$joined"
}

mid_list="$root/scripts/ci/$name-race-mid-tests.txt"
mid_names() { grep -v '^[[:space:]]*\(#\|$\)' "$mid_list" | LC_ALL=C sort -u; }
fourth_list="$root/scripts/ci/$name-race-fourth-tests.txt"
fourth_names() {
  if [ -f "$fourth_list" ]; then
    grep -v '^[[:space:]]*\(#\|$\)' "$fourth_list" | LC_ALL=C sort -u
  fi
}
join_regex() {
  local joined
  joined="$(paste -sd'|' -)"
  test -n "$joined" || { printf 'empty test list for %s\n' "$name" >&2; exit 1; }
  printf '^(%s)$' "$joined"
}
race_heavy() { LC_ALL=C comm -23 <(names) <({ mid_names; fourth_names; } | LC_ALL=C sort -u) | join_regex; }
race_mid() { mid_names | join_regex; }
race_fourth() { fourth_names | join_regex; }
race_mid_fourth() { { mid_names; fourth_names; } | LC_ALL=C sort -u | join_regex; }
race_rest() { { names; mid_names; fourth_names; } | LC_ALL=C sort -u | join_regex; }
both_lists() { LC_ALL=C comm -12 <(mid_names) <(fourth_names); }

# CHAOS-12667: the optional rest list. With it, every test of the package is in
# exactly one race list (heavy, mid, fourth or rest). rest_list_problems takes the
# package's real top-level tests and prints each problem; it prints nothing when
# the package has no rest list.
rest_list="$root/scripts/ci/$name-race-rest-tests.txt"
rest_names() { if [ -f "$rest_list" ]; then grep -v '^[[:space:]]*\(#\|$\)' "$rest_list" | LC_ALL=C sort -u; fi; }
rest_list_problems() {
  local all="$1" dup unlisted stray
  [ -f "$rest_list" ] || return 0
  dup="$(LC_ALL=C comm -12 <(rest_names) <({ names; mid_names; fourth_names; } | LC_ALL=C sort -u))"
  stray="$(LC_ALL=C comm -23 <(rest_names) <(printf '%s\n' "$all"))"
  unlisted="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <({ names; mid_names; fourth_names; rest_names; } | LC_ALL=C sort -u))"
  [ -z "$dup" ] || printf 'named in %s and also in the heavy, mid or fourth list:\n%s\n' "${rest_list##*/}" "$dup"
  [ -z "$stray" ] || printf '%s names no such test:\n%s\n' "${rest_list##*/}" "$stray"
  if [ -n "$unlisted" ]; then
    printf 'a test of %s is in no race list. Add it to scripts/ci/%s, or, if it is slow under -race, to the heavy, mid or fourth list (the race job prints each test'"'"'s real span):\n%s\n' \
      "$name" "${rest_list##*/}" "$unlisted"
  fi
}

# Codex #1967 r3: every listing here is `go test -list` WITHOUT -race, so a test
# function in a file built only under -race (`//go:build race`) is invisible to
# verify and race-partition while the race rest job still runs it. Such a file
# may hold constants (the raceDetectorEnabled pair) but no test function.
# RACE_SPLIT_SOURCE_DIR points the scan at another directory (tests only).
race_only_tests() {
  local dir="${RACE_SPLIT_SOURCE_DIR:-$root/internal/contextfabric/$name}" f constraint
  for f in "$dir"/*_test.go; do
    [ -f "$f" ] || continue
    constraint="$(grep -m1 '^//go:build' "$f" || true)"
    [ -n "$constraint" ] || continue
    # A constraint that still names race once every !race is gone needs -race to build.
    printf '%s\n' "${constraint//!race/}" | grep -qw race || continue
    grep -E '^func (Test|Fuzz|Example)[A-Za-z0-9_]*\(' "$f" | sed -E "s|^func ([A-Za-z0-9_]+).*|${f##*/}: \\1|" || true
  done
}

# Unit legs only: names of the mid/fourth lists that leg 7 (unit-heavy) runs
# instead of leg 10 (unit-mid). The race jobs ignore this list.
extra_list="$root/scripts/ci/$name-unit-heavy-extra-tests.txt"
extra_raw() { if [ -f "$extra_list" ]; then grep -v '^[[:space:]]*\(#\|$\)' "$extra_list" || true; fi; }
extra_names() { extra_raw | LC_ALL=C sort -u; }
extra_problems() {
  local dup stray
  dup="$(extra_raw | LC_ALL=C sort | uniq -d)"
  stray="$(LC_ALL=C comm -23 <(extra_names) <({ mid_names; fourth_names; } | LC_ALL=C sort -u))"
  [ -z "$dup" ] || printf 'named twice in %s:\n%s\n' "${extra_list##*/}" "$dup"
  [ -z "$stray" ] || printf 'named in %s but in neither the mid nor the fourth list:\n%s\n' "${extra_list##*/}" "$stray"
}
unit_heavy() { { LC_ALL=C comm -23 <(names) <({ mid_names; fourth_names; } | LC_ALL=C sort -u); extra_names; } | LC_ALL=C sort -u | join_regex; }
unit_mid() { LC_ALL=C comm -23 <({ mid_names; fourth_names; } | LC_ALL=C sort -u) <(extra_names) | join_regex; }

# devhealthsource's plain unit legs run three disjoint parts: heavy minus mid
# (-run, leg 8), mid (-run, leg 9), and everything else (-skip of both, leg 6).
# devhealthfacts runs the same shape: heavy minus mid/fourth plus the names of
# <package>-unit-heavy-extra-tests.txt (leg 7), mid plus fourth minus those names
# (leg 10), everything else (leg 5).
unit_partition_three_way() {
  local wf="$root/.github/workflows/ci.yml" want n all a b skipped c counted bad lost problems
  problems="$(extra_problems)"
  if [ -n "$problems" ]; then printf '%s\n' "$problems" >&2; return 1; fi
  for want in "race-split.sh $name unit-rest)" "race-split.sh $name unit-heavy)" "race-split.sh $name unit-mid)"; do
    # The `unit` job only: `unit-dedicated` carries the same steps (a contract
    # check keeps the two identical), so a count over the whole file would see two.
    n="$(awk '/^  unit:$/ { g=1; print; next } g && /^  [A-Za-z0-9_-]+:/ { g=0 } g' "$wf" | grep -cF -- "$want" || true)"
    [ "$n" -eq 1 ] || { printf 'the unit job of ci.yml must use "%s" exactly once, found %s\n' "$want" "$n" >&2; return 1; }
  done
  # -run vs -skip decides whether a part selects or excludes its tests: swapping one
  # keeps the selector text but doubles or drops tests.
  for want in 'skip|unit-rest' 'run|unit-heavy' 'run|unit-mid'; do
    flag="${want%%|*}"; mode="${want#*|}"
    grep -qE -- "GOFLAGS=\"-trimpath -$flag=\\\$\(scripts/ci/race-split\.sh $name $mode\)\"" "$wf" \
      || { printf 'ci.yml must pass "%s" to -%s\n' "$mode" "$flag" >&2; return 1; }
  done
  all="$(go test -list '.' "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
  problems="$(LC_ALL=C comm -23 <({ names; mid_names; fourth_names; extra_names; } | LC_ALL=C sort -u) <(printf '%s\n' "$all"))"
  if [ -n "$problems" ]; then printf 'a list names no such test:\n%s\n' "$problems" >&2; return 1; fi
  a="$(go test -list "$(unit_heavy)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
  b="$(go test -list "$(unit_mid)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
  skipped="$(go test -list "$(race_rest)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
  c="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$skipped"))"
  counted="$(printf '%s\n%s\n%s\n' "$a" "$b" "$c" | grep -v '^$' | LC_ALL=C sort | uniq -c)"
  bad="$(printf '%s\n' "$counted" | awk '$1 != 1 {print $1, $2}')"
  lost="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$counted" | awk '{print $2}' | LC_ALL=C sort -u))"
  if [ -n "$bad" ] || [ -n "$lost" ] || [ -z "$a" ] || [ -z "$b" ] || [ -z "$c" ]; then
    printf 'unit split is not a partition.\nrun more or less than once (count name):\n%s\nrun in no leg:\n%s\n' "$bad" "$lost" >&2
    return 1
  fi
  printf 'unit split ok: %s tests = %s heavy + %s mid + %s rest\n' \
    "$(printf '%s\n' "$all" | wc -l | tr -d ' ')" "$(printf '%s\n' "$a" | wc -l | tr -d ' ')" \
    "$(printf '%s\n' "$b" | wc -l | tr -d ' ')" "$(printf '%s\n' "$c" | wc -l | tr -d ' ')"
}

case "${2:-}" in
  heavy|rest) regex ;;
  partition)
    cd "$root"
    wf="$root/.github/workflows/ci.yml"
    unit_mode=heavy
    unit_regex="$(regex)"
    if [ "$name" = devhealthsource ] || [ "$name" = devhealthfacts ]; then
      dup="$(both_lists)"
      if [ -n "$dup" ]; then
        printf 'named in both race-mid and race-fourth lists:\n%s\n' "$dup" >&2
        exit 1
      fi
      unit_partition_three_way
      exit $?
    fi
    for want in "race-split.sh $name rest)" "race-split.sh $name $unit_mode)"; do
      [ "$unit_mode" = unit-heavy ] && [ "$want" = "race-split.sh $name rest)" ] && want="race-split.sh $name unit-heavy)"
      grep -qF -- "$want" "$wf" || { printf 'ci.yml never uses "%s"\n' "$want" >&2; exit 1; }
    done
    all="$(go test -list '.' "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    a="$(go test -list "$unit_regex" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
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
  unit-heavy) unit_heavy ;;
  unit-mid) unit_mid ;;
  unit-rest) race_rest ;;
  race-heavy) race_heavy ;;
  race-mid) race_mid ;;
  race-fourth) race_fourth ;;
  race-rest) race_rest ;;
  race-partition)
    cd "$root"
    wf="$root/.github/workflows/ci.yml"
    dup="$(both_lists)"
    if [ -n "$dup" ]; then
      printf 'named in both race-mid and race-fourth lists:\n%s\n' "$dup" >&2
      exit 1
    fi
    wants=("$name race-heavy)" "$name race-mid)" "$name race-rest)")
    [ -f "$fourth_list" ] && wants+=("$name race-fourth)")
    for want in "${wants[@]}"; do
      n="$(grep -cF -- "race-split.sh $want" "$wf" || true)"
      [ "$n" -eq 1 ] || { printf 'ci.yml must use "race-split.sh %s" exactly once, found %s\n' "$want" "$n" >&2; exit 1; }
    done
    # The flag matters as much as the mode (Codex #1967 r1): -skip of race-mid would run every non-mid test there.
    flags=('run|race-heavy' 'run|race-mid' 'skip|race-rest')
    [ -f "$fourth_list" ] && flags+=('run|race-fourth')
    for want in "${flags[@]}"; do
      flag="${want%%|*}"; mode="${want#*|}"
      grep -qE -- "GOFLAGS=\"-trimpath -$flag=\\\$\(scripts/ci/race-split\.sh $name $mode\)\"" "$wf" \
        || { printf 'ci.yml must pass "%s" to -%s\n' "$mode" "$flag" >&2; exit 1; }
    done
    if [ ! -f "$fourth_list" ] && grep -qF -- "race-split.sh $name race-fourth)" "$wf"; then
      printf 'ci.yml uses race-fourth for %s but %s does not exist\n' "$name" "$fourth_list" >&2
      exit 1
    fi
    all="$(go test -list '.' "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    problems="$(rest_list_problems "$all")"
    if [ -n "$problems" ]; then printf '%s\n' "$problems" >&2; exit 1; fi
    a="$(go test -list "$(race_heavy)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    b="$(go test -list "$(race_mid)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    d=""
    if [ -f "$fourth_list" ]; then
      d="$(go test -list "$(race_fourth)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    fi
    skipped="$(go test -list "$(race_rest)" "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    c="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$skipped"))"
    counted="$(printf '%s\n%s\n%s\n%s\n' "$a" "$b" "$d" "$c" | grep -v '^$' | LC_ALL=C sort | uniq -c)"
    bad="$(printf '%s\n' "$counted" | awk '$1 != 1 {print $1, $2}')"
    lost="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$counted" | awk '{print $2}' | LC_ALL=C sort -u))"
    if [ -n "$bad" ] || [ -n "$lost" ]; then
      printf 'race split is not a partition.\nrun more or less than once (count name):\n%s\nrun in no job:\n%s\n' "$bad" "$lost" >&2
      exit 1
    fi
    printf 'race split ok: %s tests = %s heavy + %s mid + %s fourth + %s rest\n' \
      "$(printf '%s\n' "$all" | wc -l | tr -d ' ')" "$(printf '%s\n' "$a" | wc -l | tr -d ' ')" \
      "$(printf '%s\n' "$b" | wc -l | tr -d ' ')" "$(printf '%s\n' "$d" | grep -c . || true)" \
      "$(printf '%s\n' "$c" | wc -l | tr -d ' ')"
    ;;
  verify)
    cd "$root"
    problems="$(race_only_tests)"
    if [ -n "$problems" ]; then
      printf 'a test function in a file built only under -race is invisible to go test -list, so its race job cannot be checked; move it to a file without the race constraint (read raceDetectorEnabled instead):\n%s\n' "$problems" >&2
      exit 1
    fi
    existing="$(go test -list '.' "$pkg" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort -u)"
    missing="$(LC_ALL=C comm -23 <({ names; [ -f "$mid_list" ] && mid_names; fourth_names; } | LC_ALL=C sort -u) <(printf '%s\n' "$existing"))"
    if [ -n "$missing" ]; then
      printf 'heavy list names no such test:\n%s\n' "$missing" >&2
      exit 1
    fi
    problems="$(extra_problems)"
    if [ -n "$problems" ]; then printf '%s\n' "$problems" >&2; exit 1; fi
    problems="$(rest_list_problems "$existing")"
    if [ -n "$problems" ]; then printf '%s\n' "$problems" >&2; exit 1; fi
    missing="$(LC_ALL=C comm -23 <(extra_names) <(printf '%s\n' "$existing"))"
    if [ -n "$missing" ]; then
      printf 'unit extra list names no such test:\n%s\n' "$missing" >&2
      exit 1
    fi
    printf 'heavy list ok: %s tests\n' "$(names | wc -l | tr -d ' ')"
    ;;
  *) printf 'usage: %s <package> heavy|rest|verify|partition\n' "${0##*/}" >&2; exit 2 ;;
esac
