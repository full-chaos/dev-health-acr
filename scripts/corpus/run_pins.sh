#!/usr/bin/env bash
# Run every pin file, each in a FRESH interpreter with the synthetic corpus on the path.
#
# Isolation is by process. Installing a corpus into a running interpreter and undoing it
# afterwards could not retract references already captured by imported modules, so a
# synthetic corpus could survive into a later merge. A new process cannot leak into
# another one.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
export PYTHONPATH="$HERE/testdata_corpus:${PYTHONPATH:-}"
rc=0
for f in test_findings_r1.py test_findings_r2.py test_findings_r3.py test_findings_r4.py \
         test_findings_r6.py test_instrument.py test_shard_plan.py; do
  [ -f "$HERE/$f" ] || continue
  out="$(cd "$HERE" && python3 "$f" 2>&1)"; s=$?
  if [ $s -eq 0 ]; then echo "PASS  $f"; else echo "FAIL  $f"; echo "$out" | sed 's/^/      /'; rc=1; fi
done
exit $rc
