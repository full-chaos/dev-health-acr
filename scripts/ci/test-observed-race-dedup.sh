#!/usr/bin/env bash
set -euo pipefail

# test-observed-race-dedup.sh proves observed-race-dedup.sh drops a dedicated
# unit leg only when the race jobs' observed test2json output shows every test
# of the leg passing, and that each guard can fail.
#
# The race output is REAL: a fixture module (scripts/ci/testdata/
# observed-race-dedup/fixture) holds a package named like each dedicated
# package, and `go test -race -json` of it produces the events, in four
# shapes (pass, skip, subskip, fail). The leg lists are the real `go test
# -list` of that fixture. The covering race jobs are read from the real ci.yml.
#
# Without OBSERVED_DEDUP_SCRIPT the suite runs the real script, then runs each
# planted defect (a mutated copy of the script, one clause at a time) and
# requires at least one row to fail on it.
#
# Usage: test-observed-race-dedup.sh

root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
real_script="$root/scripts/ci/observed-race-dedup.sh"
script="${OBSERVED_DEDUP_SCRIPT:-$real_script}"
workflow="$root/.github/workflows/ci.yml"
fixture_src="$root/scripts/ci/testdata/observed-race-dedup/fixture"

pkgs=(devhealthfacts devhealthsource)
fx="$tmp/fx"
mkdir -p "$fx/internal/contextfabric"
cp "$fixture_src/go.mod" "$fx/go.mod"
for p in "${pkgs[@]}"; do
  mkdir -p "$fx/internal/contextfabric/$p"
  sed "s/^package devhealthsource/package $p/" \
    "$fixture_src/internal/contextfabric/devhealthsource/fixture_test.go" >"$fx/internal/contextfabric/$p/fixture_test.go"
done

# gen MODE OUTDIR [RUN-REGEX]: one real race go test -json per package.
gen() {
  local mode="$1" out="$2" run="${3:-.}" p
  mkdir -p "$out"
  for p in "${pkgs[@]}"; do
    (cd "$fx" && FIXTURE_MODE="$mode" go test -count=1 -race -json -run "$run" "./internal/contextfabric/$p") >"$out/$p.json" || true
  done
}

for mode in pass skip subskip fail; do gen "$mode" "$tmp/raw-$mode"; done
gen pass "$tmp/raw-onlybravo" '^TestBravo$'

# racedir NAME RAWDIR: one race-json-<job>/race.json per covering job.
racedir() {
  local name="$1" raw="$2" p job
  mkdir -p "$tmp/$name"
  for p in "${pkgs[@]}"; do
    for job in $("$real_script" covering "$p"); do
      mkdir -p "$tmp/$name/race-json-$job"
      cp "$raw/$p.json" "$tmp/$name/race-json-$job/race.json"
    done
  done
}
# r-split: every covering job ran a different third of the tests (real runs with -run), so only the UNION shows all passing.
mkdir -p "$tmp/r-split"
regexes=('^TestAlpha$' '^TestBravo$' '^TestCharlie$' '^TestAlpha$')
for p in "${pkgs[@]}"; do
  i=0
  for job in $("$real_script" covering "$p"); do
    mkdir -p "$tmp/r-split/race-json-$job"
    (cd "$fx" && FIXTURE_MODE=pass go test -count=1 -race -json -run "${regexes[$((i % 4))]}" "./internal/contextfabric/$p") >"$tmp/r-split/race-json-$job/race.json" || true
    i=$((i + 1))
  done
done
racedir r-pass "$tmp/raw-pass"
racedir r-skip "$tmp/raw-skip"
mkdir -p "$tmp/raw-mixed"
cp "$tmp/raw-pass/devhealthfacts.json" "$tmp/raw-mixed/devhealthfacts.json"
cp "$tmp/raw-skip/devhealthsource.json" "$tmp/raw-mixed/devhealthsource.json"
racedir r-skip-source "$tmp/raw-mixed"
racedir r-subskip "$tmp/raw-subskip"
racedir r-fail "$tmp/raw-fail"
racedir r-absent "$tmp/raw-onlybravo"

