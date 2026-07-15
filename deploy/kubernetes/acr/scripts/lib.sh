#!/usr/bin/env bash

set -euo pipefail

ACR_KUSTOMIZE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  printf '%s\n' "error: $*" >&2
  exit 1
}

require_overlay() {
  local overlay="$1"
  [[ -n "$overlay" ]] || fail "--overlay is required"
  [[ -d "$ACR_KUSTOMIZE_ROOT/overlays/$overlay" ]] || fail "unknown overlay: $overlay"
}

require_digest_image() {
  local image="$1"
  [[ "$image" =~ ^[a-zA-Z0-9._/-]+@sha256:[a-f0-9]{64}$ ]] || fail "--image must be an immutable image digest"
}

kustomize_build() {
  local directory="$1"
  if command -v kustomize >/dev/null 2>&1; then
    kustomize build "$directory"
    return
  fi
  command -v kubectl >/dev/null 2>&1 || fail "kustomize or kubectl is required"
  kubectl kustomize "$directory"
}

render_manifest() {
  local overlay="$1"
  local image="$2"
  local scope="$3"
  local work directory

  require_overlay "$overlay"
  if [[ -n "$image" ]]; then
    require_digest_image "$image"
  fi

  work="$(mktemp -d "${TMPDIR:-/tmp}/acr-kustomize.XXXXXX")"
  cp -R "$ACR_KUSTOMIZE_ROOT" "$work/acr"
  directory="$work/acr/overlays/$overlay"

  if [[ -n "$image" ]]; then
    case "$scope" in
      all)
        cat >> "$directory/kustomization.yaml" <<EOF
  - target:
      group: apps
      version: v1
      kind: Deployment
      name: acr-api
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/image
        value: $image
  - target:
      group: batch
      version: v1
      kind: Job
      name: acr-migrate
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/image
        value: $image
EOF
        ;;
      application)
        cat >> "$directory/kustomization.yaml" <<EOF
  - target:
      group: apps
      version: v1
      kind: Deployment
      name: acr-api
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/image
        value: $image
EOF
        ;;
      *)
        fail "unknown render scope: $scope"
        ;;
    esac
  fi

  if ! kustomize_build "$directory"; then
    rm -rf "$work"
    return 1
  fi
  rm -rf "$work"
}

select_kinds() {
  local wanted="$1"
  awk -v wanted=" $wanted " '
    function emit(  count, lines, i, kind) {
      count = split(document, lines, "\n")
      for (i = 1; i <= count; i++) {
        if (lines[i] ~ /^kind: /) {
          kind = lines[i]
          sub(/^kind: /, "", kind)
          if (index(wanted, " " kind " ") > 0) {
            printf "%s---\n", document
          }
          return
        }
      }
    }
    /^---[[:space:]]*$/ {
      emit()
      document = ""
      next
    }
    { document = document $0 "\n" }
    END { emit() }
  '
}

overlay_namespace() {
  local overlay="$1"
  case "$overlay" in
    development) printf '%s\n' acr-development ;;
    staging) printf '%s\n' acr-staging ;;
    production) printf '%s\n' acr-production ;;
    *) fail "unknown overlay: $overlay" ;;
  esac
}
