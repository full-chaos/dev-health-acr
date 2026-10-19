#!/usr/bin/env bash
set -euo pipefail

# test-unit-race-dedup.sh proves that every non-race `unit` leg the plan job
# may drop on a pull request as a race duplicate (scripts/ci/reached-shards.sh
# rule 6) loses no test: the tests that leg runs, listed by `go test -list`
# with the leg's own -run/-skip from ci.yml, must all run in the race jobs the
# plan names for it (dedup_needs), each with its own -run/-skip, and none of
# them may be skipped under -race.
#
# The legs are read from the selector itself, run as a pull request that
# reaches every package with the heavy lanes on (the widest drop set). For
# every dropped leg it checks:
#   1. every package of the leg is a unit-dedicated package
#      (test-shard.sh --print-unit-dedicated-packages);
#   2. every isolated race job in ci.yml that selects the package is in
#      dedup_needs, and at least one does;
#   3. tests(leg), listed from the plain build, is a subset of the union of
#      tests(race job), listed from the -race build, so a test the race build
#      does not compile counts as not run;
#   4. no test of the leg can behave differently under -race, as listed by
#      scripts/ci/race-affected-tests.sh (raceDetectorEnabled, race.Enabled and
#      !race build-tagged declarations, followed through helpers, variables and
#      in-module packages; scripts/ci/test-race-affected-tests.sh is its
#      fixture). testing.Short() is not a race signal: neither side passes
#      -short (check 5), so it is false in both runs;
#   5. neither the leg nor its race jobs narrow the run by -short or -tags (in
#      their ci.yml blocks or the Makefile test-coverage / test-race recipes),
#      and the race jobs carry every job-level env key the unit job carries;
#   6. the leg's packages have no test that depends on a tool the unit job
#      installs and the race jobs do not (exec.LookPath, psql, pg_isready).
# Negative controls at the end plant one defect each in a copy of ci.yml (or
# force a leg the selector keeps) and require the check to fail.
#
# Usage: test-unit-race-dedup.sh

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
module="$(awk '/^module / { print $2; exit }' "$repo_root/go.mod")"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$repo_root"

job_block() {
  awk -v job="$2" '
    $0 ~ "^  " job ":" { grab=1; print; next }
    grab && /^  [A-Za-z0-9_-]+:/ { grab=0 }
    grab { print }
  ' "$1"
}

list_jobs() {
  awk '
    /^jobs:/ { in_jobs=1; next }
    in_jobs && /^[^[:space:]]/ { in_jobs=0 }
    in_jobs && /^  [A-Za-z0-9_-]+:/ { line=$0; sub(/^  /, "", line); sub(/:.*/, "", line); print line }
  ' "$1"
}

# go_tests PKG [run|skip REGEX] -> the top-level tests that invocation runs.
# With RACE=1 the list comes from the -race build, so a test the race build
# does not compile (any build constraint form) is absent from it. A control
# below drops one name from that list through RACE_LIST_DROP.
# `go test -list .` runs once per package (cached in $tmp); the race-split
# regexes are anchored name alternations (^(A|B)$), which grep -E reads the
# same way go test -run does for top-level names.
go_tests() {
  local pkg="$1" mode="${2:-all}" regex="${3:-}" cache flags=()
  [ "${RACE:-0}" != 1 ] || flags=(-race)
  cache="$tmp/list${RACE:-0}.$(printf '%s' "$pkg" | tr '/.' '__')"
  if [ ! -s "$cache" ]; then
    go test "${flags[@]}" -list '.' "$pkg" | grep '^Test' | LC_ALL=C sort -u >"$cache"
    [ -s "$cache" ] || { printf 'go test%s -list found no test in %s\n' "${flags[*]:+ ${flags[*]}}" "$pkg" >&2; return 1; }
    if [ "${RACE:-0}" = 1 ] && [ -n "${RACE_LIST_DROP:-}" ]; then
      grep -vxF "$RACE_LIST_DROP" "$cache" >"$cache.drop" || true
      mv "$cache.drop" "$cache"
    fi
  fi
  case "$regex" in '' | '^('*')$') ;; *) printf 'unexpected selection regex %s\n' "$regex" >&2; return 1 ;; esac
  case "$mode" in
    all) cat "$cache" ;;
    run) grep -E -- "$regex" "$cache" || true ;;
    skip) grep -vE -- "$regex" "$cache" || true ;;
  esac
}

