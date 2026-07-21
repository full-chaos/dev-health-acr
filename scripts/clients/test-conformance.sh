#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
clients=""
fixture=""
while (($#)); do
  case "$1" in
    --clients) clients="$2"; shift 2 ;;
    --fixture) fixture="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done

[[ -n "$clients" || -n "$fixture" ]] || exit 2
if [[ -n "$fixture" ]]; then
  metadata="$fixture/fixture.v1.json"
  [[ -f "$metadata" ]] || exit 2
  expected="$(python3 - "$metadata" <<'PY'
import json,sys
try:
 value=json.load(open(sys.argv[1],encoding='utf-8'))
 expected=value['expected_classification']
 assert isinstance(expected,str) and expected
 print(expected)
except Exception:
 raise SystemExit(2)
PY
)" || exit 2
  output="$(mktemp)"
  trap 'rm -f "$output"' EXIT
  set +e
  bash "$repo_root/scripts/clients/verify-packages.sh" --fixture "$fixture" >"$output" 2>&1
  status=$?
  set -e
  [[ $status -eq 1 ]] || exit 1
  if grep -Eqi 'fcacr_[A-Za-z0-9_-]+|bearer[[:space:]]+[A-Za-z0-9._-]+' "$output"; then
    printf 'secret material leaked in conformance output\n' >&2
    exit 1
  fi
  grep -Fqx "CLIENT_INVALID_FIXTURE classification=$expected" "$output" || exit 1
  printf 'CLIENT_CONFORMANCE_NEGATIVE_OK fixture=%s classification=%s exit=1\n' "$(basename "$fixture")" "$expected"
  exit 1
fi

[[ "$clients" == "opencode,claude-code,codex,cursor" ]] || exit 2
bash "$repo_root/scripts/clients/verify-packages.sh" --contract clients/conformance/client-bundle.v1.json
go -C "$repo_root" test -race -run 'TestClientConformance|TestClientServeCommand' ./internal/mcpclientfixtures

evidence_dir="$(mktemp -d)"
cleanup() { rm -rf "$evidence_dir"; }
trap cleanup EXIT
for scenario in mixed hosted-only writeback-default; do
  ACR_E2E_EVIDENCE_DIR="$evidence_dir" bash "$repo_root/scripts/e2e/mcp-codegraph.sh" --scenario "$scenario"
done
for scenario in local-timeout:41 hosted-unavailable:42 incompatible-version:43; do
  name="${scenario%%:*}"
  expected_exit="${scenario##*:}"
  set +e
  ACR_E2E_EVIDENCE_DIR="$evidence_dir" bash "$repo_root/scripts/e2e/mcp-codegraph.sh" --scenario "$name" >"$evidence_dir/$name.out" 2>&1
  status=$?
  set -e
  [[ $status -eq $expected_exit ]] || exit 1
  grep -Fqx "ACR_E2E_EXPECTED_FAILURE_VALIDATED scenario=$name exit_code=$expected_exit" "$evidence_dir/$name.out" || exit 1
done
printf 'CLIENT_CONFORMANCE_OK clients=%s hosted_only=mixed local_expansion=passed hosted_expansion=passed degraded=visible writeback=absent untrusted=required\n' "$clients"
