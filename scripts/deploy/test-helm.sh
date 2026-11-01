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
              missing-device-verification-url, invalid-device-verification-url,
              acr-mcp-mutable-image, acr-mcp-missing-image, acr-mcp-api-url,
              acr-mcp-base-path, acr-mcp-gateway-no-parent.
              The acr-api query embedder scenarios (positive ones render and assert, the rest fail closed):
              embed-api-off-by-default, embed-api-enabled, embed-api-enabled-no-base-url,
              embed-api-bad-port, embed-api-default-values, embed-api-override-values,
              embed-api-projector-unchanged, embed-api-bad-retries, embed-api-timeout-bounds.
              The acr-api ServiceAccount scenarios: api-service-account-dedicated (positive),
              api-service-account-shared, api-service-account-default, api-service-account-default-exchange-off,
              service-account-name-collision.
              The acr-api auth workload client scenarios (CHAOS-11195): auth-workload-client-on (positive),
              auth-workload-client-off-render, auth-workload-client-bad-inputs,
              auth-workload-client-presence-only.
              The lifecycle outbox publisher scenario (CHAOS-11198): auth-projection-url (on renders the URL in the ConfigMap and the
              runtime ACL job; off renders none; a URL with the client off fails the render).

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


# ---------------------------------------------------------------------------
# acr-api query embedder (explicit switch contextFabric.embed.apiEnabled).
# ---------------------------------------------------------------------------
embed_api_render() {
  render \
    --set-string "contextFabric.embed.baseURL=https://api.openai.com/v1" \
    --set-string "contextFabric.embed.provider=openai" \
    --set-string "contextFabric.embed.model=text-embedding-3-large" \
    --set-string "contextFabric.embed.dimension=3072" \
    --set-string "contextFabric.embed.existingSecret=acr-embed-provider" \
    "$@"
}

# embed_api_assert <rendered-file> <on|off>: acr-api container env and the
# acr-api NetworkPolicy either carry the embed wiring (on) or none of it (off).
embed_api_assert() {
  python3 - "$1" "$2" <<'PY' || exit 1
import re, sys
docs = open(sys.argv[1]).read().split('\n---\n')
want = sys.argv[2] == 'on'
def fail(msg):
    print('  FAIL embed-api: ' + msg, file=sys.stderr); sys.exit(1)
deploy = next((d for d in docs if '\nkind: Deployment' in '\n' + d and 'component: api' in d and 'name: acr-api' in d), '')
policy = next((d for d in docs if '\nkind: NetworkPolicy' in '\n' + d and 'component: api' in d), '')
if not deploy or not policy: fail('acr-api Deployment and NetworkPolicy must both render')
names = ['BASE_URL', 'PROVIDER', 'MODEL', 'DIMENSION', 'TIMEOUT', 'MAX_TRANSPORT_RETRIES', 'API_KEY']
present = [n for n in names if 'name: ACR_CONTEXT_FABRIC_EMBED_' + n in deploy]
if want:
    if present != names: fail('acr-api env is missing ' + ','.join(sorted(set(names) - set(present))))
    key = re.search(r'name: ACR_CONTEXT_FABRIC_EMBED_API_KEY\n\s+valueFrom:\n\s+secretKeyRef:\n\s+name: "acr-embed-provider"\n\s+key: "ACR_CONTEXT_FABRIC_EMBED_API_KEY"', deploy)
    if not key: fail('API key must come from the same existingSecret/apiKeyKey the projector uses (secretKeyRef)')
    if 'port: 443' not in policy: fail('acr-api NetworkPolicy has no embed egress rule (port 443)')
else:
    if present: fail('acr-api env carries ' + ','.join(present) + ' although the switch is off')
    if 'port: 443' in policy: fail('acr-api NetworkPolicy carries an embed egress rule although the switch is off')
print('  ok   embed-api: acr-api embed env and egress ' + ('rendered' if want else 'absent'))
PY
}

# embed_api_timeouts <rendered-file> <acr-api timeout> <acr-api retries> <projector timeout> <projector retries>:
# the acr-api Deployment and the acr-projector Deployment each carry exactly
# the stated EMBED_TIMEOUT / EMBED_MAX_TRANSPORT_RETRIES ("-" = not rendered).
embed_api_timeouts() {
  python3 - "$@" <<'PY' || exit 1
import re, sys
docs = open(sys.argv[1]).read().split('\n---\n')
def deployment(component):
    return next((d for d in docs if '\nkind: Deployment' in '\n' + d and ('component: ' + component) in d), '')
def env(doc, name):
    m = re.search(r'name: ACR_CONTEXT_FABRIC_EMBED_' + name + r'\n\s+value: "([^"]*)"', doc)
    return m.group(1) if m else '-'
def fail(msg):
    print('  FAIL embed-api: ' + msg, file=sys.stderr); sys.exit(1)
api, projector = deployment('api'), deployment('projector')
if not api or not projector: fail('acr-api and acr-projector Deployments must both render')
want = {'api': (sys.argv[2], sys.argv[3]), 'projector': (sys.argv[4], sys.argv[5])}
for who, doc in (('api', api), ('projector', projector)):
    got = (env(doc, 'TIMEOUT'), env(doc, 'MAX_TRANSPORT_RETRIES'))
    if got != want[who]: fail('%s timeout/retries = %s, want %s' % (who, got, want[who]))
print('  ok   embed-api: acr-api %s/%s, acr-projector %s/%s' % (want['api'] + want['projector']))
PY
}

embed_api_with_projector() {
  embed_api_render \
    --set contextFabric.projector.enabled=true \
    --set contextFabric.readsEnabled=true \
    --set-string "contextFabric.falkor.addr=falkor.acr-test.svc:6379" \
    "$@"
}

api_sa_render() {
  render \
    --set contextFabric.projector.enabled=true \
    --set contextFabric.readsEnabled=true \
    --set-string "contextFabric.falkor.addr=falkor.acr-test.svc:6379" \
    --set workloadTokenExchange.enabled=true \
    --set-string "workloadTokenExchange.trustDomain=test.cluster.local" \
    --set-json 'imagePullSecrets=[{"name":"ghcr-pull"}]' \
    "$@"
}

# api_sa_assert <rendered-file>: acr-api runs under its own ServiceAccount
# (automount off, no secrets), the pod keeps its pull secret and its projected
# reviewer token, no other workload uses that ServiceAccount, the projector
# carries no exchange wiring, and the TokenReview binding names only acr-api's.
api_sa_assert() {
  python3 - "$1" "$RELEASE" "$NAMESPACE" <<'PY' || exit 1
import re, sys
docs = open(sys.argv[1]).read().split('\n---\n')
rel = sys.argv[2]
ns = sys.argv[3]
def fail(msg):
    print('  FAIL api-sa: ' + msg, file=sys.stderr); sys.exit(1)
def kind(d): m = re.search(r'(?m)^kind: (\S+)$', d); return m.group(1) if m else ''
def name(d): m = re.search(r'(?m)^metadata:\n(?:  .*\n)*?  name: "?([^"\s]+)"?$', d); return m.group(1) if m else ''
def sa(d): m = re.search(r'(?m)^      serviceAccountName: "?([^"\s]+)"?$', d); return m.group(1) if m else None
def raw_sa(d): m = re.search(r'(?m)^      serviceAccountName: (\S+)$', d); return m.group(1) if m else None
api_sa, proj_sa = rel + '-api', rel
sas = [d for d in docs if kind(d) == 'ServiceAccount' and name(d) == api_sa]
if len(sas) != 1: fail('want exactly one ServiceAccount %s, got %d' % (api_sa, len(sas)))
if not re.search(r'(?m)^automountServiceAccountToken: false$', sas[0]): fail('ServiceAccount %s must set automountServiceAccountToken: false' % api_sa)
if re.search(r'(?m)^(secrets|imagePullSecrets):', sas[0]): fail('ServiceAccount %s must carry no secrets or imagePullSecrets' % api_sa)
workloads = [d for d in docs if kind(d) in ('Deployment', 'Job', 'StatefulSet', 'CronJob', 'DaemonSet')]
users = [(kind(d), name(d)) for d in workloads if sa(d) == api_sa]
if users != [('Deployment', rel)]: fail('workloads on %s = %s, want only the acr-api Deployment %s' % (api_sa, users, rel))
api = [d for d in workloads if kind(d) == 'Deployment' and name(d) == rel][0]
if not re.search(r'(?m)^      automountServiceAccountToken: false$', api): fail('acr-api pod must set automountServiceAccountToken: false')
if not re.search(r'(?m)^      imagePullSecrets:\n        - name: ghcr-pull$', api): fail('acr-api pod lost its imagePullSecrets')
vol = re.search(r'(?ms)^        - name: kubernetes-api-reviewer-token\n(.*?)(?=^        - name: |\Z)', api)
if not vol or 'projected:' not in vol.group(1) or 'serviceAccountToken:' not in vol.group(1): fail('acr-api lost its projected reviewer token on the kubernetes-api-reviewer-token volume')
for d in docs:
    if kind(d) == 'ServiceAccount' and not re.search(r'(?m)^metadata:\n(?:  .*\n)*?  name: "[^"]+"$', d): fail('ServiceAccount %s name is not quoted' % name(d))
for d in workloads:
    if raw_sa(d) is not None and not (raw_sa(d).startswith('"') and raw_sa(d).endswith('"')): fail('%s %s serviceAccountName is not quoted' % (kind(d), name(d)))
projector = [d for d in workloads if kind(d) == 'Deployment' and name(d) == rel + '-projector']
if len(projector) != 1: fail('want one projector Deployment')
if sa(projector[0]) != proj_sa: fail('projector serviceAccountName = %s, want %s' % (sa(projector[0]), proj_sa))
for token in ('ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE', 'ACR_WORKLOAD_TRUST_DOMAIN', 'serviceAccountToken:', 'projected:', 'kubernetes-api-reviewer-token'):
    if token in projector[0]: fail('projector pod carries %s' % token)
for d in docs:
    if kind(d) == 'ConfigMap' and name(d) == rel + '-projector-config' and ('WORKLOAD' in d): fail('projector ConfigMap carries workload-token wiring')
crb = [d for d in docs if kind(d) == 'ClusterRoleBinding' and name(d) == rel + '-token-review']
if len(crb) != 1: fail('want one TokenReview ClusterRoleBinding')
allsub = re.search(r'(?ms)^subjects:\n(.*)\Z', crb[0]).group(1)
if len(re.findall(r'(?m)^  - ', allsub)) != 1: fail('TokenReview binding must have exactly one subject, got %d' % len(re.findall(r'(?m)^  - ', allsub)))
subjects = re.findall(r'(?m)^  - kind: ServiceAccount\n    name: "([^"\s]+)"\n    namespace: (\S+)$', crb[0])
if subjects != [(api_sa, ns)]: fail('TokenReview binding subjects = %s, want [(%s, %s)]' % (subjects, api_sa, ns))
sa_names = [name(d) for d in docs if kind(d) == 'ServiceAccount']
if len(sa_names) != len(set(sa_names)): fail('a ServiceAccount name is rendered twice: %s' % sorted(n for n in set(sa_names) if sa_names.count(n) > 1))
if any(sa(d) in (None, 'default') and kind(d) != 'StatefulSet' for d in workloads): fail('a workload pod resolves to the default ServiceAccount')
print('  ok   api-sa: %s dedicated (automount off, no secrets), sole user is acr-api; projector %s clean; TokenReview bound to %s only' % (api_sa, proj_sa, api_sa))
PY
}

# ---------------------------------------------------------------------------
# acr-api auth workload client (CHAOS-11195): a projected ServiceAccount token
# for the workload-token exchange, and the env the client is built from.
# ---------------------------------------------------------------------------
auth_workload_render() {
  render \
    --set authWorkloadClient.enabled=true \
    --set-string "authWorkloadClient.exchangeURL=http://auth-service.acr-test.svc:8080" \
    --set authWorkloadClient.allowPlainHTTP=true \
    --set-string "authWorkloadClient.issuer=https://auth.example.test" \
    "$@"
}

