package devhealthsource

// The per-organization project -> team ownership group statement, kept
// test-only as the answer oracle of the stored-group reader. It is the text
// the producer ran before it read the stored groups (ops migration 147 stores
// the same groups); the producer no longer runs it, and nothing outside the
// tests calls it. TestOwnershipProducerStatementsAreByteIdenticalToTheGoldens
// pins its renders to the goldens captured from the production code.

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-go/readers"
)

// projectTeamsRowKey is the pagination tiebreaker for queryProjectTeams,
// shared by the keyset predicate and the ORDER BY so the two cannot drift.
//
// CHAOS-4635: escaped, and `provider` restored -- it is part of the GROUP BY,
// so leaving it out let two distinct groups share a key.
var projectTeamsRowKey = rowKeySQL("o.provider", "o.project_id", "o.team_id", "o.source_name")

// projectTeamsWatermark is queryProjectTeams' pagination timestamp, shared
// by the SELECT list, the keyset HAVING and the ORDER BY so the three
// cannot drift -- the same one-definition discipline projectTeamsRowKey
// carries for the tiebreaker.
//
// It aggregates row_watermark, not the raw ownership updated_at
// (CHAOS-4565): this producer reads team_project_ownership AND projects,
// and a cursor covering only one of its inputs cannot see a change in the
// other. See the RETRACTION ARM note in projectTeamsStatement.
const projectTeamsWatermark = "max(o.row_watermark)"

// projectTeamsIngestWatermark is projectTeamsWatermark on the ingest stamps
// (ownership last_synced folded with its projects' last_synced): the cursor
// position once the ingest columns exist. ObservedAt keeps projectTeamsWatermark.
const projectTeamsIngestWatermark = "max(o.row_ingest)"

// projectIdentityWithWatermarkSQL is readers.ProjectIdentityCatalogSQL
// carrying each project's own updated_at (CHAOS-4565).
//
// The catalog expansion answers "which identity values does this project
// answer to". It deliberately does not carry a timestamp, because its other
// callers resolve a requested subject list and have no cursor. This
// producer does have one, and its watermark has to cover BOTH tables it
// reads -- see the RETRACTION ARM note in projectTeamsStatement for why a
// projects-only edit that never touches team_project_ownership must still
// move this producer's cursor.
//
// The library's expansion is wrapped in `SELECT *` before being joined
// rather than joined directly: its SQL ends `) AS p`, and this function's
// own result is also aliased `p` (ProjectIdentityMatchSQL hard-codes
// `= p.scope`, so the outer alias is not free to change). Enclosing the
// library's alias inside a subquery keeps the two `p`s in separate scopes
// instead of relying on shadowing resolution -- this file has already lost
// a live round to an alias that bound to itself on ClickHouse 24.8.
// `SELECT *` also means a future column added to the expansion arrives
// here on its own.
//
// `projects FINAL` is ReplacingMergeTree(updated_at) ordered by
// (org_id, provider, id), so this join is 1:1 and cannot fan the catalog
// out; the ON is two plain column equalities, which 24.8 accepts.
//
// projectTeamsOwnershipRowsSQL is the team_project_ownership row source every
// arm resolves against, collapsed ONCE: one row per (provider, project_ref,
// project_key, team_id, source_name), carrying BOTH identity columns
// regardless of which one an arm matches through, so the columns cannot
// drift between what an arm matches on and what it reports as
// ownership_ref/ownership_key. The history is folded here, before the arms
// and the window layer, because every version of every pair would otherwise
// sit in the arms' memory: first_valid_from is the earliest version's start,
// valid_from/valid_to are the latest assertion by ownershipFactOrder (the
// order the outer statement used to apply), updated_at and ingest_at are the
// newest across the group (the pagination watermark folds them in again).
// gate is an extra WHERE conjunct inside the FINAL read; the retraction arm
// uses it to read nothing when the organization has no ambiguous key.
func projectTeamsOwnershipRowsSQL(ingest bool, gate string) string {
	return `(
		SELECT provider, project_ref, project_key, team_id, source_name,
		       min(vf) AS first_valid_from,
		       argMax(tuple(vf, vt), ord) AS latest_assertion,
		       latest_assertion.1 AS valid_from, latest_assertion.2 AS valid_to,
		       max(upd) AS updated_at` + ingestStampSQL(ingest, "max(ing) AS ingest_at") + `
		FROM (
			SELECT provider, ` + readers.ProjectOwnershipJoinColumn + ` AS project_ref, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name,
			       valid_from AS vf, valid_to AS vt, updated_at AS upd` + ingestStampSQL(ingest, "last_synced AS ing") + `,
			       ` + strings.NewReplacer("o.valid_to", "valid_to", "o.valid_from", "valid_from").Replace(ownershipFactOrder) + ` AS ord
			FROM team_project_ownership FINAL
			WHERE org_id = {org_id:String}` + gate + `
		)
		GROUP BY provider, project_ref, project_key, team_id, source_name
	)`
}

