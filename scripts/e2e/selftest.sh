#!/usr/bin/env bash
#
# scripts/e2e/selftest.sh
#
# Isolation self-test for the pinned Kind/TLS fixture (plan Todo 18). Proves,
# against REAL Docker/Kind state, that each fixture owns a uniquely named local
# registry container and Docker network derived from its cluster name, that the
# fixture's node and registry share ONLY that network, that the registry serves
# on it, and that two concurrent fixtures are mutually isolated (no host-global
# or cross-fixture collision).
#
# This is written failing-first: run against a fixture built before per-fixture
# registry/network provisioning exists, every ownership assertion fails.
#
# Subcommands:
#   static                         lock hardening regressions without Docker/Kind
#   single --name <cluster>        assert one fixture's registry/network isolation
#   pair   --a <clusterA> --b <clusterB>   assert both, plus mutual isolation
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/pins.env"

PROBE_IMG="${ACR_E2E_IMG_PROBE:-docker.io/library/busybox@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}"
FIXTURE_SCRIPT="${SCRIPT_DIR}/kind-fixture.sh"
HELM_SCRIPT="${SCRIPT_DIR}/kind-helm.sh"
STATE_ROOT="${ACR_E2E_STATE_ROOT:-${REPO_ROOT}/.tmp/e2e}"
FIXTURE_LABEL_KEY="acr-e2e.fullchaos.dev/fixture-id"

FAILURES=0
ok()   { printf '[selftest] ok: %s\n' "$*" >&2; }
bad()  { printf '[selftest] FAIL: %s\n' "$*" >&2; FAILURES=$((FAILURES+1)); }
die()  { printf '[selftest] FATAL: %s\n' "$*" >&2; exit 2; }

net_name()  { echo "$1-net"; }
reg_name()  { echo "$1-registry"; }
node_name() { echo "$1-control-plane"; }
identity_path() { echo "${STATE_ROOT}/$1/fixture-identity.env"; }

# Is docker object $2 (a container name) attached to docker network $1?
net_has_container() {
  docker network inspect "$1" --format '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep -qx "$2"
}

identity_value() {
  local name="$1" key="$2" file
  file="$(identity_path "${name}")"
  [[ -f "${file}" ]] || return 1
  awk -F= -v key="${key}" '$1 == key { print $2 }' "${file}"
}

assert_fixture_script_contains() {
  local expression="$1" description="$2"
  if grep -Eq -- "${expression}" "${FIXTURE_SCRIPT}"; then ok "${description}"; else bad "${description}"; fi
}

assert_fixture_script_lacks() {
  local expression="$1" description="$2"
  if grep -Eq -- "${expression}" "${FIXTURE_SCRIPT}"; then bad "${description}"; else ok "${description}"; fi
}

assert_helm_script_contains() {
  local expression="$1" description="$2"
  if grep -Eq -- "${expression}" "${HELM_SCRIPT}"; then ok "${description}"; else bad "${description}"; fi
}

assert_helm_script_lacks() {
  local expression="$1" description="$2"
  if grep -Eq -- "${expression}" "${HELM_SCRIPT}"; then bad "${description}"; else ok "${description}"; fi
}

assert_rendering_with_bash() {
  local shell="$1" rendered
  # shellcheck disable=SC2016
  if rendered="$("${shell}" -c '
    ACR_E2E_LIB_ONLY=1
    export ACR_E2E_LIB_ONLY
    source "$1"
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/acr-kind-selftest.XXXXXX")"
    trap "rm -rf \"${tmp}\"" EXIT
    render_pinned_manifest "$2/vendor/calico.yaml" "${tmp}/calico.yaml" \
      "quay.io/calico/cni:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_CNI}" \
      "quay.io/calico/node:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_NODE}" \
      "quay.io/calico/kube-controllers:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}"
    assert_manifest_images_pinned "${tmp}/calico.yaml"
    grep -Fxq "          image: ${ACR_E2E_IMG_CALICO_NODE}" "${tmp}/calico.yaml"
  ' -- "${FIXTURE_SCRIPT}" "${SCRIPT_DIR}" 2>&1)"; then
    ok "${shell} renders digest-only Calico image references"
  else
    bad "${shell} cannot render digest-only Calico image references: ${rendered}"
  fi
}