# auth_workload_assert <rendered-file>: acr-api carries one projected token for
# the exchange audience, read-only, no subPath, and the four env values; the
# existing reviewer token volume is untouched; acr-api keeps its own
# ServiceAccount with automount off.
auth_workload_assert() {
  python3 - "$1" "$RELEASE" <<'PY' || exit 1
import re, sys
docs = open(sys.argv[1]).read().split('\n---\n')
rel = sys.argv[2]
def fail(msg):
    print('  FAIL auth-workload: ' + msg, file=sys.stderr); sys.exit(1)
def kind(d): m = re.search(r'(?m)^kind: (\S+)$', d); return m.group(1) if m else ''
def name(d): m = re.search(r'(?m)^metadata:\n(?:  .*\n)*?  name: "?([^"\s]+)"?$', d); return m.group(1) if m else ''
api = [d for d in docs if kind(d) == 'Deployment' and name(d) == rel]
if len(api) != 1: fail('want one acr-api Deployment')
api = api[0]
vols = re.findall(r'(?ms)^        - name: auth-workload-subject\n(.*?)(?=^        - name: |\Z)', api)
if len(vols) != 1: fail('want exactly one auth-workload-subject volume, got %d' % len(vols))
vol = vols[0]
for needle, why in (
    ('projected:', 'a projected volume'),
    ('defaultMode: 256', 'file mode 0400 (defaultMode 256)'),
    ('audience: "dev-health-workload-exchange"', 'the literal exchange audience'),
    ('expirationSeconds: 600', 'expirationSeconds 600'),
    ('path: token', 'path token'),
):
    if needle not in vol: fail('volume lacks ' + why)
if len(re.findall(r'serviceAccountToken:', vol)) != 1: fail('volume must hold exactly one serviceAccountToken source')
if re.search(r'(?m)^\s+(secret|configMap|downwardAPI|emptyDir):', vol): fail('volume must hold only the projected token')
mounts = re.findall(r'(?ms)^            - name: auth-workload-subject\n(.*?)(?=^            - name: |^      volumes:|\Z)', api)
if len(mounts) != 1: fail('want exactly one auth-workload-subject mount, got %d' % len(mounts))
m = mounts[0]
if 'mountPath: "/var/run/secrets/dev-health/auth-workload"' not in m: fail('mount path is not /var/run/secrets/dev-health/auth-workload')
if 'readOnly: true' not in m: fail('mount is not read-only')
if 'subPath' in m: fail('mount uses subPath: it never receives token rotation')
want = {
    'ACR_AUTH_WORKLOAD_EXCHANGE_URL': 'http://auth-service.acr-test.svc:8080',
    'ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP': 'true',
    'ACR_AUTH_WORKLOAD_ISSUER': 'https://auth.example.test',
    'ACR_AUTH_WORKLOAD_SUBJECT_FILE': '/var/run/secrets/dev-health/auth-workload/token',
}
cm = [d for d in docs if kind(d) == 'ConfigMap' and name(d) == rel + '-config']
if len(cm) != 1: fail('want one acr-api ConfigMap')
for k, v in want.items():
    if not re.search(r'(?m)^  %s: "%s"$' % (k, re.escape(v)), cm[0]): fail('ConfigMap lacks %s: "%s"' % (k, v))
if 'ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE' in cm[0]: fail('ConfigMap carries a bootstrap file: the chart is the Kubernetes path')
if 'ACR_AUTH_WORKLOAD' in api: fail('the acr-api container env must take these from the ConfigMap only')
if not re.search(r'(?m)^      serviceAccountName: "' + re.escape(rel) + '-api"$', api): fail('acr-api is not on its own ServiceAccount')
if not re.search(r'(?m)^      automountServiceAccountToken: false$', api): fail('acr-api pod must keep automountServiceAccountToken: false')
rev = re.search(r'(?ms)^        - name: kubernetes-api-reviewer-token\n(.*?)(?=^        - name: |\Z)', api)
if rev and 'dev-health-workload-exchange' in rev.group(1): fail('the reviewer token volume must not carry the exchange audience')
for d in docs:
    if name(d) != rel and kind(d) in ('Deployment', 'Job', 'StatefulSet', 'CronJob') and ('auth-workload-subject' in d or 'ACR_AUTH_WORKLOAD' in d): fail('%s %s carries the auth workload client' % (kind(d), name(d)))
    if kind(d) == 'ConfigMap' and name(d) != rel + '-config' and 'ACR_AUTH_WORKLOAD' in d: fail('ConfigMap %s carries the auth workload client' % name(d))
print('  ok   auth-workload: projected token (audience dev-health-workload-exchange, 600 s, 0400, read-only, no subPath) and four env values on acr-api only')
PY
}

