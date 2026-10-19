#!/usr/bin/env bash
set -euo pipefail

# observed-race-dedup.sh decides which non-race `unit` legs of the dedicated
# packages (internal/contextfabric/devhealthfacts and devhealthsource) a pull
# request may drop because the race jobs already ran every test of the leg and
# every one of them PASSED under -race.
#
# The decision is made from what the race jobs observed, never from a static
# reading of the source. Each race job of a dedicated package keeps its
# test2json events (run-isolated-budget.sh with RACE_JSON_OUT) and uploads them
# as the artifact race-json-<job key>. A leg is dropped only when ALL of:
#   - the event is pull_request (push and workflow_dispatch keep every leg);
#   - the leg's compiled test list is present and not empty (a listing that
#     failed keeps the leg);
#   - every race job that covers the leg's package is named in the lane results,
#     ran with result success, and left an artifact holding a package-level pass
#     event of that package; the covering jobs are READ from ci.yml (jobs that
#     run `isolated-dedicated <package>` through run-isolated-budget.sh) and
#     their number is pinned, so a new covering job keeps every leg until the
#     pin and the plan's needs are updated;
#   - every top-level test of the leg has a pass event in that output;
#   - no skip event exists for that test or any of its subtests, and no
#     package-level skip event exists (a test the race build skips, or
#     a helper that skips inside a subtest, keeps the leg).
# Anything else keeps the leg and says why on stderr.
#
# The leg's test list is the list `go test -list` prints for the leg's own
# selection, read from the `unit` job of ci.yml (the case table of the step
# `Run Go tests with coverage and JUnit reporting`), never a second copy:
# `-run R` legs list R, `-skip S` legs list everything minus S. `go test -list`
# builds the NON-race test binary and runs it with -test.list only: package
# init and TestMain run, no test body does (the two packages' TestMain does
# nothing before m.Run).
#
# Subcommands:
#   legs     [--workflow F]                    print `leg<TAB>package<TAB>mode<TAB>mode-arg` per dedicated leg
#   covering [--workflow F] PACKAGE            print the race job keys that cover PACKAGE, one per line
#   collect  --out DIR [--workflow F] [--legs "6 7 8 9"]
#                                              write DIR/leg-N.package and DIR/leg-N.tests per leg (go + compile only)
#   defer    --event E --heavy true|false --skip-jobs JSON --unit-skip JSON [--workflow F]
#                                              the plan job's step: unit_defer, unit_exclude and skip_jobs outputs
#   decide   --event E --lists DIR --race-dir DIR --needs FILE [--defer JSON] [--unit-skip JSON] [--workflow F]
#                                              data only (jq): print drop=<JSON list of {"shard":N}>, exclude and run on stdout,
#                                              a per-leg report on stderr; --race-dir holds one directory per
#                                              artifact race-json-<job key>, each with race.json

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

expected_covering_devhealthfacts=4
expected_covering_devhealthsource=3
default_legs="6 7 8 9"

die() { printf 'observed-race-dedup: %s\n' "$*" >&2; exit 2; }

job_block() { # workflow job
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

# leg_selection WORKFLOW N -> "package<TAB>mode<TAB>mode-arg" or return 1
leg_selection() {
  local wf="$1" n="$2" line
  line="$(job_block "$wf" unit | grep -E "^[[:space:]]+${n}\\) export GOFLAGS=" || true)"
  [ "$(printf '%s\n' "$line" | grep -c .)" -eq 1 ] || return 1
  printf '%s\n' "$line" | sed -nE 's#^[[:space:]]+[0-9]+\) export GOFLAGS="-trimpath -(run|skip)=\$\(scripts/ci/race-split\.sh ([a-z]+) ([a-z-]+)\)"; split_pkg=([a-z]+)( ;;|;.*)$#\4\t\1\t\3#p' | grep .
}

# covering_jobs WORKFLOW PACKAGE -> job keys, one per line
covering_jobs() {
  awk -v want="isolated-dedicated $2" '
    /^jobs:/ { in_jobs=1; next }
    in_jobs && /^[^[:space:]]/ { in_jobs=0 }
    in_jobs && /^  [A-Za-z0-9_-]+:/ {
      if (job != "" && a && b) print job
      job=$0; sub(/^  /, "", job); sub(/:.*/, "", job); a=0; b=0; next
    }
    in_jobs && index($0, want) { a=1 }
    in_jobs && index($0, "run-isolated-budget.sh") { b=1 }
    END { if (job != "" && a && b) print job }
  ' "$1"
}

