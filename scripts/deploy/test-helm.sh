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
# violation): use any scenario listed by --help.
#   bash scripts/deploy/test-helm.sh --values <v> --image <img> \
#     --scenario <name>
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
              shared-runtime-migration-dsn, injected-mcp, entitlement-path,
              pgbouncer-missing-pooler, extra-container, direct-with-pooler,
              unsupported-entitlement-scheme, userinfo-url, query-url, fragment-url, unknown-root-key,
              unknown-config-key, alternate-port, mutable-token-copy-image,
              missing-device-verification-url, invalid-device-verification-url.

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

container_block() {
  local name="$1"
  awk -v name="$name" '
    $0 == "        - name: " name { inside = 1 }
    inside && $0 ~ /^        - name: / && $0 != "        - name: " name { exit }
    inside { print }
  ' "$rendered"
}

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
  entitlement-path)
    negative entitlement-path "entitlement-origin" \
      --set-string "config.entitlement.url=https://ops.dev-health.internal/api/internal/entitlements"
    exit 0 ;;
  pgbouncer-missing-pooler)
    negative pgbouncer-missing-pooler "pgbouncer-admin-dsn" \
      --set-string "config.postgresConnectionKind=pgbouncer"
    exit 0 ;;
  extra-container)
    negative extra-container "injected-mcp" \
      --set-json 'deployment.extraContainers=[{"name":"sidecar","image":"registry.internal/dev-health-acr/helper@sha256:2222222222222222222222222222222222222222222222222222222222222222"}]'
    exit 0 ;;
  direct-with-pooler)
    negative direct-with-pooler "direct-mode-pooler" \
      --set-string "credentials.runtime.poolerAdminDsnKey=ACR_POSTGRES_POOLER_ADMIN_DSN"
    exit 0 ;;
  unsupported-entitlement-scheme)
    negative unsupported-entitlement-scheme "entitlement-origin" \
      --set-string "config.entitlement.url=ftp://ops.dev-health.internal"
    exit 0 ;;
  userinfo-url)
    negative userinfo-url "entitlement-origin" \
      --set-string "config.entitlement.url=https://user@ops.dev-health.internal"
    exit 0 ;;
  query-url)
    negative query-url "entitlement-origin" \
      --set-string "config.entitlement.url=https://ops.dev-health.internal?x=1"
    exit 0 ;;
  fragment-url)
    negative fragment-url "entitlement-origin" \
      --set-string "config.entitlement.url=https://ops.dev-health.internal#f"
    exit 0 ;;
  unknown-root-key)
    negative unknown-root-key "bogusRootKey" \
      --set-string "bogusRootKey=x"
    exit 0 ;;
  unknown-config-key)
    negative unknown-config-key "bogusKey" \
      --set-string "config.bogusKey=x"
    exit 0 ;;
  alternate-port)
    negative alternate-port "addr" \
      --set-string "config.addr=:9090"
    exit 0 ;;
  mutable-token-copy-image)
    negative mutable-token-copy-image "mutable-image: security.tokenCopyImage" \
      --set-string "config.entitlement.url=https://ops.dev-health.internal" \
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
      --set-string "security.tokenCopyImage=registry.internal/dev-health-acr/token-copy:latest"
    exit 0 ;;
  missing-device-verification-url)
    negative missing-device-verification-url "device-verification-url" \
      --set "config.requireBackingStores=true" \
      --set-string "config.deviceVerificationUrl="
    exit 0 ;;
  invalid-device-verification-url)
    negative invalid-device-verification-url "device-verification-url" \
      --set "config.requireBackingStores=true" \
      --set-string "config.deviceVerificationUrl=/acr/device"
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
  pass "kubeconform: schema validation passed ($("$KUBECONFORM" -v 2>/dev/null | head -1))"
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

# Gate 8: local mode has no remote entitlement inputs; explicit remote mode
# retains the hardened Secret projection contract.
if grep -Eq 'ACR_DEV_HEALTH_ENTITLEMENT_|prepare-entitlement-token|entitlement-token|entitlement-ca|port: 443' "$rendered"; then
  fail_gate "local-entitlement: development render contains a remote entitlement URL, token, CA, init container, or egress port"
fi
pass "local-entitlement: development render omits remote URL/token/CA/network inputs"

