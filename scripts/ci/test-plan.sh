#!/usr/bin/env bash
set -euo pipefail
export PYTHONDONTWRITEBYTECODE=1

# test-plan.sh exercises scripts/ci/plan.sh, scripts/ci/affected_packages.py,
# the AFFECTED_PACKAGES filter of scripts/ci/test-shard.sh and the verify
# step of ci.yml offline, each against concrete inputs. Every case states the
# input and the output it must produce.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
module="$(awk '/^module / { print $2; exit }' go.mod)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

check() { # description, expected, actual
  if [ "$2" = "$3" ]; then
    printf 'ok   %s\n' "$1"
  else
    printf 'FAIL %s\n  want: %s\n  got:  %s\n' "$1" "$2" "$3" >&2
    fail=1
  fi
}

run_plan() { # EVENT DRAFT LABELS CHANGES_FILE -> prints GITHUB_OUTPUT
  : >"$tmp/out"
  EVENT="$1" DRAFT="$2" LABELS="$3" CHANGES_FILE="${4:-}" GITHUB_OUTPUT="$tmp/out" \
    scripts/ci/plan.sh >"$tmp/plan.log"
  cat "$tmp/out"
}
get() { grep "^$1=" <<<"$2" | head -n1 | cut -d= -f2-; }

# ---- affected_packages.py -------------------------------------------------
cat >"$tmp/deps.txt" <<EOF
$module/internal/pa|$module/internal/pb $module/internal/pc fmt
$module/internal/pb|$module/internal/pc fmt
$module/internal/pc|fmt
$module/internal/pd|fmt
$module/internal/pa.test|$module/internal/pa $module/internal/pb $module/internal/pc fmt
$module/internal/pc.test|$module/internal/pc fmt
EOF
printf 'M\tinternal/pc/x.go\n' >"$tmp/leaf.txt"
out="$(python3 -I scripts/ci/affected_packages.py "$tmp/leaf.txt" --deps-file "$tmp/deps.txt" | tr '\n' ' ')"
case "$out" in
  "subset "*"/internal/pa "*"/internal/pb "*"/internal/pc "*) check "leaf change selects dependents two hops up" ok ok ;;
  *) check "leaf change selects dependents two hops up" "subset pa pb pc" "$out" ;;
esac
case "$out" in
  *"/internal/pd"*) check "unrelated package is not selected" "absent" "present" ;;
  *) check "unrelated package is not selected" ok ok ;;
esac
for case_ in 'M	docs/x.md' 'M	go.mod' 'M	.github/workflows/ci.yml' 'D	internal/pc/x.go' 'M	internal/pc/testdata/a.go' 'M	internal/pc/data.json' 'M	internal/zzz/new.go' 'M	scripts/ci/plan.sh'; do
  printf '%s\n' "$case_" >"$tmp/c.txt"
  first="$(python3 -I scripts/ci/affected_packages.py "$tmp/c.txt" --deps-file "$tmp/deps.txt" | head -n1)"
  check "full for [$case_]" "full" "${first%% *}"
done
printf 'M\tinternal/pc/x.go\nM\tREADME.md\n' >"$tmp/mixed.txt"
first="$(python3 -I scripts/ci/affected_packages.py "$tmp/mixed.txt" --deps-file "$tmp/deps.txt" | head -n1)"
check "one non-Go path among Go paths is full" "full" "${first%% *}"
first="$(python3 -I scripts/ci/affected_packages.py /dev/null --deps-file "$tmp/deps.txt" | head -n1)"
check "empty change list is full" "full" "${first%% *}"