case "$scenario" in
  mutable-image)
    negative mutable-image "mutable-image" \
      --set-string "config.environment=production" \
      --set-string "config.entitlement.url=https://ops.dev-health.internal" \
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
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
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
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
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
      --set-string "config.entitlement.url=ftp://ops.dev-health.internal"
    exit 0 ;;
  userinfo-url)
    negative userinfo-url "entitlement-origin" \
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
      --set-string "config.entitlement.url=https://user@ops.dev-health.internal"
    exit 0 ;;
  query-url)
    negative query-url "entitlement-origin" \
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
      --set-string "config.entitlement.url=https://ops.dev-health.internal?x=1"
    exit 0 ;;
  fragment-url)
    negative fragment-url "entitlement-origin" \
      --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" \
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
  acr-mcp-mutable-image)
    # Production-shaped inputs so the digest rule (not the development tag
    # allowance) is what rejects the mutable tag.
    negative acr-mcp-mutable-image "mutable-image: acrMcp.image.reference" \
      --set-string config.environment=production \
      --set config.requireBackingStores=true \
      --set config.localCompositionReady=false \
      --set-string config.entitlement.url=https://ops.dev-health.internal \
      --set-string credentials.entitlementToken.existingSecret=acr-entitlement-token \
      --set acrMcp.enabled=true \
      --set-string "acrMcp.image.reference=registry.internal/dev-health-acr/acr-mcp:latest"
    exit 0 ;;
  acr-mcp-missing-image)
    negative acr-mcp-missing-image "mutable-image: acrMcp.image.reference is required" \
      --set acrMcp.enabled=true
    exit 0 ;;
  acr-mcp-api-url)
    negative acr-mcp-api-url "acr-mcp-api-url" \
      --set acrMcp.enabled=true \
      --set-string "acrMcp.image.reference=${image}" \
      --set-string "acrMcp.apiUrl=https://acr.internal/api/v1"
    exit 0 ;;
  acr-mcp-base-path)
    negative acr-mcp-base-path "acr-mcp-base-path" \
      --set acrMcp.enabled=true \
      --set-string "acrMcp.image.reference=${image}" \
      --set-string "acrMcp.basePath=/healthz"
    exit 0 ;;
  acr-mcp-gateway-no-parent)
    negative acr-mcp-gateway-no-parent "acr-mcp-gateway" \
      --set acrMcp.enabled=true \
      --set acrMcp.gateway.enabled=true \
      --set-string "acrMcp.image.reference=${image}"
    exit 0 ;;
  embed-api-off-by-default)
    printf 'scenario: embed-api-off-by-default (baseURL set for the projector, apiEnabled unset)\n'
    embed_api_render >"$workdir/embed-off.yaml"
    embed_api_assert "$workdir/embed-off.yaml" off
    printf 'RESULT: scenario embed-api-off-by-default passed\n'
    exit 0 ;;
  embed-api-enabled)
    printf 'scenario: embed-api-enabled (apiEnabled + baseURL)\n'
    embed_api_render --set contextFabric.embed.apiEnabled=true >"$workdir/embed-on.yaml"
    embed_api_assert "$workdir/embed-on.yaml" on
    printf 'RESULT: scenario embed-api-enabled passed\n'
    exit 0 ;;
  embed-api-enabled-no-base-url)
    negative embed-api-enabled-no-base-url "contextFabric.embed.apiEnabled requires contextFabric.embed.baseURL" \
      --set contextFabric.embed.apiEnabled=true
    exit 0 ;;
  embed-api-bad-port)
    negative embed-api-bad-port "/networkPolicy/egress/embedPort': maximum" \
      --set-string "contextFabric.embed.baseURL=https://api.openai.com/v1" \
      --set contextFabric.embed.apiEnabled=true \
      --set networkPolicy.egress.embedPort=70000
    exit 0 ;;
  embed-api-default-values)
    printf 'scenario: embed-api-default-values (acr-api gets its own 1s/0, the projector keeps 45s/5)\n'
    embed_api_with_projector --set contextFabric.embed.apiEnabled=true >"$workdir/embed-default.yaml"
    embed_api_timeouts "$workdir/embed-default.yaml" 1s 0 45s 5
    printf 'RESULT: scenario embed-api-default-values passed\n'
    exit 0 ;;
  embed-api-override-values)
    printf 'scenario: embed-api-override-values (api.* reach acr-api only)\n'
    embed_api_with_projector --set contextFabric.embed.apiEnabled=true \
      --set-string contextFabric.embed.api.timeout=900ms --set-string contextFabric.embed.api.maxTransportRetries=2 \
      --set-string contextFabric.embed.timeout=60s >"$workdir/embed-override.yaml"
    embed_api_timeouts "$workdir/embed-override.yaml" 900ms 2 60s 5
    printf 'RESULT: scenario embed-api-override-values passed\n'
    exit 0 ;;
  embed-api-projector-unchanged)
    printf 'scenario: embed-api-projector-unchanged (switch off: api.* overrides never touch the projector, acr-api carries no embed env)\n'
    embed_api_with_projector --set-string contextFabric.embed.api.timeout=900ms --set-string contextFabric.embed.api.maxTransportRetries=2 >"$workdir/embed-projector.yaml"
    embed_api_timeouts "$workdir/embed-projector.yaml" - - 45s 5
    printf 'RESULT: scenario embed-api-projector-unchanged passed\n'
    exit 0 ;;
  embed-api-bad-retries)
    negative embed-api-bad-retries "/contextFabric/embed/api/maxTransportRetries" \
      --set-string "contextFabric.embed.baseURL=https://api.openai.com/v1" \
      --set contextFabric.embed.apiEnabled=true \
      --set-string contextFabric.embed.api.maxTransportRetries=9
    exit 0 ;;
  embed-api-timeout-bounds)
    printf 'scenario: embed-api-timeout-bounds (the embedder accepts 10ms..1m: refuse 0s, 9ms, 2m; accept 10ms, 1m)\n'
    for bad in 0s 9ms 2m; do
      negative "embed-api-timeout-$bad" "contextFabric.embed.api.timeout must be between 10ms and 1m" \
        --set-string "contextFabric.embed.baseURL=https://api.openai.com/v1" \
        --set contextFabric.embed.apiEnabled=true \
        --set-string "contextFabric.embed.api.timeout=$bad"
    done
    for good in 10ms 1m; do
      embed_api_render --set contextFabric.embed.apiEnabled=true --set-string "contextFabric.embed.api.timeout=$good" >"$workdir/embed-timeout-$good.yaml"
      grep -q "value: \"$good\"" "$workdir/embed-timeout-$good.yaml" || fail_gate "embed-api: timeout $good not rendered into acr-api"
      pass "embed-api-timeout-$good accepted and rendered"
    done
    printf 'RESULT: scenario embed-api-timeout-bounds passed\n'
    exit 0 ;;
  api-service-account-dedicated)
    printf 'scenario: api-service-account-dedicated (acr-api has its own ServiceAccount; projector and TokenReview binding assertions)\n'
    api_sa_render >"$workdir/api-sa.yaml"
    api_sa_assert "$workdir/api-sa.yaml"
    printf 'RESULT: scenario api-service-account-dedicated passed\n'
    exit 0 ;;
  api-service-account-shared)
    negative api-service-account-shared "service-account-name-collision" \
      --set-string "serviceAccount.name=${RELEASE}-api"
    exit 0 ;;
  api-service-account-default)
    negative api-service-account-default "no workload pod may run on a namespace's shared default ServiceAccount" \
      --set workloadTokenExchange.enabled=true \
      --set-string "workloadTokenExchange.trustDomain=test.cluster.local" \
      --set serviceAccount.create=false
    exit 0 ;;
  api-service-account-default-exchange-off)
    printf 'scenario: api-service-account-default-exchange-off (exchange off: create=false with no name still renders, projector on the default ServiceAccount as before)\n'
    render --set contextFabric.projector.enabled=true --set contextFabric.readsEnabled=true \
      --set-string "contextFabric.falkor.addr=falkor.acr-test.svc:6379" \
      --set serviceAccount.create=false >"$workdir/api-sa-off.yaml"
    grep -qE '^      serviceAccountName: "?default"?$' "$workdir/api-sa-off.yaml" || fail_gate "api-sa: exchange off, create=false: projector should still render on the default ServiceAccount"
    if grep -q 'kind: ClusterRoleBinding' "$workdir/api-sa-off.yaml"; then fail_gate "api-sa: exchange off must render no TokenReview binding"; fi
    if grep -q 'kubernetes-api-reviewer-token' "$workdir/api-sa-off.yaml"; then fail_gate "api-sa: exchange off must render no reviewer token volume"; fi
    pass "api-service-account-default-exchange-off renders as before"
    printf 'RESULT: scenario api-service-account-default-exchange-off passed\n'
    exit 0 ;;
  service-account-name-collision)
    printf 'scenario: service-account-name-collision (every ServiceAccount name the chart renders or binds is unique; no consumer is empty or default)\n'
    exch=(--set workloadTokenExchange.enabled=true --set-string "workloadTokenExchange.trustDomain=test.cluster.local")
    for n in "${RELEASE}-api" "${RELEASE}-mcp" "${RELEASE}-migrate" "$RELEASE" default; do
      negative "service-account-name-collision-consumer-$n" "service-account-name-collision" \
        "${exch[@]}" --set-json "workloadTokenExchange.consumerServiceAccounts=[{\"name\":\"$n\"}]"
    done
    for bad in "${RELEASE}-api " " ${RELEASE}-api" $'tab\t' Upper-Case under_score -leading-dash; do
      negative "service-account-name-collision-consumer-invalid-name" "service-account-name-collision: workloadTokenExchange.consumerServiceAccounts[0] is not a valid Kubernetes name" \
        "${exch[@]}" --set-json "workloadTokenExchange.consumerServiceAccounts=[{\"name\":$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$bad")}]"
    done
    negative service-account-name-collision-projector-whitespace "service-account-name-collision: serviceAccount.* is not a valid Kubernetes name" \
      --set serviceAccount.create=false --set-string "serviceAccount.name=${RELEASE}-api "
    negative service-account-name-collision-consumer-empty "service-account-name-collision" \
      "${exch[@]}" --set-json 'workloadTokenExchange.consumerServiceAccounts=[{"name":""}]'
    negative service-account-name-collision-consumer-duplicate "service-account-name-collision" \
      "${exch[@]}" --set-json 'workloadTokenExchange.consumerServiceAccounts=[{"name":"panel-read"},{"name":"panel-read"}]'
    negative service-account-name-collision-shared-is-mcp "service-account-name-collision" \
      --set-string "serviceAccount.name=${RELEASE}-mcp"
    printf 'RESULT: scenario service-account-name-collision passed\n'
    exit 0 ;;
  auth-workload-client-on)
    printf 'scenario: auth-workload-client-on (projected exchange token, read-only mount, env)\n'
    auth_workload_render >"$workdir/auth-workload-on.yaml"
    auth_workload_assert "$workdir/auth-workload-on.yaml"
    printf 'RESULT: scenario auth-workload-client-on passed\n'
    exit 0 ;;
  auth-workload-client-off-render)
    printf 'scenario: auth-workload-client-off-render (off: other values set, render byte-equal to the render without the block)\n'
    render >"$workdir/auth-workload-base.yaml"
    render --set authWorkloadClient.enabled=false \
      --set-string "authWorkloadClient.exchangeURL=http://auth-service.acr-test.svc:8080" \
      --set authWorkloadClient.allowPlainHTTP=true \
      --set-string "authWorkloadClient.issuer=https://auth.example.test" >"$workdir/auth-workload-off.yaml"
    cmp -s "$workdir/auth-workload-base.yaml" "$workdir/auth-workload-off.yaml" || { diff -u "$workdir/auth-workload-base.yaml" "$workdir/auth-workload-off.yaml" >&2 || true; fail_gate "auth-workload: the off render differs from the render without the block"; }
    if grep -qE 'ACR_AUTH_WORKLOAD|auth-workload-subject|dev-health-workload-exchange' "$workdir/auth-workload-off.yaml"; then fail_gate "auth-workload: the off render carries auth workload wiring"; fi
    pass "auth-workload-client-off-render: off renders no volume, mount or env, byte-equal"
    printf 'RESULT: scenario auth-workload-client-off-render passed\n'
    exit 0 ;;
  auth-workload-client-bad-inputs)
    printf 'scenario: auth-workload-client-bad-inputs (a missing field fails the render naming the value; the audience and the expiry are no values)\n'
    on=(--set authWorkloadClient.enabled=true)
    url=(--set-string "authWorkloadClient.exchangeURL=https://auth-service.acr-test.svc")
    iss=(--set-string "authWorkloadClient.issuer=https://auth.example.test")
    negative auth-workload-missing-exchange-url "authWorkloadClient.exchangeURL" "${on[@]}" "${iss[@]}"
    negative auth-workload-missing-issuer "authWorkloadClient.issuer" "${on[@]}" "${url[@]}"
    negative auth-workload-audience-is-no-value "authWorkloadClient" "${on[@]}" "${url[@]}" "${iss[@]}" \
      --set-string "authWorkloadClient.audience=dev-health-api"
    negative auth-workload-expiration-is-no-value "authWorkloadClient" "${on[@]}" "${url[@]}" "${iss[@]}" \
      --set authWorkloadClient.expirationSeconds=3600
    printf 'RESULT: scenario auth-workload-client-bad-inputs passed\n'
    exit 0 ;;
  auth-workload-client-presence-only)
    printf 'scenario: auth-workload-client-presence-only (the chart checks presence only: a malformed URL renders; acr-api refuses it at start)\n'
    n=0
    while IFS=$'\t' read -r field plain value verdict note; do
      [[ -z "$field" || "$field" == \#* ]] && continue
      [[ "$verdict" == refuse ]] || continue
      n=$((n + 1))
      jvalue="$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$value")"
      if [[ "$field" == exchange ]]; then
        args=(--set-json "authWorkloadClient.exchangeURL=$jvalue" --set-string "authWorkloadClient.issuer=https://auth.example.test")
      else
        args=(--set-string "authWorkloadClient.exchangeURL=https://auth-service.acr-test.svc" --set-json "authWorkloadClient.issuer=$jvalue")
      fi
      render --set authWorkloadClient.enabled=true --set "authWorkloadClient.allowPlainHTTP=$plain" "${args[@]}" >"$workdir/presence-only.yaml" 2>"$workdir/presence-only.err" \
        || { printf '  FAIL %s %q (%s) failed to render, but the chart checks presence only: %s\n' "$field" "$value" "$note" "$(head -c 300 "$workdir/presence-only.err")" >&2; exit 1; }
      grep -q 'ACR_AUTH_WORKLOAD_' "$workdir/presence-only.yaml" || fail_gate "auth-workload presence-only: $field $value rendered without the env"
    done <"$(dirname "${BASH_SOURCE[0]}")/../../internal/config/testdata/auth_workload_urls.tsv"
    [[ $n -gt 50 ]] || fail_gate "auth-workload presence-only: only $n malformed cells read from the corpus"
    pass "auth-workload-client-presence-only: $n malformed values render (the Go validator refuses them at start)"
    printf 'RESULT: scenario auth-workload-client-presence-only passed\n'
    exit 0 ;;
  auth-projection-url)
    printf 'scenario: auth-projection-url (ACR_AUTH_PROJECTION_URL reaches the ConfigMap and the runtime ACL job only when set; it needs the workload client)\n'
    auth_workload_render >"$workdir/projection-off.yaml"
    if grep -q ACR_AUTH_PROJECTION_URL "$workdir/projection-off.yaml"; then fail_gate "auth-projection: the URL is rendered with no projectionURL"; fi
    auth_workload_render --set-string "authWorkloadClient.projectionURL=http://auth-service.acr-test.svc:8080" >"$workdir/projection-on.yaml"
    n="$(grep -c 'ACR_AUTH_PROJECTION_URL' "$workdir/projection-on.yaml")"
    [[ "$n" -eq 2 ]] || fail_gate "auth-projection: ACR_AUTH_PROJECTION_URL rendered $n times, want 2 (the ConfigMap and the runtime ACL job)"
    negative auth-projection-needs-the-client "authWorkloadClient.projectionURL" --set-string "authWorkloadClient.projectionURL=https://auth-service.acr-test.svc"
    # Whitespace is unset, as the runtime config reads it: no render error with the client off, no env with it on, and a
    # padded URL renders trimmed.
    render --set-string "authWorkloadClient.projectionURL=   " >"$workdir/projection-blank-off.yaml" 2>"$workdir/projection-blank-off.err" \
      || fail_gate "auth-projection: a whitespace projectionURL with the client off failed to render: $(head -c 300 "$workdir/projection-blank-off.err")"
    if grep -q ACR_AUTH_PROJECTION_URL "$workdir/projection-blank-off.yaml"; then fail_gate "auth-projection: a whitespace projectionURL rendered the env with the client off"; fi
    auth_workload_render --set-string "authWorkloadClient.projectionURL=   " >"$workdir/projection-blank-on.yaml"
    if grep -q ACR_AUTH_PROJECTION_URL "$workdir/projection-blank-on.yaml"; then fail_gate "auth-projection: a whitespace projectionURL rendered the env with the client on"; fi
    auth_workload_render --set-string "authWorkloadClient.projectionURL=  http://auth-service.acr-test.svc:8080  " >"$workdir/projection-padded.yaml"
    n="$(grep -c 'ACR_AUTH_PROJECTION_URL: "http://auth-service.acr-test.svc:8080"' "$workdir/projection-padded.yaml")"
    [[ "$n" -eq 1 ]] || fail_gate "auth-projection: a padded projectionURL rendered untrimmed or not at all in the ConfigMap ($n)"
    n="$(grep -c 'value: "http://auth-service.acr-test.svc:8080"' "$workdir/projection-padded.yaml")"
    [[ "$n" -ge 1 ]] || fail_gate "auth-projection: a padded projectionURL rendered untrimmed in the runtime ACL job"
    pass "auth-projection-url: on renders the URL twice, off never, whitespace is unset, a padded URL renders trimmed, and the client is required"
    printf 'RESULT: scenario auth-projection-url passed\n'
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

# Gate 6: acr-mcp is opt-in. A render that leaves acrMcp.enabled at its default
# must not reference acr-mcp anywhere; the enabled render is asserted below.
if grep -q 'acr-mcp' "$rendered"; then
  grep -n 'acr-mcp' "$rendered" >&2 || true
  fail_gate "mcp-default-off: rendered output references acr-mcp although acrMcp.enabled is not set"
fi
pass "mcp-default-off: default render contains no acr-mcp workload"

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
  --set-string 'config.entitlement.url=http://ops.dev-health.internal:8091' \
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
for port in ('port: 9000', 'port: 8091', 'port: 8080'):
    if port in migrate: fail('migration policy must not allow non-PostgreSQL dependency '+port)
if 'port: 8091' in api: fail('local API egress must not retain the remote entitlement port')
print('  ok   network-policy: local API permits TCP Postgres/ClickHouse ports and Gateway ingress; migration permits only DNS + TCP Postgres')
PY

# Gate 9: migration ordering via pre-install/pre-upgrade hook.
python3 - "$rendered" <<'PY' || exit 1
import sys
docs = open(sys.argv[1]).read().split('\n---\n')
job_hook = False
deploy_no_hook = True
for d in docs:
    # codex round r4 P3: comments are not configuration -- match only
    # non-comment lines so a commented-out command cannot satisfy the gate.
    d = "\n".join(l for l in d.splitlines() if not l.lstrip().startswith('#'))
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

