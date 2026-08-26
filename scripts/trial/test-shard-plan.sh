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

# FIXTURES USE THE REAL SIGNED-ANNEX SHAPE (codex xhigh review round 3,
# P1): an object whose `cases` map is keyed by DECIMAL-STRING case index,
# exactly what loadTwoTurnOracleAnnex/adaptSignedOracleAnnex consume.
#
# The first version of this file invented an array-of-entries shape that
# matched the launcher's (wrong) parse, so both agreed with each other and
# neither agreed with the annex. A fixture written from the code under test
# rather than from the real format tests only that the code is
# self-consistent. These are cut down from the real file's structure.

# A DENSE annex: indices 0..4.
# CHAOS-4302: printf + file redirect, not a heredoc -- see common.sh's own
# CHAOS-4155 comment for why a small heredoc/here-string is a deadlock risk
# on this host's bash.
printf '%s\n' \
  '{"provenance":{"corpus_sha8":"deadbeef","signoff":{"by":"t","status":"APPROVED"}},' \
  ' "cases":{"0":{"band":"b"},"1":{"band":"b"},"2":{"band":"b"},"3":{"band":"b"},"4":{"band":"b"}}}' \
  >"$tmp/dense.json"

# A SPARSE annex: indices 50, 51, 64 -- the shape that makes modulo
# splitting produce empty shards, and the reason granularity exists.
# `_comment` is a non-numeric key: adaptSignedOracleAnnex skips those, so
# the layout must skip them identically or a shard gets a case the harness
# will never run.
printf '%s\n' \
  '{"provenance":{"corpus_sha8":"deadbeef","signoff":{"by":"t","status":"APPROVED"}},' \
  ' "cases":{"50":{"band":"b"},"51":{"band":"b"},"64":{"band":"b"},"_comment":{"band":"ignored"}}}' \
  >"$tmp/sparse.json"

# 1. Granularity 1 over a dense annex: one case per shard, no empties.
check "granularity=1 dense -> 5 shards of 1" \
  '{"shard_count":5,"granularity":1,"concurrency_cap":8,"case_count":5,"pg_host":"127.0.0.1","pg_port":"5432","pg_dsn_example":"postgres://plan-only:plan-only@127.0.0.1:5432/EXAMPLE_DB?sslmode=disable","shards":[{"index":0,"cases":"0"},{"index":1,"cases":"1"},{"index":2,"cases":"2"},{"index":3,"cases":"3"},{"index":4,"cases":"4"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=1 plan "$tmp/dense.json")"

# 2. Granularity 2: contiguous chunks, last chunk short. 5 cases -> 3 shards.
check "granularity=2 dense -> 3 shards (2,2,1)" \
  '{"shard_count":3,"granularity":2,"concurrency_cap":8,"case_count":5,"pg_host":"127.0.0.1","pg_port":"5432","pg_dsn_example":"postgres://plan-only:plan-only@127.0.0.1:5432/EXAMPLE_DB?sslmode=disable","shards":[{"index":0,"cases":"0,1"},{"index":1,"cases":"2,3"},{"index":2,"cases":"4"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=2 plan "$tmp/dense.json")"

# 3. THE POINT OF GRANULARITY: over a SPARSE annex it produces no empty
#    shard, where the modulo path below does.
check "granularity=1 sparse -> 3 shards, none empty" \
  '{"shard_count":3,"granularity":1,"concurrency_cap":8,"case_count":3,"pg_host":"127.0.0.1","pg_port":"5432","pg_dsn_example":"postgres://plan-only:plan-only@127.0.0.1:5432/EXAMPLE_DB?sslmode=disable","shards":[{"index":0,"cases":"50"},{"index":1,"cases":"51"},{"index":2,"cases":"64"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=1 plan "$tmp/sparse.json")"

# 4. BACK-COMPAT: with granularity unset, the layout is the modulo rule the
#    harness has always applied (index % shard_count), including its empty
#    shard on a sparse annex. An existing invocation must select
#    byte-identical cases, so this is pinned rather than left to inspection.
check "default sparse -> modulo split, shard 1 empty" \
  '{"shard_count":4,"granularity":0,"concurrency_cap":8,"case_count":3,"pg_host":"127.0.0.1","pg_port":"5432","pg_dsn_example":"postgres://plan-only:plan-only@127.0.0.1:5432/EXAMPLE_DB?sslmode=disable","shards":[{"index":0,"cases":"64"},{"index":1,"cases":""},{"index":2,"cases":"50"},{"index":3,"cases":"51"}]}' \
  "$(plan "$tmp/sparse.json")"

# 4b. CHAOS-4116: ACR_TRIAL_PG_HOST/ACR_TRIAL_PG_PORT reach pg_dsn_example --
# trial_pg_dsn is the SAME function template_dsn and SHARD_DSN call in the
# live path (never a second copy of the template), so this is exercising
# their real construction, not a restatement of it. Default (unset) stays
# 127.0.0.1:5432, pinned by every check above that never sets the override.
check "ACR_TRIAL_PG_HOST/PORT override reaches trial_pg_dsn" \
  '{"shard_count":5,"granularity":1,"concurrency_cap":8,"case_count":5,"pg_host":"pgrelay.internal","pg_port":"15432","pg_dsn_example":"postgres://plan-only:plan-only@pgrelay.internal:15432/EXAMPLE_DB?sslmode=disable","shards":[{"index":0,"cases":"0"},{"index":1,"cases":"1"},{"index":2,"cases":"2"},{"index":3,"cases":"3"},{"index":4,"cases":"4"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=1 ACR_TRIAL_PG_HOST=pgrelay.internal ACR_TRIAL_PG_PORT=15432 plan "$tmp/dense.json")"

