#!/usr/bin/env bash
# Offline documentation verifier for private ACR deployment claims.
#
# Validates, without Docker or network access:
#   - README.md links docs/container-images.md and lists the exact
#     container verification commands.
#   - docs/implementation-backlog.md records the superseded
#     acr-developer-deployment plan's container Todo 8 as complete and
#     its local Compose Todo 9 as pending.
#   - every local (non-URL) Markdown link in README.md and docs/*.md
#     resolves to a real file.
#   - every `make <target>` snippet referenced in README.md or
#     docs/container-images.md names a target that exists in Makefile.
#   - docs/container-images.md never claims local Compose behavior is
#     merged, verified, or complete (Compose remains pending elsewhere).
#   - new developer/operator documentation references real commands, paths,
#     and environment variables without requiring Docker or a network.
#   - Helm schema and trusted-web JWKS terminology stays aligned with the
#     checked-in deployment and service contracts.
#   - documentation rejects unsafe ownership, production HTTP, plaintext
#     credential, schema-rollback, and absolute-path claims.
#
# Usage: scripts/docs/verify.sh [--root DIR]
#
# --root DIR   Check DIR instead of the repository root that contains
#              this script. Used to validate disposable fixture trees;
#              DIR need not be a full checkout (e.g. a copied docs/ tree
#              is enough to exercise the forbidden-claim check).
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
default_root="$(cd "$script_dir/../.." && pwd)"
root="$default_root"

while [ $# -gt 0 ]; do
  case "$1" in
    --root)
      [ $# -ge 2 ] || { printf 'error: --root requires a value\n' >&2; exit 2; }
      root="$2"
      shift 2
      ;;
    --root=*)
      root="${1#--root=}"
      shift
      ;;
    -h|--help)
      sed -n '2,25p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      printf 'error: unknown argument %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [ ! -d "$root" ]; then
  printf 'error: --root %s is not a directory\n' "$root" >&2
  exit 2
fi
root="$(cd "$root" && pwd)"

fail_count=0
fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fail_count=$((fail_count + 1))
}
ok() {
  printf 'ok: %s\n' "$1"
}

readme="$root/README.md"
container_doc="$root/docs/container-images.md"
backlog="$root/docs/implementation-backlog.md"
makefile="$root/Makefile"
operations_doc="$root/docs/operations.md"
fixture_mode=false
[ -f "$root/.docs-invalid-fixture" ] && fixture_mode=true

doc_scan_files=()
if [ "$fixture_mode" = true ]; then
  while IFS= read -r -d '' md_file; do
    doc_scan_files+=("$md_file")
  done < <(find "$root" -name '*.md' -print0 2>/dev/null)
else
  for md_file in \
    "$readme" \
    "$operations_doc" \
    "$root/docs/mcp-sidecar.md" \
    "$root/docs/threat-model.md" \
    "$root/docs/examples/mcp-clients/README.md"; do
    [ -f "$md_file" ] && doc_scan_files+=("$md_file")
  done
fi

forbidden_sentence='ACR is packaged by dev-health-ops and publicly published.'
forbidden_hits=""
publication_scan_files=()
if [ "$fixture_mode" = true ]; then
  publication_scan_files=("${doc_scan_files[@]}")
else
  while IFS= read -r -d '' md_file; do
    publication_scan_files+=("$md_file")
  done < <(find "$root" -name '*.md' \
    -not -path '*/.git/*' \
    -not -path '*/.omo/*' \
    -not -path '*/.tmp/*' \
    -not -path '*/testdata/docs-invalid/*' \
    -not -path '*/node_modules/*' \
    -not -path '*/vendor/*' \
    -print0 2>/dev/null)
fi
for md_file in "${publication_scan_files[@]}"; do
  if grep -qF "$forbidden_sentence" "$md_file"; then
    forbidden_hits="$forbidden_hits${forbidden_hits:+ }${md_file#"$root"/}"
  fi
done

if [ -n "$forbidden_hits" ]; then
  fail "forbidden publication claim present in: $forbidden_hits"
else
  ok "no forbidden publication claim found"
fi

# --- 2. Unsafe developer/operator claims ---------------------------------
# The real tree scans its new operations surface. A partial invalid fixture has
# no operations guide, so all of its Markdown is scanned instead.
unsafe_claim() {
  local label="$1" pattern="$2"
  local hits=""
  local f
  for f in "${doc_scan_files[@]}"; do
    if grep -Eiq "$pattern" "$f"; then
      hits="$hits${hits:+ }${f#"$root"/}"
    fi
  done
  if [ -n "$hits" ]; then
    fail "$label present in: $hits"
  else
    ok "no $label found"
  fi
}

