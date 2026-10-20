#!/usr/bin/env bash
set -euo pipefail

# Fixture test for scripts/ci/reached-shards.sh (which unit/race shards a pull
# request runs) and for the verify job's lane check that accepts the skips it
# declares. Both halves run the REAL code: reached-shards.sh against this
# repository's own package graph and ci.yml, and the lane check as the exact
# `run:` text of verify's "Assert every lane succeeded" step, cut out of
# ci.yml and run under bash with fixture `needs` JSON.
#
# Run with REACHED=<other script> or WORKFLOW=<other ci.yml> to prove the test
# goes red against a selector or a lane check that lacks a rule.

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$here/../.." && pwd)"
reached="${REACHED:-$here/reached-shards.sh}"
workflow="${WORKFLOW:-$repo_root/.github/workflows/ci.yml}"
module="$(awk '/^module / { print $2; exit }' "$repo_root/go.mod")"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0

fail() {
  printf 'FAIL %s\n' "$*"
  fails=$((fails + 1))
}
ok() { printf 'ok   %s\n' "$*"; }

# run_reached NAME EVENT PATH... -> writes $tmp/NAME.out (key=value) and
# $tmp/NAME.reached (one reached package per line). Never fails the caller:
# a selector that exits non-zero leaves an empty .out, which every assertion
# below reads as "no verdict".
run_reached() {
  local name="$1" event="$2"
  shift 2
  printf '%s\n' "$@" >"$tmp/$name.changed"
  : >"$tmp/$name.out"
  : >"$tmp/$name.reached"
  (cd "$repo_root" && "$reached" --event "$event" --changed "$tmp/$name.changed" \
    --reached-out "$tmp/$name.reached") >"$tmp/$name.out" 2>"$tmp/$name.err" || true
}

out_value() { # NAME KEY
  sed -n "s/^$2=//p" "$tmp/$1.out" | tail -n1
}

expect_full() { # NAME
  local name="$1" mode skip unit race
  mode="$(out_value "$name" reach_mode)"
  skip="$(out_value "$name" skip_jobs)"
  unit="$(out_value "$name" unit_skip)"
  race="$(out_value "$name" race_skip)"
  if [ "$mode" = full ] && [ "$skip" = '[]' ] && [ "$unit" = '[]' ] && [ "$race" = '[]' ]; then
    ok "$name runs every shard ($(out_value "$name" reach_reason))"
  else
    fail "$name: want reach_mode=full and every skip list [], got mode=${mode:-<none>} skip_jobs=${skip:-<none>} unit_skip=${unit:-<none>} race_skip=${race:-<none>}"
    sed 's/^/    /' "$tmp/$name.err" | tail -n 5
  fi
}

# ---- shard lists, read the way ci.yml runs them -----------------------------

job_block() {
  awk -v job="$2" '
    $0 ~ "^  " job ":" { grab=1; print; next }
    grab && /^  [A-Za-z0-9_-]+:/ { grab=0 }
    grab { print }
  ' "$1"
}

# leg_packages JOB N -> the packages leg N of a matrix job runs, by calling the
# job's own test-shard.sh line with ${{ matrix.shard }} set to N.
leg_packages() {
  local job="$1" n="$2" call
  call="$(job_block "$workflow" "$job" | grep -oE 'scripts/ci/test-shard\.sh [^)]*' | head -n1)"
  call="${call#scripts/ci/test-shard.sh }"
  # shellcheck disable=SC2016 # the literal GitHub expression text
  call="${call//'${{ matrix.shard }}'/$n}"
  # shellcheck disable=SC2086 # the job's own argument list, split on purpose
  (cd "$repo_root" && scripts/ci/test-shard.sh $call) | tr ' ' '\n' | grep -v '^$'
}

matrix_legs() {
  job_block "$workflow" "$1" | grep -E '^ {8}shard: *\[' | head -n1 | sed -E 's/.*\[([^]]*)\].*/\1/' | tr ',' '\n' | tr -d ' '
}