# 4c. codex review round 1 (P2, confirmed): a `"` in an operator-controlled
# ACR_TRIAL_PG_HOST value used to emit invalid JSON (printf %s with no
# escaping). jq -Rn --arg now escapes it -- the plan output must still be
# WELL-FORMED JSON (parseable, and pg_host round-trips to the literal
# string INCLUDING the quote) rather than merely "does not crash".
evil_host_plan="$(env ACR_TRIAL_CASES_PER_SHARD=1 ACR_TRIAL_PG_HOST='evil"host' ACR_TRIAL_SHARD_PLAN_ONLY=1 "$launcher" "$tmp/dense.json" 2>/dev/null)"
check "a quote in ACR_TRIAL_PG_HOST is escaped, not injected" \
  'valid
evil"host' \
  "$(printf '%s\n%s' \
    "$(printf '%s' "$evil_host_plan" | jq -e . >/dev/null 2>&1 && echo valid || echo INVALID)" \
    "$(printf '%s' "$evil_host_plan" | jq -r .pg_host)")"

# 4d. CHAOS-4228: an IPv6 ACR_TRIAL_PG_HOST must be bracketed in
# pg_dsn_example (trial_pg_dsn's own bracket_host_if_ipv6 call) -- the
# `pg_host` field itself stays the raw, unbracketed operator value (it is
# not a DSN authority on its own). Unbracketed, the DSN's trailing
# `:5432` would be indistinguishable from one more `:`-separated group of
# the address, an ambiguous/unparseable postgres:// URI.
check "an IPv6 ACR_TRIAL_PG_HOST is bracketed in pg_dsn_example" \
  '{"shard_count":5,"granularity":1,"concurrency_cap":8,"case_count":5,"pg_host":"2001:db8::1","pg_port":"5432","pg_dsn_example":"postgres://plan-only:plan-only@[2001:db8::1]:5432/EXAMPLE_DB?sslmode=disable","shards":[{"index":0,"cases":"0"},{"index":1,"cases":"1"},{"index":2,"cases":"2"},{"index":3,"cases":"3"},{"index":4,"cases":"4"}]}' \
  "$(ACR_TRIAL_CASES_PER_SHARD=1 ACR_TRIAL_PG_HOST=2001:db8::1 plan "$tmp/dense.json")"

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
echo '{"provenance":{},"cases":{}}' >"$tmp/empty.json"
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

# 9b. THE FIXTURE-AGREEMENT CHECK (codex xhigh review round 3, P1). Every
#     check above uses a fixture this repo wrote, so all of them together
#     still prove nothing about the REAL annex format -- which is exactly
#     how the array-vs-object defect survived a passing test suite.
#
#     When a real signed annex is reachable, the layout must parse it and
#     find cases. Skipped (loudly) when it is not, because the annex lives
#     in the sibling dev-health checkout that CI does not have -- a skip
#     that announces itself is honest; silently passing is what got us here.
# Walks UP from this script looking for a .remember/ directory, because
# the repo is checked out at different depths (a plain clone vs. a git
# worktree under worktrees/acr/<branch>) and a fixed ../../.. only works
# for one of them -- which is why the first version of this check silently
# SKIPPED on the machine that actually has the file.
real_annex="${ACR_TRIAL_REAL_ANNEX:-}"
if [[ -z "$real_annex" ]]; then
  probe="$script_dir"
  for _ in 1 2 3 4 5 6; do
    probe="$(dirname "$probe")"
    [[ "$probe" == "/" ]] && break
    while IFS= read -r candidate; do
      if [[ -n "$candidate" ]] && jq -e '.cases and .provenance' "$candidate" >/dev/null 2>&1; then
        real_annex="$candidate"
        break
      fi
    done < <(find "$probe/.remember" -maxdepth 1 -name '*.json' 2>/dev/null || true)
    [[ -n "$real_annex" ]] && break
  done
fi
if [[ -z "$real_annex" ]]; then
  echo "SKIP: no real signed annex reachable -- fixture-agreement unverified in this environment"
else
  real_count="$(ACR_TRIAL_SHARD_PLAN_ONLY=1 ACR_TRIAL_CASES_PER_SHARD=1 "$launcher" "$real_annex" 2>/dev/null | sed 's/.*"case_count":\([0-9]*\).*/\1/')"
  jq_count="$(jq -r '[(.cases // {}) | keys[] | select(test("^[0-9]+$"))] | length' "$real_annex")"
  if [[ "$real_count" != "$jq_count" || "$real_count" == "0" ]]; then
    echo "FAIL: layout found $real_count case(s) in the REAL annex, the annex carries $jq_count -- the launcher's parse disagrees with the shipped format" >&2
    failures=$((failures + 1))
  else
    echo "ok: layout parses the real signed annex ($real_count cases)"
  fi
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