# Gate 9b (CHAOS-6277, codex round cf-6277-r1 P3): the generic Gate 9 check
# above passes as long as ANY Job carries a pre-install,pre-upgrade hook --
# it would still pass if runtime-acl-job.yaml were deleted entirely, since
# the migration Job alone satisfies it. This gate specifically requires the
# runtime-acl Job to exist, run acr-migrate grant-runtime-acl, and be
# ordered strictly after the migration Job (a more negative hook-weight
# means it runs FIRST, so migration-weight < runtime-acl-weight is the
# correct order).
python3 - "$rendered" <<'PY' || exit 1
import sys
docs = open(sys.argv[1]).read().split('\n---\n')
def weight(d):
    for line in d.splitlines():
        if 'helm.sh/hook-weight' in line:
            return int(line.split(':')[-1].strip().strip('"'))
    return None
# codex round cf-6277-r3 P3: hook-weight only orders Jobs WITHIN the same
# hook phase -- Helm runs every pre-install,pre-upgrade hook (by weight),
# THEN every post-install,post-upgrade hook (by weight), as two separate
# passes. A weight comparison alone would pass even if the runtime-acl Job
# were moved to post-install,post-upgrade (it would then run AFTER the
# Deployment is already live, not "after migration" in any meaningful
# sense) -- reproduced by the reviewer by changing only the hook phase.
# Require the exact phase string on both Jobs, not just a relative weight.
REQUIRED_PHASE = '"helm.sh/hook": pre-install,pre-upgrade'
migration_weight = runtime_acl_weight = None
runtime_acl_command_ok = migration_phase_ok = runtime_acl_phase_ok = False
for d in docs:
    # codex round r4 P3: comments are not configuration.
    d = "\n".join(l for l in d.splitlines() if not l.lstrip().startswith('#'))
    is_job = '\nkind: Job' in ('\n'+d) or d.lstrip().startswith('kind: Job')
    if not is_job:
        continue
    if 'component: migration' in d and 'grant-runtime-acl' not in d:
        migration_weight = weight(d)
        migration_phase_ok = REQUIRED_PHASE in d
    if 'grant-runtime-acl' in d:
        runtime_acl_weight = weight(d)
        runtime_acl_phase_ok = REQUIRED_PHASE in d
        # codex round cf-6277-r2 P3: a bare 'grant-runtime-acl' in d
        # substring-matches ANY command, including a bogus binary path --
        # the actual rendered command line is
        # command: ["/usr/local/bin/acr-migrate", "grant-runtime-acl"],
        # so require that EXACT command array, not just the two strings
        # appearing anywhere in the document.
        runtime_acl_command_ok = 'command: ["/usr/local/bin/acr-migrate", "grant-runtime-acl"]' in d
if runtime_acl_weight is None:
    print("  FAIL runtime-acl-order: no Job runs acr-migrate grant-runtime-acl", file=sys.stderr); sys.exit(1)
if not runtime_acl_command_ok:
    print("  FAIL runtime-acl-order: the grant-runtime-acl Job's command is not exactly [\"/usr/local/bin/acr-migrate\", \"grant-runtime-acl\"]", file=sys.stderr); sys.exit(1)
if migration_weight is None:
    print("  FAIL runtime-acl-order: no migration Job found to order against", file=sys.stderr); sys.exit(1)
if not migration_phase_ok or not runtime_acl_phase_ok:
    print(f"  FAIL runtime-acl-order: both the migration Job and the runtime-acl Job must carry the exact hook {REQUIRED_PHASE!r} (migration_ok={migration_phase_ok} runtime_acl_ok={runtime_acl_phase_ok})", file=sys.stderr); sys.exit(1)
if not (migration_weight < runtime_acl_weight):
    print(f"  FAIL runtime-acl-order: migration Job weight ({migration_weight}) must be MORE NEGATIVE than the runtime-acl Job weight ({runtime_acl_weight}) so migration runs first", file=sys.stderr); sys.exit(1)
print(f"  ok   runtime-acl-order: grant-runtime-acl Job present, runs acr-migrate, ordered after migration (weights {migration_weight} < {runtime_acl_weight})")
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
[[ "$ent_url" == "http://ops.dev-health.internal:8091" ]] \
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

# CHAOS-12416: acr-api always declares the vocabulary labels flag: off by
# default, because a consumer on an old pin fails every answer that holds the
# map, and on only when the value is set.
vocabulary_labels_value() {
  extract_container_block acr-api | grep -A1 'name: ACR_CONTEXT_FABRIC_VOCABULARY_LABELS_ENABLED' | sed -n 's/^ *value: //p'
}
[[ "$(vocabulary_labels_value <"$rendered")" == '"false"' ]] || fail_gate "vocabulary-labels: acr-api does not declare the flag off by default"
[[ "$(render --set contextFabric.vocabularyLabelsEnabled=true | vocabulary_labels_value)" == '"true"' ]] || fail_gate "vocabulary-labels: contextFabric.vocabularyLabelsEnabled=true does not reach acr-api"
pass "vocabulary-labels: acr-api declares the flag off by default and on only when set"

# CHAOS-12472: Ask Dev's Stop needs the run lease. acr-api declares it off by
# default, on only when set, and acr-api refuses to start with the lease on and
# the event stream off, so the render refuses that first.
run_lease_value() {
  extract_container_block acr-api | grep -A1 'name: ACR_CONTEXT_FABRIC_RUN_LEASE_ENABLED' | sed -n 's/^ *value: //p'
}
[[ "$(run_lease_value <"$rendered")" == '"false"' ]] || fail_gate "run-lease: acr-api does not declare the flag off by default"
run_lease_on=(--set contextFabric.runLeaseEnabled=true --set-string 'deployment.extraEnv[0].name=ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED')
for stream_on in true TRUE True t T 1; do
  [[ "$(render "${run_lease_on[@]}" --set-string "deployment.extraEnv[0].value=${stream_on}" | run_lease_value)" == '"true"' ]] || fail_gate "run-lease: contextFabric.runLeaseEnabled=true with the event stream ${stream_on} did not reach acr-api"
done
for run_lease_refused in "--set contextFabric.runLeaseEnabled=true" "${run_lease_on[*]} --set-string deployment.extraEnv[0].value=false" \
  "--set contextFabric.runLeaseEnabled=true --set-string deployment.extraEnv[0].name=ACR_OTHER_FLAG --set-string deployment.extraEnv[0].value=true" \
  "${run_lease_on[*]} --set-string deployment.extraEnv[0].value=true --set-string deployment.extraEnv[1].name=ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED --set-string deployment.extraEnv[1].value=false"; do
  set +e
  # shellcheck disable=SC2086  # each case is a list of helm flags
  run_lease_out="$(render $run_lease_refused 2>&1)"; run_lease_status=$?
  set -e
  [[ $run_lease_status -ne 0 ]] || fail_gate "run-lease: rendered with the event stream off: ${run_lease_refused}"
  grep -qF 'needs ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED' <<<"$run_lease_out" || fail_gate "run-lease: refused without naming the event stream: ${run_lease_refused}"
done
pass "run-lease: acr-api declares the flag off by default, on when set, and the render refuses it with the event stream off"

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
  # Note: doc is cleared before exit -- awk runs END on exit, and END calls
  # flush() again, which would otherwise emit the matched document twice.
  awk -v kind="$1" -v want="$2" -v veto="${3:-}" '
    function flush() {
      if (doc ~ "(^|\n)kind: "kind"\n" && doc ~ want && (veto == "" || doc !~ veto)) {
        printf "%s", doc; doc = ""; exit
      }
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
falkordb_probe() { # $1 probe name, $2 field -> its value in the StatefulSet render
  awk -v probe="$1:" -v field="$2:" '
    $1 == probe { inprobe = 1; next }
    inprobe && $1 ~ /Probe:$/ { inprobe = 0 }
    inprobe && $1 == field { print $2; exit }
  ' <<<"$falkordb_sts"
}
falkordb_probe_command() { # $1 probe name -> its exec command line in the StatefulSet render
  awk -v probe="$1:" '
    $1 == probe { inprobe = 1; next }
    inprobe && $1 ~ /Probe:$/ { inprobe = 0 }
    inprobe && $1 == "command:" { sub(/^[[:space:]]*command:[[:space:]]*/, ""); print; exit }
  ' <<<"$falkordb_sts"
}
for probe in startupProbe readinessProbe; do
  [[ "$(falkordb_probe_command "$probe")" == '["redis-cli", "-e", "GRAPH.QUERY", "_acr_health", "RETURN 1"]' ]] \
    || fail_gate "falkordb-workload: $probe must run redis-cli -e GRAPH.QUERY so an error reply (LOADING) fails it"
done
[[ "$(falkordb_probe startupProbe failureThreshold)" == "180" && "$(falkordb_probe startupProbe periodSeconds)" == "10" ]] \
  || fail_gate "falkordb-workload: startupProbe must allow a 30 min load (failureThreshold 180 x periodSeconds 10)"
[[ "$(falkordb_probe livenessProbe timeoutSeconds)" == "10" && "$(falkordb_probe livenessProbe periodSeconds)" == "10" && "$(falkordb_probe livenessProbe failureThreshold)" == "6" ]] \
  || fail_gate "falkordb-workload: livenessProbe must be timeoutSeconds 10, periodSeconds 10, failureThreshold 6"
grep -qE '^\s+mountPath: /var/lib/falkordb/data\s*$' <<<"$falkordb_sts" \
  || fail_gate "falkordb-workload: data volume must mount the image FALKORDB_DATA_PATH"
grep -qE '^\s+volumeClaimTemplates:' <<<"$falkordb_sts" \
  || fail_gate "falkordb-workload: default persistence must render volumeClaimTemplates"

falkordb_svc="$(extract_falkordb_doc Service)"
[[ -n "$falkordb_svc" ]] || fail_gate "falkordb-workload: enabled render is missing the falkordb Service"
# Name and port are derived from the render, not hard-coded: the harness
# accepts arbitrary values files, and fullnameOverride/service.port are valid
# operator inputs. What the gate owns is the shape: a -falkordb Service on
# the falkordb component, and (below) the NetworkPolicy pinned to the SAME
# port the Service exposes. The 6379 compose-parity default itself lives in
# values.yaml and is exercised by the shipped values files.
grep -qE '^  name: \S+-falkordb\s*$' <<<"$falkordb_svc" || fail_gate "falkordb-workload: Service name must be <fullname>-falkordb"
falkordb_svc_port="$(grep -E '^\s+port: [0-9]+\s*$' <<<"$falkordb_svc" | head -1 | awk '{print $2}')"
[[ "$falkordb_svc_port" =~ ^[0-9]+$ ]] || fail_gate "falkordb-workload: Service must expose a numeric port"
grep -qE '^\s+app.kubernetes.io/component: falkordb\s*$' <<<"$falkordb_svc" \
  || fail_gate "falkordb-workload: Service selector must target the falkordb component"

falkordb_np="$(extract_falkordb_doc NetworkPolicy)"
[[ -n "$falkordb_np" ]] || fail_gate "falkordb-workload: no NetworkPolicy selects the falkordb component"
grep -qE "^\s+port: ${falkordb_svc_port}\s*\$" <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy ingress port must equal the Service port ($falkordb_svc_port)"
grep -qE '^\s+app.kubernetes.io/component: api\s*$' <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy ingress must admit the api component"
grep -qE '^\s+app.kubernetes.io/component: projector\s*$' <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy ingress must admit the projector component"
if grep -qE 'component: migration' <<<"$falkordb_np"; then
  fail_gate "falkordb-workload: the migration Job must not be in the falkordb trust set"
fi
grep -qE '^\s+egress: \[\]\s*$' <<<"$falkordb_np" \
  || fail_gate "falkordb-workload: falkordb NetworkPolicy must deny all egress (egress: [])"

# The falkor egress rule must appear on the API policy when addr is set:
# structural check (one additional egress port stanza vs the default render's
# API policy, where addr is unset), so a values-file falkorPort override
# cannot false-fail the gate.
falkordb_api_np="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$falkordb_render")"
default_api_np="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <"$rendered")"
falkor_egress_stanzas="$(grep -cE '^\s+- ports:\s*$' <<<"$falkordb_api_np" || true)"
default_egress_stanzas="$(grep -cE '^\s+- ports:\s*$' <<<"$default_api_np" || true)"
(( falkor_egress_stanzas == default_egress_stanzas + 1 )) \
  || fail_gate "falkordb-workload: setting contextFabric.falkor.addr must add exactly one API egress rule (got $falkor_egress_stanzas vs $default_egress_stanzas)"

