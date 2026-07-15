#!/usr/bin/env bash
# Offline verification harness for the private ACR Helm chart
# (deploy/helm/acr). It never contacts a cluster and never provisions a
# dependency: it renders the chart with helm and asserts the security and
# ownership contract from docs/adr/0004-deployment-ownership.md and Todo 8.
#
# Happy path (all gates must pass, exit 0):
#   TEST_IMAGE_DIGEST=registry.example/acr-api@sha256:<64hex> \
#   bash scripts/deploy/test-helm.sh \
#     --values deploy/helm/acr/values-development.yaml \
#     --image "$TEST_IMAGE_DIGEST"
#
# Negative scenarios (each must fail closed pre-apply, exit 1, naming the
# violation):
#   bash scripts/deploy/test-helm.sh --values <v> --image <img> \
#     --scenario mutable-image|invalid-secret-ref|invalid-image-pull-secret-ref|shared-runtime-migration-dsn|injected-mcp
#
# Exit codes:
#   0  requested scenario passed (happy rendered + all gates; or negative failed as required)
#   1  a gate failed / a negative scenario did not fail closed as required
#   2  usage or environment error
set -euo pipefail

CHART_DEFAULT="deploy/helm/acr"
chart="$CHART_DEFAULT"
values=""
image="${TEST_IMAGE_DIGEST:-}"
scenario="happy"

usage() {
  cat >&2 <<'EOF'
Usage: test-helm.sh --values <path> [--image <ref>] [--chart <path>] [--scenario <name>]

  --values    Values file for the render (required).
  --image     Immutable @sha256 image reference (required; or set TEST_IMAGE_DIGEST).
  --chart     Chart directory (default: deploy/helm/acr).
  --scenario  happy (default) or one of the negative scenarios:
              mutable-image, invalid-secret-ref, invalid-image-pull-secret-ref,
              shared-runtime-migration-dsn, injected-mcp.

The harness only renders (helm template/lint) and validates output offline.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --values|--image|--chart|--scenario)
      flag="$1"
      if [[ $# -lt 2 ]]; then printf 'missing value for %s\n' "$flag" >&2; usage; exit 2; fi
      case "$flag" in
        --values) values="$2" ;;
        --image) image="$2" ;;
        --chart) chart="$2" ;;
        --scenario) scenario="$2" ;;
      esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage; exit 2 ;;
  esac
done

[[ -n "$values" ]] || { printf 'missing required argument: --values\n' >&2; usage; exit 2; }
[[ -f "$values" ]] || { printf 'invalid --values path: not a file: %s\n' "$values" >&2; exit 2; }
[[ -d "$chart" ]] || { printf 'invalid --chart path: not a directory: %s\n' "$chart" >&2; exit 2; }
[[ -n "$image" ]] || { printf 'missing required argument: --image (or TEST_IMAGE_DIGEST)\n' >&2; usage; exit 2; }

command -v helm >/dev/null 2>&1 || { printf 'helm is required on PATH\n' >&2; exit 2; }

# kubeconform is optional but preferred; fall back to GOPATH/bin.
KUBECONFORM=""
if command -v kubeconform >/dev/null 2>&1; then
  KUBECONFORM="kubeconform"
elif [[ -x "$(go env GOPATH 2>/dev/null)/bin/kubeconform" ]]; then
  KUBECONFORM="$(go env GOPATH)/bin/kubeconform"
elif [[ -x "$HOME/.local/share/go/bin/kubeconform" ]]; then
  KUBECONFORM="$HOME/.local/share/go/bin/kubeconform"
fi

RELEASE="acr-test"
NAMESPACE="acr-test"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

pass() { printf '  ok   %s\n' "$1"; }
fail_gate() { printf '  FAIL %s\n' "$1" >&2; exit 1; }

render() {
  # Render with the base happy inputs plus any scenario overrides ($@).
  helm template "$RELEASE" "$chart" \
    --namespace "$NAMESPACE" \
    -f "$values" \
    --set-string "image.reference=$image" \
    "$@"
}