// projectTeamsAssertingArm is the OLD arms A and B in one arm (CHAOS-4750):
// the collapsed ownership rows (projectTeamsOwnershipRowsSQL, read once) are
// fanned into their two match variants with ARRAY JOIN, so the table is read
// once rather than once per variant and one shared project-identity scan
// serves both.
//
//   - the project_ref variant tags required_scope_kind = ” -- the SCOPE arm (old
//     arm A). Matches a scope row of EITHER kind on purpose: project_ref is
//     not an id column, it is whichever id space that row uses, and must
//     never be given a scope_kind restriction (CHAOS-4521b, CHAOS-4542).
//   - the project_key variant tags required_scope_kind = 'key' -- the KEY arm (old
//     arm B). Matches the key SCOPE ROW, never p.project_key (a column
//     EVERY scope row carries -- CHAOS-4542 defect 6): the restriction sits
//     in the WHERE below, not the ON, because 24.8 rejects an ON that is not
//     a plain equality and 'key' is a literal.
//
// Both branches join through readers.ProjectIdentityMatchSQL against
// p.scope -- the shared helper's target column, not a hand-rolled copy of
// it -- differentiated only by which raw column feeds scope_value and by the
// required_scope_kind gate. Neither branch ever names p.project_key, which
// is what makes defect 6 structurally impossible here rather than merely
// untested.
func projectTeamsAssertingArm(resolved string, ingest bool) string {
	ownership := `(
		SELECT provider, project_ref, project_key, team_id, source_name, first_valid_from, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "ingest_at") + `,
		       scope_match.1 AS scope_value, scope_match.2 AS required_scope_kind
		FROM ` + projectTeamsOwnershipRowsSQL(ingest, "") + `
		ARRAY JOIN [(project_ref, ''), (project_key, 'key')] AS scope_match
	) AS o`
	return `
		SELECT p.id AS project_id, p.provider AS provider,
		       o.project_ref AS ownership_ref, o.project_key AS ownership_key,
		       o.team_id AS team_id, o.source_name AS source_name, o.first_valid_from AS first_valid_from, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at` + ingestStampSQL(ingest, "o.ingest_at AS ingest_at") + `,
		       p.project_updated_at AS project_updated_at, p.provider_updated_at AS provider_updated_at` + ingestStampSQL(ingest, "p.project_ingest_at AS project_ingest_at, p.provider_ingest_at AS provider_ingest_at") + `, toUInt8(0) AS retraction_only
		FROM ` + resolved + `
		INNER JOIN ` + ownership + ` ON o.provider = p.provider AND ` + readers.ProjectIdentityMatchSQL("o", "scope_value") + `
		INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = o.team_id
		WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind`
}

// projectTeamsRetractionArm is the OLD arms C and D in one arm, over the
// AMBIGUOUS project source instead of the resolved one. Each collapsed
// ownership row picks ONE match value with if(): its own project_key when it
// is non-empty (old arm C, CHAOS-4566), else its project_ref (old arm D,
// CHAOS-4566 R4 P2). The two choices are mutually exclusive on the row's own
// project_key, so a row cannot match through both. The ownership read is
// gated on the ambiguous source having at least one ambiguous key: with none
// the arm cannot return a row and reads no ownership history.
func projectTeamsRetractionArm(ambiguous string, ingest bool) string {
	ownership := `(
		SELECT provider, project_ref, project_key, team_id, source_name, first_valid_from, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "ingest_at") + `,
		       if(project_key != '', project_key, project_ref) AS match_value
		FROM ` + projectTeamsOwnershipRowsSQL(ingest, " AND (SELECT count() FROM "+ambiguous+" WHERE p.key_project_count > 1) > 0") + `
	) AS o`
	return `
		SELECT p.id AS project_id, p.provider AS provider,
		       o.project_ref AS ownership_ref, o.project_key AS ownership_key,
		       o.team_id AS team_id, o.source_name AS source_name, o.first_valid_from AS first_valid_from, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at` + ingestStampSQL(ingest, "o.ingest_at AS ingest_at") + `,
		       p.project_updated_at AS project_updated_at, p.provider_updated_at AS provider_updated_at` + ingestStampSQL(ingest, "p.project_ingest_at AS project_ingest_at, p.provider_ingest_at AS provider_ingest_at") + `, toUInt8(1) AS retraction_only
		FROM ` + ambiguous + `
		INNER JOIN ` + ownership + ` ON o.provider = p.provider AND o.match_value = p.project_key
		INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = o.team_id
		WHERE p.key_project_count > 1`
}

