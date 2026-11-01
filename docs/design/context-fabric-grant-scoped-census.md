# Grant-scoped evidence census

CHAOS-12452 (PR1), CHAOS-12456 (PR2) and CHAOS-12467 (the listing). Plans and
rulings: the tickets. This records what shipped.

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

CHAOS-12467 ruling 1 extends this to every caller but project-only: an unrestricted
caller with one bound handle and nothing committed runs the round, whatever the
search says. Before, an empty search gave that caller "Retrieval found no candidate"
with no census asked. **This changes what an unrestricted caller gets: a lone
satisfier now commits where it gave `no_match`.** The line under Gates stands: for an
unrestricted principal, a satisfier outside the request must not decide
(`TestARequestedRepositoryStillDecidesForAnUnrestrictedCaller`). Two handles stay a
truncated search's question for that caller.

## When a census counts more than it commits

A census count must never read as "found nothing". `listCensusSatisfiers`
(`graphrank/census_satisfier_listing.go`) runs after the commit decision, for the kind
the question's one handle was applied to, when nothing committed. It lists what the
census proved as ambiguous candidates, so it cannot commit. An anchor-only census
counts a population and is never listed.

| Census | Result | Next turn |
| --- | --- | --- |
| 1, committed | unchanged | |
| 1, not committed (a rival hypothesis) | listed | a receipt, or `repos=` |
| 1, the graph or the gate refused it | `no_match`, the count sentence (a lone work item for a restricted caller: its own sentence) | `repos=`, or retry |
| 2 to K | `clarification_required`, all listed | a receipt, or `repos=` |
| K+1 to 999 | `clarification_required`, K listed, the partial sentence | a receipt, or `repos=` |
| Over 999: no ids held | `no_match`, the count sentence | `repos=` |
| A set that did not close; every id refused | `no_match`, the count sentence | `repos=` |
| `narrowed_to_many` | the kept ids only | a receipt |
| `narrowed_to_one` | commits, unchanged | |
| `narrowed_to_none`, team | all listed: with no in-team offer, today's offers stand (Anchor B) | a receipt |
| `narrowed_to_none`, repository | unchanged: the narrowing's own sentence | widen |
| `census_truncated`, `satisfier_read_failed` | `no_match`, the count sentence: a narrowing the caller asked for is never dropped in silence | `repos=` |
| `census_error`, deadline, historical axis, no census | unchanged: nothing was counted | |
| Clarification not allowed | `no_match`, candidates attached, the existing sentence | a receipt |

- **K** is `min(MaxSubjectCandidates, 25)`, the bound on a narrowing's keyed reads.
  Below 2 nothing is listed, and the count is stated. Reads are bounded by K too, on
  one deadline. Over the set budget (999) no statement is added.
- **Order** is sorted canonical id. Satisfiers go ahead of the pool, so a noisy pool
  cannot fill the budget.
- **The sentences** are `ContextFabricCensusUnlistedLimitation` and
  `ContextFabricCensusListedLimitation`. One text for every cause, a count and never
  a name. With nothing listed the sentence is checked ahead of the floor and the
  exclusion prompt: both would be false in that state, and a clarification with
  nothing to redeem is refused (CHAOS-5637).
- **A handle two subjects satisfy is not offered.** A redeemed handle never reaches a
  commit, so the option would loop. Candidates carry receipts instead.
- **Labels are the server's.** A label is the handle, the caller's granted slug or the
  tracker, and the title unless the title is a raw id (CHAOS-12468). `matched_terms`
  holds the handle as the question wrote it, never the census marker. The slug comes
  from `auth.RepositoryAllowed`, never from its position in the node's list: a node can
  list an old slug. A pool-built candidate keeps its label, so the prompt's id suffix
  cue can still reach one (CHAOS-12468).
- **Side channels.** The ids are inside the grant, each passes the node gate, and the
  trace carries counts only (`evidence_census_listing`). Outside rows change no
  candidate, label, offer, sentence, trace, statement or graph read
  (`TestAListingChangesNothingOutsideTheGrant`). Project-only never reaches the census,
  so it gets none of this.

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