# ---- 1. events other than pull_request always run every shard --------------

leaf="cmd/acr-annex-regen-project-ids/main.go"
test -f "$repo_root/$leaf" || { printf 'fixture leaf %s is gone; pick another leaf package\n' "$leaf" >&2; exit 2; }

run_reached push-leaf push "$leaf"
expect_full push-leaf
run_reached dispatch-leaf workflow_dispatch "$leaf"
expect_full dispatch-leaf
run_reached merge-group-leaf merge_group "$leaf"
expect_full merge-group-leaf

# ---- 2. shared inputs run every shard on a pull request -------------------

i=0
for shared in go.mod go.sum Makefile scripts/ci/test-shard.sh .github/workflows/ci.yml \
  ci/mergetestreports/main.go internal/testsupport/repopath/repopath.go \
  contracts/openapi/acr-v1.json testdata/evaluation/v1/new.json clients/conformance/client-bundle.v1.json \
  no/such/package/dir/file.go; do
  i=$((i + 1))
  run_reached "pr-shared-$i" pull_request "$leaf" "$shared"
  expect_full "pr-shared-$i"
done

# ---- 3. fail open: no verdict the selector can defend is FULL, never empty ---

run_reached pr-empty pull_request
expect_full pr-empty

: >"$tmp/unreadable.err"
(cd "$repo_root" && "$reached" --event pull_request --changed "$tmp/does-not-exist" \
  --reached-out "$tmp/unreadable.reached") >"$tmp/pr-unreadable.out" 2>"$tmp/pr-unreadable.err" || true
expect_full pr-unreadable

printf '%s\n' "$leaf" >"$tmp/golist-fails.changed"
(cd "$repo_root" && GOFLAGS=-modfile=/nonexistent/go.mod "$reached" --event pull_request \
  --changed "$tmp/golist-fails.changed" --reached-out "$tmp/golist-fails.reached") \
  >"$tmp/pr-golist-fails.out" 2>"$tmp/pr-golist-fails.err" || true
expect_full pr-golist-fails

# ---- 4. a one-package pull request runs only the shards it reaches ---------

run_reached pr-leaf pull_request "$leaf"
mode="$(out_value pr-leaf reach_mode)"
if [ "$mode" != reached ]; then
  fail "pr-leaf: want reach_mode=reached for one leaf package, got ${mode:-<none>} ($(out_value pr-leaf reach_reason))"
  sed 's/^/    /' "$tmp/pr-leaf.err" | tail -n 5
