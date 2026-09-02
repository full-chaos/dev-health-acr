#!/usr/bin/env bash
set -euo pipefail

# resolve-mirrored-images.sh prints "<upstream ref>\t<dest tag>" for every
# Docker Hub image acr CI pulls (CHAOS-4855), one line each. The single
# source of truth for BOTH consumers, so they cannot silently drift apart:
#   - .github/workflows/mirror-images.yml uses this list to know what to
#     mirror to ghcr.io/<owner>/<repo>.
#   - .github/workflows/ci.yml's mirror-preflight job uses the SAME list to
#     verify every mirrored manifest actually resolves before any job that
#     pulls one starts, so a not-yet-mirrored image fails as ONE named
#     preflight error instead of nine unrelated "manifest unknown" job
#     failures (the bootstrap-race shape hit by CHAOS-4855's own introducing
#     PR: ci.yml and mirror-images.yml are independent workflows on the same
#     push, so ci's jobs can start and pull before the mirror job's push
#     lands).
#
# Sourced from the same files the consumers read (Dockerfile, chfixture.go,
# scan.sh, verify.sh, ci.yml) rather than restated as bare strings, so this
# list cannot silently drift from what CI actually pulls -- the extraction
# fails closed (`|| die`) if any pattern stops matching.
#
# dest tag is `mirror-<first-12-of-the-sha256>` for every digest-pinned ref
# (every consumer resolves those by full digest, so the destination tag is
# never actually looked up) or the bare upstream tag for the two entries
# with no digest at all (clickhouse, by design -- CHAOS-4549 -- and ryuk,
# testcontainers-go's own hardcoded default). See mirror-images.yml's header
# comment for why the destination namespace and tag scheme are what they are.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

die() { printf '::error::%s\n' "$*" >&2; exit 1; }

golang_ref="$(grep -oE 'golang:[^[:space:]]+@sha256:[0-9a-f]{64}' Dockerfile | head -n1)" \
  || die "could not resolve the golang base image from Dockerfile"
clickhouse_ref="$(grep -oE 'const Image = "clickhouse/clickhouse-server:[^"]+"' internal/chfixture/chfixture.go \
  | sed -E 's/.*"(.*)"/\1/')"
[ -n "$clickhouse_ref" ] || die "could not resolve ClickHouseImage from internal/chfixture/chfixture.go"
trivy_ref="$(grep -oE "aquasec/trivy:[^\"']+@sha256:[0-9a-f]{64}" scripts/container/scan.sh | head -n1)" \
  || die "could not resolve the trivy image from scripts/container/scan.sh"
syft_ref="$(grep -oE "anchore/syft:[^\"']+@sha256:[0-9a-f]{64}" scripts/container/scan.sh | head -n1)" \
  || die "could not resolve the syft image from scripts/container/scan.sh"
postgres_ref="$(grep -oE 'postgres:[^[:space:]"]+@sha256:[0-9a-f]{64}' scripts/container/verify.sh | head -n1)" \
  || die "could not resolve the postgres image from scripts/container/verify.sh"
binfmt_ref="$(grep -oE 'tonistiigi/binfmt:[^[:space:]]+@sha256:[0-9a-f]{64}' .github/workflows/ci.yml | head -n1)" \
  || die "could not resolve the tonistiigi/binfmt image from ci.yml"
buildkit_ref="$(grep -oE 'moby/buildkit:[^[:space:]]+@sha256:[0-9a-f]{64}' .github/workflows/ci.yml | head -n1)" \
  || die "could not resolve the moby/buildkit image from ci.yml"
# falkordb has no chfixture-style single constant (CHAOS-4855 PR body: six
# call sites all pin the same digest); read the first one and let the
# digest-agreement contract test (scripts/ci/test-workflow-contract.sh) keep
# the rest honest.
falkordb_ref="$(grep -rhoE 'falkordb/falkordb@sha256:[0-9a-f]{64}' \
  cmd/acr-projector internal/contextfabric/falkorgraph deploy/compose | sort -u | head -n1)" \
  || die "could not resolve the falkordb image from any test/compose source"
# testcontainers-go's own hardcoded reaper default (internal/config.
# ReaperDefaultImage in the pinned module version) -- not declared anywhere
# in this repo's source, so it is restated here rather than extracted.
ryuk_ref='testcontainers/ryuk:0.14.0'

# dest_tag prints "mirror-<first-12-of-the-sha256>" for a digest-pinned ref,
# or the bare upstream tag if there is no digest (clickhouse, ryuk).
dest_tag() {
  case "$1" in
    *@sha256:*) printf 'mirror-%s\n' "${1##*sha256:}" | cut -c1-19 ;;
    *:*) printf '%s\n' "${1##*:}" ;;
    *) die "image has neither a digest nor a tag: $1" ;;
  esac
}

for image in \
  "$golang_ref" "$clickhouse_ref" "$trivy_ref" "$syft_ref" "$postgres_ref" \
  "$binfmt_ref" "$buildkit_ref" "$falkordb_ref" "$ryuk_ref"; do
  printf '%s\t%s\n' "$image" "$(dest_tag "$image")"
done