unsafe_claim 'Ops-packaged ACR claim' '(dev-health-ops|Dev Health Ops).{0,80}(packages|packaged|distributes|published).{0,80}ACR|ACR.{0,80}(packages|packaged|distributes|published).{0,80}(dev-health-ops|Dev Health Ops)'
unsafe_claim 'production HTTP endpoint' 'production.{0,80}http://|http://[^[:space:]]{0,80}production'
unsafe_claim 'plaintext ACR credential' 'fcacr_[A-Za-z0-9_-]{43}'
unsafe_claim 'supported schema rollback claim' 'ACR supports schema rollback|schema rollback is supported|schema rollback is allowed|schema rollback is performed'
unsafe_claim 'absolute filesystem path' '/(Users|home|tmp|var|opt|etc)/'
unsafe_claim 'ACR-owned CodeGraph index lifecycle claim' 'ACR (runs|run) CodeGraph (init|index|sync)'
unsafe_claim 'ACR SQLite access claim' 'ACR (reads|read|accesses|access).*SQLite'
unsafe_claim 'inferred indexed commit claim' 'ACR infers.*indexed commit|indexed commit.*(workspace HEAD|current HEAD)'
unsafe_claim 'local configuration breaks hosted bootstrap claim' 'local (configuration|config).{0,80}(prevents|blocks|fails).{0,80}hosted bootstrap'
unsafe_claim 'bare acr-mcp server registration' "bare[[:space:]]+acr-mcp|command[[:space:]]*[:=][[:space:]]*[\"']acr-mcp[\"'][[:space:]]*$"
unsafe_claim 'credential copied into project configuration' 'copy (credentials|tokens?) into (a )?project|credential(s)? (in|inside) project (file|config)'
unsafe_claim 'CodeGraph initialization or reindex claim' 'initialize CodeGraph|initialise CodeGraph|reindex CodeGraph|rebuild CodeGraph|CodeGraph initialization is supported'
unsafe_claim 'direct hosted or CodeGraph call claim' 'call(s)? (the )?(hosted )?API directly|directly call(s)? CodeGraph|direct CodeGraph call'
unsafe_claim 'automatic pre-plan claim' 'automatic(ally)?[[:space:]]+pre-plan|pre-plan.{0,40}(automatic|enabled by default)'
unsafe_claim 'public release claim' 'public(ly)?[[:space:]]+(release|published)|production release has been created or published'

if [ "$fixture_mode" = true ]; then
  if [ "$fail_count" -ne 0 ]; then
    printf '\ndocs verification FAILED (%s check(s))\n' "$fail_count" >&2
    exit 1
  fi
  printf '\ndocs verification OK\n'
  exit 0
fi

require_doc_text() {
  local file="$1" text="$2" label="$3"
  if [ ! -f "$file" ]; then
    fail "$label documentation file is missing: ${file#"$root"/}"
  elif grep -Fq "$text" "$file"; then
    ok "$label"
  else
    fail "$label is missing from ${file#"$root"/}"
  fi
}

mcp_doc="$root/docs/mcp-sidecar.md"
threat_doc="$root/docs/threat-model.md"
client_doc="$root/docs/examples/mcp-clients/README.md"
client_docs=(
  "$root/docs/examples/mcp-clients/README.md"
  "$root/docs/examples/mcp-clients/opencode.md"
  "$root/docs/examples/mcp-clients/claude-code.md"
  "$root/docs/examples/mcp-clients/codex.md"
  "$root/docs/examples/mcp-clients/cursor.md"
)
require_doc_text "$readme" "CodeGraph \`>=1.2.0,<2.0.0\`" "README pins the supported CodeGraph range"
require_doc_text "$readme" "it never runs \`init\`, \`index\`, or" "README documents the read-only direct/managed guard"
require_doc_text "$mcp_doc" "\`ACR_LOCAL_INDEX_PROVIDER\`" "sidecar documents local provider configuration"
require_doc_text "$mcp_doc" "\`indexed_commit_unknown\`" "sidecar documents unknown indexed commits"
require_doc_text "$mcp_doc" 'excluded from those caller limits' 'sidecar documents packet-content exclusions'
require_doc_text "$mcp_doc" '1024-entry, 30-minute local cache' 'sidecar documents local cache lifetime'
require_doc_text "$operations_doc" 'ACR_E2E_EXPECTED_FAILURE_VALIDATED' 'operations documents semantic expected-failure markers'
require_doc_text "$operations_doc" 'residual final-wave F2 risk' 'operations discloses the live-CodeGraph residual risk'
require_doc_text "$threat_doc" 'never uploaded to acr-api' 'threat model documents the local upload boundary'
require_doc_text "$client_doc" 'clients must not call' 'client examples preserve sidecar ownership'