distinct_token_render="$workdir/distinct-token.yaml"
render \
  --set-string 'config.entitlement.url=http://ops.dev-health.internal:8000' \
  --set-string 'credentials.entitlementToken.existingSecret=acr-entitlement-token' \
  --set-string 'config.entitlementCaBundle.existingSecret=acr-entitlement-ca' \
  --set-string 'credentials.entitlementToken.key=source-token' \
  --set-string 'config.entitlement.tokenFileName=runtime-token' \
  >"$distinct_token_render"
grep -qF 'cp /source/runtime-token /target/runtime-token' "$distinct_token_render" \
  || fail_gate "entitlement-token: init container must copy the projected token filename"
pass "entitlement-token: init container copies the projected Secret filename"

token_copy_image="registry.example/token-copy@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
token_copy_render="$(render \
  --set-string 'config.entitlement.url=https://ops.dev-health.internal' \
  --set-string 'credentials.entitlementToken.existingSecret=acr-entitlement-token' \
  --set-string "security.tokenCopyImage=${token_copy_image}")"
token_copy_block="$(awk '/        - name: prepare-entitlement-token/{inside=1} inside{print} inside && /^      containers:/{exit}' <<<"${token_copy_render}")"
grep -Fq "image: \"${token_copy_image}\"" <<<"${token_copy_block}" \
  || fail_gate "token-copy-image: prepare-entitlement-token does not use security.tokenCopyImage"
pass "token-copy-image: prepare-entitlement-token uses the configured immutable image"

assert_restricted_container() {
  local name="$1" source="${2:-$rendered}" block
  block="$(awk -v name="$name" '
    $0 == "        - name: " name { inside = 1 }
    inside && $0 ~ /^        - name: / && $0 != "        - name: " name { exit }
    inside { print }
  ' "$source")"
  [[ -n "$block" ]] || fail_gate "restricted-container: $name is missing"
  for token in 'runAsNonRoot: true' 'runAsUser: 65532' 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' 'privileged: false' 'type: RuntimeDefault'; do
    grep -qF "$token" <<<"$block" || fail_gate "restricted-container: $name must render '$token'"
  done
  if ! grep -qF 'drop:' <<<"$block" || ! grep -qF -- '- ALL' <<<"$block"; then
    fail_gate "restricted-container: $name must drop all capabilities"
  fi
  if grep -qE 'runAsUser: 0|runAsNonRoot: false|CHOWN' <<<"$block"; then
    fail_gate "restricted-container: $name must not request root or CHOWN"
  fi
}

assert_restricted_container prepare-entitlement-token "$distinct_token_render"
assert_restricted_container acr-api
assert_restricted_container acr-migrate
pass "pod-security: every rendered API, migration, and present init container is Restricted-compatible"

# Gate 9: exact native-TLS dependency ports and Gateway-only API ingress.
python3 - "$rendered" <<'PY' || exit 1
import sys
docs = open(sys.argv[1]).read().split('\n---\n')
policies = [d for d in docs if '\nkind: NetworkPolicy' in ('\n'+d)]
api = next((d for d in policies if 'component: api' in d), '')
migrate = next((d for d in policies if 'component: migration' in d), '')
def fail(msg):
    print('  FAIL network-policy: '+msg, file=sys.stderr); sys.exit(1)
if not api or not migrate: fail('API and migration NetworkPolicies must both render')
for port in ('port: 5432', 'port: 9000'):
    if port not in api: fail('API egress is missing internal dependency '+port)
if 'port: 9440' in api or 'port: 8123' in api: fail('API egress contains an unexpected ClickHouse port')
if 'protocol: TCP' not in api: fail('API egress must explicitly use TCP')
if 'port: 8080' not in api or 'namespaceSelector:' not in api: fail('API ingress must be constrained to the configured Gateway namespace selector')
if 'port: 5432' not in migrate or 'protocol: TCP' not in migrate: fail('migration policy must allow TCP PostgreSQL only')
for port in ('port: 9000', 'port: 8000', 'port: 8080'):
    if port in migrate: fail('migration policy must not allow non-PostgreSQL dependency '+port)
if 'port: 8000' in api: fail('local API egress must not retain the remote entitlement port')
print('  ok   network-policy: local API permits TCP Postgres/ClickHouse ports and Gateway ingress; migration permits only DNS + TCP Postgres')
PY

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

# Gate 11: migration hook prerequisites exist before the Job (fresh install).
python3 - "$rendered" <<'PY' || exit 1
import sys
docs = open(sys.argv[1]).read().split('\n---\n')
def weight(d):
    for line in d.splitlines():
        if 'helm.sh/hook-weight' in line:
            return int(line.split(':')[-1].strip().strip('"'))
    return None
