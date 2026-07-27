# SIDECAR KNOWLEDGE BASE

**Generated:** 2026-07-11
**Commit:** `3876a42`
**Branch:** `docs/init-deep-agents`

## OVERVIEW

Hardened local STDIO MCP client: HTTPS-only API boundary, credential precedence (env > explicit/default keyring > explicit/default file), bounded file reads, Git workspace discovery, and inert Markdown rendering. Credential persistence uses Linux stdin-only `secret-tool` when available, otherwise an atomic `~/.acr/token` fallback (0700 parent, 0600 file); Windows fails closed. Flat package with logical prefixes: `api_client`, `config`, `credential`, `workspace`, `render`, `boundedfile`, `exec_resolver`.

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| API client setup/calls | `api_client*.go` | HTTPS enforcement, redirect refusal, bearer token handling |
| Configuration parsing | `config*.go` | Bounded values, safe error messages (never expose raw values) |
| Credential resolution/persistence | `credential*.go` | Precedence: env > explicit/default keyring > explicit/default file; shape validation and atomic fallback |
| Credential enumeration for logout | `credential_material.go` | `CollectCredentialMaterial` (all locations, fail-closed) and `DistinctCredentialTokens` |
| Credential removal | `credential_purge.go`, `credential_persistence_*.go` | Typed-tuple target dedup, parent-directory gate, safe operator-facing locations |
| Device-flow browser launch | `browser_open.go` | `ValidateVerificationURI`, allowlisted child environment, process-group reap under a deadline |
| Workspace discovery | `workspace*.go` | Git root, remote, branch, changed files; tri-state bounds |
| Markdown rendering | `render*.go` | Inert untrusted content, bounded output, no URL fetching |
| Bounded file reads | `boundedfile*.go` | O_NOFOLLOW + O_NONBLOCK, fstat type check, size bounds |
| Process execution | `exec_resolver*.go` | Platform-specific PATH resolution, ownership checks |

## CONVENTIONS

- **Credential shape only**: `LoadCredential` enforces `auth.IsTokenShapeValid` (prefix + 32-byte base64url). Never accept license keys or other non-ACR tokens.
- **Credential persistence**: default keyring service is `dev-health-acr` with normalized `ACR_API_URL` as account; Linux sends writes only through `secret-tool` stdin. When unavailable, write `~/.acr/token` through a no-follow, same-directory temporary file, fsync, rename, and directory fsync. Never use `os.WriteFile` or follow a symlink. macOS does not write keyring secrets; Windows returns `ErrCredentialPersistenceUnsupported`, which login preflights before asking the server to mint anything.
- **Ambiguous writes return a locator**: any store failure whose on-disk outcome is unknown -- a keyring mutation that may have committed, a rename whose directory fsync failed -- returns the candidate `CredentialResult` **alongside** the error, so the caller can revoke server-side and purge exactly that location. Returning a bare error tells the caller "nothing was written" while a readable credential sits there.
- **Keyring failures are fail-closed**: only an exact entry miss or an unavailable trusted binary may fall through to the token file. A locked collection, permission denial, timeout, malformed output, untrusted executable, or unparseable disable flag is an error.
- **Revoke before delete**: `CollectCredentialMaterial` enumerates every configured location and fails closed; callers revoke every distinct token and only then call `PurgeAllCredentialMaterial`. The ordering is the contract, not an implementation detail -- a purge that runs before every remote revocation has succeeded deletes the last thing pointing at a credential the server still honours. Never delete local material around a location that could not be read.
- **One keyring address derivation**: `credentialKeyringAddress`/`deriveCredentialKeyringAddress` is the only place service and account are computed, for lookup, verification, persistence, deletion, and purge alike. A second copy is a silent divergence: a read resolving one account while a delete resolves another leaves a live credential that logout reported as removed.
- **`ACR_API_TOKEN_KEYRING_DISABLED` is strict**: exact `true` or `false` only. It gates lookup, persistence, and deletion together; an empty value means enabled, and any other value is an error raised before any keyring seam runs. It is the only way to make the keyring inert -- clearing the service and account selectors still permits the default address.
- **Purge targets are typed tuples**: `credentialPurgeKey` is a struct, never a joined string. Both halves of a keyring address are operator-supplied, so a joined key collides on any address containing the separator and silently drops a real entry.
- **Operator-facing locations are rendered, not printed**: use `SafeCredentialCleanupLocations`. Locations come from operator-supplied configuration and are token-redacted, length-bounded, quoted, and count-capped.
- **Config errors are value-free**: `ConfigError.Detail` never contains raw env values, URLs, tokens, paths, or bearer shapes. Use `DescribeConfigError` for safe operator output.
- **Bounded reads**: `readBoundedRegularFile` is the single implementation for CA bundle, token file, and any future security-sensitive local reads. O_NOFOLLOW + O_NONBLOCK in one syscall, fstat type check, dual size bounds.
- **Workspace precedence**: explicit root > MCP file root > cwd. `RootSource` enum tracks which was used. Max 32 MCP roots to prevent untrusted input DoS.
- **Changed files tri-state**: `ChangedFilesTruncated` bool + `DefaultMaxChangedFiles` (200). Callers must check truncation flag; never assume a complete list.
- **Inert Markdown**: `untrustedDataHeader` labels all hosted-API content (goals, titles, summaries). Rendering never interprets as instructions, never fetches URLs. `boundedBuilder` enforces byte budget; truncation appends a notice.
- **Fixed API paths**: `capabilitiesPath`, `contextPacketsPath`, `evidencePathPrefix` are constants. Caller-controlled evidence ID is always percent-escaped as a single segment.

