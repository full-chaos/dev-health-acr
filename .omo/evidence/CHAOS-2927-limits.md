# CHAOS-2927 limits core evidence

## Scope

Added the additive `internal/limits` package only. It exposes five typed
request policies (`AuthPolicy`, `ContextPolicy`, `EvidencePolicy`,
`SnapshotPolicy`, and `EpisodePolicy`), per-organization concurrency leases,
per-organization and per-credential window totals, and an idempotent
`Claim.DoneClaim()` release.

`Claim` rechecks cancellation both before and after acquiring the internal
mutex, so a request canceled while waiting for the lock consumes no quota,
concurrency slot, or usage counter. Subject identifiers reject empty,
whitespace, control-character, oversized, and invalid UTF-8 values.
Quota and tracking-capacity denials return an upward-rounded retry duration
capped by `Options.MaxRetryAfter` (default 1 minute); concurrency denials
return a small conservative bounded hint (`Options.ConcurrencyRetryAfter`,
default 5s, also capped by `MaxRetryAfter`) rather than 0, since immediate
retry storms were flagged as unsafe.

## Characterization and tests

- Existing authentication limiter characterization passed:
  `go test ./internal/auth -run TestMemoryLimiterAttemptAndFailureWindows -count=1`
- The new API was written failing-first; before implementation,
  `go test ./internal/limits` failed with unresolved `PolicySet`, policy,
  manager, claim, and decision symbols.
- Deterministic tests cover independent endpoint policies, per-org and
  per-credential totals, window rollover, cancellation (including
  lock-wait cancellation via a synthetic `Context.Err` wrapper), malformed
  subjects, invalid policy/configuration, capped retry rounding for both
  quota and concurrency denials, idempotent releases, repeated/negative
  `Complete()` calls, and 16-way concurrent claims. A zero-valued injected
  clock is also supported; it cannot bypass a fixed-window quota.
- Focused race/shuffle run passed:
  `go test -race -shuffle=on ./internal/limits`

## Manual Go driver

`go test -run TestDriverQuotaConcurrencyAndRetryHints -v ./internal/limits`
passed and emitted:

```text
quota denied: reason=credential_quota retry_after=1m0s
concurrency denied: reason=org_concurrency retry_after=5s
```

The driver demonstrates a deterministic quota retry hint and the conservative
bounded concurrency retry hint.

## Reviewer P1 fixes (round 2)

- Bounded state: `Options.MaxTrackedOrganizations` (default 1024) and
  `MaxCredentialsPerOrganization` (default 128) cap map growth; each manager
  operation lazily sweeps expired windows past `Options.StateRetention`
  (default 1h) or their policy window. `TestManagerBoundsAndSweepsTrackedState`
  proves the cap denies new orgs/credentials with `tracking_capacity` and that
  lazy sweep reclaims capacity after expiry.
- Post-lock cancellation: `TestManagerDoesNotConsumeStateAfterCanceledLockWait`
  proves a request canceled while blocked on the mutex returns
  `context.Canceled` and leaves no residual state (a subsequent claim still
  succeeds against the same quota).
- Concurrency denial now returns a nonzero capped retry hint instead of 0;
  `TestManagerCapsQuotaAndConcurrencyRetryHints` proves both quota and
  concurrency retry hints are clamped to `MaxRetryAfter`.
- Usage accounting is now `UsageCounters{Admitted, Denied, Completed, Items,
  Tokens, Bytes}` per org and per credential. `DoneClaim()` remains the
  zero-unit case of the new `Claim.Complete(ResourceUsage)`, which is called
  once (idempotent, and rejects negative units with `ErrInvalidUsage` without
  mutating counters or the concurrency slot).

## Repository gates

`gofmt`, `go vet ./...`, `go build ./...`, `make contract-test`, and
`git diff --check` all pass. Full `go test ./...` and `make verify` now pass
repository-wide (the earlier `internal/api` observability blocker was
resolved upstream). Focused `go test -race -shuffle=on ./internal/limits`
and the manual driver (`TestDriverQuotaConcurrencyAndRetryHints`) pass. All
non-test files in `internal/limits` remain under 250 pure LOC.

## Reviewer P0 fixes (round 3)

- `ResourceBudget{MaxItems, MaxTokens, MaxBytes}` is typed and available on
  each separate endpoint policy. A zero field is unlimited, so this package
  does not duplicate Context Packet assembler defaults; callers opt in only to
  stricter post-admission limits.
- `Claim.Complete(ResourceUsage)` atomically rejects an over-budget result,
  releases its concurrency lease, records completion plus denial without
  crediting excess resource units, and returns `ErrResourceBudgetExceeded`.
  `TestManagerEnforcesPerClassResourceBudget` covers items, tokens, bytes,
  release after rejection, and the per-org/per-credential counters.
- Quota counters now reset by policy window while accounting totals are kept
  until bounded idle retention. Active claims are never swept, so a completion
  after a quota window rollover remains correctly accounted; this is covered
  by `TestManagerRetainsRolloverWindowUntilClaimCompletes`.
- `internal/limits/doc.go` states the deployment boundary: the in-memory
  manager is process-local and cannot enforce a cluster-wide quota or
  concurrency limit across replicas. Multi-replica global enforcement requires
  a shared backend.

## Reviewer round-4 corrections

- Quota retry hints now use the active `quotaStarted` epoch rather than the
  longer-lived accounting start time. `TestManagerUsesQuotaEpochForRolloverRetry`
  proves an exhausted new quota window returns the full positive capped hint.
- `Claim.Complete` caches its terminal result under the same `sync.Once` that
  records completion. Repeating a successful completion remains successful;
  repeating a rejected completion returns `ErrResourceBudgetExceeded`, with no
  second counter mutation. The resource-budget test covers both call orders.
