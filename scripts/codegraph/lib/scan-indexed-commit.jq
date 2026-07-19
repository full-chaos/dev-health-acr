# Detects any forbidden indexed-commit-shaped key whose value is not the
# indexed_commit_unknown sentinel. Invoked as:
# jq --argjson m <manifest> -f this <fixture>
. as $doc
| ($m.forbidden_indexed_commit_keys) as $keys
| ($m.indexed_commit_sentinel) as $sentinel
| ([ $keys[] as $k | select(($doc | type) == "object" and ($doc | has($k))) | { key: $k, value: $doc[$k] } ]) as $present
| ([ $present[] | select(.value != $sentinel) ]) as $violations
| { present: $present, violations: $violations, ok: ($violations == []) }
