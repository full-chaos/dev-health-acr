#!/usr/bin/env bash
# Offline-at-test-time render check for every fenced ```mermaid block
# under docs/**.
#
# A diagram that mermaid's own parser rejects is worse than no diagram: it
# renders as a broken block for every reader and nobody notices until they
# hit it by eye. This script renders every fenced mermaid block with
# mermaid-cli (mmdc) and fails loudly, naming the file, the block's index,
# its start line, and mmdc's own parse error, on the first one that does
# not render. It asserts the number of blocks it checked is greater than
# zero, so a glob typo or an empty docs/ tree cannot read as a pass.
#
# Makes no network call itself: it uses whatever mmdc the caller already
# installed (node_modules/.bin/mmdc from a prior `npm install`, or --mmdc)
# or falls back to a pinned `npx`, which only ever fetches the version
# recorded in mermaid-cli-version.txt.
#
# Usage: scripts/docs/verify-mermaid.sh [--root DIR] [--mmdc PATH]
#   --root DIR   Check DIR/docs instead of the repository root's docs/.
#                Used to validate disposable fixture trees (DIR need not be
#                a full checkout -- a docs/ subtree is enough).
#   --mmdc PATH  Explicit mmdc binary/command to use instead of the
#                node_modules/.bin/mmdc or pinned-npx defaults.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
default_root="$(cd "$script_dir/../.." && pwd)"
root="$default_root"
mmdc_override=""

while [ $# -gt 0 ]; do
  case "$1" in
    --root)
      [ $# -ge 2 ] || { printf 'error: --root requires a value\n' >&2; exit 2; }
      root="$2"
      shift 2
      ;;
    --root=*)
      root="${1#--root=}"
      shift
      ;;
    --mmdc)
      [ $# -ge 2 ] || { printf 'error: --mmdc requires a value\n' >&2; exit 2; }
      mmdc_override="$2"
      shift 2
      ;;
    --mmdc=*)
      mmdc_override="${1#--mmdc=}"
      shift
      ;;
    -h|--help)
      sed -n '2,21p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      printf 'error: unknown argument %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [ ! -d "$root" ]; then
  printf 'error: --root %s is not a directory\n' "$root" >&2
  exit 2
fi
root="$(cd "$root" && pwd)"
docs_dir="$root/docs"
if [ ! -d "$docs_dir" ]; then
  printf 'error: no docs/ directory under %s\n' "$root" >&2
  exit 2
fi

pinned_version="$(tr -d '[:space:]' < "$script_dir/mermaid-cli-version.txt")"
puppeteer_config="$script_dir/mermaid-puppeteer-config.json"
if [ ! -f "$puppeteer_config" ]; then
  printf 'error: missing puppeteer config: %s\n' "$puppeteer_config" >&2
  exit 2
fi

declare -a mmdc_cmd
if [ -n "$mmdc_override" ]; then
  # shellcheck disable=SC2206
  mmdc_cmd=($mmdc_override)
elif [ -x "$default_root/node_modules/.bin/mmdc" ]; then
  mmdc_cmd=("$default_root/node_modules/.bin/mmdc")
else
  mmdc_cmd=(npx --yes "@mermaid-js/mermaid-cli@${pinned_version}")
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/blocks" "$work/out"

mapfile -t md_files < <(find "$docs_dir" -type f -name '*.md' | LC_ALL=C sort)

# One awk pass over every Markdown file: extracts each fenced ```mermaid
# block to its own numbered file under $work/blocks and prints a manifest
# row "file<TAB>block-index<TAB>start-line<TAB>first-line" per block. A
# single invocation (not one per file) lets a plain running counter number
# blocks uniquely across the whole docs/ tree without passing state back
# out of a subshell.
if [ "${#md_files[@]}" -gt 0 ]; then
  awk -v work="$work" '
    FNR == 1 { in_block = 0 }
    /^```mermaid[[:space:]]*$/ && !in_block {
      in_block = 1
      global_idx++
      body = ""
      first = ""
      start_line = FNR + 1
      next
    }
    in_block && /^```[[:space:]]*$/ {
      in_block = 0
      out = work "/blocks/" global_idx ".mmd"
      printf "%s", body > out
      close(out)
      printf "%s\t%d\t%d\t%s\n", FILENAME, global_idx, start_line, first
      next
    }
    in_block {
      if (first == "") { first = $0 }
      body = body $0 "\n"
      next
    }
  ' "${md_files[@]}" > "$work/manifest.tsv"
fi

block_count=0
fail_count=0

if [ -s "$work/manifest.tsv" ]; then
  while IFS=$'\t' read -r file idx start_line first_line; do
    [ -n "$idx" ] || continue
    block_count=$((block_count + 1))
    rel_file="${file#"$root"/}"
    log="$work/out/${idx}.log"
    if "${mmdc_cmd[@]}" -i "$work/blocks/${idx}.mmd" -o "$work/out/${idx}.svg" \
        -p "$puppeteer_config" >"$log" 2>&1; then
      printf 'ok: %s block #%s (line %s) renders\n' "$rel_file" "$idx" "$start_line"
    else
      fail_count=$((fail_count + 1))
      printf 'FAIL: %s block #%s (line %s, first line: %s) failed to render\n' \
        "$rel_file" "$idx" "$start_line" "$first_line" >&2
      sed 's/^/    /' "$log" >&2
    fi
  done < "$work/manifest.tsv"
fi

if [ "$block_count" -eq 0 ]; then
  printf 'FAIL: found zero fenced ```mermaid blocks under %s -- this check exercised nothing\n' \
    "$docs_dir" >&2
  exit 1
fi

if [ "$fail_count" -ne 0 ]; then
  printf '\nmermaid render check FAILED (%s of %s block(s) failed to render)\n' \
    "$fail_count" "$block_count" >&2
  exit 1
fi

printf '\nmermaid render check OK (%s block(s) rendered)\n' "$block_count"