sa=cm=np=job=None
for d in docs:
    is_migrate = 'component: migration' in d
    pre = ('pre-install' in d) and ('pre-upgrade' in d)
    if not (is_migrate and pre):
        continue
    if '\nkind: ServiceAccount' in ('\n'+d): sa=weight(d)
    elif '\nkind: ConfigMap' in ('\n'+d): cm=weight(d)
    elif '\nkind: NetworkPolicy' in ('\n'+d): np=weight(d)
    elif '\nkind: Job' in ('\n'+d): job=weight(d)
miss=[n for n,v in (('ServiceAccount',sa),('ConfigMap',cm),('NetworkPolicy',np),('Job',job)) if v is None]
if miss:
    print('  FAIL migration-prereqs: missing migration hook resource(s): '+','.join(miss), file=sys.stderr); sys.exit(1)
if not (sa < job and cm < job and np < job):
    print(f'  FAIL migration-prereqs: prereq weights (sa={sa},cm={cm},np={np}) must be more negative than Job ({job})', file=sys.stderr); sys.exit(1)
print('  ok   migration-prereqs: migration SA/ConfigMap/NetworkPolicy are pre-install,pre-upgrade hooks ordered before the Job')
PY

# Gate 12: evidence-ID signing keys sourced from an existing Secret.
for key in 'ACR_EVIDENCE_ID_ACTIVE_KID' 'ACR_EVIDENCE_ID_KEYS'; do
  grep -q "$key" "$rendered" || fail_gate "evidence-keys: $key not wired into the Deployment"
done
if ! { grep -q 'ACR_EVIDENCE_ID_KEYS' "$rendered" && grep -A3 'ACR_EVIDENCE_ID_KEYS' "$rendered" | grep -q 'secretKeyRef:'; }; then
  fail_gate "evidence-keys: ACR_EVIDENCE_ID_KEYS must come from a secretKeyRef"
fi
pass "evidence-keys: ACR_EVIDENCE_ID_ACTIVE_KID + ACR_EVIDENCE_ID_KEYS sourced from existing Secret"

# Gate 13: the hosted runtime's device authorization browser URL is rendered.
device_verification_url="$(grep 'ACR_DEVICE_VERIFICATION_URL' "$rendered" | head -1 | grep -oE 'https?://[^"]+')"
[[ "$device_verification_url" == "https://dev-health.internal/acr/device" ]] \
  || fail_gate "device-verification-url: rendered URL '$device_verification_url' does not match the configured approval page"
pass "device-verification-url: hosted runtime approval URL is rendered ($device_verification_url)"

# Gate 14: remote mode remains explicit and accepts an ordinary HTTP service origin.
ent_url="$(grep 'ACR_DEV_HEALTH_ENTITLEMENT_URL' "$distinct_token_render" | head -1 | grep -oE 'https?://[^"]+')"
[[ "$ent_url" == "http://ops.dev-health.internal:8000" ]] \
  || fail_gate "entitlement-origin: explicit remote render did not retain the HTTP origin"
pass "entitlement-origin: explicit remote render retains HTTP origin and token projection"

# Gate 15: Secret rotation rolls pods (checksum/credentials present and reactive).
cc=$(grep -c 'checksum/credentials' "$rendered")
[[ "$cc" -ge 2 ]] || fail_gate "secret-rotation: checksum/credentials must annotate both Deployment and migration Job (found $cc)"
sum_a=$(render | grep -m1 'checksum/credentials' | awk '{print $2}')
sum_b=$(render --set-string credentials.rotationRevision=rotated-2 | grep -m1 'checksum/credentials' | awk '{print $2}')
[[ -n "$sum_a" && "$sum_a" != "$sum_b" ]] || fail_gate "secret-rotation: bumping credentials.rotationRevision must change checksum/credentials (a=$sum_a b=$sum_b)"
pass "secret-rotation: checksum/credentials present on both workloads and changes with rotationRevision"

# Gate 16: migration workload is covered by a NetworkPolicy.
python3 - "$rendered" <<'PY' || exit 1
import sys
docs = open(sys.argv[1]).read().split('\n---\n')
ok = any((('\nkind: NetworkPolicy' in ('\n'+d)) and ('component: migration' in d)) for d in docs)
if not ok:
    print('  FAIL migration-netpol: no NetworkPolicy selects the migration component', file=sys.stderr); sys.exit(1)