expected_covering() {
  case "$1" in
    devhealthfacts) printf '%s' "$expected_covering_devhealthfacts" ;;
    devhealthsource) printf '%s' "$expected_covering_devhealthsource" ;;
    *) printf '0' ;;
  esac
}

cmd_legs() {
  local wf="$repo_root/.github/workflows/ci.yml" n sel
  while [ "$#" -gt 0 ]; do
    case "$1" in --workflow) wf="$2"; shift 2 ;; *) die "legs: unknown argument $1" ;; esac
  done
  for n in $default_legs; do
    sel="$(leg_selection "$wf" "$n")" || die "leg $n: no single parsable GOFLAGS selection in the unit job of $wf"
    printf '%s\t%s\n' "$n" "$sel"
  done
}

# go_list_tests REGEX PACKAGE-DIR -> sorted top-level test names, one per line
go_list_tests() {
  local regex="$1" dir="$2" out
  out="$(cd "$repo_root" && go test -trimpath -list "$regex" "./internal/contextfabric/$dir")" || return 1
  printf '%s\n' "$out" | grep '^Test' | LC_ALL=C sort -u
}

cmd_collect() {
  local wf="$repo_root/.github/workflows/ci.yml" out="" legs="$default_legs" n sel pkg mode arg regex all part
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --workflow) wf="$2"; shift 2 ;;
      --out) out="$2"; shift 2 ;;
      --legs) legs="$2"; shift 2 ;;
      *) die "collect: unknown argument $1" ;;
    esac
  done
  [ -n "$out" ] || die "collect: --out is required"
  mkdir -p "$out"
  for n in $legs; do
    rm -f "$out/leg-$n.package" "$out/leg-$n.tests"
    if ! sel="$(leg_selection "$wf" "$n")"; then
      printf 'observed-race-dedup: leg %s: selection not parsable from the unit job; the leg stays\n' "$n" >&2
      continue
    fi
    IFS=$'\t' read -r pkg mode arg <<<"$sel"
    if ! regex="$("$repo_root/scripts/ci/race-split.sh" "$pkg" "$arg")"; then
      printf 'observed-race-dedup: leg %s: race-split.sh %s %s failed; the leg stays\n' "$n" "$pkg" "$arg" >&2
      continue
    fi
    case "$mode" in
      run)
        part="$(go_list_tests "$regex" "$pkg")" || { printf 'observed-race-dedup: leg %s: go test -list failed; the leg stays\n' "$n" >&2; continue; }
        ;;
      skip)
        all="$(go_list_tests '.' "$pkg")" || { printf 'observed-race-dedup: leg %s: go test -list failed; the leg stays\n' "$n" >&2; continue; }
        part="$(go_list_tests "$regex" "$pkg")" || { printf 'observed-race-dedup: leg %s: go test -list failed; the leg stays\n' "$n" >&2; continue; }
        part="$(LC_ALL=C comm -23 <(printf '%s\n' "$all") <(printf '%s\n' "$part"))"
        ;;
      *) printf 'observed-race-dedup: leg %s: unknown mode %s; the leg stays\n' "$n" "$mode" >&2; continue ;;
    esac
    printf '%s\n' "$pkg" >"$out/leg-$n.package"
    printf '%s\n' "$part" | grep . >"$out/leg-$n.tests" || : >"$out/leg-$n.tests"
    printf 'observed-race-dedup: leg %s: package %s, %s tests (non-race compiled list, %s %s)\n' "$n" "$pkg" "$(grep -c . "$out/leg-$n.tests")" "$mode" "$arg" >&2
  done
}