// projectTeamsArms is the ordered arm list projectTeamsStatement unions, and
// the seam a test reads to check which arms may assert.
//
// TWO arms, not four (CHAOS-4750). The four CHAOS-4566 arms unioned four
// separately-embedded copies of the project-identity expansion -- 12
// physical `projects FINAL` scans per read, measured against real
// ClickHouse before this change. Every arm's OUTPUT is unchanged: this
// function still produces the same four match combinations (scope/either
// kind, key/key-scope-only, ambiguous-via-own-key, ambiguous-via-ref), just
// grouped into two arms that each embed their shared project source ONCE.
// See projectTeamsAssertingArm and projectTeamsRetractionArm for what each
// pair still means.
func projectTeamsArms() []string { return projectTeamsArmsFor(false) }

// projectTeamsArmsFor is projectTeamsArms with the ownership and project
// stamps read from their ingest columns (ingest) or their provider times.
func projectTeamsArmsFor(ingest bool) []string {
	resolved := projectIdentityWithWatermarkSQL(ingest)
	ambiguous := ambiguousProjectIdentitySQL(ingest)
	return []string{
		projectTeamsAssertingArm(resolved, ingest),
		projectTeamsRetractionArm(ambiguous, ingest),
	}
}

// ingestStampSQL is ", <column>" when the ingest columns are in use, else
// nothing: one more selected column carrying a row's server insert time. A
// column that already starts with ", " or ends with "," is not re-delimited.
func ingestStampSQL(ingest bool, column string) string {
	if !ingest {
		return ""
	}
	column = strings.TrimSpace(column)
	if strings.HasPrefix(column, ",") || strings.HasSuffix(column, ",") {
		return " " + column
	}
	return ", " + column
}

