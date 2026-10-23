#!/usr/bin/env bash
set -euo pipefail

# Fixture test for content-sanity.sh on throwaway git repos that carry this
# repository's .gitignore. Run with SCRIPT=<other script> to prove the test goes
# red against a gate that lacks a rule. After the real run it also mutates the
# gate, one rule at a time, and requires the same cases to fail against each
# mutant: a case that cannot tell the gate from a mutant proves nothing.

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
real="${SCRIPT:-$here/content-sanity.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fails=0
quiet=0

# new_repo <dir>: a repo with the repo .gitignore and one base commit on `main`.
new_repo() {
  local d="$1"
  rm -rf "$d"
  mkdir -p "$d"
  git -C "$d" init -q -b main
  git -C "$d" config user.email t@example.com
  git -C "$d" config user.name t
  git -C "$d" config commit.gpgsign false
  cp "$root/.gitignore" "$d/.gitignore"
  mkdir -p "$d/pkg"
  printf 'package pkg\n' >"$d/pkg/a.go"
  git -C "$d" add -A
  git -C "$d" commit -q -m base
}

# commit_all <repo> <message>: commit the working tree, force-adding what the
# case created (the point is to land files .gitignore would normally refuse).
commit_all() {
  git -C "$1" add -A -f
  git -C "$1" commit -q -m "$2"
}

# expect <name> <want_rc> <pattern> <repo> [env...]: run the gate on base..HEAD.
expect() {
  local name="$1" want="$2" pat="$3" repo="$4" rc=0 out
  shift 4
  out="$(cd "$repo" && env "$@" "$script" "$(git rev-parse main)" HEAD 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ] || ! grep -qE -- "$pat" <<<"$out"; then
    fails=$((fails + 1))
    if [ "$quiet" -eq 0 ]; then
      echo "FAIL $name: rc=$rc want=$want pattern='$pat'"
      printf '    %s\n' "${out//$'\n'/$'\n    '}"
    fi
  elif [ "$quiet" -eq 0 ]; then
    echo "ok   $name"
  fi
}

scenario() { # <name>: fresh repo on a topic branch
  r="$tmp/$1"
  new_repo "$r"
  git -C "$r" checkout -q -b topic
}

many_files() { # <dir> <count>
  local i
  mkdir -p "$1"
  for ((i = 1; i <= $2; i++)); do printf 'x%d\n' "$i" >"$1/f$i.txt"; done
}