# S1a: the ops query service egress rule is API-policy only and gated on
# networkPolicy.egress.queryInternalPort (0 renders nothing).
qi_on="$(render --set networkPolicy.egress.queryInternalPort=8095)"
qi_off="$(render --set networkPolicy.egress.queryInternalPort=0)"
qi_on_api="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$qi_on")"
qi_off_api="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$qi_off")"
grep -Pzq 'protocol: TCP\n\s+port: 8095\n' <<<"$qi_on_api" \
  || fail_gate "query-egress: queryInternalPort=8095 must add a TCP 8095 egress rule to the API policy"
if grep -qE 'port: 8095' <<<"$qi_off_api"; then
  fail_gate "query-egress: queryInternalPort=0 must render no 8095 egress rule"
fi
qi_on_count="$(grep -cE '^\s+- ports:\s*$' <<<"$qi_on_api" || true)"
qi_off_count="$(grep -cE '^\s+- ports:\s*$' <<<"$qi_off_api" || true)"
(( qi_on_count == qi_off_count + 1 )) \
  || fail_gate "query-egress: queryInternalPort must add exactly one API egress rule (got $qi_on_count vs $qi_off_count)"
pass "query-egress: API egress rule is gated on queryInternalPort"

# clickhouseExtraPorts: one port-only rule per entry, API policy only, default none; 9000 stays.
ch_x="$(render --set 'networkPolicy.egress.clickhouseExtraPorts={9440}')"
ch_x_api="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$ch_x")"
grep -Pzq 'protocol: TCP\n\s+port: 9000\n' <<<"$ch_x_api" \
  || fail_gate "clickhouse-extra-ports: the clickhousePort 9000 rule must stay"
grep -Pzq 'protocol: TCP\n\s+port: 9440\n' <<<"$ch_x_api" \
  || fail_gate "clickhouse-extra-ports: clickhouseExtraPorts=[9440] must add a TCP 9440 egress rule"
if grep -qE 'port: 9440' <<<"$default_api_np"; then
  fail_gate "clickhouse-extra-ports: the default render must carry no 9440 egress rule"
fi
ch_x_count="$(grep -cE '^\s+- ports:\s*$' <<<"$ch_x_api" || true)"
ch_d_count="$(grep -cE '^\s+- ports:\s*$' <<<"$default_api_np" || true)"
(( ch_x_count == ch_d_count + 1 )) \
  || fail_gate "clickhouse-extra-ports: one extra port must add exactly one API egress rule (got $ch_x_count vs $ch_d_count)"
if render --set 'networkPolicy.egress.clickhouseExtraPorts={0}' >/dev/null 2>&1; then
  fail_gate "clickhouse-extra-ports: port 0 must be refused by the schema"
fi
pass "clickhouse-extra-ports: extra ClickHouse egress ports render one rule each"

# CHAOS-7075: GWC's MCP listener egress rule (graphql_query) is API-policy
# only, default off (queryMcpPort 0), and one rule when set.
qm_on="$(render --set networkPolicy.egress.queryMcpPort=8092)"
qm_on_api="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$qm_on")"
grep -Pzq 'protocol: TCP\n\s+port: 8092\n' <<<"$qm_on_api" \
  || fail_gate "query-mcp-egress: queryMcpPort=8092 must add a TCP 8092 egress rule to the API policy"
if grep -qE 'port: 8092' <<<"$default_api_np"; then
  fail_gate "query-mcp-egress: the default render (queryMcpPort 0) must carry no 8092 egress rule"
fi
qm_on_count="$(grep -cE '^\s+- ports:\s*$' <<<"$qm_on_api" || true)"
qm_off_count="$(grep -cE '^\s+- ports:\s*$' <<<"$default_api_np" || true)"
(( qm_on_count == qm_off_count + 1 )) \
  || fail_gate "query-mcp-egress: queryMcpPort must add exactly one API egress rule (got $qm_on_count vs $qm_off_count)"
grep -q 'ACR_DATA_GRAPHQL_URL' <<<"$(render --set config.dataGraphqlUrl=http://mcp.example:8092)" \
  || fail_gate "query-mcp-egress: config.dataGraphqlUrl must render ACR_DATA_GRAPHQL_URL"
if grep -q 'ACR_DATA_GRAPHQL_URL' "$rendered"; then
  fail_gate "query-mcp-egress: the default render must not set ACR_DATA_GRAPHQL_URL"
fi
qm_sel="$(render --set networkPolicy.egress.queryMcpPort=8092 --set-string networkPolicy.egress.queryMcpPodSelector.app\\.kubernetes\\.io/name=ops-query)"
qm_sel_api="$(extract_doc NetworkPolicy 'component: api' 'component: falkordb' <<<"$qm_sel")"
grep -Pzq 'to:\n\s+- podSelector:\n\s+matchLabels:\n\s+app.kubernetes.io/name: ops-query\n\s+ports:\n\s+- protocol: TCP\n\s+port: 8092\n' <<<"$qm_sel_api" \
  || fail_gate "query-mcp-egress: queryMcpPodSelector must narrow the 8092 rule to those pods"
if grep -Pzq 'port: 8092\n\s+to:' <<<"$qm_on_api"; then
  fail_gate "query-mcp-egress: empty queryMcpPodSelector must stay port-only"
fi
pass "query-mcp-egress: API egress rule and URL are gated on queryMcpPort / dataGraphqlUrl"

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

# Gate 13: hosted acr-mcp workload. Enabled, it is its own Deployment from its
# own digest-pinned image, restricted, probed on /healthz and /readyz, and holds
# NO credential: no Secret mounted or read into its environment, no mounted
# token, no ACR_API_TOKEN, no service-account secrets. The only Secret
# reference it may carry is the chart-wide imagePullSecrets (registry auth). acr-api gains ingress from it and nothing else changes.
mcp_image="registry.example/acr-mcp@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
mcp_render="$(render \
  --set acrMcp.enabled=true \
  --set-string "acrMcp.image.reference=${mcp_image}" \
  --set acrMcp.gateway.enabled=true \
  --set-json 'acrMcp.gateway.httpRoute.parentRefs=[{"name":"acr-gateway","namespace":"gateway-system","sectionName":"https"}]' \
  --set-json 'acrMcp.gateway.httpRoute.hostnames=["mcp.dev-health.internal"]')"
# Select by resource NAME: the acr-api NetworkPolicy also carries a `component: mcp`
# label (its ingress from the acr-mcp pods), so a label match alone is ambiguous.
extract_mcp_doc() { extract_doc "$1" '  name: "?[^\n"]*-mcp"?(-config)?\n' <<<"$mcp_render"; }

mcp_deploy="$(extract_mcp_doc Deployment)"
[[ -n "$mcp_deploy" ]] || fail_gate "acr-mcp: enabled render is missing the acr-mcp Deployment"
grep -qF "image: \"${mcp_image}\"" <<<"$mcp_deploy" || fail_gate "acr-mcp: Deployment does not run acrMcp.image.reference"
grep -qF 'args: ["serve"]' <<<"$mcp_deploy" || fail_gate "acr-mcp: Deployment must run the serve command"
grep -qE '^\s+path: /healthz\s*$' <<<"$mcp_deploy" || fail_gate "acr-mcp: liveness probe must use /healthz"
grep -qE '^\s+path: /readyz\s*$' <<<"$mcp_deploy" || fail_gate "acr-mcp: readiness probe must use /readyz"
# The startup probe must not depend on acr-api (/readyz does): a slow acr-api
# at install would restart the pod. Readiness keeps /readyz.
mcp_probe_path() { awk -v p="$1" '$1 == p":"{f=1;next} f&&$1=="path:"{print $2;exit}' <<<"$mcp_deploy"; }
[[ "$(mcp_probe_path startupProbe)" == "/healthz" ]] || fail_gate "acr-mcp: startup probe must use /healthz"
[[ "$(mcp_probe_path readinessProbe)" == "/readyz" ]] || fail_gate "acr-mcp: readiness probe must stay on /readyz"
grep -qE '^\s+containerPort: 8081\s*$' <<<"$mcp_deploy" || fail_gate "acr-mcp: container must listen on 8081"
grep -qF 'automountServiceAccountToken: false' <<<"$mcp_deploy" || fail_gate "acr-mcp: pod must not mount a service-account token"
if grep -qE 'secretKeyRef|secretName|secretRef|projected:|ACR_API_TOKEN|serviceAccountToken' <<<"$mcp_deploy"; then
  fail_gate "acr-mcp: Deployment mounts a Secret, projected token, or ACR_API_TOKEN; the pod must hold no credential"
fi
# The only Secret name the pod may reference is one of the chart's imagePullSecrets.
mcp_pull_names="$(awk '/^      imagePullSecrets:/{f=1;next} f&&/^        - name: /{print $3;next} f{f=0}' <<<"$mcp_deploy")"
chart_pull_names="$(awk '/^      imagePullSecrets:/{f=1;next} f&&/^        - name: /{print $3;next} f{f=0}' <<<"$(extract_doc Deployment 'component: api' <<<"$mcp_render")")"
[[ "$mcp_pull_names" == "$chart_pull_names" ]] || fail_gate "acr-mcp: pod imagePullSecrets ($mcp_pull_names) must equal the chart-wide list ($chart_pull_names) and nothing else"
assert_restricted_container acr-mcp <(printf '%s\n' "$mcp_deploy")
mcp_sa="$(extract_mcp_doc ServiceAccount)"
[[ -n "$mcp_sa" ]] || fail_gate "acr-mcp: enabled render is missing the acr-mcp ServiceAccount"
grep -qF 'automountServiceAccountToken: false' <<<"$mcp_sa" || fail_gate "acr-mcp: ServiceAccount must not automount a token"
if grep -qE '^(secrets|imagePullSecrets):' <<<"$mcp_sa"; then fail_gate "acr-mcp: ServiceAccount must carry no secrets"; fi
mcp_cm="$(extract_mcp_doc ConfigMap)"
for token in 'ACR_MCP_TRANSPORT: "http"' 'ACR_MCP_HTTP_BASE_PATH: "/mcp"' 'ACR_API_URL: "http://' 'ACR_API_ALLOW_INSECURE_INTERNAL_HTTP: "true"'; do
  grep -qF "$token" <<<"$mcp_cm" || fail_gate "acr-mcp: ConfigMap is missing $token"
done
if grep -qE 'TOKEN|PASSWORD|SECRET|DSN' <<<"$mcp_cm"; then fail_gate "acr-mcp: ConfigMap carries a credential-shaped key"; fi
# CHAOS-7196: the edge failure gate resolves the same limit inputs as acr-api.
# The ConfigMap mirrors EXACTLY the gate inputs (ACR_REQUESTS_PER_MINUTE,
# ACR_LIMIT_WINDOW and the ACR_AUTH_* limit settings) and only as literals,
# with a deployment.extraEnv literal winning as it does for acr-api: no proxy
# trust list (acr-mcp has its own), no unrelated setting, and no other
# ACR_AUTH_* name (a secret-shaped one must never land in this ConfigMap).
grep -qF 'ACR_REQUESTS_PER_MINUTE: "' <<<"$mcp_cm" || fail_gate "acr-mcp: ConfigMap is missing ACR_REQUESTS_PER_MINUTE"
mirror_render="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" \
  --set-json 'deployment.extraEnv=[{"name":"ACR_TRUSTED_PROXY_CIDRS","value":"10.42.0.0/24"},{"name":"ACR_POSTGRES_MAX_OPEN_CONNS","value":"40"},{"name":"ACR_AUTH_FAILURES_PER_WINDOW","value":"7"},{"name":"ACR_LIMIT_WINDOW","value":"2m"},{"name":"ACR_REQUESTS_PER_MINUTE","value":"5"},{"name":"ACR_AUTH_PRIVATE_KEY","value":"PLACEHOLDER_NOT_A_SECRET"},{"name":"ACR_AUTH_FROM_SECRET","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]')"