// ambiguousProjectIdentitySQL is the retraction arm's project source: every
// project reachable through a project_key that names MORE THAN ONE project.
//
// WHY IT CANNOT REUSE key_resolution_count, which is the obvious instinct and
// silently produces nothing. The identity expansion emits TWO scope rows per
// project, and the ID row hard-codes `toUInt64(1) AS key_resolution_count`
// (readers v0.5.5, deliberately -- projects.id is unique, so an id match is
// unambiguous by construction, and emitting the project-level number there had
// already caused a consumer to discard every unambiguous Linear id match).
// The KEY row carries the real count but is emitted only for
// key_resolution_count = 1. So for an AMBIGUOUS key there is no row anywhere
// in the expansion carrying a count above 1: `p.key_resolution_count > 1`
// matches zero rows, always. That is not a hypothetical -- it is what the
// first version of this arm did, and the ambiguity retraction subtests caught
// it against a real ClickHouse while the conflicting-identity one passed.
//
// So ambiguity is re-derived, from the expansion's OWN output rather than a
// second read of `projects`: count the projects that answer to each
// (provider, project_key). That is the same statement the expansion's guard
// makes -- "a project_key naming more than one project cannot resolve an
// ownership row that carries only that key" -- computed one layer out.
//
// scope_kind = 'id' is a DE-DUPLICATOR, not a narrowing, and the reasoning is
// worth keeping because it is not obvious: the expansion's GROUP BY collapses
// a project whose id EQUALS its project_key into a single row labelled 'key'
// (max('id','key') = 'key'), which would drop it from this count. That can
// only happen when the KEY branch emitted, which requires
// key_resolution_count = 1, which means that key names exactly one project.
// So every member of a genuinely ambiguous partition is labelled 'id', and
// filtering on it cannot undercount an ambiguity into invisibility.
//
// The WHERE is in the same SELECT as the window on purpose: ClickHouse
// evaluates WHERE before window functions, so the count runs over the filtered
// rows, which is what makes the de-duplication above hold.
func ambiguousProjectIdentitySQL(ingest bool) string {
	return `(
	SELECT pa.provider AS provider, pa.id AS id, pa.project_key AS project_key, pa.project_updated_at AS project_updated_at, pa.provider_updated_at AS provider_updated_at,` + ingestStampSQL(ingest, " pa.project_ingest_at AS project_ingest_at, pa.provider_ingest_at AS provider_ingest_at,") + `
	       count() OVER (PARTITION BY pa.provider, pa.project_key) AS key_project_count
	FROM (SELECT * FROM ` + projectIdentityWithWatermarkSQL(ingest) + `) AS pa
	WHERE pa.scope_kind = 'id' AND pa.project_key != ''
) AS p`
}

func projectIdentityWithWatermarkSQL(ingest bool) string {
	return `(
	SELECT pi.*, w.project_updated_at AS project_updated_at, w.provider_updated_at AS provider_updated_at` + ingestStampSQL(ingest, ", w.project_ingest_at AS project_ingest_at, w.provider_ingest_at AS provider_ingest_at") + `
	FROM (SELECT * FROM ` + readers.ProjectIdentityCatalogSQL() + `) AS pi
	INNER JOIN (
		SELECT provider, id, updated_at AS project_updated_at, max(updated_at) OVER (PARTITION BY provider) AS provider_updated_at` + ingestStampSQL(ingest, "last_synced AS project_ingest_at, max(last_synced) OVER (PARTITION BY provider) AS provider_ingest_at") + `
		FROM projects FINAL
		WHERE org_id = {org_id:String}
	) AS w ON w.provider = pi.provider AND w.id = pi.id
) AS p`
}