for client_doc_path in "${client_docs[@]}"; do
  client_name="${client_doc_path##*/}"
  require_doc_text "$client_doc_path" 'acr-mcp serve' "$client_name documents exact serve registration"
  require_doc_text "$client_doc_path" 'acr-mcp doctor --offline' "$client_name documents offline doctor"
  require_doc_text "$client_doc_path" 'context_for_task' "$client_name documents context workflow"
  require_doc_text "$client_doc_path" 'source_evidence' "$client_name documents evidence workflow"
  require_doc_text "$client_doc_path" 'untrusted data' "$client_name documents untrusted content"
  require_doc_text "$client_doc_path" 'degraded' "$client_name documents visible degradation"
  require_doc_text "$client_doc_path" 'disabled by default' "$client_name documents disabled defaults"
  require_doc_text "$client_doc_path" 'README.md' "$client_name links shared index"
done

for package_readme in "$root"/clients/{opencode,claude-code,codex}/README.md; do
  if [ ! -f "$package_readme" ]; then
    fail "client package README is missing: ${package_readme#"$root"/}"
  elif grep -Fq 'Reserved Task 12 namespace' "$package_readme"; then
    fail "stale client package README placeholder: ${package_readme#"$root"/}"
  else
    ok "client package README is actionable: ${package_readme#"$root"/}"
  fi
done

# --- 3. README links docs/container-images.md and lists commands -------
required_commands="make container-contract
make container-pins
make container-test
make container-oci
make container-scan
make container-reproducible"

if [ ! -f "$readme" ]; then
  fail "README.md missing at $root"
else
  if grep -qE '\(docs/container-images\.md\)' "$readme"; then
    ok "README.md links docs/container-images.md"
  else
    fail "README.md does not link docs/container-images.md"
  fi

  while IFS= read -r cmd; do
    [ -n "$cmd" ] || continue
    if grep -qF "$cmd" "$readme"; then
      ok "README.md lists verification command: $cmd"
    else
      fail "README.md is missing verification command: $cmd"
    fi
  done <<EOF
$required_commands
EOF
fi

# --- 4. docs/container-images.md exists and is linkable -----------------
if [ ! -f "$container_doc" ]; then
  fail "docs/container-images.md missing at $root"
else
  ok "docs/container-images.md exists"

  # Compose must never be documented as merged/verified/complete here;
  # Compose acceptance is a separate, still-pending todo (acr-project-
  # completion Todo 7 / superseded acr-developer-deployment Todo 9).
  if grep -inE 'compose[^.]{0,40}(complete|merged|verified|tested|passing)' "$container_doc" \
      || grep -inE '(complete|merged|verified|tested|passing)[^.]{0,40}compose' "$container_doc"; then
    fail "docs/container-images.md appears to claim local Compose behavior is done"
  else
    ok "docs/container-images.md does not claim Compose completion"
  fi
fi

# --- 5. Backlog records Todo 8 complete / Todo 9 pending -----------------
if [ ! -f "$backlog" ]; then
  fail "docs/implementation-backlog.md missing at $root"
else
  # Flatten prose whitespace (including line wraps) so a phrase split
  # across Markdown source lines is still matched as one statement.
  backlog_flat="$(tr '\n' ' ' < "$backlog" | tr -s ' ')"

  if printf '%s' "$backlog_flat" | grep -qE 'Todo 8.{0,200}\bcomplete\b' \
      || printf '%s' "$backlog_flat" | grep -qiE 'complete.{0,200}Todo 8'; then
    ok "docs/implementation-backlog.md records Todo 8 complete"
  else
    fail "docs/implementation-backlog.md does not record superseded deployment plan Todo 8 as complete"
  fi

  if printf '%s' "$backlog_flat" | grep -qE 'Todo 9.{0,200}\bpending\b' \
      || printf '%s' "$backlog_flat" | grep -qiE 'pending.{0,200}Todo 9'; then
    ok "docs/implementation-backlog.md records Todo 9 pending"
  else
    fail "docs/implementation-backlog.md does not record superseded deployment plan Todo 9 as pending"
  fi
fi

# --- 6. Local Markdown links resolve -------------------------------------
check_links_in_file() {
  file="$1"
  file_dir="$(dirname "$file")"
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    case "$target" in
      http://*|https://*|mailto:*|\#*) continue ;;
    esac
    clean_target="${target%%#*}"
    [ -n "$clean_target" ] || continue
    if [ ! -e "$file_dir/$clean_target" ] && [ ! -e "$root/$clean_target" ]; then
      fail "${file#"$root"/}: broken local link -> $target"
    fi
  done < <(grep -oE '\]\([^)]+\)' "$file" | sed -E 's/^\]\(//; s/\)$//')
}

