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
  'docker.io/library/golang:1.26.6-alpine3.23|sha256:5978cc992ad5ef96a7469713c8af849c1433824761ce3be2c56381403cd8d9a3'
  'gcr.io/distroless/static-debian12:nonroot|sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a'
  'cgr.dev/chainguard/git:latest|sha256:1d0957e6ec5f9586d91ded20999b1c029d4b24107d20b409fbb0992ed164d8f6'
  'docker.io/tonistiigi/binfmt:qemu-v10.2.3|sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0'
  'docker.io/moby/buildkit:v0.31.0|sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'
  'docker.io/aquasec/trivy:0.69.3|sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c'
  'docker.io/anchore/syft:v1.46.0|sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
  'docker.io/library/postgres:18-alpine|sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44'
  'docker.io/clickhouse/clickhouse-server:latest|sha256:f90a77560f72b10802106ee49e9870e41668cbc496e280c3911f6e3b216657f3'
  'docker.io/edoburu/pgbouncer:latest|sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd'
  'docker.io/valkey/valkey:9-alpine|sha256:ee91f7a174ac4d6a6b0685b3a60e321f0a9dbbb691f9b0e285be2ba1d1be8328'
  'docker.io/axllent/mailpit:latest|sha256:d5ecbb067db3705fa953d79e1b7f81ef84038df67aba6c52825d8c02a1ea748a'
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