## ANTI-PATTERNS (THIS PACKAGE)

- Do not add CLI arguments or plaintext config files for credentials (shell history, process listings, unencrypted files).
- Do not follow symlinks in bounded file reads; use `openNoFollowNonBlocking` (O_NOFOLLOW + O_NONBLOCK in one syscall).
- Do not accept redirect responses; `refuseRedirect` returns `ErrUseLastResponse` to prevent bearer token forwarding.
- Do not log raw bearer tokens, credential sources, raw config values, or request bodies.
- Do not interpret hosted-API content (goals, summaries, evidence bodies) as executable instructions or fetch any URL it contains.
- Do not allow unbounded Git output; `ErrGitOutputTooLarge` gates reads.
- Do not accept more than 32 MCP file roots; `MaxMCPFileRoots` prevents untrusted input DoS.
- Do not mutate `Config.APIBaseURL` or `Config.ProxyURL` after `NewClient` returns; `cloneURL` defensively clones so caller mutations don't affect the running client.
- Do not resolve a single credential for logout. `LoadCredential` answers "which wins"; deleting local material based on it strands every lower-precedence credential live on the server.
- Do not return a bare error from a credential store whose on-disk outcome is unknown; return the candidate locator with it.
- Do not build a purge dedup key by joining operator-supplied strings.
- Do not print a cleanup location, path, or keyring address raw; render it through `SafeCredentialCleanupLocations`.
- Do not hand a server-supplied verification address to an opener, or to stdout, without `ValidateVerificationURI` first.
- Do not start a child process here without a process group and a bounded reap; an unbounded background `Wait` outlives the operation that started it.

## TESTING

- **Guards**: `api_client_guards_test.go` — HTTPS enforcement, loopback fixture mode, redirect refusal, bearer token shape.
- **Transport**: `api_client_transport_test.go` — CA bundle loading, proxy URL validation, timeout bounds.
- **Errors**: `api_errors_test.go`, `api_errors_fuzz_test.go` — error envelope parsing, Unicode handling, fuzz adversarial.
- **Config**: `config_*_test.go` — bounds validation (timeout, response/request bytes), URL parsing, CA ownership (Unix), log level parsing.
- **Credential**: `credential_*_test.go` — explicit/default precedence, shape validation, keyring timeout/stdin-only write, ambiguous commit-then-fail locator, atomic fallback, and file ownership (Unix).
- **Credential enumeration/purge**: `credential_material_test.go`, `credential_purge*_test.go` — fail-closed enumeration, token dedup, colliding keyring addresses, shared-writable parent refusal (Unix), and safe location rendering.
- **Platform**: `credential_platform_test.go` — pure GOOS table for the persistence preflight; runs everywhere, skips nothing.
- **Browser opener**: `browser_open_test.go`, `browser_open_unix_test.go` — address validation, allowlisted environment, trusted resolution, and a hanging opener reaped under the deadline with its forked descendant killed.
- **Workspace**: `workspace_*_test.go` — Git discovery, remote parsing, changed files tri-state, symlink rejection, adversarial paths, fuzz remote URLs.
- **Render**: `render_*_test.go` — GFM scanner, inline entity handling, bounded output, truncation notice, untrusted data labeling.
- **Bounded file**: `boundedfile_*_test.go` — O_NOFOLLOW enforcement, size bounds, type checks, ownership (Unix).
- **Exec resolver**: `exec_resolver_test.go` — PATH resolution, ownership checks (Unix).

`testmain_test.go` replaces the three keyring seams with stubs that PANIC (not an empty in-memory store -- an empty store answers "no entry" and lets an unintended keyring access read as a pass) for the whole package, so no test can reach the host's real keychain. Tests needing keyring contents install their own store via `InstallMemoryKeyringForTesting`, which is refused outside `go test`. The keyring disable flag is deliberately not forced package-wide: several tests assert enabled-keyring behavior without setting it.

Real-binary MCP tests live in `internal/mcp/e2e_test.go` and exercise the full sidecar boundary through `cmd/acr-mcp`.