assert_helm_harness_behaviors() {
  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    build_local_image() {
      imported_image_refs+=("image-$1")
      remote_archives+=("archive-$1")
      built_image_ref="image-$1"
    }
    build_local_image v1
    build_local_image v2
    [[ "${built_image_ref}" == image-v2 ]]
    [[ "${#imported_image_refs[@]}" -eq 2 && "${#remote_archives[@]}" -eq 2 ]]
  ' -- "${HELM_SCRIPT}"; then
    ok 'image build tracking remains in the parent shell through lifecycle'
  else
    bad 'image build tracking must remain in the parent shell through lifecycle'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    ACR_E2E_IMAGE_PULL_SECRET=secret
    validate_immutable_image_ref() { :; }
    publish_image_to_fixture_registry() {
      imported_image_refs+=(registry-image)
      published_image_ref=registry-image
    }
    publish_image_to_fixture_registry local-image
    [[ "${published_image_ref}" == registry-image && "${#imported_image_refs[@]}" -eq 1 ]]
  ' -- "${HELM_SCRIPT}"; then
    ok 'registry publish tracking remains in the parent shell'
  else
    bad 'registry publish tracking must remain in the parent shell'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    TIMEOUT_TERM_GRACE_SECONDS=1
    signals=()
    kill() { [[ "$1" == -0 ]] || signals+=("$1"); return 0; }
    wait_for_process_exit() { return 1; }
    wait() { return 0; }
    if run_with_timeout 0 sh -c true; then
      exit 1
    else
      status=$?
    fi
    [[ "${status}" == 124 ]]
  ' -- "${HELM_SCRIPT}"; then
    ok 'timeout sends TERM then KILL and returns within its grace bound'
  else
    bad 'timeout must send TERM then KILL and return within its grace bound'
  fi

  # shellcheck disable=SC2016
  local evidence_status
  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    write_evidence() { return 1; }
    queued_evidence_result=passed
    queued_evidence_detail=detail
    on_exit 42
  ' -- "${HELM_SCRIPT}"; then
    evidence_status=0
  else
    evidence_status=$?
  fi
  if [[ "${evidence_status}" == 42 ]]; then
    ok 'failed evidence writing preserves the original scenario status'
  else
    bad 'failed evidence writing must preserve the original scenario status'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    stop_port_forwards() { return 1; }
    cleanup_namespace() { return 0; }
    cleanup_kind_images() { return 0; }
    queued_evidence_result=""
    run_dir=""
    on_exit 0
  ' -- "${HELM_SCRIPT}"; then
    bad 'cleanup failure must turn an otherwise successful exit into failure'
  else
    ok 'cleanup failure turns an otherwise successful exit into failure'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    port_forward_pids=(42)
    kill() { return 0; }
    wait_for_process_exit() { return 0; }
    wait() { return 143; }
    stop_port_forwards
  ' -- "${HELM_SCRIPT}"; then
    ok 'expected port-forward signal exit is accepted during bounded cleanup'
  else
    bad 'expected port-forward signal exit is accepted during bounded cleanup'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    cluster=test
    imported_image_refs=(owned-ref)
    docker() { return 1; }
    if cleanup_kind_images; then exit 1; fi
  ' -- "${HELM_SCRIPT}"; then
    ok 'Kind image inspection failures fail cleanup closed'
  else
    bad 'Kind image inspection failures fail cleanup closed'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    owned_namespace=1
    namespace_labelled=0
    cleanup_started=0
    namespace=test
    run_id=run
    kube() {
      case "$*" in
        *"get namespace"*) printf "" ;;
        *"delete namespace"*|*"wait --for=delete"*) return 0 ;;
      esac
    }
    kube_cleanup() { kube "$@"; }
    cleanup_namespace
  ' -- "${HELM_SCRIPT}"; then
    ok 'namespace created before label failure remains cleanup-owned'
  else
    bad 'namespace created before label failure remains cleanup-owned'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    capture_clean_git_provenance() { :; }
    source_tree_hash() { printf fixed; }
    sleep() { [[ "$1" == 60 ]]; }
    establish_source_guard
    [[ "${source_hash}" == fixed ]]
  ' -- "${HELM_SCRIPT}"; then
    ok 'source guard requires a sixty-second quiescence window before a scenario'
  else
    bad 'source guard requires a sixty-second quiescence window before a scenario'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    root="$(mktemp -d)"
    trap "rm -rf \"${root}\"" EXIT
    mkdir -p "${root}/cmd" "${root}/internal" "${root}/migrations" "${root}/deploy/helm" "${root}/scripts/container" "${root}/scripts/e2e"
    printf x >"${root}/Dockerfile"
    printf x >"${root}/Dockerfile.dockerignore"
    printf x >"${root}/go.mod"
    printf x >"${root}/go.sum"
    printf x >"${root}/.dockerignore"
    printf x >"${root}/scripts/e2e/kind-helm.sh"
    printf x >"${root}/scripts/e2e/kind-fixture.sh"
    printf x >"${root}/scripts/e2e/pins.env"
    REPO_ROOT="${root}"
    before="$(source_tree_hash)"
    printf x >"${root}/cmd/untracked.go"
    after="$(source_tree_hash)"
    [[ "${before}" != "${after}" ]]
  ' -- "${HELM_SCRIPT}"; then
    ok 'source guard fingerprints untracked container build inputs'
  else
    bad 'source guard fingerprints untracked container build inputs'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    if run_with_timeout 0 sh -c "exit 0"; then exit 1; else [[ "$?" == 124 ]]; fi
  ' -- "${HELM_SCRIPT}"; then
    ok 'registry timeout wrapper returns a bounded timeout failure'
  else
    bad 'registry timeout wrapper returns a bounded timeout failure'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    kube() {
      case "$*" in
        *"get deployment/acr-runtime"*) return 0 ;;
        *"wait --for=condition=Available deployment/acr-runtime"*) return 1 ;;
        *"get pods -l"*) printf acr-runtime-api-pod ;;
        *"get pod/acr-runtime-api-pod"*"waiting.reason"*) printf CreateContainerConfigError ;;
        *"get events"*"involvedObject.kind=Pod,involvedObject.name=acr-runtime-api-pod"*) printf "Warning Failed pod/acr-runtime-api-pod CreateContainerConfigError: secret \"other-runtime\" not found" ;;
      esac
      return 0
    }
    if assert_missing_runtime_secret; then exit 1; fi
  ' -- "${HELM_SCRIPT}"; then
    ok 'unrelated not-found events do not satisfy the missing runtime Secret assertion'
  else
    bad 'unrelated not-found events must not satisfy the missing runtime Secret assertion'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    kube() {
      case "$*" in
        *"get deployment/acr-runtime"*) return 0 ;;
        *"wait --for=condition=Available deployment/acr-runtime"*) return 1 ;;
        *"get pods -l"*) printf acr-runtime-api-pod ;;
        *"get pod/acr-runtime-api-pod"*"waiting.reason"*) printf CreateContainerConfigError ;;
        *"get events"*"involvedObject.kind=Pod,involvedObject.name=acr-runtime-api-pod"*) printf "Warning Failed pod/acr-runtime-api-pod CreateContainerConfigError: secret \"acr-runtime\" not found" ;;
      esac
      return 0
    }
    assert_missing_runtime_secret
  ' -- "${HELM_SCRIPT}"; then
    ok 'exact API Pod missing runtime Secret event satisfies the assertion'
  else
    bad 'exact API Pod missing runtime Secret event must satisfy the assertion'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    expected_migration_failure_marker="PostgreSQL is unavailable"
    expected_migration_failure_dsn="postgres://postgres:acr-e2e-pass@postgres.invalid:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
    kube() {
      case "$*" in
        *"wait --for=condition=failed job/acr-runtime-migrate"*) return 0 ;;
        *"get job/acr-runtime-migrate"*) printf pre-install,pre-upgrade ;;
        *"get pods -l job-name=acr-runtime-migrate"*) printf acr-runtime-migrate-pod ;;
        *".spec.containers"*) printf "/usr/local/bin/acr-migrate up" ;;
        *"get secret acr-migration"*) printf "%s" "${expected_migration_failure_dsn}" | base64 ;;
        *".status.containerStatuses"*) printf 1 ;;
        *"logs acr-runtime-migrate-pod"*) printf "exec: invalid binary" ;;
        *"wait --for=condition=Available deployment/acr-runtime"*) return 1 ;;
      esac
      return 0
    }
    if assert_failed_migration_hook; then exit 1; fi
  ' -- "${HELM_SCRIPT}"; then
    ok 'unrelated migration binary failures do not satisfy the injected fixture failure assertion'
  else
    bad 'unrelated migration binary failures must not satisfy the injected fixture failure assertion'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    expected_migration_failure_marker="PostgreSQL is unavailable"
    expected_migration_failure_dsn="postgres://postgres:acr-e2e-pass@postgres.invalid:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
    kube() {
      case "$*" in
        *"wait --for=condition=failed job/acr-runtime-migrate"*) return 0 ;;
        *"get job/acr-runtime-migrate"*) printf pre-install,pre-upgrade ;;
        *"get pods -l job-name=acr-runtime-migrate"*) printf acr-runtime-migrate-pod ;;
        *".spec.containers"*) printf "/usr/local/bin/acr-migrate up" ;;
        *"get secret acr-migration"*) printf "%s" "${expected_migration_failure_dsn}" | base64 ;;
        *".status.containerStatuses"*) printf 1 ;;
        *"logs acr-runtime-migrate-pod"*) printf "acr-migrate: invalid PostgreSQL configuration" ;;
        *"wait --for=condition=Available deployment/acr-runtime"*) return 1 ;;
      esac
      return 0
    }
    if assert_failed_migration_hook; then exit 1; fi
  ' -- "${HELM_SCRIPT}"; then
    ok 'redacted migration configuration failures do not satisfy the unavailable boundary assertion'
  else
    bad 'redacted migration configuration failures must not satisfy the unavailable boundary assertion'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    expected_migration_failure_marker="PostgreSQL is unavailable"
    expected_migration_failure_dsn="postgres://postgres:acr-e2e-pass@postgres.invalid:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
    kube() {
      case "$*" in
        *"wait --for=condition=failed job/acr-runtime-migrate"*) return 0 ;;
        *"get job/acr-runtime-migrate"*) printf pre-install,pre-upgrade ;;
        *"get pods -l job-name=acr-runtime-migrate"*) printf acr-runtime-migrate-pod ;;
        *".spec.containers"*) printf "/usr/local/bin/acr-migrate up" ;;
        *"get secret acr-migration"*) printf "%s" "${expected_migration_failure_dsn}" | base64 ;;
        *".status.containerStatuses"*) printf 1 ;;
        *"logs acr-runtime-migrate-pod"*) printf "acr-migrate: PostgreSQL is unavailable; dsn=%s" "${expected_migration_failure_dsn}" ;;
        *"wait --for=condition=Available deployment/acr-runtime"*) return 1 ;;
      esac
      return 0
    }
    if assert_failed_migration_hook; then exit 1; fi
  ' -- "${HELM_SCRIPT}"; then
    ok 'unredacted migration connection details do not satisfy the unavailable boundary assertion'
  else
    bad 'unredacted migration connection details must not satisfy the unavailable boundary assertion'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    expected_migration_failure_marker="PostgreSQL is unavailable"
    expected_migration_failure_dsn="postgres://postgres:acr-e2e-pass@postgres.invalid:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
    kube() {
      case "$*" in
        *"wait --for=condition=failed job/acr-runtime-migrate"*) return 0 ;;
        *"get job/acr-runtime-migrate"*) printf pre-install,pre-upgrade ;;
        *"get pods -l job-name=acr-runtime-migrate"*) printf acr-runtime-migrate-pod ;;
        *".spec.containers"*) printf "/usr/local/bin/acr-migrate up" ;;
        *"get secret acr-migration"*) printf "%s" "${expected_migration_failure_dsn}" | base64 ;;
        *".status.containerStatuses"*) printf 1 ;;
        *"logs acr-runtime-migrate-pod"*) printf "acr-migrate: PostgreSQL is unavailable" ;;
        *"wait --for=condition=Available deployment/acr-runtime"*) return 1 ;;
      esac
      return 0
    }
    assert_failed_migration_hook
  ' -- "${HELM_SCRIPT}"; then
    ok 'redacted PostgreSQL unavailable boundary proves the injected migration failure'
  else
    bad 'redacted PostgreSQL unavailable boundary must prove the injected migration failure'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    release=acr-runtime
    ACR_E2E_DEPS_NAMESPACE=deps
    create_fixture_references() { printf https://ops-entitlement.deps.svc.cluster.local:8443; }
    build_local_image() { :; }
    write_values() { :; }
    inject_denied_migration_policy() { :; }
    run_helm_failure() { return 1; }
    diagnose_workload_failure() { :; }
    assert_failed_migration_hook() {
      [[ "${expected_migration_failure_marker}" == "PostgreSQL is unavailable" ]]
      [[ "${expected_migration_failure_dsn}" == "postgres://postgres:acr-e2e-pass@postgres.deps.svc.cluster.local:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt" ]]
    }
    assert_denied_migration_egress() { :; }
    queue_expected_failure() { :; }
    log() { :; }
    run_denied_egress
  ' -- "${HELM_SCRIPT}"; then
    ok 'denied-egress initializes its causal marker before the migration failure assertion'
  else
    bad 'denied-egress must not exit on an unbound migration failure marker'
  fi

  # shellcheck disable=SC2016
  if "${BASH}" -c '
    ACR_E2E_LIB_ONLY=1 source "$1" --scenario static
    namespace=test
    poll_file="$(mktemp)"
    trap "rm -f \"${poll_file}\"" EXIT
    printf 0 >"${poll_file}"
    kube() {
      case "$*" in
        *observedGeneration*) if [[ "$(cat "${poll_file}")" -gt 1 ]]; then printf 3; fi ;;
        *metadata.generation*) printf 3 ;;
        *Programmed*status*) poll=$(( $(cat "${poll_file}") + 1 )); printf %s "${poll}" >"${poll_file}"; if [[ "${poll}" -gt 1 ]]; then printf False; fi ;;
      esac
      return 0
    }
    sleep() { :; }
    wait_gateway_programmed_false gateway
  ' -- "${HELM_SCRIPT}"; then
    ok 'Gateway False assertion waits for a reconciled condition'
  else
    bad 'Gateway False assertion waits for a reconciled condition'
  fi
}

