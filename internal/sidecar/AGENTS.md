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
| Workspace discovery | `workspace*.go` | Git root, remote, branch, changed files; tri-state bounds |
| Markdown rendering | `render*.go` | Inert untrusted content, bounded output, no URL fetching |
| Bounded file reads | `boundedfile*.go` | O_NOFOLLOW + O_NONBLOCK, fstat type check, size bounds |
| Process execution | `exec_resolver*.go` | Platform-specific PATH resolution, ownership checks |

## CONVENTIONS

- **Credential shape only**: `LoadCredential` enforces `auth.IsTokenShapeValid` (prefix + 32-byte base64url). Never accept license keys or other non-ACR tokens.
- **Credential persistence**: default keyring service is `dev-health-acr` with normalized `ACR_API_URL` as account; Linux sends writes only through `secret-tool` stdin. When unavailable, write `~/.acr/token` through a no-follow, same-directory temporary file, fsync, rename, and directory fsync. Never use `os.WriteFile` or follow a symlink. macOS does not write keyring secrets; Windows returns `ErrCredentialPersistenceUnsupported`.
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

## TESTING

- **Guards**: `api_client_guards_test.go` — HTTPS enforcement, loopback fixture mode, redirect refusal, bearer token shape.
- **Transport**: `api_client_transport_test.go` — CA bundle loading, proxy URL validation, timeout bounds.
- **Errors**: `api_errors_test.go`, `api_errors_fuzz_test.go` — error envelope parsing, Unicode handling, fuzz adversarial.
- **Config**: `config_*_test.go` — bounds validation (timeout, response/request bytes), URL parsing, CA ownership (Unix), log level parsing.
- **Credential**: `credential_*_test.go` — explicit/default precedence, shape validation, keyring timeout/stdin-only write, atomic fallback, and file ownership (Unix).
- **Workspace**: `workspace_*_test.go` — Git discovery, remote parsing, changed files tri-state, symlink rejection, adversarial paths, fuzz remote URLs.
- **Render**: `render_*_test.go` — GFM scanner, inline entity handling, bounded output, truncation notice, untrusted data labeling.
- **Bounded file**: `boundedfile_*_test.go` — O_NOFOLLOW enforcement, size bounds, type checks, ownership (Unix).
- **Exec resolver**: `exec_resolver_test.go` — PATH resolution, ownership checks (Unix).

Real-binary MCP tests live in `internal/mcp/e2e_test.go` and exercise the full sidecar boundary through `cmd/acr-mcp`.