# selection BLOCK_LINE -> "run REGEX" / "skip REGEX" / "all" for a GOFLAGS line.
selection() {
  local line="$1" flag split mode
  if [ -z "$line" ]; then
    printf 'all\n'
    return 0
  fi
  flag="$(sed -nE 's/.*-trimpath -(run|skip)=\$\(scripts\/ci\/race-split\.sh [a-z]+ [a-z-]+\)".*/\1/p' <<<"$line")"
  split="$(sed -nE 's/.*race-split\.sh ([a-z]+) ([a-z-]+)\)".*/\1/p' <<<"$line")"
  mode="$(sed -nE 's/.*race-split\.sh ([a-z]+) ([a-z-]+)\)".*/\2/p' <<<"$line")"
  if [ -z "$flag" ] || [ -z "$split" ] || [ -z "$mode" ]; then
    printf 'unreadable GOFLAGS line: %s\n' "$line" >&2
    return 1
  fi
  printf '%s %s\n' "$flag" "$(scripts/ci/race-split.sh "$split" "$mode")"
}

env_keys() { # JOB_BLOCK on stdin -> job-level env keys
  awk '/^    env:/ { e=1; next } e && /^      [A-Z_][A-Z0-9_]*:/ { k=$1; sub(/:$/, "", k); print k; next } e && /^    [a-z]/ { e=0 }' | LC_ALL=C sort -u
}

