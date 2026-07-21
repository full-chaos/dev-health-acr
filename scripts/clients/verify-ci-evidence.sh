#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
out=""
[[ $# -eq 2 && "$1" == --out && "$2" != /* && "$2" != *".."* ]] || { printf '%s\n' 'usage: verify-ci-evidence.sh --out <relative-output-directory>' >&2; exit 2; }
out="$2"
cd "$root"
python3 - "$out" "$(git rev-parse HEAD)" <<'PY'
import hashlib,json,os,re,stat,sys
out,head=sys.argv[1:]
sha=lambda value: hashlib.sha256(value.encode()).hexdigest()
for task in range(14,18):
    path=os.path.join(out,f"task-{task}-receipt.v1.json")
    if not os.path.isfile(path) or stat.S_IMODE(os.stat(path).st_mode) != 0o600: raise SystemExit(f"invalid receipt mode: task {task}")
    raw=open(path,encoding="utf-8").read()
    if "/Users/" in raw or re.search(r"(?:^|[\s\"'])(?:bearer|token|secret|password)[\s:=]",raw,re.I): raise SystemExit(f"unsafe receipt: task {task}")
    value=json.loads(raw)
    required={"schema_version","task","source_revision","state","commands","deferral","receipt_sha256"}
    if set(value) != required or value["schema_version"] != "acr.ci_evidence_receipt.v1" or value["task"] != f"CHAOS-3007 Task {task}" or value["source_revision"] != head: raise SystemExit(f"stale or malformed receipt: task {task}")
    if "self_asserted" in raw or not isinstance(value["commands"],list) or len(value["commands"]) != 1: raise SystemExit(f"self-asserted receipt: task {task}")
    command=value["commands"][0]
    if set(command) != {"command","command_sha256","input_sha256","result","output_sha256"}: raise SystemExit(f"missing hashes: task {task}")
    if not all(isinstance(command[key],str) and re.fullmatch(r"[0-9a-f]{64}",command[key]) for key in ("command_sha256","input_sha256","output_sha256")): raise SystemExit(f"invalid hashes: task {task}")
    if command["command_sha256"] != sha(command["command"]) or command["output_sha256"] != sha(command["result"]): raise SystemExit(f"tampered receipt: task {task}")
    copied=dict(value); receipt_hash=copied.pop("receipt_sha256")
    if not re.fullmatch(r"[0-9a-f]{64}",receipt_hash) or receipt_hash != sha(json.dumps(copied,sort_keys=True,separators=(",",":"))): raise SystemExit(f"receipt hash mismatch: task {task}")
print("CI_EVIDENCE_VERIFY_OK receipts=4")
PY