else
  leaf_pkg="$module/${leaf%/*}"
  grep -qxF "$leaf_pkg" "$tmp/pr-leaf.reached" || fail "pr-leaf: the changed package $leaf_pkg is not in its own reached set"
  unit_skip="$(out_value pr-leaf unit_skip)"
  race_skip="$(out_value pr-leaf race_skip)"
  skip_jobs="$(out_value pr-leaf skip_jobs)"
  skipped_any=0
  for job in unit race; do
    case "$job" in unit) skips="$unit_skip" ;; race) skips="$race_skip" ;; esac
    for n in $(matrix_legs "$job"); do
      pkgs="$(leg_packages "$job" "$n")"
      hit="$(grep -cxF -f "$tmp/pr-leaf.reached" <<<"$pkgs" || true)"
      if grep -qF "{\"shard\":$n}" <<<"$skips"; then
        skipped_any=1
        [ "$hit" -eq 0 ] || fail "pr-leaf: $job leg $n holds $hit reached package(s) but is skipped"
      else
        [ "$hit" -gt 0 ] || fail "pr-leaf: $job leg $n holds no reached package but runs"
      fi
      if grep -qxF "$leaf_pkg" <<<"$pkgs" && grep -qF "{\"shard\":$n}" <<<"$skips"; then
        fail "pr-leaf: $job leg $n holds the changed package and is skipped"
      fi
    done
  done
  for dedicated in race-devhealthfacts race-devhealthfacts-heavy race-devhealthsource race-factoracle; do
    grep -qF "\"$dedicated\"" <<<"$skip_jobs" || fail "pr-leaf: $dedicated does not reach $leaf_pkg but is not skipped (skip_jobs=$skip_jobs)"
  done
  [ "$skipped_any" -eq 1 ] || fail "pr-leaf: no unit or race leg was skipped, so this case proves nothing"
  # The report names the changed package, why each package is reached, and a
  # reached package behind every run decision.
  grep -qxF "reached-shards: changed package $leaf_pkg ($leaf)" "$tmp/pr-leaf.err" \
    || fail "pr-leaf: the report does not name the changed package and the file that changed it"
  grep -qxF "reached-shards: reached $leaf_pkg (changed)" "$tmp/pr-leaf.err" \
    || fail "pr-leaf: the report does not give the reach basis of the changed package"
  while IFS= read -r held; do
    grep -qxF "$held" "$tmp/pr-leaf.reached" || fail "pr-leaf: a run decision names $held, which is not reached"
  done < <(sed -nE 's/^reached-shards: run  [a-z0-9:-]+ \(holds (.*)\)$/\1/p' "$tmp/pr-leaf.err")
  [ "$(grep -c '^reached-shards: run  ' "$tmp/pr-leaf.err")" -eq "$(grep -c '^reached-shards: run  .* (holds ' "$tmp/pr-leaf.err")" ] \
    || fail "pr-leaf: a run decision does not name the reached package it holds"
  [ "$fails" -eq 0 ] && ok "pr-leaf runs only legs that hold a reached package (unit_skip=$unit_skip race_skip=$race_skip)"
fi

# ---- 5. files read outside the build graph ---------------------------------

# A Markdown file a test opens by name reaches that test's package.
run_reached pr-doc pull_request docs/observability.md
if [ "$(out_value pr-doc reach_mode)" = reached ] && grep -qxF "$module/internal/hostedmetrics" "$tmp/pr-doc.reached"; then
  ok "pr-doc reaches internal/hostedmetrics, whose test reads docs/observability.md"
else
  fail "pr-doc: docs/observability.md must reach internal/hostedmetrics (mode=$(out_value pr-doc reach_mode))"
fi

# Package testdata reaches its package and every package that imports it.
run_reached pr-testdata pull_request internal/contextfabric/testdata/emitted_fields_reachable.txt
if [ "$(out_value pr-testdata reach_mode)" = reached ] \
   && grep -qxF "$module/internal/contextfabric" "$tmp/pr-testdata.reached" \
   && grep -qxF "$module/internal/contextfabric/devhealthfacts" "$tmp/pr-testdata.reached"; then
  ok "pr-testdata reaches internal/contextfabric and its importers"
else
  fail "pr-testdata: internal/contextfabric/testdata must reach internal/contextfabric and devhealthfacts (mode=$(out_value pr-testdata reach_mode))"
fi

# The same rules on files that no test names and no walker owns, so only the
# testdata rule and the //go:embed rule can reach the package.
for case in \
  "pr-testdata-unnamed internal/contextfabric/genkitruntime/testdata/interpretation_system_prompt.golden internal/contextfabric/genkitruntime" \
  "pr-embed migrations/postgres/0053_graph_walk_version_reuse_key.sql internal/storage/postgres" \
  "pr-deleted migrations/postgres/9999_removed_input.sql migrations/postgres"; do
  read -r name path want <<<"$case"
  base="${path##*/}"
  if grep -rqF --include='*_test.go' "$base" "$repo_root"; then
    printf 'fixture %s is named by a test, so it no longer isolates its rule; pick another file\n' "$path" >&2
    exit 2
  fi
  run_reached "$name" pull_request "$path"
  if [ "$(out_value "$name" reach_mode)" = reached ] && grep -qxF "$module/$want" "$tmp/$name.reached"; then
    ok "$name: $path reaches $want"
  else
    fail "$name: $path must reach $want (mode=$(out_value "$name" reach_mode))"
  fi
