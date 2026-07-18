#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
source "$root/scripts/release/approval-receipt.sh"

approval_parse_options "$@" || { printf 'usage: verify-private-consumer.sh --approval-receipt RECEIPT --digest sha256:DIGEST [--dry-run] TARGET VERSION\n' >&2; exit 1; }
((${#APPROVAL_ARGS[@]} == 2)) || exit 1
target="${APPROVAL_ARGS[0]}"
version="${APPROVAL_ARGS[1]}"
repo="full-chaos/dev-health-acr"

[[ "$target" != *public* && "$target" != *:latest* ]] || exit 1
approval_verify "$APPROVAL_RECEIPT" verify_private_consumer "$repo" "$target" "$version" "$APPROVAL_DIGEST" || exit 1
if "$APPROVAL_DRY_RUN"; then
  printf 'dry-run approved: private consumer verification remains blocked before remote access\n'
  exit 0
fi
printf 'private consumer verification is intentionally not implemented by preflight\n' >&2
exit 1