# summarize_race_json FILE PACKAGE -> JSON {pk, pass:{}, skip:{}, fail:{}}
summarize_race_json() {
  jq -Rn --arg suf "/internal/contextfabric/$2" '
    reduce (inputs | fromjson? | select(type == "object" and (.Package // "" | endswith($suf)) and (.Action == "pass" or .Action == "skip"))) as $e
      ({pk: false, pass: {}, skip: {}};
        if $e.Test == null then
          (if $e.Action == "pass" then .pk = true else .skip["<package>"] = true end)
        elif $e.Action == "pass" then .pass[$e.Test] = true
        else .skip[$e.Test] = true end)
  ' "$1"
}

cmd_decide() {
  local wf="$repo_root/.github/workflows/ci.yml" event="" lists="" racedir="" needs="" defer="" unit_skip="[]" tmp
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --workflow) wf="$2"; shift 2 ;;
      --unit-skip) unit_skip="$2"; shift 2 ;;
      --event) event="$2"; shift 2 ;;
      --lists) lists="$2"; shift 2 ;;
      --race-dir) racedir="$2"; shift 2 ;;
      --needs) needs="$2"; shift 2 ;;
      --defer) defer="$2"; shift 2 ;;
      *) die "decide: unknown argument $1" ;;
    esac
  done
  if [ -z "$event" ] || [ -z "$lists" ] || [ -z "$racedir" ] || [ -z "$needs" ]; then
    die "decide: --event, --lists, --race-dir and --needs are required"
  fi
  if [ "$event" != pull_request ]; then
    printf 'observed-race-dedup: event %s keeps every leg\n' "$event" >&2
    emit_decision '[]' "$unit_skip" "$defer"
    return 0
  fi
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  local cand n drops=() pkg tests jobs job want got ok reason merged file res
  if [ -n "$defer" ]; then
    cand="$(printf '%s' "$defer" | jq -r '.[].shard' 2>/dev/null)" || cand=""
  else
    cand="${default_legs// /$'\n'}"
  fi
  for n in $cand; do
    ok=yes
    reason=""
    pkg="$(cat "$lists/leg-$n.package" 2>/dev/null || true)"
    tests="$lists/leg-$n.tests"
    if [ -z "$pkg" ] || [ ! -f "$tests" ]; then
      printf 'observed-race-dedup: leg %s: KEEP: no compiled test list\n' "$n" >&2
      continue
    fi
    if [ "$(grep -c . "$tests")" -eq 0 ]; then
      printf 'observed-race-dedup: leg %s: KEEP: the compiled test list is empty\n' "$n" >&2
      continue
    fi
    want="$(expected_covering "$pkg")"
    jobs="$(covering_jobs "$wf" "$pkg")"
    got="$(printf '%s\n' "$jobs" | grep -c . || true)"
    if [ "$want" -eq 0 ] || [ "$got" -ne "$want" ]; then
      printf 'observed-race-dedup: leg %s: KEEP: %s covering race jobs read from ci.yml for %s, pinned %s\n' "$n" "$got" "$pkg" "$want" >&2
      continue
    fi
    merged="$tmp/merged-$pkg-$n.json"
    printf '{"pass":{},"skip":{}}' >"$merged"
    for job in $jobs; do
      res="$(jq -r --arg j "$job" '.[$j].result // "absent"' "$needs" 2>/dev/null || printf 'unreadable')"
      if [ "$res" != success ]; then
        ok=no; reason="covering job $job result is $res"; break
      fi
      file="$racedir/race-json-$job/race.json"
      if ! summarize_race_json "$file" "$pkg" >"$tmp/sum.json" 2>/dev/null; then
        ok=no; reason="covering job $job left no readable race.json"; break
      fi
      if [ "$(jq -r '.pk' "$tmp/sum.json")" != true ]; then
        ok=no; reason="covering job $job has no package-level pass event for $pkg"; break
      fi
      jq -n --slurpfile a "$merged" --slurpfile b "$tmp/sum.json" \
        '{pass: ($a[0].pass + $b[0].pass), skip: ($a[0].skip + $b[0].skip)}' >"$tmp/next.json"
      mv "$tmp/next.json" "$merged"
    done
    if [ "$ok" = yes ]; then
      local bad
      bad="$(jq -Rn --slurpfile m "$merged" '
        [inputs | select(length > 0)] as $L
        | ($m[0].skip | keys) as $skips
        | [ $L[] as $t
            | if ($m[0].pass[$t] // false | not) then "absent:" + $t
              elif ($skips | any(. == $t or . == "<package>" or startswith($t + "/"))) then "skip:" + $t
              else empty end ]
        | if ($skips | index("<package>")) != null then ["skip:<package>"] + . else . end' "$tests")"
      if [ "$(printf '%s' "$bad" | jq 'length')" -ne 0 ]; then
        ok=no
        reason="$(printf '%s' "$bad" | jq -r 'length') test(s) not observed passing under -race: $(printf '%s' "$bad" | jq -r '.[:5] | join(", ")')"
      fi
    fi
    if [ "$ok" = yes ]; then
      printf 'observed-race-dedup: leg %s: DROP: %s tests of %s all passed under -race in %s covering jobs\n' "$n" "$(grep -c . "$tests")" "$pkg" "$got" >&2
      drops+=("$n")
    else
      printf 'observed-race-dedup: leg %s: KEEP: %s\n' "$n" "$reason" >&2
    fi
  done
  if [ "${#drops[@]}" -eq 0 ]; then
    emit_decision '[]' "$unit_skip" "$defer"
  else
    emit_decision "$(printf '%s\n' "${drops[@]}" | sort -n | jq -Rsc 'split("\n") | map(select(length > 0) | {shard: tonumber})')" "$unit_skip" "$defer"
  fi
}

