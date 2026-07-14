#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

stable_root="${tmp_dir}/stable"
first_candidate="${tmp_dir}/candidate-first"
second_candidate="${tmp_dir}/candidate-second"
third_candidate="${tmp_dir}/candidate-third"
mkdir -p "$first_candidate" "$second_candidate" "$third_candidate"
printf 'first\n' >"${first_candidate}/value"
printf 'second\n' >"${second_candidate}/value"
printf 'third\n' >"${third_candidate}/value"

bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_root" "$first_candidate" 2
test -L "$stable_root" || { printf 'first publication did not create an atomic pointer\n' >&2; exit 1; }
test "$(cat "${stable_root}/value")" = first

missing_marker="${tmp_dir}/missing"
(
  for _ in {1..2000}; do
    if [[ ! -f "${stable_root}/value" ]]; then
      : >"$missing_marker"
      exit
    fi
    case "$(cat "${stable_root}/value")" in
      first|second) ;;
      *) : >"$missing_marker"; exit ;;
    esac
  done
) &
reader_pid=$!
bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_root" "$second_candidate" 2
wait "$reader_pid"
test ! -e "$missing_marker" || { printf 'reader observed a missing or partial publication\n' >&2; exit 1; }
test "$(cat "${stable_root}/value")" = second

bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_root" "$third_candidate" 2
test "$(cat "${stable_root}/value")" = third
generations=("${stable_root}.generations"/generation.*)
test "${#generations[@]}" -eq 2 || {
  printf 'publisher retained %s generations, expected current and previous only\n' "${#generations[@]}" >&2
  exit 1
}

legacy_root="${tmp_dir}/legacy"
legacy_candidate="${tmp_dir}/candidate-legacy"
mkdir -p "$legacy_root" "$legacy_candidate"
printf 'preserve\n' >"${legacy_root}/value"
printf 'candidate\n' >"${legacy_candidate}/value"
if bash "${repo_root}/scripts/container/publish-directory.sh" "$legacy_root" "$legacy_candidate" 2 >/dev/null 2>&1; then
  printf 'publisher replaced a legacy directory without an atomic pointer\n' >&2
  exit 1
fi
test "$(cat "${legacy_root}/value")" = preserve
test "$(cat "${legacy_candidate}/value")" = candidate

stale_root="${tmp_dir}/stale"
stale_candidate="${tmp_dir}/candidate-stale"
mkdir -p "$stale_candidate" "${stale_root}.publish.lock"
printf 'stale-recovered\n' >"${stale_candidate}/value"
printf '99999999\n' >"${stale_root}.publish.lock/pid"
bash "${repo_root}/scripts/container/publish-directory.sh" "$stale_root" "$stale_candidate" 2
test "$(cat "${stale_root}/value")" = stale-recovered
test ! -e "${stale_root}.publish.lock"
