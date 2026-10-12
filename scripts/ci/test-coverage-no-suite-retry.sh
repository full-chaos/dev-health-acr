#!/usr/bin/env bash
set -euo pipefail

# Fails if `make test-coverage` wraps the gotestsum TEST RUN in retry.sh (a
# deterministic red would re-run the whole package suite once per attempt) or
# uses --rerun-fails (it overwrites the -coverprofile). The tool prefetch
# (`gotestsum ... --version`) may stay under retry.sh.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

recipe="$(make -n test-coverage GOTEST_PKGS=./... 2>&1)"

run_lines="$(printf '%s\n' "$recipe" | grep -F 'gotestsum' | grep -F -- '--junitfile' || true)"
if [ -z "$run_lines" ]; then
  echo "FAIL: no gotestsum test-run line found in make -n test-coverage" >&2
  exit 1
fi
if printf '%s\n' "$run_lines" | grep -Fq 'retry.sh'; then
  echo "FAIL: gotestsum test run is wrapped in retry.sh" >&2
  printf '%s\n' "$run_lines" >&2
  exit 1
fi
if printf '%s\n' "$recipe" | grep -Fq -- '--rerun-fails'; then
  echo "FAIL: --rerun-fails overwrites the coverage profile" >&2
  exit 1
fi
if ! printf '%s\n' "$recipe" | grep -F 'retry.sh' | grep -F 'gotestsum' | grep -Fq -- '--version'; then
  echo "FAIL: gotestsum prefetch is not under retry.sh" >&2
  exit 1
fi
echo "ok: test-coverage retries only the gotestsum fetch"
