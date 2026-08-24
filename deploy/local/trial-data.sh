#!/usr/bin/env bash
# Standing trial data-plane driver (CHAOS-4186): postgres + clickhouse +
# falkordb in one namespace on whatever cluster KUBECONFIG points at
# (normally the kiac.sh acr-local cluster -- this reuses that cluster, it
# does not create a second one). Persistent: unlike shard.sh, data survives
# apply/delete of the workloads; only `trial-data.sh wipe` destroys the PVCs.
#
# Usage:
#   deploy/local/trial-data.sh render
#   deploy/local/trial-data.sh apply
#   deploy/local/trial-data.sh wait
#   deploy/local/trial-data.sh dsn
#   deploy/local/trial-data.sh restore-postgres <dump.sql.gz>
#   deploy/local/trial-data.sh restore-clickhouse <zip1> [zip2 ...]
#   deploy/local/trial-data.sh wipe     # deletes the rendered resources
#                              (never namespace-wide); also deletes the
#                              namespace ITSELF only when it is the
#                              hardcoded default acr-trial-data
#
# Also requires `yq` (mikefarah, already used elsewhere in this repo's
# e2e/ci scripts) in addition to kubectl.
#
# Environment:
#   ACR_TRIAL_DATA_NAMESPACE   namespace (default: acr-trial-data)
#   ACR_TRIAL_PG_PASSWORD      postgres/clickhouse shared password (default:
#                              acr-trial-dev; DEV ONLY; [A-Za-z0-9._~-] only)
#   ACR_TRIAL_PG_STORAGE       postgres PVC size (default: 20Gi)
#   ACR_TRIAL_CH_STORAGE       clickhouse data PVC size (default: 30Gi)
#   ACR_TRIAL_CH_BACKUPS_STORAGE  clickhouse backups-staging PVC size (default: 5Gi)
#   ACR_TRIAL_FALKOR_STORAGE   falkordb PVC size (default: 5Gi)
#   ACR_TRIAL_CH_IMAGE         clickhouse image (default: the digest this
#                              script pins, matching the compose stack's
#                              currently-running clickhouse/clickhouse-server
#                              :latest resolution -- parity discipline, not
#                              an arbitrary pin; re-resolve and update the
#                              default if the compose stack's version moves)
#   KUBECONFIG                 cluster to operate on (required for everything
#                              but render; the ambient default is refused)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/templates/trial-data.yaml"

NAMESPACE="${ACR_TRIAL_DATA_NAMESPACE:-acr-trial-data}"
PG_PASSWORD="${ACR_TRIAL_PG_PASSWORD:-acr-trial-dev}"
PG_STORAGE="${ACR_TRIAL_PG_STORAGE:-20Gi}"
CH_STORAGE="${ACR_TRIAL_CH_STORAGE:-30Gi}"
CH_BACKUPS_STORAGE="${ACR_TRIAL_CH_BACKUPS_STORAGE:-5Gi}"
FALKOR_STORAGE="${ACR_TRIAL_FALKOR_STORAGE:-5Gi}"
# Resolved 2026-08-24 from the running compose clickhouse container
# (clickhouse/clickhouse-server:latest -> 26.7.3.19) so the trial data plane
# starts on the SAME version the seed dump/parity-smoke baseline was captured
# under -- compose's :latest can drift later; this pin will not follow it
# silently.
CH_IMAGE="${ACR_TRIAL_CH_IMAGE:-clickhouse/clickhouse-server@sha256:f90a77560f72b10802106ee49e9870e41668cbc496e280c3911f6e3b216657f3}"

# Fixed NodePorts, deliberately outside shard.sh's 31000-32766 budget
# (i<=883 -> 31000+2i / 31001+2i) so a standing data-plane port can never
# collide with a live per-shard pair.
PG_NODEPORT=30500
CH_HTTP_NODEPORT=30501
CH_NATIVE_NODEPORT=30502
FALKOR_NODEPORT=30503

log() { printf '[trial-data.sh] %s\n' "$*" >&2; }
die() { printf '[trial-data.sh] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,38p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 2
}

