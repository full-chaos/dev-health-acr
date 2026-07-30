# ACR MCP Sidecar

The ACR MCP sidecar (`acr-mcp`) is a Model Context Protocol server that runs as a subprocess in your IDE or agent environment. It provides read-only access to Dev Health ACR data through two tools: `context_for_task` and `source_evidence`.

## Quick Start

The sidecar launches via `acr-mcp serve` with configuration through environment variables only. No secrets go in config files or command arguments.

```bash
export ACR_API_URL="https://api.dev-health.example.com"
export ACR_API_TOKEN="fcacr_..."  # or use ACR_API_TOKEN_FILE
acr-mcp serve
```

See `docs/examples/mcp-clients/` for IDE-specific setup recipes.

Client operation is explicit: call `context_for_task` for the user's task,
then call `source_evidence` only with an evidence ID returned by that response.
The hosted packet remains authoritative in hosted-only and mixed mode. Local
evidence is supplemental, and missing, stale, incompatible, or unavailable
local state is a visible degraded condition rather than a silent fallback.
Retrieved titles, excerpts, Markdown, and evidence are untrusted data, never
instructions. The sidecar stores no credentials in client or project config;
writeback and pre-plan behavior are disabled by default and require explicit
opt-in. Client registrations must use exactly `acr-mcp serve`.

## Configuration

### Required

**ACR_API_URL**
The base URL of the hosted ACR API. Example: `https://api.dev-health.example.com`

