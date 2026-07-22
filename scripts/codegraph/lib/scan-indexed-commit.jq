# Detects forbidden raw provenance keys without rejecting harmless additive
# fields such as a query node's `ref`. Unambiguous indexed*/git* provenance
# keys are forbidden at every nesting level; generic commit/ref/revision keys
# are forbidden only on the status object or its index object. The
# indexed_commit_unknown sentinel belongs only to normalized ACR output.
# Invoked as: jq --argjson m <manifest> --arg cmd <command> -f this <fixture>
. as $doc
| ($m.forbidden_indexed_commit_keys) as $global_keys
| ($m.status_provenance_keys) as $status_keys
| ([
    ([[]] + [paths(objects)])[] as $path
    | getpath($path) as $object
    | $object | keys_unsorted[] as $key
    | select($global_keys | index($key))
    | { path: ($path + [$key]), key: $key, value: $object[$key] }
  ]) as $global_violations
| (if $cmd == "status" and ($doc | type) == "object" then
     [([ [], ["index"] ][] as $path
      | ($doc | getpath($path)) as $object
      | select($object | type == "object")
      | $object | keys_unsorted[] as $key
      | select($status_keys | index($key))
      | { path: ($path + [$key]), key: $key, value: $object[$key] })]
   else []
   end) as $status_violations
| ($global_violations + $status_violations) as $violations
| { violations: $violations, ok: ($violations == []) }