# needs NAME [JOB=RESULT ...]: toJSON(needs) shaped lane results, success unless named.
needs() {
  local name="$1" p job pair
  shift
  local -a jobs=()
  for p in "${pkgs[@]}"; do for job in $("$real_script" covering "$p"); do jobs+=("$job"); done; done
  jq -n --argjson jobs "$(printf '%s\n' "${jobs[@]}" | jq -Rsc 'split("\n") | map(select(length > 0))')" \
    '[$jobs[] | {key: ., value: {result: "success"}}] | from_entries' >"$tmp/$name.json"
  for pair in "$@"; do
    job="${pair%%=*}"
    if [ "${pair#*=}" = ABSENT ]; then
      jq --arg j "$job" 'del(.[$j])' "$tmp/$name.json" >"$tmp/$name.next"
    else
      jq --arg j "$job" --arg r "${pair#*=}" '.[$j].result = $r' "$tmp/$name.json" >"$tmp/$name.next"
    fi
    mv "$tmp/$name.next" "$tmp/$name.json"
  done
}
needs n-ok
needs n-skipped race-devhealthsource-heavy=skipped
needs n-failed race-devhealthfacts-mid=failure
needs n-cancelled race-devhealthfacts-fourth=cancelled
needs n-absent race-devhealthsource-mid=ABSENT

# lists NAME: the real `go test -list` of the fixture packages as legs 6 (source) and 7 (facts).
mkdir -p "$tmp/lists"
list_of() { (cd "$fx" && go test -list . "./internal/contextfabric/$1") | grep '^Test' | LC_ALL=C sort -u; }
write_lists() {
  local dir="$1"
  mkdir -p "$dir"
  printf 'devhealthsource\n' >"$dir/leg-6.package"; list_of devhealthsource >"$dir/leg-6.tests"
  printf 'devhealthfacts\n' >"$dir/leg-7.package"; list_of devhealthfacts >"$dir/leg-7.tests"
}
write_lists "$tmp/lists"
write_lists "$tmp/lists-empty"; : >"$tmp/lists-empty/leg-6.tests"
write_lists "$tmp/lists-nofile"; rm -f "$tmp/lists-nofile/leg-6.tests"
write_lists "$tmp/lists-extra"; printf 'TestNotInAnyRaceOutput\n' >>"$tmp/lists-extra/leg-6.tests"

defer_both='[{"shard":6},{"shard":7}]'
both='drop=[{"shard":6},{"shard":7}]'
none='drop=[]'

# a covering job renamed away from `isolated-dedicated devhealthsource`: 2 of 3 left.
sed '/^  race-devhealthsource-mid:/,/^  [a-z0-9-]*:/ s/isolated-dedicated devhealthsource/isolated-dedicated devhealthsourcX/' "$workflow" >"$tmp/ci-fewer-covering.yml"
cmp -s "$workflow" "$tmp/ci-fewer-covering.yml" && { echo 'SETUP FAILED: ci-fewer-covering.yml equals ci.yml' >&2; exit 1; }

fails=0
rows=0
row() { # name want event lists racedir needs [workflow]
  local name="$1" want="$2" event="$3" lists="$4" rd="$5" nd="$6" wf="${7:-$workflow}" got err
  rows=$((rows + 1))
  err="$tmp/err.$rows"
  got="$("$script" decide --event "$event" --lists "$lists" --race-dir "$rd" --needs "$nd" --defer "$defer_both" --workflow "$wf" 2>"$err" | grep '^drop=')" || got="rc=$?"
  if [ "$got" = "$want" ]; then
    [ -n "${QUIET:-}" ] || printf 'ok   %s\n' "$name"
  else
    [ -n "${QUIET:-}" ] || { printf 'FAIL %s: got %s want %s\n' "$name" "$got" "$want"; sed 's/^/    /' "$err"; }
    fails=$((fails + 1))
  fi
}

