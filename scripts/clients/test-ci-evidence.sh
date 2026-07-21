#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
out=".tmp/ci-evidence-test-$$"
cleanup() { rm -rf "$out" .tmp/ci-evidence-copy-*-$$; }
trap cleanup EXIT
cd "$root"

bash scripts/clients/ci-evidence-gate.sh --mode generate --out "$out"
bash scripts/clients/verify-ci-evidence.sh --out "$out"
expect_reject() { if "$@" >/dev/null 2>&1; then printf '%s\n' 'expected rejection did not occur' >&2; exit 1; fi; }
expect_reject bash scripts/clients/ci-evidence-gate.sh --mode generate --mode generate --out "$out"
expect_reject bash scripts/clients/ci-evidence-gate.sh --mode generate --out "$out" --unexpected

for mutation in stale tamper missing absolute secret self_asserted; do
  copy=".tmp/ci-evidence-copy-$mutation-$$"; mkdir -p "$copy"; cp "$out"/*.json "$copy/"
  python3 - "$copy/task-14-receipt.v1.json" "$mutation" <<'PY'
import json,sys
p,kind=sys.argv[1:]
raw=open(p).read()
if kind == "absolute": raw=raw.replace("CHAOS-3007", "/Users/example CHAOS-3007", 1)
elif kind == "secret": raw=raw.replace("CHAOS-3007", "Bearer secret CHAOS-3007", 1)
elif kind == "self_asserted": raw=raw.replace("CHAOS-3007", "self_asserted CHAOS-3007", 1)
elif kind == "missing":
    value=json.loads(raw); del value["commands"][0]["output_sha256"]; raw=json.dumps(value)
else:
    value=json.loads(raw); value["source_revision"]="0"*40 if kind == "stale" else value["source_revision"]; value["commands"][0]["result"]="tampered" if kind == "tamper" else value["commands"][0]["result"]; raw=json.dumps(value)
open(p,"w").write(raw)
PY
  chmod 600 "$copy/task-14-receipt.v1.json"
  expect_reject bash scripts/clients/verify-ci-evidence.sh --out "$copy"
  rm -rf "$copy"
done
printf '%s\n' 'CI_EVIDENCE_SELFTEST_OK negatives=stale,tamper,missing,absolute,secret,self_asserted,mode_bypass'
