#!/usr/bin/env bash
# CHAOS-12788: cells for scripts/ci/check-pr-title.sh (the house PR-title rule, root AGENTS.md).
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
script="$repo_root/scripts/ci/check-pr-title.sh"
fails=0

# run <name> <want: pass|fail> <author> <title>
run() {
  local name=$1 want=$2 author=$3 title=$4 rc=0
  EVENT=pull_request AUTHOR="$author" TITLE="$title" bash "$script" >/dev/null 2>&1 || rc=$?
  if { [ "$want" = pass ] && [ "$rc" = 0 ]; } || { [ "$want" = fail ] && [ "$rc" != 0 ]; }; then
    echo "ok   $name"
  else
    echo "FAIL $name (want $want, rc=$rc)"; fails=$((fails + 1))
  fi
}

# 1. titles from the evidence list (no type, no id) fail
run bad-evidence-no-id-no-type fail chaos 'authruntime: pre-fault control login of the JWKS stall test needs no p'
run bad-evidence-docs-no-id fail chaos 'docs: one-off operator Pod for dho workers matches the prod form'
run bad-evidence-typed-no-id fail chaos 'chore(acr): roll go-api to the ops c2097f94 build'
# 2. a valid title passes
run valid pass chaos 'chore(ci): CHAOS-12788 add a required PR-title check'
run valid-every-type pass chaos 'enhancement(acr): CHAOS-1 roll web'
# 3. dependabot is exempt by author, the same title from a human fails
run dependabot-exempt pass 'dependabot[bot]' 'Bump actions/checkout from 4 to 5'
run human-same-title fail chaos 'Bump actions/checkout from 4 to 5'
# 4. two ids fail (different and repeated)
run two-ids fail chaos 'fix(ci): CHAOS-1 CHAOS-2 roll web'
run same-id-twice fail chaos 'fix(ci): CHAOS-1 roll web for CHAOS-1'
# extras
run bad-type fail chaos 'feature(ci): CHAOS-1 roll web'
run no-description fail chaos 'fix(ci): CHAOS-1'
run no-area fail chaos 'fix: CHAOS-1 roll web'

[ "$fails" = 0 ] || { echo "$fails cell(s) failed" >&2; exit 1; }
