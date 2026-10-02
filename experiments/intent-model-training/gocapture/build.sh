#!/usr/bin/env bash
# The only supported build of gocapture (SPEC-incumbent-capture.md §2).
# Writes bin/gocapture and bin/gocapture.build.json.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
cd "$root"
exp="experiments/intent-model-training"
out="$exp/bin/gocapture"
tags=()
if [[ "${1:-}" == "--loopback" ]]; then
  # End-to-end test build only: loopback provider allowed (gocapture/loopback.go).
  out="$exp/bin/gocapture-loopback"
  tags=(-tags gocapture_loopback)
fi
pin="$(git rev-parse HEAD)"
status="$(git status --porcelain -- internal cmd go.mod go.sum)"
clean=true
if [[ -n "$status" ]] || ! git diff --quiet HEAD -- internal cmd go.mod go.sum; then clean=false; fi
mkdir -p "$exp/bin"
go build ${tags[@]+"${tags[@]}"} -o "$out" "./$exp/gocapture"
binsha="$(shasum -a 256 "$out" | cut -d' ' -f1)"
gosum="$(shasum -a 256 go.sum | cut -d' ' -f1)"
expsrc="$( (cd "$exp" && find gocapture internal/interpreq -type f ! -name '*.build.json' | LC_ALL=C sort | while read -r f; do printf '%s\n' "$f"; cat "$f"; done) | shasum -a 256 | cut -d' ' -f1)"
modified="$(git status --porcelain | cut -c4- | LC_ALL=C sort | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))')"
python3 - "$out.build.json" <<PY
import json, sys
json.dump({
  "schema": "gocapture.build.v1",
  "binary_sha256": "$binsha",
  "go_version": "$(go env GOVERSION)",
  "built_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source_pin": "$pin",
  "production_tree_clean": $( [[ $clean == true ]] && echo True || echo False ),
  "go_sum_sha256": "$gosum",
  "experiment_source_sha256": "$expsrc",
  "vcs_modified_paths": $modified,
}, open(sys.argv[1], "w"), indent=1, sort_keys=True)
PY
chmod 600 "$out.build.json"
echo "built $out ($binsha)"
