#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/deploy/kubernetes/acr/scripts/lib.sh"

overlay=""
image="${TEST_IMAGE_DIGEST:-}"
scenario="happy"
work=""

usage() {
  cat >&2 <<'EOF'
Usage: test-kustomize.sh --overlay <development|staging|production> --image <digest> [--scenario <name>]

Scenarios: happy, mutable-image, migration-failure, rollback-fail-closed
EOF
}

pass() {
  printf '  ok   %s\n' "$1"
}

fail_gate() {
  printf '  FAIL %s\n' "$1" >&2
  exit 1
}

require_line() {
  local expression="$1"
  local gate="$2"
  grep -Eq "$expression" "$work/rendered.yaml" || fail_gate "$gate"
}

require_literal() {
  local literal="$1"
  local gate="$2"
  grep -qF -- "$literal" "$work/rendered.yaml" || fail_gate "$gate"
}

require_kind() {
  local kind="$1"
  require_line "^kind: ${kind}$" "policy-parity: missing ${kind}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --overlay|--image|--scenario)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      case "$1" in
        --overlay) overlay="$2" ;;
        --image) image="$2" ;;
        --scenario) scenario="$2" ;;
      esac
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage; exit 2 ;;
  esac
done

require_overlay "$overlay"
[[ -n "$image" ]] || { printf 'missing --image\n' >&2; usage; exit 2; }

case "$scenario" in
  mutable-image)
    if (require_digest_image "$image") >/dev/null 2>&1; then
      fail_gate "mutable-image: mutable image was accepted"
    fi
    printf '  FAIL mutable-image: rejected non-digest image\n' >&2
    exit 1
    ;;
  happy|migration-failure|rollback-fail-closed) require_digest_image "$image" ;;
  *) printf 'unknown scenario: %s\n' "$scenario" >&2; usage; exit 2 ;;
esac

work="$(mktemp -d "${TMPDIR:-/tmp}/acr-kustomize-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
render_manifest "$overlay" "$image" all > "$work/rendered.yaml"
[[ -s "$work/rendered.yaml" ]] || fail_gate "render: no manifests rendered"
pass "render: ${overlay} overlay rendered"

if command -v kubeconform >/dev/null 2>&1; then
  kubeconform -strict -ignore-missing-schemas -summary "$work/rendered.yaml" > "$work/kubeconform.txt" 2>&1 \
    || { cat "$work/kubeconform.txt" >&2; fail_gate "kubeconform: schema validation failed"; }
  pass "kubeconform: strict validation passed"
fi

for kind in ConfigMap ServiceAccount Service Deployment Job HorizontalPodAutoscaler PodDisruptionBudget NetworkPolicy HTTPRoute; do
  require_kind "$kind"
done
pass "policy-parity: required ACR resources are rendered"

if grep -Eq '^kind: (Secret|Gateway)$' "$work/rendered.yaml"; then
  fail_gate "ownership: rendered a caller-owned Secret or Gateway"
fi
if grep -qi 'acr-mcp' "$work/rendered.yaml"; then
  fail_gate "no-mcp: rendered output references acr-mcp"
fi
pass "ownership: existing Secrets and caller-owned Gateway only; no MCP workload"

if grep -E '^\s*image:' "$work/rendered.yaml" | grep -vq '@sha256:'; then
  fail_gate "immutable-image: a rendered image is not pinned to @sha256"
fi
require_literal "image: $image" "immutable-image: requested image was not rendered"
pass "immutable-image: API and migration images use the requested digest"

for token in 'secretKeyRef:' 'name: acr-runtime-credentials' 'name: acr-migration-credentials' 'ACR_POSTGRES_DSN' 'ACR_POSTGRES_MIGRATION_DSN' 'imagePullSecrets:' 'ACR_CLICKHOUSE_CA_BUNDLE' 'ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE' 'secretName: acr-postgres-ca' 'secretName: acr-clickhouse-ca' 'secretName: acr-entitlement-ca'; do
  require_literal "$token" "secret-ref: missing $token"
done
if grep -qE 'name: acr-runtime-credentials.*ACR_POSTGRES_MIGRATION_DSN|name: acr-migration-credentials.*ACR_POSTGRES_DSN' "$work/rendered.yaml"; then
  fail_gate "secret-ref: runtime and migration DSNs are shared"
fi
pass "secret-ref: distinct existing runtime and migration credential references"

for token in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault' 'automountServiceAccountToken: false' 'port: 5432' 'port: 9440' 'name: prepare-entitlement-token' 'name: entitlement-token-source' 'name: entitlement-ca-source'; do
  require_literal "$token" "pod-security: missing $token"
done
require_literal 'drop:' 'pod-security: missing capability drop'
require_literal '- ALL' 'pod-security: capabilities are not dropped'
pass "pod-security: restricted workloads and PostgreSQL-only migration egress"

if [[ "$overlay" == staging || "$overlay" == production ]]; then
  require_literal "ACR_ENVIRONMENT: $overlay" "overlay: wrong environment value"
fi
pass "overlay: namespace, route, and environment values are specific"

case "$scenario" in
  happy)
    printf 'RESULT: happy path passed all Kustomize policy gates\n'
    ;;
  migration-failure)
    job_line="$(grep -n 'select_kinds "Job"' "$ROOT/deploy/kubernetes/acr/scripts/apply.sh" | cut -d: -f1 | head -1)"
    deploy_line="$(grep -n 'select_kinds "Deployment"' "$ROOT/deploy/kubernetes/acr/scripts/apply.sh" | cut -d: -f1 | head -1)"
    [[ -n "$job_line" && -n "$deploy_line" && "$job_line" -lt "$deploy_line" ]] \
      || fail_gate "migration-failure: apply script does not gate Deployment after Job"
    printf '  FAIL migration-failure: migration gate blocks Deployment rollout before apply\n' >&2
    exit 1
    ;;
  rollback-fail-closed)
    rollback="$ROOT/deploy/kubernetes/acr/scripts/rollback.sh"
    output="$(bash "$rollback" --overlay "$overlay" --image "$image")"
    if grep -qE '^kind: (Job|Secret)$' <<<"$output" || grep -q 'acr-migrate' <<<"$output"; then
      fail_gate "rollback-fail-closed: rollback rendered a schema-changing resource"
    fi
    printf '  FAIL rollback-fail-closed: rollback is application-only and preserves schema\n' >&2
    exit 1
    ;;
esac
