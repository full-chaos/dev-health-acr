#!/usr/bin/env bash
set -euo pipefail

command -v docker >/dev/null || { printf 'docker is required\n' >&2; exit 1; }

pins=(
  'docker.io/docker/dockerfile:1.20|sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d'
  'docker.io/library/golang:1.26.5-alpine3.23|sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc'
  'gcr.io/distroless/static-debian12:nonroot|sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b'
  'cgr.dev/chainguard/git:latest|sha256:80e0a917dd1e89a57adaded0df96fd3b9f51f68773afb618ca72551dc7da2516'
  'docker.io/tonistiigi/binfmt:qemu-v10.2.3|sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0'
  'docker.io/moby/buildkit:v0.31.0|sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'
  'docker.io/aquasec/trivy:0.69.3|sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c'
  'docker.io/anchore/syft:v1.46.0|sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
  'docker.io/library/postgres:17-alpine|sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193'
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