`contextfabric.work_item_link_reach` (Info, `context_fabric: work item link reach`)
is one line per adapter call of a restricted caller that read the graph, adding up
its keyed reads: `outcome` (`read`, `failed`), `reads`, `ids`, `rows`, `failed`,
total `elapsed_ms`, and `request_id` when the context has one. Counts and a closed
value only, never an id or a slug. A call with a failed read also writes a Warn line.
This is where the live run reads the cost of the reach.

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
- An owner grant admits what the gate admits. The library's owner match reads the
  first path part only, but its link aggregate tests the two-part slug grammar
  first, so `acme/*` does not reach `acme/group/secret` or `acme/trailing-`. Real
  ClickHouse pins it (`TestAWorkItemOwnerGrantAdmitsWhatTheGateAdmitsOnClickHouse`),
  and a unit test pins the grammar in the statement, for a library bump.
- A census with no handle stays refused: a count inside the link arm would miss the
  GitHub and GitLab items the node gate admits.
- A requested repository stays on `linkScoped` and `withinLinkScope`: the library's
  requested selector needs a real repository, so it would deny every repo-less row.

**The node gate.** A repo-less node carries only the no-repository sentinel, so the
gate cannot decide from it. `falkorgraph/link_reach.go` follows each wrapped read
with one keyed read: for the repo-less work items that page returned, the native
`LINKS_PULL_REQUEST` edges to pull requests of the grant. It adds
`authorization_link_repositories` in memory to each item a link proves.
`AuthorizedAttributes` tests it only when the node's list is the sentinel alone.

- Nothing is stored, so no rebuild. A site that is not wrapped denies.
- The keyed read runs once for every wrapped read, an empty page included, and its
  text is one constant. So a subject outside the grant changes no statement and no
  count of statements. It changes the id list only. Reading only when a page held
  an id would make the read itself the signal.
- The proof lives for one read, so a read proves its own page and no other. There
  is no cap and no cut: the id list is what the page held.
- Cost. A grant-wide reach, read once per call, took 45 to 63 ms (median 53) for
  8,482 rows on askdev-1. That misses the 50 ms gate, and a cap on it would deny. The
  keyed read took 0.11 to 1.28 ms there, with an empty list and with 50 ids. A real
  engine turn makes 27 wrapped reads on a repository question, 19 on a work item and
  16 on a team (`TestLiveWrappedReadsOfATurn`, up to synthesis). So a repository
  turn costs about 35 ms at the top of that range, by arithmetic.
- A link to a pull request outside the grant, beside one inside, admits the item.
  The outside one is not read. A merged pull request or a closed issue still counts:
  validity in time is not an authorization input.
- A work item with a repository of its own is decided by that repository. A link
  never widens it. Unrestricted, universal and project-only callers read no reach.

| Failure | Subject | Caller reads |
| --- | --- | --- |
| Keyed read fails in a turn, in resolution or discovery | stays denied, the turn goes on, an answer is marked partial | `ContextFabricWorkItemLinksUnreadLimitation`, on a terminal or an answer, whatever committed |
| Keyed read fails at the stored gate or the recheck | `ErrUnavailable` | 503, never `no_match` |
| Census error, deadline or row bound | `census_error` | the same sentence |
| Census names an item, node gate refuses (projection lag) | `refused` | the same sentence |

**Disclosure.** A census that completed with no linked item gives
`ContextFabricWorkItemNoGrantedLinkLimitation`. It is one text for no such item, an
item outside the grant, an inside item with no link, and a lower-tier link only: it
is true of all four, so it is no oracle. Neither sentence reads as "does not exist".
A requested repository narrows the census, so a zero there gives neither sentence.

**Handles.** A linked item passes `VerifyHandle`, fresh and remembered. A work-item
handle option mints for a restricted caller only from evidence inside the grant: an
inside count of exactly 1, or a candidate the node gate admitted. So no option
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
- **Timing of the keyed read.** A hidden repo-less item on a page adds one id to the
  list, so the read's duration can follow the id count. The statement and the count
  of statements do not change. The difference stays inside the 0.11 to 1.28 ms range
  above, across an empty list and 50 ids. The 50 ms gate stands and the live run
  measures it.
- **A slug the grant cannot hold.** `NormalizeRepositorySlug` accepts owner/name
  only. A grant entry of another shape refuses that caller's census; it never
  widens it.
- **A stale native link.** Ops never removes a native row, so a link removed at the
  provider keeps granting until ops can retract it (CHAOS-12456 ruling 2).
- **A name that starts with a dot.** The library's grammar refuses `acme/.github`
  and the gate admits it. So an item linked only there counts 0: the census misses
  it inside the grant, and search can still reach it. No signal leaves the grant.
- **The fact readers.** `workItemRepositoryAuthorization` and
  `RepoLessWorkItemAdmitter` still admit by project ownership. Nothing commits by
  that path (ruling 3).
