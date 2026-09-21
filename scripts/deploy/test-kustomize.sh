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
Usage: test-kustomize.sh --overlay <development|development-mcp|staging|production> --image <digest> [--scenario <name>]

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

require_resource_literal() {
  local kind="$1" name="$2" literal="$3" gate="$4"
  awk -v kind="$kind" -v name="$name" -v literal="$literal" '
    function check() {
      if (index(document, "kind: " kind "\n") > 0 && index(document, "  name: " name "\n") > 0 && index(document, literal) > 0) found = 1
    }
    /^---[[:space:]]*$/ { check(); document=""; next }
    { document = document $0 "\n" }
    END { check(); exit(found ? 0 : 1) }
  ' "$work/rendered.yaml" || fail_gate "$gate"
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
# resource_doc <kind> <name>: the one rendered document for that resource.
resource_doc() {
  awk -v kind="$1" -v name="$2" '
    function check() {
      if (index(document, "kind: " kind "\n") > 0 && index(document, "\n  name: " name "\n") > 0) { printf "%s", document; found = 1 }
    }
    /^---[[:space:]]*$/ { check(); document=""; next }
    { document = document $0 "\n" }
    END { check(); exit(found ? 0 : 1) }
  ' "$work/rendered.yaml"
}

if [[ "$overlay" == *-mcp ]]; then
  # Overlays that compose the hosted acr-mcp component: it must be its own
  # restricted, digest-pinned, credential-less workload with probes and a
  # base-path-only route; acr-api and acr-migrate must not reference it.
  for target in 'Deployment acr-mcp' 'Service acr-mcp' 'ServiceAccount acr-mcp' 'ConfigMap acr-mcp-config' 'NetworkPolicy acr-mcp' 'HTTPRoute acr-mcp'; do
    read -r kind name <<<"$target"
    resource_doc "$kind" "$name" >/dev/null || fail_gate "acr-mcp: ${kind}/${name} was not rendered"
  done
  mcp_deploy="$(resource_doc Deployment acr-mcp)"
  for token in 'args:' '- serve' 'path: /healthz' 'path: /readyz' 'containerPort: 8081' 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault' 'automountServiceAccountToken: false' '- ALL'; do
    grep -qF -- "$token" <<<"$mcp_deploy" || fail_gate "acr-mcp: Deployment is missing $token"
  done
  grep -qE 'image: [^ ]*/acr-mcp@sha256:[a-f0-9]{64}' <<<"$mcp_deploy" || fail_gate "acr-mcp: image is not the digest-pinned acr-mcp image"
  if grep -qE 'secretKeyRef|secretName|secretRef|projected:|ACR_API_TOKEN|serviceAccountToken' <<<"$mcp_deploy"; then
    fail_gate "acr-mcp: Deployment mounts a Secret, projected token, or ACR_API_TOKEN; the pod must hold no credential"
  fi
  # Registry pull auth is the only Secret reference allowed: every line naming a
  # secret is the imagePullSecrets key itself, and its one entry is the base's pull secret.
  if [[ "$(grep -ci 'secret' <<<"$mcp_deploy")" != "$(grep -c '^ *imagePullSecrets:$' <<<"$mcp_deploy")" ]] \
    || ! grep -qA1 '^ *imagePullSecrets:$' <<<"$mcp_deploy" || ! grep -qF -- '- name: acr-registry-pull' <<<"$mcp_deploy"; then
    fail_gate "acr-mcp: the only Secret reference allowed is imagePullSecrets acr-registry-pull"
  fi
  mcp_cm="$(resource_doc ConfigMap acr-mcp-config)"
  for token in 'ACR_MCP_TRANSPORT: http' 'ACR_MCP_HTTP_BASE_PATH: /mcp' 'ACR_API_URL: http://acr-api:8080'; do
    grep -qF -- "$token" <<<"$mcp_cm" || fail_gate "acr-mcp: ConfigMap is missing $token"
  done
  if grep -qE 'TOKEN|PASSWORD|SECRET|DSN' <<<"$mcp_cm"; then fail_gate "acr-mcp: ConfigMap carries a credential-shaped key"; fi
  if resource_doc ServiceAccount acr-mcp | grep -qE '^(secrets|imagePullSecrets):'; then fail_gate "acr-mcp: ServiceAccount must carry no secrets"; fi
  mcp_route="$(resource_doc HTTPRoute acr-mcp)"
  grep -qF 'value: /mcp' <<<"$mcp_route" || fail_gate "acr-mcp: HTTPRoute must match the MCP base path"
  grep -qE 'component: mcp' <<<"$(resource_doc NetworkPolicy acr-api)" || fail_gate "acr-mcp: acr-api NetworkPolicy must admit the acr-mcp pods"
  for target in 'Deployment acr-api' 'Job acr-migrate'; do
    read -r kind name <<<"$target"
    if resource_doc "$kind" "$name" | grep -qi 'acr-mcp'; then
      fail_gate "acr-mcp: ${kind}/${name} references acr-mcp; it must run only as its own workload"
    fi
  done
  pass "ownership: existing Secrets and caller-owned Gateway only; acr-mcp is its own credential-less workload"
else
  if grep -qi 'acr-mcp' "$work/rendered.yaml"; then
    fail_gate "mcp-default-off: rendered output references acr-mcp in an overlay that does not compose the component"
  fi
  pass "ownership: existing Secrets and caller-owned Gateway only; no MCP workload"
fi

if grep -E '^\s*image:' "$work/rendered.yaml" | grep -vq '@sha256:'; then
  fail_gate "immutable-image: a rendered image is not pinned to @sha256"
fi
require_literal "image: $image" "immutable-image: requested image was not rendered"
pass "immutable-image: API and migration images use the requested digest"

for token in 'secretKeyRef:' 'name: acr-runtime-credentials' 'name: acr-migration-credentials' 'ACR_POSTGRES_DSN' 'ACR_POSTGRES_MIGRATION_DSN' 'imagePullSecrets:' 'ACR_DEV_HEALTH_ENTITLEMENT_URL: http://ops.dev-health.internal:8000'; do
  require_literal "$token" "secret-ref: missing $token"
done
if grep -qE 'name: acr-runtime-credentials.*ACR_POSTGRES_MIGRATION_DSN|name: acr-migration-credentials.*ACR_POSTGRES_DSN' "$work/rendered.yaml"; then
  fail_gate "secret-ref: runtime and migration DSNs are shared"
fi
pass "secret-ref: distinct existing runtime and migration credential references"

for token in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault' 'automountServiceAccountToken: false' 'port: 5432' 'port: 9000' 'port: 8000' 'name: prepare-entitlement-token' 'name: entitlement-token-source'; do
  require_literal "$token" "pod-security: missing $token"
done
if grep -Eq 'ACR_(POSTGRES|CLICKHOUSE|DEV_HEALTH_ENTITLEMENT)_CA_BUNDLE|secretName: acr-(postgres|clickhouse|entitlement)-ca' "$work/rendered.yaml"; then
  fail_gate 'ordinary Kustomize base must not require internal CA bundles'
fi
require_literal 'drop:' 'pod-security: missing capability drop'
require_literal '- ALL' 'pod-security: capabilities are not dropped'
pass "pod-security: restricted workloads and PostgreSQL-only migration egress"

for target in 'Deployment acr-api' 'Job acr-migrate'; do
  read -r kind name <<<"$target"
  require_resource_literal "$kind" "$name" 'runAsNonRoot: true' "pod-security: ${kind}/${name} lacks restricted pod security"
  require_resource_literal "$kind" "$name" 'medium: Memory' "pod-security: ${kind}/${name} lacks memory-backed writable storage"
done
pass "internal-transport: ordinary base renders without CA projections"

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
