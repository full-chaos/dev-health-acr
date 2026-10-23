#!/usr/bin/env bash
set -euo pipefail

# Opt-in git pre-push hook: runs the content sanity gate over what this branch
# adds on top of origin/main. Install with `make install-pre-push-hook`, which
# copies this file into the hooks directory; it writes no git config. The hook
# reads its range from origin/main, not from the refs git passes on stdin.
root="$(git rev-parse --show-toplevel)"
exec "$root/scripts/ci/content-sanity.sh"