// CHAOS-4542: joined on PROJECT IDENTITY, and grouped on the RESOLVED
// projects.id.
//
// WHY the join moved. CHAOS-4530 makes ownership rows UUID-keyed on
// team_project_ownership.project_id, nulls their project_key, and
// leaves real Linear projects' projects.project_key nil by design. The
// old `USING (provider, project_key)` plus `WHERE o.project_key != ”`
// therefore matches NOTHING for Linear once that deploys: this producer
// would emit zero OWNED_BY_TEAM edges and the graph would silently lose
// team<->project for every Linear project. It already reaches no real
// Linear project today, for the same reason.
//
// WHY the catalog form of the identity expansion, not the filtered one.
// This is a CATALOG walker -- it paginates the whole org by cursor and
// has no requested-subject list -- so ProjectIdentityJoinSQL's
// `... IN {ids:Array(String)}` row source cannot be used here: with no
// `ids` binding it fails with `Code: 456 Substitution 'ids' is not set`.
// That is the mismatch a first attempt at this change hit, and it is
// why v0.5.3 added ProjectIdentityCatalogSQL as the unfiltered sibling.
//
// WHY the GROUP BY moved with it, which is the dangerous half. The
// THIRD note above explains that grouping on the source's own
// project_id was avoided because two rows differing only in project_id
// resolve to the SAME projects.id, duplicating a RelationshipID --
// ContextFabricProjectionBatch.Validate() then rejects the whole batch,
// a rejected batch never advances a checkpoint, and the organization's
// projection WEDGES PERMANENTLY. Matching on identity makes that
// collision reachable for real during the 4530 transition, when one
// project can carry both a key-shaped and a UUID-shaped ownership row.
//
// Grouping on the resolved p.id closes it by construction: whatever id
// space the source rows arrive in, they collapse into ONE group per
// (project, team, source), because the group key IS the projected
// identity. Strictly safer than the previous grouping, which held only
// because (provider, project_key) happened to resolve 1:1. The collapse
// the FIRST note describes -- 616 rows in, exactly 3 out -- is
// preserved: same aggregation, keyed one join later.
//
// Pagination follows the aggregate. updated_at is now max(o.updated_at),
// which a WHERE cannot reference, so the keyset condition is emitted as
// HAVING through havingSincePredicate -- the same condition delegated,
// not a second spelling of it.
func projectTeamsStatement(cursor cursorState) string { return projectTeamsStatementFor(cursor, false) }