run_cases() {
  # ---- must PASS ----
  scenario clean
  mkdir -p "$r/pkg" && printf 'package pkg\n' >"$r/pkg/b.go" && commit_all "$r" "add b"
  expect clean-add 0 'content-sanity: ok' "$r"
  expect clean-add-prints-stat 0 'pkg/b.go' "$r"

  scenario nested-log
  mkdir -p "$r/testdata" && printf 'l\n' >"$r/testdata/x.log" && commit_all "$r" "nested log"
  expect nested-log-is-not-root 0 'content-sanity: ok' "$r"

  scenario files-at-limit
  many_files "$r/data" 100 && commit_all "$r" "100 files"
  expect exactly-100-files 0 'adds 100 file' "$r"

  scenario bytes-at-limit
  head -c 1048576 /dev/zero | tr '\0' 'a' >"$r/big.txt" && commit_all "$r" "1 MiB"
  expect exactly-1-MiB 0 'content-sanity: ok' "$r"

  scenario marker-lifts-count
  many_files "$r/data" 150 && git -C "$r" add -A && git -C "$r" commit -q -m "vendored drop" -m "Content-Check: allow-large vendored e2e manifests"
  expect marker-with-reason-lifts-count 0 'override=1' "$r"

  scenario marker-in-pr-body
  many_files "$r/data" 150 && commit_all "$r" "drop"
  expect marker-in-override-text 0 'override=1' "$r" CONTENT_CHECK_OVERRIDE_TEXT=$'text\nContent-Check: allow-large generated goldens\nmore'
  printf '{"pull_request":{"body":"intro\\nContent-Check: allow-large from the event payload\\n"}}' >"$tmp/event.json"
  expect marker-in-event-payload 0 'override=1' "$r" GITHUB_EVENT_PATH="$tmp/event.json"

  scenario pure-rename
  many_files "$r/old" 150 && git -C "$r" add -A && git -C "$r" commit -q -m "old"
  git -C "$r" branch -f base-with-old
  git -C "$r" mv old new && git -C "$r" commit -q -m "rename"
  out_rc=0
  out="$(cd "$r" && "$script" base-with-old HEAD 2>&1)" || out_rc=$?
  if [ "$out_rc" -ne 0 ] || ! grep -q 'adds 0 file' <<<"$out"; then
    fails=$((fails + 1)); if [ "$quiet" -eq 0 ]; then echo "FAIL pure-rename-of-150-files: rc=$out_rc"; printf '    %s\n' "$out"; fi
  elif [ "$quiet" -eq 0 ]; then echo "ok   pure-rename-of-150-files"; fi

  scenario delete-artifact
  mkdir -p "$r/.gocache" && printf 'c\n' >"$r/.gocache/c" && git -C "$r" add -A -f && git -C "$r" commit -q -m "old artifact"
  git -C "$r" branch -f base-with-artifact
  git -C "$r" rm -q -r .gocache && git -C "$r" commit -q -m "remove it"
  out_rc=0
  out="$(cd "$r" && "$script" base-with-artifact HEAD 2>&1)" || out_rc=$?
  if [ "$out_rc" -ne 0 ]; then
    fails=$((fails + 1)); if [ "$quiet" -eq 0 ]; then echo "FAIL delete-of-an-artifact: rc=$out_rc"; printf '    %s\n' "$out"; fi
  elif [ "$quiet" -eq 0 ]; then echo "ok   delete-of-an-artifact"; fi

  scenario modify-tracked-ignored
  mkdir -p "$r/.omo/evidence" && printf 'one\n' >"$r/.omo/evidence/e.md" && git -C "$r" add -A -f && git -C "$r" commit -q -m "grandfathered"
  git -C "$r" branch -f base-with-omo
  printf 'two\n' >"$r/.omo/evidence/e.md" && git -C "$r" commit -q -a -m "modify it"
  out_rc=0
  out="$(cd "$r" && "$script" base-with-omo HEAD 2>&1)" || out_rc=$?
  if [ "$out_rc" -ne 0 ]; then
    fails=$((fails + 1)); if [ "$quiet" -eq 0 ]; then echo "FAIL modify-of-a-tracked-ignored-file: rc=$out_rc"; printf '    %s\n' "$out"; fi
  elif [ "$quiet" -eq 0 ]; then echo "ok   modify-of-a-tracked-ignored-file"; fi

  scenario empty-range
  expect empty-range 0 'adds 0 file' "$r"

  scenario odd-names
  printf 'a\n' >"$r/with space.txt" && printf 'b\n' >"$r/$(printf 'new\nline.txt')" && commit_all "$r" "odd names"
  expect space-and-newline-names 0 'adds 2 file' "$r"

  # ---- must FAIL ----
  scenario gocache
  mkdir -p "$r/.gocache/aa" && printf 'c\n' >"$r/.gocache/aa/obj" && commit_all "$r" "cache"
  expect gocache-root 1 'tool artifact: .gocache/aa/obj' "$r"

  scenario gocache-nested
  mkdir -p "$r/sub/.gocache" && printf 'c\n' >"$r/sub/.gocache/obj" && commit_all "$r" "cache"
  expect gocache-nested 1 'Go build cache' "$r"

  scenario go-build-dir
  mkdir -p "$r/tmp/go-build" && printf 'c\n' >"$r/tmp/go-build/obj" && commit_all "$r" "cache"
  expect go-build-dir 1 'go-build/' "$r"

  scenario root-log
  printf 'l\n' >"$r/app.log" && commit_all "$r" "log"
  expect root-log 1 'root-level log file' "$r"

  scenario codex-gate
  printf 'l\n' >"$r/codex-gate-r9.md" && commit_all "$r" "codex"
  expect codex-gate-name 1 'codex scratch \(codex-gate' "$r"

  scenario codex-prompt
  mkdir -p "$r/sub" && printf 'p\n' >"$r/sub/prompt.md" && commit_all "$r" "prompt"
  expect codex-prompt-md 1 'codex scratch \(prompt.md\)' "$r"

  scenario codex-patch
  printf 'p\n' >"$r/.codex-review.patch" && commit_all "$r" "patch"
  expect codex-review-patch 1 'codex-review' "$r"

  scenario ledger
  printf 'p\n' >"$r/LEDGER.md" && commit_all "$r" "ledger"
  expect ledger-md 1 'LEDGER.md' "$r"

  scenario node-modules
  mkdir -p "$r/web/node_modules/x" && printf 'p\n' >"$r/web/node_modules/x/i.js" && commit_all "$r" "nm"
  expect node-modules 1 'node_modules/' "$r"

  scenario force-added-ignored
  mkdir -p "$r/.omo" && printf 'e\n' >"$r/.omo/e.md" && commit_all "$r" "omo"
  expect force-added-ignored-file 1 'ignored by .gitignore but added: .omo/e.md' "$r"

  scenario rename-into-artifact
  printf 'x\n' >"$r/pkg/c.go" && git -C "$r" add -A && git -C "$r" commit -q -m "c"
  git -C "$r" branch -f base-with-c
  mkdir -p "$r/.gocache" && git -C "$r" mv pkg/c.go .gocache/c.go && git -C "$r" commit -q -m "hide it"
  out_rc=0
  out="$(cd "$r" && "$script" base-with-c HEAD 2>&1)" || out_rc=$?
  if [ "$out_rc" -ne 1 ] || ! grep -q 'tool artifact: .gocache/c.go' <<<"$out"; then
    fails=$((fails + 1)); if [ "$quiet" -eq 0 ]; then echo "FAIL rename-into-an-artifact-path: rc=$out_rc"; printf '    %s\n' "$out"; fi
  elif [ "$quiet" -eq 0 ]; then echo "ok   rename-into-an-artifact-path"; fi

  scenario too-many-files
  many_files "$r/data" 101 && commit_all "$r" "101 files"
  expect 101-files 1 'adds 101 files, over the limit of 100' "$r"

  scenario too-many-bytes
  head -c 1048577 /dev/zero | tr '\0' 'a' >"$r/big.txt" && commit_all "$r" "1 MiB + 1"
  expect one-MiB-plus-one 1 'adds 1048577 bytes, over the limit of 1048576' "$r"

  scenario env-limits
  many_files "$r/data" 3 && commit_all "$r" "3 files"
  expect env-lowers-file-limit 1 'over the limit of 2' "$r" CONTENT_MAX_FILES=2
  expect env-lowers-byte-limit 1 'over the limit of 5' "$r" CONTENT_MAX_BYTES=5

  scenario bare-marker
  many_files "$r/data" 150 && git -C "$r" add -A && git -C "$r" commit -q -m "drop" -m "Content-Check: allow-large"
  expect marker-without-reason 1 'marker has no reason' "$r"

  scenario marker-not-for-names
  many_files "$r/data" 150 && mkdir -p "$r/.gocache" && printf 'c\n' >"$r/.gocache/c" && git -C "$r" add -A -f && git -C "$r" commit -q -m "drop" -m "Content-Check: allow-large reason given"
  expect marker-does-not-lift-name-rules 1 'tool artifact: .gocache/c' "$r"

  scenario marker-mid-line
  many_files "$r/data" 150 && git -C "$r" add -A && git -C "$r" commit -q -m "drop" -m "see Content-Check: allow-large because"
  expect marker-must-start-a-line 1 'over the limit' "$r"

  scenario bad-ref
  out_rc=0
  out="$(cd "$r" && "$script" no-such-ref HEAD 2>&1)" || out_rc=$?
  if [ "$out_rc" -ne 2 ] || ! grep -q 'cannot resolve' <<<"$out"; then
    fails=$((fails + 1)); if [ "$quiet" -eq 0 ]; then echo "FAIL unresolvable-base-is-exit-2: rc=$out_rc"; printf '    %s\n' "$out"; fi
  elif [ "$quiet" -eq 0 ]; then echo "ok   unresolvable-base-is-exit-2"; fi
}

