#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: bash scripts/e2e/demo.sh [--repeat N] [--scenario NAME] [--out FILE]

Runs the deterministic public ACR evaluation fixture. Scenarios: default,
corrupt-hash, empty-evidence, mismatched-task.
EOF
}

repeat=1
scenario=default
out=

while (($# > 0)); do
  case "$1" in
    --repeat)
      repeat=${2:?--repeat requires a value}
      shift 2
      ;;
    --scenario)
      scenario=${2:?--scenario requires a value}
      shift 2
      ;;
    --out)
      out=${2:?--out requires a value}
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

args=(--repeat "$repeat" --scenario "$scenario")
if [[ -n "$out" ]]; then
  args+=(--out "$out")
fi

go run ./cmd/acr-eval-demo "${args[@]}"
