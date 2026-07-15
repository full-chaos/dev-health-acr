#!/usr/bin/env bash
# Offline documentation verifier for container/backlog claims.
#
# Validates, without Docker or network access:
#   - README.md links docs/container-images.md and lists the exact
#     container verification commands.
#   - docs/implementation-backlog.md records the superseded
#     acr-developer-deployment plan's container Todo 8 as complete and
#     its local Compose Todo 9 as pending.
#   - every local (non-URL) Markdown link in README.md and docs/*.md
#     resolves to a real file.
#   - every `make <target>` snippet referenced in README.md or
#     docs/container-images.md names a target that exists in Makefile.
#   - docs/container-images.md never claims local Compose behavior is
#     merged, verified, or complete (Compose remains pending elsewhere).
#   - no tracked Markdown file contains the forbidden public-publication
#     claim about ACR packaging.
#
# Usage: scripts/docs/verify.sh [--root DIR]
#
# --root DIR   Check DIR instead of the repository root that contains
#              this script. Used to validate disposable fixture trees;
#              DIR need not be a full checkout (e.g. a copied docs/ tree
#              is enough to exercise the forbidden-claim check).
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
default_root="$(cd "$script_dir/../.." && pwd)"
root="$default_root"

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
    -h|--help)
      sed -n '2,25p' "${BASH_SOURCE[0]}"
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

fail_count=0
fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fail_count=$((fail_count + 1))
}
ok() {
  printf 'ok: %s\n' "$1"
}

readme="$root/README.md"
container_doc="$root/docs/container-images.md"
backlog="$root/docs/implementation-backlog.md"
makefile="$root/Makefile"

# --- 1. Forbidden public-publication claim -----------------------------
# Runs first and unconditionally over every tracked Markdown file under
# the checked root, independent of whether README/backlog/Makefile exist,
# so a disposable partial fixture tree (e.g. a copied docs/ directory)
# still exercises this guard.
forbidden_sentence='ACR is packaged by dev-health-ops and publicly published.'
forbidden_hits=""
while IFS= read -r -d '' md_file; do
  if grep -qF "$forbidden_sentence" "$md_file"; then
    forbidden_hits="$forbidden_hits${forbidden_hits:+ }${md_file#"$root"/}"
  fi
done < <(find "$root" -name '*.md' \
  -not -path '*/.git/*' \
  -not -path '*/.omo/*' \
  -not -path '*/.tmp/*' \
  -not -path '*/node_modules/*' \
  -not -path '*/vendor/*' \
  -print0 2>/dev/null)

if [ -n "$forbidden_hits" ]; then
  fail "forbidden publication claim present in: $forbidden_hits"
else
  ok "no forbidden publication claim found"
fi

# --- 2. README links docs/container-images.md and lists commands -------
required_commands="make container-contract
make container-pins
make container-test
make container-oci
make container-scan
make container-reproducible"

if [ ! -f "$readme" ]; then
  fail "README.md missing at $root"
else
  if grep -qE '\(docs/container-images\.md\)' "$readme"; then
    ok "README.md links docs/container-images.md"
  else
    fail "README.md does not link docs/container-images.md"
  fi

  while IFS= read -r cmd; do
    [ -n "$cmd" ] || continue
    if grep -qF "$cmd" "$readme"; then
      ok "README.md lists verification command: $cmd"
    else
      fail "README.md is missing verification command: $cmd"
    fi
  done <<EOF
$required_commands
EOF
fi

# --- 3. docs/container-images.md exists and is linkable -----------------
if [ ! -f "$container_doc" ]; then
  fail "docs/container-images.md missing at $root"
else
  ok "docs/container-images.md exists"

  # Compose must never be documented as merged/verified/complete here;
  # Compose acceptance is a separate, still-pending todo (acr-project-
  # completion Todo 7 / superseded acr-developer-deployment Todo 9).
  if grep -inE 'compose[^.]{0,40}(complete|merged|verified|tested|passing)' "$container_doc" \
      || grep -inE '(complete|merged|verified|tested|passing)[^.]{0,40}compose' "$container_doc"; then
    fail "docs/container-images.md appears to claim local Compose behavior is done"
  else
    ok "docs/container-images.md does not claim Compose completion"
  fi