script="$real"
run_cases
if [ "$fails" -ne 0 ]; then
  echo "content-sanity test: $fails case(s) failed against $script" >&2
  exit 1
fi
echo "ok   all cases pass against $script"

# ---- negative controls: each mutant must fail at least one case ----
mutate() { # <name> <sed-expression>
  local name="$1" expr="$2" m="$tmp/mutant-$1.sh"
  sed -E "$expr" "$real" >"$m"
  chmod +x "$m"
  bash -n "$m" || { echo "NEGATIVE CONTROL SETUP FAILED: mutant '$name' is not valid bash" >&2; exit 1; }
  if cmp -s "$real" "$m"; then
    echo "NEGATIVE CONTROL SETUP FAILED: '$name' did not change the gate" >&2
    exit 1
  fi
  script="$m"
  fails=0
  quiet=1
  run_cases
  quiet=0
  if [ "$fails" -eq 0 ]; then
    echo "NEGATIVE CONTROL FAILED: the cases cannot tell the gate from mutant '$name'" >&2
    exit 1
  fi
  echo "ok   mutant '$name' fails $fails case(s)"
}

# shellcheck disable=SC2016
if [ -z "${SCRIPT:-}" ]; then
  mutate no-name-rules 's/^artifact_reason\(\) \{/artifact_reason() { return 0/'
  mutate no-gocache-rule 's/^    \.gocache\/\* \| \*\/\.gocache\/\*\) echo.*$//'
  mutate no-root-log-rule 's/^    \*\.log\) echo.*$//'
  mutate no-check-ignore 's/git check-ignore --no-index -z --stdin/cat >\/dev\/null; true/'
  mutate no-file-limit 's/if \[ "\$n_files" -gt "\$max_files" \]/if false/'
  mutate no-byte-limit 's/if \[ "\$total_bytes" -gt "\$max_bytes" \]/if false/'
  mutate off-by-one-files 's/"\$n_files" -gt "\$max_files"/"$n_files" -ge "$max_files"/'
  mutate off-by-one-bytes 's/"\$total_bytes" -gt "\$max_bytes"/"$total_bytes" -ge "$max_bytes"/'
  mutate bare-marker-accepted 's/\[\[:space:\]\]\+\[\^\[:space:\]\]/[[:space:]]*/'
  mutate marker-anywhere "s/grep -qE '\\^Content-Check: allow-large/grep -qE 'Content-Check: allow-large/"
  mutate no-rename-detection 's/ -M --no-abbrev/ --no-renames --no-abbrev/'
  mutate renames-skip-name-rules 's/^      target_paths\+=\("\$dst"\)$/      :/'
fi
echo "content-sanity test: ok"
