#!/usr/bin/env bash
set -euo pipefail

# verify-mirrored-images.sh (CHAOS-4855 R2, hardened R4, R5): resolves every
# entry from scripts/ci/resolve-mirrored-images.sh against the ghcr mirror
# (read-only `docker buildx imagetools inspect`) BEFORE any job that pulls
# one is allowed to start, so the ci.yml/mirror-images.yml bootstrap race
# (independent workflows on the same push, no cross-workflow `needs`) fails
# as ONE named error here instead of scattering across every pulling job.
# Shared by ci.yml's and release.yml's `mirror-preflight` jobs so the two
# workflows cannot silently diverge on what "the mirror is ready" means.
#
# For every digest-pinned entry this also compares the mirrored manifest's
# actual digest against the one the source ref names (not just "does the tag
# exist") -- see the MISMATCH branch below for why that distinction matters.
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
repo="${1:?usage: verify-mirrored-images.sh <owner/repo, e.g. \$GITHUB_REPOSITORY> [dispatch-ref]}"
# CHAOS-4889: optional second arg names the ref actually being built (e.g.
# release.yml's mirror-preflight passes the resolved release commit, which
# for an old-tag re-release is NOT main -- see mirror-images.yml's own
# CHAOS-4889 `ref` input). When set, a MISSING/MISMATCH error names the
# exact `gh workflow run` command an operator needs, instead of the generic
# "run Mirror images" hint that defaults to mirroring main's pins, which do
# not help an old tag whose pins were never mirrored. ci.yml's call passes
# no second arg and keeps the generic hint unchanged.
# The script has no way to tell whether dispatch_ref is an old tag's commit
# or simply the current main tip (release.yml passes the resolved release
# commit unconditionally, including on an ordinary main push) -- so the
# hint is worded conditionally rather than asserting "this is a re-release"
# outright.
dispatch_ref="${2:-}"
if [ -n "$dispatch_ref" ]; then
  dispatch_hint="if this is a re-release of an older ref, run \`gh workflow run mirror-images.yml -f ref=${dispatch_ref}\`; otherwise dispatch \"Mirror images\" (workflow_dispatch) for main -- then wait for it to complete and re-run this job"
else
  dispatch_hint='run "Mirror images" (workflow_dispatch) and wait for it to complete, then re-run this job'
fi

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
  mirrored="$(docker buildx imagetools inspect "$dest" --format '{{ .Manifest.Digest }}' 2>/dev/null || true)"
  if [ -z "$mirrored" ]; then
    printf '::error::MISSING %-40s -> %s -- %s\n' \
      "$image" "$dest" "$dispatch_hint"
    missing=1
    continue
  fi
  case "$image" in
    *@sha256:*)
      # CHAOS-4855 R5 (codex round 3, executed): mirror-images.yml tags every
      # digest-pinned image `mirror-<first-12-of-the-sha256>` (see that
      # workflow's own header for why), so two upstream digests that happen
      # to share a 12-hex prefix would collide on the SAME destination tag.
      # mirror-images.yml's own verify step catches that WITHIN the run that
      # caused it (the run fails), but does not undo the already-rewritten
      # tag -- an existence-only check here (the previous version) would
      # then print OK for a tag now pointing at the WRONG image on every
      # later run, forever, with no further signal. Comparing the resolved
      # digest against the one the source ref actually names closes that:
      # a rebound tag now fails HERE too, not just inside the mirror
      # workflow's own single run.
      want="${image##*@}"
      if [ "$mirrored" = "$want" ]; then
        printf 'OK      %-40s -> %s\n' "$image" "$dest"
      else
        printf '::error::MISMATCH %-40s -> %s has digest %s, want %s -- the mirror tag has been overwritten by a different image (see docs/container-images.md); do not just re-run the mirror workflow, the destination tag needs a fix\n' \
          "$image" "$dest" "$mirrored" "$want"
        missing=1
      fi
      ;;
    *)
      # Tag-pinned upstream (clickhouse, ryuk): no digest to compare
      # against; existence is all this can assert.
      printf 'OK      %-40s -> %s (tag-pinned upstream, digest %s)\n' "$image" "$dest" "$mirrored"
      ;;
  esac
done < "$images_file"

exit "$missing"