// projectTeamsStatementFor is projectTeamsStatement reading the ingest stamps
// when ingest is set.
func projectTeamsStatementFor(cursor cursorState, ingest bool) string {
	// TWO arm BLOCKS (CHAOS-4750 respelling of what was four separately
	// embedded arms), UNION ALL'd at ROW level, then aggregated on the
	// RESOLVED projects.id. Each block itself unions two match variants at
	// the OWNERSHIP row level -- see projectTeamsAssertingArm and
	// projectTeamsRetractionArm -- so there are still four ways a row can
	// resolve; what changed is only how many times the project source gets
	// embedded, not what any of the four can do:
	//
	//  The ASSERTING block's SCOPE variant matches project_ref against a
	//  scope row of BOTH kinds deliberately and must NOT be given a
	//  scope_kind restriction: project_ref is not an id column, it is
	//  whichever id space that row uses, and today's GitLab rows hold a
	//  project KEY there. CHAOS-4530's UUID-keyed rows match the id row,
	//  today's GitLab rows match the key row.
	//
	//  The ASSERTING block's KEY variant, and the only one that names
	//  scope_kind, matches project_key against the key SCOPE ROW rather
	//  than p.project_key, a column EVERY scope row carries: joining that
	//  column let an id row satisfy a key-shaped guard, and two projects
	//  sharing a key both matched an ownership row naming neither
	//  (CHAOS-4542 defect 6). An ambiguous key now has no scope row at
	//  all -- readers v0.5.5 applies the filter inside the expansion --
	//  so neither variant can resolve one, and no guard here can be
	//  forgotten. This variant is otherwise the ORIGINAL join, kept. An
	//  ownership row may carry a project_ref that correlates with nothing
	//  while its project_key is the only column tying it to a project.
	//  Dropping this variant loses those rows entirely -- which is exactly
	//  what the "tied assertions resolve deterministically" fixture
	//  caught: it seeds project_id 'ownership-row-open'/'-closed' against
	//  project_key 'TIE-KEY', so the scope variant matches neither.
	//
	// The union is at ROW level, not after aggregation, because the
	// aggregates (min(valid_from), the argMax window pair, max(updated_at))
	// must see EVERY contributing row for a (project, team, source) at
	// once -- aggregating per arm and merging afterwards would compute the
	// validity window twice over two partial row sets and pick a winner
	// from the wrong one.
	//
	// The outer GROUP BY on the resolved p.id is what makes the arms safe
	// to union at all, and is the wedge guard: two ownership rows in
	// DIFFERENT id spaces resolving to one project collapse into one group,
	// so they cannot duplicate a RelationshipID. A duplicate rejects the
	// batch, a rejected batch never advances a checkpoint, and the
	// organization's projection wedges PERMANENTLY -- the hazard the THIRD
	// note above records.
	// where carries any scope_kind restriction. It is a WHERE and not part
	// of the ON because 24.8 rejects an ON that is not a plain column
	// equality, and 'key' is a literal.
	//
	// retractionOnly is the literal '0' or '1' (CHAOS-4565). A retraction
	// block's rows exist ONLY so a group can be SEEN and retracted; they can
	// never assert an edge -- see the RETRACTION ARM note below.
	// FAIL CLOSED on conflicting identities (codex R2 P2-1).
	//
	// The scope and key variants resolve INDEPENDENTLY, so one ownership row whose
	// project_id resolves project A while its project_key resolves a
	// DIFFERENT project B produces a row from each, and the outer grouping
	// keeps both because it groups by the RESOLVED project. One of those
	// two OWNED_BY_TEAM edges is fabricated -- an ownership the source
	// never asserted -- and this table's project_id is documented as
	// unreliable, so there is no basis for calling either the winner.
	//
	// Same principle as an ambiguous key: never pick between two
	// disagreeing columns. min() = max() over the ownership row's own
	// identity is "exactly one distinct resolved project", spelled without
	// a DISTINCT aggregate (24.8 has no windowed uniqExact) and as a plain
	// equality.
	//
	// The conflicting rows are NOT filtered out in SQL. They are marked and
	// omitted in the scan, which is what lets them reach the ledger -- an
	// unrecorded omission is indistinguishable from an ownership that does
	// not exist, and a fabricated edge suppressed silently is exactly the
	// kind of correction an operator needs to see.
	//
	// The inner flag is named `unassertable`, and the outer one
	// `edge_suppressed`: an alias that shadows the column it aggregates
	// bound to itself on 24.8 once already in this file's history, and cost
	// a live round to find, so no two layers here share a name. The same
	// rule is why the window layer emits `row_watermark` rather than
	// re-aliasing `updated_at` over an expression that reads it.
	//
	// `unassertable` is not `identity_conflict` renamed for taste
	// (CHAOS-4565). It answers a strictly wider question -- "can this row
	// assert its edge" -- of which a conflicting identity is now only one
	// of two NOs, the other being "this row is a retraction row and never
	// could". Keeping the old name would have made every
	// `...If(..., = 0)` in the aggregate read as a conflict test while
	// actually gating on something broader, which is the kind of quiet
	// mismatch this file has paid for before. conflict_identities and
	// conflicting_identity_present spell the conflict half out explicitly
	// where the ledger and the retraction reason still need exactly it.
	// THE RETRACTION ARM (CHAOS-4565), and why the asserting block could
	// not carry it. Full design note, with the rejected alternatives and the
	// mermaid of the whole path:
	// docs/design/context-fabric-ownership-edge-retraction.md
	//
	// Suppression is a decision NOT TO ASSERT. It is not a retraction, and
	// incremental graph application does not delete an absent relationship
	// (internal/contextfabric/AGENTS.md: deletion semantics need an
	// explicit complete-enumeration proof, which an incremental page is
	// not). So an OWNED_BY_TEAM edge projected BEFORE its ownership row
	// became suppressed stays live forever, and only a full rebuild clears
	// it. The graph cannot tell "we never asserted this" from "we can no
	// longer substantiate it".
	//
	// The two suppression paths are NOT symmetric, which is the whole
	// reason this arm exists:
	//
	//   - CONFLICTING IDENTITY is decided in the SCAN. Both asserting
	//     variants produce a row, the group sees them, and edge_suppressed
	//     already reports it. A group that reaches the scan can be
	//     retracted from the scan.
	//   - An AMBIGUOUS KEY is decided UPSTREAM, in SQL:
	//     ProjectIdentityJoinSQL emits no key scope row at all for
	//     key_resolution_count > 1. The key variant therefore matches
	//     nothing, the scope variant matches nothing for a key-only
	//     ownership row, and the row produces NO RESULT ROW. There is
	//     nothing for the scan to see, so a scan-side retraction alone
	//     would silently fix only half the defect -- and the ambiguity
	//     half is the OLDER one, live since v7.
	//
	// The retraction arm makes the invisible half visible WITHOUT
	// re-admitting it as an assertion: it resolves an ownership key across
	// the AMBIGUOUS key
	// partition (key_resolution_count > 1), one row per project sharing
	// that key, flagged retraction_only = 1. Those projects are exactly the
	// candidates the edge could have been projected to while the key was
	// still unambiguous, so tombstoning every one of them retracts the real
	// edge and no-ops on the rest -- the same unconditional, idempotent
	// healing shape queryWorkItemDependencies' ref-form tombstones use
	// (tables.go), and for the same reason: no cross-row bookkeeping is
	// needed to know WHETHER a retraction is necessary.
	//
	//   scope_kind = 'id' is not a filter on WHICH projects match, it is a
	//   de-duplicator: an ambiguous key has no key scope row by
	//   construction, so 'id' is the only row each such project has, and
	//   naming it keeps one row per project if that ever changes.
	//   The retraction block's two variants each restrict to project_key
	//   != '' or project_key = '' as defence in depth -- the empty-key
	//   partition already has key_resolution_count = 0, so it cannot reach
	//   > 1 -- but an empty key matching every keyless project is precisely
	//   the defect class this file has shipped before, so it is spelled
	//   out, and keeping the two guards mutually exclusive is what stops
	//   one ownership row from double-matching through both variants.
	//
	// This arm must never be given the power to assert. unassertable below
	// is forced to 1 for every retraction_only row, so a retraction arm can
	// only ever SUPPRESS a group into existence, never keep one alive.
	//
	// WHY THE WATERMARK GREW A PROJECTS SIDE, and why the arm is dead code
	// without it. This producer's keyset cursor was max(o.updated_at) over
	// team_project_ownership ALONE. But ambiguity usually arrives because a
	// NEW project starts sharing an existing key -- that writes `projects`,
	// not team_project_ownership, so the ownership row's own updated_at
	// never moves, the group stays BEHIND the checkpoint, and no
	// incremental tick ever re-reads it. The retraction would be emitted by
	// code that is never reached. The same applies to a conflicting
	// identity that arrives because some OTHER project's project_key
	// changed to collide with this row's.
	//
	// So the row watermark is greatest(the ownership row's own updated_at,
	// the newest updated_at among every project this ownership row's
	// identity values can reach). The window partition is the ownership
	// row's own identity -- and both arm blocks are unioned BEFORE it --
	// so "every project this row can reach" is not a second query, it is
	// exactly the rows already present. A projects-side edit therefore
	// pushes the affected groups past the cursor on the very next tick,
	// with no rebuild and no checkpoint reset.
	//
	// retraction_only is IN the partition key. Without it, the retraction
	// block's extra resolved projects would land inside the asserting
	// block's own min()/max() comparison and be read as an identity
	// conflict, so an ownership row with a perfectly good project_id would
	// be suppressed the moment its UNUSED project_key became ambiguous --
	// turning a retraction feature into an edge-deletion bug. Splitting the
	// partition keeps the asserting block's conflict test byte-identical
	// to what it was.
	cursorWatermark := projectTeamsWatermark
	if ingest {
		cursorWatermark = projectTeamsIngestWatermark
	}
	return projectTeamsGroupSQL(ingest) + havingSincePredicate(cursor, cursorWatermark, projectTeamsRowKey) + orderBy(cursorWatermark, projectTeamsRowKey)
}

