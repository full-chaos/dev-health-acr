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
#   deploy/local/trial-data.sh wipe     # deletes the namespace incl. PVCs
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
  sed -n '2,34p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
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
  local ip
  ip="$(node_ip)"
  [[ -n "$ip" ]] || die "could not resolve a node InternalIP from KUBECONFIG"
  printf 'ACR_TEST_TRIAL_POSTGRES_DSN=postgres://devhealth:%s@%s:%d/acr?sslmode=disable\n' "$PG_PASSWORD" "$ip" "$PG_NODEPORT"
  printf 'ACR_TEST_TRIAL_CLICKHOUSE_DSN=clickhouse://ch:%s@%s:%d/default\n' "$PG_PASSWORD" "$ip" "$CH_NATIVE_NODEPORT"
  printf 'ACR_TEST_TRIAL_FALKOR_ADDR=%s:%d\n' "$ip" "$FALKOR_NODEPORT"
  printf '# clickhouse HTTP (for RESTORE/BACKUP admin queries): http://%s:%d\n' "$ip" "$CH_HTTP_NODEPORT"
  # scripts/trial/run-two-turn-parallel.sh (codex xhigh review round 2, P1):
  # this launcher does NOT read ACR_TEST_TRIAL_POSTGRES_DSN above -- it opens
  # its own connection from ACR_TRIAL_PG_HOST/PORT (default 127.0.0.1:5432)
  # plus POSTGRES_USER/POSTGRES_PASSWORD read from ops/.env, independently of
  # every var this command prints. Point it at this data plane with:
  printf 'ACR_TRIAL_PG_HOST=%s\n' "$ip"
  printf 'ACR_TRIAL_PG_PORT=%d\n' "$PG_NODEPORT"
  # CREDENTIAL COUPLING: ops/.env's POSTGRES_USER/POSTGRES_PASSWORD must
  # match what this data plane's devhealth role actually has -- USER already
  # matches (the seed dump always creates "devhealth"), but PASSWORD does
  # NOT unless restore-postgres was run with ACR_TRIAL_PG_PASSWORD set to
  # ops/.env's own POSTGRES_PASSWORD (this data plane's default,
  # acr-trial-dev, will NOT match ops/.env's compose credential). Re-run
  # restore-postgres with that override, or run the parallel launcher with a
  # temporary POSTGRES_PASSWORD override of its own, before pointing it here.
}

cmd_restore_postgres() {
  require_kubeconfig
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
  # Ownership check, not just name-format validation (codex xhigh review
  # round 2, P1): a format-valid namespace name is not necessarily THIS
  # script's own namespace -- ACR_TRIAL_DATA_NAMESPACE=acr-pilot passes the
  # format check above and would otherwise let this delete an unrelated,
  # pre-existing namespace (and everything in it) on this shared cluster.
  # Every namespace this script creates carries the acr-trial-data-plane
  # part-of label (trial-data.yaml); refuse to touch anything that either
  # doesn't exist (nothing to protect) or doesn't carry it.
  local owner
  owner="$(kubectl get namespace "$NAMESPACE" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/part-of}' 2>/dev/null || true)"
  if [[ -n "$owner" && "$owner" != "acr-trial-data-plane" ]]; then
    die "namespace $NAMESPACE exists but is NOT labeled app.kubernetes.io/part-of=acr-trial-data-plane (found: '$owner') -- refusing to wipe a namespace this script did not create"
  fi
  kubectl delete namespace --ignore-not-found --wait=true --timeout=180s -- "$NAMESPACE"
  log "trial data plane wiped (namespace $NAMESPACE deleted, PVCs gone)"
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
