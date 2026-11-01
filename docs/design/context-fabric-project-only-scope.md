# Context Fabric: the project-only grant scope

Status: c1 (CHAOS-12297), c2 (CHAOS-12298), c3 (CHAOS-12299), c4 (CHAOS-12300),
the typed refusals (CHAOS-12330) and c5 (CHAOS-12301) are built. Nothing is
`project_only` in production until c5 is live and ops turns on
`ACR_ACCESS_PROJECT_SCOPE`. Design of record: CHAOS-12107 (v3 and its r3
addendum; Linear). Parent ticket: CHAOS-12228.

## Why

An organization whose Dev Health inventory holds projects but no repositories
cannot use Ask Dev today. Every ACR gate reads a caller's grant as a
repository list. An empty list means *unrestricted* (no repository check), so a
no-repository organization either gets every answer refused (`409
no_repository`, `internal/api/principal_access.go:146-219`) or, if it were let
through, would read the whole organization. The project-only scope admits such
a caller under an explicit set of projects instead. It never widens to
unrestricted or `*`, and it leaves every other caller exactly as it is.

## The grant class

`auth.GrantClass` (`internal/auth/grant_class.go`) is the one classification of
a grant:

| Class | Grant | `IsRestricted()` |
|---|---|---|
| `unrestricted` | no repository grant list | false |
| `universal` | the list holds `*` | false |
| `restricted` | specific repositories or owner wildcards | true |
| `project_only` | an explicit set of projects, no repository inventory | true |