// projectTeamsGroupSQL is the projector's ownership GROUP statement up to and
// including its GROUP BY: one row per (project, team, source) group, with no
// pagination. It is the ONE spelling of that group: the projector pages it
// (projectTeamsStatementFor) and the project-team source-row read selects one
// group from it (ProjectTeamSourceRowStatement), so the two cannot disagree
// on which groups exist.
// projectTeamsSuppressedSQL is true for a group none of whose ownership rows
// can be asserted: the group projects a retraction, not an edge.
const projectTeamsSuppressedSQL = "countIf(o.unassertable = 0) = 0"

func projectTeamsGroupSQL(ingest bool) string {
	return `SELECT o.project_id, o.team_id, o.source_name,
       minIf(o.first_valid_from, o.unassertable = 0) AS first_valid_from,
       argMaxIf(tuple(o.valid_to), ` + ownershipFactOrder + `, o.unassertable = 0).1 IS NULL AS latest_is_open,
       ifNull(argMaxIf(tuple(o.valid_to), ` + ownershipFactOrder + `, o.unassertable = 0).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
       ` + projectTeamsWatermark + ` AS observed_at, o.provider,
       toUInt8(` + projectTeamsSuppressedSQL + `) AS edge_suppressed,
       groupUniqArrayIf(concat(o.ownership_ref, '\0', o.ownership_key, '\0', o.team_id, '\0', o.source_name), o.unassertable = 1 AND o.retraction_only = 0) AS conflict_identities,
       toUInt8(countIf(o.unassertable = 1 AND o.retraction_only = 0) > 0) AS conflicting_identity_present` + ingestStampSQL(ingest, projectTeamsIngestWatermark+" AS ingest_at") + `
` + projectTeamsFromGroupSQL(ingest)
}

