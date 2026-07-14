#!/usr/bin/env bash
# Verifies the private ACR deployment ownership boundary recorded in
# docs/adr/0004-deployment-ownership.md:
#
#   - dev-health-acr owns every ACR Helm/Kubernetes/Compose deployment
#     artifact and documents that ownership in the ADR.
#   - dev-health-ops ships no ACR-named deployment artifact under deploy/
#     or docker/.
#   - the local root Compose file remains an operator-owned artifact
#     outside both repositories.
#
# Exit codes:
#   0  boundary intact
#   1  a boundary violation was found (see the "BOUNDARY VIOLATION" line)
#   2  usage error (missing/invalid arguments)
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: verify-boundaries.sh --acr <path> --ops <path> --compose <path>

  --acr      Path to the dev-health-acr repository checkout (this repo).
  --ops      Path to the dev-health-ops repository checkout (read-only).
  --compose  Path to the operator-owned root Compose file (read-only).

All three flags are required. Paths under --ops and --compose are never
modified; this script only reads them.
EOF
}

acr_path=""
ops_path=""
compose_path=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --acr|--ops|--compose)
      flag="$1"
      if [[ $# -lt 2 ]]; then
        printf 'missing value for %s\n' "$flag" >&2
        usage
        exit 2
      fi
      case "$flag" in
        --acr) acr_path="$2" ;;
        --ops) ops_path="$2" ;;
        --compose) compose_path="$2" ;;
      esac
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage
      exit 2
      ;;
  esac
done

if [[ -z "$acr_path" || -z "$ops_path" || -z "$compose_path" ]]; then
  printf 'missing required argument(s): --acr, --ops, and --compose are all required\n' >&2
  usage
  exit 2
fi

if [[ ! -d "$acr_path" ]]; then
  printf 'invalid --acr path: not a directory: %s\n' "$acr_path" >&2
  exit 2
fi

if [[ ! -d "$ops_path" ]]; then
  printf 'invalid --ops path: not a directory: %s\n' "$ops_path" >&2
  exit 2
fi

if [[ ! -f "$compose_path" ]]; then
  printf 'invalid --compose path: not a regular file: %s\n' "$compose_path" >&2
  exit 2
fi

acr_abs="$(cd "$acr_path" && pwd)"
ops_abs="$(cd "$ops_path" && pwd)"
compose_dir_abs="$(cd "$(dirname "$compose_path")" && pwd)"
compose_abs="$compose_dir_abs/$(basename "$compose_path")"

adr_path="$acr_abs/docs/adr/0004-deployment-ownership.md"

fail() {
  printf 'BOUNDARY VIOLATION: %s\n' "$1" >&2
  exit 1
}

require_adr_regex() {
  local pattern="$1" label="$2"
  grep -qiE "$pattern" "$adr_path" || fail "ADR docs/adr/0004-deployment-ownership.md is missing required content: $label"
}

require_adr_literal() {
  local needle="$1" label="$2"
  grep -qF "$needle" "$adr_path" || fail "ADR docs/adr/0004-deployment-ownership.md is missing required content: $label (expected exact text: \"$needle\")"
}

# 1. The ownership ADR must exist before any packaging work proceeds.
[[ -f "$adr_path" ]] || fail "missing deployment ownership ADR: docs/adr/0004-deployment-ownership.md"

# 2. The ADR must document every required decision from Todo 5.
require_adr_regex 'external.*(postgres|clickhouse)' "external Postgres/ClickHouse/Ops dependency declaration"
require_adr_regex 'existing-secret-only|existing secret' "existing-Secret-only credential contract"
require_adr_regex 'immutable' "immutable private image requirement"
require_adr_regex 'no mcp|mcp is not deployed|acr-mcp is a local' "no-MCP-workload boundary"
require_adr_regex 'supersede' "superseded Ops-owned packaging paths"
require_adr_literal 'Todos 9-11' "reference to superseded deployment plan Todos 9-11"

for exact_path in 'deploy/helm/acr' 'deploy/kubernetes/acr' 'deploy/compose/acr.compose.yml'; do
  require_adr_literal "$exact_path" "ownership table naming the ACR-owned deploy path $exact_path"
done

# 3. Every relative markdown link in the ADR must resolve to a real file
#    (docs/link check scoped to this ADR).
while IFS= read -r link; do
  [[ -z "$link" ]] && continue
  case "$link" in
    http://*|https://*|mailto:*) continue ;;
  esac
  link_path="${link%%#*}"
  [[ -z "$link_path" ]] && continue
  link_dir="$(dirname "$link_path")"
  link_base="$(basename "$link_path")"
  resolved_dir="$(cd "$acr_abs/docs/adr/$link_dir" 2>/dev/null && pwd)" || fail "ADR link target directory does not exist: $link"
  resolved="$resolved_dir/$link_base"
  [[ -f "$resolved" ]] || fail "ADR link target is missing: $link (resolved: $resolved)"
done < <(grep -oE '\]\([^)]+\)' "$adr_path" | sed -E 's/^\]\(//; s/\)$//')

# 4. dev-health-ops must ship no ACR-named deployment artifact. Only
#    deploy/ and docker/ are scanned: legitimate Ops application code
#    such as the internal ACR entitlement API lives elsewhere and is not
#    a deployment artifact.
ops_deploy_roots=()
[[ -d "$ops_abs/deploy" ]] && ops_deploy_roots+=("$ops_abs/deploy")
[[ -d "$ops_abs/docker" ]] && ops_deploy_roots+=("$ops_abs/docker")

if [[ ${#ops_deploy_roots[@]} -gt 0 ]]; then
  while IFS= read -r hit; do
    [[ -z "$hit" ]] && continue
    fail "ACR-named deployment artifact found under dev-health-ops: ${hit#"$ops_abs"/} (private ACR packaging must live only under dev-health-acr, never dev-health-ops)"
  done < <(find "${ops_deploy_roots[@]}" -iname '*acr*' -print 2>/dev/null | sort)
fi

# 5. The root Compose file stays an operator-owned artifact outside both
#    repositories; it is never vendored into ACR or Ops tracked deploy
#    assets.
case "$compose_abs" in
  "$acr_abs"/*)
    fail "root Compose file $compose_abs is located inside the ACR repository; it must remain operator-owned outside dev-health-acr"
    ;;
esac
case "$compose_abs" in
  "$ops_abs"/*)
    fail "root Compose file $compose_abs is located inside the Ops repository; it must remain operator-owned outside dev-health-ops"
    ;;
esac

printf 'ok: private ACR deployment ownership boundary intact\n'
printf '  ADR:     %s\n' "${adr_path#"$acr_abs"/}"
printf '  ACR:     %s\n' "$acr_abs"
printf '  Ops:     %s (no ACR-named artifact under deploy/ or docker/)\n' "$ops_abs"
printf '  Compose: %s (operator-owned, outside both repositories)\n' "$compose_abs"
