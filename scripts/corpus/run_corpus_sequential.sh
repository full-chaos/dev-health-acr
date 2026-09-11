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

# CHAOS-5562: NO DEFAULT. This launcher used to default an unset CORPUS_BASE to the
# shared rig leg -- the same class of silent default the harness itself no longer has.
# Refuse before doing anything else -- including creating logs/ below (r2 review found
# the old order left logs/ on disk even on a refusal, contradicting "before doing
# anything else") -- naming the variable, same as harness.require_base().
: "${CORPUS_BASE:?CORPUS_BASE is not set -- refusing to start. There is no default rig leg; set CORPUS_BASE to the investigations endpoint you own (see scripts/corpus/README.md).}"
export CORPUS_BASE

# r1 #12: the run dirs are created here. Neither launcher used to create logs/, and the
# repository ships no such directory, so a fresh checkout died on the first tee/redirect
# before producing any artefact at all.
mkdir -p "$HERE/logs"

# r1 #13: probe the base the harness will ACTUALLY use. These probes used to hard-code
# :3040/:18090/:18095, so pointing CORPUS_BASE at a private leg aborted against ports
# that were not under test -- or, worse, passed because the SHARED rig was healthy while
# the configured endpoint was not. Extra probes stay available via CORPUS_EXTRA_PROBES.
base_root="$("$HERE/corpus_origin.sh" "$CORPUS_BASE")"
probes=("$base_root/")
for extra in ${CORPUS_EXTRA_PROBES:-}; do probes+=("$extra"); done
for probe in "${probes[@]}"; do
  code="$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$probe" || true)"
  # CHAOS-5562 r2: redact before printing -- $probe carries whatever credentials/token
  # CORPUS_BASE embeds (corpus_origin.sh's own origin preserves userinfo, on purpose,
  # because the real request above needs it); the ABORT line is logging, not a request.
  [[ "$code" == "200" ]] || { echo "ABORT: $("$HERE/corpus_redact.sh" "$probe") returned $code, expected 200" >&2; exit 1; }
done

# CHAOS-5562 r3: ONE check-only request -- verifies base+build BEFORE the real run
# starts. Belt and braces with run_shard.py's own per-process check (which already
# bounds this shape at 1 process); the sequential launcher only ever runs one shard,
# so this mirrors the parallel launcher's fix rather than closing a gap of its own.
python3 "$HERE/harness.py" --check-only

CORPUS_SHARD_DIR="$HERE/seq/shard-00" python3 "$HERE/run_shard.py" 0 1 "$REP" \
  2>&1 | tee "$HERE/logs/sequential.log"

python3 "$HERE/merge_corpus.py" --shape sequential --in "$HERE/seq" \
  --out "$HERE/verdict-sequential.json" --baseline "$HERE/baseline-20260905-sweep1.json"
