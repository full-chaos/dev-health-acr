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

if [[ "$failures" -gt 0 ]]; then
  echo "shard-plan checks FAILED ($failures)" >&2
  exit 1
fi
echo "shard-plan checks passed"
