#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
evidence="$root/.omo/evidence"
task8="$evidence/context-fabric-08-no-upload.json"
task9="$evidence/context-fabric-09-mixed-mcp.json"
live="$evidence/context-fabric-09-live-mcp.json"
mkdir -p "$evidence"
if ! git -C "$root" diff --quiet; then
  printf 'test requires a clean source worktree\n' >&2
  exit 1
fi
if ! git -C "$root" diff --cached --quiet; then
  printf 'test requires a clean source worktree\n' >&2
  exit 1
fi
if [[ -n "$(git -C "$root" status --porcelain)" ]]; then
  printf 'test requires no untracked source files\n' >&2
  exit 1
fi
backup="$(mktemp -d)"
restored=0
restore_receipts() {
  [[ "$restored" == 0 ]] || return
  restored=1
  for name in context-fabric-08-no-upload.json context-fabric-09-mixed-mcp.json context-fabric-09-live-mcp.json; do
    if [[ -e "$backup/$name" ]]; then cp "$backup/$name" "$evidence/$name"; else rm -f "$evidence/$name"; fi
  done
  rm -rf "$backup"
}
trap restore_receipts EXIT INT TERM
for name in context-fabric-08-no-upload.json context-fabric-09-mixed-mcp.json context-fabric-09-live-mcp.json; do
  [[ ! -e "$evidence/$name" ]] || cp "$evidence/$name" "$backup/$name"
done

printf '{"sentinel":"task8"}\n' > "$task8"
task8_before="$(shasum -a 256 "$task8" | awk '{print $1}')"
for scenario in injected-source-leak injected-path-leak forced-invariant-failure; do
  if "$root/scripts/e2e/local-index-privacy.sh" --scenario "$scenario"; then exit 1; fi
  [[ "$task8_before" == "$(shasum -a 256 "$task8" | awk '{print $1}')" ]] || exit 1
done
printf '{"stale":true}\n' > "$task8"
if ACR_E2E_FORCE_CANONICAL_FAILURE=1 "$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload; then exit 1; fi
[[ ! -e "$task8" ]] || exit 1

printf '{"sentinel":"task9"}\n' > "$task9"
task9_before="$(shasum -a 256 "$task9" | awk '{print $1}')"
for scenario in hosted-only local-timeout packet-content-overflow hosted-unavailable writeback-default post-response-process-failure; do
  set +e
  "$root/scripts/e2e/mcp-codegraph.sh" --scenario "$scenario"
  status=$?
  set -e
  [[ "$status" -eq 0 || "$status" -gt 0 ]] || exit 1
  [[ "$task9_before" == "$(shasum -a 256 "$task9" | awk '{print $1}')" ]] || exit 1
done
printf '{"stale":true}\n' > "$task9"
if ACR_E2E_FORCE_CANONICAL_FAILURE=1 "$root/scripts/e2e/mcp-codegraph.sh" --scenario mixed; then exit 1; fi
[[ ! -e "$task9" ]] || exit 1

"$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload
"$root/scripts/e2e/mcp-codegraph.sh" --scenario mixed
python3 - "$task8" "$task9" "$root" <<'PY'
import json,subprocess,sys
head=subprocess.check_output(['git','-C',sys.argv[3],'rev-parse','HEAD'],text=True).strip()
for path,task in zip(sys.argv[1:3],('CHAOS-3007 Task 8','CHAOS-3007 Task 9')):
    receipt=json.load(open(path))
    assert receipt['task']==task and receipt['source_revision']==head
    assert receipt['source_worktree_clean'] and receipt['source_identity_unchanged']
    assert receipt['harness_sha256'] and receipt['binary_sha256'] and receipt['tls_verified']
PY
task9_before="$(shasum -a 256 "$task9" | awk '{print $1}')"
"$root/scripts/e2e/mcp-codegraph-live.sh" --self-test
[[ "$task9_before" == "$(shasum -a 256 "$task9" | awk '{print $1}')" ]] || exit 1
printf '{"stale":true}\n' > "$live"
for receipt in "$task8" "$task9" "$live"; do
  python3 - "$receipt" <<'PY'
import json,os,stat,sys
assert stat.S_IMODE(os.stat(sys.argv[1]).st_mode)==0o600
json.load(open(sys.argv[1]))
PY
done
printf '{"stale":true}\n' > "$live"
if "$root/scripts/e2e/mcp-codegraph-live.sh" --repo /definitely-missing --scenario mixed; then exit 1; fi
[[ ! -e "$live" ]] || exit 1
out_of_tree="$(mktemp -d)"
(cd "$out_of_tree" && "$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload)
rm -rf "$out_of_tree"
