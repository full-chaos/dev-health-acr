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
present=0
declared=0
for f in test_findings_r1.py test_findings_r2.py test_findings_r3.py test_findings_r4.py \
         test_findings_r6.py test_findings_r10.py test_findings_5430.py test_findings_5452.py \
         test_findings_5380.py test_findings_5562.py test_instrument.py test_shard_plan.py; do
  declared=$((declared+1))
  if [ ! -f "$HERE/$f" ]; then
    # Review round 2: this was `|| continue`, so a DELETED pin file was a silent SKIP.
    # Removing test_findings_5380.py -- the whole safety net for the attempt classifier --
    # left the runner printing ten PASS lines, exiting 0, and never naming the missing
    # file. A declared list is a contract, not a wishlist.
    echo "MISSING  $f -- declared in this runner and not present"; rc=1; continue
  fi
  present=$((present+1))
  out="$(cd "$HERE" && python3 "$f" 2>&1)"; s=$?
  if [ $s -eq 0 ]; then echo "PASS  $f"; else echo "FAIL  $f"; echo "$out" | sed 's/^/      /'; rc=1; fi
done
# A runner that matched NOTHING exits 0 under the old shape, having measured nothing at
# all. Count what actually ran and refuse an empty or short set out loud.
if [ "$present" -eq 0 ]; then
  echo "NO PIN FILES RAN -- the runner measured nothing; this is a failure, not a pass"
  rc=1
fi
echo "pin files: ran=$present of declared=$declared"
exit $rc
