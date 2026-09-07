#!/usr/bin/env bash
# SEQUENTIAL CONTROL. Same corpus, same rig, same harness, one
# process, one row at a time: the execution shape lane-corpus-sweep-1's
# 2026-09-05 baseline used, so its number is directly comparable and so the
# parallel run has the control that run-two-turn-parallel.sh's methodology guard
# requires before parallel numbers may be trusted for measurement.
#
# Implemented as a 1-shard run of the same code path (run_shard.py with
# shard-count 1) rather than a second driver: two drivers would be two
# instruments, and the whole point of the control is that only the SHAPE differs.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REP="${1:-1}"

for probe in "http://127.0.0.1:18090/readyz" "http://127.0.0.1:18095/readyz" "http://127.0.0.1:3040/"; do
  code="$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$probe" || true)"
  [[ "$code" == "200" ]] || { echo "ABORT: $probe returned $code, expected 200" >&2; exit 1; }
done

CORPUS_SHARD_DIR="$HERE/seq/shard-00" python3 "$HERE/run_shard.py" 0 1 "$REP" \
  2>&1 | tee "$HERE/logs/sequential.log"

python3 "$HERE/merge_corpus.py" --shape sequential --in "$HERE/seq" \
  --out "$HERE/verdict-sequential.json" --baseline "$HERE/baseline-20260905-sweep1.json"