rowfull() { # name want event lists racedir needs unit_skip
  local name="$1" want="$2" event="$3" lists="$4" rd="$5" nd="$6" skip="$7" got err
  rows=$((rows + 1))
  err="$tmp/err.$rows"
  got="$("$script" decide --event "$event" --lists "$lists" --race-dir "$rd" --needs "$nd" --defer "$([ "$event" = pull_request ] && printf '%s' "$defer_both" || printf '[]')" --unit-skip "$skip" 2>"$err")" || got="rc=$?"
  if [ "$got" = "$want" ]; then
    [ -n "${QUIET:-}" ] || printf 'ok   %s\n' "$name"
  else
    [ -n "${QUIET:-}" ] || { printf 'FAIL %s: got %s want %s\n' "$name" "$got" "$want"; sed 's/^/    /' "$err"; }
    fails=$((fails + 1))
  fi
}

defer_row() { # name want event heavy skip_jobs unit_skip
  local name="$1" want="$2" got
  rows=$((rows + 1))
  got="$("$script" defer --event "$3" --heavy "$4" --skip-jobs "$5" --unit-skip "$6" 2>&1)" || got="rc=$?"
  if [ "$got" = "$want" ]; then
    [ -n "${QUIET:-}" ] || printf 'ok   %s\n' "$name"
  else
    [ -n "${QUIET:-}" ] || printf 'FAIL %s: got %s want %s\n' "$name" "$got" "$want"
    fails=$((fails + 1))
  fi
}

