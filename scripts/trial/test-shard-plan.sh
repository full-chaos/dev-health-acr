#!/usr/bin/env bash
# CHAOS-4100: offline checks for run-two-turn-parallel.sh's shard LAYOUT.
#
# The layout is the one part of that script with real logic in it -- how a
# corpus is cut, how many shards result, and which cases land where -- and
# every other part needs a live stack, a subscription and an hour to
# exercise. ACR_TRIAL_SHARD_PLAN_ONLY makes the layout computable without
# provisioning anything, and this file is what turns that into a gate.
#
# Nothing here starts a container, touches postgres, or calls a model.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
launcher="$script_dir/run-two-turn-parallel.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label" >&2
    echo "  want: $want" >&2
    echo "  got:  $got" >&2
    failures=$((failures + 1))
  else
    echo "ok: $label"
  fi
}

plan() { ACR_TRIAL_SHARD_PLAN_ONLY=1 "$launcher" "$@" 2>/dev/null; }

# A DENSE annex: indices 0..4, one entry per (case, member).
cat >"$tmp/dense.json" <<'JSON'
[{"index":0,"member":"expected_kind"},{"index":0,"member":"window"},
 {"index":1,"member":"expected_kind"},{"index":2,"member":"expected_kind"},
 {"index":3,"member":"expected_kind"},{"index":4,"member":"expected_kind"}]
JSON

# A SPARSE annex: indices 50, 51, 64 -- the shape that makes modulo
# splitting produce empty shards, and the reason granularity exists.
cat >"$tmp/sparse.json" <<'JSON'
[{"index":50,"member":"expected_kind"},{"index":50,"member":"window"},
 {"index":51,"member":"expected_kind"},{"index":64,"member":"window"}]
JSON

# 1. Granularity 1 over a dense annex: one case per shard, no empties.
check "granularity=1 dense -> 5 shards of 1" \
  '{"shard_count":5,"granularity":1,"concurrency_cap":8,"case_count":5,"shards":[{"index":0,"cases":"0"},{"index":1,"cases":"1"},{"index":2,"cases":"2"},{"index":3,"cases":"3"},{"index":4,"cases":"4"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=1 plan "$tmp/dense.json")"

# 2. Granularity 2: contiguous chunks, last chunk short. 5 cases -> 3 shards.
check "granularity=2 dense -> 3 shards (2,2,1)" \
  '{"shard_count":3,"granularity":2,"concurrency_cap":8,"case_count":5,"shards":[{"index":0,"cases":"0,1"},{"index":1,"cases":"2,3"},{"index":2,"cases":"4"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=2 plan "$tmp/dense.json")"

# 3. THE POINT OF GRANULARITY: over a SPARSE annex it produces no empty
#    shard, where the modulo path below does.
check "granularity=1 sparse -> 3 shards, none empty" \
  '{"shard_count":3,"granularity":1,"concurrency_cap":8,"case_count":3,"shards":[{"index":0,"cases":"50"},{"index":1,"cases":"51"},{"index":2,"cases":"64"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=1 plan "$tmp/sparse.json")"

# 4. BACK-COMPAT: with granularity unset, the layout is the modulo rule the
#    harness has always applied (index % shard_count), including its empty
#    shard on a sparse annex. An existing invocation must select
#    byte-identical cases, so this is pinned rather than left to inspection.
check "default sparse -> modulo split, shard 1 empty" \
  '{"shard_count":4,"granularity":0,"concurrency_cap":8,"case_count":3,"shards":[{"index":0,"cases":"64"},{"index":1,"cases":""},{"index":2,"cases":"50"},{"index":3,"cases":"51"}]}' \
  "$(plan "$tmp/sparse.json")"

# 5. The concurrency cap is reported in the plan, so an operator can see
#    what a run WILL do before it does it.
check "concurrency cap is configurable and reported" \
  "32" \
  "$(ACR_TRIAL_MAX_CONCURRENT_SHARDS=32 plan "$tmp/dense.json" | sed 's/.*"concurrency_cap":\([0-9]*\).*/\1/')"

# 6. Malformed knobs fail CLOSED rather than silently falling back to a
#    default -- a run that quietly used 8 when the operator asked for "eight"
#    would produce a wall-clock number attributable to nothing.
for bad in 0 -1 eight; do
  if ACR_TRIAL_MAX_CONCURRENT_SHARDS="$bad" "$launcher" "$tmp/dense.json" >/dev/null 2>&1; then
    echo "FAIL: concurrency cap $bad was accepted" >&2
    failures=$((failures + 1))
  else
    echo "ok: concurrency cap $bad refused"
  fi
