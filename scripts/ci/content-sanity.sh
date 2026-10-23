#!/usr/bin/env bash
set -euo pipefail

# content-sanity.sh asks what a change ADDS, which no test, vet or format gate
# does: a tool's side-effect output (a Go build cache, a codex log) reaching a
# commit is invisible to all of them.
#
# Usage: content-sanity.sh                 local: merge-base(origin/main, HEAD)..HEAD
#        content-sanity.sh <base> <head>   an explicit range
#
# In CI the checkout is the PR merge ref or the main push commit, fetched at
# depth 2, and the range is HEAD^1 HEAD. On a PR merge ref HEAD^1 is the base
# tip, so the range is the whole PR diff against base; on a main push HEAD is
# the squash commit and the range is exactly what that merge landed.
#
# Fails (exit 1) when an ADDED path (or a rename target):
#   - matches a tool-artifact name rule (artifact_reason below), or
#   - is ignored by .gitignore (a force-added file), or
# and, unless the override marker is present, when the range adds more than
# CONTENT_MAX_FILES files or CONTENT_MAX_BYTES bytes (defaults 100 / 1 MiB;
# the last 200 acr PRs added at most 28 files and 196 KB).
# Modified or deleted paths never trip it, so files tracked before a rule
# existed are not a problem. Pure renames add nothing.
#
# Override marker, a line in a commit message of the range or in the PR body:
#   Content-Check: allow-large <reason>
# The reason is required. The marker lifts the count and size limits only,
# never the name rules.
#
# The PR body is read from the event payload ($GITHUB_EVENT_PATH) unless
# CONTENT_CHECK_OVERRIDE_TEXT is set. Exit 2 is an operational error.

max_files="${CONTENT_MAX_FILES:-100}"
max_bytes="${CONTENT_MAX_BYTES:-1048576}"

usage() {
  echo "usage: ${0##*/} [<base> <head>]" >&2
  exit 2
}

case "$#" in
  0)
    git rev-parse --verify -q origin/main >/dev/null || { echo "content-sanity: origin/main not found; pass <base> <head>" >&2; exit 2; }
    base="$(git merge-base origin/main HEAD)"
    head="HEAD"
    ;;
  2)
    base="$1"
    head="$2"
    ;;
  *) usage ;;
esac
git rev-parse --verify -q "$base^{commit}" >/dev/null || { echo "content-sanity: cannot resolve base '$base'" >&2; exit 2; }
git rev-parse --verify -q "$head^{commit}" >/dev/null || { echo "content-sanity: cannot resolve head '$head'" >&2; exit 2; }

