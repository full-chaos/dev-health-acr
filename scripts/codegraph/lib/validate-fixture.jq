# Structural required-field check for one CodeGraph fixture against the
# manifest command spec named by --arg cmd, tolerating any additive/unknown
# field. Invoked as: jq --argjson m <manifest> --arg cmd <name> -f this <fixture>
. as $doc
| $m.commands[$cmd] as $spec
| (
    if $spec.shape == "object" then
      {
        missing_top: ($spec.required_fields - ($doc | keys)),
        bad_entries: (
          if ($spec.array_field // null) != null then
            ([ ($doc[$spec.array_field] // [])[] | ($spec.array_field_entry_required_fields - keys) ] | map(select(length > 0)))
          else [] end
        ),
        bad_nested: []
      }
    elif $spec.shape == "array" then
      {
        missing_top: [],
        bad_entries: ([ $doc[] | ($spec.entry_required_fields - keys) ] | map(select(length > 0))),
        bad_nested: (
          if ($spec.entry_nested_field // null) != null then
            ([ $doc[] | (.[$spec.entry_nested_field] // {}) | ($spec.entry_nested_required_fields - keys) ] | map(select(length > 0)))
          else [] end
        )
      }
    else
      { missing_top: ["UNKNOWN_SHAPE:" + ($spec.shape // "null")], bad_entries: [], bad_nested: [] }
    end
  ) as $result
| $result + { ok: (($result.missing_top == []) and ($result.bad_entries == []) and ($result.bad_nested == [])) }
