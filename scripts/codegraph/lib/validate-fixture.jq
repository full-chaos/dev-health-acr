# Deep schema check for one CodeGraph fixture against the manifest command
# spec named by --arg cmd. Unknown/additive fields remain valid, but every
# declared required field must exist, be non-null unless explicitly nullable,
# and match its declared type and numeric bounds.
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

. as $doc
| ($m.commands[$cmd].schema // null) as $schema
| {
    command: $cmd,
    schema_present: ($schema != null),
    ok: (($schema != null) and ($doc | matches($schema)))
  }