# ---- source-reading guard detector ------------------------------------------
detect() { # prints yes/no for a Go source snippet on stdin
  python3 -I -B -c '
import importlib.util, sys
spec = importlib.util.spec_from_file_location("ap", "scripts/ci/affected_packages.py")
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
print("yes" if m.reads_source_tree(sys.stdin.read()) else "no")'
}
check "detector: ReadFile above the package dir" yes "$(detect <<<'b, _ := os.ReadFile("../../docs/x.md")')"
check "detector: ReadFile via repopath" yes "$(detect <<<'b, _ := os.ReadFile(repopath.Path(t, "a.go"))')"
check "detector: Walk of a non-dot root" yes "$(detect <<<'filepath.WalkDir(root, fn)')"
check "detector: packages.Load" yes "$(detect <<<'packages.Load(cfg, p)')"
check "detector: go list" yes "$(detect <<<'exec.Command("go", "list", "./...")')"
check "detector: ParseFile with runtime.Caller root" yes "$(detect <<<'_, f, _, _ := runtime.Caller(0); parser.ParseFile(fset, f, nil, 0)')"
check "detector: walk of the package dir only" no "$(detect <<<'filepath.WalkDir(".", fn)')"
check "detector: ReadFile of a local file" no "$(detect <<<'os.ReadFile("testdata/a.json")')"
check "detector: pure logic" no "$(detect <<<'func Add(a, b int) int { return a + b }')"
printf 'M\tcmd/gendimtable/main.go\n' >"$tmp/leafgo_early.txt"
cmd_out="$(python3 -I scripts/ci/affected_packages.py "$tmp/leafgo_early.txt")"
for must in internal/contextfabric/devhealthschema internal/releasebuild internal/contracts/v1; do
  case "$cmd_out" in
    *"$module/$must"*) check "source-reading guard package $must always runs on a subset" ok ok ;;
    *) check "source-reading guard package $must always runs on a subset" present absent ;;
  esac
done

# ---- plan.sh ---------------------------------------------------------------
printf 'M\tdocs/x.md\n' >"$tmp/docs.txt"
o="$(run_plan push '' '' "$tmp/docs.txt")"
check "push: heavy" true "$(get heavy "$o")"
check "push: scope" full "$(get scope "$o")"
check "push: nothing skipped" '[]' "$(get skip_jobs "$o")"
o="$(run_plan pull_request false '[]' "$tmp/docs.txt")"
check "ready PR with docs change: full, nothing skipped" "full []" "$(get scope "$o") $(get skip_jobs "$o")"
o="$(run_plan pull_request true '[]' "$tmp/docs.txt")"
check "draft PR: heavy=false" false "$(get heavy "$o")"
heavy_count="$(scripts/ci/plan.sh --print-heavy-jobs | wc -l | tr -d ' ')"
check "draft PR skips every heavy job" "$heavy_count" "$(get skip_jobs "$o" | jq 'length')"
o="$(run_plan pull_request true '["CI-FULL"]' "$tmp/docs.txt")"
check "draft PR with CI-FULL label (any case): heavy=true" true "$(get heavy "$o")"

pkg=cmd/gendimtable/
file=cmd/gendimtable/main.go
test -f "$file" || { printf 'FAIL fixture file %s is missing\n' "$file" >&2; exit 1; }
printf 'M\t%s\n' "$file" >"$tmp/leafgo.txt"
o="$(run_plan pull_request false '[]' "$tmp/leafgo.txt")"
check "ready PR, one cmd .go change: scope subset" subset "$(get scope "$o")"
case " $(get affected "$o") " in
  *" $module/${pkg%/} "*) check "changed package is in affected" ok ok ;;
  *) check "changed package is in affected" present absent ;;
esac
check "subset skip_jobs is a JSON array" array "$(get skip_jobs "$o" | jq -r type)"
for flag in race_schema race_facts race_source race_factoracle; do
  v="$(get "$flag" "$o")"
  case "$v" in true | false) check "$flag is boolean" ok ok ;; *) check "$flag is boolean" "true|false" "$v" ;; esac
done

# ---- test-shard.sh AFFECTED_PACKAGES --------------------------------------
all_union=""
filtered_union=""
sample="$module/internal/limits $module/internal/auth"
for i in 1 2 3 4; do
  all_union+="$(scripts/ci/test-shard.sh "$i" 4) "
  filtered_union+="$(AFFECTED_PACKAGES="$sample" scripts/ci/test-shard.sh "$i" 4) "