done

# A package whose tests walk the module tree is reached by any change.
for walker in internal/contextfabric/devhealthschema internal/testsupport/repopath; do
  if grep -qxF "$module/$walker" "$tmp/pr-leaf.reached"; then
    ok "pr-leaf reaches tree walker $walker"
  else
    fail "pr-leaf: $walker walks the module tree in its tests but is not reached by a leaf change"
  fi
done

# ---- 6. verify's lane check, run from ci.yml --------------------------------

lane_script="$tmp/lane-check.sh"
job_block "$workflow" verify | awk '
  $0 == "      - name: Assert every lane succeeded" { s=1; next }
  s && /^      (- |#)/ { s=0 }
  s && /^ {8}run: \|$/ { r=1; next }
  s && r && /^ {8}[a-z-]+:/ { r=0 }
  s && r { sub(/^ {10}/, ""); print }
' >"$lane_script"
test -s "$lane_script" || { printf 'cannot cut the lane check out of %s\n' "$workflow" >&2; exit 2; }
mapfile -t verify_needs < <(job_block "$workflow" verify | awk '
  /^ {4}needs:/ { n=1; next }
  n && /^ {6}- / { sub(/^ {6}- /, ""); print; next }
  n { n=0 }
')

# lane NAME WANT_RC EVENT PLAN_OUTPUTS_JSON [job=result ...]
lane() {
  local name="$1" want="$2" event="$3" outputs="$4" needs rc=0 kv job
  shift 4
  # The plan outputs of a run that defers nothing; a fixture's own keys win.
  needs="$(jq -n --argjson o "$outputs" '{plan: {result: "success", outputs: ({unit_defer: "[]", unit_exclude: ($o.unit_skip // "[]")} + $o)}}')"
  for job in "${verify_needs[@]}"; do
    [ "$job" = plan ] && continue
    needs="$(jq --arg j "$job" '. + {($j): {result: "success", outputs: {}}}' <<<"$needs")"
  done
  # unit-dedup-plan and unit-dedicated are skipped unless the plan deferred legs.
  needs="$(jq --arg d "${dedup_drop:-[]}" '.["unit-dedup-plan"].result = "skipped" | .["unit-dedicated"].result = "skipped" | .["unit-dedup-plan"].outputs.drop = $d' <<<"$needs")"
  for kv in "$@"; do
    needs="$(jq --arg j "${kv%%=*}" --arg r "${kv#*=}" '.[$j].result = $r' <<<"$needs")"
  done
  (cd "$repo_root" && NEEDS="$needs" EVENT="$event" bash "$lane_script") >"$tmp/lane-$name.log" 2>&1 || rc=$?
  if { [ "$want" -eq 0 ] && [ "$rc" -eq 0 ]; } || { [ "$want" -ne 0 ] && [ "$rc" -ne 0 ]; }; then
    ok "lane check $name (rc=$rc)"
  else
    fail "lane check $name: rc=$rc, want $([ "$want" -eq 0 ] && echo pass || echo fail)"
    sed 's/^/    /' "$tmp/lane-$name.log" | tail -n 4
  fi
}

full='{"heavy":"true","reach_mode":"full","skip_jobs":"[]","unit_skip":"[]","race_skip":"[]"}'
reached_unit='{"heavy":"true","reach_mode":"reached","skip_jobs":"[\"unit\"]","unit_skip":"[{\"shard\":1},{\"shard\":2},{\"shard\":3},{\"shard\":4},{\"shard\":5},{\"shard\":6},{\"shard\":7},{\"shard\":8},{\"shard\":9}]","race_skip":"[]"}'
reached_none='{"heavy":"true","reach_mode":"reached","skip_jobs":"[]","unit_skip":"[]","race_skip":"[]"}'
reached_fo='{"heavy":"true","reach_mode":"reached","skip_jobs":"[\"race-factoracle\"]","unit_skip":"[]","race_skip":"[]"}'
full_with_skip='{"heavy":"true","reach_mode":"full","skip_jobs":"[\"unit\"]","unit_skip":"[]","race_skip":"[]"}'
full_with_leg='{"heavy":"true","reach_mode":"full","skip_jobs":"[]","unit_skip":"[{\"shard\":2}]","race_skip":"[]"}'
no_reach='{"heavy":"true"}'
whole_unit_no_legs='{"heavy":"true","reach_mode":"reached","skip_jobs":"[\"unit\"]","unit_skip":"[]","race_skip":"[]"}'
whole_unit_some_legs='{"heavy":"true","reach_mode":"reached","skip_jobs":"[\"unit\"]","unit_skip":"[{\"shard\":5},{\"shard\":6}]","race_skip":"[]"}'
whole_race_partial='{"heavy":"true","reach_mode":"reached","skip_jobs":"[\"race\"]","unit_skip":"[]","race_skip":"[{\"shard\":1}]"}'
whole_race_all='{"heavy":"true","reach_mode":"reached","skip_jobs":"[\"race\"]","unit_skip":"[]","race_skip":"[{\"shard\":4},{\"shard\":1},{\"shard\":3},{\"shard\":2}]"}'
no_reach_lists='{"heavy":"true","skip_jobs":"[]","unit_skip":"[]","race_skip":"[]"}'
bad_reach='{"heavy":"true","reach_mode":"partial","skip_jobs":"[]","unit_skip":"[]","race_skip":"[]"}'
draft_full='{"heavy":"false","reach_mode":"full","skip_jobs":"[]","unit_skip":"[]","race_skip":"[]"}'
heavy_lanes=(race race-devhealthschema race-devhealthfacts race-devhealthfacts-heavy race-devhealthfacts-mid
  race-devhealthfacts-fourth race-devhealthsource race-devhealthsource-heavy race-devhealthsource-mid
  race-factoracle race-factoracle-heavy build contracts container-contract-smoke container-reproducible container-oci-scan)

lane push-all-green 0 push "$full"
lane push-unit-skipped 1 push "$full" unit=skipped
lane push-reached-mode 1 push "$reached_unit" unit=skipped
lane push-reached-mode-all-green 1 push "$reached_none"
lane dispatch-reached-mode 1 workflow_dispatch "$reached_unit" unit=skipped
lane pr-declared-skip 0 pull_request "$reached_unit" unit=skipped
lane pr-whole-unit-no-legs 1 pull_request "$whole_unit_no_legs" unit=skipped
lane pr-whole-unit-some-legs 1 pull_request "$whole_unit_some_legs" unit=skipped
lane pr-whole-race-partial 1 pull_request "$whole_race_partial" race=skipped
lane pr-whole-race-all 0 pull_request "$whole_race_all" race=skipped
lane pr-undeclared-skip 1 pull_request "$reached_none" unit=skipped
lane pr-declared-skip-failed 1 pull_request "$reached_unit" unit=failure
lane pr-declared-skip-but-ran 1 pull_request "$reached_fo"
lane pr-declared-skip-cancelled 1 pull_request "$reached_fo" race-factoracle=cancelled
lane pr-full-with-skip-list 1 pull_request "$full_with_skip" unit=skipped
lane pr-full-with-leg-list 1 pull_request "$full_with_leg"
lane pr-missing-reach-mode 1 pull_request "$no_reach"
lane pr-missing-reach-mode-only 1 pull_request "$no_reach_lists"
lane pr-unknown-reach-mode 1 pull_request "$bad_reach"
draft_skips=()
for job in "${heavy_lanes[@]}"; do draft_skips+=("$job=skipped"); done
lane pr-draft-heavy-skipped 0 pull_request "$draft_full" "${draft_skips[@]}"

# ---- 7. observed race dedup: the two jobs the deferral adds ------------------

four='[{"shard":6},{"shard":7},{"shard":8},{"shard":9}]'
defer_out() { # UNIT_SKIP_JSON DEFER_JSON EXCLUDE_JSON [REACH]
  jq -nc --arg us "$1" --arg d "$2" --arg e "$3" --arg r "${4:-full}" \
    '{heavy: "true", reach_mode: $r, skip_jobs: "[]", unit_skip: $us, race_skip: "[]", unit_defer: $d, unit_exclude: $e}'
}
defer_ok="$(defer_out '[]' "$four" "$four")"
dedup_drop='[]'
lane pr-defer-nothing-dropped 0 pull_request "$defer_ok" unit-dedup-plan=success unit-dedicated=success
dedup_drop='[{"shard":7},{"shard":9}]'
lane pr-defer-some-dropped 0 pull_request "$defer_ok" unit-dedup-plan=success unit-dedicated=success
dedup_drop="$four"
lane pr-defer-all-dropped 0 pull_request "$defer_ok" unit-dedup-plan=success
lane pr-defer-all-dropped-but-dedicated-ran 1 pull_request "$defer_ok" unit-dedup-plan=success unit-dedicated=success
dedup_drop='[]'
lane pr-defer-kept-but-dedicated-skipped 1 pull_request "$defer_ok" unit-dedup-plan=success
lane pr-defer-plan-job-failed 1 pull_request "$defer_ok" unit-dedup-plan=failure unit-dedicated=skipped
lane pr-defer-plan-job-skipped 1 pull_request "$defer_ok" unit-dedicated=success
lane pr-defer-dedicated-failed 1 pull_request "$defer_ok" unit-dedup-plan=success unit-dedicated=failure
lane push-with-defer 1 push "$defer_ok" unit-dedup-plan=success unit-dedicated=success
lane pr-draft-with-defer 1 pull_request "$(jq -c '.heavy = "false"' <<<"$defer_ok")" unit-dedup-plan=success unit-dedicated=success
dedup_drop='[{"shard":6}]'
lane pr-drop-without-defer 1 pull_request "$full" unit-dedup-plan=success
lane pr-nothing-deferred-jobs-ran 1 pull_request "$full" unit-dedup-plan=success unit-dedicated=success
dedup_drop='[]'
lane pr-exclude-short 1 pull_request "$(defer_out '[]' "$four" '[{"shard":6},{"shard":7},{"shard":9}]')" unit-dedup-plan=success unit-dedicated=success
lane pr-defer-not-a-list 1 pull_request "$(defer_out '[]' 'oops' "$four")" unit-dedup-plan=success unit-dedicated=success
lane pr-reach-skips-and-drops 0 pull_request "$(defer_out '[{"shard":6},{"shard":7}]' "$four" "$four" reached)" unit-dedup-plan=success unit-dedicated=success
dedup_drop='[{"shard":8},{"shard":9}]'
lane pr-reach-skips-and-drops-all 0 pull_request "$(defer_out '[{"shard":6},{"shard":7}]' "$four" "$four" reached)" unit-dedup-plan=success
dedup_drop='[]'
all_nine='[{"shard":1},{"shard":2},{"shard":3},{"shard":4},{"shard":5},{"shard":6},{"shard":7},{"shard":8},{"shard":9}]'
whole_unit_defer="$(jq -nc --argjson all "$all_nine" --arg four "$four" '{heavy: "true", reach_mode: "reached", skip_jobs: "[\"unit\"]", unit_skip: ($all[:5] | tostring), race_skip: "[]", unit_defer: $four, unit_exclude: ($all | tostring)}')"
lane pr-whole-unit-skip-by-reach-and-defer 0 pull_request "$whole_unit_defer" unit=skipped unit-dedup-plan=success unit-dedicated=success
lane pr-whole-unit-skip-ran-anyway 1 pull_request "$whole_unit_defer" unit-dedup-plan=success unit-dedicated=success

if [ "$fails" -ne 0 ]; then
  printf '%s failure(s)\n' "$fails"
  exit 1
fi
printf 'PASS: reached-shard selection and the lane check behave as documented\n'