# ---------------------------------------------------------------------------
# Negative scenarios: must fail closed pre-apply, exit 1, naming the violation.
# ---------------------------------------------------------------------------
negative() {
  local name="$1"; shift
  local expect="$1"; shift
  local out status
  printf 'scenario: %s (expect fail-closed naming %q)\n' "$name" "$expect"
  set +e
  out="$(render "$@" 2>&1)"
  status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    printf '  FAIL %s rendered successfully but a fail-closed violation was required\n' "$name" >&2
    exit 1
  fi
  if ! grep -qF "$expect" <<<"$out"; then
    printf '  FAIL %s failed (exit %d) but the error did not name %q. Got:\n%s\n' "$name" "$status" "$expect" "$out" >&2
    exit 1
  fi
  pass "$name failed closed naming '$expect' (exit $status)"
  printf 'RESULT: negative scenario %s passed\n' "$name"
}

case "$scenario" in
  mutable-image)
    negative mutable-image "mutable-image" \
      --set-string "image.reference=registry.internal/dev-health-acr/acr-api:latest"
    exit 0 ;;
  invalid-secret-ref)
    negative invalid-secret-ref "invalid-secret-ref" \
      --set-string "credentials.runtime.existingSecret=Invalid_Secret_Name"
    exit 0 ;;
  invalid-image-pull-secret-ref)
    negative invalid-image-pull-secret-ref "invalid-image-pull-secret-ref" \
      --set-json 'imagePullSecrets=[{"name":"Bad_Pull_Secret"}]'
    exit 0 ;;
  shared-runtime-migration-dsn)
    negative shared-runtime-migration-dsn "shared-runtime-migration-dsn" \
      --set-string "credentials.migration.existingSecret=acr-runtime-credentials" \
      --set-string "credentials.migration.postgresDsnKey=ACR_POSTGRES_DSN" \
      --set-string "credentials.runtime.postgresDsnKey=ACR_POSTGRES_DSN"
    exit 0 ;;
  injected-mcp)
    negative injected-mcp "injected-mcp" \
      --set-json 'deployment.extraContainers=[{"name":"mcp","image":"registry.internal/dev-health-acr/acr-mcp@sha256:1111111111111111111111111111111111111111111111111111111111111111","command":["/usr/local/bin/acr-mcp","serve"]}]'
    exit 0 ;;
  happy) : ;;
  *) printf 'unknown scenario: %s\n' "$scenario" >&2; usage; exit 2 ;;
esac

# ---------------------------------------------------------------------------
# Happy path: every gate must pass.
# ---------------------------------------------------------------------------
printf 'scenario: happy (chart=%s values=%s)\n' "$chart" "$values"

# Gate 0: image argument must itself be an immutable digest.
grep -Eq '@sha256:[0-9a-f]{64}$' <<<"$image" || fail_gate "immutable-image: --image $image is not an @sha256 digest reference"
pass "immutable-image: --image is an @sha256 digest reference"

# Gate 1: values schema present.
[[ -f "$chart/values.schema.json" ]] || fail_gate "values-schema: $chart/values.schema.json is missing"
if command -v python3 >/dev/null 2>&1; then
  python3 -c "import json,sys; json.load(open('$chart/values.schema.json'))" || fail_gate "values-schema: values.schema.json is not valid JSON"
fi
pass "values-schema: values.schema.json present and valid"

# Gate 2: helm lint (validates values.schema.json + templates, strict).
helm lint "$chart" -f "$values" --set-string "image.reference=$image" --strict >"$workdir/lint.txt" 2>&1 \
  || { cat "$workdir/lint.txt" >&2; fail_gate "helm-lint: strict lint failed"; }
pass "helm-lint: strict lint passed"

# Gate 3: strict template render.
render >"$workdir/rendered.yaml" 2>"$workdir/render.err" \
  || { cat "$workdir/render.err" >&2; fail_gate "helm-template: render failed"; }
[[ -s "$workdir/rendered.yaml" ]] || fail_gate "helm-template: render produced no output"
pass "helm-template: strict render succeeded"
rendered="$workdir/rendered.yaml"

# Gate 4: kubeconform schema validation (CRDs such as HTTPRoute are ignored).
if [[ -n "$KUBECONFORM" ]]; then
  "$KUBECONFORM" -strict -ignore-missing-schemas -summary "$rendered" >"$workdir/kubeconform.txt" 2>&1 \
    || { cat "$workdir/kubeconform.txt" >&2; fail_gate "kubeconform: schema validation failed"; }
  pass "kubeconform: schema validation passed ($("$KUBECONFORM" -version 2>/dev/null | head -1))"
else
  printf '  SKIP kubeconform not installed (install github.com/yannh/kubeconform to enable this gate)\n' >&2