link_check_files=""
[ -f "$readme" ] && link_check_files="$link_check_files $readme"
if [ -d "$root/docs" ]; then
  while IFS= read -r -d '' f; do
    link_check_files="$link_check_files $f"
  done < <(find "$root/docs" -maxdepth 1 -name '*.md' -print0)
fi

if [ -n "$link_check_files" ]; then
  link_failures_before=$fail_count
  for f in $link_check_files; do
    check_links_in_file "$f"
  done
  if [ "$fail_count" -eq "$link_failures_before" ]; then
    ok "all local Markdown links resolve"
  fi
fi

# --- 7. make snippets reference real Makefile targets ---------------------
if [ -f "$makefile" ]; then
  snippet_files=""
  [ -f "$readme" ] && snippet_files="$snippet_files $readme"
  [ -f "$container_doc" ] && snippet_files="$snippet_files $container_doc"
  snippet_failures_before=$fail_count
  for f in $snippet_files; do
    while IFS= read -r target; do
      [ -n "$target" ] || continue
      if ! grep -qE "^${target}:" "$makefile"; then
        fail "${f#"$root"/}: references undefined make target '$target'"
      fi
    done < <(grep -oE 'make [a-zA-Z0-9_-]+' "$f" | awk '{print $2}' | sort -u)
  done
  if [ -n "$snippet_files" ] && [ "$fail_count" -eq "$snippet_failures_before" ]; then
    ok "all referenced make targets exist in Makefile"
  fi
else
  ok "Makefile not present under --root; skipping make-target snippet check"
fi

# --- 8. Operations commands, environment, schema, and JWKS ----------------
if [ ! -f "$operations_doc" ]; then
  fail "docs/operations.md missing at $root"
else
  command_failures_before=$fail_count
  while IFS= read -r command_path; do
    [ -n "$command_path" ] || continue
    if [ ! -e "$root/$command_path" ]; then
      fail "docs/operations.md references missing command path '$command_path'"
    fi
  done < <(grep -oE '(bash|sh)[[:space:]]+(scripts|deploy)/[A-Za-z0-9._/-]+' "$operations_doc" | awk '{print $2}' | sort -u)

  while IFS= read -r go_path; do
    [ -n "$go_path" ] || continue
    if [ ! -e "$root/$go_path" ]; then
      fail "docs/operations.md references missing Go command path '$go_path'"
    fi
  done < <(grep -oE 'go run[[:space:]]+\./cmd/[A-Za-z0-9._/-]+' "$operations_doc" | awk '{print $3}' | sort -u)

  if [ "$fail_count" -eq "$command_failures_before" ]; then
    ok "operations command paths exist"
  fi

  env_failures_before=$fail_count
  while IFS= read -r environment_name; do
    [ -n "$environment_name" ] || continue
    if ! grep -Rqs --include='*.go' --include='*.sh' --include='*.yml' --include='*.yaml' --include='*.json' --include='Makefile' \
      --exclude-dir=.git --exclude-dir=.omo --exclude-dir=.tmp --exclude-dir=testdata \
      "$environment_name" "$root/cmd" "$root/internal" "$root/deploy" "$root/scripts" "$root/Makefile" 2>/dev/null; then
      fail "docs/operations.md references undocumented environment name '$environment_name'"
    fi
  done < <(grep -oE '(ACR|TEST)_[A-Z0-9_]+' "$operations_doc" | sort -u)
  if [ "$fail_count" -eq "$env_failures_before" ]; then
    ok "operations environment names are read by code or deployment artifacts"
  fi

  if [ -f "$root/deploy/helm/acr/values.schema.json" ] \
    && grep -qF 'deploy/helm/acr/values.schema.json' "$operations_doc" \
    && grep -qF 'ACR_WEB_ASSERTION_ISSUER' "$operations_doc" \
    && grep -qF 'ACR_WEB_ASSERTION_AUDIENCE' "$operations_doc" \
    && grep -qF 'ACR_WEB_ASSERTION_JWKS_FILE' "$operations_doc" \
    && grep -qi 'JWKS' "$operations_doc"; then
    ok "operations schema and JWKS terminology is consistent"
  else
    fail "operations schema/JWKS terminology is incomplete or values schema is missing"
  fi
fi

if [ "$fail_count" -ne 0 ]; then
  printf '\ndocs verification FAILED (%s check(s))\n' "$fail_count" >&2
  exit 1
fi

printf '\ndocs verification OK\n'
