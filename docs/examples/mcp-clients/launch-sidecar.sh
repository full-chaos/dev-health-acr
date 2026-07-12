#!/bin/bash
# Launch the ACR MCP sidecar with STDIO transport
# Usage: ./launch-sidecar.sh [api-url] [token-file]

set -euo pipefail

# Configuration
ACR_API_URL="${1:-https://api.dev-health.example.com}"
ACR_API_TOKEN_FILE="${2:-${HOME}/.acr/token}"
ACR_MCP_BINARY="${ACR_MCP_BINARY:-/path/to/acr-mcp}"

# Validate inputs
if [[ ! -f "$ACR_MCP_BINARY" ]]; then
  echo "Error: ACR_MCP_BINARY not found: $ACR_MCP_BINARY" >&2
  exit 1
fi

if [[ ! -x "$ACR_MCP_BINARY" ]]; then
  echo "Error: ACR_MCP_BINARY is not executable: $ACR_MCP_BINARY" >&2
  exit 1
fi

if [[ ! -f "$ACR_API_TOKEN_FILE" ]]; then
  echo "Error: Token file not found: $ACR_API_TOKEN_FILE" >&2
  exit 1
fi

# Check token file permissions. stat(1) has an incompatible BSD vs. GNU
# flag syntax (macOS ships BSD stat, but a coreutils install can shadow it
# on PATH with GNU stat); probe which flavor is present with `stat
# --version` (GNU-only, silently discarded either way) and run exactly one
# matching stat invocation so a flavor mismatch never leaks the other
# implementation's usage/error text into $perms. If neither flavor can be
# determined, the permission probe is skipped quietly rather than firing a
# false warning.
if [[ "$(uname)" != "Windows_NT" ]]; then
  if stat --version >/dev/null 2>&1; then
    perms=$(stat -c "%a" "$ACR_API_TOKEN_FILE" 2>/dev/null)
  else
    perms=$(stat -f "%OLp" "$ACR_API_TOKEN_FILE" 2>/dev/null)
  fi
  if [[ -n "$perms" && "$perms" != "600" ]]; then
    echo "Warning: Token file permissions are not 0600: $perms" >&2
    echo "Run: chmod 600 $ACR_API_TOKEN_FILE" >&2
  fi
fi

# Export environment variables
export ACR_API_URL
export ACR_API_TOKEN_FILE

# Launch the sidecar
exec "$ACR_MCP_BINARY" serve