fi

# Gate 5: immutable image in every rendered container.
if grep -E '^\s*image:' "$rendered" | grep -vq '@sha256:'; then
  grep -nE '^\s*image:' "$rendered" | grep -v '@sha256:' >&2 || true
  fail_gate "immutable-image: a rendered container image is not pinned to @sha256"
fi
pass "immutable-image: all rendered images pinned to @sha256"

# Gate 6: no-MCP workload anywhere in the render.
if grep -q 'acr-mcp' "$rendered"; then
  grep -n 'acr-mcp' "$rendered" >&2 || true
  fail_gate "no-mcp: rendered output references acr-mcp"
fi
pass "no-mcp: rendered output contains no acr-mcp workload"

# Gate 7: existing-Secret-only credential + imagePullSecret reference syntax.
grep -q 'secretKeyRef:' "$rendered" || fail_gate "secret-ref: no secretKeyRef found (credentials must come from existing Secrets)"
grep -q 'ACR_POSTGRES_DSN' "$rendered" || fail_gate "secret-ref: runtime ACR_POSTGRES_DSN reference missing"
grep -q 'ACR_POSTGRES_MIGRATION_DSN' "$rendered" || fail_gate "secret-ref: migration ACR_POSTGRES_MIGRATION_DSN reference missing"
grep -q 'imagePullSecrets:' "$rendered" || fail_gate "secret-ref: imagePullSecrets missing"
# No inline Secret object or plaintext credential material may be rendered.
if grep -qE '^\s*kind:\s*Secret\s*$' "$rendered"; then
  fail_gate "secret-ref: chart rendered a Secret object; the contract is existing-Secret-only"
fi
# stringData is Secret-only; a ConfigMap's data: field is legitimate and not checked here.
if grep -qiE '^\s*stringData:\s*$' "$rendered"; then
  fail_gate "secret-ref: chart rendered inline Secret stringData; credentials must be references only"
fi
pass "secret-ref: credentials and imagePullSecrets are existing-Secret references only"

# Gate 8: pod-security (restricted) posture.
for token in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'type: RuntimeDefault'; do
  grep -qF "$token" "$rendered" || fail_gate "pod-security: expected '$token' not found"
done
grep -q 'drop:' "$rendered" && grep -q '\- ALL' "$rendered" || fail_gate "pod-security: capabilities are not dropped (drop: [ALL])"
pass "pod-security: restricted context (nonroot, RO rootfs, no-privesc, drop ALL, seccomp)"

# Gate 9: migration ordering via pre-install/pre-upgrade hook.
python3 - "$rendered" <<'PY' || exit 1
import sys
docs = open(sys.argv[1]).read().split('\n---\n')
job_hook = False
deploy_no_hook = True
for d in docs:
    is_job = '\nkind: Job' in ('\n'+d) or d.lstrip().startswith('kind: Job')
    is_deploy = '\nkind: Deployment' in ('\n'+d) or d.lstrip().startswith('kind: Deployment')
    has_pre = ('helm.sh/hook' in d) and ('pre-install' in d) and ('pre-upgrade' in d)
    if is_job and has_pre:
        job_hook = True
    if is_deploy and ('helm.sh/hook' in d):
        deploy_no_hook = False
if not job_hook:
    print("  FAIL migration-order: no Job with pre-install,pre-upgrade hook", file=sys.stderr); sys.exit(1)
if not deploy_no_hook:
    print("  FAIL migration-order: Deployment must not carry a helm hook", file=sys.stderr); sys.exit(1)
print("  ok   migration-order: migration Job is a pre-install,pre-upgrade hook; Deployment is not hooked")
PY

# Gate 10: HTTPRoute targets a caller-supplied Gateway and no Gateway is created.
if grep -q 'kind: HTTPRoute' "$rendered"; then
  grep -q 'parentRefs:' "$rendered" || fail_gate "httproute: HTTPRoute rendered without parentRefs"
  if grep -qE '^\s*kind:\s*Gateway\s*$' "$rendered"; then
    fail_gate "httproute: chart rendered a Gateway object; it must target a caller-supplied Gateway only"
  fi
  pass "httproute: HTTPRoute targets caller-supplied Gateway; no Gateway object created"
else
  printf '  note HTTPRoute disabled in these values (gateway.enabled=false)\n'
fi

printf 'RESULT: happy path passed all gates\n'
