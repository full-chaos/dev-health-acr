#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT}/scripts/e2e/kind-helm.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

test_import_tracking_records_every_new_alias() {
  local state_root fake_bin output expected
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*)
    printf '%s\n' \
      'acr-e2e.local/acr-api-test:v1' \
      'acr-e2e.local/acr-api-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
      'acr-e2e.local/acr-api-test@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  output="$(PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    kind_image_refs_before=""
    imported_image_refs=()
    record_imported_image_refs acr-e2e.local/acr-api-test
    printf "%s\n" "${imported_image_refs[@]}"
  ' -- "${HARNESS}")"
  rm -rf "${state_root}"

  expected=$'acr-e2e.local/acr-api-test:v1\nacr-e2e.local/acr-api-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nacr-e2e.local/acr-api-test@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  [[ "${output}" == "${expected}" ]] || fail 'OCI import aliases were not all tracked for cleanup'
}

test_cleanup_retries_and_preserves_unrelated_refs() {
  local state_root fake_bin output
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
refs="${FAKE_REFS}"
case " $* " in
  *" ctr -n k8s.io images list -q "*) cat "${refs}" ;;
  *" ctr -n k8s.io images rm "*)
    ref="${*: -1}"
    count_file="${FAKE_STATE}/remove-count"
    count="$(cat "${count_file}" 2>/dev/null || printf 0)"
    printf '%s\n' "$((count + 1))" >"${count_file}"
    [[ "${count}" -ge 1 ]] || exit 1
    grep -Fxv "${ref}" "${refs}" >"${refs}.next" || true
    mv "${refs}.next" "${refs}"
    ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  printf '%s\n%s\n' "acr-e2e.local/acr-api-run:v1" "unrelated.example/keep:v1" >"${state_root}/refs"
  output="$(PATH="${fake_bin}:${PATH}" FAKE_REFS="${state_root}/refs" FAKE_STATE="${state_root}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    run_id=run
    imported_image_refs=("acr-e2e.local/acr-api-run:v1")
    cleanup_kind_images
    printf "attempts=%s\n" "$(cat "${FAKE_STATE}/remove-count")"
    cat "${FAKE_REFS}"
  ' -- "${HARNESS}")"
  rm -rf "${state_root}"
  [[ "${output}" == $'attempts=2\nunrelated.example/keep:v1' ]] || fail 'cleanup did not retry tracked removal while preserving unrelated refs'
}

test_cleanup_preserves_untracked_same_run_alias() {
  local state_root fake_bin output
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*) cat "${FAKE_REFS}" ;;
  *" ctr -n k8s.io images rm "*)
    ref="${*: -1}"
    grep -Fxv "${ref}" "${FAKE_REFS}" >"${FAKE_REFS}.next" || true
    mv "${FAKE_REFS}.next" "${FAKE_REFS}"
    ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  printf '%s\n%s\n' \
    'acr-e2e.local/acr-api-run:v1' \
    'acr-e2e.local/acr-api-run:preexisting-alias' >"${state_root}/refs"
  output="$(PATH="${fake_bin}:${PATH}" FAKE_REFS="${state_root}/refs" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    run_id=run
    kind_image_refs_before="acr-e2e.local/acr-api-run:preexisting-alias"
    imported_image_refs=("acr-e2e.local/acr-api-run:v1")
    cleanup_kind_images
    cat "${FAKE_REFS}"
  ' -- "${HARNESS}")"
  rm -rf "${state_root}"
  [[ "${output}" == 'acr-e2e.local/acr-api-run:preexisting-alias' ]] || fail 'cleanup removed an untracked same-run alias'
}

test_cleanup_recovers_from_transient_list_failure() {
  local state_root fake_bin
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*)
    count="$(cat "${FAKE_STATE}/list-count" 2>/dev/null || printf 0)"
    printf '%s\n' "$((count + 1))" >"${FAKE_STATE}/list-count"
    [[ "${count}" -ge 1 ]] || exit 1
    cat "${FAKE_REFS}"
    ;;
  *" ctr -n k8s.io images rm "*)
    ref="${*: -1}"
    grep -Fxv "${ref}" "${FAKE_REFS}" >"${FAKE_REFS}.next" || true
    mv "${FAKE_REFS}.next" "${FAKE_REFS}"
    ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  printf '%s\n' 'acr-e2e.local/acr-api-run:v1' >"${state_root}/refs"
  if ! PATH="${fake_bin}:${PATH}" FAKE_REFS="${state_root}/refs" FAKE_STATE="${state_root}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    run_id=run
    imported_image_refs=("acr-e2e.local/acr-api-run:v1")
    cleanup_kind_images
  ' -- "${HARNESS}"; then
    rm -rf "${state_root}"
    fail 'cleanup did not recover from a transient Kind reference list failure'
  fi
  [[ ! -s "${state_root}/refs" ]] || fail 'cleanup left a tracked ref after transient list recovery'
  rm -rf "${state_root}"
}

test_cleanup_fails_after_exhausted_list_retries() {
  local state_root fake_bin
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*) exit 1 ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  if PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    cleanup_kind_images
  ' -- "${HARNESS}"; then
    rm -rf "${state_root}"
    fail 'cleanup succeeded after Kind reference list retries were exhausted'
  fi
  rm -rf "${state_root}"
}

