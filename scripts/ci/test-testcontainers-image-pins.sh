#!/usr/bin/env bash
set -euo pipefail

# test-testcontainers-image-pins.sh (CHAOS-4855 R3): mirror-preflight and
# resolve-mirrored-images.sh both verify the images THIS SCRIPT'S AUTHOR
# already knew to list. Neither one notices a NEW or reintroduced bare-tag
# testcontainers image reference anywhere else in the module -- exactly the
# gap that produced 20 unpinned postgres:18-alpine call sites and one
# unpinned edoburu/pgbouncer:latest call site, invisible to the mirror list
# until each one hit `manifest unknown` in CI.
#
# This is the closing check: it enumerates EVERY testcontainers image
# reference in the module via `git grep` (tracked files only, not a `rg`
# filesystem walk -- a reference in an untracked scratch file is not
# something CI runs), resolves each one to an image ref, and asserts BOTH
# properties every consumer above depends on:
#   1. the ref is digest-pinned (`@sha256:...`), so
#      TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves it by digest rather
#      than a bare tag the mirror never publishes under -- OR it is the one
#      documented exception (clickhouse/clickhouse-server via
#      internal/chfixture.Image, CHAOS-4549: tag-pinned by design, no
#      digest to have);
#   2. that digest (or, for the one exception, that exact tag) actually
#      appears in scripts/ci/resolve-mirrored-images.sh's output -- so a
#      site can be digest-pinned and STILL fail here if nobody added it to
#      the mirror list.
#
# Three reference shapes are enumerated, matching every shape found in this
# module today:
#   (a) tcpostgres.Run(ctx, "<image>", ...)               -- direct literal
#   (b) Image: "<image>"                                  -- direct literal
#   (c) Image: <identifier>                               -- resolved via a
#       `const/var <identifier> = "<image>"` declaration somewhere in the
#       same package directory
# A reference this script cannot resolve to a literal string FAILS CLOSED
# (reported as unresolved, not silently skipped) -- an unrecognised shape is
# exactly the case a mirror-list edit could miss.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

offenders=()

# mirrored_refs holds the resolvable dimension of what's actually mirrored:
# one line per mirror-list entry, "<digest-or-bare-ref>". Digest-pinned
# entries are recorded by their bare sha256 hex (so any repo/tag wrapping a
# matching digest counts as covered); the one non-digest entry (clickhouse)
# is recorded by its full ref text instead, since there is no digest to key
# on and its EXACT tag is what must match.
mirrored_refs="$(bash scripts/ci/resolve-mirrored-images.sh | cut -f1)"
mirrored_digests="$(printf '%s\n' "$mirrored_refs" | grep -oE 'sha256:[0-9a-f]{64}' | sort -u)"

is_mirrored_digest() {
  printf '%s\n' "$mirrored_digests" | grep -qxF "$1"
}

is_mirrored_exact_ref() {
  printf '%s\n' "$mirrored_refs" | grep -qxF "$1"
}

# resolve_identifier looks up `const <ident> = "<value>"` or
# `<ident> = "<value>"` (bare package-level var, same shape minus `const`).
#
# CHAOS-4855 R5 (codex round 3, executed): the first version searched the
# whole package directory and took the FIRST match found by `git grep`,
# with no regard for which declaration is actually in scope at the
# reference site. Repro: a package-level `const testImage = "<pinned>"`
# alongside a function-LOCAL `const testImage = "postgres:18-alpine"` that
# shadows it within the very function containing the `Image: testImage`
# reference -- valid Go (an inner scope may shadow an outer one) -- left the
# gate green while the actual runtime reference resolved to the unpinned
# local shadow.
#
# This does not implement real Go scope resolution, but narrows the gap to
# the one shape that matters here: if $file (optional; the file containing
# the reference) and $refline (its line number) are given, prefer the
# CLOSEST matching declaration in THAT SAME FILE at or before $refline --
# Go requires a local declaration to precede its use textually, so the
# nearest preceding same-file declaration is always at least as specific as
# any cross-file package-level one, and correctly picks up a local shadow
# instead of skipping past it. Falls back to the old directory-wide search
# (for the legitimate cross-file package-level case, e.g. falkordbImage
# referenced from a sibling file) only when the file has no match at all.
resolve_identifier() {
  local dir="$1" ident="$2" file="${3:-}" refline="${4:-}" value=""
  if [ -n "$file" ] && [ -n "$refline" ]; then
    value="$(git grep -nE "(const|var)[[:space:]]+${ident}[[:space:]]*=[[:space:]]*\"[^\"]+\"" -- "$file" 2>/dev/null \
      | awk -F: -v maxline="$refline" '$2 <= maxline { print }' \
      | sort -t: -k2 -n | tail -n1 \
      | sed -E 's/.*"(.*)"/\1/')"
  fi
  if [ -z "$value" ]; then
    value="$(git grep -hoE "(const|var)[[:space:]]+${ident}[[:space:]]*=[[:space:]]*\"[^\"]+\"" -- "${dir}"'/*.go' 2>/dev/null \
      | sed -E 's/.*"(.*)"/\1/' | head -n1)"
  fi
  printf '%s' "$value"
}

