#!/bin/sh
# Re-vendors the ops change failure rule and the deployment status meaning it
# uses, byte for byte except the header and the one import path.
# Usage: vendor.sh <ops checkout> <ops commit sha>
set -eu
ops="$1"; sha="$2"
here="$(cd "$(dirname "$0")" && pwd)"
src=internal/jobs/metrics
{
  printf '// Code generated from dev-health-ops %s/changefailure/changefailure.go\n' "$src"
  printf '// at commit %s (CHAOS-9621). DO NOT EDIT: the\n' "$sha"
  printf '// file is the differential oracle of the change failure rule; the digest test\n'
  printf '// in devhealthfacts pins its bytes, and the rule test executes it.\n\n'
  git -C "$ops" show "$sha:$src/changefailure/changefailure.go" |
    sed 's#github.com/full-chaos/dev-health-ops/internal/jobs/metrics/deploymentstatus#github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts/internal/deploymentstatus#; s#^package changefailure$#package opschangefailure#'
} > "$here/changefailure.go"
{
  printf '// Code generated from dev-health-ops %s/deploymentstatus/deploymentstatus.go\n' "$src"
  printf '// at commit %s (CHAOS-9621). DO NOT EDIT.\n\n' "$sha"
  git -C "$ops" show "$sha:$src/deploymentstatus/deploymentstatus.go"
} > "$here/../deploymentstatus/deploymentstatus.go"
sha256sum "$here/changefailure.go" "$here/../deploymentstatus/deploymentstatus.go"