run_rows() {
  row 'E all tests pass in every covering job -> both legs drop' "$both" pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-ok.json"
  row 'E2 tests split across the covering jobs: the union shows every test passing' "$both" pull_request "$tmp/lists" "$tmp/r-split" "$tmp/n-ok.json"
  row 'D push keeps every leg' "$none" push "$tmp/lists" "$tmp/r-pass" "$tmp/n-ok.json"
  row 'D workflow_dispatch keeps every leg' "$none" workflow_dispatch "$tmp/lists" "$tmp/r-pass" "$tmp/n-ok.json"
  row 'A a test skipped under -race keeps the leg' "$none" pull_request "$tmp/lists" "$tmp/r-skip" "$tmp/n-ok.json"
  row 'A2 a subtest skipped under -race keeps the leg' "$none" pull_request "$tmp/lists" "$tmp/r-subskip" "$tmp/n-ok.json"
  row 'H a failing test keeps the leg' "$none" pull_request "$tmp/lists" "$tmp/r-fail" "$tmp/n-ok.json"
  row 'C a test absent from the race output keeps the leg' "$none" pull_request "$tmp/lists" "$tmp/r-absent" "$tmp/n-ok.json"
  row 'C2 a listed test no race job ran keeps that leg only' 'drop=[{"shard":7}]' pull_request "$tmp/lists-extra" "$tmp/r-pass" "$tmp/n-ok.json"
  row 'B1 a covering job skipped keeps the package legs' 'drop=[{"shard":7}]' pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-skipped.json"
  row 'B2 a covering job failed keeps the package legs' 'drop=[{"shard":6}]' pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-failed.json"
  row 'B3 a covering job cancelled keeps the package legs' 'drop=[{"shard":6}]' pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-cancelled.json"
  row 'B4 a covering job missing from the lane results keeps the package legs' 'drop=[{"shard":7}]' pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-absent.json"
  row 'F1 an empty compiled list keeps that leg' 'drop=[{"shard":7}]' pull_request "$tmp/lists-empty" "$tmp/r-pass" "$tmp/n-ok.json"
  row 'F2 no compiled list keeps that leg' 'drop=[{"shard":7}]' pull_request "$tmp/lists-nofile" "$tmp/r-pass" "$tmp/n-ok.json"
  row 'G fewer covering jobs than pinned keeps that package' 'drop=[{"shard":7}]' pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-ok.json" "$tmp/ci-fewer-covering.yml"

  rowfull 'R1 both legs dropped, nothing else to run' $'drop=[{"shard":6},{"shard":7}]\nexclude=[{"shard":6},{"shard":7}]\nrun=false' \
    pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-ok.json" '[]'
  rowfull 'R2 a kept leg means the dedicated job runs and excludes the dropped leg' $'drop=[{"shard":7}]\nexclude=[{"shard":7}]\nrun=true' \
    pull_request "$tmp/lists" "$tmp/r-skip-source" "$tmp/n-ok.json" '[]'
  rowfull 'R3 a leg the reach step skipped stays excluded' $'drop=[{"shard":6}]\nexclude=[{"shard":6},{"shard":7}]\nrun=false' \
    pull_request "$tmp/lists" "$tmp/r-pass" "$tmp/n-failed.json" '[{"shard":7}]'
  rowfull 'R4 push decides nothing: no leg is deferred, none is dropped' $'drop=[]\nexclude=[]\nrun=false' \
    push "$tmp/lists" "$tmp/r-pass" "$tmp/n-ok.json" '[]'
  defer_row 'P1 heavy pull_request defers legs 6-9' $'unit_defer=[{"shard":6},{"shard":7},{"shard":8},{"shard":9}]\nunit_exclude=[{"shard":6},{"shard":7},{"shard":8},{"shard":9}]\nskip_jobs=[]' \
    pull_request true '[]' '[]'
  defer_row 'P2 push defers nothing' $'unit_defer=[]\nunit_exclude=[]\nskip_jobs=[]' push true '[]' '[]'
  defer_row 'P3 workflow_dispatch defers nothing' $'unit_defer=[]\nunit_exclude=[]\nskip_jobs=[]' workflow_dispatch true '[]' '[]'
  defer_row 'P4 a draft pull request (no race jobs) defers nothing' $'unit_defer=[]\nunit_exclude=[]\nskip_jobs=[]' pull_request false '[]' '[]'
  defer_row 'P5 reach skips legs 1-5: the unit matrix is empty and named in skip_jobs' \
    $'unit_defer=[{"shard":6},{"shard":7},{"shard":8},{"shard":9}]\nunit_exclude=[{"shard":1},{"shard":2},{"shard":3},{"shard":4},{"shard":5},{"shard":6},{"shard":7},{"shard":8},{"shard":9}]\nskip_jobs=["unit"]' \
    pull_request true '[]' '[{"shard":1},{"shard":2},{"shard":3},{"shard":4},{"shard":5}]'
  defer_row 'P6 reach skips only leg 2: unit keeps running' \
    $'unit_defer=[{"shard":6},{"shard":7},{"shard":8},{"shard":9}]\nunit_exclude=[{"shard":2},{"shard":6},{"shard":7},{"shard":8},{"shard":9}]\nskip_jobs=[]' \
    pull_request true '[]' '[{"shard":2}]'

  local rd
  rd="$tmp/r-noartifact"; rm -rf "$rd"; cp -R "$tmp/r-pass" "$rd"; rm -rf "$rd/race-json-race-devhealthfacts-heavy"
  row 'B5 a covering job artifact missing keeps the package legs' 'drop=[{"shard":6}]' pull_request "$tmp/lists" "$rd" "$tmp/n-ok.json"
  rd="$tmp/r-emptyartifact"; rm -rf "$rd"; cp -R "$tmp/r-pass" "$rd"; : >"$rd/race-json-race-devhealthsource/race.json"
  row 'B6 a covering job artifact empty keeps the package legs' 'drop=[{"shard":7}]' pull_request "$tmp/lists" "$rd" "$tmp/n-ok.json"
  rd="$tmp/r-garbage"; rm -rf "$rd"; cp -R "$tmp/r-pass" "$rd"; printf 'not json\nbuild failed\n' >"$rd/race-json-race-devhealthsource-mid/race.json"
  row 'B7 a covering job artifact that is not test2json keeps the package legs' 'drop=[{"shard":7}]' pull_request "$tmp/lists" "$rd" "$tmp/n-ok.json"
  rd="$tmp/r-nopkgpass"; rm -rf "$rd"; cp -R "$tmp/r-pass" "$rd"
  grep -v '"Action":"pass","Package":"[^"]*","Elapsed"' "$rd/race-json-race-devhealthsource/race.json" | grep -v '"Action":"pass","Package":"[^"]*devhealthsource","Elapsed"' >"$rd/x" || true
  mv "$rd/x" "$rd/race-json-race-devhealthsource/race.json"
  row 'B8 a covering job with no package-level pass event keeps the package legs' 'drop=[{"shard":7}]' pull_request "$tmp/lists" "$rd" "$tmp/n-ok.json"
  rd="$tmp/r-otherpkg"; rm -rf "$rd"; cp -R "$tmp/r-skip" "$rd"
  cp "$tmp/raw-pass/devhealthsource.json" "$rd/race-json-race-devhealthsource/race.json"
  cp "$tmp/raw-pass/devhealthsource.json" "$rd/race-json-race-devhealthsource-heavy/race.json"
  cp "$tmp/raw-skip/devhealthsource.json" "$rd/race-json-race-devhealthsource-mid/race.json"
  row 'A3 one covering job of three shows a skip: union keeps the leg' 'drop=[]' pull_request "$tmp/lists" "$rd" "$tmp/n-ok.json"
}