# check WORKFLOW [FORCED_LEGS] -> 0 when every dropped leg is covered.
check() {
  local wf="$1" forced="${2:-}" out unit_skip dedup legs n status=0
  local unit_block call leg_pkgs pkg job block sub line sel leg_sel covered want union skipped hit dedicated
  printf 'go.mod\n' >"$tmp/changed"
  out="$(scripts/ci/reached-shards.sh --event pull_request --heavy true --changed "$tmp/changed" --workflow "$wf" 2>/dev/null)"
  unit_skip="$(sed -n 's/^unit_skip=//p' <<<"$out")"
  dedup="$(sed -n 's/^dedup_needs=//p' <<<"$out")"
  if [ -n "$forced" ]; then
    legs="$forced"
  else
    legs="$(grep -oE '"shard":[0-9]+' <<<"$unit_skip" | cut -d: -f2 | tr '\n' ' ')"
  fi
  if [ -z "${legs// /}" ]; then
    printf 'the selector drops no unit leg as a race duplicate (unit_skip=%s), so this check proves nothing\n' "$unit_skip" >&2
    return 1
  fi
  dedicated=" $(scripts/ci/test-shard.sh --print-unit-dedicated-packages) "
  unit_block="$(job_block "$wf" unit)"
  call="$(grep -oE 'scripts/ci/test-shard\.sh [^)]*' <<<"$unit_block" | head -n1)"
  call="${call#scripts/ci/test-shard.sh }"
  if grep -qE -- '(^|[^a-z])-short|-tags' <<<"$unit_block"; then
    printf 'the unit job narrows its run with -short or -tags\n' >&2
    status=1
  fi
  for target in test-coverage test-race; do
    if awk -v t="$target:" '$1 == t { g=1; next } g && /^[^\t]/ { g=0 } g' Makefile | grep -qE -- '-short|-tags'; then
      printf 'Makefile %s passes -short or -tags\n' "$target" >&2
      status=1
    fi
  done
  for n in $legs; do
    # shellcheck disable=SC2086,SC2016 # the job's own argument list; the literal GitHub expression text
    read -r -a leg_pkgs <<<"$(scripts/ci/test-shard.sh ${call//'${{ matrix.shard }}'/$n})"
    line="$(grep -E "^ +$n\) export GOFLAGS=" <<<"$unit_block" | head -n1 || true)"
    leg_sel="$(selection "$line")" || return 1
    for pkg in "${leg_pkgs[@]}"; do
      case "$dedicated" in *" $pkg "*) ;; *) printf 'unit leg %s holds %s, which is not unit-dedicated\n' "$n" "$pkg" >&2; status=1; continue ;; esac
      covered=0
      union=""
      while IFS= read -r job; do
        block="$(job_block "$wf" "$job")"
        sub="$(grep -oE 'test-shard\.sh[[:space:]]+isolated(-[a-z]+)?([[:space:]]+[a-z0-9]+)?' <<<"$block" | head -n1 | awk '{$1=""; sub(/^ /,""); print}' || true)"
        [ -n "$sub" ] || continue
        # shellcheck disable=SC2086 # "<subcommand> [basename]", split on purpose
        case " $(scripts/ci/test-shard.sh $sub) " in *" $pkg "*) ;; *) continue ;; esac
        covered=1
        case "$dedup" in *"\"$job\""*) ;; *) printf 'race job %s runs %s but is not in dedup_needs=%s\n' "$job" "$pkg" "$dedup" >&2; status=1 ;; esac
        if grep -qE -- '(^|[^a-z])-short|-tags' <<<"$block"; then
          printf 'race job %s narrows its run with -short or -tags\n' "$job" >&2
          status=1
        fi
        want="$(LC_ALL=C comm -23 <(env_keys <<<"$unit_block") <(env_keys <<<"$block"))"
        if [ -n "$want" ]; then
          printf 'race job %s lacks the unit job env key(s): %s\n' "$job" "$(tr '\n' ' ' <<<"$want")" >&2
          status=1
        fi
        sel="$(selection "$(grep -E '^ +export GOFLAGS=' <<<"$block" | head -n1 || true)")" || return 1
        # shellcheck disable=SC2086 # "<run|skip> <regex>" or "all", split on purpose
        union+="$(RACE=1 go_tests "./${pkg#"$module"/}" $sel)"$'\n'
      done < <(list_jobs "$wf")
      if [ "$covered" -eq 0 ]; then
        printf 'unit leg %s holds %s, which no isolated race job runs\n' "$n" "$pkg" >&2
        status=1
        continue
      fi
      # shellcheck disable=SC2086 # "<run|skip> <regex>" or "all", split on purpose
      go_tests "./${pkg#"$module"/}" $leg_sel >"$tmp/leg"
      [ -s "$tmp/leg" ] || { printf 'unit leg %s lists no test for %s\n' "$n" "$pkg" >&2; status=1; continue; }
      printf '%s' "$union" | grep -v '^$' | LC_ALL=C sort -u >"$tmp/union"
      want="$(LC_ALL=C comm -23 "$tmp/leg" "$tmp/union")"
      if [ -n "$want" ]; then
        printf 'unit leg %s runs %s test(s) of %s that no race job runs: %s\n' "$n" "$(wc -l <<<"$want" | tr -d ' ')" "$pkg" "$(head -n 5 <<<"$want" | tr '\n' ' ')" >&2
        status=1
      fi
      skipped="$(scripts/ci/race-affected-tests.sh "${pkg#"$module"/}")" || { printf 'race-affected-tests.sh failed for %s\n' "$pkg" >&2; status=1; continue; }
      hit="$(LC_ALL=C comm -12 "$tmp/leg" <(printf '%s\n' "$skipped" | grep -v '^$' || true))"
      if [ -n "$hit" ]; then
        printf 'unit leg %s runs test(s) of %s that skip under -race: %s\n' "$n" "$pkg" "$(tr '\n' ' ' <<<"$hit")" >&2
        status=1
      fi
      if grep -lE 'exec\.LookPath\(|pg_isready|"psql"' "$repo_root/${pkg#"$module"/}"/*_test.go >/dev/null 2>&1; then
        printf '%s has a test that depends on a tool the race jobs may not install\n' "$pkg" >&2
        status=1
      fi
      [ "$status" -ne 0 ] || printf 'unit leg %s: %s tests of %s, all run by its race jobs, none skipped under -race\n' "$n" "$(wc -l <"$tmp/leg" | tr -d ' ')" "$pkg"
    done
  done
  return "$status"
}

wf="$repo_root/.github/workflows/ci.yml"
check "$wf"
printf 'PASS: every unit leg dropped as a race duplicate is covered by its race jobs\n'

assert_fails() { # DESC WORKFLOW [FORCED_LEGS]
  if check "$2" "${3:-}" >"$tmp/control.log" 2>&1; then
    printf 'NEGATIVE CONTROL FAILED: %s -- the check passed\n' "$1" >&2
    exit 1
  fi
  printf 'control ok: %s -> %s\n' "$1" "$(grep -vE '^unit leg [0-9]+: ' "$tmp/control.log" | head -n1)"
}

mutate() { # NAME OLD NEW -> a copy of ci.yml with the first OLD replaced
  local out="$tmp/$1.yml"
  awk -v old="$2" -v new="$3" '!done && index($0, old) { $0 = substr($0, 1, index($0, old) - 1) new substr($0, index($0, old) + length(old)); done=1 } { print }' "$wf" >"$out"
  cmp -s "$wf" "$out" && { printf 'NEGATIVE CONTROL SETUP FAILED: %s did not change ci.yml\n' "$1" >&2; exit 1; }
  printf '%s' "$out"
}

# A race list cut: the devhealthsource heavy race job runs the mid list, so the
# heavy tests run in no race job while unit leg 8 is still dropped.
assert_fails 'devhealthsource race-heavy job runs the mid list instead' \
  "$(mutate heavy-cut 'race-split.sh devhealthsource race-heavy)' 'race-split.sh devhealthsource race-mid)')"
# A package removed from a race job: the devhealthsource rest race job (the
# first that selects devhealthsource) selects factoracle instead.
assert_fails 'race-devhealthsource selects another package' \
  "$(mutate pkg-cut 'test-shard.sh isolated-dedicated devhealthsource)' 'test-shard.sh isolated-dedicated factoracle)')"
# A race job narrowed by -short.
# shellcheck disable=SC2016 # the literal workflow text
assert_fails 'race-devhealthfacts-heavy runs with -short' \
  "$(mutate short 'export GOFLAGS="-trimpath -run=$(scripts/ci/race-split.sh devhealthfacts race-heavy)"' 'export GOFLAGS="-trimpath -short -run=$(scripts/ci/race-split.sh devhealthfacts race-heavy)"')"
# A test the race build does not compile: one name of the devhealthfacts heavy
# leg is dropped from the -race test list.
drop="$(RACE=0 go_tests ./internal/contextfabric/devhealthfacts run "$(scripts/ci/race-split.sh devhealthfacts heavy)" | head -n1)"
[ -n "$drop" ] || { printf 'NEGATIVE CONTROL SETUP FAILED: no devhealthfacts heavy test to drop\n' >&2; exit 1; }
rm -f "$tmp"/list1.*
if RACE_LIST_DROP="$drop" check "$wf" >"$tmp/control.log" 2>&1; then
  printf 'NEGATIVE CONTROL FAILED: %s missing from the -race list -- the check passed\n' "$drop" >&2
  exit 1
fi
printf 'control ok: %s missing from the -race list -> %s\n' "$drop" "$(grep -vE '^unit leg [0-9]+: ' "$tmp/control.log" | head -n1)"
rm -f "$tmp"/list1.*

# A leg the selector keeps because tests in it skip under -race (devhealthfacts
# rest), forced into the drop set.
assert_fails 'devhealthfacts rest leg forced into the drop set' "$wf" 5
printf 'PASS: all negative controls failed the check\n'
