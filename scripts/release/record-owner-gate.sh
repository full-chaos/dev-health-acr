#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
source "$root/scripts/release/approval-receipt.sh"

approval_parse_options "$@" || { printf 'usage: record-owner-gate.sh --approval-receipt RECEIPT --digest sha256:DIGEST [--dry-run] TARGET VERSION\n' >&2; exit 1; }
((${#APPROVAL_ARGS[@]} == 2)) || exit 1
target="${APPROVAL_ARGS[0]}"
version="${APPROVAL_ARGS[1]}"
repo="full-chaos/dev-health-acr"

approval_verify "$APPROVAL_RECEIPT" record_owner_gate "$repo" "$target" "$version" "$APPROVAL_DIGEST" || exit 1
if "$APPROVAL_DRY_RUN"; then
  printf 'dry-run approved: owner gate remains a later human-controlled action\n'
  exit 0
fi
printf 'owner gate recording is intentionally deferred to the human owner step\n' >&2
exit 1