fi

# --- 4. Backlog records Todo 8 complete / Todo 9 pending -----------------
if [ ! -f "$backlog" ]; then
  fail "docs/implementation-backlog.md missing at $root"
else
  # Flatten prose whitespace (including line wraps) so a phrase split
  # across Markdown source lines is still matched as one statement.
  backlog_flat="$(tr '\n' ' ' < "$backlog" | tr -s ' ')"

  if printf '%s' "$backlog_flat" | grep -qE 'Todo 8.{0,200}\bcomplete\b' \
      || printf '%s' "$backlog_flat" | grep -qiE 'complete.{0,200}Todo 8'; then
    ok "docs/implementation-backlog.md records Todo 8 complete"
  else
    fail "docs/implementation-backlog.md does not record superseded deployment plan Todo 8 as complete"
  fi

  if printf '%s' "$backlog_flat" | grep -qE 'Todo 9.{0,200}\bpending\b' \
      || printf '%s' "$backlog_flat" | grep -qiE 'pending.{0,200}Todo 9'; then
    ok "docs/implementation-backlog.md records Todo 9 pending"
  else
    fail "docs/implementation-backlog.md does not record superseded deployment plan Todo 9 as pending"
  fi
fi

# --- 5. Local Markdown links resolve -------------------------------------
check_links_in_file() {
  file="$1"
  file_dir="$(dirname "$file")"
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    case "$target" in
      http://*|https://*|mailto:*|\#*) continue ;;
    esac
    clean_target="${target%%#*}"
    [ -n "$clean_target" ] || continue
    if [ ! -e "$file_dir/$clean_target" ] && [ ! -e "$root/$clean_target" ]; then
      fail "${file#"$root"/}: broken local link -> $target"
    fi
  done < <(grep -oE '\]\([^)]+\)' "$file" | sed -E 's/^\]\(//; s/\)$//')
}

link_check_files=""
[ -f "$readme" ] && link_check_files="$link_check_files $readme"
if [ -d "$root/docs" ]; then
  while IFS= read -r -d '' f; do
    link_check_files="$link_check_files $f"
  done < <(find "$root/docs" -maxdepth 1 -name '*.md' -print0)
fi

if [ -n "$link_check_files" ]; then
  link_failures_before=$fail_count
  for f in $link_check_files; do
    check_links_in_file "$f"
  done
  if [ "$fail_count" -eq "$link_failures_before" ]; then
    ok "all local Markdown links resolve"
  fi
fi

# --- 6. make snippets reference real Makefile targets ---------------------
if [ -f "$makefile" ]; then
  snippet_files=""
  [ -f "$readme" ] && snippet_files="$snippet_files $readme"
  [ -f "$container_doc" ] && snippet_files="$snippet_files $container_doc"
  snippet_failures_before=$fail_count
  for f in $snippet_files; do
    while IFS= read -r target; do
      [ -n "$target" ] || continue
      if ! grep -qE "^${target}:" "$makefile"; then
        fail "${f#"$root"/}: references undefined make target '$target'"
      fi
    done < <(grep -oE 'make [a-zA-Z0-9_-]+' "$f" | awk '{print $2}' | sort -u)
  done
  if [ -n "$snippet_files" ] && [ "$fail_count" -eq "$snippet_failures_before" ]; then
    ok "all referenced make targets exist in Makefile"
  fi
else
  ok "Makefile not present under --root; skipping make-target snippet check"
fi

if [ "$fail_count" -ne 0 ]; then
  printf '\ndocs verification FAILED (%s check(s))\n' "$fail_count" >&2
  exit 1
fi

printf '\ndocs verification OK\n'