# emit_decision DROP UNIT_SKIP DEFER: GITHUB_OUTPUT lines. exclude is what the
# unit-dedicated matrix leaves out (the plan's unit_skip plus the dropped legs);
# run is true when at least one deferred leg is left to run.
emit_decision() {
  local drop="$1" skip="$2" defer="${3:-[]}"
  [ -n "$defer" ] || defer='[]'
  jq -nrc --argjson drop "$drop" --argjson skip "$skip" --argjson defer "$defer" '
    ($skip + $drop | unique_by(.shard)) as $ex
    | "drop=\($drop | tostring)",
      "exclude=\($ex | tostring)",
      "run=\([$defer[] | select(.shard as $s | $ex | any(.shard == $s) | not)] | length > 0)"'
}

# unit_matrix_legs WORKFLOW -> the shard numbers of the `unit` matrix, one per line
unit_matrix_legs() {
  job_block "$1" unit | grep -E '^[[:space:]]+shard: *\[' | head -n1 | sed -E 's/.*\[([^]]*)\].*/\1/' | tr ',' '\n' | tr -d ' ' | grep -E '^[0-9]+$' | sort -n
}

# defer: the plan job's step. On a heavy pull_request the dedicated unit legs
# wait for the race jobs (unit-dedup-plan, unit-dedicated); everywhere else they
# stay in the `unit` matrix and start with it. Prints unit_defer, unit_exclude
# (what the `unit` matrix leaves out) and skip_jobs (the reach step's list plus
# `unit` when every leg of the matrix is left out).
cmd_defer() {
  local wf="$repo_root/.github/workflows/ci.yml" event="" heavy="" skip_jobs="[]" unit_skip="[]" legs n defer='[]' exclude all_excluded
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --workflow) wf="$2"; shift 2 ;;
      --event) event="$2"; shift 2 ;;
      --heavy) heavy="$2"; shift 2 ;;
      --skip-jobs) skip_jobs="$2"; shift 2 ;;
      --unit-skip) unit_skip="$2"; shift 2 ;;
      *) die "defer: unknown argument $1" ;;
    esac
  done
  printf '%s' "$skip_jobs" | jq -e 'type == "array"' >/dev/null || die "defer: --skip-jobs is not a JSON list"
  printf '%s' "$unit_skip" | jq -e 'type == "array"' >/dev/null || die "defer: --unit-skip is not a JSON list"
  legs="$(unit_matrix_legs "$wf")"
  [ -n "$legs" ] || die "defer: no shard matrix in the unit job of $wf"
  if [ "$event" = pull_request ] && [ "$heavy" = true ]; then
    defer="$(cmd_legs --workflow "$wf" | awk -F'\t' '{ print $1 }' | jq -Rsc 'split("\n") | map(select(length > 0) | {shard: tonumber})')"
    for n in $(printf '%s' "$defer" | jq -r '.[].shard'); do
      printf '%s\n' "$legs" | grep -qx "$n" || die "defer: leg $n is not in the unit matrix"
    done
  fi
  exclude="$(jq -nc --argjson a "$unit_skip" --argjson b "$defer" '$a + $b | unique_by(.shard)')"
  all_excluded="$(jq -n --argjson ex "$exclude" --argjson legs "$(printf '%s\n' "$legs" | jq -Rsc 'split("\n") | map(select(length > 0) | tonumber)')" '[$legs[] | select(. as $l | $ex | any(.shard == $l) | not)] | length == 0')"
  if [ "$all_excluded" = true ]; then
    skip_jobs="$(printf '%s' "$skip_jobs" | jq -c '. + ["unit"] | unique')"
  fi
  printf 'unit_defer=%s\nunit_exclude=%s\nskip_jobs=%s\n' "$defer" "$exclude" "$skip_jobs"
}

cmd_covering() {
  local wf="$repo_root/.github/workflows/ci.yml" pkg=""
  while [ "$#" -gt 0 ]; do
    case "$1" in --workflow) wf="$2"; shift 2 ;; *) pkg="$1"; shift ;; esac
  done
  [ -n "$pkg" ] || die "covering: a package is required"
  covering_jobs "$wf" "$pkg"
}

cmd="${1:-}"
[ "$#" -gt 0 ] && shift
case "$cmd" in
  covering) cmd_covering "$@" ;;
  legs) cmd_legs "$@" ;;
  defer) cmd_defer "$@" ;;
  collect) cmd_collect "$@" ;;
  decide) cmd_decide "$@" ;;
  *) die "usage: ${0##*/} legs|collect|decide ..." ;;
esac
