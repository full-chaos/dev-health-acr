# Context Fabric: the project-only grant scope

Status: c1 (CHAOS-12297) is built; c2 through c5 are planned. Design of record:
CHAOS-12107 (v3 and its r3 addendum; Linear). Parent ticket: CHAOS-12228.

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
explicitly takes the restricted path. `auth.ClassifyGrant` keeps today's rule
and never returns `project_only`. Only the access-read decoder (c5,
CHAOS-12301) produces it.

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
- tagged switches that miss a class or carry a `default`;
- a comparison or classifier result that leaves its function (a derived bool,
  directly or through a local variable);
- functions used as values;
- conversions to `string` or an interface, explicit or as an argument,
  composite element, assignment or return;
- lookups in a map keyed by a class;
- calls of the classifiers;
- calls of the raw repository-grant classifiers (`scopesUnrestricted`,
  `needsProjectReach`, `orgWideFigureAllowed`, `sourceHealthRestricted`,
  `workItemRepositoryRestricted`, `admitsWildcard`).

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

## Graph admission

Under `project_only` (c3, CHAOS-12299):

- **Nodes.** A node is admitted when it is the caller's organization node, a
  granted project, a work item whose `authorization_project_identities` meets
  the grant exactly, or a team reached through `OWNED_BY_TEAM` from a granted
  project. Absent membership denies.
- **E1: structural edges.** An edge is admitted iff both endpoints pass the node
  rule, at every edge decision site. The edge's own repository list is never
  consulted. A real edge project list must intersect the grant; `"*"` or an
  absent list defers to the endpoints. An unresolved optional endpoint does not
  pass.
- **E2: attribution edges.** `DOCUMENTED_BY` and `HAS_EPISODE` are denied, as a
  typed limitation.
- **The shared predicate.** It is `graphrank.ProjectMembershipOf`, and the
  request-side narrowing (CHAOS-12229) uses the same one.

## Digests and reuse

c2, CHAOS-12298:

- **Sentinel.** A `project_only` principal's repository list holds one reserved
  value with the prefix `acr-reserved:`, so any site that still reads the raw
  list fails closed. The prefix is refused where repository names enter acr and
  is denied in the raw-equality matchers. No digest, reuse key or proof hashes
  it as a repository.
- **Stored-result grant digest.** It is `p:` plus 32 hex, a framed digest of
  the canonical project set (migration 0066, CHAOS-12227).
- **Work-item census digest.** It keeps its 64-hex envelope over a class-tagged
  grant plus the requested-scope inputs. The reuse key is class-tagged.
- **R2-4.** An unrestricted or universal reader may read a `project_only`
  result. No new unrestricted investigation reuses one, and the served result
  keeps its `project_only` limitations and coverage as stored.
- **R3-3.** A census-backed work-item tuple read by an unrestricted reader is
  not-found, because the census digest is recomputed for the reader
  (`internal/contextfabric/work_item_stored_serving.go`).

## Facts and aggregates

- **Served:** intrinsic project facts, and the facts of member work items.
- **Typed limitation, with a coverage reason:** facts that rest on a team or on
  a cross-project population (F5).
- **Org-wide figures:** a typed limitation (D3; CHAOS-12225 widens it later).

## Observability

c4, CHAOS-12300. At Info, corpus-safe (never ids):

- the admission basis: class, project count and digest class;
- the E1/E2 decision basis;
- a grant-equality result;
- membership outcomes `missing`, `stale` and `cleared`, never folded into
  "absent".

## The split and the roll order

| Step | Ticket | What it does | Behavior change |
|---|---|---|---|
| c1 | CHAOS-12297 | the grant class, the decision gate, this document | none |
| c2 | CHAOS-12298 | the sentinel, digests and reuse keyed by class | none until c5 |
| c3 | CHAOS-12299 | graph and fact admission, the shared membership predicate | none until c5 |
| c4 | CHAOS-12300 | Info observability | logging only |
| c5 | CHAOS-12301 | the access-read decoder: the ONLY change that admits `project_only` | admits `project_only` |

The roll order is fixed: acr with c5 live in production BEFORE ops turns on
`ACR_ACCESS_PROJECT_SCOPE`. The decoder refuses unknown fields today
(`internal/entitlements/principal_access.go`), so the reverse order fails
closed.

The ops access read answers an inventory above 100 with a 503 and a code:
`acr_access_project_scope_over_limit` or
`acr_access_repository_scope_over_limit`. c5 maps each code to a typed
limitation of access; every other 503 stays a plain unavailable.