done
for bad in 0 -1 two; do
  if ACR_TRIAL_CASES_PER_SHARD="$bad" ACR_TRIAL_SHARD_PLAN_ONLY=1 "$launcher" "$tmp/dense.json" >/dev/null 2>&1; then
    echo "FAIL: granularity $bad was accepted" >&2
    failures=$((failures + 1))
  else
    echo "ok: granularity $bad refused"
  fi
done

# 7. An annex carrying no cases is refused rather than producing a run that
#    measures nothing and passes.
echo '[]' >"$tmp/empty.json"
if ACR_TRIAL_SHARD_PLAN_ONLY=1 "$launcher" "$tmp/empty.json" >/dev/null 2>&1; then
  echo "FAIL: an empty annex was accepted" >&2
  failures=$((failures + 1))
else
  echo "ok: empty annex refused"
fi

# 8. The concurrency cap must count TEST processes only (codex xhigh review
#    round 1, P1). launch_shard starts a responder AND a test; a bare
#    `wait -n` returns when EITHER exits, so a responder finishing early
#    would free a slot while its test still runs and the cap would be
#    exceeded silently -- which is the whole failure mode the cap exists to
#    prevent.
#
#    Pinned at the source level because the alternative is a live run: the
#    bug is invisible to any offline check of the layout, and reproducing it
#    needs real shards, real responders and a subscription. This is not a
#    proof that the scheduler is correct; it is a proof that the specific
#    shape that was wrong cannot come back unnoticed.
if grep -qE '^\s*wait -n\s*(\|\||$)' "$launcher"; then
  echo "FAIL: run-two-turn-parallel.sh contains a bare 'wait -n' -- it returns on ANY child, including responders, so the concurrency cap would count processes that are not shards" >&2
  failures=$((failures + 1))
else
  echo "ok: no bare 'wait -n' (cap counts test processes only)"
fi
if ! grep -q 'INFLIGHT+=("${TEST_PIDS\[' "$launcher"; then
  echo "FAIL: the in-flight set is no longer fed from TEST_PIDS -- the cap must count shard tests, not arbitrary children" >&2
  failures=$((failures + 1))
else
  echo "ok: in-flight set is fed from TEST_PIDS"
fi

# 9. Plan-only must not require the private ops/.env or a sibling
#    dev-health checkout (codex xhigh review round 1, P1). This gate runs in
#    `make verify`, so a plan-only path that sources live credentials would
#    fail every clean checkout and every CI runner while passing on the one
#    machine that happens to have them.
#    Tested by RUNNING it somewhere those files do not exist, not by
#    grepping for the guard: a grep passes for a guard that has been renamed
#    into uselessness, which is precisely what a first draft of this check
#    did. The copy below has no sibling dev-health checkout above it, so
#    common.sh's own resolve_dev_health_root/ops/.env checks would both fire.
isolated="$tmp/isolated/repo/scripts/trial"
mkdir -p "$isolated"
cp "$launcher" "$script_dir/common.sh" "$isolated/"
if ! out="$(cd "$tmp/isolated/repo" && ACR_TRIAL_SHARD_PLAN_ONLY=1 ACR_TRIAL_CASES_PER_SHARD=1 \
  bash scripts/trial/run-two-turn-parallel.sh "$tmp/dense.json" 2>&1)"; then
  echo "FAIL: plan-only failed in a checkout with no sibling dev-health/ops/.env -- this gate runs in 'make verify', so that is every CI runner" >&2
  echo "  output: $out" >&2
  failures=$((failures + 1))
elif [[ "$out" != *'"shard_count":5'* ]]; then
  echo "FAIL: plan-only produced no layout without credentials: $out" >&2
  failures=$((failures + 1))
else
  echo "ok: plan-only runs with no dev-health checkout and no ops/.env"
fi

# 10. The ramp smoke's own counters (codex xhigh review round 2, P2). Its
#     first version died on the HEALTHY path -- grep exits 1 when nothing
#     matches, pipefail propagated that into the assignment, and `set -e`
#     killed the script before it could record a successful step -- and
#     blocked on stdin when no logs existed. Its self-test runs the healthy
#     path explicitly, which is the case no other check exercised.
if bash "$script_dir/run-shard-ramp-smoke.sh" --self-test >/dev/null 2>&1; then
  echo "ok: ramp-smoke counters self-test"
else
  echo "FAIL: run-shard-ramp-smoke.sh --self-test failed -- the throughput record this ticket depends on cannot be produced" >&2
  failures=$((failures + 1))
fi

if [[ "$failures" -gt 0 ]]; then
  echo "shard-plan checks FAILED ($failures)" >&2
  exit 1
fi
echo "shard-plan checks passed"
