#!/usr/bin/env bash
# Public-text check for pages that reach readers outside the team.
#
# Every file under docs/ must not carry text that only means something inside the
# project's own tooling: tracker ids (any letter case), drill record or lane names,
# named run ids, record checksums, sha256 digests that pin nothing, local filesystem
# paths or internal host names. The check names the file, the line and the class of
# every hit, and fails when it finds one.
#
# It also fails when it scans nothing: an empty docs/ tree, a listed file that does
# not exist or a glob typo cannot read as a pass.
#
# Exact-context exemptions (a public pin is not internal text): a 64-hex digest right
# after `@sha256:` (an OCI image reference) or on a line that says SHA-256 (a published
# asset checksum); the literal host names `host.docker.internal` and
# `acr.internal.example`.
#
# Usage: scripts/docs/verify-public-text.sh [--root DIR] [--list FILE]
#   --root DIR   Scan DIR/docs (default: the repository root).
#   --list FILE  Scan only the files named in FILE (paths relative to the root) instead
#                of everything under docs/. The fixtures use it.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$script_dir/../.." && pwd)"
list=""

while [ $# -gt 0 ]; do
  case "$1" in
    --root)
      [ $# -ge 2 ] || { printf 'error: --root requires a value\n' >&2; exit 2; }
      root="$2"
      shift 2
      ;;
    --list)
      [ $# -ge 2 ] || { printf 'error: --list requires a value\n' >&2; exit 2; }
      list="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,22p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      printf 'error: unknown argument %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

[ -d "$root" ] || { printf 'error: --root %s is not a directory\n' "$root" >&2; exit 2; }
root="$(cd "$root" && pwd)"
if [ -n "$list" ] && [ ! -f "$list" ]; then
  printf 'error: list %s does not exist\n' "$list" >&2
  exit 2
fi

# label~~extended regular expression~~sed expression that removes the exempt forms first
# (the backticks in the checksum class are literal)
# shellcheck disable=SC2016
classes=(
  'tracker id~~[Cc][Hh][Aa][Oo][Ss]-[0-9]+~~'
  'drill record name~~RECORD-[0-9]+~~'
  'lane name~~(^|[^A-Za-z0-9])lane-[0-9]+~~'
  'run id~~run-[a-z0-9]+-[0-9]+~~'
  'record checksum~~`[0-9a-f]{12}`~~'
  'sha256 digest~~(^|[^0-9A-Fa-f])[0-9a-f]{64}([^0-9A-Fa-f]|$)~~/[Ss][Hh][Aa]-256/d;s/@sha256:[0-9a-f]{64}//g'
  'local path~~(/var/lib/|/home/[a-z]|/Users/[A-Za-z]|/tmp/)~~'
  'internal host~~(bigboy|[A-Za-z0-9-]+\.internal([^A-Za-z0-9]|$))~~s/host\.docker\.internal//g;s/acr\.internal\.example//g'
)

scanned=0
fail_count=0
scan_file() {
  local entry="$1" file="$root/$1" class label rest pattern exempt hit
  scanned=$((scanned + 1))
  for class in "${classes[@]}"; do
    label="${class%%~~*}"
    rest="${class#*~~}"
    pattern="${rest%%~~*}"
    exempt="${rest#*~~}"
    while IFS= read -r hit; do
      [ -n "$hit" ] || continue
      printf 'FAIL: %s:%s: %s\n' "$entry" "${hit%%:*}" "$label" >&2
      fail_count=$((fail_count + 1))
    done < <(if [ -n "$exempt" ]; then
               grep -n '' "$file" | sed -E "s/^([0-9]+):/\1\x01/" | sed -E "$exempt" | grep -E -- "$pattern" | sed -E 's/\x01.*$//' || true
             else
               grep -nE -- "$pattern" "$file" | sed -E 's/^([0-9]+):.*$/\1/' || true
             fi)
  done
}

if [ -n "$list" ]; then
  while IFS= read -r entry || [ -n "$entry" ]; do
    case "$entry" in ''|\#*) continue ;; esac
    if [ ! -f "$root/$entry" ]; then
      printf 'FAIL: %s is listed but does not exist\n' "$entry" >&2
      fail_count=$((fail_count + 1))
      continue
    fi
    scan_file "$entry"
  done <"$list"
elif [ -d "$root/docs" ]; then
  while IFS= read -r -d '' file; do
    scan_file "${file#"$root"/}"
  done < <(find "$root/docs" -type f -print0 | sort -z)
fi

if [ "$scanned" -eq 0 ]; then
  printf 'FAIL: the list scanned 0 files; an empty scan is not a pass\n' >&2
  exit 1
fi
if [ "$fail_count" -ne 0 ]; then
  printf '%s public-text finding(s) in %s file(s)\n' "$fail_count" "$scanned" >&2
  exit 1
fi
printf 'ok: %s file(s) carry no tracker id, record name, lane name, run id, record checksum, local path or internal host\n' "$scanned"