check_literal() {
  local file="$1" line="$2" image="$3"
  case "$image" in
    *@sha256:*)
      local digest="${image##*@}"
      if ! is_mirrored_digest "$digest"; then
        offenders+=("${file}:${line}: image \"${image}\" is digest-pinned but its digest is NOT in the mirror list (scripts/ci/resolve-mirrored-images.sh) -- add it there before this can pass")
      fi
      ;;
    *)
      offenders+=("${file}:${line}: image \"${image}\" is not digest-pinned -- TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX would resolve this by its bare tag, which the mirror never publishes under (see docs/container-images.md)")
      ;;
  esac
}

# (a) tcpostgres.Run(<any first-arg expression>, "<image>" | <identifier>, ...)
# CHAOS-4855 R4 (codex round 2): the earlier pattern hardcoded the literal
# variable name "ctx" and a literal string as the SECOND argument -- a call
# spelled tcpostgres.Run(context.Background(), barePostgres) (a different
# context expression, an identifier instead of an inline literal) matched
# neither this pattern nor pattern (c) below (which only looks at `Image:`
# struct fields, not tcpostgres.Run's positional args) and was invisible to
# the gate entirely. Executed: injecting exactly that call into a scratch
# file left the gate green. Fixed by matching any first-argument expression
# with no embedded comma, then trying a literal extraction first and an
# identifier resolution second.
while IFS=: read -r file line rest; do
  [ -n "$file" ] || continue
  image="$(printf '%s' "$rest" | sed -E 's/.*tcpostgres\.Run\([^,]+,[[:space:]]*"([^"]+)".*/\1/')"
  if [ -n "$image" ] && [ "$image" != "$rest" ]; then
    check_literal "$file" "$line" "$image"
    continue
  fi
  ident="$(printf '%s' "$rest" | sed -E 's/.*tcpostgres\.Run\([^,]+,[[:space:]]*([A-Za-z_][A-Za-z0-9_.]*)[,)].*/\1/')"
  if [ -n "$ident" ] && [ "$ident" != "$rest" ]; then
    dir="$(dirname "$file")"
    value="$(resolve_identifier "$dir" "$ident" "$file" "$line")"
    if [ -z "$value" ]; then
      offenders+=("${file}:${line}: could not resolve identifier \"${ident}\" passed to tcpostgres.Run to a const/var string in ${dir} -- investigate manually")
      continue
    fi
    check_literal "$file" "$line" "$value"
    continue
  fi
  offenders+=("${file}:${line}: could not extract the image argument from a tcpostgres.Run(...) call -- investigate manually (a new call shape this gate does not recognise is exactly what it exists to catch)")
done < <(git grep -nE 'tcpostgres\.Run\(' -- '*.go')

# (b) Image: "<image>" -- direct string literal
while IFS=: read -r file line rest; do
  [ -n "$file" ] || continue
  image="$(printf '%s' "$rest" | sed -E 's/.*Image:[[:space:]]*"([^"]+)".*/\1/')"
  [ -n "$image" ] || { offenders+=("${file}:${line}: could not extract the image literal from an Image: \"...\" field -- investigate manually"); continue; }
  check_literal "$file" "$line" "$image"
done < <(git grep -nE 'Image:[[:space:]]*"[^"]+"' -- '*.go')

# (c) Image: <identifier> -- resolved via a const/var declaration in the
# same package directory (or, for a qualified reference like
# chfixture.Image, that package's own directory).
while IFS=: read -r file line rest; do
  [ -n "$file" ] || continue
  ident="$(printf '%s' "$rest" | sed -E 's/.*Image:[[:space:]]*([A-Za-z_][A-Za-z0-9_.]*),.*/\1/')"
  [ -n "$ident" ] || { offenders+=("${file}:${line}: could not extract the identifier from an Image: <ident> field -- investigate manually"); continue; }

  case "$ident" in
    chfixture.Image)
      # THE ONE DOCUMENTED EXCEPTION (CHAOS-4549): tag-pinned by design, no
      # digest. Must match resolve-mirrored-images.sh's clickhouse entry
      # EXACTLY -- there is no digest to fall back on if it does not.
      value="$(resolve_identifier internal/chfixture Image)"
      [ -n "$value" ] || { offenders+=("${file}:${line}: could not resolve chfixture.Image from internal/chfixture/chfixture.go -- investigate manually"); continue; }
      if ! is_mirrored_exact_ref "$value"; then
        offenders+=("${file}:${line}: image \"${value}\" (chfixture.Image) does not match scripts/ci/resolve-mirrored-images.sh's clickhouse entry exactly -- a tag-only pin must match verbatim, there is no digest to fall back on")
      fi
      ;;
    *)
      dir="$(dirname "$file")"
      value="$(resolve_identifier "$dir" "$ident" "$file" "$line")"
      if [ -z "$value" ]; then
        offenders+=("${file}:${line}: could not resolve identifier \"${ident}\" to a const/var string in ${dir} -- investigate manually (a new reference shape this gate does not recognise is exactly what it exists to catch)")
        continue
      fi
      check_literal "$file" "$line" "$value"
      ;;
  esac
done < <(git grep -nE 'Image:[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*,' -- '*.go' | grep -vE 'Image:[[:space:]]*"')

if [ "${#offenders[@]}" -gt 0 ]; then
  printf '%s\n' "${offenders[@]}" >&2
  printf '\n%d testcontainers image reference(s) failed the pin/mirror check\n' "${#offenders[@]}" >&2
  exit 1
fi

printf 'PASS: every testcontainers image reference in the module is digest-pinned (or the documented clickhouse exception) and present in the mirror list\n'
