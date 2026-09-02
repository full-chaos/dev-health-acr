#!/usr/bin/env bash
set -euo pipefail

# verify-mirrored-images.sh (CHAOS-4855 R2, hardened R4): resolves every
# entry from scripts/ci/resolve-mirrored-images.sh against the ghcr mirror
# (read-only `docker buildx imagetools inspect`) BEFORE any job that pulls
# one is allowed to start, so the ci.yml/mirror-images.yml bootstrap race
# (independent workflows on the same push, no cross-workflow `needs`) fails
# as ONE named error here instead of scattering across every pulling job.
# Shared by ci.yml's and release.yml's `mirror-preflight` jobs so the two
# workflows cannot silently diverge on what "the mirror is ready" means.
#
# CHAOS-4855 R4 (codex round 2, executed): the first version of this check
# was inlined per-workflow as `while ... done < <(bash resolve-mirrored-
# images.sh)`. Process substitution's exit status is NOT visible to the
# enclosing shell -- `set -e` does not fire if the substituted command
# fails, so a broken resolver (e.g. a source file moved, breaking one of its
# `grep` patterns) printed its own `::error::` to stderr and the surrounding
# loop still finished with `missing=0` and exit 0: a fully broken preflight
# reading as green. Reproduced: `resolve-mirrored-images.sh` edited to fail
# on purpose still exited the old inline block 0. Fixed by capturing the
# resolver's output to a real file FIRST, under `set -o pipefail`-independent
# sequencing (a plain command, not a substitution), so its exit status is
# checked directly before anything reads the file.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
repo="${1:?usage: verify-mirrored-images.sh <owner/repo, e.g. \$GITHUB_REPOSITORY>}"

images_file="$(mktemp)"
trap 'rm -f "$images_file"' EXIT

if ! bash "${repo_root}/scripts/ci/resolve-mirrored-images.sh" > "$images_file"; then
  printf '::error::scripts/ci/resolve-mirrored-images.sh failed -- cannot verify the mirror; see its output above\n' >&2
  exit 1
fi

missing=0
while IFS=$'\t' read -r image dest_tag; do
  [ -n "$image" ] || continue
  ref_repo="${image%%@*}"
  ref_repo="${ref_repo%%:*}"
  dest="ghcr.io/${repo}/${ref_repo}:${dest_tag}"
  if docker buildx imagetools inspect "$dest" >/dev/null 2>&1; then
    printf 'OK      %-40s -> %s\n' "$image" "$dest"
  else
    printf '::error::MISSING %-40s -> %s -- run "Mirror images" (workflow_dispatch) and wait for it to complete, then re-run this job\n' \
      "$image" "$dest"
    missing=1
  fi
done < "$images_file"

exit "$missing"
