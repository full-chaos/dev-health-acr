# Validates the JSON syntax-independent structure of named rejection fixtures.
# Predicate evaluation remains in verify-contract.sh; malformed or unrelated
# fixtures are integrity failures rather than accidental rejections.
def integer: type == "number" and floor == .;

def matches($rule):
  . as $value
  | if $rule.type == "null" then ($value | type == "null")
    elif $rule.type == "boolean" then ($value | type == "boolean")
    elif $rule.type == "string" then ($value | type == "string")
    elif $rule.type == "number" then ($value | type == "number")
    elif $rule.type == "integer" then ($value | integer)
    elif $rule.type == "array" then
      ($value | type == "array") and all($value[]; matches($rule.items))
    elif $rule.type == "object" then
      ($value | type == "object")
      and all(
        $rule.required | to_entries[];
        .key as $key | .value as $child
        | ($value | has($key) and ($value[$key] | matches($child)))
      )
    elif $rule.one_of then any($rule.one_of[]; . as $candidate | ($value | matches($candidate)))
    else false
    end
  | if ($rule.minimum // null) != null then . and ($value >= $rule.minimum) else . end
  | if ($rule.maximum // null) != null then . and ($value <= $rule.maximum) else . end;

def attempted_argv:
  type == "object"
  and (.reason | type == "string")
  and (.attempted_argv | type == "array" and length >= 2 and all(.[]; type == "string"));

def status_schema: $m.commands.status.schema;

def missing_file_count_status:
  (. as $status
   | ($m.commands.status.schema | .required |= with_entries(select(.key != "fileCount"))) as $schema
   | ($status | type == "object" and has("fileCount") | not)
   and ($status | matches($schema)));

. as $doc
| (if $scenario == "forbidden-command" then
     ($doc | attempted_argv and .attempted_argv[0] == "codegraph")
   elif $scenario == "inferred-indexed-commit" then
     ($doc | matches(status_schema))
   elif $scenario == "unsupported-version" then
     ($doc | type == "object" and (.reason | type == "string")
      and (.version | type == "string" and test("^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$")))
   elif $scenario == "non-json-mode" then
     ($doc | attempted_argv and .attempted_argv[0] == "codegraph")
   elif $scenario == "missing-field" then
     ($doc | missing_file_count_status)
   elif $scenario == "sqlite-access" then
     ($doc | attempted_argv)
   else false
   end) as $valid
| {scenario: $scenario, ok: $valid}
