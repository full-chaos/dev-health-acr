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
#   ACR_TRIAL_NODEPORT_BASE    first of the four consecutive NodePorts
#                              (default: 30500). Must be a MULTIPLE OF 10 in
#                              30000-30990: the 10-port stride is what keeps
#                              two lanes' four-port RANGES disjoint, not just
#                              their base values. One base per lane namespace
#                              -- NodePorts are cluster-scoped, so two data
#                              planes on one cluster need different bases
#                              (CHAOS-4428).
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



log() { printf '[trial-data.sh] %s\n' "$*" >&2; }
die() { printf '[trial-data.sh] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  # Stop at `set -euo pipefail` rather than a hardcoded end line (codex
  # review, CHAOS-4428): the previous `sed -n '2,38p'` silently truncated
  # help mid-sentence the moment the header grew, dropping KUBECONFIG and
  # part of ACR_TRIAL_CH_IMAGE. Anchored on the real end of the comment
  # block, help can never fall out of sync with the header again.
  sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" \
    | sed '$d' | sed 's/^# \{0,1\}//'
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

# NodePorts, deliberately outside shard.sh's 31000-32766 budget
# (i<=883 -> 31000+2i / 31001+2i) so a standing data-plane port can never
# collide with a live per-shard pair.
#
# CHAOS-4428: the four ports are now derived from ACR_TRIAL_NODEPORT_BASE
# rather than hardcoded, because namespace-per-lane puts SEVERAL trial data
# planes on ONE cluster and a NodePort is cluster-scoped, not
# namespace-scoped -- two namespaces on the fixed 30500-30503 quadruple
# collide at apply time. The base stays constrained to 30000-30996 so the
# derived quadruple can never reach shard.sh's 31000 floor, which keeps the
# original guarantee intact rather than trading it away for the new one.
# Default 30500 is the previous hardcoded value, so an existing caller that
# sets nothing gets byte-identical ports.
NODEPORT_BASE="${ACR_TRIAL_NODEPORT_BASE:-30500}"
# Strict FIVE-digit decimal, checked BEFORE any arithmetic (codex review round
# 3, matching deploy/local/shard.sh:47-50's own guard). `^[0-9]+$` alone let an
# overlong value through to `(( ))`, where bash wraps at 64 bits: reproduced
# live, ACR_TRIAL_NODEPORT_BASE=18446744073709582116 passed both range and
# stride checks and rendered 30500-30503 -- silently colliding with the DEFAULT
# lane while looking nothing like a legal base. A leading zero is rejected for
# the same family of reason: bash arithmetic reads 030500 as octal.
[[ "$NODEPORT_BASE" =~ ^[1-9][0-9]{4}$ ]] \
  || die "ACR_TRIAL_NODEPORT_BASE=$NODEPORT_BASE is not a plain five-digit decimal (no leading zeros; longer values overflow bash arithmetic and would bypass the range check)"
(( NODEPORT_BASE >= 30000 && NODEPORT_BASE <= 30990 )) \
  || die "ACR_TRIAL_NODEPORT_BASE=$NODEPORT_BASE is outside 30000-30990 (the derived quadruple must stay below shard.sh's 31000 floor)"
# Codex review, CHAOS-4428: validating only the BASE let two lanes pick
# different-but-adjacent bases (30500 and 30501), whose four-port ranges
# still overlap at 30501-30503 -- the second `apply` then fails on
# cluster-scoped NodePort allocation, which is exactly the collision this
# variable exists to prevent. A 10-port stride makes the RANGES disjoint by
# construction, not merely the bases, and still leaves 100 lane slots.
(( NODEPORT_BASE % 10 == 0 )) \
  || die "ACR_TRIAL_NODEPORT_BASE=$NODEPORT_BASE must be a multiple of 10 (bases are strided so two lanes' four-port ranges can never overlap)"
PG_NODEPORT=$((NODEPORT_BASE))
CH_HTTP_NODEPORT=$((NODEPORT_BASE + 1))
CH_NATIVE_NODEPORT=$((NODEPORT_BASE + 2))
FALKOR_NODEPORT=$((NODEPORT_BASE + 3))

# validate_render_vars (codex xhigh review, fresh cycle round 1, P1): every
# one of these is `sed`-interpolated straight into the YAML template with
# no escaping -- validate_password was the only one actually checked. sed
# is not YAML-aware: a value containing a literal newline (or YAML
# metacharacters positioned to close/reopen a mapping) can inject an
# ENTIRELY NEW DOCUMENT into the stream, including a CLUSTER-SCOPED kind
# (e.g. PersistentVolume) that `kubectl -n $NAMESPACE delete -f -` would
# still act on regardless of the -n flag. Reproduced live by the reviewer
# via a crafted ACR_TRIAL_PG_STORAGE value. Storage sizes must be a bare
# Kubernetes quantity (digits + an optional binary/decimal suffix, no
# multi-char runs that could smuggle a colon or newline); the image ref
# is restricted to the character set real image references actually use
# (no whitespace, quotes, or newlines -- the injection vector itself).
validate_render_vars() {
  # Kubernetes quantity grammar (codex xhigh review, fresh cycle round 2,
  # LOW): binary suffixes (Ki/Mi/.../Ei), decimal SI suffixes (m/k/M/G/T/P/E
  # -- lowercase k specifically, not K), and a bare decimal exponent form
  # (1e3) are all real quantity forms; the previous regex rejected the
  # exponent form and accepted a nonstandard uppercase K. Purely a
  # strictness fix -- rejecting valid operator input, never an injection
  # path either way (still anchored, still no metacharacters possible).
  local -r qty_re='^[0-9]+(\.[0-9]+)?(e[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|[mkMGTPE])?$'
  [[ "$PG_STORAGE" =~ $qty_re ]] || die "ACR_TRIAL_PG_STORAGE=$PG_STORAGE is not a plain Kubernetes storage quantity (e.g. 20Gi)"
  [[ "$CH_STORAGE" =~ $qty_re ]] || die "ACR_TRIAL_CH_STORAGE=$CH_STORAGE is not a plain Kubernetes storage quantity (e.g. 30Gi)"
  [[ "$CH_BACKUPS_STORAGE" =~ $qty_re ]] || die "ACR_TRIAL_CH_BACKUPS_STORAGE=$CH_BACKUPS_STORAGE is not a plain Kubernetes storage quantity (e.g. 5Gi)"
  [[ "$FALKOR_STORAGE" =~ $qty_re ]] || die "ACR_TRIAL_FALKOR_STORAGE=$FALKOR_STORAGE is not a plain Kubernetes storage quantity (e.g. 5Gi)"
  [[ "$CH_IMAGE" =~ ^[A-Za-z0-9./_:@-]+$ ]] || die "ACR_TRIAL_CH_IMAGE=$CH_IMAGE contains characters outside a plain image reference (no whitespace/quotes/newlines)"
}

render() {
  validate_password
  validate_render_vars
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

# live_nodeport reads a port off the DEPLOYED Service rather than trusting the
# current shell's ACR_TRIAL_NODEPORT_BASE (codex review, CHAOS-4428).
#
# The two can disagree whenever a lane was applied with one base and `dsn` is
# invoked later without it (or with a different one) -- and because every lane
# shares the same default password, the DSN this command printed would then
# CONNECT SUCCESSFULLY to a different lane's datastore instead of failing.
# Silently reading another lane's data is the worst outcome available here, so
# the deployed Service is the authority for ports exactly as the cluster Secret
# is already the authority for the password (see the note further down).
live_nodeport() {
  local service="$1" port_name="$2" value
  value="$(kubectl -n "$NAMESPACE" get "service/$service" \
    -o jsonpath="{.spec.ports[?(@.name==\"$port_name\")].nodePort}" 2>/dev/null)" \
    || die "could not read service/$service in namespace $NAMESPACE -- has 'apply' been run?"
  [[ "$value" =~ ^[0-9]+$ ]] \
    || die "service/$service has no numeric nodePort for port $port_name in namespace $NAMESPACE (got: ${value:-<empty>})"
  printf '%s' "$value"
}

cmd_dsn() {
  require_kubeconfig
  validate_password
  local env_mode=0
  [[ "${1:-}" == "--env" ]] && env_mode=1
  local ip password
  # Shadow the render-time defaults with what is actually deployed.
  local PG_NODEPORT CH_HTTP_NODEPORT CH_NATIVE_NODEPORT FALKOR_NODEPORT
  PG_NODEPORT="$(live_nodeport trial-postgres postgres)"
  CH_HTTP_NODEPORT="$(live_nodeport trial-clickhouse http)"
  CH_NATIVE_NODEPORT="$(live_nodeport trial-clickhouse native)"
  FALKOR_NODEPORT="$(live_nodeport trial-falkordb redis)"
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

  if [[ "$env_mode" == "1" ]]; then
    # Structured key=value handoff (team-lead design ruling, CHAOS-4186
    # fresh review cycle, residual finding 6): common.sh used to parse this
    # command's DSN strings back apart with `:`/`@` splitting to recover
    # the raw host/port/user/password components run-two-turn-parallel.sh
    # needs -- delimiter-dependent, and fragile against an IPv6 host or a
    # password containing `@`. Emitting each component as its own
    # KEY=value line means there is no DSN string to split on `:`/`@`.
    # Raw, UNQUOTED values (round-3 follow-up ruling): the consumer reads
    # this output line-by-line and splits on the first `=` -- it never
    # `eval`s it, so there is nothing here for shell quoting to protect
    # against, and `printf %q` quoting would just be a layer the reader
    # would have to strip back off for no benefit.
    printf 'ACR_TEST_TRIAL_PG_HOST=%s\n' "$ip"
    printf 'ACR_TEST_TRIAL_PG_PORT=%s\n' "$PG_NODEPORT"
    printf 'ACR_TEST_TRIAL_PG_USER=%s\n' "devhealth"
    printf 'ACR_TEST_TRIAL_PG_PASSWORD=%s\n' "$password"
    printf 'ACR_TEST_TRIAL_PG_DB=%s\n' "acr"
    printf 'ACR_TEST_TRIAL_CH_HOST=%s\n' "$ip"
    printf 'ACR_TEST_TRIAL_CH_PORT=%s\n' "$CH_NATIVE_NODEPORT"
    printf 'ACR_TEST_TRIAL_CH_HTTP_PORT=%s\n' "$CH_HTTP_NODEPORT"
    printf 'ACR_TEST_TRIAL_CH_USER=%s\n' "ch"
    printf 'ACR_TEST_TRIAL_CH_PASSWORD=%s\n' "$password"
    printf 'ACR_TEST_TRIAL_CH_DB=%s\n' "default"
    printf 'ACR_TEST_TRIAL_FALKOR_HOST=%s\n' "$ip"
    printf 'ACR_TEST_TRIAL_FALKOR_PORT=%s\n' "$FALKOR_NODEPORT"
    # ACR_CONTEXT_FABRIC_FALKOR_TLS / ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE
    # (CHAOS-4186 follow-up, real incident during the VM resize): NOT
    # ACR_TEST_TRIAL_*-prefixed -- these are acr-projector's own raw config
    # var names (internal/contextfabric/falkorgraph/config.go), emitted
    # here verbatim so any consumer pointed at the kiac plane's FalkorDB
    # inherits the correct values without having to know them by heart.
    # The trial FalkorDB pod always serves plaintext RESP, never TLS, so
    # these are static, not derived. Without them, acr-projector's client
    # defaults to TLS=true (its own safe-by-default posture) and sends a
    # TLS ClientHello against the plaintext port, which never gets a
    # ServerHello back and hangs until ACR_CONTEXT_FABRIC_FALKOR_REQUEST_
    # TIMEOUT (default 30s) on every single tick -- indistinguishable at
    # first glance from a genuine connectivity or performance problem.
    printf 'ACR_CONTEXT_FABRIC_FALKOR_TLS=%s\n' "false"
    printf 'ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=%s\n' "true"
    return
  fi

  printf 'ACR_TEST_TRIAL_POSTGRES_DSN=postgres://devhealth:%s@%s:%d/acr?sslmode=disable\n' "$password" "$ip" "$PG_NODEPORT"
  printf 'ACR_TEST_TRIAL_CLICKHOUSE_DSN=clickhouse://ch:%s@%s:%d/default\n' "$password" "$ip" "$CH_NATIVE_NODEPORT"
  printf 'ACR_TEST_TRIAL_FALKOR_ADDR=%s:%d\n' "$ip" "$FALKOR_NODEPORT"
  printf '# clickhouse HTTP (for RESTORE/BACKUP admin queries): http://%s:%d\n' "$ip" "$CH_HTTP_NODEPORT"
  printf '# structured key=value handoff for scripting: trial-data.sh dsn --env\n'
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
  # PRIMITIVE, narrowed further (team-lead design ruling, CHAOS-4186 fresh
  # review cycle, residual finding 1): three rounds of P1s against a
  # label-based ownership check, then a name-scoped-but-namespace-flexible
  # delete that could still collide with same-named resources in an
  # unrelated namespace, means the right fix is to shrink what `wipe` can
  # ever touch, not to keep guarding a wider surface. `wipe` operates on
  # the HARDCODED default namespace ONLY -- it does not read
  # ACR_TRIAL_DATA_NAMESPACE at all. `apply`/`render`/`dsn`/`restore-*`
  # still honor the override (a caller CAN stand up a differently-named
  # instance), but that instance's resources are then this script's
  # responsibility to remove manually -- there is no namespace argument
  # here for an operator-controlled value to ever reach.
  if [[ "$NAMESPACE" != "acr-trial-data" ]]; then
    die "wipe operates only on acr-trial-data; remove resources in $NAMESPACE manually"
  fi
  render | yq eval-all 'select(.kind != "Namespace")' - \
    | kubectl -n acr-trial-data delete --ignore-not-found --wait=true --timeout=180s -f -
  kubectl delete namespace --ignore-not-found --wait=true --timeout=180s -- acr-trial-data
  log "trial data plane wiped (resources + namespace acr-trial-data deleted, PVCs gone)"
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
