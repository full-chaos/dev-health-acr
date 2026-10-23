#!/usr/bin/env bash
set -euo pipefail

# prune-dropped-unit-shards.sh DIR DROP-JSON
#
# After a `gh run rerun --failed`, unit-dedup-plan reruns and may drop legs an
# earlier attempt ran, because the race jobs now pass. The earlier attempt's
# go-unit-shard-N artifact stays in the run, and mergetestreports refuses it
# as the artifact of an excluded leg. This removes DIR/go-unit-shard-N for each
# {"shard":N} in DROP-JSON (the list verify re-derived from this attempt's race
# artifacts), but only when that artifact PASSED: go-test.json is non-empty and
# holds no fail event, and junit.xml is non-empty. A failed or unreadable
# artifact is kept, so the merge refuses it and verify stays red. Every
# removal and every keep is logged.

dir="${1:?usage: prune-dropped-unit-shards.sh DIR DROP-JSON}"
drop="${2:?usage: prune-dropped-unit-shards.sh DIR DROP-JSON}"

printf '%s' "$drop" | jq -e 'type == "array" and all(.[]; type == "object" and keys == ["shard"] and (.shard | type == "number" and . == floor and . >= 1))' >/dev/null 2>&1 \
  || { printf 'prune-dropped-unit-shards: drop %s is not a list of {"shard":N}\n' "$drop" >&2; exit 2; }

for n in $(printf '%s' "$drop" | jq -r '.[].shard'); do
  d="$dir/go-unit-shard-$n"
  if [ ! -d "$d" ]; then
    printf 'prune-dropped-unit-shards: go-unit-shard-%s: no artifact, nothing to prune\n' "$n"
    continue
  fi
  if [ -s "$d/junit.xml" ] \
     && jq -e -s 'length > 0 and all(.[]; .Action != "fail")' "$d/go-test.json" >/dev/null 2>&1; then
    rm -rf "$d"
    printf '::notice::pruned go-unit-shard-%s: a stale artifact of an earlier attempt, passed, and the current plan dropped the leg\n' "$n"
  else
    printf '::warning::kept go-unit-shard-%s: the leg is dropped but its artifact did not pass or cannot be read; the merge will refuse it\n' "$n"
  fi
done
