#!/bin/bash
# Launch the ACR MCP sidecar with STDIO transport
# Usage: ./launch-sidecar.sh [api-url]

set -euo pipefail

# Configuration
ACR_API_URL="${1:-https://api.dev-health.example.com}"
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

# `acr-mcp login` persists the credential before this launcher is used; serve
# discovers the default keyring/file source automatically. A private CA may be
# supplied separately with ACR_API_CA_BUNDLE.
export ACR_API_URL

# Launch the sidecar
exec "$ACR_MCP_BINARY" serve