mirror_cm="$(extract_doc ConfigMap '  name: [^\n]*-mcp-config\n' <<<"$mirror_render")"
[[ -n "$mirror_cm" ]] || fail_gate "acr-mcp: extraEnv mirror render is missing the acr-mcp ConfigMap"
for token in 'ACR_AUTH_FAILURES_PER_WINDOW: "7"' 'ACR_LIMIT_WINDOW: "2m"' 'ACR_REQUESTS_PER_MINUTE: "5"'; do
  grep -qF "$token" <<<"$mirror_cm" || fail_gate "acr-mcp: ConfigMap does not mirror $token from deployment.extraEnv"
done
if [[ "$(grep -c 'ACR_REQUESTS_PER_MINUTE' <<<"$mirror_cm")" != 1 ]]; then fail_gate "acr-mcp: ConfigMap carries ACR_REQUESTS_PER_MINUTE more than once"; fi
for absent in ACR_TRUSTED_PROXY_CIDRS ACR_POSTGRES_MAX_OPEN_CONNS ACR_AUTH_PRIVATE_KEY PLACEHOLDER_NOT_A_SECRET ACR_AUTH_FROM_SECRET; do
  if grep -qF "$absent" <<<"$mirror_cm"; then fail_gate "acr-mcp: ConfigMap mirrored $absent from deployment.extraEnv (only the exact failure-gate inputs may be mirrored)"; fi
done
pass "acr-mcp: ConfigMap mirrors exactly the failure-gate inputs of deployment.extraEnv (override wins, nothing else copied)"
# Every mirrored name: present positively, and each unusable shape fails closed.
for gate_name in ACR_REQUESTS_PER_MINUTE ACR_LIMIT_WINDOW ACR_AUTH_LIMIT_WINDOW ACR_AUTH_FAILURES_PER_WINDOW ACR_AUTH_MAX_TRACKED_KEYS ACR_AUTH_MAX_IN_FLIGHT; do
  each_render="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" \
    --set-json "deployment.extraEnv=[{\"name\":\"${gate_name}\",\"value\":\"9\"}]")"
  each_cm="$(extract_doc ConfigMap '  name: [^\n]*-mcp-config\n' <<<"$each_render")"
  grep -qF "${gate_name}: \"9\"" <<<"$each_cm" || fail_gate "acr-mcp: ConfigMap does not mirror ${gate_name} from deployment.extraEnv"
  for shape in \
    '"valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}' \
    '"value":"","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}' \
    '"value":7'; do
    set +e
    shape_out="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" \
      --set-json "deployment.extraEnv=[{\"name\":\"${gate_name}\",${shape}}]" 2>&1)"
    shape_status=$?
    set -e
    if [[ $shape_status -eq 0 ]] || ! grep -qF 'acr-mcp-gate-env' <<<"$shape_out"; then
      fail_gate "acr-mcp: extraEnv ${gate_name} with ${shape} must fail the render naming acr-mcp-gate-env"
    fi
  done
done
pass "acr-mcp: every mirrored gate input is mirrored as a string literal and fails closed on valueFrom / hybrid / non-string"
mcp_svc="$(extract_mcp_doc Service)"
grep -qE '^\s+port: 8081\s*$' <<<"$mcp_svc" || fail_gate "acr-mcp: Service must expose 8081"
mcp_route="$(extract_mcp_doc HTTPRoute)"
grep -qE '^\s+value: "/mcp"\s*$' <<<"$mcp_route" || fail_gate "acr-mcp: HTTPRoute must match the MCP base path"
if grep -qE '^\s+value: "?/"?\s*$|/healthz|/readyz' <<<"$mcp_route"; then fail_gate "acr-mcp: HTTPRoute must route only the MCP base path"; fi
mcp_np="$(extract_mcp_doc NetworkPolicy)"
grep -qE '^\s+port: 8081\s*$' <<<"$mcp_np" || fail_gate "acr-mcp: NetworkPolicy must admit 8081"
# S1a: the query service egress rule (default queryInternalPort 8095) is acr-api only.
if grep -qE 'port: 8095' <<<"$mcp_np"; then fail_gate "query-egress: the acr-mcp policy must not carry the query service egress rule"; fi
# The route targets a Gateway in gateway-system; the policy must admit that namespace
# or the route it renders is unreachable.
grep -qE '^\s+kubernetes.io/metadata.name: "gateway-system"\s*$' <<<"$mcp_np" \
  || fail_gate "acr-mcp: NetworkPolicy ingress must admit the namespace of the HTTPRoute parentRef (gateway-system)"
grep -qE '^\s+app.kubernetes.io/component: api\s*$' <<<"$mcp_np" || fail_gate "acr-mcp: NetworkPolicy egress must reach the acr-api pods"
mcp_api_np="$(extract_doc NetworkPolicy 'component: api' '  name: [^\n]*-mcp\n' <<<"$mcp_render")"
grep -qE '^\s+app.kubernetes.io/component: mcp\s*$' <<<"$mcp_api_np" || fail_gate "acr-mcp: acr-api NetworkPolicy must admit the acr-mcp pods"
# The acr-mcp reference must stay out of the acr-api Deployment and the migration Job.
for pair in 'Deployment|component: api' 'Job|component: migration'; do
  other_doc="$(extract_doc "${pair%%|*}" "${pair#*|}" <<<"$mcp_render")"
  [[ -n "$other_doc" ]] || fail_gate "acr-mcp: ${pair} resource is missing from the enabled render"
  if grep -q 'acr-mcp' <<<"$other_doc"; then
    fail_gate "acr-mcp: the ${pair#*|} ${pair%%|*} references acr-mcp; it must run only as its own workload"
  fi
done
if grep -qE '^\s*kind:\s*(Secret|Gateway)\s*$' <<<"$mcp_render"; then fail_gate "acr-mcp: enabled render created a Secret or Gateway"; fi
pass "acr-mcp: enabled render is its own restricted, credential-less, digest-pinned workload with a base-path-only route"

# With no pull secret configured the pod references no Secret at all.
mcp_public="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" --set-json 'imagePullSecrets=[]')"
mcp_public_deploy="$(extract_doc Deployment '  name: [^\n]*-mcp\n' <<<"$mcp_public")"
[[ -n "$mcp_public_deploy" ]] || fail_gate "acr-mcp: public-image render is missing the acr-mcp Deployment"
if grep -qiE 'secret' <<<"$mcp_public_deploy"; then
  fail_gate "acr-mcp: with no imagePullSecrets the Deployment must reference no Secret at all"
fi
pass "acr-mcp: with no pull secret configured the pod references no Secret"

mcp_single="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" --set acrMcp.replicaCount=1)"
if [[ -n "$(extract_doc PodDisruptionBudget '  name: [^\n]*-mcp\n' <<<"$mcp_single")" ]]; then
  fail_gate "acr-mcp: a single replica must not render a PodDisruptionBudget"
fi
pass "acr-mcp: single-replica render has no PodDisruptionBudget that would block a drain"

# OAuth login: acr-api and the hosted MCP endpoint must agree on issuer and
# resource, and OAuth turns on the existing web-assertion wiring in any
# environment.
oauth_args=(--set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}"
  --set config.oauth.enabled=true --set-string config.oauth.issuer=https://acr.example.test
  --set-string 'config.oauth.resources[0]=https://mcp.example.test/mcp'
  --set-string config.oauth.consentUrl=https://www.example.test/acr/authorize
  --set-string config.webAssertion.issuer=https://web.example.test --set-string config.webAssertion.audience=acr-api
  --set-string config.webAssertion.existingSecret=acr-web-assertion-jwks)
oauth_render="$(render "${oauth_args[@]}" --set-string acrMcp.oauth.resourceUrl=https://mcp.example.test/mcp --set-string acrMcp.oauth.authorizationServer=https://acr.example.test)"
for want in 'ACR_OAUTH_ISSUER: "https://acr.example.test"' 'ACR_OAUTH_RESOURCES: "https://mcp.example.test/mcp"' \
  'ACR_OAUTH_CONSENT_URL: "https://www.example.test/acr/authorize"' \
  'ACR_MCP_RESOURCE_URL: "https://mcp.example.test/mcp"' 'ACR_MCP_AUTHORIZATION_SERVER: "https://acr.example.test"' \
  'ACR_WEB_ASSERTION_JWKS_FILE:' 'secretName: "acr-web-assertion-jwks"'; do
  grep -qF "$want" <<<"$oauth_render" || fail_gate "oauth: render is missing ${want}"
done
pass "oauth: agreeing acr-api and acr-mcp settings render the OAuth and web-assertion wiring"
grep -qF 'ACR_OAUTH_CLIENT_METADATA_DOCUMENTS: "true"' <<<"$oauth_render" || fail_gate "oauth: client ID metadata documents are not on by default"
oauth_cimd_off="$(render "${oauth_args[@]}" --set config.oauth.clientMetadataDocuments=false)"
grep -qF 'ACR_OAUTH_CLIENT_METADATA_DOCUMENTS: "false"' <<<"$oauth_cimd_off" || fail_gate "oauth: config.oauth.clientMetadataDocuments=false does not render ACR_OAUTH_CLIENT_METADATA_DOCUMENTS=false"
pass "oauth: config.oauth.clientMetadataDocuments renders ACR_OAUTH_CLIENT_METADATA_DOCUMENTS (default true)"
oauth_off="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}")"
if grep -qE 'ACR_OAUTH_|ACR_MCP_RESOURCE_URL|ACR_MCP_AUTHORIZATION_SERVER' <<<"$oauth_off"; then
  fail_gate "oauth: the default render must carry no OAuth settings"
fi
pass "oauth: the default render carries no OAuth settings"
# CHAOS-12140: the browser client settings reach acr-api AND the
# grant-runtime-acl Job, which grants the browser family purge's DELETE only
# when it sees one; off (the default, and OAuth on without them) neither does.
browser_doc() { # browser_doc <render> <kind> <name-suffix>: one document
  BROWSER_RENDER="$1" python3 - "$2" "$RELEASE$3" <<'PY'
import os, re, sys
want_kind, want_name = sys.argv[1], sys.argv[2]
for d in os.environ['BROWSER_RENDER'].split('\n---\n'):
    k = re.search(r'(?m)^kind: (\S+)$', d)
    n = re.search(r'(?m)^metadata:\n(?:  .*\n)*?  name: "?([^"\s]+)"?$', d)
    if k and n and k.group(1) == want_kind and n.group(1) == want_name:
        print(d)
PY
}
browser_args=("${oauth_args[@]}" --set-string 'config.oauth.resources[1]=https://acr.example.test'
  --set-string 'config.oauth.browserClients[0]=https://www.example.test/acr/client-metadata.json')
