# Validates the fixed CodeGraph CLI allowlist. The manifest is a contract,
# not runtime configuration: any argv drift is an integrity error.
def error_if($condition; $message):
  if $condition then [$message] else [] end;

. as $m
| {
  status: ["codegraph", "status", "--json"],
  query: ["codegraph", "query", "--json", "<search>", "[--limit N]"],
  callers: ["codegraph", "callers", "--json", "<symbol>", "[--limit N]"],
  callees: ["codegraph", "callees", "--json", "<symbol>", "[--limit N]"],
  impact: ["codegraph", "impact", "--json", "<symbol>", "[--depth N]"],
  affected: ["codegraph", "affected", "--json", "[files...]", "[--stdin]", "[--depth N]", "[--filter GLOB]"],
  files: ["codegraph", "files", "--json", "[--filter DIR]", "[--pattern GLOB]", "[--max-depth N]", "[--no-metadata]"]
} as $expected_argv
| {
  status: ["codegraph", "status", "--json"],
  query: ["codegraph", "query", "--json", "Assemble", "--limit", "3"],
  callers: ["codegraph", "callers", "--json", "Assemble", "--limit", "3"],
  callees: ["codegraph", "callees", "--json", "Assemble", "--limit", "3"],
  impact: ["codegraph", "impact", "--json", "Assemble", "--depth", "2"],
  affected: ["codegraph", "affected", "--json", "acr/internal/contextpacket/assembler.go", "--depth", "2"],
  files: ["codegraph", "files", "--json", "--filter", "acr/internal/contextpacket", "--max-depth", "2"]
} as $expected_examples
| [
    error_if((.contract_version != "1"); "contract_version must be 1"),
    error_if((.supported_codegraph_version_range != ">=1.2.0,<2.0.0"); "supported_codegraph_version_range must be >=1.2.0,<2.0.0"),
    error_if((.fixture_version != .observed_codegraph_version); "fixture_version must equal observed_codegraph_version"),
    error_if((.provider_id != "codegraph"); "provider_id must be codegraph"),
    error_if((.provider_version_field != "status.version"); "provider_version_field must be status.version"),
    error_if((.query_version != "codegraph-json-contract-v1"); "query_version must be codegraph-json-contract-v1"),
    error_if((.transport != {"mode": "subprocess-json-stdout", "socket_access": false, "direct_sqlite_access": false}); "transport must be subprocess JSON stdout with socket and SQLite access disabled"),
    error_if((.read_only != true); "read_only must be true"),
    error_if((.max_commands_per_task != 8); "max_commands_per_task must be 8"),
    error_if((.max_traversal_depth != 2); "max_traversal_depth must be 2"),
    error_if(((.commands | keys | sort) != ($expected_argv | keys | sort)); "commands must contain exactly the seven permitted read-only commands"),
    [ $expected_argv | to_entries[] | .key as $name | .value as $argv
      | error_if(($m.commands[$name].argv != $argv); "\($name).argv is not the fixed allowlisted argv")
    ],
    [ $expected_examples | to_entries[] | .key as $name | .value as $argv
      | error_if(($m.commands[$name].example_argv != $argv); "\($name).example_argv is not the fixed canonical argv")
    ],
    [ $expected_argv | keys[] as $name
      | error_if(($m.commands[$name].schema | type) != "object"; "\($name).schema must be an object")
    ],
    error_if(($m.commands.impact.max_depth != 2); "impact.max_depth must be 2"),
    error_if(($m.commands.affected.max_depth != 2); "affected.max_depth must be 2"),
    error_if(($m.commands.affected.cli_default_depth != 5); "affected.cli_default_depth must record CodeGraph 1.2.0's default of 5"),
    error_if(($m.commands.affected.production_requires_explicit_depth != true); "affected must require explicit --depth 2"),
    error_if(($m.commands.files.max_depth != 2); "files.max_depth must be the bounded presentation value 2"),
    error_if(($m.commands.files.max_depth_scope != "tree_presentation_only"); "files.max_depth_scope must be tree_presentation_only")
  ] | flatten as $errors
| { errors: $errors, ok: ($errors == []) }
