#!/usr/bin/env bash
# fan the 36-row corpus out across N shards against the live rig,
# then merge. The merge is the single verdict; a shard's own artefact is not.
#
# Shape adopted from acr/scripts/trial/run-two-turn-parallel.sh (the two-turn parallel precedent): N shards, per-shard isolated run state, bounded concurrency, a merge
# step as the single verdict. It is the SHAPE, not the script: that script drives
# a Go test over an oracle annex; this corpus is HTTP-driven Python through
# ask-dev :3040 -> acr-api :18090.
#
# METHODOLOGY GUARD (that script's own, lines 44-52, ratified): parallel is a NEW
# execution shape, and the first parallel run against a real corpus must be
# validated against a SEQUENTIAL CONTROL on the same corpus and SHA before its
# numbers are trusted for measurement. Run ./run_corpus_sequential.sh first.
#
# CONCURRENCY IS NOT FREE HERE, and the reason is specific rather than general:
# every shard's synthesis stage is one gpt-5.6-luna call against ONE API
# credential, under acr's own 60s per-request budget. The 2026-09-05 baseline
# already recorded two rows dying at exactly duration_ms=60001 while running
# SEQUENTIALLY. Raising concurrency raises synthesis queueing, which converts
# served rows into deadline_exceeded rows and reads as a regression that is
# really an instrument artefact. Find the ceiling in a ramp smoke whose failures
# are free (acr's run-shard-ramp-smoke.sh is the precedent), not in the
# measurement run. Default is deliberately low.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SHARDS="${1:-6}"
REP="${2:-1}"
MAX_CONCURRENT="${CORPUS_MAX_CONCURRENT_SHARDS:-4}"

# Refuse to run against a rig that is not up. A corpus row that fails because a
# leg is down is not a measurement, and a half-up rig is the easiest way to
# manufacture a fake regression.
# r1 #12: the run dirs are created here. Neither launcher used to create logs/, and the
# repository ships no such directory, so a fresh checkout died on the first tee/redirect
# before producing any artefact at all.
mkdir -p "$HERE/logs"

# r1 #13: probe the base the harness will ACTUALLY use. These probes used to hard-code
# :3040/:18090/:18095, so pointing CORPUS_BASE at a private leg aborted against ports
# that were not under test -- or, worse, passed because the SHARED rig was healthy while
# the configured endpoint was not. Extra probes stay available via CORPUS_EXTRA_PROBES.
CORPUS_BASE="${CORPUS_BASE:-http://127.0.0.1:3040/api/investigations}"
export CORPUS_BASE
base_root="$("$HERE/corpus_origin.sh" "$CORPUS_BASE")"
probes=("$base_root/")
for extra in ${CORPUS_EXTRA_PROBES:-}; do probes+=("$extra"); done
for probe in "${probes[@]}"; do
  code="$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$probe" || true)"
  [[ "$code" == "200" ]] || { echo "ABORT: $probe returned $code, expected 200" >&2; exit 1; }
done

echo "layout:"
# NB: the f-string below must not need escaped quotes inside a single-quoted
# python -c — the previous form did, bash passed the backslashes through, and
# python died with SyntaxError. `bash -n` cannot see this (it is runtime python
# inside a shell string), and `set -e` then aborted the whole run before a single
# shard started. Keep the inner quoting single-level.
python3 "$HERE/shard_plan.py" "$SHARDS" | python3 -c 'import json,sys
p = json.load(sys.stdin)
for s in p["shards"]:
    print("  shard {}: {} rows".format(s["shard"], s["n"]))'

pids=()
rcs=0
for i in $(seq 0 $((SHARDS - 1))); do
  # Bound how many shards are IN FLIGHT, independently of how many there are.
  while (( $(jobs -rp | wc -l) >= MAX_CONCURRENT )); do sleep 2; done
  log="$HERE/logs/shard-$(printf '%02d' "$i").log"
  echo "launching shard $i -> $log"
  python3 "$HERE/run_shard.py" "$i" "$SHARDS" "$REP" >"$log" 2>&1 &
  pids+=("$!")
done

for p in "${pids[@]}"; do
  if ! wait "$p"; then
    echo "SHARD pid $p exited non-zero" >&2
    rcs=$((rcs + 1))
  fi
done

# A non-zero shard exit still aborts the merge: a genuine per-row driver failure
# is real regardless of shard size and must never be silently merged away.
if (( rcs > 0 )); then
  echo "ABORT: $rcs shard(s) failed; not merging" >&2
  exit 1
fi

python3 "$HERE/merge_corpus.py" --shape parallel --in "$HERE/shards" \
  --out "$HERE/verdict-parallel.json" --baseline "$HERE/baseline-20260905-sweep1.json"