browser_on="$(render "${browser_args[@]}")"
browser_cm="$(browser_doc "$browser_on" ConfigMap -config)"
browser_job="$(browser_doc "$browser_on" Job -grant-runtime-acl)"
grep -qF 'ACR_OAUTH_BROWSER_CLIENTS: "https://www.example.test/acr/client-metadata.json"' <<<"$browser_cm" || fail_gate "browser client: the acr-api ConfigMap lacks ACR_OAUTH_BROWSER_CLIENTS"
grep -qF 'grant-runtime-acl' <<<"$browser_job" || fail_gate "browser client: the grant-runtime-acl Job was not found"
grep -A1 -F -- '- name: ACR_OAUTH_BROWSER_CLIENTS' <<<"$browser_job" | grep -qF 'value: "https://www.example.test/acr/client-metadata.json"' || fail_gate "browser client: the grant-runtime-acl Job lacks ACR_OAUTH_BROWSER_CLIENTS (its browser family purge grant would be off)"
pass "browser client: config.oauth.browserClients renders for acr-api and the grant-runtime-acl Job"
browser_dev="$(render "${browser_args[@]}" --set-string 'config.oauth.devBrowserRedirectUris[0]=http://127.0.0.1:3000/acr/callback')"
[[ "$(grep -c 'ACR_OAUTH_DEV_BROWSER_REDIRECT_URIS' <<<"$browser_dev")" -ge 2 ]] || fail_gate "browser client: devBrowserRedirectUris must render for acr-api and the grant Job"
pass "browser client: config.oauth.devBrowserRedirectUris renders for acr-api and the grant-runtime-acl Job"
if grep -qE 'ACR_OAUTH_BROWSER_CLIENTS|ACR_OAUTH_DEV_BROWSER_REDIRECT_URIS' <<<"$oauth_render$oauth_off"; then
  fail_gate "browser client: a render without browser clients must carry no browser client setting"
fi
pass "browser client: OAuth on or off without browser clients renders no browser client setting"
oauth_must_fail() {
  local name="$1" expect="$2"; shift 2
  local out status
  set +e; out="$(render "$@" 2>&1)"; status=$?; set -e
  [[ $status -ne 0 ]] || fail_gate "oauth: ${name} rendered but must fail closed"
  grep -qF "$expect" <<<"$out" || fail_gate "oauth: ${name} failed without naming ${expect}"
  pass "oauth: ${name} fails closed naming '${expect}'"
}
oauth_must_fail "mcp resource not issued for" "must be listed in config.oauth.resources" "${oauth_args[@]}" \
  --set-string acrMcp.oauth.resourceUrl=https://other.example.test/mcp --set-string acrMcp.oauth.authorizationServer=https://acr.example.test
oauth_must_fail "mcp names another issuer" "must equal config.oauth.issuer" "${oauth_args[@]}" \
  --set-string acrMcp.oauth.resourceUrl=https://mcp.example.test/mcp --set-string acrMcp.oauth.authorizationServer=https://other.example.test
oauth_must_fail "mcp advertises OAuth the api does not serve" "requires config.oauth.enabled" --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" \
  --set-string acrMcp.oauth.resourceUrl=https://mcp.example.test/mcp --set-string acrMcp.oauth.authorizationServer=https://acr.example.test
oauth_must_fail "half-set mcp pair" "are set together" --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" \
  --set-string acrMcp.oauth.resourceUrl=https://mcp.example.test/mcp
oauth_must_fail "oauth without web assertions" "requires config.webAssertion" --set config.oauth.enabled=true --set-string config.webAssertion.existingSecret= \
  --set-string config.oauth.issuer=https://acr.example.test --set-string 'config.oauth.resources[0]=https://mcp.example.test/mcp'
oauth_must_fail "issuer with a path" "must be an https origin" --set config.oauth.enabled=true \
  --set-string config.oauth.issuer=https://acr.example.test/x --set-string 'config.oauth.resources[0]=https://mcp.example.test/mcp'
for bad_consent in "" "https://www.example.test" "https://www.example.test/" "https://www.example.test/acr/authorize?x=1" \
  "https://www.example.test/acr/authorize#f" "http://www.example.test/acr/authorize"; do
  oauth_must_fail "consent url '${bad_consent}'" "config.oauth.consentUrl" "${oauth_args[@]}" --set-string "config.oauth.consentUrl=${bad_consent}"
done

# CHAOS-12476: http is for a loopback host only, the rule of acr-api and acr-mcp
# (internal/config/oauth.go, internal/mcp/http_oauth.go). A local stack with no
# TLS renders; a named host never does, whatever it starts with.
for loopback in localhost 127.0.0.1 '[::1]'; do
  if ! loopback_render="$(render "${oauth_args[@]}" --set-string "config.oauth.issuer=http://${loopback}:8088" \
    --set-string "config.oauth.resources[0]=http://${loopback}:8089/mcp" --set-string "config.oauth.consentUrl=http://${loopback}:3000/acr/authorize" \
    --set-string "acrMcp.oauth.resourceUrl=http://${loopback}:8089/mcp" --set-string "acrMcp.oauth.authorizationServer=http://${loopback}:8088" 2>&1)"; then
    fail_gate "oauth: an http origin on ${loopback} must render: $(tail -1 <<<"$loopback_render")"
  fi
  grep -qF "http://${loopback}:8088" <<<"$loopback_render" || fail_gate "oauth: the render lost the http issuer on ${loopback}"
  grep -qF "http://${loopback}:3000/acr/authorize" <<<"$loopback_render" || fail_gate "oauth: the render lost the http consent url on ${loopback}"
done
if ! loopback_render="$(render "${oauth_args[@]}" --set-string config.oauth.issuer=http://localhost --set-string 'config.oauth.resources[0]=http://localhost' \
  --set-string config.oauth.consentUrl=http://localhost/acr/authorize 2>&1)"; then
  fail_gate "oauth: an http origin with no port must render: $(tail -1 <<<"$loopback_render")"
fi
pass "oauth: an http issuer, resource and consent url render on each loopback host"
for bad_issuer in "http://acr.localhost:8088" "http://localhost.example.test" "http://127.0.0.1.example.test" "http://127a0b0c1" "http://example.test:8088" \
  "http://localhost:8088/x" "http://user@localhost:8088" "http://localhost:port"; do
  oauth_must_fail "http issuer '${bad_issuer}'" "must be an https origin" "${oauth_args[@]}" --set-string "config.oauth.issuer=${bad_issuer}"
done
for bad_resource in "http://acr.localhost/mcp" "http://localhost.example.test/mcp" "http://localhost:8089/mcp?x=1" "http://localhost:8089/mcp#f" \
  "http://user@localhost/mcp"; do
  oauth_must_fail "http resource '${bad_resource}'" "config.oauth.resources entry" "${oauth_args[@]}" --set-string "config.oauth.resources[0]=${bad_resource}"
done
for bad_consent in "http://acr.localhost:3000/acr/authorize" "http://localhost.example.test/acr/authorize" "http://localhost:3000" "http://localhost:3000/" \
  "http://localhost:3000/acr/authorize?x=1" "http://user@localhost:3000/acr/authorize"; do
  oauth_must_fail "http consent url '${bad_consent}'" "config.oauth.consentUrl" "${oauth_args[@]}" --set-string "config.oauth.consentUrl=${bad_consent}"
done

# CHAOS-11952: Ask Dev retention is one switch, default off. CHAOS-12244: the
# job runs as acr-api's runtime role, so the chart renders the switch and
# nothing else: no retention credential, DSN or grant-Job input exists, and
# credentials.retention is refused by the schema.
retention_off="$(render)"
grep -qF 'ACR_ASK_DEV_RETENTION_ENABLED: "false"' <<<"$(browser_doc "$retention_off" ConfigMap -config)" || fail_gate "retention: the default render must set ACR_ASK_DEV_RETENTION_ENABLED false"
retention_on="$(render --set config.askDevRetentionEnabled=true)"
grep -qF 'ACR_ASK_DEV_RETENTION_ENABLED: "true"' <<<"$(browser_doc "$retention_on" ConfigMap -config)" || fail_gate "retention: the switch on must render ACR_ASK_DEV_RETENTION_ENABLED true"
for rendered in "$retention_off" "$retention_on"; do
  if grep -qF 'RETENTION_DSN' <<<"$rendered"; then
    fail_gate "retention: a render carries a retention DSN; the job runs as the runtime role"
  fi
done
pass "retention: the switch renders off and on, with no retention DSN either way"
set +e; retention_cred_out="$(render --set-string credentials.retention.existingSecret=acr-retention-credentials 2>&1)"; retention_cred_status=$?; set -e
[[ $retention_cred_status -ne 0 ]] || fail_gate "retention: credentials.retention rendered but must be refused"
pass "retention: credentials.retention is refused"