require_kubeconfig() {
  [[ -n "${KUBECONFIG:-}" && -f "$KUBECONFIG" ]] \
    || die "KUBECONFIG must be set to an existing file (e.g. \$(deploy/local/kiac.sh kubeconfig)); refusing to use the ambient default cluster"
}

validate_password() {
  [[ "$PG_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ ]] \
    || die "ACR_TRIAL_PG_PASSWORD may only contain [A-Za-z0-9._~-] (substituted into YAML and a DSN verbatim)"
}

# validate_namespace (codex xhigh review, P1): a namespace override that
# starts with "-" (e.g. ACR_TRIAL_DATA_NAMESPACE=--all) would be parsed by
# kubectl as a FLAG, not the namespace argument -- `kubectl delete namespace
# --all --ignore-not-found ...` deletes every namespace in whatever cluster
# KUBECONFIG points at, not just this trial's own state. This is a shared
# kiac cluster (also hosts acr-pilot) -- fail closed on anything that is not
# a plain, valid Kubernetes namespace name.
validate_namespace() {
  [[ "$NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]] \
    || die "ACR_TRIAL_DATA_NAMESPACE=$NAMESPACE is not a valid Kubernetes namespace name (lowercase alphanumeric and '-', must not start with '-') -- refusing, this is a shared cluster"
}
validate_namespace

render() {
  validate_password
  sed \
    -e "s|__NAMESPACE__|$NAMESPACE|g" \
    -e "s|__PG_PASSWORD__|$PG_PASSWORD|g" \
    -e "s|__PG_STORAGE__|$PG_STORAGE|g" \
    -e "s|__CH_STORAGE__|$CH_STORAGE|g" \
    -e "s|__CH_BACKUPS_STORAGE__|$CH_BACKUPS_STORAGE|g" \
    -e "s|__FALKOR_STORAGE__|$FALKOR_STORAGE|g" \
    -e "s|__CH_IMAGE__|$CH_IMAGE|g" \
    -e "s|__PG_NODEPORT__|$PG_NODEPORT|g" \
    -e "s|__CH_HTTP_NODEPORT__|$CH_HTTP_NODEPORT|g" \
    -e "s|__CH_NATIVE_NODEPORT__|$CH_NATIVE_NODEPORT|g" \
    -e "s|__FALKOR_NODEPORT__|$FALKOR_NODEPORT|g" \
    "$TEMPLATE"
}

node_ip() {
  kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}'
}

cmd_render() { render; }

cmd_apply() {
  require_kubeconfig
  render | kubectl apply -f -
  log "trial data plane applied to namespace $NAMESPACE"
}

cmd_wait() {
  require_kubeconfig
  kubectl -n "$NAMESPACE" rollout status deployment/trial-postgres --timeout=180s
  kubectl -n "$NAMESPACE" rollout status deployment/trial-clickhouse --timeout=180s
  kubectl -n "$NAMESPACE" rollout status deployment/trial-falkordb --timeout=180s
  log "trial data plane ready: postgres, clickhouse, falkordb rolled out in $NAMESPACE"
}

cmd_dsn() {
  require_kubeconfig
  validate_password
  local ip password
  ip="$(node_ip)"
  [[ -n "$ip" ]] || die "could not resolve a node InternalIP from KUBECONFIG"
  # Cluster secret is the credential source of truth (team-lead design
  # ruling, CHAOS-4186 round 3), not the local ACR_TRIAL_PG_PASSWORD env --
  # that only reflects what the operator's CURRENT shell happens to hold,
  # which can silently diverge from what the running postgres/clickhouse
  # pods were actually seeded with (e.g. changed after apply, before a
  # matching restore-postgres). Reading the live secret means this
  # command's output always matches what is actually running, not intent.
  password="$(kubectl -n "$NAMESPACE" get secret trial-postgres -o jsonpath='{.data.POSTGRES_PASSWORD}' 2>/dev/null | base64 -d)"
  [[ -n "$password" ]] || die "could not read secret trial-postgres/POSTGRES_PASSWORD in namespace $NAMESPACE -- has 'apply' been run?"
  printf 'ACR_TEST_TRIAL_POSTGRES_DSN=postgres://devhealth:%s@%s:%d/acr?sslmode=disable\n' "$password" "$ip" "$PG_NODEPORT"
  printf 'ACR_TEST_TRIAL_CLICKHOUSE_DSN=clickhouse://ch:%s@%s:%d/default\n' "$password" "$ip" "$CH_NATIVE_NODEPORT"
  printf 'ACR_TEST_TRIAL_FALKOR_ADDR=%s:%d\n' "$ip" "$FALKOR_NODEPORT"
  printf '# clickhouse HTTP (for RESTORE/BACKUP admin queries): http://%s:%d\n' "$ip" "$CH_HTTP_NODEPORT"
  # Rotating the effective password: changing ACR_TRIAL_PG_PASSWORD and
  # re-running `apply` updates the Secret object, but that alone does NOT
  # rotate either running database's actual credential -- postgres only
  # picks up a new devhealth password via restore-postgres's explicit
  # ALTER ROLE step, and clickhouse only reads its `ch` user's password at
  # container start, so its pod must be recreated too. `dsn`'s output
  # above is only ever as current as those two steps.
}

cmd_restore_postgres() {
  require_kubeconfig
  validate_password
  [[ $# -ge 1 ]] || die "usage: trial-data.sh restore-postgres <dump.sql.gz>"
  local dump="$1"
  [[ -f "$dump" ]] || die "not found: $dump"
  local pod
  pod="$(kubectl -n "$NAMESPACE" get pod -l app.kubernetes.io/component=postgres -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "$pod" ]] || die "no postgres pod found in $NAMESPACE"
  log "streaming $dump into $pod (pg_dumpall format -- recreates roles+databases; may take several minutes for a ~100MB dump)"
  gunzip -c "$dump" | kubectl -n "$NAMESPACE" exec -i "$pod" -- psql -U postgres -d postgres -v ON_ERROR_STOP=1
  # The dump's own "ALTER ROLE devhealth ... PASSWORD 'SCRAM-SHA-256$...'"
  # sets whatever password was live in the SOURCE stack at backup time --
  # which this script does not know as plaintext. Pin it to our known
  # ACR_TRIAL_PG_PASSWORD so the DSN this script prints (cmd_dsn) stays
  # correct regardless of what the source stack's credential was.
  kubectl -n "$NAMESPACE" exec "$pod" -- psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
    -c "ALTER ROLE devhealth WITH PASSWORD '$PG_PASSWORD';"
  log "postgres restore complete"
}

cmd_restore_clickhouse() {
  require_kubeconfig
  validate_password
  [[ $# -ge 1 ]] || die "usage: trial-data.sh restore-clickhouse <zip1> [zip2 ...]"
  local pod
  pod="$(kubectl -n "$NAMESPACE" get pod -l app.kubernetes.io/component=clickhouse -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "$pod" ]] || die "no clickhouse pod found in $NAMESPACE"
  local workdir
  workdir="$(mktemp -d)"
  # Double-quoted (not single-quoted): bakes the actual path in NOW, at trap
  # registration time. A single-quoted trap would defer $workdir's expansion
  # to whenever the EXIT trap fires -- which happens in the top-level script
  # scope, where this function's `local workdir` no longer exists (confirmed
  # live: "workdir: unbound variable" from exactly that).
  trap "rm -rf '$workdir'" EXIT
  for zip in "$@"; do
    [[ -f "$zip" ]] || die "not found: $zip"
    local base name db extractdir
    base="$(basename "$zip")"
    name="${base%.zip}"
    # dev-health/backups/ names each file clickhouse-<dbname>-<timestamp>.zip
    db="$(sed -E 's/^clickhouse-(.+)-[0-9]{8}-[0-9]{6}\.zip$/\1/' <<<"$base")"
    [[ -n "$db" && "$db" != "$base" ]] || die "could not parse a database name out of $base (expected clickhouse-<dbname>-<timestamp>.zip)"
    extractdir="$workdir/$name"
    mkdir -p "$extractdir"
    log "unzipping $base locally (this server build has no 'Zip' backup engine; Disk() restore needs the tree on disk)"
    unzip -q "$zip" -d "$extractdir"
    # ClickHouse's own backup archive stores some entries (.backup) with
    # restrictive perms that survive unzip AND survive kubectl cp's tar
    # transport into the container (confirmed live, twice: first "permission
    # denied" reading our own freshly-unzipped file locally with u+rwX still
    # set -- fixed by widening past owner-only; second "CANNOT_OPEN_FILE"
    # from clickhouse-server itself, which runs as a different UID inside
    # the container than whatever kubectl cp's tar extraction runs as, so
    # owner-only bits still didn't cover it). World-readable is fine here --
    # this is a throwaway local extraction scratch dir, not the archive
    # itself.
    chmod -R a+rwX "$extractdir"
    log "copying extracted $name/ into $pod:/backups/"
    kubectl -n "$NAMESPACE" cp "$extractdir" "$pod:/backups/$name"
    rm -rf "$extractdir"
    # kubectl cp's tar transport did not reliably carry the local chmod
    # through into the container (confirmed live: the local file showed
    # a+rwX, but the SAME file inside the pod still showed the archive's
    # original restrictive mode) -- fix it server-side instead of trusting
    # the copy to preserve it.
    kubectl -n "$NAMESPACE" exec "$pod" -- chmod -R a+rwX "/backups/$name"
    log "restoring database $db from $name"
    kubectl -n "$NAMESPACE" exec "$pod" -- clickhouse-client -u ch --password "$PG_PASSWORD" \
      --query "RESTORE DATABASE \`$db\` FROM Disk('trial_backups', '$name')"
  done
  log "clickhouse restore complete: $# database(s)"
}

cmd_wipe() {
  require_kubeconfig
  # PRIMITIVE (team-lead design ruling, CHAOS-4186 round 3): three rounds
  # of P1s against a label-based ownership check (fails open on an empty
  # or erroring lookup; the label itself is adoptable by pointing
  # ACR_TRIAL_DATA_NAMESPACE at an existing namespace and re-`apply`ing;
  # get-then-delete is TOCTOU-racy with no UID/resourceVersion precondition)
  # means the check was the wrong primitive, not under-guarded -- replaced
  # rather than patched again. `wipe` never infers ownership: it deletes
  # EXACTLY the named resources `apply` renders (Secret/Deployment/
  # Service/PVC/ConfigMap, matched by kind+name, never "everything in this
  # namespace"), which cannot touch an unrelated pre-existing resource no
  # matter what ACR_TRIAL_DATA_NAMESPACE names. The Namespace document is
  # filtered out of the stream first -- namespace deletion is handled
  # separately below, deliberately NOT via -f, so it can never be driven
  # by an operator-controlled value.
  render | yq eval-all 'select(.kind != "Namespace")' - \
    | kubectl -n "$NAMESPACE" delete --ignore-not-found --wait=true --timeout=180s -f -
  # The namespace ITSELF is deleted only when it equals this script's own
  # HARDCODED default -- a literal string compare against a constant, never
  # the (possibly operator-overridden) $NAMESPACE variable, so no env
  # value can ever point this specific delete at a different namespace.
  # A non-default namespace keeps its own (now-empty) namespace object;
  # the resources inside it are still gone from the delete above.
  if [[ "$NAMESPACE" == "acr-trial-data" ]]; then
    kubectl delete namespace --ignore-not-found --wait=true --timeout=180s -- acr-trial-data
    log "trial data plane wiped (resources + namespace acr-trial-data deleted, PVCs gone)"
  else
    log "trial data plane resources wiped in namespace $NAMESPACE (namespace left in place -- wipe only ever deletes the namespace object itself when it is the hardcoded default acr-trial-data)"
  fi
}

[[ $# -ge 1 ]] || usage
cmd="$1"; shift || true
case "$cmd" in
  render) cmd_render "$@" ;;
  apply) cmd_apply "$@" ;;
  wait) cmd_wait "$@" ;;
  dsn) cmd_dsn "$@" ;;
  restore-postgres) cmd_restore_postgres "$@" ;;
  restore-clickhouse) cmd_restore_clickhouse "$@" ;;
  wipe) cmd_wipe "$@" ;;
  -h|--help|help) usage ;;
  *) die "unknown command: $cmd (try --help)" ;;
esac