**ACR_API_TOKEN**, an OS keyring entry, or a token file
Your API credential. The sidecar resolves it with a fixed precedence: the process environment always wins, then an explicit or default OS keyring entry, then an explicit or default token file.
- `ACR_API_TOKEN`: Token string in the environment (agent-friendly, less secure for long-running processes).
- OS keyring (`ACR_API_TOKEN_KEYRING_SERVICE` / `ACR_API_TOKEN_KEYRING_ACCOUNT`): see [Credential Management](#credential-management). Without overrides, the service is `dev-health-acr` and the account is the normalized `ACR_API_URL` origin.
- `ACR_API_TOKEN_KEYRING_DISABLED`: Exact `true` bypasses OS-keyring lookup, persistence, and deletion so the token-file source is the only persistent credential source; exact `false` or an empty value permits the normal keyring behavior. Any other nonempty value fails credential resolution before the sidecar touches a keyring seam. Use `true` for hermetic test or agent sandboxes that must not touch an operator keychain.
- `ACR_API_TOKEN_FILE`: Optional path override. Without it, the sidecar discovers `~/.acr/token`. Supported only on macOS and Linux: the file must deny group and world access (mode `0600`, i.e. `info.Mode().Perm()&0o077 == 0`); the sidecar refuses to load it otherwise. **On every other platform, including Windows, the sidecar fails closed and refuses to load a token file at all** -- use `ACR_API_TOKEN` instead; the OS keyring source above is macOS/Linux only too. Preferred for persistent processes on macOS/Linux.

### Optional

**ACR_API_TIMEOUT**
Request timeout as a Go duration string (for example `20s`, `90s`, `1m30s`) bounding a single hosted API call end to end (dial, TLS, request, response). Default: `20s`. Must be between `1s` and `2m`.

**ACR_API_MAX_RESPONSE_BYTES**
Maximum bytes of a hosted response body the sidecar reads before treating the response as too large. Default: `1048576` (1 MiB). Must be between `8192` and `8388608`.

**ACR_API_MAX_REQUEST_BODY_BYTES**
Maximum serialized size of outgoing request bodies (currently only the context-packet request). Default: `262144` (256 KiB). Must be between `1024` and `4194304`.

**ACR_API_PROXY_URL**
HTTP proxy URL. Example: `http://proxy.corp.example.com:8080`. When set, it replaces the standard `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` environment resolution for all hosted API requests.

**ACR_API_CA_BUNDLE**
Path to an additional PEM-encoded CA bundle file, layered on top of the system trust store for the hosted API TLS connection. Must be a regular file, not a directory. Use when the API server uses a self-signed or corporate certificate. Supported only on macOS and Linux; on every other platform, including Windows, loading fails closed (see Platform Support below).

**ACR_API_ALLOW_INSECURE_LOOPBACK**
Boolean (`true`/`false`). Opts into plain HTTP instead of HTTPS, and only when `ACR_API_URL` resolves to a loopback host (`127.0.0.1`, `::1`, or `localhost`). Default: `false`. For local fixture/test drivers only; never set this against a non-loopback host.

**ACR_SIDECAR_CLIENT_NAME**, **ACR_SIDECAR_CLIENT_VERSION**, **ACR_SIDECAR_VERSION**
Identify this sidecar instance to the hosted API (client info payload, `X-ACR-Client-Version` header). Defaults: `dev-health-acr-mcp`, `dev`, `dev`. In a release binary built with `-ldflags` version injection (see [CHAOS-2926](https://linear.app/fullchaos/issue/CHAOS-2926)), `serve` and plain `doctor` use the compiled release identity before constructing the hosted API client, so environment values cannot spoof a released binary. An unreleased local fixture may set an explicit valid SemVer value for compatibility testing; a literal `dev` sentinel is rejected by a real hosted API.

**ACR_LOG_LEVEL**
`acr-mcp serve`'s structured diagnostic verbosity, written to stderr as JSON. One of `debug`, `info`, `warn`/`warning`, or `error` (case-insensitive). Default: `info`. An unrecognized value is rejected at startup (fails closed, same as every other sidecar-config invariant) rather than silently falling back to the default. This level only controls how much non-secret operational detail (startup version/service identity/enabled-tools/entitlement summary) is emitted; it never gates redaction -- credentials and response bodies are never logged regardless of level.

**ACR_ENABLE_WRITEBACK**
Boolean (`true`/`false`). When `true`, enables the `record_episode` tool if all four gates pass: (1) this flag is `true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Default: `false`. Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; plain doctor (or `doctor --live`) diagnoses the hosted gates.

### Optional local CodeGraph evidence

CodeGraph is an optional local evidence provider, not an ACR-managed service.
The sidecar consumes an existing index read-only and never installs CodeGraph,
creates or refreshes an index, queries or parses CodeGraph SQLite storage, or
parses human-oriented output. On supported Unix platforms it may perform a
bounded read-only `codegraph.db` identity check; it does not use that file as an
evidence/query store. Its direct/managed guard permits only CodeGraph `>=1.2.0,<2.0.0` JSON
commands `status`, `query`, `callers`, `callees`, `impact`, `affected`, and
`files`; it never invokes `init`, `index`, or `sync`.

| Variable | Default | Accepted values / bounds |
| --- | --- | --- |
| `ACR_LOCAL_INDEX_PROVIDER` | `auto` | `auto`, `disabled`, or `codegraph` |
| `ACR_CODEGRAPH_EXECUTABLE` | trusted-name resolution | optional absolute executable path |
| `ACR_LOCAL_INDEX_TIMEOUT` | `3s` | Go duration, `100ms`–`15s` |
| `ACR_LOCAL_INDEX_MAX_ITEMS` | `5` | `1`–`12` |
| `ACR_LOCAL_INDEX_MAX_OUTPUT_TOKENS` | `1000` | `125`–`4000` |
| `ACR_LOCAL_INDEX_MAX_SERIALIZED_BYTES` | `65536` | `2048`–`262144` |
| `ACR_LOCAL_INDEX_STALE_POLICY` | `graceful` | `graceful` or `strict` |

`disabled` gives hosted-only results. `auto` and `codegraph` attempt the local
provider only after hosted bootstrap remains usable. Invalid settings, a missing
or incompatible executable/index, a timeout, or a workspace mismatch never
turn a hosted request into local-only success. Under `graceful`, usable stale
evidence is labeled stale; under `strict`, stale or mismatched local evidence is
omitted. CodeGraph 1.2.0 does not expose an indexed commit, so the sidecar emits
`indexed_commit_unknown` rather than substituting the workspace HEAD.

When local evidence is usable, `context_for_task` preserves the hosted packet
unchanged and may add `local_context` plus `federated_budget`. Packet-content
limits apply to hosted packet content plus local items/evidence only; the MCP
envelope, `federated_budget`, and `rendered_markdown` are separately bounded and
excluded from those caller limits. Local identifiers are opaque
`local:codegraph:v1:<sha256>` values. Their source expansion is served only from
the sidecar's bounded 1024-entry, 30-minute local cache; unknown or expired local
IDs are safe not-found responses and are never sent to the hosted API.

## Credential Management

### Platform Support

The `acr-mcp` binary builds and runs on macOS, Linux, and Windows. Credential and CA-bundle sources differ by platform:

- `ACR_API_TOKEN` (environment variable): works on every supported platform.
- OS keyring (`ACR_API_TOKEN_KEYRING_SERVICE`): macOS and Linux only (see below).
- `ACR_API_TOKEN_FILE` and `ACR_API_CA_BUNDLE` (local file reads): macOS and Linux only. These reads use an atomic, symlink- and FIFO-resistant open implementation verified only on macOS and Linux; on every other platform, including Windows, the sidecar fails closed and refuses to load the file rather than falling back to an unverified or less-safe read path. `acr-mcp doctor` and `LoadCredential`/`LoadConfig` report this as a load error, not a silent skip.


If you need a persistent, non-environment credential on Windows today, there is no supported option; track platform support before relying on `ACR_API_TOKEN_FILE` there.

### Environment Variable (ACR_API_TOKEN)

Simplest for local development and agent-based workflows. The token is visible in process listings and environment inspection.

```bash
export ACR_API_TOKEN="fcacr_your_token_here"
acr-mcp serve
```

### OS Keyring (ACR_API_TOKEN_KEYRING_SERVICE)

Optional convenience source consulted between the environment and the token file. It defaults to service `dev-health-acr` and account equal to the normalized `ACR_API_URL` origin. `ACR_API_TOKEN_KEYRING_SERVICE` and `ACR_API_TOKEN_KEYRING_ACCOUNT` explicitly override those defaults.

Set `ACR_API_TOKEN_KEYRING_DISABLED=true` to skip this source entirely, including login
persistence and logout deletion. The setting accepts only exact `true` or `false`; an empty
value uses the normal keyring behavior, while any other nonempty value fails before a keyring
lookup, write, or deletion. This explicit setting is required for hermetic environments;
clearing the service/account selectors alone still permits the default service/account lookup.

```bash
export ACR_API_TOKEN_KEYRING_SERVICE="acr-mcp"
acr-mcp serve
```

- macOS: reads via the `security` CLI (`find-generic-password`).
- Linux: reads via the `secret-tool` CLI (libsecret).
- An **exact entry miss** or an unavailable trusted backend binary falls through to the token file after a bounded 2-second lookup; it never blocks startup.
- **Every other keyring outcome fails closed.** A locked collection, a permission denial, a timeout, malformed output, an untrusted executable, or an unreadable `ACR_API_TOKEN_KEYRING_DISABLED` value is an error, not a fall-through. A stale token file must never silently stand in for a keyring the sidecar could not read, and `logout` must never delete local material while a location it could not read may still hold a live credential -- it reports "local credential material could not be enumerated safely; nothing was removed" and removes nothing.
- Linux persistence writes to `secret-tool` only through stdin; the token never appears in command arguments. macOS intentionally has no keyring writer because its `security` write interface would expose the secret in process arguments; persistence falls back to the token file instead.
- A store that **commits and then fails** (the mutation lands, the collection write-out or reply does not) is ambiguous on disk. `login` receives the candidate service/account back alongside the failure, revokes the issued credential server-side, and purges exactly that entry. If that purge also fails, the exact keyring service and account are reported for operator action.

### Token File (ACR_API_TOKEN_FILE, macOS/Linux only)

Recommended fallback for production and long-running sidecars on macOS and Linux. The default path is `~/.acr/token`; `ACR_API_TOKEN_FILE` is an explicit override. The persistence writer creates its default parent with mode `0700`, creates a same-directory no-follow temporary file at `0600`, fsyncs it, atomically renames it, and fsyncs the directory. The sidecar reads the file at startup and validates permissions. **Not supported on Windows or any other platform** -- see Platform Support below.

```bash
# Create a restricted file
echo "fcacr_your_token_here" > ~/.acr/token
chmod 600 ~/.acr/token

# Point the sidecar to it
export ACR_API_TOKEN_FILE="$HOME/.acr/token"
acr-mcp serve
```

**Permission Requirements:**
- macOS/Linux: File must have mode `0600` (read/write for owner only). The sidecar checks this at load time (`info.Mode().Perm()&0o077 != 0` is rejected) and refuses to load the token if group or world bits are set.
- **The parent directory must not grant group or world write access, for removal as well as for writing.** Removal proves the target is an ACR credential and then unlinks it by name, so a window exists between the two no matter what. Refusing a shared-writable parent narrows who can act in that window to principals who can already write the directory -- the owner, and root -- rather than any local user; it does not make the sequence atomic. `logout` refuses to remove under such a parent and reports the path instead of deleting it. Fix the directory (`chmod go-w`) and run `logout` again.
- **A parent `login` creates is `0700`; a parent that already exists is inspected, not rewritten.** `ACR_API_TOKEN_FILE` is operator-supplied, and unconditionally chmod-ing its parent once reduced an entire home directory to `0700`. Group- or world-writable is refused outright; every other mode bit is left exactly as found.
- Windows and all other platforms: **The token file source is unavailable, full stop.** The sidecar fails closed before it ever inspects permissions and refuses to load any file -- there is no unenforced pass-through. Use `ACR_API_TOKEN`; the OS keyring source is also macOS/Linux only.

If permissions are too loose (macOS/Linux) or the platform doesn't support bounded file loading at all (Windows and others), the sidecar refuses to load the token and exits with an error. It never loads a token file silently or without a permission check.

### Login and Logout

```text
Usage: acr-mcp login [--refresh] [--no-browser] [--org <org>] [--repo <owner/repo>]
Usage: acr-mcp logout
```

`login` runs the device authorization flow, prints the verification address and
user code, and persists the issued credential through the sources above. The
default web approval grants all current and future repositories in the
authenticated organization (`repository_scopes: ["*"]`). `--repo` supplies a
display/request hint; it does not silently narrow the grant. The current web UI
does not offer a limited-grant selector and always approves `["*"]`. Exact
repository scopes remain protocol and service support for explicit clients and
a future limited-grant UX. Local Git discovery, the current working directory,
and analytics repository inventory do not authorize or deauthorize a
repository.
`login --refresh` rotates the credential already in use and never starts a
device flow. Rotation preserves the existing repository scopes; it never widens
an exact credential. To adopt the organization-wide default from an older exact
credential, run `acr-mcp logout`, then complete a fresh `acr-mcp login` approval.
`logout` revokes remotely and then removes local material.

Only one `login`, `login --refresh`, or `logout` may run for the same local user
at a time. If another lifecycle process is still running, the CLI identifies
that live local contention and tells you to stop the other process or wait for
it to finish before retrying. A process that has actually terminated releases
the operating-system lock immediately; an abandoned browser approval does not
require deleting a lock file or bypassing the concurrency guard.

**The verification address is validated before it is printed.** It is
server-supplied data that this command renders into your terminal and hands to a
desktop opener, so it must be `https`, or `http` to a validated loopback address
for local development, and must carry no userinfo, control characters,
whitespace, or `fcacr_` bearer text. An address that fails any of these is
refused without being displayed and without an opener being resolved; the
refusal deliberately quotes no part of it. Validation is client-side: the hosted
contract checks `verification_uri` through a helper shared with every other v1
URI field, and tightening that would change requiredness across the contract.

**`--no-browser` suppresses only the launch.** The address and code are still
printed and still validated, so a headless host, a remote shell, or an automated
QA run completes the flow without a desktop opener ever being resolved or
executed. Without the flag, the launch is attempted and any failure is
deliberately nonfatal -- an absent or untrusted opener costs only the
convenience. The launched opener runs in its own process group with an
allowlisted environment (no `ACR_` variables reach the browser) and is reaped
under a fixed deadline if it blocks. That deadline alone bounded only an
unattended login: launching the opener hands off immediately and reaps in a
background goroutine, and a login that succeeds (or fails, or restarts) before
a slow or hung opener returns would otherwise exit while the deadline was
still pending, orphaning the opener process and anything it forked for
however long they kept running afterward. Login therefore also synchronously
tears the opener down on every return path from the attempt that launched
it -- success, failure, or restart alike -- so the opener's process group
never outlives the attempt regardless of how quickly it concludes, not only
when it happens to run past the deadline.

**`logout` revokes every configured credential before it deletes any of them.**
It enumerates all three sources rather than resolving the single winner by
precedence: an exported `ACR_API_TOKEN` over a keyring entry over a token file is
three credentials, and revoking only the winner left the other two live on the
server while their local copies were deleted. Distinct values are revoked once
each -- the same credential in several locations is deduplicated -- and removal
starts only after every revocation has succeeded. Any revocation failure retains
**all** local material, since a local copy may be the last thing pointing at a
credential that is still live. An established credential the server answers
`invalid_token` for is already inactive and does not block cleanup.

The purge that follows is a single deduplicated pass over the whole set, not one
purge per credential. Purging one at a time reported the same configured
location once per credential and, worse, could remove a location belonging to a
credential whose own remote revocation had not been attempted yet. With
`ACR_API_TOKEN_KEYRING_DISABLED=true` the keyring is skipped for enumeration,
revocation, and removal alike -- a disabled keyring is not consulted even to ask
whether something is there.

**`logout` reports every location it could not clean, and exits nonzero.**
`ACR_API_TOKEN` cannot be unset in your parent shell from inside the process, so
an exported credential is always reported as an unremovable location -- but it is
never an early exit: the keyring entry and token file underneath it are still
revoked and removed first. Unset the variable in your shell to finish:

```bash
acr-mcp logout        # revokes everything, clears the keyring entry and token file
unset ACR_API_TOKEN   # the one location logout cannot reach
```

Reported locations are exact so they are actionable, but they are built from
operator-supplied configuration, so each is token-redacted, length-bounded, and
quoted before printing, and the list itself is capped with the omitted count
stated rather than silently truncated.

**MCP itself stays local.** `login` and `logout` talk to the hosted API over
HTTPS; the MCP surface (`serve`) is local STDIO only and exposes no network
listener, so no credential lifecycle operation is reachable from another host.

### Token Format

A valid token is the literal prefix `fcacr_` followed by the unpadded, URL-safe base64 (`base64.RawURLEncoding`) encoding of a 32-byte (256-bit) random secret: 43 base64url characters, for a fixed total length of 49 characters (`len("fcacr_")` + 43). The sidecar checks only this shape (prefix present, remainder decodes to exactly 32 bytes); it does not decrypt or otherwise inspect the secret. A value that merely starts with `fcacr_` but has the wrong length or non-base64url characters after the prefix is rejected as malformed -- it is not enough to "start with `fcacr_`". Do not parse, truncate, or reformat tokens; treat the whole string as opaque.

## Diagnostics

### Version

```bash
acr-mcp version
# dev commit=unknown built=unknown
```

Prints the full build identity as a single line: `<version> commit=<commit> built=<build_date>`. For an unreleased local build this is exactly `dev commit=unknown built=unknown`; a release binary prints its injected SemVer, full commit SHA, and RFC3339 build date. `--version` and `-version` are compatible aliases with identical output. `metadata` and `doctor` expose the same identity as separate `version`, `commit`, and `build_date` JSON fields.

### Doctor

```bash
acr-mcp doctor
acr-mcp doctor --offline
acr-mcp doctor --live
```

Runs static configuration checks and outputs JSON. When `ACR_API_URL` and the
credential are both valid, plain `acr-mcp doctor` then performs the same
bounded hosted capabilities handshake used by `serve` and includes the live
entitlement, scope, and enabled-tool result. `--live` is a compatible explicit
alias for that default behavior. `--offline` is the explicit network-free mode:
it reports only static checks even when the local configuration is valid.
The JSON output always includes the build identity fields `version`, `commit`,
and `build_date`.

When static configuration is incomplete or invalid, plain `doctor` returns the
static report without a network call. This makes local configuration diagnosis
safe before an API endpoint or credential is usable.

```json
{
  "service": "dev-health-acr-mcp",
  "version": "dev",
  "commit": "unknown",
  "build_date": "unknown",
  "api_url_set": true,
  "api_url_valid": true,
  "credential_set": true,
  "credential_source": "environment",
  "credential_shape_valid": true,
  "write_enabled": false,
  "transcript_capture_enabled": false,
  "log_level": "INFO",
  "checks": [
    {
      "name": "binary",
      "status": "ok",
      "detail": "acr-mcp is executable"
    },
    {
      "name": "transport",
      "status": "ok",
      "detail": "STDIO is the SVS MCP transport"
    },
    {
      "name": "api_url",
      "status": "ok",
      "detail": "ACR_API_URL is configured and valid"
    },
    {
      "name": "credential",
      "status": "ok",
      "detail": "ACR API credential is configured and redacted via environment"
    }
  ],
  "status": "ok"
}
```

Status values:
- `ok`: All checks passed. The sidecar is ready to serve.
- `incomplete_configuration`: ACR_API_URL or credential is missing.
- `invalid_configuration`: ACR_API_URL is set but fails validation (wrong scheme, embedded userinfo, a path/query/fragment, or another sidecar-config invariant), and/or the credential is set but malformed.
- `live_check_unreachable`: static checks passed and the hosted API handshake
  attempted by plain `doctor` or `doctor --live` failed before a valid
  capabilities response was available (for example, network, TLS, or auth).
- `live_check_incompatible`: the hosted API was reached and returned valid
  capabilities, but their version, schemas, enabled tools, entitlement, or
  credential scopes are incompatible with this sidecar. The `live_check`
  fields retain the actual hosted capability values and a safe detail.

#### Live capability check (plain `doctor` or `doctor --live`)

```bash
acr-mcp doctor --live
```

Plain `acr-mcp doctor` runs every static check and -- only when the local
configuration is already valid (`api_url_valid` and `credential_shape_valid`
are both true) -- attempts a real, bounded hosted API capabilities handshake
(the same `NewBootstrap` path `serve` uses). `doctor --live` is a compatible
explicit alias. Both add a `live_check` object reporting the *actual*
entitlement, scope, and enabled-tool availability the hosted API returned for
the configured credential, not just static local configuration:

```json
"live_check": {
  "reachable": true,
  "agent_context_runtime": true,
  "context_read_scope": true,
  "evidence_read_scope": true,
  "enabled_tools": ["context_for_task", "source_evidence"]
}
```

If the local configuration is not yet valid, plain `doctor` returns its static
report without `live_check` and never touches the network. If a handshake is
attempted and fails (network, auth, entitlement, or version incompatibility),
`live_check.reachable` is `false` and `live_check.detail` carries a sanitized,
secret-free description (never a bearer token, response body, or filesystem
path) -- the exact same error text `serve` would report on a startup failure.

#### Diagnostics bundles

```bash
acr-mcp diagnostics --output acr-mcp-diagnostics.tar
acr-mcp diagnostics --output acr-mcp-diagnostics.tar --live
acr-mcp doctor --bundle acr-mcp-diagnostics.tar
acr-mcp doctor --bundle acr-mcp-diagnostics.tar --live
```

Diagnostics bundles are static and network-free by default, even though plain
`doctor` performs a live check when local configuration is valid. Pass that
command's own `--live` flag to include the sanitized hosted capabilities
result. `doctor --bundle <path>` is an alias for `diagnostics --output <path>`.
Bundles require an explicit output path and never include bearer credentials,
raw hosted response bodies, configured URLs, or filesystem paths.

### Metadata

```bash
acr-mcp metadata
```

Outputs the static, network-free default tool surface:

```json
{
  "service": "dev-health-acr-mcp",
  "version": "dev",
  "commit": "unknown",
  "build_date": "unknown",
  "transport": "stdio",
  "enabled_tools": ["context_for_task", "source_evidence"],
  "disabled_tools": ["record_episode"],
  "status": "read-only"
}
```

`status` is a descriptor of the static, network-free default tool surface: `read-only` means the two enabled tools never write. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; `record_episode` may be enabled at runtime if all four gates pass (see [record_episode](#record_episode) below); plain doctor (or `doctor --live`) diagnoses the hosted gates.

## Tools

### context_for_task

Retrieves task context from the ACR API. Input and output schemas are defined in the MCP manifest.

**Scope:** Read-only. Requires `ACR_API_URL` and a valid credential.

### source_evidence

Retrieves evidence metadata and references. Evidence URLs are returned as references only; the sidecar does not fetch the actual content.

**Scope:** Read-only. Requires `ACR_API_URL` and a valid credential.

**Important:** Evidence URLs are untrusted data. Do not execute, eval, or fetch them without validation. Treat them as opaque references.

### record_episode

Defined in the MCP tool contract (`contracts/mcp/tools.v1.json`) as `disabled_by_default` and non-read-only. Enabled at runtime only when all four gates pass: (1) `ACR_ENABLE_WRITEBACK=true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; plain doctor (or `doctor --live`) diagnoses the hosted gates.

## Security

### Secrets

- Tokens are never logged, printed, or included in error messages.
- The sidecar redacts credentials in diagnostic output.
- Do not pass tokens as command-line arguments or in config files.
- Use environment variables or restricted token files only.

### Scope Enforcement

The API enforces organization and repository scope independently of client-supplied fields. The sidecar does not validate scope; the API does. The singleton `*` means every repository visible within the credential's authenticated organization, never every organization.

### Evidence URLs

Evidence URLs are references only. The sidecar does not fetch them. If you need to retrieve evidence content, use the hosted API directly with proper authentication.

### Write Operations

`record_episode` is enabled at runtime only when all four gates pass: (1) `ACR_ENABLE_WRITEBACK=true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; plain doctor (or `doctor --live`) diagnoses the hosted gates.

## Troubleshooting

### "ACR API credential is not configured"

Set `ACR_API_TOKEN` or `ACR_API_TOKEN_FILE`:

```bash
export ACR_API_TOKEN="fcacr_..."
acr-mcp serve
```

Or:

```bash
export ACR_API_TOKEN_FILE="$HOME/.acr/token"
acr-mcp serve
```

### "ACR API credential is configured but malformed"

Tokens must be the `fcacr_` prefix followed by exactly 43 base64url characters decoding to a 32-byte secret (see [Token Format](#token-format)); a truncated, corrupted, or non-ACR credential (including a Dev Health license key) fails this check. Check the token's length and prefix without printing the whole value:

```bash
echo "$ACR_API_TOKEN" | head -c 10
```

### "ACR credential file permissions must not grant group or world access" (POSIX only)

Fix file permissions:

```bash
chmod 600 ~/.acr/token
```

### "ACR_API_URL is not configured"

Set the API URL:

```bash
export ACR_API_URL="https://api.dev-health.example.com"
acr-mcp serve
```

### Timeout or Slow Responses

Increase the timeout:

```bash
export ACR_API_TIMEOUT="60s"
acr-mcp serve
```

### Custom CA Certificate

If the API server uses a self-signed or corporate certificate:

```bash
export ACR_API_CA_BUNDLE="/path/to/ca.pem"
acr-mcp serve
```

### Proxy Issues

If behind a corporate proxy:

```bash
export ACR_API_PROXY_URL="http://proxy.corp.example.com:8080"
acr-mcp serve
```

## Working Directory and Roots

The sidecar does not require a specific working directory. It reads configuration from environment variables only. The MCP client (IDE or agent) is responsible for managing workspace roots and file paths.

## Write Tool Availability

`record_episode` is enabled at runtime only when all four gates pass: (1) `ACR_ENABLE_WRITEBACK=true`, (2) the hosted API grants `agent_context_runtime` entitlement, (3) the credential has `episode:write` permission, and (4) the API's `EnabledTools` list includes `record_episode`. Independently, transcript references in the request require `ACR_ENABLE_TRANSCRIPT_CAPTURE=true` (default `false`); this is not a tool enablement gate, only a validation gate for transcript data. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; plain doctor (or `doctor --live`) diagnoses the hosted gates.

## Next Steps

- See `docs/examples/mcp-clients/` for IDE-specific setup.
- Run `acr-mcp doctor` to verify your configuration.
- Check the hosted API documentation for tool schemas and examples.