# CHAOS-12094 (F1): the acr-mcp hop proof, rolled in two steps. Step 1
# (acrMcp.hop.enabled): acr-mcp mounts ONE projected token of its own
# ServiceAccount for the hop audience (600 s) and names its file, and nothing
# else changes (no Secret, automount still off, acr-api unchanged). Step 2
# (config.mcpHop.required): acr-api requires the hop for the acr-mcp
# ServiceAccount, and gains TokenReview access. Required without step 1, or
# without the OAuth login, fails closed.
hop1="$(render --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" --set acrMcp.hop.enabled=true)"
hop1_mcp="$(extract_doc Deployment '  name: "?[^\n"]*-mcp"?\n' <<<"$hop1")"
[[ -n "$hop1_mcp" ]] || fail_gate "mcp-hop: step 1 render is missing the acr-mcp Deployment"
for want in 'audience: "dev-health-acr-api-mcp-hop"' 'expirationSeconds: 600' 'path: token' 'value: /var/run/secrets/acr-mcp-hop/token' 'automountServiceAccountToken: false'; do
  grep -qF "$want" <<<"$hop1_mcp" || fail_gate "mcp-hop: step 1 acr-mcp Deployment lacks ${want}"
done
[[ "$(grep -c 'serviceAccountToken:' <<<"$hop1_mcp")" == 1 ]] || fail_gate "mcp-hop: step 1 acr-mcp must project exactly one ServiceAccount token"
if grep -qE 'secretKeyRef|secretName|secretRef|ACR_API_TOKEN' <<<"$hop1_mcp"; then fail_gate "mcp-hop: step 1 acr-mcp references a Secret"; fi
if grep -qE 'ACR_MCP_HOP_REQUIRED|-token-review' <<<"$hop1"; then fail_gate "mcp-hop: step 1 changed acr-api (required or TokenReview access)"; fi
pass "mcp-hop: step 1 projects one hop-audience token into acr-mcp and leaves acr-api unchanged"
hop2="$(render "${oauth_args[@]}" --set acrMcp.hop.enabled=true --set config.mcpHop.required=true)"
hop2_cm="$(extract_doc ConfigMap '  name: "?[^\n"]*-config"?\n' '-mcp-config|-migrate-config|-projector-config' <<<"$hop2")"
for want in 'ACR_MCP_HOP_REQUIRED: "true"' 'ACR_MCP_HOP_AUDIENCE: "dev-health-acr-api-mcp-hop"' "ACR_MCP_HOP_SERVICE_ACCOUNT_NAMESPACE: \"${NAMESPACE}\"" "ACR_MCP_HOP_SERVICE_ACCOUNT_NAME: \"${RELEASE}-mcp\""; do
  grep -qF "$want" <<<"$hop2_cm" || fail_gate "mcp-hop: step 2 acr-api ConfigMap lacks ${want}"
done
[[ -n "$(extract_doc ClusterRoleBinding '  name: "?[^\n"]*-token-review"?\n' <<<"$hop2")" ]] || fail_gate "mcp-hop: step 2 renders no TokenReview binding for acr-api"
grep -qF 'kubernetes-api-reviewer-token' <<<"$(extract_doc Deployment 'component: api' <<<"$hop2")" || fail_gate "mcp-hop: step 2 acr-api lacks its reviewer token"
pass "mcp-hop: step 2 requires the hop for the acr-mcp ServiceAccount and grants acr-api TokenReview"
# One name (codex #1799 r1 P2): a custom acrMcp.serviceAccountName is the
# ServiceAccount rendered, the pod's, and the one acr-api accepts.
hop_custom="$(render "${oauth_args[@]}" --set acrMcp.hop.enabled=true --set config.mcpHop.required=true --set-string acrMcp.serviceAccountName=edge-mcp)"
grep -qF 'ACR_MCP_HOP_SERVICE_ACCOUNT_NAME: "edge-mcp"' <<<"$hop_custom" || fail_gate "mcp-hop: acrMcp.serviceAccountName does not set the accepted ServiceAccount"
grep -qF 'serviceAccountName: "edge-mcp"' <<<"$(extract_doc Deployment '  name: "?[^\n"]*-mcp"?\n' <<<"$hop_custom")" || fail_gate "mcp-hop: the acr-mcp pod does not run as acrMcp.serviceAccountName"
grep -qE '^  name: "?edge-mcp"?$' <<<"$(extract_doc ServiceAccount 'name: "?edge-mcp"?' <<<"$hop_custom")" || fail_gate "mcp-hop: the chart does not render the acrMcp.serviceAccountName ServiceAccount"
if grep -qF "${RELEASE}-mcp\"" <<<"$(extract_doc ServiceAccount 'component: mcp' <<<"$hop_custom")"; then fail_gate "mcp-hop: the default acr-mcp ServiceAccount is still rendered beside the custom one"; fi
pass "mcp-hop: acrMcp.serviceAccountName is the rendered ServiceAccount, the pod's, and the accepted hop identity"
oauth_must_fail "mcp hop required before acr-mcp sends it" "requires acrMcp.enabled and acrMcp.hop.enabled" "${oauth_args[@]}" --set config.mcpHop.required=true
oauth_must_fail "mcp hop required without the OAuth login" "requires config.oauth.enabled" --set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}" \
  --set acrMcp.hop.enabled=true --set config.mcpHop.required=true

# Gate: OTLP export. Off by default: no OTEL_ key anywhere and no collector
# egress. On: every workload ConfigMap (api, mcp, projector) carries the three
# keys with its own service name, and both the api and mcp NetworkPolicies
# admit egress to the collector port. On without an endpoint fails closed.
otel_base=(--set acrMcp.enabled=true --set-string "acrMcp.image.reference=${mcp_image}"
  --set contextFabric.projector.enabled=true)
otel_off="$(render "${otel_base[@]}")"
if grep -qE 'OTEL_|port: 4317' <<<"$otel_off"; then fail_gate "otel: the default render must carry no OTEL_ key and no collector egress"; fi
[[ -n "$(extract_doc ConfigMap '  name: [^\n]*-projector-config\n' <<<"$otel_off")" ]] || fail_gate "otel: projector ConfigMap missing from the base render (the off check would be vacuous)"
pass "otel: the default render carries no OTEL_ key and no collector egress"
otel_on="$(render "${otel_base[@]}" --set otel.enabled=true --set-string otel.endpoint=http://10.0.0.151:4317)"
for pair in 'api;  name: [^\n]*-config\n;-(mcp|projector)-config\n;acr-api' 'mcp;  name: [^\n]*-mcp-config\n;;acr-mcp' 'projector;  name: [^\n]*-projector-config\n;;acr-projector'; do
  IFS=';' read -r who selector veto service <<<"$pair"
  cm="$(extract_doc ConfigMap "$selector" "$veto" <<<"$otel_on")"
  [[ -n "$cm" ]] || fail_gate "otel: ${who} ConfigMap missing from the enabled render"
  for want in 'OTEL_ENABLED: "true"' 'OTEL_EXPORTER_OTLP_ENDPOINT: "http://10.0.0.151:4317"' "OTEL_SERVICE_NAME: \"${service}\""; do
    grep -qF "$want" <<<"$cm" || fail_gate "otel: ${who} ConfigMap is missing ${want}"
  done
done
pass "otel: enabled render gives acr-api, acr-mcp and acr-projector the endpoint and their own service names"
for pair in 'api|component: api|  name: [^\n]*-mcp\n' 'mcp|  name: [^\n]*-mcp\n|'; do
  IFS='|' read -r who selector exclude <<<"$pair"
  if [[ -n "$exclude" ]]; then np="$(extract_doc NetworkPolicy "$selector" "$exclude" <<<"$otel_on")"; else np="$(extract_doc NetworkPolicy "$selector" <<<"$otel_on")"; fi
  [[ -n "$np" ]] || fail_gate "otel: ${who} NetworkPolicy missing from the enabled render"
  # Protocol AND port: OTLP/gRPC is TCP, and a port-only assertion passes on a
  # UDP rule that would never carry the export.
  grep -Pzq 'protocol: TCP\n\s+port: 4317\n' <<<"$np" || fail_gate "otel: ${who} NetworkPolicy does not admit TCP egress to the collector port"
done
pass "otel: enabled render admits TCP collector egress from the api and mcp NetworkPolicies"
# The projector exports too. Today no NetworkPolicy selects its pods, so its
# egress is unrestricted; if one is ever added it must carry the collector
# port, or the projector goes dark with the chart still reporting enabled.
otel_projector="$(render "${otel_base[@]}" --set otel.enabled=true --set-string otel.endpoint=http://10.0.0.151:4317 --set networkPolicy.enabled=true)"
projector_np="$(extract_doc NetworkPolicy 'component: projector' <<<"$otel_projector")"
if [[ -n "$projector_np" ]]; then
  grep -Pzq 'protocol: TCP\n\s+port: 4317\n' <<<"$projector_np" || fail_gate "otel: a NetworkPolicy now selects the projector but does not admit TCP egress to the collector port"
  pass "otel: the projector NetworkPolicy admits TCP collector egress"
else
  grep -qF 'OTEL_ENABLED: "true"' <<<"$(extract_doc ConfigMap '  name: [^\n]*-projector-config\n' <<<"$otel_projector")" \
    || fail_gate "otel: projector ConfigMap does not enable export, so the unrestricted-egress claim below pins nothing"
  pass "otel: the projector exports with no NetworkPolicy selecting its pods (egress unrestricted by construction)"
fi
otel_endpoint_must_fail() {
  local name="$1" expect="$2"; shift 2
  local out status
  set +e; out="$(render --set otel.enabled=true "$@" 2>&1)"; status=$?; set -e
  [[ $status -ne 0 ]] || fail_gate "otel: ${name} rendered but must fail closed"
  grep -qF "$expect" <<<"$out" || fail_gate "otel: ${name} failed without naming ${expect}"
  pass "otel: ${name} fails closed naming '${expect}'"
}
otel_endpoint_must_fail "enabled without an endpoint" "otel.endpoint"
otel_endpoint_must_fail "enabled with an empty endpoint" "otel.endpoint" --set-string otel.endpoint=
otel_endpoint_must_fail "enabled with a whitespace endpoint" "/otel/endpoint" --set-string 'otel.endpoint=   '
otel_endpoint_must_fail "enabled with a schemeless endpoint" "/otel/endpoint" --set-string otel.endpoint=10.0.0.151:4317
otel_endpoint_must_fail "enabled with a hostless endpoint" "/otel/endpoint" --set-string otel.endpoint=http://:4317
otel_endpoint_must_fail "enabled with a blank service name" "otel.serviceNames" --set-string otel.endpoint=http://collector:4317 --set-string 'otel.serviceNames.api= '

# Gate: the projector Deployment renders strategy type Recreate with no
# rollingUpdate key (Recreate forbids one; server-side apply cannot clear a
# defaulted block, so the chart must never render it), and acr-api / acr-mcp
# keep their own strategy.
strategy_render="$(render "${otel_base[@]}")"
projector_dep="$(extract_doc Deployment '  name: [^\n]*-projector\n' <<<"$strategy_render")"
[[ -n "$projector_dep" ]] || fail_gate "projector-strategy: projector Deployment missing from the render"
grep -Pzq '  strategy:\n    type: Recreate\n  selector:' <<<"$projector_dep" \
  || fail_gate "projector-strategy: projector Deployment must render strategy type Recreate with no rollingUpdate key"
pass "projector-strategy: projector renders Recreate with no rollingUpdate key"
other_deps="$(awk 'BEGIN{RS="\n---\n"} /\nkind: Deployment\n/ && !/\n  name: [^\n]*-projector\n/ {print}' <<<"$strategy_render")"
[[ -n "$other_deps" ]] || fail_gate "projector-strategy: no non-projector Deployment in the render (the untouched check would be vacuous)"
if grep -q 'type: Recreate' <<<"$other_deps"; then fail_gate "projector-strategy: acr-api/acr-mcp Deployments must not render Recreate"; fi
pass "projector-strategy: acr-api and acr-mcp strategies are untouched"

# Gate: acr-api's trusted proxy networks and CORS origins are chart values
# (config.trustedProxyCidrs, config.cors.allowedOrigins), rendered into the
# acr-api ConfigMap only when set. deployment.extraEnv may not name the same
# variable while the value is set: an env entry silently beats the ConfigMap.
proxy_cors_render="$(render --set-string 'config.trustedProxyCidrs[0]=10.42.0.0/24' --set-string 'config.trustedProxyCidrs[1]=192.0.2.10/32' \
  --set-string 'config.cors.allowedOrigins[0]=https://www.example.test' --set-string 'config.cors.allowedOrigins[1]=http://localhost:3000')"
proxy_cors_cm="$(browser_doc "$proxy_cors_render" ConfigMap -config)"
grep -qF "name: ${RELEASE}-config" <<<"$proxy_cors_cm" || fail_gate "proxy-cors: the acr-api ConfigMap was not found"
grep -qxF '  ACR_TRUSTED_PROXY_CIDRS: "10.42.0.0/24,192.0.2.10/32"' <<<"$proxy_cors_cm" || fail_gate "proxy-cors: config.trustedProxyCidrs does not render ACR_TRUSTED_PROXY_CIDRS in the acr-api ConfigMap"
grep -qxF '  ACR_CORS_ALLOWED_ORIGINS: "https://www.example.test,http://localhost:3000"' <<<"$proxy_cors_cm" || fail_gate "proxy-cors: config.cors.allowedOrigins does not render ACR_CORS_ALLOWED_ORIGINS in the acr-api ConfigMap"
pass "proxy-cors: config.trustedProxyCidrs and config.cors.allowedOrigins render into the acr-api ConfigMap"
proxy_cors_off="$(render)"
if grep -qE 'ACR_TRUSTED_PROXY_CIDRS|ACR_CORS_ALLOWED_ORIGINS' <<<"$proxy_cors_off"; then
  fail_gate "proxy-cors: the render without the values must carry neither variable"
fi
pass "proxy-cors: unset values render neither variable"
proxy_cors_must_fail() {
  local name="$1" expect="$2"; shift 2
  local out status
  set +e; out="$(render "$@" 2>&1)"; status=$?; set -e
  [[ $status -ne 0 ]] || fail_gate "proxy-cors: ${name} rendered but must fail closed"
  grep -qF "$expect" <<<"$out" || fail_gate "proxy-cors: ${name} failed without naming ${expect}"
  pass "proxy-cors: ${name} fails closed naming '${expect}'"
}
proxy_cors_must_fail "proxy CIDRs from both the value and extraEnv" "deployment.extraEnv names ACR_TRUSTED_PROXY_CIDRS" \
  --set-string 'config.trustedProxyCidrs[0]=10.42.0.0/24' \
  --set-string 'deployment.extraEnv[0].name=ACR_TRUSTED_PROXY_CIDRS' --set-string 'deployment.extraEnv[0].value=10.0.0.0/8'
proxy_cors_must_fail "CORS origins from both the value and extraEnv" "deployment.extraEnv names ACR_CORS_ALLOWED_ORIGINS" \
  --set-string 'config.cors.allowedOrigins[0]=https://www.example.test' \
  --set-string 'deployment.extraEnv[0].name=ACR_CORS_ALLOWED_ORIGINS' --set-string 'deployment.extraEnv[0].value=https://other.example.test'
# extraEnv alone (the form environments use today) still renders, unchanged.
proxy_env_only="$(render --set-string 'deployment.extraEnv[0].name=ACR_TRUSTED_PROXY_CIDRS' --set-string 'deployment.extraEnv[0].value=10.42.0.0/24')"
grep -A1 -F -- '- name: ACR_TRUSTED_PROXY_CIDRS' <<<"$proxy_env_only" | grep -qE 'value: "?10\.42\.0\.0/24"?$' || fail_gate "proxy-cors: deployment.extraEnv ACR_TRUSTED_PROXY_CIDRS alone no longer renders"
pass "proxy-cors: deployment.extraEnv alone still renders the variable"

printf 'RESULT: happy path passed all gates\n'
