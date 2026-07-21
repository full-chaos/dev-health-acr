#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
package_path=""
while (($#)); do
  case "$1" in
    --package) package_path="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ -n "$package_path" ]] || exit 2
if [[ "$package_path" = /* ]]; then package_root="$package_path"; else package_root="$repo_root/$package_path"; fi

fail() { printf 'CURSOR_MANIFEST_FAIL reason=%s\n' "$1" >&2; exit 1; }

plugin_json="$package_root/.cursor-plugin/plugin.json"
mcp_json="$package_root/mcp.json"
[[ -f "$plugin_json" ]] || fail "plugin.json.missing"
[[ -f "$mcp_json" ]] || fail "mcp.json.missing"
jq empty "$plugin_json" >/dev/null 2>&1 || fail "plugin.json.invalid_json"
jq empty "$mcp_json" >/dev/null 2>&1 || fail "mcp.json.invalid_json"

# --- plugin.json: allowed top-level keys per the official cursor/plugins schema ---
allowed_plugin_keys='["name","displayName","description","version","author","publisher","homepage","repository","license","logo","keywords","category","tags","commands","agents","skills","rules","hooks","mcpServers"]'
unknown_keys="$(jq -r --argjson allowed "$allowed_plugin_keys" '(keys - $allowed) | join(",")' "$plugin_json")"
[[ -z "$unknown_keys" ]] || fail "plugin.json.unknown_field:$unknown_keys"

jq -e 'has("name")' "$plugin_json" >/dev/null || fail "plugin.json.name_missing"
name_value="$(jq -r '.name' "$plugin_json")"
[[ "$name_value" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || fail "plugin.json.name_invalid"

for field in displayName description version homepage repository license logo publisher; do
  if jq -e --arg f "$field" 'has($f)' "$plugin_json" >/dev/null; then
    type="$(jq -r --arg f "$field" '.[$f] | type' "$plugin_json")"
    [[ "$type" == "string" ]] || fail "plugin.json.${field}_type"
  fi
done

resolve_component_path() {
  local value="$1" resolved segment segments
  case "$value" in
    /*) return 1 ;;
  esac
  [[ "$value" != *"//"* ]] || return 1
  resolved="${value#./}"
  IFS='/' read -ra segments <<<"$resolved"
  for segment in "${segments[@]}"; do
    [[ "$segment" != ".." ]] || return 1
  done
  [[ -e "$package_root/$resolved" ]] || return 1
  return 0
}

for field in rules skills commands agents; do
  if jq -e --arg f "$field" 'has($f)' "$plugin_json" >/dev/null; then
    ftype="$(jq -r --arg f "$field" '.[$f] | type' "$plugin_json")"
    case "$ftype" in
      string)
        fvalue="$(jq -r --arg f "$field" '.[$f]' "$plugin_json")"
        resolve_component_path "$fvalue" || fail "plugin.json.${field}_path_missing:$fvalue"
        ;;
      array)
        while IFS= read -r fvalue; do
          [[ -n "$fvalue" ]] || continue
          resolve_component_path "$fvalue" || fail "plugin.json.${field}_path_missing:$fvalue"
        done < <(jq -r --arg f "$field" '.[$f][]' "$plugin_json")
        ;;
      *) fail "plugin.json.${field}_type" ;;
    esac
  fi
done

jq -e 'has("mcpServers")' "$plugin_json" >/dev/null || fail "plugin.json.mcpServers_missing"
mtype="$(jq -r '.mcpServers | type' "$plugin_json")"
case "$mtype" in
  string)
    mvalue="$(jq -r '.mcpServers' "$plugin_json")"
    resolve_component_path "$mvalue" || fail "plugin.json.mcpServers_path_missing:$mvalue"
    ;;
  object|array) : ;;
  *) fail "plugin.json.mcpServers_type" ;;
esac

# --- mcp.json: strict shape for the acr STDIO server ---
mcp_top_keys="$(jq -r '(keys - ["mcpServers"]) | join(",")' "$mcp_json")"
[[ -z "$mcp_top_keys" ]] || fail "mcp.json.unknown_top_field:$mcp_top_keys"
jq -e 'has("mcpServers")' "$mcp_json" >/dev/null || fail "mcp.json.mcpServers_missing"
# Exactly the intended single server -- no extra registrations riding along.
server_count="$(jq '.mcpServers | length' "$mcp_json")"
[[ "$server_count" == "1" ]] || fail "mcp.json.server_count:$server_count"
server_names="$(jq -r '.mcpServers | keys | join(",")' "$mcp_json")"
[[ "$server_names" == "acr" ]] || fail "mcp.json.unexpected_server:$server_names"

allowed_server_keys='["type","command","args","env","envFile"]'
unknown_server_keys="$(jq -r --argjson allowed "$allowed_server_keys" '.mcpServers.acr | (keys - $allowed) | join(",")' "$mcp_json")"
[[ -z "$unknown_server_keys" ]] || fail "mcp.json.acr_unknown_field:$unknown_server_keys"

server_type="$(jq -r '.mcpServers.acr.type' "$mcp_json")"
[[ "$server_type" == "stdio" ]] || fail "mcp.json.acr_type"
server_command="$(jq -r '.mcpServers.acr.command' "$mcp_json")"
[[ "$server_command" == "acr-mcp" ]] || fail "mcp.json.acr_command"
server_args="$(jq -c '.mcpServers.acr.args' "$mcp_json")"
[[ "$server_args" == '["serve"]' ]] || fail "mcp.json.acr_args"

# --- frontmatter: explicit allowlist + required-field check, no YAML dependency ---
validate_frontmatter() {
  local file="$1" allowed="$2" required="$3" first_line body seen="" key line req
  [[ -f "$file" ]] || fail "frontmatter.file_missing:$file"
  first_line="$(head -n1 "$file")"
  [[ "$first_line" == "---" ]] || fail "frontmatter.missing:$file"
  body="$(awk 'NR==1{next} /^---$/{exit} {print}' "$file")"
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    key="${line%%:*}"
    key="${key// /}"
    case " $allowed " in
      *" $key "*) : ;;
      *) fail "frontmatter.unknown_field:$file:$key" ;;
    esac
    seen="$seen $key"
  done <<<"$body"
  for req in $required; do
    case " $seen " in
      *" $req "*) : ;;
      *) fail "frontmatter.required_field_missing:$file:$req" ;;
    esac
  done
}

shopt -s nullglob
for cmd_file in "$package_root"/commands/*.md; do
  validate_frontmatter "$cmd_file" "name description" "name description"
done
# Structured always-apply policy: exactly one rule (the no-automatic-use
# guard) may set alwaysApply:true, and only with its exact restrictive
# body -- any other always-applied rule, or a guard with weakened
# content, is a policy violation, not a lexical string check.
allowed_always_apply_rule="no-automatic-use.mdc"
guard_required_phrase="Never call the \`context_for_task\` or \`source_evidence\` MCP tools"
guard_seen=0
for rule_file in "$package_root"/rules/*.mdc; do
  validate_frontmatter "$rule_file" "description globs alwaysApply" ""
  rule_basename="$(basename "$rule_file")"
  always_apply_value="$(awk 'NR==1{next} /^---$/{exit} /^alwaysApply:/{sub(/^alwaysApply:[[:space:]]*/,""); print; exit}' "$rule_file")"
  if [[ "$rule_basename" == "$allowed_always_apply_rule" ]]; then
    guard_seen=1
    [[ "$always_apply_value" == "true" ]] || fail "rule.guard_must_be_always_apply:$rule_basename"
    grep -Fq -- "$guard_required_phrase" "$rule_file" || fail "rule.guard_content_mismatch:$rule_basename"
  else
    [[ "$always_apply_value" != "true" ]] || fail "rule.unexpected_always_apply:$rule_basename"
  fi
done
(( guard_seen == 1 )) || fail "rule.guard_missing:$allowed_always_apply_rule"
shopt -u nullglob

skill_file="$package_root/skills/context-fabric/SKILL.md"
[[ -f "$skill_file" ]] || fail "skill.missing"
validate_frontmatter "$skill_file" "name description paths disable-model-invocation metadata globs" "name description"
skill_name="$(awk 'NR==1{next} /^---$/{exit} /^name:/{sub(/^name:[[:space:]]*/,""); print; exit}' "$skill_file")"
[[ "$skill_name" == "context-fabric" ]] || fail "skill.name_mismatch:$skill_name"

printf 'CURSOR_MANIFEST_OK\n'
