# Grant-scoped evidence census

CHAOS-12452 (PR1) and CHAOS-12456 (PR2). Plans and rulings: the tickets. This
records what shipped.

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

## When the round runs

`searchTruncated` is set by the adapter before any authorization, so rows a
restricted caller cannot see fill the page. It used to decide the round, and an
inside subject committed only beside hidden rows
(`TestHiddenRowsInAFullPageDoNotDecideARestrictedRound`). For a restricted caller
with a bound handle and nothing committed, the round now runs whatever
`searchTruncated` says.

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

The entity tree relates an issue to a repository only through its linked pull
requests. So a restricted caller reaches a repo-less work item by one rule: **a
native link of record to a pull request of a granted repository.** `explicit_text`
and `heuristic` links never grant. Project, owning team and the team's
repositories are never the relation.

**The census.** `withWorkItemGrantFilter`, in `devhealthsource`:

- The grant passes `grantApplies` first. Past that line the library reads an empty
  list or a `*` as the organization-wide grant.
- `readers.WorkItemScopeSQLAmong` is narrowed to the handle's candidates. The
  statement keeps an id by `Provenance[pr_link].Expr`, never by `AuthorizationExpr`,
  which ORs the project path, and never by the item's own repository.
- `HAVING min(...)`: an id is in only when every row carrying it passes.
- A census with no handle stays refused: a count inside the link arm would miss the
  GitHub and GitLab items the node gate admits.
- A requested repository stays on `linkScoped` and `withinLinkScope`: the library's
  requested selector needs a real repository, so it would deny every repo-less row.

**The node gate.** A repo-less node carries only the no-repository sentinel, so the
gate cannot decide from it. `falkorgraph/link_reach.go` reads, once per adapter
call, the native `LINKS_PULL_REQUEST` edges to pull requests of the grant, and adds
`authorization_link_repositories` in memory to each repo-less node a read returns.
`AuthorizedAttributes` tests it only when the node's list is the sentinel alone.

- Nothing is stored, so no rebuild. A site that is not wrapped denies.
- The read is anchored on the grant and runs at the first read of the call, whatever
  that read returned. So no statement depends on a subject outside the grant.
- A link to a pull request outside the grant, beside one inside, admits the item.
  The outside one is not read. A merged pull request or a closed issue still counts:
  validity in time is not an authorization input.
- A work item with a repository of its own is decided by that repository. A link
  never widens it. Unrestricted, universal and project-only callers read no reach.

| Failure | Subject | Caller reads |
| --- | --- | --- |
| Reach read fails in a turn | stays denied, the turn goes on | `ContextFabricWorkItemLinksUnreadLimitation` |
| Reach read fails at the stored gate or the recheck | `ErrUnavailable` | 503, never `no_match` |
| Reach cut at `linkReachMaxRows` | an id it holds is proven; the rest stay denied | the same sentence |
| Census error, deadline or row bound | `census_error` | the same sentence |
| Census names an item, node gate refuses (projection lag) | `refused` | the same sentence |

**Disclosure.** A census that completed with no linked item gives
`ContextFabricWorkItemNoGrantedLinkLimitation`. It is one text for no such item, an
item outside the grant, an inside item with no link, and a lower-tier link only: it
is true of all four, so it is no oracle. Neither sentence reads as "does not exist".
A requested repository narrows the census, so a zero there gives neither sentence.

**Handles.** A linked item passes `VerifyHandle`, fresh and remembered. A work-item
handle option mints for a restricted caller only from evidence inside the grant: an
inside count of at least 1, or a candidate the node gate admitted. So no option
dead-ends. A remembered handle that fails its reverify is echoed as `carried`,
`vetoed_unresolved`, so it no longer vanishes in telemetry alone.

**Project-only.** Refused for every kind: the reserved value fails the grant check,
with no statement. It is offered no handle option, and a stored `handr_` receipt
still vetoes with the unresolved text.

**Providers.** Linear and Jira items are repo-less. GitHub and GitLab items carry a
repository. All four write native links through one admission table in ops, and the
rule reads link rows only, so `native_team_key` plays no part. Jira writes a native
link only under `fetch_dev_status`; without it every Jira item stays unresolved,
with the disclosure. A free-text handle binds `CHAOS-n` only, so another key reaches
the gate through search.

## Residuals

- **Timing.** The filter runs in the scan, so a statement reads the organization
  either way. Duration follows organization size, not a match outside the grant,
  and the statement sequence is identical. One matching outside row costs
  ClickHouse a row filter. That is not measurable as a proof, so the tests assert
  the sequence. A sub-millisecond difference stays a named residual.
- **A slug the grant cannot hold.** `NormalizeRepositorySlug` accepts owner/name
  only. A grant entry of another shape refuses that caller's census; it never
  widens it.
- **A stale native link.** Ops never removes a native row, so a link removed at the
  provider keeps granting until ops can retract it (CHAOS-12456 ruling 2).
- **The fact readers.** `workItemRepositoryAuthorization` and
  `RepoLessWorkItemAdmitter` still admit by project ownership. Nothing commits by
  that path (ruling 3).
