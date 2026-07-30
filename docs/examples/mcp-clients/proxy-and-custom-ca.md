# Proxy and Custom CA Configuration

This page is the focused reference for two optional sidecar settings that
come up most often in corporate/managed-network setups: routing through an
explicit HTTP proxy, and trusting a private or self-signed TLS certificate
authority for the hosted ACR API connection. Each IDE guide in this
directory ([Claude Code](claude-code.md), [Cursor](cursor.md),
[Codex](codex.md), [Generic STDIO](generic-stdio.md)) links here instead of
repeating this detail.

## `ACR_API_PROXY_URL` (optional)

An explicit HTTP proxy URL for every hosted API request. Example:

```bash
export ACR_API_PROXY_URL="http://proxy.corp.example.com:8080"
```

- When set, it **replaces** the standard `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`
  environment resolution entirely for hosted API requests -- the sidecar
  does not merge the two. If you already rely on those variables and don't
  need a different proxy specifically for ACR traffic, leave
  `ACR_API_PROXY_URL` unset.
- The value must be a valid absolute URL with a host (for example
  `http://proxy.example.com:3128`); a bare host:port pair, or a URL missing
  a host, is rejected. `acr-mcp doctor`'s JSON output never echoes the
  configured proxy value, valid or not -- only a fixed, value-free
  description of what's wrong when it's rejected.
- Proxy authentication (userinfo embedded in the proxy URL, e.g.
  `http://user:pass@proxy.example.com:3128`) is supported by the
  underlying Go HTTP transport, but avoid it in an `env` block that ends
  up in a git-committed client config; prefer a proxy that authenticates
  by source IP/network, or a proxy credential source your client injects
  at process-launch time instead.

In an MCP client config, add it to the same `env` block as the other
sidecar variables:

```json
"env": {
  "ACR_API_URL": "https://api.dev-health.example.com",
  "ACR_API_PROXY_URL": "http://proxy.corp.example.com:8080"
}
```

## `ACR_API_CA_BUNDLE` (optional)

Path to an additional PEM-encoded CA bundle file, layered on top of the
system trust store, for the hosted API's TLS connection. Use this when
your ACR API endpoint is served behind a corporate TLS-inspecting proxy or
a self-signed/internal certificate authority that the OS trust store
doesn't already include.

```bash
export ACR_API_CA_BUNDLE="/path/to/corp-ca-bundle.pem"
```

- Must point to a regular file, not a directory, a symlink, a FIFO, or any
  other special file type -- the sidecar's bounded file reader rejects
  every other type outright, the same way it rejects a non-regular token
  file (see [Advanced Token-File Override](claude-code.md#advanced-token-file-override)
  in each client guide).
- The file must contain valid PEM certificate data; an empty, truncated,
  or non-PEM file is rejected by `acr-mcp doctor` and at `serve` startup
  alike, before any network connection is attempted.
- **macOS and Linux only.** On every other platform, including Windows,
  loading a configured `ACR_API_CA_BUNDLE` fails closed: the sidecar
  refuses to start rather than silently skipping the extra CA (see
  Platform Support in `docs/mcp-sidecar.md`).
- `acr-mcp doctor`'s JSON output never echoes the configured CA bundle
  path or its contents -- only whether it is present and whether it
  validated successfully.

In an MCP client config:

```json
"env": {
  "ACR_API_URL": "https://api.dev-health.example.com",
  "ACR_API_CA_BUNDLE": "/path/to/corp-ca-bundle.pem"
}
```

## Verifying proxy and CA configuration

Neither setting requires a network round trip to validate structurally.
Run `acr-mcp doctor --offline` (guaranteed network-free) after setting
either variable and check the `api_url` entry in the `checks` list -- an
invalid proxy URL or an unusable CA bundle is reported there as
`status: "error"` with a fixed, value-free `detail` string, before any
network connection is attempted:

```bash
export ACR_API_URL="https://api.dev-health.example.com"
export ACR_API_PROXY_URL="http://proxy.corp.example.com:8080"
export ACR_API_CA_BUNDLE="/path/to/corp-ca-bundle.pem"
acr-mcp doctor --offline
acr-mcp login
```

The first command can validate the API/proxy/CA settings before a credential
exists (the credential check will still report incomplete). Login then persists
the credential in its default source; MCP registration does not need a token or
token-file path.

Once those static checks report a valid, complete configuration, plain
`acr-mcp doctor` (no flags) automatically attempts a real, bounded live
capabilities handshake too -- confirming the hosted API is actually
reachable through the configured proxy and trusts the configured CA --
with no extra flag required. `acr-mcp doctor --live` is a compatible,
explicit alias for that same automatic behavior; use `--offline` instead
whenever you specifically want to skip the network call, including when
the static configuration is already valid:

```bash
acr-mcp doctor          # attempts the live handshake automatically once static checks pass
acr-mcp doctor --live   # equivalent, explicit form
acr-mcp doctor --offline  # always network-free, regardless of configuration validity
```

For a secrets-free snapshot of all of the above -- to share through an
approved private support channel instead of pasting `doctor` JSON by hand
-- see [Diagnostic Bundles](README.md#diagnostic-bundles).