print('  ok   migration-netpol: a NetworkPolicy applies to the migration workload')
PY

# Gate 17: PgBouncer mode fully wires both pooler admin DSNs.
pgb=$(render --set-string config.postgresConnectionKind=pgbouncer \
  --set-string credentials.runtime.poolerAdminDsnKey=ACR_POSTGRES_POOLER_ADMIN_DSN \
  --set-string credentials.migration.poolerAdminDsnKey=ACR_POSTGRES_MIGRATION_POOLER_ADMIN_DSN 2>&1)
grep -q 'ACR_POSTGRES_POOLER_ADMIN_DSN' <<<"$pgb" || fail_gate "pgbouncer: runtime ACR_POSTGRES_POOLER_ADMIN_DSN not wired in pgbouncer mode"
grep -q 'ACR_POSTGRES_MIGRATION_POOLER_ADMIN_DSN' <<<"$pgb" || fail_gate "pgbouncer: migration ACR_POSTGRES_MIGRATION_POOLER_ADMIN_DSN not wired in pgbouncer mode"
pass "pgbouncer: connection kind pgbouncer wires runtime + migration pooler admin DSNs"

# Gate 18: contextFabric.falkor.* wires ACR_CONTEXT_FABRIC_FALKOR_* into
# acr-api (CHAOS-3774), both with and without an existingSecret password.
if grep -q 'ACR_CONTEXT_FABRIC_FALKOR' "$rendered"; then
  fail_gate "falkor-env: contextFabric.falkor.addr is empty in these values but ACR_CONTEXT_FABRIC_FALKOR_* rendered anyway"
fi
pass "falkor-env: unset contextFabric.falkor.addr renders no ACR_CONTEXT_FABRIC_FALKOR_* (never fails closed)"

extract_container_block() {
  # Same extraction as container_block()/assert_restricted_container(), but
  # over an arbitrary rendered doc read from stdin rather than $rendered.
  local name="$1"
  awk -v name="$name" '
    $0 == "        - name: " name { inside = 1 }
    inside && $0 ~ /^        - name: / && $0 != "        - name: " name { exit }
    inside { print }
  '
}

falkor_no_secret="$(render \
  --set-string contextFabric.falkor.addr=falkordb.internal:6379 \
  --set-string contextFabric.falkor.graphPrefix=acr-cf)"
falkor_no_secret_api="$(extract_container_block acr-api <<<"$falkor_no_secret")"
for key in 'ACR_CONTEXT_FABRIC_FALKOR_ADDR' 'ACR_CONTEXT_FABRIC_FALKOR_TLS' 'ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE' 'ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX'; do
  grep -qF "$key" <<<"$falkor_no_secret_api" || fail_gate "falkor-env: $key missing from acr-api with no existingSecret configured"
done
grep -q 'ACR_CONTEXT_FABRIC_FALKOR_PASSWORD' <<<"$falkor_no_secret_api" && fail_gate "falkor-env: ACR_CONTEXT_FABRIC_FALKOR_PASSWORD rendered without an existingSecret"
pass "falkor-env: no-secret FalkorDB values render addr/tls/allowInsecure/graphPrefix into acr-api, no password ref"

falkor_with_secret="$(render \
  --set-string contextFabric.falkor.addr=falkordb.internal:6379 \
  --set-string contextFabric.falkor.existingSecret=acr-falkor-credentials \
  --set-string contextFabric.falkor.passwordKey=ACR_CONTEXT_FABRIC_FALKOR_PASSWORD)"
falkor_with_secret_api="$(extract_container_block acr-api <<<"$falkor_with_secret")"
grep -q 'ACR_CONTEXT_FABRIC_FALKOR_PASSWORD' <<<"$falkor_with_secret_api" || fail_gate "falkor-env: ACR_CONTEXT_FABRIC_FALKOR_PASSWORD missing from acr-api with existingSecret configured"
grep -A3 'ACR_CONTEXT_FABRIC_FALKOR_PASSWORD' <<<"$falkor_with_secret_api" | grep -q 'name: "acr-falkor-credentials"' \
  || fail_gate "falkor-env: ACR_CONTEXT_FABRIC_FALKOR_PASSWORD does not reference contextFabric.falkor.existingSecret"