assert_static_hardening() {
  local literal_dollar='$'
  assert_fixture_script_contains 'require_kind_version' 'Kind version gate is implemented'
  assert_fixture_script_contains 'kind version -q' 'Kind version gate queries exact version'
  assert_fixture_script_contains 'render_pinned_manifest' 'vendored manifests are rewritten before apply'
  assert_fixture_script_contains 'calico\.pinned\.yaml' 'Calico applies digest-only rendered manifest'
  assert_fixture_script_contains 'envoy-gateway\.pinned\.yaml' 'Envoy Gateway applies digest-only rendered manifest'
  assert_fixture_script_contains 'assert_manifest_images_pinned' 'rendered manifests reject mutable image tags'
  assert_fixture_script_contains 'kind: BackendTLSPolicy' 'Gateway backend TLS policy is rendered'
  assert_fixture_script_contains 'verify_north_south_entitlement' 'actual north-south HTTPS response is checked'
  assert_fixture_script_contains 'wrong-\$\{backend_host\}' 'Backend TLS verification includes a wrong-SAN negative control'
  assert_fixture_script_contains '--noproxy' 'north-south localhost probe bypasses HTTPS proxies'
  assert_fixture_script_contains 'rollback_create_on_exit' 'create has a rollback trap'
  assert_fixture_script_contains 'cleanup_owned_fixture' 'destroy uses scoped ownership cleanup'
  assert_fixture_script_contains 'fixture-identity\.env' 'destroy requires a recorded fixture identity'
  assert_fixture_script_contains 'fixture-id' 'Docker and Kubernetes resources carry fixture identity labels'
  assert_fixture_script_contains 'docker image inspect.*ACR_E2E_NODE_IMAGE' 'node verification compares exact pinned image ID'
  assert_fixture_script_contains 'verify_host_container_image' 'registry verification compares exact pinned runtime image ID'
  assert_fixture_script_contains 'runtime_image_id_matches_pin' 'pod runtime image IDs resolve back to the pinned image'
  assert_fixture_script_contains 'runtime_image_id_%d' 'evidence records observed runtime image IDs'
  assert_fixture_script_contains 'commit_sha=' 'evidence records the exact checked-out commit SHA'
  assert_fixture_script_contains 'evidence_payload_sha256=' 'evidence records a deterministic payload hash'
  assert_fixture_script_contains 'validate_verification_evidence' 'evidence hash is verified before success'
  assert_fixture_script_contains 'evidence_written_at_utc=' 'evidence records timestamped completion'
  assert_fixture_script_contains 'acquire_fixture_lock' 'create and destroy serialize fixture lifecycle ownership'
  assert_fixture_script_contains 'fixture ownership verification failed' 'verify aborts before mutating an unowned fixture'
  assert_fixture_script_contains 'probe_kind_nodes' 'Kind teardown authenticates its complete recorded node set'
  assert_fixture_script_contains '--no-trunc' 'Kind node ownership records immutable full Docker IDs'
  assert_fixture_script_contains 'kind-created-before-record' 'partial Kind creation has a reconciled rollback path'
  assert_fixture_script_contains 'shutdown-manager' 'Envoy shutdown-manager runtime image is verified'
  assert_fixture_script_contains 'probe_docker_network' 'Docker observation errors are fail-closed'
  assert_fixture_script_lacks 'mapfile' 'north-south service discovery supports stock macOS Bash'
  assert_rendering_with_bash "${BASH}" 
  if [[ -x /bin/bash && /bin/bash != "${BASH}" ]]; then assert_rendering_with_bash /bin/bash; fi
  assert_fixture_script_lacks 'kind delete cluster --name .*[|][|] true' 'cluster deletion failures are not suppressed'
  assert_fixture_script_lacks 'docker rm -f .*[|][|] true' 'registry deletion failures are not suppressed'
  assert_fixture_script_lacks 'docker network rm .*[|][|] true' 'network deletion failures are not suppressed'
  assert_helm_script_contains 'establish_source_guard' 'every live Helm scenario requires a source quiescence guard'
  assert_helm_script_contains 'sleep 60' 'source guard establishes sixty seconds of source quiescence'
  assert_helm_script_contains 'capture_clean_git_provenance' 'source guard captures exact clean Git provenance'
  assert_helm_script_contains 'from-literal=token=acr-e2e-ops-token-initial' 'fixture entitlement token matches the controlled Ops responder'
  assert_helm_script_contains 'commit_sha=' 'scenario evidence records its exact commit SHA'
  assert_helm_script_contains 'working_tree_clean=' 'scenario evidence records clean-tree provenance'
  assert_helm_script_contains 'assert_source_guard' 'local image builds recheck the source hash guard'
  assert_helm_script_contains 'scripts/e2e/kind-helm.sh' 'source guard covers the executed Helm harness'
  assert_helm_script_contains 'scripts/container' 'source guard covers untracked container build helpers'
  assert_helm_script_contains --request-timeout=10s 'Kubectl reconciliation calls have request deadlines'
  assert_helm_script_contains 'exit "\$\{cleanup_status\}"' 'EXIT cleanup failures change a successful lifecycle exit status'
  assert_helm_script_contains 'run_with_timeout' 'registry pushes are bounded and fail closed'
  assert_helm_script_contains 'wait_gateway_programmed_false' 'Gateway False assertions wait for reconciliation'
  assert_helm_script_contains 'involvedObject.name=\$\{pod\}' 'image-pull evidence is scoped to the migration Pod'
  assert_helm_script_contains 'involvedObject.name=\$\{pod\}' 'missing runtime Secret evidence is scoped to the API Pod'
  assert_helm_script_contains 'secret_name="acr-runtime"' 'missing runtime Secret assertion requires the exact Secret name'
  assert_helm_script_contains 'waiting_reason.*CreateContainerConfigError' 'missing runtime Secret assertion requires the exact waiting reason'
  assert_helm_script_contains 'expected_migration_failure_dsn' 'bad migration assertion selects the exact verified-TLS fixture configuration'
  assert_helm_script_contains "entitlement_url=\"https://\\${literal_dollar}\{ACR_E2E_OPS_ENTITLEMENT_HOST\}:\\${literal_dollar}\{ACR_E2E_OPS_ENTITLEMENT_PORT\}\"" 'Helm fixture derives entitlement URL host and port from fixture exports'
  assert_helm_script_contains "entitlementPort: \\${literal_dollar}\{ACR_E2E_OPS_ENTITLEMENT_PORT\}" 'Helm fixture allows the exported entitlement TLS port'
  assert_helm_script_contains "expected_migration_failure_dsn=\"postgres://postgres:acr-e2e-pass@postgres\.\\${literal_dollar}\{ACR_E2E_DEPS_NAMESPACE\}\.svc\.cluster\.local:5432/acr\\?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca\.crt\"" 'denied-egress initializes the exact verified migration DSN before assertion'
  assert_helm_script_contains 'PostgreSQL is unavailable' 'bad migration assertion classifies the redacted unavailable boundary'
  assert_helm_script_contains 'migration hook exposed injected connection details' 'bad migration assertion rejects leaked connection details'
  assert_helm_script_contains 'acr-migrate.*up' 'bad migration assertion proves the intended migration hook command'
  assert_helm_script_contains 'assert_anonymous_registry_pull_denied' 'missing pull-secret scenario proves anonymous registry denial'
  assert_helm_script_contains 'prepare_registry_image_aliases' 'registry target aliases are derived during preflight before cleanup is armed'
  assert_helm_script_contains 'assert_kind_images_absent' 'Kind image imports reject pre-existing references before tracking cleanup ownership'
  assert_helm_script_contains 'namespace_labelled' 'namespace cleanup handles label failure after namespace creation'
  assert_helm_script_lacks '^[[:space:]]*docker exec "\$\{node\}" ctr -n k8s.io images push' 'registry pushes cannot run without a bounded wrapper'
  assert_helm_harness_behaviors
}

