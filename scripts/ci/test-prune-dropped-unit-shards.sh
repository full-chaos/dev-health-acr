#!/usr/bin/env bash
set -euo pipefail

# test-prune-dropped-unit-shards.sh proves prune-dropped-unit-shards.sh removes
# only the passed artifact of a dropped leg, and that each guard can fail.

root="$(cd "$(dirname "$0")/../.." && pwd)"
script="${PRUNE_SCRIPT:-$root/scripts/ci/prune-dropped-unit-shards.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0

mk() { # dir shard kind
  local d="$1/go-unit-shard-$2"
  mkdir -p "$d"
  case "$3" in
    pass) printf '{"Action":"run","Test":"TestA"}\n{"Action":"pass","Test":"TestA"}\n{"Action":"pass"}\n' >"$d/go-test.json" ;;
    fail) printf '{"Action":"run","Test":"TestA"}\n{"Action":"fail","Test":"TestA"}\n{"Action":"fail"}\n' >"$d/go-test.json" ;;
    empty) : >"$d/go-test.json" ;;
  esac
  printf '<testsuites/>\n' >"$d/junit.xml"
}

check() { # name condition-exit-status
  if [ "$2" -eq 0 ]; then printf 'ok   %s\n' "$1"; else printf 'FAIL %s\n' "$1"; fails=$((fails + 1)); fi
}
exists() { [ -d "$1" ]; }

d="$tmp/a"; mkdir -p "$d"; mk "$d" 7 pass; mk "$d" 8 pass; mk "$d" 3 pass
out="$("$script" "$d" '[{"shard":7},{"shard":8}]')"
check "dropped passed artifacts are removed" "$(! exists "$d/go-unit-shard-7" && ! exists "$d/go-unit-shard-8"; echo $?)"
check "a shard not in the drop list is untouched" "$(exists "$d/go-unit-shard-3"; echo $?)"
check "every removal is logged by name" "$(printf '%s' "$out" | grep -q 'pruned go-unit-shard-7' && printf '%s' "$out" | grep -q 'pruned go-unit-shard-8'; echo $?)"

d="$tmp/b"; mkdir -p "$d"; mk "$d" 8 fail; mk "$d" 9 pass
out="$("$script" "$d" '[{"shard":8},{"shard":9}]')"
check "a dropped leg whose artifact failed is kept" "$(exists "$d/go-unit-shard-8"; echo $?)"
check "the keep is logged" "$(printf '%s' "$out" | grep -q 'kept go-unit-shard-8'; echo $?)"
check "the passed sibling is still pruned" "$(! exists "$d/go-unit-shard-9"; echo $?)"

d="$tmp/c"; mkdir -p "$d"; mk "$d" 8 empty
"$script" "$d" '[{"shard":8}]' >/dev/null
check "an empty go-test.json is kept" "$(exists "$d/go-unit-shard-8"; echo $?)"

d="$tmp/d"; mkdir -p "$d"; mk "$d" 8 pass
rm "$d/go-unit-shard-8/junit.xml"
"$script" "$d" '[{"shard":8}]' >/dev/null
check "a missing junit.xml is kept" "$(exists "$d/go-unit-shard-8"; echo $?)"

d="$tmp/e"; mkdir -p "$d"; mk "$d" 8 pass
"$script" "$d" '[]' >/dev/null
check "an empty drop list prunes nothing" "$(exists "$d/go-unit-shard-8"; echo $?)"
"$script" "$d" '[{"shard":2}]' >/dev/null
check "a dropped leg with no artifact is a no-op" "$(exists "$d/go-unit-shard-8"; echo $?)"

for bad in 'x' '{"shard":8}' '[8]' '[{"shard":8,"a":1}]' '[{"shard":"8"}]' '[{"shard":0}]'; do
  rc=0; "$script" "$d" "$bad" >/dev/null 2>&1 || rc=$?
  check "bad drop $bad is refused" "$([ "$rc" -eq 2 ]; echo $?)"
done

[ "$fails" -eq 0 ] || { printf '%s row(s) failed\n' "$fails" >&2; exit 1; }

if [ -z "${PRUNE_SCRIPT:-}" ]; then
  # planted defects: each mutated copy must fail at least one row
  mut() { # name sed-expr
    local m="$tmp/mut-$1.sh"
    sed -E "$2" "$script" >"$m"; chmod +x "$m"
    if cmp -s "$script" "$m"; then printf 'FAIL planted defect %s did not change the script\n' "$1" >&2; exit 1; fi
    if PRUNE_SCRIPT="$m" "$0" >/dev/null 2>&1; then printf 'FAIL planted defect %s survived\n' "$1" >&2; exit 1; fi
    printf 'ok   planted defect %s is caught\n' "$1"
  }
  mut ignore-fail 's/all\(\.\[\]; \.Action != "fail"\)/all(.[]; true)/'
  mut empty-json-ok 's/length > 0 and //'
  # shellcheck disable=SC2016
  mut no-junit-ok 's/if \[ -s "\$d\/junit.xml" \] \\/if true \\/'
  mut no-log 's/printf .::notice::pruned/printf ":: pruned/; s/(printf .::notice::pruned go-unit-shard-%s)/\1/'
fi