# artifact_reason prints why a path is a tool artifact; empty when it is not.
artifact_reason() {
  local p="$1"
  case "$p" in
    .gocache/* | */.gocache/*) echo "Go build cache (.gocache/)" ;;
    .gomodcache/* | */.gomodcache/*) echo "Go module cache (.gomodcache/)" ;;
    go-build/* | */go-build/*) echo "Go build cache (go-build/)" ;;
    .cache/* | */.cache/*) echo "tool cache (.cache/)" ;;
    node_modules/* | */node_modules/*) echo "node_modules/" ;;
    __pycache__/* | */__pycache__/*) echo "__pycache__/" ;;
    *.pyc) echo "compiled Python (*.pyc)" ;;
    .DS_Store | */.DS_Store) echo ".DS_Store" ;;
    .tmp/* | */.tmp/*) echo "scratch dir (.tmp/)" ;;
    .probe-tmp/* | */.probe-tmp/*) echo "scratch dir (.probe-tmp/)" ;;
    .trial-exchange*/* | */.trial-exchange*/*) echo "trial exchange scratch (.trial-exchange*/)" ;;
    prompt.md | */prompt.md) echo "codex scratch (prompt.md)" ;;
    .codex-review* | */.codex-review*) echo "codex scratch (.codex-review*)" ;;
    LEDGER.md | */LEDGER.md) echo "codex scratch (LEDGER.md)" ;;
    codex-gate* | */codex-gate*) echo "codex scratch (codex-gate*)" ;;
    */*) ;;
    *.log) echo "root-level log file (*.log)" ;;
  esac
}

added_paths=()
added_shas=()
target_paths=()
while IFS= read -r -d '' meta; do
  status="${meta##* }"
  IFS=' ' read -r _ mode_new _ sha_new _ <<<"${meta#:}"
  case "$status" in
    R* | C*)
      IFS= read -r -d '' _src
      IFS= read -r -d '' dst
      target_paths+=("$dst")
      ;;
    A)
      IFS= read -r -d '' dst
      target_paths+=("$dst")
      added_paths+=("$dst")
      if [ "$mode_new" = "160000" ]; then
        added_shas+=("")
      else
        added_shas+=("$sha_new")
      fi
      ;;
    *)
      IFS= read -r -d '' _skip
      ;;
  esac
done < <(git -c diff.renameLimit=5000 diff -z --raw -M --no-abbrev "$base" "$head")

n_files="${#added_paths[@]}"
total_bytes=0
if [ "$n_files" -gt 0 ]; then
  while IFS= read -r size; do
    total_bytes=$((total_bytes + size))
  done < <(
    for s in "${added_shas[@]}"; do
      if [ -n "$s" ]; then printf '%s\n' "$s"; fi
    done | git cat-file --batch-check='%(objectsize)'
  )
fi

problems=()
for p in "${target_paths[@]}"; do
  reason="$(artifact_reason "$p")"
  if [ -n "$reason" ]; then
    problems+=("tool artifact: $p ($reason)")
  fi
done

if [ "${#target_paths[@]}" -gt 0 ]; then
  ignored_rc=0
  ignored="$(printf '%s\0' "${target_paths[@]}" | git check-ignore --no-index -z --stdin | tr '\0' '\n')" || ignored_rc=$?
  if [ "$ignored_rc" -gt 1 ]; then
    echo "content-sanity: git check-ignore failed (rc=$ignored_rc)" >&2
    exit 2
  fi
  while IFS= read -r p; do
    if [ -n "$p" ]; then
      problems+=("ignored by .gitignore but added: $p")
    fi
  done <<<"$ignored"
fi

override_text="$(git log --format=%B "$base..$head" 2>/dev/null || true)"
if [ -n "${CONTENT_CHECK_OVERRIDE_TEXT+x}" ]; then
  override_text+=$'\n'"$CONTENT_CHECK_OVERRIDE_TEXT"
elif [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -r "$GITHUB_EVENT_PATH" ] && command -v jq >/dev/null 2>&1; then
  override_text+=$'\n'"$(jq -r '.pull_request.body // ""' "$GITHUB_EVENT_PATH")"
fi
override_text="${override_text//$'\r'/}"

override=0
bare_marker=0
if grep -qE '^Content-Check: allow-large[[:space:]]+[^[:space:]]' <<<"$override_text"; then
  override=1
elif grep -qE '^Content-Check: allow-large[[:space:]]*$' <<<"$override_text"; then
  bare_marker=1
fi

if [ "$override" -eq 0 ]; then
  if [ "$n_files" -gt "$max_files" ]; then
    hint=""
    if [ "$bare_marker" -eq 1 ]; then hint=" (the Content-Check marker has no reason)"; fi
    problems+=("adds $n_files files, over the limit of $max_files$hint")
  fi
  if [ "$total_bytes" -gt "$max_bytes" ]; then
    hint=""
    if [ "$bare_marker" -eq 1 ]; then hint=" (the Content-Check marker has no reason)"; fi
    problems+=("adds $total_bytes bytes, over the limit of $max_bytes$hint")
  fi
fi

stat_out="$(git -c diff.renameLimit=5000 diff -M --stat=120 --stat-count=60 "$base" "$head")"
summary="content-sanity: $base..$head adds $n_files file(s), $total_bytes byte(s) (limits $max_files / $max_bytes, override=$override)"
printf '%s\n' "$summary"
if [ -n "$stat_out" ]; then printf '%s\n' "$stat_out"; fi
fence='```'
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    printf '### Content sanity\n\n%s\n\n%s\n%s\n%s\n' "$summary" "$fence" "$stat_out" "$fence"
  } >>"$GITHUB_STEP_SUMMARY"
fi

if [ "${#problems[@]}" -gt 0 ]; then
  {
    echo "content-sanity: FAILED"
    for line in "${problems[@]}"; do printf '  %s\n' "$line"; done
    echo "Remove the files (do not use git add -A in a tool-shared worktree). For a legitimate large add, put"
    echo "'Content-Check: allow-large <reason>' in a commit message or the PR body; name rules cannot be overridden."
  } >&2
  exit 1
fi
echo "content-sanity: ok"
