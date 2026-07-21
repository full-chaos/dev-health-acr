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
  output="$(mktemp)"
  trap 'rm -f "$output"' EXIT
  set +e
  bash "$repo_root/scripts/clients/verify-packages.sh" --fixture "$fixture" >"$output" 2>&1
  status=$?
  set -e
  [[ $status -eq 1 ]] || { cat "$output" >&2; exit 1; }
  if grep -Eqi 'fcacr_[A-Za-z0-9_-]+|bearer[[:space:]]+[A-Za-z0-9._-]+' "$output"; then
    printf 'secret material leaked in conformance output\n' >&2
    exit 1
  fi
  printf 'CLIENT_CONFORMANCE_NEGATIVE_OK fixture=%s exit=1\n' "$(basename "$fixture")"
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
for scenario in local-timeout hosted-unavailable incompatible-version; do
  set +e
  ACR_E2E_EVIDENCE_DIR="$evidence_dir" bash "$repo_root/scripts/e2e/mcp-codegraph.sh" --scenario "$scenario" >"$evidence_dir/$scenario.out" 2>&1
  status=$?
  set -e
  [[ $status -ne 0 ]] || { cat "$evidence_dir/$scenario.out" >&2; exit 1; }
done
printf 'CLIENT_CONFORMANCE_OK clients=%s hosted_only=mixed local_expansion=passed hosted_expansion=passed degraded=visible writeback=absent untrusted=required\n' "$clients"