pass "falkor-env: existingSecret FalkorDB values render ACR_CONTEXT_FABRIC_FALKOR_PASSWORD as a secretKeyRef in acr-api"

if grep -q '/var/run/acr/postgres-ca' "$rendered"; then
  fail_gate "postgres-transport: ordinary development render must not require a PostgreSQL CA bundle"
fi
pass "postgres-transport: ordinary development render has no mandatory PostgreSQL CA bundle"

custom_projection="$(render \
  --set-string config.entitlement.url=https://ops.dev-health.internal \
  --set-string credentials.entitlementToken.existingSecret=acr-entitlement-token \
  --set-string credentials.entitlementToken.key=entitlement.custom \
  --set-string config.entitlement.tokenFileName=token.custom \
  --set-string config.postgresCaBundle.existingSecret=acr-postgres-ca \
  --set-string config.postgresCaBundle.key=postgres.custom \
  --set-string config.clickhouseCaBundle.existingSecret=acr-clickhouse-ca \
  --set-string config.clickhouseCaBundle.key=clickhouse.custom \
  --set-string config.entitlementCaBundle.existingSecret=acr-entitlement-ca \
  --set-string config.entitlementCaBundle.key=entitlement-ca.custom)"
grep -Fq 'key: "entitlement.custom"' <<<"$custom_projection" || fail_gate "secret-projection: custom entitlement Secret key is not rendered"
grep -Fq 'path: "token.custom"' <<<"$custom_projection" || fail_gate "secret-projection: entitlement token is not projected to tokenFileName"
grep -Fq 'cp /source/token.custom /target/token.custom' <<<"$custom_projection" || fail_gate "secret-projection: init container does not consume the projected entitlement token filename"
for key in postgres.custom clickhouse.custom entitlement-ca.custom; do
  grep -Fq "key: \"$key\"" <<<"$custom_projection" || fail_gate "secret-projection: custom CA Secret key $key is not rendered"
done
for path in '/var/run/acr/postgres-ca/ca.crt' '/var/run/acr/clickhouse-ca/ca.crt' '/var/run/acr/entitlement-ca/ca.crt'; do
  grep -Fq "$path" <<<"$custom_projection" || fail_gate "secret-projection: runtime does not consume canonical CA projection $path"
done
pass "secret-projection: custom Secret keys map to canonical projected filenames"

# Gate 19 (CHAOS-4055): optional in-release FalkorDB workload. Off by default;
# when enabled it must render a non-empty digest-pinned StatefulSet + Service
# with the compose service's GRAPH.QUERY health vocabulary, mount the image's
# real data path, and stay under the default-deny NetworkPolicy posture.
# Literals like acr-test-falkordb and 6379 are intentional: this gate asserts
# THIS harness's fixed release name and the chart's default (compose-parity)
# port, the same convention as gate 13's hard-coded approval URL -- it does
# not claim fullnameOverride/service.port overrides are invalid.
if grep -qE '^\s*kind:\s*StatefulSet\s*$' "$rendered" || grep -q 'component: falkordb' "$rendered"; then
  fail_gate "falkordb-workload: default render must not contain the FalkorDB workload"
fi
pass "falkordb-workload: disabled by default (no StatefulSet in default render)"

falkordb_render="$(render \
  --set contextFabric.falkordb.enabled=true \
  --set-string contextFabric.falkor.addr=acr-test-falkordb:6379)"
[[ -n "$falkordb_render" ]] || fail_gate "falkordb-workload: enabled render produced no output"

# Doc-scoped extraction: the assertions below must hold inside the specific
# rendered document, not anywhere in the concatenated output (a comment or an
# unrelated doc must not satisfy them). Line-based document accumulation (a
# line that is exactly "---" separates documents) rather than a regex RS,
# which POSIX awk does not guarantee.
extract_doc() {
  # extract_doc <kind> <must-match-regex> [must-not-match-regex]
  awk -v kind="$1" -v want="$2" -v veto="${3:-}" '
    function flush() {
      if (doc ~ "\nkind: "kind"\n" && doc ~ want && (veto == "" || doc !~ veto)) { printf "%s", doc; exit }
      doc = ""
    }
    /^---$/ { flush(); next }
    { doc = doc $0 "\n" }
    END { flush() }
  '
}
extract_falkordb_doc() {
  extract_doc "$1" 'component: falkordb' <<<"$falkordb_render"
}

