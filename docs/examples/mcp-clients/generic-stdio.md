# Generic STDIO MCP Client Setup

The ACR MCP sidecar uses STDIO transport, which is compatible with any MCP client that supports subprocess-based servers.

## Basic Setup

To integrate the ACR sidecar with a generic MCP client, you need to:

1. Build the sidecar binary.
2. Create a token file.
3. Configure your MCP client to launch the sidecar with the correct environment variables.

## Building the Sidecar

```bash
cd /path/to/acr
go build -o acr-mcp ./cmd/acr-mcp
```

## Creating a Token File

```bash
mkdir -p ~/.acr
echo "fcacr_your_token_here" > ~/.acr/token
chmod 600 ~/.acr/token
```

`fcacr_your_token_here` is a placeholder, not a real token shape -- a real token is `fcacr_` followed by 43 base64url characters (see `docs/mcp-sidecar.md#token-format`). On Unix/Linux/macOS the sidecar rejects a group- or world-readable token file. **`ACR_API_TOKEN_FILE` is not supported on Windows**: the sidecar fails closed and refuses to load any token file there; use `ACR_API_TOKEN` instead -- the OS keyring source is also macOS/Linux only.

## Launching the Sidecar

The sidecar is launched with the `serve` command:

```bash
export ACR_API_URL="https://api.dev-health.example.com"
export ACR_API_TOKEN_FILE="$HOME/.acr/token"
/path/to/acr-mcp serve
```

The sidecar will:
1. Load the credential from the token file.
2. Validate the API URL.
3. Listen on STDIN for MCP requests.
4. Write MCP responses to STDOUT.
5. Write diagnostic messages to STDERR.

## MCP Client Configuration

Your MCP client should:

1. Launch the sidecar as a subprocess with the command: `/path/to/acr-mcp serve`
2. Set environment variables:
   - `ACR_API_URL`: Base URL of the ACR API.
   - `ACR_API_TOKEN_FILE` (or `ACR_API_TOKEN` / `ACR_API_TOKEN_KEYRING_SERVICE`): Credential source, checked in that precedence order.
   - (Optional) `ACR_API_TIMEOUT`: Request timeout as a Go duration string (e.g. `20s`). Default: `20s`.
   - (Optional) `ACR_API_PROXY_URL`: HTTP proxy URL.
   - (Optional) `ACR_API_CA_BUNDLE`: Path to a PEM-encoded CA bundle file.
   - (Optional) `ACR_ENABLE_WRITEBACK`: Read only for `acr-mcp doctor` diagnostics; it does not enable `record_episode`, which is unavailable in this release (tracked under CHAOS-2909).
3. Connect to the subprocess via STDIN/STDOUT.
4. Send MCP requests as JSON-RPC 2.0 messages.
5. Parse MCP responses from STDOUT.

## Example: Python MCP Client

```python
import subprocess
import json
import os

# Set up environment
env = os.environ.copy()
env["ACR_API_URL"] = "https://api.dev-health.example.com"
env["ACR_API_TOKEN_FILE"] = os.path.expanduser("~/.acr/token")

# Launch the sidecar
process = subprocess.Popen(
    ["/path/to/acr-mcp", "serve"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    env=env,
    text=True,
)

# Send an MCP request (example: initialize)
request = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {
            "name": "example-client",
            "version": "1.0.0",
        },
    },
}

process.stdin.write(json.dumps(request) + "\n")
process.stdin.flush()

# Read the response
response_line = process.stdout.readline()
response = json.loads(response_line)
print(json.dumps(response, indent=2))

# Clean up
process.terminate()
```

## Example: Node.js MCP Client

```javascript
const { spawn } = require("child_process");
const readline = require("readline");

// Set up environment
const env = {
  ...process.env,
  ACR_API_URL: "https://api.dev-health.example.com",
  ACR_API_TOKEN_FILE: `${process.env.HOME}/.acr/token`,
};

// Launch the sidecar
const sidecar = spawn("/path/to/acr-mcp", ["serve"], { env });

// Create a readline interface for reading responses
const rl = readline.createInterface({
  input: sidecar.stdout,
});

// Send an MCP request (example: initialize)
const request = {
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: {
    protocolVersion: "2024-11-05",
    capabilities: {},
    clientInfo: {
      name: "example-client",
      version: "1.0.0",
    },
  },
};

sidecar.stdin.write(JSON.stringify(request) + "\n");

// Read the response
rl.once("line", (line) => {
  const response = JSON.parse(line);
  console.log(JSON.stringify(response, null, 2));
  sidecar.kill();
});
```

## Diagnostics

Before integrating with your MCP client, verify the sidecar is working:

```bash
export ACR_API_URL="https://api.dev-health.example.com"
export ACR_API_TOKEN_FILE="$HOME/.acr/token"
/path/to/acr-mcp doctor
```

This should output a JSON report with status `ok` if everything is configured correctly.

## Troubleshooting

### Sidecar Exits Immediately

- Check that `ACR_API_URL` and `ACR_API_TOKEN_FILE` are set.
- Run `acr-mcp doctor` to see what's missing.

### "ACR API credential is not configured"

- Verify the token file exists and is readable.
- Check that `ACR_API_TOKEN_FILE` points to the correct path.
- Ensure the token file is not empty.

### "ACR_API_URL is not configured"

- Verify `ACR_API_URL` is set in the environment.

### Timeout or Slow Responses

- Increase `ACR_API_TIMEOUT` in the environment.
- Check network connectivity to the API server.

### Permission Denied

- Ensure the sidecar binary is executable: `chmod +x /path/to/acr-mcp`
- Ensure the token file has correct permissions: `chmod 600 ~/.acr/token`

## MCP Protocol Reference

The sidecar implements the Model Context Protocol (MCP) 2024-11-05 specification. For details on the protocol, see the official MCP documentation.

## Next Steps

- See `docs/mcp-sidecar.md` for detailed configuration and troubleshooting.
- Run `acr-mcp doctor` to verify your setup.
