#!/usr/bin/env bash
# Public-text check for pages that reach readers outside the team.
#
# A page listed in scripts/docs/public-text-files.txt must not carry text that
# only means something inside the project's own tooling: tracker ids, drill
# record or lane names, run ids, record checksums, local filesystem paths or
# internal host names. The check names the file, the line and the class of
# every hit, and fails on the first run that finds one.
#
# It also fails when it scans nothing: an empty list, a listed file that does
# not exist or a glob typo cannot read as a pass.
#
# Usage: scripts/docs/verify-public-text.sh [--root DIR] [--list FILE]
#   --root DIR   Resolve the listed files against DIR instead of the repository root.
#   --list FILE  Read the file list from FILE instead of scripts/docs/public-text-files.txt.
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
      sed -n '2,19p' "${BASH_SOURCE[0]}"
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
[ -n "$list" ] || list="$script_dir/public-text-files.txt"
[ -f "$list" ] || { printf 'error: list %s does not exist\n' "$list" >&2; exit 2; }

# class|extended regular expression (the backticks in the checksum class are literal)
# shellcheck disable=SC2016
classes=(
  'tracker id|CHAOS-[0-9]+'
  'drill record name|RECORD-[0-9]+'
  'lane name|(^|[^A-Za-z0-9])lane-[0-9]+'
  'run id|run-[a-z0-9]+-[0-9]+'
  'record checksum|`[0-9a-f]{12}`'
  'local path|(/var/lib/|/home/[a-z]|/Users/[A-Za-z]|/tmp/)'
  'internal host|(bigboy|[A-Za-z0-9-]+\.internal([^A-Za-z0-9]|$))'
)

scanned=0
fail_count=0
while IFS= read -r entry || [ -n "$entry" ]; do
  case "$entry" in ''|\#*) continue ;; esac
  file="$root/$entry"
  if [ ! -f "$file" ]; then
    printf 'FAIL: %s is listed but does not exist\n' "$entry" >&2
    fail_count=$((fail_count + 1))
    continue
  fi
  scanned=$((scanned + 1))
  for class in "${classes[@]}"; do
    label="${class%%|*}"
    pattern="${class#*|}"
    while IFS= read -r hit; do
      [ -n "$hit" ] || continue
      printf 'FAIL: %s:%s: %s\n' "$entry" "${hit%%:*}" "$label" >&2
      fail_count=$((fail_count + 1))
    done < <(grep -nE -- "$pattern" "$file" || true)
  done
done <"$list"

if [ "$scanned" -eq 0 ]; then
  printf 'FAIL: the list scanned 0 files; an empty scan is not a pass\n' >&2
  exit 1
fi
if [ "$fail_count" -ne 0 ]; then
  printf '%s public-text finding(s) in %s file(s)\n' "$fail_count" "$scanned" >&2
  exit 1
fi
printf 'ok: %s file(s) carry no tracker id, record name, lane name, run id, record checksum, local path or internal host\n' "$scanned"