falkordb_sts="$(extract_falkordb_doc StatefulSet)"
[[ -n "$falkordb_sts" ]] || fail_gate "falkordb-workload: enabled render is missing the falkordb StatefulSet"
grep -qE '^\s+image: "[^"]*falkordb/falkordb@sha256:[0-9a-f]{64}"' <<<"$falkordb_sts" \
  || fail_gate "falkordb-workload: StatefulSet image is not digest-pinned"
grep -qF 'GRAPH.QUERY' <<<"$falkordb_sts" || fail_gate "falkordb-workload: StatefulSet probes must use the GRAPH.QUERY vocabulary, not PING alone"
grep -qE '^\s+mountPath: /var/lib/falkordb/data\s*$' <<<"$falkordb_sts" \
  || fail_gate "falkordb-workload: data volume must mount the image FALKORDB_DATA_PATH"
grep -qE '^\s+volumeClaimTemplates:' <<<"$falkordb_sts" \
  || fail_gate "falkordb-workload: default persistence must render volumeClaimTemplates"

falkordb_svc="$(extract_falkordb_doc Service)"
[[ -n "$falkordb_svc" ]] || fail_gate "falkordb-workload: enabled render is missing the falkordb Service"
grep -q 'name: acr-test-falkordb' <<<"$falkordb_svc" || fail_gate "falkordb-workload: Service name must be <fullname>-falkordb"
grep -qE '^\s+port: 6379\s*$' <<<"$falkordb_svc" || fail_gate "falkordb-workload: Service must expose port 6379"
grep -qE '^\s+app.kubernetes.io/component: falkordb\s*$' <<<"$falkordb_svc" \
  || fail_gate "falkordb-workload: Service selector must target the falkordb component"

falkordb_np="$(extract_falkordb_doc NetworkPolicy)"
[[ -n "$falkordb_np" ]] || fail_gate "falkordb-workload: no NetworkPolicy selects the falkordb component"
grep -qE '^\s+port: 6379\s*$' <<<"$falkordb_np" || fail_gate "falkordb-workload: falkordb NetworkPolicy must constrain ingress to port 6379"
grep -qE '^\s+app.kubernetes.io/component: api\s*$' <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy ingress must admit the api component"
grep -qE '^\s+app.kubernetes.io/component: projector\s*$' <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy ingress must admit the projector component"
if grep -qE 'component: migration' <<<"$falkordb_np"; then
  fail_gate "falkordb-workload: the migration Job must not be in the falkordb trust set"
fi
grep -qE '^\s+egress: \[\]\s*$' <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy must deny all egress (egress: [])"

# The falkor egress rule must appear on the API policy when addr is set.
falkordb_api_np="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$falkordb_render")"
grep -qE '^\s+port: 6379\s*$' <<<"$falkordb_api_np" \
  || fail_gate "falkordb-workload: API NetworkPolicy must gain falkor egress port 6379 when contextFabric.falkor.addr is set"

# Operator podLabels must never detach the pod from the selectors: the last
# (winning) occurrence of the component label must stay falkordb.
falkordb_override="$(render \
  --set contextFabric.falkordb.enabled=true \
  --set-json 'contextFabric.falkordb.podLabels={"app.kubernetes.io/component":"bogus"}')"
falkordb_override_sts="$(extract_doc StatefulSet 'component: falkordb' <<<"$falkordb_override")"
[[ "$(grep -E '^\s+app.kubernetes.io/component:' <<<"$falkordb_override_sts" | tail -1 | awk '{print $2}')" == "falkordb" ]] \
  || fail_gate "falkordb-workload: podLabels must not be able to override the component selector label"
pass "falkordb-workload: enabled render has digest-pinned StatefulSet, Service, PVC, GRAPH.QUERY readiness, scoped NetworkPolicies"

set +e
falkordb_mutable="$(render \
  --set contextFabric.falkordb.enabled=true \
  --set-string contextFabric.falkordb.image=falkordb/falkordb:latest 2>&1)"
falkordb_mutable_status=$?
set -e
[[ $falkordb_mutable_status -ne 0 ]] || fail_gate "falkordb-workload: a mutable falkordb image tag must fail closed"
grep -qF 'mutable-image: contextFabric.falkordb.image' <<<"$falkordb_mutable" \
  || fail_gate "falkordb-workload: mutable falkordb image failure did not name the violation"
pass "falkordb-workload: mutable falkordb image reference fails closed naming the violation"

printf 'RESULT: happy path passed all gates\n'
