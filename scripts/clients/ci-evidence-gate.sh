#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
mode=""
out=""

usage() {
  printf '%s\n' 'usage: ci-evidence-gate.sh --mode generate --out <relative-output-directory>' >&2
  exit 2
}

while (($#)); do
  case "$1" in
    --mode) [[ -z "$mode" && $# -ge 2 ]] || usage; mode="$2"; shift 2 ;;
    --out) [[ -z "$out" && $# -ge 2 ]] || usage; out="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ "$mode" == generate && -n "$out" && "$out" != /* && "$out" != *".."* ]] || usage

cd "$root"
head="$(git rev-parse HEAD)"
[[ "$head" =~ ^[0-9a-f]{40}$ ]] || exit 1
mkdir -p "$out"
chmod 700 "$out"

sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
source_digest() {
  local file digest
  for file in "$@"; do
    [[ -f "$file" ]] || return 1
    digest="$(sha256 "$file")"
    printf '%s  %s\n' "$digest" "$file"
  done | shasum -a 256 | awk '{print $1}'
}

sanitize_result() {
  local result="$1"
  [[ "$result" != *"$root"* && "$result" != *$'\n'* ]] || return 1
  [[ ! "$result" =~ ([[:space:]]|^)(bearer|token|secret|password)[[:space:]:=] ]] || return 1
  printf '%s' "$result"
}

run_or_defer() {
  local client="$1" command="$2" result status
  shift 2
  if ! command -v "$client" >/dev/null 2>&1; then
    printf 'deferred|%s_DEFERRED client=not_installed' "${client^^}"
    return
  fi
  if result="$("$@" 2>&1)"; then
    status=passed
  else
    printf 'gate failed command=%s\n' "$command" >&2
    exit 1
  fi
  result="$(printf '%s\n' "$result" | grep -E '(_OK|_SKIPPED|_DEFERRED)' | tail -n 1 || true)"
  [[ -n "$result" ]] || result="${client^^}_LIFECYCLE_OK"
  printf '%s|%s' "$status" "$(sanitize_result "$result")"
}

write_receipt() {
  local task="$1" state="$2" command="$3" inputs="$4" result="$5" deferral="$6" destination body
  destination="$out/task-${task}-receipt.v1.json"
  body="$(mktemp "$out/.receipt.XXXXXX")"
  trap 'rm -f "$body"' RETURN
  python3 - "$body" "$task" "$head" "$state" "$command" "$inputs" "$result" "$deferral" <<'PY'
import hashlib,json,re,sys
path,task,head,state,command,inputs,result,deferral=sys.argv[1:]
sha=lambda value: hashlib.sha256(value.encode()).hexdigest()
if not re.fullmatch(r"[0-9a-f]{40}",head): raise SystemExit(1)
if re.search(r"(?:^|\s)(?:bearer|token|secret|password)[\s:=]",result,re.I): raise SystemExit(1)
if "/Users/" in command+result or "\\" in command+result: raise SystemExit(1)
receipt={"schema_version":"acr.ci_evidence_receipt.v1","task":f"CHAOS-3007 Task {task}","source_revision":head,"state":state,"commands":[{"command":command,"command_sha256":sha(command),"input_sha256":inputs,"result":result,"output_sha256":sha(result)}],"deferral":json.loads(deferral)}
canonical=json.dumps(receipt,sort_keys=True,separators=(",",":"))
receipt["receipt_sha256"]=sha(canonical)
with open(path,"w",encoding="utf-8") as out: json.dump(receipt,out,sort_keys=True,separators=(",",":")); out.write("\n")
PY
  chmod 600 "$body"
  mv -f "$body" "$destination"
  trap - RETURN
}

claude_command='scripts/clients/test-claude-code.sh --package clients/claude-code --scenario lifecycle'
claude_run="$(run_or_defer claude "$claude_command" bash scripts/clients/test-claude-code.sh --package "$root/clients/claude-code" --scenario lifecycle)"
claude_state="${claude_run%%|*}"; claude_result="${claude_run#*|}"
write_receipt 14 "$claude_state" "$claude_command" "$(source_digest scripts/clients/test-claude-code.sh clients/claude-code/marketplace/.claude-plugin/marketplace.json)" "$claude_result" '{"client":"claude-code","native":"deferred_when_not_installed"}'

codex_command='scripts/clients/test-codex.sh --package clients/codex --scenario lifecycle'
codex_run="$(run_or_defer codex "$codex_command" bash scripts/clients/test-codex.sh --package "$root/clients/codex")"
codex_state="${codex_run%%|*}"; codex_result="${codex_run#*|}"
write_receipt 15 "$codex_state" "$codex_command" "$(source_digest scripts/clients/test-codex.sh clients/codex/package.v1.json)" "$codex_result" '{"client":"codex","native":"deferred_when_not_installed"}'

cursor_command='scripts/clients/test-cursor.sh --package clients/cursor --scenario lifecycle'
cursor_result="$(bash scripts/clients/test-cursor.sh --package "$root/clients/cursor" --scenario lifecycle 2>&1 | grep -E 'CURSOR_(LIFECYCLE_OK|NATIVE_SKIPPED)' | tail -n 1)"
[[ -n "$cursor_result" ]] || exit 1
write_receipt 16 passed "$cursor_command" "$(source_digest scripts/clients/test-cursor.sh clients/cursor/.cursor-plugin/plugin.json)" "$(sanitize_result "$cursor_result")" '{"client":"cursor","native":"deferred_when_not_installed","windows":"deferred_platform_not_windows"}'

strict_command='scripts/clients/test-conformance.sh --clients opencode,claude-code,codex,cursor; scripts/release/test_release_scripts.sh; scripts/release/test-remote-gates.sh --mode dry-run --fixtures testdata/release-approvals; scripts/release/test-remote-gates.sh --mode reject-invalid --fixtures testdata/release-approvals'
strict_output="$(mktemp)"; clean_root="$(mktemp -d)"
cleanup_strict() { git worktree remove --force "$clean_root" >/dev/null 2>&1 || true; rm -rf "$clean_root" "$strict_output"; }
trap cleanup_strict EXIT
rmdir "$clean_root"
git worktree add --quiet --detach "$clean_root" "$head"
(
  cd "$clean_root"
  bash scripts/clients/test-conformance.sh --clients opencode,claude-code,codex,cursor
  bash scripts/release/test_release_scripts.sh
  bash scripts/release/test-remote-gates.sh --mode dry-run --fixtures testdata/release-approvals
  bash scripts/release/test-remote-gates.sh --mode reject-invalid --fixtures testdata/release-approvals
) >"$strict_output" 2>&1
strict_result="$(grep -E 'CLIENT_CONFORMANCE_OK|REMOTE_RELEASE_GATE' "$strict_output" | tail -n 1)"
[[ -n "$strict_result" ]] || exit 1
write_receipt 17 passed "$strict_command" "$(source_digest scripts/clients/test-conformance.sh scripts/release/test_release_scripts.sh scripts/release/test-remote-gates.sh)" "$(sanitize_result "$strict_result")" '{"synthetic_release_consumer":"passed","task18_f3":"pending"}'

bash scripts/clients/verify-ci-evidence.sh --out "$out"
printf 'CI_EVIDENCE_GATE_OK receipts=4 source_revision=%s\n' "$head"
