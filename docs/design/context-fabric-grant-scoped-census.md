# Grant-scoped evidence census

CHAOS-12452, PR1. Plan and rulings: the ticket. This records what shipped.

## The rule

A restricted caller's census runs inside its grant. The grant is ANDed into
every statement of every census call. A grant that cannot apply is an error,
never a read of the organization.

Before this change a restricted caller got no census: an organization-wide count
would be an existence oracle. A pull request named by number then stalled in a
truncated search for that caller alone. An unrestricted caller was rescued.

## What reads the census

`contextfabric.ScopeCensusToCaller` binds the grant once, on the context the
census reads with. The adapter (`devhealthsource.NewCensusFunc`) reads it on
every call, so no call can be missed.

| Caller of the census | Grant |
| --- | --- |
| Main loop of the round | bound on `roundCtx` (`runShadowEvidenceRoundForResolution`) |
| `kindInsensitivityProof` | same context |
| `confirmedHandleInsensitivityProbe` | same context |
| `explicitHandleSatisfier` | bound at the call; anchored on a committed repository as well |
| `VerifyHandle`, fresh `handr_` redemption (`structure.go`) | bound by the engine before `HandleVerifier` |
| `VerifyHandle`, remembered need (`chaos5639_confirmed_need.go`) | bound by the engine before `HandleVerifier` |
| `directread` handle lookup (`subject_lookup_modes.go`) | no grant bound: one anchored census per granted repository, already scoped; work items refused by type |

`grantEnforced` wraps the census at the round, the explicit handle and
`VerifyHandle`. It refuses an outcome that does not report `GrantApplied`, so a
census that ignores the grant decides nothing.

## The statement

Repository-column kinds (pull request, CI run, review) take the same clause as a
requested repository filter, under its own binding names (`census_grant_N`):
the count, the set read and the row read all carry it. A requested narrowing
ANDs a second clause (`census_repo_N`) beside it.

An owner wildcard in a grant admits what the graph gate admits for that owner:
a valid two-part slug (`reposlug.NamePattern`), not only the prefix. `acme/*`
does not cover `acme/group/secret` or `acme/trailing-`, because
`NormalizeRepositorySlug` refuses both. A requested filter keeps its prefix: it
has a post-filter, and its statement stays byte-identical.

`ErrCensusGrantNotApplied` ends the read, with no statement, for: more than 200
slugs, a bare `*`, an invalid or reserved slug, an empty list, or a kind with no
repository column. The round reports it as `census_error`. A requested filter
that cannot apply keeps its old fallback; the grant stays in the statement.

## Gates

- `grantScopedCensus(principal)` is `ClassifyGrant == GrantRestricted`. It is the
  only widening.
- `grantScopedRequest` ORs into the census visibility at both sites. No
  narrowing, a repository narrowing, or a team narrowing with its reader wired
  gets a census. A project narrowing, or repositories with teams, does not: as
  for an unrestricted principal, a satisfier outside the request must not decide.
- The count of matches outside the requested repositories is CHAOS-12446's own
  rule: it holds what the caller's grant admits, and only ordinary search feeds
  it. The census reads inside the grant AND the request, and a satisfier the
  request refuses leaves before the merge, so the census neither adds to that
  count nor contradicts it.
- Unrestricted and universal callers bind nothing. Their statements do not
  change.
- Project-only stays refused. See `context-fabric-project-only-scope.md`.

## Telemetry

`evidence_round` adds `shadow_grant_scoped`. On a grant-scoped round it is true
and `shadow_unscoped_visibility` is false. On every other line it is false.

## Work items

PR1 reads no work item under a grant. `work_items` has no repository column the
entity tree accepts, so a work-item census under a grant is the refusal error:

- The round reports `would_clarify` with reason `census_error`, where it
  reported `scoped_visibility`. The outcome does not change.
- A restricted caller's `handr_` redemption of a work-item handle is now vetoed
  (`reverify_rejected`), where an organization-wide census used to answer yes or
  no. That answer was an existence oracle: an offer is minted from the question
  alone, so a caller could name any key and read the verdict. PR2 restores the
  redemption inside the grant.
- The same holds for a project-only caller, for every kind.
- The caller reads the fresh veto as status `no_match` (label "no answer"), the
  sentence "a structure confirmation receipt could not be resolved", and the
  echoed member with disposition `vetoed_unresolved`. That is the text of every
  unresolved veto, a real absence included: it never says the handle does not
  exist, and a refused census cannot be told from an absent row.
- A remembered handle that fails the same read is dropped alone, as any remembered
  member is. The drop goes to telemetry (`reverify_not_confirmed`); the caller
  gets no sentence, and the turn runs as if the handle was never confirmed.
- Until PR2 the offer is still minted, so that option ends in the veto.

A round that pools a work item beside a pull request is reported the same way:
one refused kind leaves the round incomplete, so it clarifies.

## Residuals

- **Timing.** The filter runs in the scan, so a statement reads the organization
  either way. Duration follows organization size, not a match outside the grant,
  and the statement sequence is identical. One matching outside row costs
  ClickHouse a row filter. That is not measurable as a proof, so the tests assert
  the sequence. A sub-millisecond difference stays a named residual.
- **A slug the grant cannot hold.** `NormalizeRepositorySlug` accepts owner/name
  only. A grant entry of another shape refuses that caller's census; it never
  widens it.