assert_single() {
  local name="$1"
  local net reg node fixture_id
  net="$(net_name "$name")"; reg="$(reg_name "$name")"; node="$(node_name "$name")"
  fixture_id="$(identity_value "${name}" fixture_id || true)"

  if [[ "${fixture_id}" =~ ^[a-f0-9]{32}$ ]]; then ok "fixture ${name} has a recorded opaque identity"; else bad "fixture ${name} has no valid ownership identity"; fi

  # 1. Uniquely named Docker network exists and is fixture-derived.
  if docker network inspect "$net" >/dev/null 2>&1; then ok "network ${net} exists"; else bad "network ${net} missing"; fi
  if [[ "$(docker network inspect "$net" --format "{{ index .Labels \"${FIXTURE_LABEL_KEY}\" }}" 2>/dev/null)" == "${fixture_id}" ]]; then
    ok "network ${net} label matches exact fixture identity"
  else
    bad "network ${net} is not owned by fixture identity"
  fi

  # 2. Uniquely named registry container exists and is running.
  if [[ "$(docker inspect -f '{{.State.Running}}' "$reg" 2>/dev/null)" == "true" ]]; then
    ok "registry ${reg} running"
  else
    bad "registry ${reg} not running"
  fi
  if [[ "$(docker inspect "$reg" --format "{{ index .Config.Labels \"${FIXTURE_LABEL_KEY}\" }}" 2>/dev/null)" == "${fixture_id}" ]]; then
    ok "registry ${reg} label matches exact fixture identity"
  else
    bad "registry ${reg} is not owned by fixture identity"
  fi

  # 3. Registry is attached to THIS fixture's network.
  if net_has_container "$net" "$reg"; then ok "registry ${reg} attached to ${net}"; else bad "registry ${reg} not on ${net}"; fi

  # 4. The fixture node is attached to THIS fixture's network.
  if net_has_container "$net" "$node"; then ok "node ${node} attached to ${net}"; else bad "node ${node} not on ${net}"; fi

  # 5. Node is NOT on the host-global default "kind" network (no shared bridge).
  if docker network inspect kind >/dev/null 2>&1; then
    if net_has_container kind "$node"; then bad "node ${node} leaked onto host-global 'kind' network"; else ok "node ${node} not on host-global 'kind' network"; fi
  else
    ok "no host-global 'kind' network present"
  fi

  # 6. Registry actually serves the v2 API on the fixture network (real reach).
  local out
  out="$(docker run --rm --network "$net" "$PROBE_IMG" wget -qO- --header='Authorization: Basic Zml4dHVyZTpmaXh0dXJl' "http://${reg}:5000/v2/" 2>/dev/null || true)"
  if [[ "$out" == "{}" ]]; then ok "registry ${reg} serves /v2/ on ${net}"; else bad "registry ${reg} not reachable/serving on ${net} (got: '${out}')"; fi
}

assert_pair_isolation() {
  local a="$1" b="$2"
  local aNet bNet aReg bReg
  aNet="$(net_name "$a")"; bNet="$(net_name "$b")"; aReg="$(reg_name "$a")"; bReg="$(reg_name "$b")"

  # Distinct network identities.
  local aId bId
  aId="$(docker network inspect "$aNet" -f '{{.Id}}' 2>/dev/null || echo none-a)"
  bId="$(docker network inspect "$bNet" -f '{{.Id}}' 2>/dev/null || echo none-b)"
  if [[ "$aId" != "$bId" && "$aId" != none-a && "$bId" != none-b ]]; then ok "networks ${aNet} and ${bNet} are distinct"; else bad "networks not distinct/absent"; fi

  # Cross-membership must NOT exist: A's registry off B's net and vice versa.
  if net_has_container "$bNet" "$aReg"; then bad "${aReg} leaked onto ${bNet}"; else ok "${aReg} absent from ${bNet}"; fi
  if net_has_container "$aNet" "$bReg"; then bad "${bReg} leaked onto ${aNet}"; else ok "${bReg} absent from ${aNet}"; fi

  # B's registry must NOT be resolvable/reachable from A's network (isolation).
  local xout
  xout="$(docker run --rm --network "$aNet" "$PROBE_IMG" wget -T 4 -qO- --header='Authorization: Basic Zml4dHVyZTpmaXh0dXJl' "http://${bReg}:5000/v2/" 2>/dev/null || true)"
  if [[ -z "$xout" ]]; then ok "${bReg} unreachable from ${aNet} (isolated)"; else bad "${bReg} reachable from ${aNet} (isolation breach)"; fi
}

main() {
  local sub="${1:-}"; shift || true
  local name="" a="" b=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name) name="${2:-}"; shift 2 ;;
      --a) a="${2:-}"; shift 2 ;;
      --b) b="${2:-}"; shift 2 ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  case "$sub" in
    static)
      [[ "$#" -eq 0 ]] || die "static accepts no arguments"
      assert_static_hardening ;;
    single)
      [[ -n "$name" ]] || die "single requires --name"
      assert_single "$name" ;;
    pair)
      [[ -n "$a" && -n "$b" ]] || die "pair requires --a and --b"
      assert_single "$a"; assert_single "$b"; assert_pair_isolation "$a" "$b" ;;
    *) echo "usage: $0 {static|single --name <c>|pair --a <c> --b <c>}" >&2; exit 2 ;;
  esac

  if [[ "$FAILURES" -eq 0 ]]; then printf '[selftest] PASS: fixture hardening proven\n' >&2; exit 0; fi
  printf '[selftest] FAIL: %d fixture hardening violation(s)\n' "$FAILURES" >&2; exit 1
}

main "$@"