echo "positive and negative rows against $script"
run_rows

if [ -z "${OBSERVED_DEDUP_SCRIPT:-}" ]; then
  mkdir -p "$tmp/mutants"
  python3 - "$real_script" "$tmp/mutants" <<'PY'
import sys
src, outdir = sys.argv[1:3]
text = open(src).read()
mutants = [
    ("skip-check", 'elif ($skips | any(. == $t or . == "<package>" or startswith($t + "/"))) then', 'elif false then'),
    ("skip-subtest-clause", 'elif ($skips | any(. == $t or . == "<package>" or startswith($t + "/"))) then', 'elif ($skips | any(. == $t or . == "<package>")) then'),
    ("absent-check", 'if ($m[0].pass[$t] // false | not) then', 'if false then'),
    ("job-result", 'if [ "$res" != success ]; then', 'if false; then'),
    ("artifact-readable", 'if ! summarize_race_json "$file" "$pkg" >"$tmp/sum.json" 2>/dev/null; then', 'if false; then'),
    ("package-pass", 'if [ "$(jq -r \'.pk\' "$tmp/sum.json")" != true ]; then', 'if false; then'),
    ("event", 'if [ "$event" != pull_request ]; then', 'if false; then'),
    ("pin", 'if [ "$want" -eq 0 ] || [ "$got" -ne "$want" ]; then', 'if false; then'),
    ("empty-list", 'if [ "$(grep -c . "$tests")" -eq 0 ]; then', 'if false; then'),
    ("list-present", 'if [ -z "$pkg" ] || [ ! -f "$tests" ]; then', 'if false; then'),
    ("union-of-jobs", 'jq -n --slurpfile a "$merged" --slurpfile b "$tmp/sum.json"', 'jq -n --slurpfile a "$tmp/sum.json" --slurpfile b "$tmp/sum.json"'),
]
for name, old, new in mutants:
    if text.count(old) != 1:
        sys.stderr.write("MUTANT SETUP FAILED: %s: pattern found %d times\n" % (name, text.count(old)))
        sys.exit(3)
    open("%s/%s.sh" % (outdir, name), "w").write(text.replace(old, new))
PY
  for mutated in "$tmp"/mutants/*.sh; do
    name="$(basename "$mutated" .sh)"
    chmod +x "$mutated"
    before=$fails
    script="$mutated" QUIET=1 run_rows
    if [ "$fails" -gt "$before" ]; then
      printf 'CONTROL OK: mutant %s is caught by %s row(s)\n' "$name" "$((fails - before))"
      fails=$before
    else
      printf 'NEGATIVE CONTROL FAILED: mutant %s was not caught by any row\n' "$name" >&2
      fails=$((fails + 1))
    fi
  done
fi

[ "$fails" -eq 0 ] || { printf '%s failure(s)\n' "$fails" >&2; exit 1; }
printf 'test-observed-race-dedup: all rows ok\n'