// projectTeamsFromGroupSQL is the row source and GROUP BY of the ownership
// groups: one row per (project, provider, team, source). The edge count reads
// the same text.
func projectTeamsFromGroupSQL(ingest bool) string {
	const identityPartition = " OVER (PARTITION BY provider, ownership_ref, ownership_key, retraction_only)"
	// The provider-wide project stamp widens WHICH groups a cursor revisits. It
	// belongs in the cursor position only: observed_at is the edge's own
	// ObservedAt and a tombstone's EffectiveAt, which an unrelated project's
	// write must not move. On the ingest cursor the position is row_ingest, so
	// observed_at stays free of it; the legacy cursor's position IS observed_at,
	// so there it carries the term.
	legacyProviderTerm := ""
	if !ingest {
		legacyProviderTerm = ", max(provider_updated_at)" + identityPartition
	}
	return `FROM (
	SELECT project_id, provider, ownership_ref, ownership_key, team_id, source_name, first_valid_from, valid_from, valid_to, retraction_only,
	       greatest(updated_at, max(project_updated_at)` + identityPartition + legacyProviderTerm + `) AS row_watermark,` + ingestStampSQL(ingest, " greatest(ingest_at, max(project_ingest_at)"+identityPartition+", max(provider_ingest_at)"+identityPartition+") AS row_ingest,") + `
	       toUInt8(retraction_only = 1 OR min(project_id)` + identityPartition + ` != max(project_id)` + identityPartition + `) AS unassertable
	FROM (` + strings.Join(projectTeamsArmsFor(ingest), "\n\n\t\tUNION ALL\n") + `
	)
) AS o
GROUP BY o.project_id, o.provider, o.team_id, o.source_name`
}
