# Detects forbidden raw indexed-commit/ref-shaped keys recursively. The
# indexed_commit_unknown sentinel belongs only to ACR's downstream normalized
# output; it never makes a raw CodeGraph field acceptable. Invoked as:
# jq --argjson m <manifest> -f this <fixture>
. as $doc
| ($m.forbidden_indexed_commit_keys) as $keys
| ([
    ([[]] + [paths(objects)])[] as $path
    | getpath($path) as $object
    | $object | keys_unsorted[] as $key
    | select($keys | index($key))
    | { path: ($path + [$key]), key: $key, value: $object[$key] }
  ]) as $violations
| { violations: $violations, ok: ($violations == []) }
