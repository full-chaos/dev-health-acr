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
  'docker.io/library/golang:1.27.3-alpine3.23|sha256:8a5046497cda39860b832ffdbe8dd66018a0f05bb01352f533a36e7e2ced66a0'
  'gcr.io/distroless/static-debian12:nonroot|sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab'
  'cgr.dev/chainguard/git:latest|sha256:f30343d11878c992336af4f150dc5e2ab6200e4e5bc5e3a366aebb1665abbf65'
  'docker.io/tonistiigi/binfmt:qemu-v10.2.3|sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0'
  'docker.io/moby/buildkit:v0.31.0|sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'
  'docker.io/aquasec/trivy:0.69.3|sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c'
  'docker.io/anchore/syft:v1.46.0|sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
  'docker.io/library/postgres:18-alpine|sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5'
  'docker.io/clickhouse/clickhouse-server:latest|sha256:739e099abd1157390960a59e4532d21781d8fde57e6e4151cb920ea02c30b1ce'
  'docker.io/edoburu/pgbouncer:latest|sha256:8638aa563607256d3c7440437534195f1066a92b68015d89ae9d6b935b444a6d'
  'docker.io/valkey/valkey:9-alpine|sha256:bf09d58e96d6b4524a15396b65b242bbe18beebbcc15a04d8ee75ddac62fae0b'
  'docker.io/axllent/mailpit:latest|sha256:9b3a463be15354c0f0eba91697d80e3e0d8a5623ef0e627cacc836bc17fd5231'
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