`project_only` is a **restricted subtype**: a gate that does not decide it
explicitly takes the restricted path. `auth.ClassifyGrant` returns
`project_only` iff `storage.Principal.ProjectScopes` (canonical `project.v2`
identities) is non-empty, whatever the repository list holds; otherwise it
keeps today's rule. Only the access gate (c5, CHAOS-12301) sets
`ProjectScopes`; see [Admission](#admission).

The three gate vocabularies derive from the grant class with no `default`.
`project_only` maps to their restricted member, so no wire value changes:

- `directread.PrincipalClassForGrant` (`internal/contextfabric/directread/principal_class.go`);
- the operation policy's caller class reads the grant through it:
  `directread.CallerClassFor(PrincipalClassForGrant(...))`
  (`internal/contextfabric/directread/operation_policy.go`);
- `storedResultScopeForGrant` (`internal/contextfabric/stored_result_authorization.go`).

Those vocabularies are closed in the event specification (`internal/contextfabric/eventspec/spec.go`:
`principal_scope`, `principal_class`, `caller_class`), and the caller class
drives the operation-policy artifact (`contracts/mcp/operations.v1.json`).

## The decision gate

`internal/cigates/grantclass` loads every release build with full type
information (the `rowsclose` machinery, `internal/cigates/sourceload`). It finds
every site that reads a grant class:

- comparisons;
- every tagged switch over a class (an exhaustive one is a decision too),
  and separately one that misses a class or carries a `default`;
- a comparison or classifier result that leaves its function (a derived bool,
  directly or through a local variable);
- functions used as values;
- conversions of a class to any other type (`string`, an interface, a named
  type of its own such as `type Wrapped GrantClass`), or implicitly to an
  interface as an argument, composite element, map key (in a literal or an
  index), struct field, assignment, variable declaration, channel send or
  return;
- a typed constant of a class declared outside the class's own package: a
  new class value its own package's pin cannot see;
- collections of a class: a slice, array or map literal over a class, and a
  call argument of such a type (`slices.Contains`);
- lookups in a map keyed by a class;
- calls of the classifiers;
- calls of the raw repository-grant classifiers (`scopesUnrestricted`,
  `needsProjectReach`, `orgWideFigureAllowed`, `sourceHealthRestricted`,
  `workItemRepositoryRestricted`, `admitsWildcard`).

`decisions.tsv` also pins each class type's constants as `Name=value`. When a
class gains, loses or renames a constant, the test fails and lists every row of
that class to be decided again, comparisons as much as switches. Two release
targets that see different constants of one class fail the scan.

It scans every distinct (GOOS, GOARCH) pair of the release matrix, and a
decision inside a function literal counts for its enclosing declaration. It
does not see a class inside a generic function, where it is a type parameter,
inside a named struct transported whole, in a class-to-interface conversion
inside a multi-value (tuple) assignment or call, or through reflection or
unsafe.

Every site has a row in `internal/cigates/grantclass/decisions.tsv` that says
what `project_only` does there:

- `r-sub`: the restricted path is right;
- `proj`: project-aware logic is due, and the ticket is named;
- `deny`: refused;
- `leave`: a repository-only branch `project_only` never reaches;
- `consent`: the original consent grant;
- `vocab`: a mapping, vocabulary or telemetry.

An unlisted site, an undecided (`open`) row, a count that differs (a second
decision added inside a listed function), a row without a note and a stale row
all fail. `planted_test.go` proves each kind is found.

## Membership: the issue's own project link

A work item's current project membership is
`authorization_project_identities` on its graph node (CHAOS-12226).

- **Source.** It is read from the ops view `project_membership_presence` (the
  work item's own `project_id`, or its latest transition into a project) and
  resolved to exactly one `projects` row. It is never taken from repositories,
  issue-key prefixes or team ownership (the entity tree:
  `ops/docs/contribute/architecture/data-and-storage.md`).
- **Shape.** It is a list of canonical identities. An empty list means *in no
  project*; an absent value means *not stated*, and admits nothing. `"*"` never
  has wildcard meaning.
- **Turning it on is a roll step.** It is written only with
  `ACR_CONTEXT_FABRIC_PROJECT_WORK_ITEM_PROJECT_MEMBERSHIP_ENABLED` (default
  off). Turning it on needs an approved `acr-projector rebuild --org`, because
  the source's tables share one cursor; see `docs/operations.md`.

## One identity encoding

A project identity is `project.v2:<EncodeSegment(provider)>:<EncodeSegment(id)>`
(`internal/contextfabric/identity`, `Derive`).

- **Decode.** Ops sends `{provider, id}` pairs (ops CHAOS-12224). acr derives
  the canonical form once, at decode. An empty or invalid pair, an oversized
  identity or a duplicate rejects the read. A non-empty set never becomes the
  empty (unrestricted) grant.
- **Where it is used.** The canonical form is the only one used for
  authorization, in the principal, the node attribute, the fact selectors and
  the digests.

## Admission

c5, CHAOS-12301. This is the only change that admits `project_only`. The order
is unchanged: auth, access read, scope check, budget, admission. The grant comes
from the live read on every protected call and is never cached
(`internal/api/principal_access.go`).

- **The wire.** Ops sends `project_scope` only with `ACR_ACCESS_PROJECT_SCOPE`
  on and only for an organization with no repository inventory:
  `{"kind":"projects","projects":[{"provider":...,"id":...}]}`, at most 100
  pairs, or `{"kind":"none"}` with no `projects` key.
- **The decoder** (`internal/entitlements/principal_access.go`). It derives each
  identity once, sorts them, and keeps them as an inventory
  (`PrincipalAccess.Projects`), not a grant. It is all or nothing: an empty list,
  more than 100 pairs, an empty or blank provider or id, an oversized identity,
  a `|` (the membership projection can never state such a project) and a
  duplicate each reject the whole read. A pair is never dropped. Unknown fields
  stay refused. A rejected read is the plain 503, never a class change.
- **D2** (`auth.NarrowToProjectScopes`, called by `narrowLiveScope`):

  | Consent grant | Live repositories | Live projects | Result |
  |---|---|---|---|
  | `*` | none | some | `project_only` |
  | `*` | some | any | today's repository scope |
  | `*` | none | none, or no `project_scope` | 409 `no_repository` |
  | a repository, an owner wildcard, or no list | none | some | 409 `no_repository` |
  | a repository or an owner wildcard | some | any | today's repository scope |

  Widening an explicit repository grant to every project would go beyond what
  the user consented to. A credential with no user is never read, so it is never
  `project_only`.
- **The principal.** `ProjectScopes` holds the projects and `RepositoryScopes`
  holds only the reserved value, set after normalization, because
  `NormalizeRepositoryScopes` refuses its prefix. Nothing on the protected chain
  normalizes a principal's list again: the self-lifecycle and device routes do,
  and they read the stored grant, outside the access read.
- **Over the limit.** A 503 with `acr_access_project_scope_over_limit` or
  `acr_access_repository_scope_over_limit` is a typed limitation, not an outage:
  rejection reason `project_scope_over_limit` or `repository_scope_over_limit`,
  an Info line and an audit event with that reason, nothing admitted. The answer
  stays 503 `upstream_unavailable` (the error code vocabulary is closed) with
  `retryable: false`, because a retry cannot clear it. Every other 503, a body
  that does not decode included, stays reason `status`, retryable.
- **A `*` investigation assertion stays refused.** A web assertion may name
  every repository only on the two credential approvals, so `project_only` never
  reopens a `*` investigation.

## Graph admission

Under `project_only` (c3, CHAOS-12299):

- **Nodes** (`graphrank.AuthorizedAttributes`, `projectOnlyAdmits`). A node
  is admitted only when it is the caller's own organization node, a project
  node whose canonical id is in the grant, or a work item whose
  `authorization_project_identities` meets the grant exactly
  (`ProjectMembershipOf` is Member). Everything else is denied: a work item
  with no stated membership, a team (ownership reach is CHAOS-12323), a pull
  request (CHAOS-12308), a repository, a deployment, an incident, a document
  and an episode. The repository grant is never consulted. An attribute map
  with no subject kind (a synthetic repository or team decision) is denied, so
  the direct data tools refuse every repository-anchored and team row.
- **Requested narrowing.** A request that names projects is refused: nothing
  is admitted, and it is never answered over the whole grant. The intersection
  with the grant is CHAOS-12324 (after CHAOS-12229 maps raw ids). A requested
  repository or team narrowing still narrows a node.
- **E1: structural edges** (`graphrank.AuthorizedEdge`). An edge is admitted
  iff both endpoints pass the node rule, at every edge decision site. The
  edge's own repository and team lists are never consulted. A real edge
  project list must hold a granted canonical identity; the projection writes
  raw project ids there, so such an edge is denied until CHAOS-12229. `"*"` or
  an absent list defers to the endpoints. An unresolved optional endpoint does
  not pass (`sourcerow` `encodedTargets`). A link tier grants a project-only
  caller no authority in the tree walk.
- **E2: attribution edges.** `DOCUMENTED_BY` and `HAS_EPISODE` are denied.
- **Reach.** A project-only caller reads no project or ownership reach. It is
  restricted wherever a reader asks whether the caller sees only some
  repositories (`needsProjectReach`), and fails closed there.
- **The shared predicate.** It is `graphrank.ProjectMembershipOf`, and the
  request-side narrowing (CHAOS-12229) uses the same one.

## Fail closed before c2 (c3 r1)

Until c2 (CHAOS-12298) lands, c3 refuses a `project_only` principal every
channel that reads its repository list or hashes only that list:

- **Repositories.** `auth.AuthorizeRepository` refuses it every repository,
  whatever its list holds, so every route and evidence read that authorizes a
  repository through it refuses it. The storage episode checks refuse it too
  (`storage.Principal.HoldsProjectGrant`, the rule `auth.ClassifyGrant`
  reads).
- **Work items.** `auth.WorkItemGrantSelectorSet` gives it the zero selector
  set, which the reader denies, never the organization-wide one. Every
  work-item read (status, work, actual completion, blockers, required
  children, commitment, the tree filter, membership, the scope expander, the
  repository-less admission, evidence and source rows) then reads nothing for
  it, so a work-item subject's facts are withheld.
- **Stored results and reuse.** c3 refused it every stored result
  (`grant_mismatch`), made no reuse lookup and stored none of its results as
  reusable, because the grant digest and the reuse key hashed only the
  repository list. c2 lifts all three: its digest and key are now class-tagged
  and built from its project set (see "Digests and reuse").
- **Never unscoped (r3).** Every reader of "the caller sees every
  repository" decides on the principal (`auth.ClassifyGrant`), never on the
  raw list: the resolve census visibility, the organization figures
  (`sourceHealthRestricted`, `orgWideFigureAllowed`), the work-item tuple
  (`workItemRepositoryRestricted`), the graph reach (`needsProjectReach`) and
  the link read, which binds no repository grant for it. A stored work-item
  tuple, a context packet, an episode write or purge and the evidence
  repository listing are refused to it too.
- **Counts.** `withheld.edges_not_visible` and `page.examined` are unchanged:
  they are the disclosure contract, the same for a restricted caller.

## Digests and reuse

c2, CHAOS-12298. Every other grant class keeps its digests and its reuse key
byte for byte.

- **Sentinel.** A `project_only` principal's repository list holds one reserved
  value, `contractsv1.ProjectOnlyRepositoryScope` (`acr-reserved:project-only`;
  the prefix is `contractsv1.ReservedRepositoryScopePrefix`, compared trimmed
  and case-insensitive), so any site that still reads the raw list sees a
  restricted list that matches nothing. The access-read decoder (c5) sets it.
  - Refused where repository names enter acr: credential scopes
    (`auth.NormalizeRepositoryScopes`, by name, before the slug grammar), the
    projection port (`ContextFabricAuthorizationScope.Validate`, every entity,
    relationship and scope it guards) and the source slug producer
    (`devhealthsource.repoAuthorization` writes the unresolved sentinel).
  - Denied in the raw-equality matchers: `graphrank.ScopeMatch` never matches a
    reserved value (its raw fallback would otherwise match a node holding the
    same value) and `auth.RepositoryAllowed` skips one.
  - No digest or reuse key hashes it: the project-only forms below are built
    from the project set alone.
- **The project grant digest.** `projectGrantDigest`
  (`internal/contextfabric/reuse_grant_scope.go`): the canonical `project.v2`
  identities, compared exactly (never trimmed or case-folded), de-duplicated,
  sorted and framed (a domain tag, then each entry length-prefixed), SHA-256,
  128 bits.
- **Stored-result grant digest.** `p:` plus those 32 hex
  (`StoredResultGrantDigest`; migration 0066, CHAOS-12227, admits the form). A
  project-only reader is a restricted reader of the gate: it is served only a
  result whose digest equals its own, never `open`, `g:` or another `p:`; a
  restricted reader's `g:` digest never equals a `p:` one.
- **Reuse key.** Class-tagged: `+p:` plus the project grant digest for
  `project_only`, `+g:` for a restricted grant, the bare key for unrestricted
  and universal (`GrantScopedTimeAxisKey`, the lookup and the only Save both).
  Two different project grants never share a key.
- **Work-item census digest.** It keeps its 64-hex envelope. For
  `project_only` the document is class-tagged: `grant_class` plus the sorted
  project set, and an empty repository grant list
  (`WorkItemAuthorizationDigest`). The two fields are omitted for every other
  class, so their documents are unchanged.
- **R2-4.** An unrestricted or universal reader may read a `project_only`
  result: the stored-result gate checks no digest for them, as before. No new
  unrestricted or universal investigation reuses one, because its reuse key
  never equals a `+p:` key, and the served result keeps its `project_only`
  limitations and coverage exactly as stored.
- **R3-3.** A census-backed work-item tuple computed under one grant class is
  not-found to a reader of another, because the census digest is recomputed
  for the reader (`internal/contextfabric/work_item_stored_serving.go`).
- **Postgres.** The `p:` replay (identical, changed, repository, universal and
  unrestricted grants) is the shared parity suite
  `paritytest.RunProjectGrantDigestReplaySuite`, run by both adapters (the
  Postgres one with `ACR_CONTAINER_TESTS=1`).

## Facts and refusals

CHAOS-12330. A project-only caller is served no fact: no provider could be
proven to serve it only what its grant covers. So the fact registry refuses
every planned read, once, before any provider or scope expander runs
(`internal/contextfabric/fact_project_scope_withheld.go`), and the refusal is a
typed answer, not an empty read. An empty read is a false statement about the
source ("the source was reached and held no rows"), and it does not degrade.

Every refusal is the one coverage code `project_scope_withheld`: degrading,
with `source_state: unauthorized`, and exactly one of `fact_kind` and `kind`.
That pair fixes the reason, so the wire gains no field and no reason token, and
the server composes the label from it
(`internal/contracts/v1/context_fabric_display_labels.go`). No model text
carries a reason.

| Refused | Field | Label states |
|---|---|---|
| health, flow, landscape, metrics, readiness, workload, operational deficiencies | `fact_kind` | built from owning teams, which a project grant does not cover |
| investment | `fact_kind` | placed by repository or spanning the organization |
| source health | `fact_kind` | an organization-wide figure |
| pull requests, reviews | `fact_kind` | pull requests are not in a project grant yet |
| identity, status, work, actual completion, blockers, required children, commitment | `fact_kind` | exact project membership is not proven yet |
| membership, continuous integration, deployments, incidents, security alerts, file hotspots, evidence | `fact_kind` | they belong to repositories |
| a request that names projects | `kind: project` | requested project narrowing not available yet |
| teams, pull requests, repositories, when the plan asks for the kind | `kind` | not in a project grant |

- **Facts.** The registry files the detail beside the source observation, and
  a pruned or unconfigured kind keeps its own state: nothing is refused there.
- **The floor keeps the narrative.** A complete answer is floored to partial
  with its own limitation, and its narrative stays
  (`applyProjectScopeWithheldStatusFloor`, the status-only
  `floorKeepingNarrative`; CHAOS-12301, ruling (b)). Every fact kind is withheld
  from this caller, so withholding the narrative too would leave it almost no
  written answer, even a correct graph-only one. No withheld value can be in the
  narrative: the registry refuses the read before any provider runs, so synthesis
  is never given one, and a claim with no fact behind it meets the same check as
  any other answer. The head is the server's and states partial.
- **Other floors still withhold.** This floor runs after every floor that
  withholds the narrative (`ApplyServedStatusFloors`), so it never pre-empts one.
  A denied cohort, an unverified project narrowing, a status filter over a cut
  read and an organization survey each withhold the narrative for their own
  reason, on a project-only answer too. Those answers do not carry the floor's
  sentence; their `project_scope_withheld` details still name what was refused.
- **read_facts.** Each row reads outcome `withheld_project_scope` with
  `kind_state: unauthorized`, and the read is partial.
- **A request that names projects (ruling B).** The engine stops before every
  read, the narrowing resolver's included, and serves a typed `no_match`
  (`internal/contextfabric/project_scope_refusal.go`). Nothing is answered over
  the whole grant. CHAOS-12324 narrows once CHAOS-12229 maps raw ids.
- **Asked kinds.** A detail fires only when the plan asks for the kind, read
  from its requirement rows because a refused kind never has a cohort. It
  states the grant class, never that a subject of the kind exists. It replaces
  a `graph_no_member_found` row of the kind: the search could not see that
  kind, so "none found" is a claim it never measured.
- **Unchanged.** `denied_or_not_found`, the cohort denial, `repo_forbidden` and
  the stored-result refusal hide existence on purpose.

The provider-level guards of c3 are gone: nothing reaches a provider for this
caller, so they could not be reached and a test could not show otherwise. The
one provider read outside the registry (`action_findings.go`) reads only team
subjects, which the graph denies this caller.

## Observability

c4, CHAOS-12300. At Info, corpus-safe: counts, closed vocabularies and
classes only, never an id, a label or a digest value. The decision
vocabularies (`principal_class`, `principal_scope`, `caller_class`) keep
project_only as a restricted subtype, so the gates' decisions do not move; a
separate `grant_class` field (`auth.GrantClassVocabulary`: `unrestricted`,
`universal`, `restricted`, `project_only`) tells it apart.

- **Admission basis** (`internal/api/principal_access.go`
  `logAccessGranted`). For a project-only principal, `principal access read
  granted` is written at Info with `grant_class`, `project_scope_count` and
  `grant_digest_class`. Every other class keeps its Debug line unchanged.
- **Grant class on every authorization line.** `grant_class` and
  `project_scope_count` are required fields of
  `contextfabric.direct_read_authorization`,
  `contextfabric.direct_relationships_read` and
  `contextfabric.stored_result_authorization` (eventspec).
- **E1/E2 decision basis.** `graphrank.AuthorizedEdgeBasis` is the one
  decision; `AuthorizedEdge` is "the basis is `admitted`". Its bases:
  `admitted`, `attributes` (any other class), `project_narrowing_refused`,
  `attribution_relation` (E2) and `project_list` (E1: a real project list with
  no granted identity).
  - The direct relationships line counts `edges_withheld_attribution_relation`
    and `edges_withheld_project_list` apart from `edges_withheld_attributes`,
    `edges_withheld_source` and `edges_withheld_target` (E1 endpoints). The
    public `edges_not_visible` total is unchanged.
  - The walk's `context_fabric: relationship edges filtered` line, for a
    project-only caller only, adds `grant_class`, `authz_endpoint`,
    `authz_attribution_relation`, `authz_project_list`,
    `authz_project_narrowing` and `authz_unclassified`. The generic walk and
    the tree walks both classify each refusal, and a denial that only a tree
    walk reported still takes this line. `authz_unclassified` holds what no
    site can name (a fused link read that returned no feed edge), so the parts
    add up to `authz`. Every other class keeps its three counts.
- **Grant-equality result.** Every read of a stored result (not the
  fact-root recheck) adds `grant_digest_class` (the reader's),
  `stored_grant_digest_class` (the result's: `open`, `repository`, `project`,
  `unrecorded`, `malformed`) and `grant_digest_equal`, on an admitted and on a
  denied read alike. `repository` and `project` need exactly 32 lowercase hex
  after the prefix, the shape migration 0066 admits; any other value is
  `malformed`. A different project set
  of the same size and class reads as `grant_digest_equal: false`; the R2-4
  read of a `p:` result by an unrestricted reader reads as admitted and
  unequal.
- **Membership outcomes** (`devhealthsource`, the #1821 review F3). Each
  membership read writes `context fabric work item project membership stated`
  with `table` (`work_items` or `work_items_project_membership`),
  `work_items`, `member`, `missing` (no membership row), `stale` (rows that
  resolve to no single statable project) and `cleared` (the refresh table
  found no row after a membership change: the last project was left). An empty
  set is never one "absent" count. A refresh pass that re-read no work item
  writes its line too, with zeros, so a normal empty refresh is visible; the
  line is one per pass, never one per work item.
- **Node basis.** For project_only the subject kind is the node rule: an
  admitted organization is the caller's own, an admitted project is granted,
  an admitted work item is a member. The gate line's `admitted_count`,
  `denied_count` and `refused_kinds` carry it. It does not tell an unstated
  work item from a stated non-member: the graph authorizer returns an outcome,
  not the attributes.

## The split and the roll order

| Step | Ticket | What it does | Behavior change |
|---|---|---|---|
| c1 | CHAOS-12297 | the grant class, the decision gate, this document | none |
| c2 | CHAOS-12298 | the sentinel, digests and reuse keyed by class | none until c5 |
| c3 | CHAOS-12299 | graph and fact admission, the shared membership predicate | none until c5 |
| c4 | CHAOS-12300 | Info observability | logging only |
| typed refusals | CHAOS-12330 | one coverage code, minted by the registry; rides a pin train | none until c5 |
| c5 | CHAOS-12301 | the access-read decoder: the ONLY change that admits `project_only` | admits `project_only` |

The roll order is fixed: acr with c5 live in production BEFORE ops turns on
`ACR_ACCESS_PROJECT_SCOPE`. The decoder refuses unknown fields today
(`internal/entitlements/principal_access.go`), so the reverse order fails
closed.

The ops access read answers an inventory above 100 with a 503 and a code; see
[Admission](#admission).