done
want="$(for p in $sample; do case " $all_union " in *" $p "*) echo "$p" ;; esac; done | sort | tr '\n' ' ')"
got="$(tr ' ' '\n' <<<"$filtered_union" | grep -v '^$' | sort | tr '\n' ' ')"
check "filtered shards' union == affected packages that the unfiltered shards cover" "$want" "$got"
check "unset AFFECTED_PACKAGES leaves a shard unchanged" "$(scripts/ci/test-shard.sh 1 4)" "$(AFFECTED_PACKAGES='' scripts/ci/test-shard.sh 1 4)"
empty="$(AFFECTED_PACKAGES="$module/internal/nonexistent" scripts/ci/test-shard.sh 1 4)"
check "a leg with nothing affected prints nothing" "" "$empty"

# ---- verify step ------------------------------------------------------------
awk '/^      - name: Assert every lane succeeded/ { grab=1 } grab' .github/workflows/ci.yml \
  | awk '/^        run: \|/ { run=1; next } run && /^      - / { exit } run' \
  | sed 's/^          //' >"$tmp/verify.sh"
test -s "$tmp/verify.sh" || { printf 'FAIL could not extract the verify step\n' >&2; exit 1; }

needs_json() { # heavy skip_jobs-json then "job=result" pairs
  local heavy="$1" skip="$2"
  shift 2
  local json skipq
  skipq="$(jq -Rn --arg s "$skip" '$s')"
  json='{"plan":{"result":"success","outputs":{"heavy":"'"$heavy"'","skip_jobs":'"$skipq"'}}'
  local kv
  for kv in "$@"; do json+=',"'"${kv%%=*}"'":{"result":"'"${kv##*=}"'","outputs":{}}'; done
  printf '%s}' "$json"
}
verify() { NEEDS="$1" bash "$tmp/verify.sh" >"$tmp/verify.log" 2>&1 && echo pass || echo fail; }
base=(mirror-preflight=success scripts=success lint=success docs-mermaid=success unit=success)
check "verify: all success, nothing skipped" pass "$(verify "$(needs_json true '[]' "${base[@]}" race=success build=success)")"
check "verify: heavy job failed" fail "$(verify "$(needs_json true '[]' "${base[@]}" race=failure build=success)")"
check "verify: heavy job skipped but not listed" fail "$(verify "$(needs_json true '[]' "${base[@]}" race=skipped build=success)")"
check "verify: listed job skipped" pass "$(verify "$(needs_json true '["race"]' "${base[@]}" race=skipped build=success)")"
check "verify: listed job ran" fail "$(verify "$(needs_json true '["race"]' "${base[@]}" race=success build=success)")"
check "verify: listed job failed" fail "$(verify "$(needs_json true '["race"]' "${base[@]}" race=failure build=success)")"
check "verify: listed job cancelled" fail "$(verify "$(needs_json true '["race"]' "${base[@]}" race=cancelled build=success)")"
check "verify: light job listed in skip_jobs" fail "$(verify "$(needs_json true '["unit"]' "${base[@]/unit=success/unit=skipped}" race=success)")"
check "verify: light job skipped" fail "$(verify "$(needs_json true '[]' "${base[@]/unit=success/unit=skipped}" race=success)")"
check "verify: plan output missing" fail "$(verify '{"plan":{"result":"failure","outputs":{}},"scripts":{"result":"success","outputs":{}}}')"
check "verify: skip_jobs malformed" fail "$(verify "$(needs_json true 'not json' "${base[@]}" race=success)")"
check "verify: heavy value invalid" fail "$(verify "$(needs_json maybe '[]' "${base[@]}" race=success)")"

[ "$fail" -eq 0 ] || exit 1
printf 'PASS: plan, selector, shard filter and verify cases\n'
