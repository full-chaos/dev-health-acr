# Generic STDIO MCP Client Setup

The ACR MCP sidecar uses STDIO transport, which is compatible with any MCP client that supports subprocess-based servers.

## Basic Setup

To integrate the ACR sidecar with a generic MCP client, you need to:

1. Install the sidecar binary.
2. Create a token file.
3. Configure your MCP client to launch the sidecar with the correct environment variables.

## Installing the Sidecar

<!-- FIXTURE:install-sidecar -->
1. **Install the sidecar binary.** The normal install path is a signed
   private release download, not a local build. macOS/Linux:

   ```bash
   # Download the release archive for your OS/arch (e.g. acr-mcp_<version>_darwin_arm64.tar.gz)
   # plus SHA256SUMS and SHA256SUMS.sig from the private GitHub Releases page for
   # full-chaos/dev-health-acr. Do NOT trust a cosign.pub bundled alongside the
   # release assets -- obtain signing/cosign.pub from a reviewed commit in this
   # repository instead, then verify. set -euo pipefail so a failed git,
   # cosign, or checksum step halts here rather than falling through to
   # extract an unverified archive:
   set -euo pipefail
   git show <trusted-ref>:signing/cosign.pub > signing/cosign.pub
   cosign verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig \
     --insecure-ignore-tlog SHA256SUMS
   archive="acr-mcp_<version>_<os>_<arch>.tar.gz"
   checksum_line="$(awk -v name="$archive" '$2 == name' SHA256SUMS)"
   test "$(printf '%s\n' "$checksum_line" | wc -l | tr -d ' ')" = 1
   if command -v sha256sum >/dev/null 2>&1; then
     printf '%s\n' "$checksum_line" | sha256sum --check -
   else
     printf '%s\n' "$checksum_line" | shasum -a 256 --check -
   fi
   tar -xzf "$archive"
   chmod +x acr-mcp
   ```

   See `docs/release-policy.md` for the full verification runbook.
   Windows users: see [Installing on Windows](README.md#installing-on-windows).

   **Development only:** `go build` produces an unversioned `dev` binary. A
   production ACR API rejects a `dev`-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   ```bash
   cd /path/to/acr
   go build -o acr-mcp ./cmd/acr-mcp
   ```

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in `dev` identity:

   ```bash
   export ACR_SIDECAR_VERSION="1.0.0"        # must satisfy the target API's minimum_sidecar_version
   export ACR_SIDECAR_CLIENT_VERSION="1.0.0"
   ```
<!-- /FIXTURE:install-sidecar -->

### Installing on Windows

<!-- FIXTURE:install-sidecar-windows -->
1. **Install the sidecar binary (Windows).** The normal install path is a
   signed private release download, not a local build:

   ```powershell
   # Download the release archive for your Windows build (e.g. acr-mcp_<version>_windows_amd64.zip)
   # plus SHA256SUMS and SHA256SUMS.sig from the private GitHub Releases page for
   # full-chaos/dev-health-acr. Do NOT trust a cosign.pub bundled alongside the
   # release assets -- obtain signing/cosign.pub from a reviewed commit in this
   # repository instead, then verify. $ErrorActionPreference = 'Stop' covers
   # any later failing cmdlet; git.exe and cosign.exe are native
   # executables, so $LASTEXITCODE is checked explicitly right after each:
   $ErrorActionPreference = 'Stop'
   git show <trusted-ref>:signing/cosign.pub > signing/cosign.pub
   if ($LASTEXITCODE -ne 0) { throw "git show failed with exit code $LASTEXITCODE" }
   cosign.exe verify-blob --key signing/cosign.pub --signature SHA256SUMS.sig `
     --insecure-ignore-tlog SHA256SUMS
   if ($LASTEXITCODE -ne 0) { throw "cosign verify-blob failed with exit code $LASTEXITCODE" }

   $archive = "acr-mcp_<version>_windows_amd64.zip"
   $line = @(Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $archive") })
   if ($line.Count -ne 1) { throw "expected exactly one checksum line for $archive" }
   $expectedHash = $line[0].Split(' ')[0]
   $actualHash = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($actualHash -ne $expectedHash) { throw "checksum mismatch for $archive" }

   Expand-Archive -Path $archive -DestinationPath .
   ```

   See `docs/release-policy.md` for the full verification runbook.
   There is no `chmod` equivalent on Windows: an extracted `.exe` is directly
   runnable.

   **Development only:** `go build` produces an unversioned `dev` binary. A
   production ACR API rejects a `dev`-identified sidecar outright (426 Upgrade
   Required, before any tool call is accepted) -- only use this against a
   non-production/test fixture API, never a real hosted ACR API:

   ```powershell
   cd C:\path\to\acr
   go build -o acr-mcp.exe .\cmd\acr-mcp
   ```

   To test a locally built binary against a hosted API that enforces a
   minimum sidecar version, set an explicit valid version override instead
   of relying on the compiled-in `dev` identity:

   ```powershell
   $env:ACR_SIDECAR_VERSION = "1.0.0"        # must satisfy the target API's minimum_sidecar_version
   $env:ACR_SIDECAR_CLIENT_VERSION = "1.0.0"
   ```
<!-- /FIXTURE:install-sidecar-windows -->

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
   - `ACR_API_TOKEN`, an OS keyring entry (`ACR_API_TOKEN_KEYRING_SERVICE` / `ACR_API_TOKEN_KEYRING_ACCOUNT`), or `ACR_API_TOKEN_FILE`: credential source. Precedence is environment first, then the explicit or default keyring entry, then the explicit or default token file -- not the order these names happen to be listed in. Pointing `ACR_API_TOKEN_FILE` at a new credential does not override an exported `ACR_API_TOKEN`.
   - (Optional) `ACR_API_TIMEOUT`: Request timeout as a Go duration string (e.g. `20s`). Default: `20s`.
   - (Optional) `ACR_API_PROXY_URL`: HTTP proxy URL.
   - (Optional) `ACR_API_CA_BUNDLE`: Path to a PEM-encoded CA bundle file.
     See [Proxy and Custom CA Configuration](proxy-and-custom-ca.md) for validation rules and bounds for both settings.
   - (Optional) `ACR_ENABLE_WRITEBACK`: Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. <!-- FIXTURE:doctor-gate-note -->Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; `doctor` diagnoses the hosted gates automatically once local configuration is valid (network-free otherwise), `doctor --offline` forces a network-free check regardless of configuration validity, and `doctor --live` is an explicit, equivalent alias for that automatic behavior.<!-- /FIXTURE:doctor-gate-note -->
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
/path/to/acr-mcp doctor --offline
```

This should output a JSON report with status `ok` if everything is configured correctly. `--offline` keeps this deterministic even though `https://api.dev-health.example.com` above is a placeholder domain -- plain `acr-mcp doctor` (no flags) additionally attempts a real, live capabilities handshake once static configuration is valid; see [Proxy and Custom CA Configuration](proxy-and-custom-ca.md#verifying-proxy-and-ca-configuration) for that behavior.

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
- Run `acr-mcp doctor` to verify your setup, or `acr-mcp diagnostics --output ./acr-diagnostics.tar` for <!-- FIXTURE:bundle-share-caution -->a bundle safe to share only through an approved private support channel (never a public issue tracker)<!-- /FIXTURE:bundle-share-caution --> (see [Diagnostic Bundles](README.md#diagnostic-bundles)).
