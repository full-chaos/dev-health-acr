#!/usr/bin/env bash
set -euo pipefail

command -v docker >/dev/null || { printf 'docker is required\n' >&2; exit 1; }

# This command intentionally remains an on-demand review tool rather than a CI
# gate. Several reviewed tags, including cgr.dev/chainguard/git:latest, move as
# part of their normal upstream release cadence. The Dockerfile and deployment
# inputs remain digest-pinned; run this command when explicitly refreshing those
# immutable pins, not on every unrelated change.
pins=(
  'docker.io/docker/dockerfile:1.20|sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d'
  'docker.io/library/golang:1.26.5-alpine3.23|sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc'
  'gcr.io/distroless/static-debian12:nonroot|sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35'
  'cgr.dev/chainguard/git:latest|sha256:0690687448b0beca8c84f52a26ddc3b088623f617def56348194fde48f8573cd'
  'docker.io/tonistiigi/binfmt:qemu-v10.2.3|sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0'
  'docker.io/moby/buildkit:v0.31.0|sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'
  'docker.io/anchore/syft:v1.46.0|sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
  'docker.io/library/postgres:18-alpine|sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15'
  'docker.io/clickhouse/clickhouse-server:latest|sha256:d7556a3841027651307b5aa08d72b5c467d0241d3db5b67d9e158ef3975626f5'
  'docker.io/edoburu/pgbouncer:latest|sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd'
  'docker.io/valkey/valkey:9-alpine|sha256:ee91f7a174ac4d6a6b0685b3a60e321f0a9dbbb691f9b0e285be2ba1d1be8328'
  'docker.io/axllent/mailpit:latest|sha256:b868afa176bfd6cce2323ea316cd99ccad77915e51e595748f6d786700ecf109'
  'docker.io/library/nginx:1.27-alpine|sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10'
)

for pin in "${pins[@]}"; do
  reference="${pin%%|*}"
  expected="${pin#*|}"
  inspection="$(docker buildx imagetools inspect "$reference")"
  actual="$(awk '$1 == "Digest:" { digest = $2 } END { print digest }' <<<"$inspection")"
  if [[ "$actual" != "$expected" ]]; then
    printf 'tag digest changed for %s: expected %s, got %s\n' "$reference" "$expected" "${actual:-missing}" >&2
    exit 1
  fi
  printf 'verified %s@%s\n' "$reference" "$expected"
done
