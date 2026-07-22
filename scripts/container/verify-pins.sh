#!/usr/bin/env bash
set -euo pipefail

command -v docker >/dev/null || { printf 'docker is required\n' >&2; exit 1; }

pins=(
  'docker.io/docker/dockerfile:1.20|sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d'
  'docker.io/library/golang:1.26.5-alpine3.23|sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc'
  'gcr.io/distroless/static-debian12:nonroot|sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35'
  'cgr.dev/chainguard/git:latest|sha256:62135ba579e1ac309441b3de21983230a4979603ab879bafa6d2773e8d5fd626'
  'docker.io/tonistiigi/binfmt:qemu-v10.2.3|sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0'
  'docker.io/moby/buildkit:v0.31.0|sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'
  'docker.io/aquasec/trivy:0.69.3|sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c'
  'docker.io/anchore/syft:v1.46.0|sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
  'docker.io/library/postgres:17-alpine|sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193'
  'docker.io/library/postgres:18-alpine|sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15'
  'docker.io/clickhouse/clickhouse-server:latest|sha256:1d1f6508eba2dccce2cee9913907c5f7766327debc57a6b1991f2c9e3176c163'
  'docker.io/edoburu/pgbouncer:latest|sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd'
  'docker.io/valkey/valkey:9-alpine|sha256:c9b77919daeba2c02ad954d0c844cc4e7142069d177b89c5fd771f405daf9e02'
  'docker.io/axllent/mailpit:latest|sha256:5a49a77c5bdbe7c5474450b4f46348d09949df3695257729c93a30369382d4f6'
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