test_cleanup_fails_for_untracked_created_delta() {
  local state_root fake_bin
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*) printf '%s\n' 'acr-e2e.local/acr-api-run:untracked-created-alias' ;;
  *" ctr -n k8s.io images rm "*) exit 1 ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  if PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    run_id=run
    kind_image_refs_before=""
    imported_image_refs=()
    cleanup_kind_images
  ' -- "${HARNESS}"; then
    rm -rf "${state_root}"
    fail 'cleanup accepted an untracked post-preflight Kind image alias'
  fi
  rm -rf "${state_root}"
}

test_prepare_run_rejects_alias_before_arming_cleanup() {
  local state_root fake_bin
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/kind" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' fake
EOF
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*) printf '%s\n' 'acr-e2e.local/acr-api-run:preexisting-alias' ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/kind" "${fake_bin}/docker"
  if PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    state_root="$2"
    shift 2
    source "${harness}"
    cluster=fake
    run_id=run
    require_tools() { :; }
    establish_source_guard() { :; }
    load_fixture_exports() { :; }
    assert_fixture_ready() { :; }
    kube() { printf "%s\n" "$*" >>"${state_root}/kube"; return 1; }
    on_exit() { : >"${state_root}/cleanup-armed"; }
    prepare_run
  ' -- "${HARNESS}" "${state_root}"; then
    rm -rf "${state_root}"
    fail 'prepare_run accepted a pre-existing same-run Kind alias'
  fi
  [[ ! -e "${state_root}/cleanup-armed" ]] || fail 'cleanup trap armed before ownership preflight'
  [[ "$(cat "${state_root}/kube")" == 'get namespace acr-run' ]] || fail 'prepare_run created resources before rejecting alias ownership'
  rm -rf "${state_root}"
}

test_partial_operations_reconcile_created_aliases() {
  local state_root fake_bin fake_root phase
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  fake_root="${state_root}/root"
  mkdir -p "${fake_bin}" "${fake_root}/scripts/container"
  : >"${state_root}/refs"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
digest='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
refs="${FAKE_REFS}"
case " $* " in
  *" version --format "*) printf 'amd64\n' ;;
  *" ctr -n k8s.io images list -q "*) cat "${refs}" ;;
  *" ctr -n k8s.io images import "*)
    printf 'import\n' >>"${FAKE_STATE}/operations"
    printf '%s\n%s\n' "acr-e2e.local/acr-api-run@${digest}" "acr-e2e.local/acr-api-run@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" >>"${refs}"
    [[ "${FAIL_PHASE}" != import ]] || exit 1
    ;;
  *" ctr -n k8s.io images tag "*)
    printf 'tag\n' >>"${FAKE_STATE}/operations"
    printf '%s\n' 'acr-e2e.local/acr-api-run:v1' >>"${refs}"
    [[ "${FAIL_PHASE}" != tag ]] || exit 1
    ;;
  *" ctr -n k8s.io images rm "*)
    ref="${*: -1}"
    grep -Fxv "${ref}" "${refs}" >"${refs}.next" || true
    mv "${refs}.next" "${refs}"
    ;;
  *" mkdir -p /var/lib/acr-e2e "*|*" rm -f /var/lib/acr-e2e/"*) : ;;
  *) : ;;
esac
EOF
  cat >"${fake_root}/scripts/container/build.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "${fake_bin}/docker" "${fake_root}/scripts/container/build.sh"
  for phase in import tag; do
    : >"${state_root}/refs"
    : >"${state_root}/operations"
    if PATH="${fake_bin}:${PATH}" FAKE_REFS="${state_root}/refs" FAKE_STATE="${state_root}" FAIL_PHASE="${phase}" ACR_E2E_LIB_ONLY=1 bash -c '
      harness="$1"
      root="$2"
      shift 2
      source "${harness}"
      REPO_ROOT="${root}"
      cluster=fake
      run_id=run
      run_dir="${TMPDIR:-/tmp}"
      assert_source_guard() { :; }
      image_digest_from_oci() { printf "%s\n" "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"; }
      trap cleanup_kind_images EXIT
      build_local_image v1
    ' -- "${HARNESS}" "${fake_root}"; then
      rm -rf "${state_root}"
      fail "partial ${phase} unexpectedly succeeded"
    fi
    [[ ! -s "${state_root}/refs" ]] || {
      rm -rf "${state_root}"
      fail "partial ${phase} left a tracked Kind alias after cleanup"
    }
    grep -Fxq "${phase}" "${state_root}/operations" || {
      rm -rf "${state_root}"
      fail "partial ${phase} did not reach the corresponding ctr operation"
    }
  done
  rm -rf "${state_root}"
}

test_cleanup_fails_when_owned_ref_remains() {
  local state_root fake_bin
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*) printf '%s\n' 'acr-e2e.local/acr-api-run@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' ;;
  *" ctr -n k8s.io images rm "*) exit 1 ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  if PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    run_id=run
    imported_image_refs=("acr-e2e.local/acr-api-run@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
    cleanup_kind_images
  ' -- "${HARNESS}"; then
    rm -rf "${state_root}"
    fail 'cleanup succeeded although a tracked ref remained after bounded retries'
  fi
  rm -rf "${state_root}"
}

test_import_tracking_records_every_new_alias
test_cleanup_retries_and_preserves_unrelated_refs
test_cleanup_preserves_untracked_same_run_alias
test_cleanup_recovers_from_transient_list_failure
test_cleanup_fails_after_exhausted_list_retries
test_cleanup_fails_for_untracked_created_delta
test_prepare_run_rejects_alias_before_arming_cleanup
test_partial_operations_reconcile_created_aliases
test_cleanup_fails_when_owned_ref_remains
printf 'RESULT: Kind Helm harness contract tests passed\n'
