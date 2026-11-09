package devhealthsource

import (
	"context"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/ownershipresolve"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TeamsProjectsRelationshipTypes lists every ContextFabricRelationshipType
// this source's edge producers can write, the same contract
// ProducedRelationshipTypes (clickhouse.go) holds for the repository/work-item
// source. cmd/acr-projector's AC-3779-9 cross-wiring test unions both.
func TeamsProjectsRelationshipTypes() []contractsv1.ContextFabricRelationshipType {
	return []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBelongsToProject,
		contractsv1.ContextFabricRelationshipOwnedByTeam,
	}
}

// attributionProperties carries an Ops-computed attribution's own derivation
// metadata onto the edge. Both attribution tables below record HOW they
// decided (a source enum; work_item_team_attributions also a confidence
// enum), and that is exactly the information a consumer needs in order not to
// read a 'manual_fallback'/'inferred' attribution as though it were a
// canonical column -- see the derivation note on each producer.
func attributionProperties(source, confidence string) map[string]contractsv1.ContextFabricScalarValue {
	properties := map[string]contractsv1.ContextFabricScalarValue{}
	if source != "" {
		properties["attribution_source"] = stringScalar(source)
	}
	if confidence != "" {
		properties["attribution_confidence"] = stringScalar(confidence)
	}
	return properties
}

// querySubjectProjectMemberships projects work_item -> project and
// pull_request -> project (BELONGS_TO_PROJECT), reading the
// project_membership_presence VIEW (CHAOS-4193/4194; ops migration 077,
// mirrored verbatim at devhealthschema.ProjectMembershipPresenceViewDDL)
// instead of work_items.project_id directly (this producer's own prior
// shape, CHAOS-3802/CHAOS-4108). The read swap is WHY
// TeamsProjectsSourceVersion bumped v3->v4 (teams_projects.go): the row
// shape changed, so every already-projected organization needs a full
// rebuild rather than an incremental catch-up under a version marker that
// no longer describes what it reads.

// attributionSourceNativeTeam is work_item_team_attributions.source's
// strongest rank (ops/src/dev_health_ops/metrics/compute_work_items.py's
// _SOURCE_ORDER, rank 0): the work item carries a native_team_key the
// provider itself asserted (Linear teams only -- jira/github/gitlab work
// items carry native_team_key=None, per AGENTS.md's provider coverage
// section, so every one of their rows resolves through a LOWER-ranked,
// Ops-COMPUTED source instead: issue_project/project_ownership/
// repo_ownership/assignee_membership, all fed by the Python
// team_autoimport_{linear,jira,github,gitlab}.py heuristic populators, or
// linked_issue/manual_fallback). It is the only source value this producer
// treats as an asserted fact rather than an inference.
const attributionSourceNativeTeam = "native_team"

// workItemTeamAttributionDerivation maps work_item_team_attributions.source
// onto this edge's Derivation/EpistemicStatus pair (CHAOS-4101).
//
// THE BUG THIS FIXES. Every row of this edge previously carried the SAME
// hardcoded EpistemicStatus (source_asserted) regardless of source -- so a
// row whose real source was repo_ownership (a heuristic pattern match run by
// the Python team_autoimport populators, CHAOS-4198 still unported) was
// indistinguishable from a row whose source was native_team (a provider-
// asserted fact). A consumer reading the edge's EpistemicStatus rather than
// unpacking its attribution_source property saw every attribution as equally
// trustworthy, which is the exact overstatement CHAOS-4101's scope pass
// found: "the acr projection hardcodes Derivation=RuleInferred +
// EpistemicStatus=SourceAsserted on every row regardless of the row's
// actual source."
//
// Derivation stays RuleInferred for every value, including native_team: this
// edge is always ONE ROW of Ops' own precedence-resolved output
// (compute_work_item_team_attributions/_SOURCE_ORDER), never a plain
// canonical column the way querySubjectProjectMemberships' BELONGS_TO_PROJECT
// edge can be (its own work_item_column arm) -- even the native_team
// candidate only wins because the resolver ran
// its ranking over every candidate source and this one came out on top.
// EpistemicStatus is what actually varies: SourceAsserted only for
// native_team, whose underlying claim (this provider says this item belongs
// to this team) the provider itself made; Inferred for every other source,
// none of which any provider asserted.
//
// FAILS TOWARD THE WEAKER CLASSIFICATION for any source value this function
// does not recognise (a future Enum8 addition this producer has not been
// updated for): Inferred, never SourceAsserted. A reader must never see an
// attribution's basis overstated because a new enum value slipped past this
// switch.
func workItemTeamAttributionDerivation(source string) (contractsv1.ContextFabricDerivationMethod, contractsv1.ContextFabricEpistemicStatus) {
	if source == attributionSourceNativeTeam {
		return contractsv1.ContextFabricDerivationRuleInferred, contractsv1.ContextFabricEpistemicSourceAsserted
	}
	return contractsv1.ContextFabricDerivationRuleInferred, contractsv1.ContextFabricEpistemicInferred
}

// TWO subject kinds, one producer. The view's own ORDER BY-equivalent key
// is (org_id, subject_kind, repo_id, subject_id, project_id) -- Context
// Fabric ruling 2026-08-24 11:40 -- so unlike the old single-column source,
// ONE subject can now carry SEVERAL active project rows (a PR on two
// boards). Every row is projected independently; nothing here collapses
// them, which is what lets a PR removed from board B keep its edge to
// board A rather than losing both (the exact defect the view's own doc
// comment records the pre-ruling grouping caused).
//
// source ∈ {transition, work_item_column} (Context Fabric ruling
// 2026-08-24 09:55) discriminates the edge's evidentiary weight:
//
//   - 'work_item_column' is a plain canonical column read (the SAME
//     shape this producer always had for work_items.project_id, now
//     passed through the view's column arm unchanged) -- presence only,
//     no validity interval, exactly as before this migration.
//   - 'transition' is derived from project_membership_transitions'
//     FULL touch history for the (subject, project) pair, not merely its
//     latest touch (CHAOS-4109; see intervalDerivationCTE's own doc
//     comment for the interval rule). A subject that was added, removed
//     and re-added to the SAME project now projects MULTIPLE edges, one
//     per interval it actually held membership -- a closed one for the
//     earlier stretch, an open one for the current stretch if it is a
//     member again. Each interval is its OWN relationship candidate with
//     its own RelationshipID (see relationshipID below): a single
//     "earliest add to latest state" window, the shape
//     ownershipValidity (below) uses for the sibling project->team edge,
//     would be WRONG here -- it would claim the subject belonged to the
//     project throughout the gap it was actually removed, which is
//     exactly the false historical answer CHAOS-3781's H6 refusal
//     existed to prevent one edge at a time.
//
// pull_request subjects use the LEGACY "pull_request:<repo_id>:<number>"
// canonical id (tables.go:295,897; chaos4099_scope_expander.go:425) --
// deliberately NOT identity.Derive/identity.Registry. pull_request is
// grandfathered OUT of the identity registry on purpose
// (chaos3898_s3_census_bridge.go's bridgePullRequestSatisfier doc comment;
// registry_parity_test.go's TestRegistryCoversEveryChangedKind pins
// EXACTLY the five design-brief kinds, "zero exemptions, no extras"; and
// graphrank's TestPullRequestHandleValue proves the handle-offer extractor
// recognizes only this legacy shape). Deriving a `pull_request.v2:...` id
// here instead would mint a BELONGS_TO_PROJECT edge whose From endpoint
// matches no real pull_request entity node in the graph -- the exact
// dangling-edge class CHAOS-4108 fixed for projects, reintroduced on the
// other endpoint.
//
// Project resolution keeps queryProjects' dual id/key widening (CHAOS-4108)
// unchanged in shape, now scoped by provider (PARTITION BY provider,
// join_key rather than the old bare join_key): the view's own `provider`
// column is a real per-row fact for BOTH arms (project_membership_transitions.
// provider / work_items.provider), matching the 2026-08-24 09:45 ruling's
// vocabulary constraint that (provider, project_id) -- not project_id
// alone -- is what must resolve to a `projects` row. The join is LEFT, not
// INNER: an INNER join would silently drop an unresolvable row before this
// function's scan ever saw it, which is exactly the "measurement fails
// toward fine" shape this package's telemetry rules forbid. A LEFT JOIN
// miss reports key_resolution_count = 0 (ClickHouse's default-value
// behaviour for an unmatched non-Nullable column), so "zero matches" and
// "more than one match" are both visible to the scan and both counted by
// telemetry.recordUnresolved/recordAmbiguous -- see presenceTelemetryLedger
// (teams_projects.go).
// resolvedProjectsSubquery is the widened project-id/project-key join arm
// CHAOS-4108 proved live (id space AND project_key space, scoped by
// provider), factored out as a Go constant so both UNION arms of
// querySubjectProjectMemberships' statement can each embed it -- codex
// xhigh review R2's fan-out guard (compute key_resolution_count as a
// window function BEFORE LIMIT 1 BY collapses the group, never after) is
// stated ONCE here rather than twice, so the two arms cannot silently
// drift out of agreement on how ambiguity is counted.
//
// A plain parenthesized derived table, NOT a `WITH ... AS (...)` CTE:
// runtimeclickhouse's validateReadOnlyStatement requires a read-only
// statement's FIRST TOKEN to be literally "SELECT" (ErrUnsafeStatement
// otherwise), which a top-level WITH clause is not, however deeply nested
// the SELECT beneath it.
//
// ONE read of projects: each row fans out to its id row and, when it has a
// project_key, its key row (arrayJoin), the rows an id-arm/key-arm UNION
// gave with two reads.
const resolvedProjectsSubquery = `(
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS join_key
      FROM projects FINAL WHERE org_id = {org_id:String}
    )
  )
  LIMIT 1 BY provider, join_key
)`

// membershipIntervalsSubquery derives EVERY membership interval a
// (subject, project) pair ever held from project_membership_transitions'
// full touch history (CHAOS-4109) -- not merely the latest one
// project_membership_presence's transition arm reports.
//
// THE INTERVAL RULE (revised per team-lead ruling 2026-08-25, superseding
// this subquery's own first version). Unpivot each transition row into up
// to two polarized touches of a project: to_project_id is an ADD
// (is_add=1), and from_project_id -- when it differs from to_project_id --
// is a REMOVE (is_add=0). A row where from_project_id = to_project_id (a
// provider re-asserting a membership it already holds, typically a
// project KEY change with the id unchanged) is EXCLUDED from both arms: it
// touches no boundary, by the same reasoning project_membership_presence's
// own "(P, P) contributes ONE touch, the joined side, not two" comment
// gives for presence.
//
// Ordered per (org_id, subject_kind, repo_id, subject_id, provider,
// project_id) by (occurred_at, event_id) -- the transitions table's own
// ORDER BY suffix, so ties break identically to the source of truth. Each
// touch is classified against its own immediate predecessor via
// lagInFrame (a proper window function; neighbor() is block-order-
// dependent and explicitly unsafe here):
//
//   - an ADD immediately preceded by another ADD (no REMOVE between) is a
//     DUPLICATE_ADD, not malformed: #1896's discard-and-replay path and an
//     ordinary re-sync of an unchanged board membership both legitimately
//     emit a second ADD for a subject that never left. It is a
//     CONTINUATION of the already-open interval, not a new one -- ValidFrom
//     stays the FIRST ADD's timestamp, no interval boundary is created for
//     it, and the traversal keeps attributing through the original
//     interval unchanged. Counted (never silently dropped), never
//     fabricated into a boundary the source data does not assert.
//   - a REMOVE immediately preceded by anything OTHER than an ADD (no
//     ADD at all before it in this history, or another REMOVE) is a
//     DANGLING REMOVE: it closes nothing, because nothing is open. THIS is
//     the one case still reserved for "malformed": skipped (it can never
//     become an interval boundary) and counted, never silently absorbed.
//     A subject's very first tracked touch of a project being a REMOVE
//     falls in this case too -- "this membership existed before transition
//     tracking began", whose true start is genuinely unknown.
//   - a REMOVE immediately preceded by an ADD is the ordinary case: it
//     closes that ADD's interval at this REMOVE's own timestamp.
//
// IMPLEMENTATION: a two-stage window-function pass, because a single
// leadInFrame(x, 1) cannot skip an arbitrary-length run of duplicate ADDs
// to find the touch that actually closes an interval. Stage one
// (`classified`) computes dup_flag/dangling_flag from each touch's
// immediate predecessor over the FULL unfiltered touch set. Stage two
// (`closed`) re-runs row_number/count/leadInFrame over the touch set with
// duplicate ADDs REMOVED (`WHERE NOT dup_flag`) -- in that filtered set, by
// construction, no is_add=1 row can ever have another is_add=1 row as its
// immediate successor (that case was exactly what dup_flag removed), so
// the "next touch is ANOTHER ADD" case this subquery's first version had
// to guard against is now structurally impossible rather than merely
// checked for.
//
// CODEX/CLICKHOUSE TRAP (live-verified against ClickHouse 24.8, worth
// recording): the two CTE-local classification columns are named
// `dup_flag`/`dangling_flag`, NOT `is_duplicate_add`/`is_malformed` --
// those exact names are also the OUTER query's own output aliases below.
// Reusing them for the CTE-internal columns silently duplicated every row
// across UNION ALL branches (each branch's literal `0 AS is_malformed`/
// `1 AS is_duplicate_add` collided with the identically-named upstream
// window-function column still carried through `SELECT *`-shaped CTEs),
// even though each branch's own WHERE clause, queried in isolation,
// filtered correctly. Reproduced and fixed by hand against a real
// ClickHouse container before this shape was ever run through Go.
//
// A plain parenthesized derived table, not a top-level `WITH` -- see
// resolvedProjectsSubquery's own doc comment for why (ErrUnsafeStatement
// only checks the OUTERMOST statement's first token, so a `WITH` clause
// nested inside this parenthesized subquery, as used below, is fine).
//
// event_id is projected through (codex xhigh review R1, HIGH, confirmed
// real): project_membership_transitions' own ORDER BY is (..., occurred_at,
// event_id), because two DISTINCT reassignment events CAN legally share an
// occurred_at down to this column's DateTime64(3) millisecond precision
// (a bulk re-sync, or two rapid moves landing in the same millisecond).
// ValidFrom alone (an interval's own occurred_at) is therefore NOT always a
// unique key for a (subject, project) pair's intervals -- two intervals
// opened in the same millisecond would mint the SAME RelationshipID (one
// silently overwriting the other via the graph write's MERGE-on-id
// semantics) and the SAME keyset-pagination cursor position (one row
// silently skipped). event_id is this table's own tiebreaker for exactly
// this case, so querySubjectProjectMemberships' own RelationshipID suffix
// and rowSortKey both include it too, below.
func membershipIntervalsSubquery(ingest bool, changed string) string {
	touchIngest := "occurred_at"
	if ingest {
		touchIngest = "ingested_at"
	}
	return membershipIntervalsSQL(touchIngest, ingest, changed)
}

// membershipIntervalsSQL renders the interval source with ingestExpr (a
// project_membership_transitions column) carried through as ingest_at: a
// touch's own stamp folded with the touch before it (a late earlier touch
// changes this touch's duplicate/dangling classification) and, among the
// non-duplicate touches, the one after it (a late later touch changes this
// interval's valid_to).
// ingested says whether the transitions table has ingested_at (the dedupe
// tie-break; see devhealthschema.DedupedMembershipTransitions).
func membershipIntervalsSQL(ingestExpr string, ingested bool, changed string) string {
	deduped := devhealthschema.DedupedMembershipTransitionsFiltered(ingested, changed)
	return `(
  WITH touches AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider, to_project_id AS project_id, occurred_at, event_id, ` + ingestExpr + ` AS ingest_at, 1 AS is_add
    FROM ` + deduped + `
    WHERE org_id = {org_id:String} AND to_project_id != '' AND to_project_id != from_project_id
    UNION ALL
    SELECT org_id, subject_kind, repo_id, subject_id, provider, from_project_id AS project_id, occurred_at, event_id, ` + ingestExpr + ` AS ingest_at, 0 AS is_add
    FROM ` + deduped + `
    WHERE org_id = {org_id:String} AND from_project_id != '' AND from_project_id != to_project_id
  ),
  classified AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at, event_id, is_add,
      greatest(ingest_at, lagInFrame(ingest_at, 1, ingest_at) OVER w) AS ingest_c,
      (is_add = 1 AND lagInFrame(is_add, 1, 2) OVER w = 1) AS dup_flag,
      (is_add = 0 AND lagInFrame(is_add, 1, 2) OVER w != 1) AS dangling_flag
    FROM touches
    -- lagInFrame's own default (the third argument, 2 -- an impossible
    -- value for is_add's {0,1} domain) marks "no previous touch at all"
    -- distinctly from an actual REMOVE (0), so the very first touch of a
    -- (subject, project) pair is correctly classified: an ADD is never
    -- flagged duplicate (nothing preceded it) and a REMOVE is always
    -- flagged dangling (nothing preceded it either). See
    -- resolvedProjectsSubquery's own ROWS BETWEEN note -- the identical
    -- explicit frame is load-bearing here for the same reason.
    WINDOW w AS (PARTITION BY org_id, subject_kind, repo_id, subject_id, provider, project_id ORDER BY occurred_at, event_id ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
  ),
  closed AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at, event_id, is_add,
      greatest(ingest_c, leadInFrame(ingest_c, 1, ingest_c) OVER w2) AS ingest_at,
      row_number() OVER w2 AS rn,
      count() OVER (PARTITION BY org_id, subject_kind, repo_id, subject_id, provider, project_id) AS touch_count,
      leadInFrame(occurred_at, 1, occurred_at) OVER w2 AS next_occurred_at,
      leadInFrame(is_add, 1, is_add) OVER w2 AS next_is_add
    FROM classified
    WHERE NOT dup_flag
    WINDOW w2 AS (PARTITION BY org_id, subject_kind, repo_id, subject_id, provider, project_id ORDER BY occurred_at, event_id ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
  )
  SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at AS observed_at, event_id, ingest_at,
    'transition' AS source,
    if(rn < touch_count AND next_is_add = 0, next_occurred_at, NULL) AS valid_to,
    0 AS is_malformed, 0 AS is_duplicate_add
  FROM closed
  WHERE is_add = 1
  UNION ALL
  SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at AS observed_at, event_id, ingest_c AS ingest_at,
    'transition' AS source,
    NULL AS valid_to,
    1 AS is_malformed, 0 AS is_duplicate_add
  FROM classified
  WHERE dangling_flag
  UNION ALL
  SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at AS observed_at, event_id, ingest_c AS ingest_at,
    'transition' AS source,
    NULL AS valid_to,
    0 AS is_malformed, 1 AS is_duplicate_add
  FROM classified
  WHERE dup_flag
)`
}

// columnSupersededSource labels the rows that retract a column-arm edge.
const columnSupersededSource = "column_superseded"

// supersededColumnsSubquery is every work item that still carries a project in
// work_items but also has transition history. The presence view drops such a
// work item's column row (subjects_with_history), so the producer never sees it
// again and its column-arm edge would stay open beside the transition edges.
// The predicate on provider and project is the view's own column arm, so each
// row names exactly the project whose column edge was projected.
// observed_at / ingest_at carry the newer of the work item's own stamp and its
// history's, so the row re-enters the cursor when either side changes.
func supersededColumnsSubquery(ingest bool, changed string) string {
	historyStamp, workItemStamp := "occurred_at", "w.updated_at"
	if ingest {
		historyStamp, workItemStamp = "ingested_at", "w.ingested_at"
	}
	return `(
  SELECT w.org_id AS org_id, w.repo_id AS repo_id, w.work_item_id AS subject_id, w.provider AS provider, w.project_id AS project_id,
    greatest(w.updated_at, h.latest_occurred) AS observed_at, greatest(` + workItemStamp + `, h.latest_stamp) AS ingest_at
  FROM (SELECT * FROM work_items FINAL WHERE org_id = {org_id:String}) AS w
  INNER JOIN (
    SELECT org_id, repo_id, subject_id, max(occurred_at) AS latest_occurred, max(` + historyStamp + `) AS latest_stamp
    FROM ` + devhealthschema.DedupedMembershipTransitionsFiltered(ingest, changed) + `
    WHERE subject_kind = 'work_item'
    GROUP BY org_id, repo_id, subject_id
  ) AS h ON h.org_id = w.org_id AND h.repo_id = w.repo_id AND h.subject_id = w.work_item_id
  WHERE w.org_id = {org_id:String} AND w.project_id != ''
    AND w.provider != 'gitlab'
    AND (w.provider != 'github' OR startsWith(w.project_id, 'ghprojv2:'))
)`
}

// columnProjectRetractions is the one guard that keeps a work item's open
// column-arm edges to the project its column names now: a column row whose
// project resolves to nothing (or to several) and a work item that history
// supersedes both retract every open column edge of the work item, whichever
// project it names. A resolved column row needs nothing here: the sink keeps
// only the edge it asserts. A pull request can belong to several boards, so
// only work items are covered. One candidate per row, so the page cut counts
// rows as before.
func columnRetractionReason(source string) string {
	if source == columnSupersededSource {
		return "superseded_by_transition_history"
	}
	return "column_project_unresolved"
}

func columnProjectRetractions(subjectKind, repoID, subjectID, source string, observedAt time.Time, sortKey string) ([]candidate, error) {
	var out []candidate
	if subjectKind == "work_item" && (source == "work_item_column" || source == columnSupersededSource) {
		canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, subjectID}, nil)
		if err != nil {
			return nil, err
		}
		if !omitted {
			tombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind:          contextfabric.TombstoneKindColumnProjectEdges,
				CanonicalID:   canonicalID,
				Reason:        columnRetractionReason(source),
				EffectiveAt:   observedAt,
				SourceVersion: TeamsProjectsSourceVersion,
			}
			out = append(out, candidate{observedAt: observedAt, sortKey: sortKey, tombstone: &tombstone})
		}
	}
	if len(out) == 0 {
		out = []candidate{progressCandidate(observedAt, sortKey)}
	}
	return out, nil
}

// changedMembershipFilters bound the statement's cost to what the keyset
// predicate can still return. The cursor column of every arm is built only
// from the touches, the work item row, the presence row and their history --
// never from the project or repository side -- so a subject none of those
// stamped at or after the cursor yields no row past it. Whole subjects are
// kept or dropped, so every window function runs over the full history of the
// subjects it sees. An empty cursor keeps everything.
func changedMembershipFilters(cursor cursorState, ingest bool) (touches, superseded, presence string) {
	if cursor.Since.IsZero() {
		return "", "", ""
	}
	transitionStamp, workItemStamp, presenceStamp := "occurred_at", "updated_at", "m.observed_at"
	if ingest {
		transitionStamp, workItemStamp, presenceStamp = "ingested_at", "ingested_at", "m.last_synced"
	}
	touches = " AND (subject_kind, repo_id, subject_id) IN (SELECT subject_kind, repo_id, subject_id FROM project_membership_transitions WHERE org_id = {org_id:String} AND " + transitionStamp + " >= " + sinceBoundSQL + ")"
	superseded = " AND (repo_id, subject_id) IN (SELECT repo_id, subject_id FROM project_membership_transitions WHERE org_id = {org_id:String} AND subject_kind = 'work_item' AND " + transitionStamp + " >= " + sinceBoundSQL +
		" UNION ALL SELECT repo_id, work_item_id FROM work_items FINAL WHERE org_id = {org_id:String} AND " + workItemStamp + " >= " + sinceBoundSQL + ")"
	presence = " AND " + presenceStamp + " >= " + sinceBoundSQL
	return touches, superseded, presence
}

func subjectProjectMembershipsQuery(telemetry *presenceTelemetryLedger, ingest bool) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return querySubjectProjectMemberships(ctx, client, orgID, cursor, limit, telemetry, ingest)
	}
}

func membershipStatement(cursor cursorState, ingest, pruned bool) string {
	var incrementalSettings string
	if pruned && !cursor.Since.IsZero() {
		// Measured on a real server: with the subjects pruned, the memory
		// left is the per-thread buffers of the three arms (about 50 MB at
		// 4 threads, 17 MB at one), not the data.
		incrementalSettings = " SETTINGS max_threads = 1"
	}
	cursorColumn := "observed_at"
	if ingest {
		cursorColumn = "ingest_at"
	}
	rowKey := membershipRowKey()
	// The page is chosen from the arm rows BEFORE any project is resolved: the
	// keyset, order and limit read membership columns only, so the projects
	// lookup runs once, for at most row_limit rows, instead of once per arm
	// for every row of the organization. The LEFT JOIN keeps one row per page
	// row (resolvedProjectsSubquery is LIMIT 1 BY its join key), and the outer
	// ORDER BY restores the page order the join does not keep.
	page := membershipArmRows(cursor, ingest, pruned) + sincePredicate(cursor, cursorColumn, rowKey) + orderBy(cursorColumn, rowKey)
	return "SELECT * FROM (" + membershipResolvedSQL(page, ingest) + ")" + orderBy(cursorColumn, rowKey) + incrementalSettings
}

// membershipRowKey is the keyset tiebreaker of the membership statement.
//
// event_id is part of the row key (codex xhigh review R1, HIGH,
// confirmed real -- see membershipIntervalsSubquery's own doc comment):
// two transition-arm intervals for the SAME (subject, project) pair CAN
// legally share an observed_at down to the millisecond, and without
// event_id in the tiebreaker, both the RelationshipID (below) and the
// keyset-pagination cursor would collide on it.
func membershipRowKey() string {
	return rowKeySQL("subject_kind", "repo_id_str", "subject_id", "provider", "project_id", "event_id")
}

// membershipRows is every row of the three arms with its project resolved.
// The edge count reads it through membershipEdgeCountSQL; the producer reads
// the same arm rows and the same resolution (membershipStatement), so the
// count and the producer share one row source.
func membershipRows(cursor cursorState, ingest, pruned bool) string {
	return membershipResolvedSQL(membershipArmRows(cursor, ingest, pruned), ingest)
}

// membershipResolvedSQL resolves each row of rows -- membershipArmRows,
// whole or paged -- to its project with ONE join of resolvedProjectsSubquery.
func membershipResolvedSQL(rows string, ingest bool) string {
	ingestSelect := ""
	if ingest {
		ingestSelect = ", pg.ingest_at AS ingest_at"
	}
	return `SELECT pg.subject_kind AS subject_kind, pg.repo_id_str AS repo_id_str, pg.subject_id AS subject_id, pg.repo_slug AS repo_slug, pg.observed_at AS observed_at, pg.event_id AS event_id, pg.source AS source, pg.provider AS provider, pg.project_id AS project_id, p.id AS resolved_project_id, p.key_resolution_count AS key_resolution_count, pg.valid_to_present AS valid_to_present, pg.valid_to_value AS valid_to_value, pg.is_malformed AS is_malformed, pg.is_duplicate_add AS is_duplicate_add` + ingestSelect + `
FROM (` + rows + `) AS pg
LEFT JOIN ` + resolvedProjectsSubquery + ` AS p ON p.provider = pg.provider AND p.join_key = pg.project_id`
}

// membershipArmRows is the membership row source up to and including its
// WHERE 1 = 1: every row of the three arms, before project resolution, the
// keyset predicate, ordering and page limit.
func membershipArmRows(cursor cursorState, ingest, pruned bool) string {
	var changedTouches, changedSuperseded, changedPresence string
	if pruned {
		changedTouches, changedSuperseded, changedPresence = changedMembershipFilters(cursor, ingest)
	}
	// toDateTime64(0, 3, 'UTC') matches project_membership_transitions.
	// occurred_at's own declared scale (DateTime64(3)) -- the NULL branch of
	// transition arm's `valid_to` (membershipIntervalsSubquery) is derived
	// from that column via leadInFrame, so the ifNull default must share its
	// scale for the two UNION arms to agree on one column type.
	// ingest: the cursor pages on the ingest stamp (the transitions'
	// ingested_at, folded with the adjacent touches, and the presence view's
	// last_synced) selected as the LAST column; otherwise on observed_at.
	ingestSelect, transitionIngest, columnIngest, supersededIngest := "", "", "", ""
	if ingest {
		ingestSelect = ", ingest_at"
		transitionIngest, columnIngest, supersededIngest = ", m.ingest_at AS ingest_at", ", m.last_synced AS ingest_at", ", m.ingest_at AS ingest_at"
	}
	return `SELECT subject_kind, repo_id_str, subject_id, repo_slug, observed_at, event_id, source, provider, project_id, valid_to_present, valid_to_value, is_malformed, is_duplicate_add` + ingestSelect + `
FROM (
  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, m.event_id AS event_id, m.source AS source, m.provider AS provider, m.project_id AS project_id,
    isNotNull(m.valid_to) AS valid_to_present, ifNull(m.valid_to, toDateTime64(0, 3, 'UTC')) AS valid_to_value, m.is_malformed AS is_malformed, m.is_duplicate_add AS is_duplicate_add` + transitionIngest + `
  FROM ` + membershipIntervalsSubquery(ingest, changedTouches) + ` AS m
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
  UNION ALL
  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, '' AS event_id, m.source AS source, m.provider AS provider, m.project_id AS project_id,
    0 AS valid_to_present, toDateTime64(0, 3, 'UTC') AS valid_to_value, 0 AS is_malformed, 0 AS is_duplicate_add` + columnIngest + `
  FROM project_membership_presence AS m
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
  WHERE m.org_id = {org_id:String} AND m.source = 'work_item_column'` + changedPresence + `
  UNION ALL
  SELECT 'work_item' AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, '' AS event_id, 'column_superseded' AS source, m.provider AS provider, m.project_id AS project_id,
    0 AS valid_to_present, toDateTime64(0, 3, 'UTC') AS valid_to_value, 0 AS is_malformed, 0 AS is_duplicate_add` + supersededIngest + `
  FROM ` + supersededColumnsSubquery(ingest, changedSuperseded) + ` AS m
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
)
WHERE 1 = 1`
}

func querySubjectProjectMemberships(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, telemetry *presenceTelemetryLedger, ingest bool) ([]candidate, bool, error) {
	statement := membershipStatement(cursor, ingest, true)
	fetchRows := fetch
	if ingest {
		fetchRows = fetchIngest
	}
	// rowArm is the arm of the row scanRow read last; every candidate of that
	// row is labelled with it for the page telemetry.
	var rowArm string
	scanRow := func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var subjectKind, repoID, subjectID, repoSlug, eventID, source, provider, projectID, resolvedProjectID string
		var observedAt, validToValue time.Time
		var keyResolutionCount uint64
		var validToPresent, isMalformed, isDuplicateAdd uint8
		if err := r.Scan(&subjectKind, &repoID, &subjectID, &repoSlug, &observedAt, &eventID, &source, &provider, &projectID, &resolvedProjectID, &keyResolutionCount, &validToPresent, &validToValue, &isMalformed, &isDuplicateAdd); err != nil {
			return nil, err
		}
		rowArm = source
		observedAt = observedAt.UTC()
		rowSortKey := identity.JoinSegments(subjectKind, repoID, subjectID, provider, projectID, eventID)
		telemetry.recordRead(source, subjectKind)
		duplicateAdd := isDuplicateAdd != 0
		if duplicateAdd {
			// An ADD immediately preceded by another ADD, no REMOVE between
			// (a discard-and-replay or an ordinary re-sync). Counted, not
			// malformed. The interval it belongs to is opened by the FIRST
			// ADD of the run. When this touch was projected earlier as its
			// own open interval (a late earlier ADD has since displaced it
			// as the run's first), that edge is retracted below.
			telemetry.recordDuplicateAdd(provider, projectID)
		}
		if isMalformed != 0 {
			// See membershipIntervalsSubquery's own doc comment: a REMOVE
			// with no prior ADD to close (either the very first tracked
			// touch of this pair, or a second REMOVE with nothing reopened
			// in between). Unlike a duplicate ADD, this closes nothing and
			// starts nothing -- a producer-side data-quality anomaly,
			// never guessed into an interval boundary the source data does
			// not assert -- omitted and counted, the same discipline the
			// unresolved/ambiguous branches below apply.
			telemetry.recordMalformedTouch(provider, projectID)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		switch {
		case keyResolutionCount == 0:
			if source != columnSupersededSource {
				telemetry.recordUnresolved(provider, projectID)
			}
			return columnProjectRetractions(subjectKind, repoID, subjectID, source, observedAt, rowSortKey)
		case keyResolutionCount > 1:
			if source != columnSupersededSource {
				telemetry.recordAmbiguous(provider, projectID)
			}
			return columnProjectRetractions(subjectKind, repoID, subjectID, source, observedAt, rowSortKey)
		}
		// The canonical id is always derived from the JOINED project row's
		// own (provider, id) -- resolvedProjectID (p.id) -- never from
		// projectID (m.project_id): the latter can be a project_key value,
		// which is not the segment shape identity.Derive(KindProject, ...)
		// expects and would mint an id no graph node actually carries.
		projectCanonicalID, projectOmitted, err := identity.Derive(identity.KindProject, []string{provider, resolvedProjectID}, nil)
		if err != nil {
			return nil, err
		}
		if projectOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}

		var fromSubject contractsv1.ContextFabricSubjectRef
		var evidenceRefID string
		var authorization contractsv1.ContextFabricAuthorizationScope
		switch subjectKind {
		case "work_item":
			workItemCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, subjectID}, nil)
			if err != nil {
				return nil, err
			}
			if omitted {
				return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
			}
			fromSubject = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: workItemCanonicalID, Label: contextfabric.ReadableSourceID(contextfabric.SubjectWorkItem, subjectID)}
			evidenceRefID = contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, repoID+":"+subjectID)
			// CHAOS-3785 zero-UUID discipline: a Linear-sourced work item's
			// repo_id is repo-less BY DESIGN and must not read as an orphan.
			authorization = workItemAuthorization(repoID, repoSlug)
		case "pull_request":
			// git_pull_requests.number is UInt32 (tables.go's CHAOS-3789
			// scan-then-convert note; chaos3899_census_registry.go's
			// pullRequestNumberPredicate is the sibling handle-registry
			// parse of the same column, same bound) -- codex xhigh review
			// R2: subjectID here comes from the view's unconstrained
			// String source column (schema.go), so parsing into an int64
			// would silently accept a negative or oversized value no real
			// PR entity can carry, producing a canonical id that matches
			// no node in the graph.
			number, parseErr := strconv.ParseUint(subjectID, 10, 32)
			if parseErr != nil {
				return nil, fmt.Errorf("devhealthsource: presence view pull_request subject_id %q is not a valid UInt32 PR number: %w", subjectID, parseErr)
			}
			// Legacy pull_request canonical id scheme -- see this
			// function's own doc comment on why identity.Derive is
			// deliberately never used here.
			fromSubject = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: pullRequestCanonicalID(repoID, int64(number)), Label: fmt.Sprintf("PR #%d", number)}
			evidenceRefID = contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, repoID+":"+subjectID)
			// Unlike a Linear work item, a pull request always belongs to a
			// real git repository -- git_pull_requests.repo_id is a
			// non-nullable UUID at the source. A repoSlug miss here is
			// therefore a genuine orphan, never a by-design repo-less
			// subject, so this does NOT route through workItemAuthorization's
			// zero-UUID branch.
			if repoSlug != "" {
				authorization = repoAuthorization(repoSlug)
			} else {
				authorization = contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{orphanedRepositorySentinel}}
			}
		default:
			// subject_kind is a closed vocabulary at the source
			// (project_membership_transitions.subject_kind and the view's
			// own column-arm literal): {work_item, pull_request}. An
			// unrecognized value is schema drift this producer must not
			// silently misroute.
			return nil, &ProducerRejection{Reason: fmt.Sprintf("project_membership_presence returned unknown subject_kind %q (source %q, repo %q, subject %q, project %q)", subjectKind, source, repoID, subjectID, projectID)}
		}

		// source is a closed vocabulary too ({transition, work_item_column})
		// -- codex xhigh review R1: an unconditional
		// if-transition-else-column-semantics fall-through would silently
		// misclassify a future/drifted source value as work_item_column (no
		// interval) rather than rejecting it, the same class of silent
		// misroute the subject_kind switch above already fails closed
		// against.
		//
		// relationshipIDIntervalSuffix (CHAOS-4109) makes a transition-arm
		// RelationshipID unique PER INTERVAL, not merely per (subject,
		// project): membershipIntervalsSubquery can now hand this scan
		// several rows sharing every other key and differing only in
		// ValidFrom (an earlier closed stretch, a later open one), and
		// ContextFabricProjectionBatch.Validate rejects a batch carrying two
		// candidates with the same RelationshipID outright. ValidFrom is
		// immutable once written (a later REMOVE only ever fills in this
		// SAME interval's ValidTo via the graph write's own MERGE-on-id
		// semantics; it never moves where the interval started), so the
		// suffix is stable across rebuilds for the identical interval and
		// distinct for a genuinely different one. work_item_column rows
		// never carry a ValidFrom and keep the bare id unchanged.
		//
		// eventID is appended too (codex xhigh review R1, HIGH, confirmed
		// real): observedAt alone is not always unique across intervals for
		// the SAME (subject, project) pair -- project_membership_transitions'
		// own DateTime64(3) millisecond precision means two distinct ADD
		// events CAN share a timestamp, which would otherwise mint the SAME
		// RelationshipID for two different intervals (one silently
		// clobbering the other via the graph write's MERGE-on-id). event_id
		// is that table's own tiebreaker for exactly this case.
		var validFrom, validTo *time.Time
		var relationshipIDIntervalSuffix string
		switch source {
		case "transition":
			validFrom = requiredTime(observedAt)
			validTo = optionalTime(validToPresent, validToValue)
			relationshipIDIntervalSuffix = ":" + observedAt.Format(time.RFC3339Nano) + ":" + eventID
		case "work_item_column":
			// No interval: a plain canonical-column passthrough, presence only.
		case columnSupersededSource:
			// The subject has transition history, so no column project owns it
			// any more: retract every open column-arm edge of the work item.
			return columnProjectRetractions(subjectKind, repoID, subjectID, source, observedAt, rowSortKey)
		default:
			return nil, &ProducerRejection{Reason: fmt.Sprintf("project_membership_presence returned unknown source %q", source)}
		}

		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID:  projectMembershipRelationshipID(fromSubject.CanonicalID, projectCanonicalID, relationshipIDIntervalSuffix),
			Type:            contractsv1.ContextFabricRelationshipBelongsToProject,
			From:            fromSubject,
			To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: projectCanonicalID, Label: projectID},
			Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization:   authorization,
			EvidenceRefIDs:  []string{evidenceRefID},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
			ValidFrom:       validFrom,
			ValidTo:         validTo,
		}
		if duplicateAdd {
			// Tombstone the interval this touch would have opened. A no-op
			// for an edge never projected; for one projected before a late
			// earlier ADD arrived it removes the superseded open interval.
			tombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind:          "relationship",
				CanonicalID:   relationship.RelationshipID,
				Reason:        "superseded_by_earlier_add",
				EffectiveAt:   observedAt,
				SourceVersion: TeamsProjectsSourceVersion,
			}
			return []candidate{{observedAt: observedAt, sortKey: rowSortKey, tombstone: &tombstone}}, nil
		}
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	}
	rows, more, err := fetchRows(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		items, err := scanRow(r)
		for i := range items {
			items[i].arm = rowArm
		}
		return items, err
	})
	if err == nil {
		telemetry.recordStatement(rows, more)
	}
	return rows, more, err
}

// queryWorkItemTeams (statement: workItemTeamsStatement) projects work_item -> team (OWNED_BY_TEAM) from
// work_item_team_attributions: the primary row (is_primary = 1) and the
// co-owner rows (is_primary = 2), one edge per team.
//
// The edge is per (work item, team), so a co-owner team gets its own edge and
// a work item with a primary and a co-owner row collapses onto no duplicate
// relationship ID. Candidate rows (is_primary = 0) are excluded: without the
// is_primary filter the same work item carries up to five attributions from
// different sources (native_team, assignee_membership, issue_project,
// linked_issue, project_ownership) for the same team, which would collapse
// onto duplicate relationship IDs and fail
// ContextFabricProjectionBatch.Validate() outright. The page key carries the
// team id because a primary and a co-owner row of one work item share a
// computed_at.
//
// Derivation is always RuleInferred, NOT canonical_structured: this table is
// Ops' own computed attribution (its source enum spans native_team through
// manual_fallback, with a confidence enum beside it), not a canonical column
// the way work_items.project_id is -- even a native_team row only wins
// because Ops' own resolver ranked it first among the candidates it saw.
// EpistemicStatus, unlike Derivation, VARIES BY ROW (CHAOS-4101, fixed
// 2026-08-24): SourceAsserted for a native_team row (the provider itself
// asserted that claim; Linear only -- see workItemTeamAttributionDerivation),
// Inferred for every other source. Relabelling a low-confidence
// manual_fallback attribution as observed canonical truth is precisely the
// "graph discoveries may not mint canonical truth" line in this package's
// AGENTS.md, and hardcoding SourceAsserted for every row regardless of source
// was the identical overstatement one level up. Both enums also ride along
// as edge properties (attribution_source/attribution_confidence) so a
// consumer can see the exact underlying value, not just the two-way
// EpistemicStatus split.
//
// Authorization deliberately derives from the work item's own repo via
// work_items, NOT from work_item_team_attributions.repo_id: 5077 of that
// column's 5089 rows are the zero UUID (CHAOS-3785's trap), so scoping on it
// would be meaningless.
func workItemTeamsQuery(ledger *presenceTelemetryLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return queryWorkItemTeams(ctx, client, orgID, cursor, limit, ledger)
	}
}

// workItemTeamsGroupColumns is the GROUP BY of workItemTeamsStatement and the
// column list of its row key: one list, so the two cannot drift.
var workItemTeamsGroupColumns = []string{"toString(w.repo_id)", "ifNull(r.repo, '')", "a.work_item_id", "ifNull(a.team_id, '')"}

var workItemTeamsRowKey = rowKeySQL("toString(w.repo_id)", "a.work_item_id", "ifNull(a.team_id, '')")

// workItemTeamsQualifies is the row-level rule for an edge: a primary or
// co-owner attribution. A pair of an active team with no qualifying row is
// not dropped from the read: it is read to be retracted (CHAOS-9794). A pair
// of an inactive team is not read at all, so a team that is deactivated and
// comes back leaves its edges as they were.
var workItemTeamsQualifies = "(" + devhealthschema.TeamAttributionPredicate("a", devhealthschema.AttributionScopeTeam) + ")"

// workItemTeamsWatermark is the page position: the newest computed_at of the
// group, so a later row of the pair moves the edge past the cursor.
const workItemTeamsWatermark = "max(a.computed_at)"

// workItemTeamsCurrentOrder picks the row that represents one (work item,
// team): the primary row before a co-owner row (the is_primary meaning
// TeamAttributionPredicate fixes), then the latest computed_at (the table is
// ReplacingMergeTree(computed_at)), then source and the attribution repo_id
// for a total order. It is minimised with ONE argMin over the winner's fields
// (workItemTeamsWinner), so the source, confidence and evidence ref always
// come from the same row.
const workItemTeamsCurrentOrder = "tuple(toUInt8(a.is_primary != 1), -toUnixTimestamp64Micro(a.computed_at), toString(a.source), toString(a.repo_id))"

// workItemTeamsWinner is the winning row's (source, confidence, attribution
// repo_id).
var workItemTeamsWinner = "argMinIf(tuple(toString(a.source), toString(a.confidence), toString(a.repo_id)), " + workItemTeamsCurrentOrder + ", " + workItemTeamsQualifies + ")"

// workItemTeamsFromGroupSQL is the row source and GROUP BY of the work item
// team groups: one row per (work item, team). The edge count reads the same
// text.
func workItemTeamsFromGroupSQL() string {
	return `FROM work_item_team_attributions AS a FINAL
INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.org_id = a.org_id
INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = ifNull(a.team_id, '')
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE a.org_id = {org_id:String} AND ifNull(a.team_id, '') != ''
GROUP BY ` + strings.Join(workItemTeamsGroupColumns, ", ")
}

// workItemTeamsStatement reads one row per (work item, team). The table keys
// on source, so a pair can hold a primary row and a co-owner row, or rows of
// two sources, and the edge id is per pair: left apart, they reach the batch
// as one id twice and the batch pass quarantines the second.
func workItemTeamsStatement(cursor cursorState) string {
	return `SELECT a.work_item_id, ifNull(a.team_id, '') AS owning_team_id,
       ` + workItemTeamsWinner + `.1 AS current_source,
       ` + workItemTeamsWinner + `.2 AS current_confidence,
       toString(w.repo_id) AS work_item_repo_id, ifNull(r.repo, '') AS work_item_repo_slug,
       ` + workItemTeamsWatermark + ` AS observed_at,
       ` + workItemTeamsWinner + `.3 AS attribution_repo_id,
       countIf(` + workItemTeamsQualifies + `) AS group_rows
` + workItemTeamsFromGroupSQL() + havingSincePredicate(cursor, workItemTeamsWatermark, workItemTeamsRowKey) + orderBy(workItemTeamsWatermark, workItemTeamsRowKey)
}

func queryWorkItemTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, ledger *presenceTelemetryLedger) ([]candidate, bool, error) {
	return fetch(ctx, client, workItemTeamsStatement(cursor), rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, teamID, source, confidence, repoID, repoSlug, attributionRepoID string
		var observedAt time.Time
		var groupRows uint64
		if err := r.Scan(&workItemID, &teamID, &source, &confidence, &repoID, &repoSlug, &observedAt, &attributionRepoID, &groupRows); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := identity.JoinSegments(repoID, workItemID, teamID)
		workItemCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
		if err != nil {
			return nil, err
		}
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// RETRACTION (CHAOS-9794). The pair has attribution rows but none
		// qualifies (the row left is_primary 1/2), so
		// an edge projected on an EARLIER tick would stay open: incremental
		// application never deletes an absent relationship. The tombstone is
		// the retraction. It is idempotent (a never-projected id matches zero
		// rows) and cannot delete an edge this batch asserts, because a pair
		// emits an edge or a tombstone, never both. Its EffectiveAt is the
		// pair's watermark (the newest computed_at), which is the stamp of the
		// row that demoted it and not older than the edge it retracts.
		if groupRows == 0 {
			ledger.recordWorkItemTeamRetracted()
			tombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind:          "relationship",
				CanonicalID:   workItemTeamRelationshipID(workItemCanonicalID, teamID),
				Reason:        "work item team attribution no longer qualifies: not a primary or co-owner row",
				EffectiveAt:   observedAt,
				SourceVersion: TeamsProjectsSourceVersion,
			}
			return []candidate{{observedAt: observedAt, sortKey: rowSortKey, tombstone: &tombstone}}, nil
		}
		if groupRows > 1 {
			ledger.recordWorkItemTeamCollapsed(int(groupRows - 1))
		}
		derivation, epistemicStatus := workItemTeamAttributionDerivation(source)
		// CHAOS-7252: the ref is the attribution row's full key -- its OWN
		// repo_id (usually the zero UUID; the work item's repository
		// authorizes the edge instead), the work item, the team and the
		// source -- each escaped. Team ids are Atlassian ARIs with ':'.
		workItemTeamRef, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, attributionRepoID, workItemID, teamID, source)
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID:  workItemTeamRelationshipID(workItemCanonicalID, teamID),
			Type:            contractsv1.ContextFabricRelationshipOwnedByTeam,
			From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: workItemCanonicalID, Label: contextfabric.ReadableSourceID(contextfabric.SubjectWorkItem, workItemID)},
			To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(teamID), Label: teamID},
			Properties:      attributionProperties(source, confidence),
			Derivation:      derivation,
			EpistemicStatus: epistemicStatus,
			Authorization:   workItemAuthorization(repoID, repoSlug),
			EvidenceRefIDs:  []string{workItemTeamRef},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}

// queryProjectTeams projects project -> team (OWNED_BY_TEAM) from
// team_project_ownership. Two live findings shape every line of it.
//
// FIRST -- the collapse, and why FINAL is not enough. This table's ORDER BY is
// (org_id, provider, project_id, team_id, source, valid_from), so valid_from
// is part of the ReplacingMergeTree dedup key and FINAL returns 616 rows for
// the ground-truth org's THREE real edges -- one of them alone accounts for
// 608, every window still open. Read row-per-row that is 616 relationship
// candidates carrying 3 distinct relationship IDs;
// ContextFabricProjectionBatch.Validate() rejects the batch outright
// ("relationship IDs must be unique within a batch"), and because a rejected
// batch never advances a checkpoint, the organization's team/project
// projection would wedge permanently on the first tick. The GROUP BY below is
// therefore load-bearing, not an optimization: it is what makes this producer
// correct at all. Verified against live data: 616 rows in, exactly 3 out.
//
// SECOND -- the id-space trap. This table's own project_id column is NOT
// projects.id: for gitlab rows it holds the project KEY
// ('full.chaos/chaos-ops') where projects.id is
// '<org>:gitlab:71133891', and only 1 of 3 distinct values resolves. The
// project_key column joins cleanly instead (3 of 3). So the join is on
// project_key and the projected canonical id comes from the PROJECTS row,
// never from this table. (projects.team_ids is unusable for the same class of
// reason -- it carries the provider's own native team UUID, matching neither
// teams.id nor teams.team_uuid.)
//
// THIRD -- provider scoping (codex round-1 F1). project_key is only unique
// WITHIN a provider, so both the aggregation and the projects join carry
// provider. Without it, two providers' independent ownership assertions merge
// on a shared key and the join fabricates cross-provider project->team edges
// -- asserting an ownership nobody recorded -- and can emit two rows
// resolving to one projects.id, duplicating a RelationshipID and wedging the
// batch. Live today there are zero such collisions (0 keys under >1 provider
// in either table, across every organization), so this is latent rather than
// active; it is guarded because nothing in the schema prevents it and the
// failure mode is silent fabrication, not an error.
//
// The GROUP BY deliberately does NOT include this table's own project_id,
// though it is part of the source's natural key. The projected identity is
// built from projects.id, so two rows differing only in project_id would
// resolve to the SAME projects.id and duplicate a RelationshipID -- the exact
// wedge this producer exists to avoid. Grouping on (provider, project_key)
// instead cannot do that, because (org, provider, project_key) resolves to
// exactly one projects.id (verified across every organization), and no
// information is lost: zero groups map to more than one project_id.
//
// THIRD-B -- ambiguity, the failure mode provider-scoping MOVES rather than
// removes. Grouping on (provider, project_key) and resolving through projects
// assumes (org, provider, project_key) names exactly one project. That holds
// in live data today (verified across every organization), but nothing in the
// schema enforces it, and the failure is invisible where every other duplicate
// is loud: a key resolving to two projects fans this join out to two rows with
// two DISTINCT projects.id, so their RelationshipIDs differ and
// ContextFabricProjectionBatch.Validate never trips. The result would be a
// fabricated ownership edge to a project the source never asserted.
//
// key_resolution_count carries the per-(provider, project_key) project count
// out of SQL, and the scan omits BOTH candidates when it exceeds one. Omitted,
// not guessed: choosing between two equally-matching projects would be minting
// canonical truth from a coin flip. Omitted, not fatal: one ambiguous key must
// not take an organization's whole projection down, so this never returns an
// error -- it drops the edge and logs a bounded reason plus a count
// (logAmbiguousProjectKeys), because an unlogged omission is indistinguishable
// from an ownership that simply does not exist.
//
// FOURTH -- validity, and a ClickHouse NULL trap (codex round-1 F2). The
// window runs from the EARLIEST assertion ever observed to whatever the
// LATEST assertion says, keyed on valid_from. Neither obvious spelling works:
// max(valid_to) reports the largest date rather than the newest assertion, so
// an ownership superseded by an earlier close looks live for months longer;
// and plain argMax(valid_to, valid_from) SKIPS a NULL, so a newer OPEN
// assertion loses to an older closed one and a live ownership reads as ended.
// Both were verified directly against this ClickHouse version.
// argMax(tuple(valid_to), ...).1 preserves the NULL, which is why the tuple
// wrapper is load-bearing rather than decorative -- see
// TestOwnershipWindowTakesTheLatestAssertion, which asserts both directions
// because each spelling passes one and fails the other.
//
// The ordering key is a TUPLE, not valid_from alone (codex round-2 F2):
// valid_from alone leaves two assertions stamped at the same instant
// unordered, so a group holding one open and one closed assertion at that
// instant could project either, flipping with merge order. The key is
// (valid_to IS NULL, valid_from, ifNull(valid_to, epoch)):
//
//   - an OPEN assertion outranks every closed one: a fact is current when an
//     open row exists for its key, whatever later rows closed (ownership
//     writers keep the earliest open row of a fact and close its duplicates,
//     so the newest row of a current fact is routinely the closed one), and a
//     fact that was closed and came back is a new open row;
//   - among assertions of the same kind the latest valid_from wins;
//   - among tied closed assertions, the latest valid_to wins, so even that
//     case is ordered rather than arbitrary.
//
// The open-wins choice is justified semantically, NOT empirically, and that
// distinction is worth stating: live data cannot adjudicate it, because the
// writer has never emitted a closed ownership row at all (0 of 618 rows carry
// a valid_to). Reaching the tie also requires rows differing in project_id --
// the only column inside this table's ORDER BY but outside the GROUP BY,
// which FINAL would otherwise collapse -- and that is realistic exactly
// because project_id here is unreliable (see the id-space note above).
// projectTeamsQuery binds the run-scoped ambiguity ledger to the ownership
// producer. The producer records omissions; the SOURCE logs the run total
// once per batch, so the number an operator sees is the run's distinct-key
// count rather than one page's slice.
func projectTeamsQuery(omissions *ambiguityLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return queryProjectTeams(ctx, client, orgID, cursor, limit, omissions)
	}
}

// rowKeySegmentSQL is identity.EncodeSegment, in SQL.
//
// The escape ORDER is load-bearing and must mirror the Go function exactly:
// '%' first, then ':'. Reversed, a literal "%3A" in the data would decode
// back to ':' and the escape would not be injective -- the very property
// this exists to give.
func rowKeySegmentSQL(column string) string {
	return "replaceAll(replaceAll(" + column + ", '%', '%25'), ':', '%3A')"
}

// rowKeySQL builds a keyset tiebreaker that is INJECTIVE over its components,
// the SQL half of a pair whose Go half is identity.JoinSegments (CHAOS-4635).
//
// WHY THIS IS NOT COSMETIC. A tiebreaker's whole job is to totally order rows
// that share a watermark, and the keyset predicate is a STRICT `>`. If two
// distinct rows produce one key, the second is excluded from the next page
// and is never revisited -- a valid edge silently lost until some later
// watermark change or a rebuild. The previous keys were raw colon joins over
// id spaces that CONTAIN colons (`{org}:gitlab:71133891`, `gl:full.chaos`,
// `linear:CHAOS-3802`), so that was reachable.
//
// queryProjectTeams' key was worse than merely ambiguous: it omitted
// `provider`, which its own GROUP BY includes. Two groups differing only by
// provider collided with no colon trickery at all -- and cross-provider equal
// project ids are a documented property of this data model, not an edge case.
//
// THE HAZARD THIS INTRODUCES, stated so it is not discovered later. The
// keyset predicate compares `toString(<this SQL>) > {after:String}` against a
// string BUILT IN GO, so the two spellings must agree BYTE FOR BYTE or
// pagination silently skips or replays. They are two implementations of one
// definition and cannot be collapsed into one, so they are pinned by
// EXECUTING the SQL against a real server and comparing to the Go value for
// adversarial inputs -- see the SQL/Go agreement test. Never by eyeballing.
func rowKeySQL(columns ...string) string {
	segments := make([]string, len(columns))
	for i, column := range columns {
		segments[i] = rowKeySegmentSQL(column)
	}
	return "concat(" + strings.Join(segments, ", ':', ") + ")"
}

// projectTeamRelationshipID is the ONE definition of an OWNED_BY_TEAM
// project<->team edge's identity, used by the assertion and by the
// retraction tombstone that removes it.
//
// It is a function and not two string concatenations on purpose. A
// tombstone whose canonical id does not match the projected edge's
// relationship id BYTE FOR BYTE deletes nothing, reports success, and the
// stale edge survives -- a silent no-op that every count, log and receipt
// in this pipeline would report as a successful retraction. The two
// spellings drifting apart is the single failure mode that turns this
// whole change into decoration, so they are not allowed to be two
// spellings.
//
// CHAOS-4635: a DIGEST over the endpoints' own canonical ids, not a raw
// colon join of the source values. The previous form was
// `"relationship:project_team:" + provider + ":" + projectID + ":" + teamID
// + ":" + source`, and the delimiter was not a delimiter -- `projects.id` is
// routinely `{org}:gitlab:71133891` and team ids are routinely
// `gl:full.chaos`, so two different ownership facts could produce one id:
//
//	project "project:team" + team "source"      -> ...:github:project:team:source:native
//	project "project"      + team "team:source" -> ...:github:project:team:source:native
//
// Two ASSERTIONS colliding are rejected as a duplicate RelationshipID, and a
// rejected batch never advances a checkpoint -- the organization's projection
// wedges PERMANENTLY. An assertion colliding with a TOMBSTONE used to pass
// validation outright (they are checked in separate passes) and, because
// falkorgraph applies tombstones after relationships, silently deleted a
// valid, still-asserted edge. CHAOS-4565's contract guard turned the second
// case into the first -- loud instead of silent -- and this removes the cause
// underneath both.
//
// identity.DeriveRelationship, not a fourth hand-rolled encoding: it is the
// scheme queryWorkItemDependencies and queryWorkItemHierarchy already use for
// exactly this defect (CHAOS-3898 §1.5), and it is injective because
// JoinSegments ESCAPES ':' inside each component before joining, so no value
// can introduce a separator the reader would honour.
//
// `provider` leaves the argument list but not the identity: it is already
// inside projectCanonicalID, which identity.Derive builds from
// (provider, projectID). Taking the CANONICAL id rather than the raw one also
// makes the assert/retract agreement structural -- both call sites now pass
// the same already-derived value, so they cannot disagree about how a project
// is named.
//
// `source` is the row's own discriminator and is deliberately IN the digest:
// two ownership assertions for one project and team from different sources
// are different assertions, the same reason a work_item_dependency's
// relationship_type distinguishes "blocks" from "relates_to" between one
// endpoint pair.
func projectTeamRelationshipID(projectCanonicalID, teamID, source string) string {
	return identity.DeriveRelationship(identity.RelationshipFamilyProjectTeam, projectCanonicalID, teamCanonicalID(teamID), source)
}

// workItemTeamRelationshipID is the work_item<->team OWNED_BY_TEAM edge's
// identity (CHAOS-4635), on the same scheme and for the same reason as its
// project<->team sibling: the pre-v2 form joined repo id, work item id and
// team id with colons over values that carry colons (`linear:CHAOS-3802`,
// `gl:full.chaos`).
//
// `repoID` is not an argument because it is already inside
// workItemCanonicalID (identity.Derive over {repoID, workItemID}) -- the same
// reason `provider` left the project<->team signature.
//
// The type slot is EMPTY, deliberately. The pre-v2 id carried no
// discriminator beyond the endpoint pair, so this producer already guarantees
// at most one attribution per (repo, work item, team); passing the
// attribution source here would SPLIT ids that are one edge today and strand
// every existing one. A fixed empty slot preserves exactly the current
// grain -- widening it is a behaviour change, not a rename.
func workItemTeamRelationshipID(workItemCanonicalID, teamID string) string {
	return identity.DeriveRelationship(identity.RelationshipFamilyWorkItemTeam, workItemCanonicalID, teamCanonicalID(teamID), "")
}

// repositoryTeamRelationshipID is the ONE definition of an OWNED_BY_TEAM
// repository<->team edge's identity (CHAOS-6561), on the same digest scheme
// and for the same reason as projectTeamRelationshipID: the assertion and
// any later retraction must spell it byte for byte alike, and team ids carry
// colons (`gl:full.chaos`).
//
// The type slot carries BOTH the provider and the ownership row's source,
// joined through identity.JoinSegments so the pair stays injective. source is
// there for the reason project_team carries it: two assertions from different
// sources are different assertions. provider is there because, unlike a
// project's canonical id, `repository:<repos.id>` does not carry it, and
// queryRepositoryTeams groups by provider (team_repo_ownership's own ORDER BY
// leads with it) -- two groups differing only by provider would otherwise
// mint one id, and a duplicate RelationshipID rejects the batch and wedges
// the organization's projection.
func repositoryTeamRelationshipID(repositoryCanonicalID, teamID, provider, source string) string {
	return identity.DeriveRelationship(identity.RelationshipFamilyRepositoryTeam, repositoryCanonicalID, teamCanonicalID(teamID), identity.JoinSegments(provider, source))
}

// repositoryCanonicalID is the repository subject's canonical id, the exact
// shape queryRepositories (tables.go) mints for the node, so this edge's From
// endpoint lands on the projected repository rather than a stub no entity
// write ever fills. Both read contextfabric.RepositoryCanonicalID (CHAOS-12230).
func repositoryCanonicalID(repoID string) string { return contextfabric.RepositoryCanonicalID(repoID) }

// projectMembershipRelationshipID is the subject<->project BELONGS_TO_PROJECT
// edge's identity (CHAOS-4635), covering both the work-item and pull-request
// arms.
//
// ONE family for both, where the pre-v2 ids used separate
// `work_item_project:`/`pull_request_project:` prefixes: the subject's own
// canonical id already carries its kind (`work_item.v2:...` versus
// `pull_request:...`), so the digest separates the arms without a second
// discriminator. Encoding the same fact twice would only give the two
// spellings a chance to disagree.
//
// intervalSuffix is CHAOS-4109's per-interval discriminator, and it goes in
// the DIGEST rather than being appended to the id. That is the whole point:
// appended, it was one more unescaped colon-joined component
// (`:<RFC3339Nano>:<eventID>`); in the digest it still makes a transition
// interval distinct from every other interval for the same (subject, project)
// pair, which is exactly what it exists to do. work_item_column rows pass the
// empty string and keep a single id per pair, unchanged.
func projectMembershipRelationshipID(subjectCanonicalID, projectCanonicalID, intervalSuffix string) string {
	return identity.DeriveRelationship(identity.RelationshipFamilyProjectMembership, subjectCanonicalID, projectCanonicalID, intervalSuffix)
}

func queryProjectTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, omissions *ambiguityLedger) ([]candidate, bool, error) {
	return fetchIngest(ctx, client, ownershipGroupsPageStatement(cursor), rowLimitBindings(orgID, cursor, limit), limit, scanProjectTeamGroup(omissions))
}

// scanProjectTeamGroup turns one stored ownership group row into its
// candidate: the group's columns, its row key, and live (0 for a log row that
// is no longer its group's current row or whose group is gone: a progress
// candidate, nothing written).
func scanProjectTeamGroup(omissions *ambiguityLedger) func(contextpacket.ClickHouseRowScanner) ([]candidate, error) {
	return func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var projectID, teamID, source, provider, rowSortKey string
		var validFrom, latestValidTo, observedAt time.Time
		var latestIsOpen, edgeSuppressed, conflictingIdentityPresent, live uint8
		var conflictIdentities []string
		if err := r.Scan(&projectID, &teamID, &source, &validFrom, &latestIsOpen, &latestValidTo, &observedAt, &provider, &edgeSuppressed, &conflictIdentities, &conflictingIdentityPresent, &rowSortKey, &live); err != nil {
			return nil, err
		}
		if live == 0 {
			return []candidate{progressCandidate(time.Time{}, rowSortKey)}, nil
		}
		observedAt, validFrom, latestValidTo = observedAt.UTC(), validFrom.UTC(), latestValidTo.UTC()
		// FAIL CLOSED: this ownership row's project_id and project_key
		// resolve to DIFFERENT projects, so at most one of the two edges it
		// produces is real and nothing here can say which. Emit neither, and
		// record it -- a suppressed fabrication an operator never hears about
		// is only half a fix. Still a PROGRESS candidate: the row was
		// consumed and spent page budget, so the cursor must advance past it
		// or a page of conflicts stalls the walk forever.
		// Recording and suppressing are INDEPENDENT (team-lead review). A
		// conflicting row is recorded even when a CLEAN row in the same group
		// keeps the edge alive: "was an ownership dropped" is not the same
		// question as "did this edge survive", and answering only the second
		// hides the first.
		{
			// Keyed on the OWNERSHIP row, not the resolved edge (codex R3).
			// One conflicting row produces two flagged result rows -- one per
			// resolved project -- and rowSortKey carries the resolved id, so
			// keying on it counted a single disagreeing source row twice and
			// reported double the suppressions that happened.
			//
			// The full SET, not a representative. max() would have named one
			// ownership row per group, and a group can hold several: two rows
			// sharing a team, a source and a project_id but disagreeing via
			// DIFFERENT keys both land here, and max() would have counted
			// them as one. Over-reporting and under-reporting are the same
			// defect wearing opposite signs, and this ticket has now shipped
			// both.
			for _, conflicted := range conflictIdentities {
				omissions.addConflict(provider, conflicted)
			}
		}
		// Suppressed only when NO clean row asserted this edge. The group is
		// (project_id, provider, team_id, source_name), so a clean row can
		// share it with a conflicting one; suppressing the GROUP would drop
		// the clean row's legitimate edge -- a missing edge, the exact class
		// this ticket exists to remove, reintroduced by the guard against
		// fabricating one.
		// REPRESENTABILITY FIRST, and the ordering is load-bearing
		// (CHAOS-4565). This check used to sit below the suppression branch,
		// where it only ever guarded the assertion. Now that a suppressed
		// group EMITS something, it has to guard both -- and it is the same
		// question for both: a project this producer cannot represent was
		// never projected, so there is no edge to retract, and a retraction
		// is not merely useless but actively dangerous.
		//
		// The danger is specific. Both the relationship id and the tombstone's
		// canonical id embed the raw project id, and the v1 contract bounds
		// both at 256 characters. An oversized id therefore fails
		// ContextFabricProjectionTombstone.Validate(), which rejects the WHOLE
		// batch, and a rejected batch never advances a checkpoint -- the
		// organization's projection wedges PERMANENTLY. That is not
		// theoretical: leaving this check below the suppression branch broke
		// two long-standing subtests
		// ("ambiguous rows do not stall pagination", "omitted rows beyond the
		// skip bound still converge") with
		// `tombstones: projection tombstone violates v1 bounds`, on the very
		// run that proved the retraction works. Emitting a progress candidate
		// here keeps the row consuming page budget exactly as it did before,
		// so the walk still advances past it.
		projectCanonicalID, projectOmitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil {
			return nil, err
		}
		if projectOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		// RETRACTION (CHAOS-4565). This group has no row that can assert
		// the edge -- every contributing ownership row is either an
		// identity conflict or an ambiguous-key retraction row. Emitting a
		// bare progress candidate here, which is what this branch used to
		// do, leaves an edge projected on an EARLIER tick live forever:
		// incremental application never deletes an absent relationship, so
		// omission and retraction are indistinguishable to the graph.
		//
		// A relationship tombstone is the retraction, and it is idempotent
		// by construction -- applyTombstone's DELETE matches zero rows
		// against a relationship_id that was never projected, so the
		// never-projected case and the re-run case are the same no-op and
		// need no bookkeeping to tell apart. It is also ordering-safe in
		// both directions: a tombstone arriving before the edge was ever
		// projected deletes nothing and the group never asserts, so no edge
		// appears afterwards either.
		//
		// It CANNOT delete an edge this same batch asserts. The tombstone's
		// canonical id is projectTeamRelationshipID over exactly the GROUP
		// KEY (provider, project_id, team_id, source_name), and a group
		// emits an edge or a tombstone, never both -- edge_suppressed is
		// false the moment ONE asserting row shares the group. That matters
		// because falkorgraph applies tombstones AFTER relationships, so a
		// batch holding both for one id would delete the edge it had just
		// written.
		//
		// Still a PROGRESS candidate in the pagination sense: a tombstone
		// candidate carries the row's (observedAt, sortKey), so the cursor
		// advances past this group exactly as it did before.
		if edgeSuppressed == 1 {
			reason := retractionReasonAmbiguousKey
			if conflictingIdentityPresent == 1 {
				reason = retractionReasonConflictingIdentity
			}
			omissions.addRetraction(reason)
			tombstone := contractsv1.ContextFabricProjectionTombstone{
				Kind:          "relationship",
				CanonicalID:   projectTeamRelationshipID(projectCanonicalID, teamID, source),
				Reason:        retractionTombstoneReason(reason),
				EffectiveAt:   observedAt,
				SourceVersion: TeamsProjectsSourceVersion,
			}
			return []candidate{{observedAt: observedAt, sortKey: rowSortKey, tombstone: &tombstone}}, nil
		}
		// No ambiguity branch here on purpose. Ambiguity is a property of the
		// KEY arm, and that arm now excludes an ambiguous key in SQL
		// (ProjectIdentityJoinSQL emits a key scope row only for
		// key_resolution_count = 1), so every row reaching this scan is a
		// resolved match. The scan-side guard that used to live here read a
		// PROJECT-level count off a per-scope row and discarded unambiguous
		// id matches with it -- CHAOS-4542 class 4. Keeping it as a
		// belt-and-braces check would be worse than useless: it can no
		// longer fire, and a guard that cannot fire is a false claim that
		// something is being guarded. recordAmbiguousProjectKeys carries the
		// telemetry instead.
		// CHAOS-7252: the ref names the ownership GROUP this edge is --
		// (provider, projects.id, team, source), each escaped. The source is
		// part of it: two sources are two edges (projectTeamRelationshipID).
		projectTeamRef, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityProjectTeamV2, provider, projectID, teamID, source)
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: projectTeamRelationshipID(projectCanonicalID, teamID, source),
			Type:           contractsv1.ContextFabricRelationshipOwnedByTeam,
			From:           contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: projectCanonicalID, Label: projectID},
			To:             contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(teamID), Label: teamID},
			Properties:     attributionProperties(source, ""),
			// Same reasoning as queryWorkItemTeams: an ownership row's source
			// enum spans native through inferred/manual, so this is Ops
			// asserting an ownership, not a canonical structural column.
			Derivation:      contractsv1.ContextFabricDerivationRuleInferred,
			EpistemicStatus: contractsv1.ContextFabricEpistemicSourceAsserted,
			Authorization:   contractsv1.ContextFabricAuthorizationScope{ProjectIDs: []string{projectID}, TeamIDs: []string{teamID}},
			EvidenceRefIDs:  []string{projectTeamRef},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
		}
		relationship.ValidFrom, relationship.ValidTo = ownershipValidity(validFrom, latestIsOpen, latestValidTo)
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	}
}

// queryRepositoryTeams projects repository -> team (OWNED_BY_TEAM) from
// team_repo_ownership (CHAOS-6561). Ownership rows are the ONLY source of
// team attribution this platform admits (AGENTS.md; CHAOS-4321: ownership,
// never membership), and queryTeams already reads this very table for a
// team's authorization scope -- but until this producer nothing projected it
// as an edge, so the graph could not walk from a repository to the team that
// owns it.
//
// Modelled on queryProjectTeams, and the same three live findings apply:
//
//   - THE COLLAPSE. ReplacingMergeTree(updated_at) ORDER BY (org_id,
//     provider, repo_full_name, team_id, source, valid_from): valid_from is
//     in the dedup key, so FINAL keeps one row PER ASSERTION. The GROUP BY is
//     what makes one edge per (provider, repository, team, source), and
//     without it a duplicate RelationshipID rejects the batch and wedges the
//     organization's projection.
//   - THE VALIDITY RULE. The window runs from the earliest assertion to what
//     the winning assertion says, ordered by (valid_to IS NULL, valid_from,
//     valid_to), an open assertion first, with the argMax(tuple(valid_to)) NULL-preserving spelling --
//     see queryProjectTeams' FOURTH note, verified there against this
//     ClickHouse version. A closed latest assertion ENDS the edge (ValidTo),
//     exactly as ownershipValidity ends a project->team edge. It is history,
//     not a retraction: the CHAOS-4565 tombstone is reserved for an ownership
//     that can no longer be SUBSTANTIATED, and a closed window is fully
//     substantiated -- it is the record of when the team owned the repo.
//     Tombstoning it would erase the history the CHAOS-3781 temporal axis
//     exists to answer over (applyActiveValidity's same argument).
//   - THE WATERMARK covers every row in the group, open and closed alike, so
//     a revocation advances the cursor as reliably as a grant (the CHAOS-4390
//     round-2 finding recorded on ownedRepositoriesJoinSQL).
//
// Grouped on repo_id, the projected identity -- not repo_full_name, which is
// a display value that can be renamed under a stable repo_id. The latest
// assertion's repo_full_name is still carried out for the label.
//
// NULL repo_id (the column is Nullable(UUID)) -- CHAOS-7119, K11 edge half.
// Such a row is RESOLVED BY NAME with the one rule the fact reads use
// (ownershipresolve, a port of ops teamscope RepoCondition): a (provider,
// lower-cased repo) match in repos for this org makes it an ordinary
// repository -> team edge to that repository, exactly as if the row had
// carried the repo_id. A row's own repo_id always wins over its name. This
// replaces the CHAOS-6561 rule that omitted every such row: the GitHub
// team-autoimport writer leaves repo_id NULL on ALL its rows, so omission
// meant those teams had no repository edge at all. Provider stays in the
// match (a full name is not unique across providers), and the join's
// `matched` sentinel -- never `r.id IS NOT NULL`, which a ClickHouse LEFT
// JOIN fills with the zero UUID -- decides whether a name resolved. A row
// that still resolves to nothing (a glob pattern, a ghost name, a provider
// mismatch) is OMITTED, never guessed; it keeps an empty repo_key so it is
// counted in the run's repositoryOwnershipLedger and logged, and still moves
// the cursor. Unresolved rows collapse per (provider, team, source), so the
// count is per group, not per repository name.
//
// THE GROUP KEY. The group is (provider, resolved repo_key, team, source),
// nothing else. The CHAOS-6561 key also carried null_repo_name; it is gone
// from the SQL, the GROUP BY, the row key and the Go scan TOGETHER, because a
// name-resolved row and an id row for the same (repository, team, source)
// are the same edge: split into two groups they would emit two relationships
// with the SAME RelationshipID, and a duplicate id rejects the batch and
// wedges the organization.
//
// A repo_id with NO repos row still projects (CHAOS-7119 keeps this; the
// fact reads drop such a row, as ops does -- DEFAULT pending a ruling): the
// repository endpoint is the
// deterministic `repository:<repo_id>` id queryRepositories mints, so the
// edge lands on the right node whenever the repository is projected. Its
// authorization cannot name a slug, so it fails CLOSED to
// orphanedRepositorySentinel -- the pull_request precedent in
// querySubjectProjectMemberships -- and is counted.
//
// THE REPOS SIDE OF THE WATERMARK (codex P1 on this PR, reproduced live). The
// slug comes from the LEFT JOINed repos row, so the edge's scope depends on a
// table the ownership row's updated_at knows nothing about. With a watermark
// over team_repo_ownership alone, a repos row arriving AFTER the first
// projection never moved the group past the cursor: the edge kept its
// orphan-sentinel scope until the ownership row changed or a rebuild, and
// repository-scoped graph reads failed closed on it. So the watermark is
// greatest(max(o.updated_at), max(o.repo_synced_at)) -- the repos row's
// last_synced (repos is ReplacingMergeTree(last_synced); it has no
// updated_at) folded in exactly as queryProjectTeams folds project_updated_at
// and queryTeams folds its joined ownership rows
// (queryTeamsEffectiveUpdatedAtExpr). A missing repos row
// contributes the epoch, so the pre-arrival watermark is unchanged. The edge's
// ObservedAt is that same watermark, i.e. the later of the two facts it was
// built from. TRADE-OFF: every repos re-sync moves last_synced and therefore
// re-emits that repository's ownership edges. That is accepted: the edges are
// idempotent MERGEs, and queryRepositories already re-emits the repository
// node on every last_synced move, so this adds edges per re-synced repository,
// not a new re-emission class.
//
// Teams are INNER JOINed exactly as queryProjectTeams does: an edge to a team
// with no teams row would name a node no producer writes.
//
// Provenance: attribution_source (the row's own source enum), match_type,
// is_primary, specificity and priority, all from the latest assertion.
// team_repo_ownership has NO confidence column, so no attribution_confidence
// property is emitted -- inventing one would be the overstatement CHAOS-4101
// removed.
func repositoryTeamsQuery(ledger *repositoryOwnershipLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return queryRepositoryTeams(ctx, client, orgID, cursor, limit, ledger)
	}
}

// repositoryTeamsWatermark / repositoryTeamsRowKey are queryRepositoryTeams'
// pagination pair, shared by the SELECT, HAVING and ORDER BY so they cannot
// drift. The watermark folds in the repos row's last_synced (see THE REPOS
// SIDE OF THE WATERMARK above). The row key names every GROUP BY column (CHAOS-4635: a key missing a
// grouped column lets two groups tie and the strict `>` drop one).
const repositoryTeamsWatermark = "greatest(max(o.updated_at), max(o.repo_synced_at))"

var repositoryTeamsRowKey = rowKeySQL(repositoryTeamsGroupColumns...)

// repositoryTeamsGroupColumns is the GROUP BY of repositoryTeamsStatement and
// the column list of its row key -- one list, so the two cannot drift
// (CHAOS-7119 removed null_repo_name from both at once).
var repositoryTeamsGroupColumns = []string{"o.provider", "o.repo_key", "o.team_id", "o.source_name"}

// repositoryTeamsOwnershipSource is the resolved ownership table the edge
// reads: the K11 rule from ownershipresolve in its KeepUnresolved mode, with
// the provenance, validity and watermark columns the edge carries.
//
// CHAOS-7130: the "currently owned" arm (valid_from <= now64(3)) lives HERE,
// in the ONE source both the edge and the team authorization list read, so a
// future-dated assertion is ignored identically by both (before v15 the edge
// read it unfiltered while the list filtered it: codex #733 r3).
var repositoryTeamsOwnershipSource = ownershipresolve.OwnedRepositoriesSource(" AND valid_from <= now64(3)", ownershipresolve.Options{
	OwnershipColumns: []string{"source", "match_type", "is_primary", "specificity", "priority", "valid_from", "valid_to", "updated_at"},
	Columns: []string{
		"o.org_id AS org_id", "o.provider AS provider", "o.source AS source", "o.match_type AS match_type",
		"o.is_primary AS is_primary", "o.specificity AS specificity", "o.priority AS priority",
		"o.valid_from AS valid_from", "o.valid_to AS valid_to", "o.updated_at AS updated_at",
	},
	KeepUnresolved: true,
})

// ownershipFactOrder is the ONE ordering that collapses the rows of an
// ownership fact (a project/repository, team and source) to the row that
// represents it. Every collapse of team_*_ownership rows uses it, so the rule
// cannot drift between the edge builders and the team authorization list:
//
//   - a fact is current when an OPEN row (valid_to IS NULL) exists, whatever
//     later rows closed: the ownership writers keep the earliest open row and
//     close later duplicates, so the newest row of a current fact is routinely
//     the closed one;
//   - among open rows the EARLIEST valid_from wins (first seen), because a
//     duplicate is closed only on a run with proof of its end and two open rows
//     can coexist;
//   - with no open row the LATEST valid_to wins (the fact ended at its last
//     close), then the latest valid_from;
//   - a fact that was closed and came back is a new open row, so it wins.
//
// The columns are DateTime64(3): the key is built from toUnixTimestamp64Micro,
// never from an integer cast, which would drop the fractional second and let
// rows within one second tie.
const ownershipFactOrder = "(o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))"

// repositoryTeamsLatestOrder is the ordering key of the repository edge's
// collapse: ownershipFactOrder, the same as queryProjectTeams' and
// ownedRepositoriesJoinSQL's.
const repositoryTeamsLatestOrder = ownershipFactOrder

// repositoryTeamsStatement. Inner column aliases never reuse a source column's
// own name (match_type_name, not match_type; repo_synced_at, not
// last_synced): an alias that shadows the
// column it reads bound to itself on 24.8 once already in this file's history.
func repositoryTeamsStatement(cursor cursorState) string {
	return repositoryTeamsGroupedSQL() + havingSincePredicate(cursor, repositoryTeamsWatermark, repositoryTeamsRowKey) + orderBy(repositoryTeamsWatermark, repositoryTeamsRowKey)
}

// repositoryTeamsGroupedSQL is the per-(provider, repo, team, source) latest
// assertion rows, WITHOUT cursor/paging: the ONE derivation. The edge pages it
// (repositoryTeamsStatement) and the team authorization list aggregates it per
// team (ownedRepositoriesJoinSQL) -- same ownership source, same filters, same
// latest order, same open predicate -- so the two can never disagree on what
// is currently owned (CHAOS-7130; codex #733 r1-r3 found three separate
// disagreements between two hand-written derivations).
func repositoryTeamsGroupedSQL() string {
	return `SELECT o.repo_key,
       argMax(o.repo_full_name, ` + repositoryTeamsLatestOrder + `) AS latest_repo_full_name,
       max(o.repo_slug) AS resolved_repo_slug,
       o.team_id AS owning_team_id, o.source_name, o.provider,
       argMax(o.match_type_name, ` + repositoryTeamsLatestOrder + `) AS latest_match_type,
       argMax(o.is_primary_flag, ` + repositoryTeamsLatestOrder + `) AS latest_is_primary,
       argMax(o.specificity_value, ` + repositoryTeamsLatestOrder + `) AS latest_specificity,
       argMax(o.priority_value, ` + repositoryTeamsLatestOrder + `) AS latest_priority,
       min(o.valid_from) AS first_valid_from,
       argMax(tuple(o.valid_to), ` + repositoryTeamsLatestOrder + `).1 IS NULL AS latest_is_open,
       ifNull(argMax(tuple(o.valid_to), ` + repositoryTeamsLatestOrder + `).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
       ` + repositoryTeamsWatermark + ` AS observed_at
` + repositoryTeamsFromGroupSQL()
}

// repositoryTeamsFromGroupSQL is the row source and GROUP BY of the repository
// ownership groups: one row per (provider, repository, team, source). The edge
// count reads the same text.
func repositoryTeamsFromGroupSQL() string {
	return `FROM (
	SELECT rto.provider AS provider, rto.repo_key AS repo_key,
	       rto.repo_full_name AS repo_full_name, ifNull(r.repo, '') AS repo_slug,
	       rto.team_id AS team_id, toString(rto.source) AS source_name,
	       toString(rto.match_type) AS match_type_name, toUInt8(rto.is_primary) AS is_primary_flag,
	       toInt64(rto.specificity) AS specificity_value, toInt64(rto.priority) AS priority_value,
	       rto.valid_from AS valid_from, rto.valid_to AS valid_to, rto.updated_at AS updated_at,
	       ifNull(r.last_synced, toDateTime64(0, 3, 'UTC')) AS repo_synced_at
	FROM ` + repositoryTeamsOwnershipSource + ` AS rto
	LEFT JOIN repos AS r FINAL ON toString(r.id) = rto.repo_key AND r.org_id = rto.org_id
	INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = rto.team_id
	WHERE rto.org_id = {org_id:String}
) AS o
GROUP BY ` + strings.Join(repositoryTeamsGroupColumns, ", ")
}

// repositoryOwnershipSourceAsserted is the closed set of team_repo_ownership
// sources whose claim some provider or person actually ASSERTED. 'inferred'
// is the Python team_autoimport heuristic, so it is not in it; and any value
// this set does not know FAILS TOWARD the weaker classification, the
// CHAOS-4101 rule workItemTeamAttributionDerivation already applies.
var repositoryOwnershipSourceAsserted = map[string]bool{"native": true, "jira_legacy": true, "provider_access": true, "manual": true}

// repositoryTeamOwnershipDerivation: Derivation is always RuleInferred (the
// row is Ops' own ownership record, not a canonical structural column --
// queryProjectTeams' reasoning). EpistemicStatus is SourceAsserted only for
// an asserted source matched EXACTLY; a 'pattern' match is a glob Ops applied,
// not an assertion about this repository, so it is Inferred whatever its
// source.
func repositoryTeamOwnershipDerivation(source, matchType string) (contractsv1.ContextFabricDerivationMethod, contractsv1.ContextFabricEpistemicStatus) {
	if repositoryOwnershipSourceAsserted[source] && matchType == "exact" {
		return contractsv1.ContextFabricDerivationRuleInferred, contractsv1.ContextFabricEpistemicSourceAsserted
	}
	return contractsv1.ContextFabricDerivationRuleInferred, contractsv1.ContextFabricEpistemicInferred
}

func queryRepositoryTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, ledger *repositoryOwnershipLedger) ([]candidate, bool, error) {
	return fetch(ctx, client, repositoryTeamsStatement(cursor), rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var repoKey, repoFullName, repoSlug, teamID, source, provider, matchType string
		var isPrimary, latestIsOpen uint8
		var specificity, priority int64
		var validFrom, latestValidTo, observedAt time.Time
		if err := r.Scan(&repoKey, &repoFullName, &repoSlug, &teamID, &source, &provider, &matchType,
			&isPrimary, &specificity, &priority, &validFrom, &latestIsOpen, &latestValidTo, &observedAt); err != nil {
			return nil, err
		}
		observedAt, validFrom, latestValidTo = observedAt.UTC(), validFrom.UTC(), latestValidTo.UTC()
		// The Go half of repositoryTeamsRowKey, same component order.
		rowSortKey := identity.JoinSegments(provider, repoKey, teamID, source)
		if repoKey == "" {
			// NULL repo_id that no repos row matches by (provider, name): no
			// repository node to point at. Omitted, never guessed; still a
			// PROGRESS candidate so the cursor moves past it.
			ledger.recordUnresolved(provider, teamID, source)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		authorization := contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{repoSlug}, TeamIDs: []string{teamID}}
		if repoSlug == "" {
			authorization.RepositorySlugs = []string{orphanedRepositorySentinel}
			ledger.recordOrphanedRepository()
		}
		label := repoFullName
		if repoSlug != "" {
			label = repoSlug
		}
		properties := attributionProperties(source, "")
		if matchType != "" {
			properties["match_type"] = stringScalar(matchType)
		}
		properties["is_primary"] = boolScalar(isPrimary != 0)
		properties["specificity"] = intScalar(specificity)
		properties["priority"] = intScalar(priority)
		derivation, epistemicStatus := repositoryTeamOwnershipDerivation(source, matchType)
		fromCanonicalID := repositoryCanonicalID(repoKey)
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID:  repositoryTeamRelationshipID(fromCanonicalID, teamID, provider, source),
			Type:            contractsv1.ContextFabricRelationshipOwnedByTeam,
			From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: fromCanonicalID, Label: label},
			To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(teamID), Label: teamID},
			Properties:      properties,
			Derivation:      derivation,
			EpistemicStatus: epistemicStatus,
			Authorization:   authorization,
			// Existing entity types only: the ownership fact is evidenced by
			// its two endpoints. A dedicated repository_team evidence type
			// would widen the v1 contract.
			EvidenceRefIDs: []string{
				contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, repoKey),
				contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID),
			},
			ObservedAt:    observedAt,
			SourceVersion: TeamsProjectsSourceVersion,
		}
		relationship.ValidFrom, relationship.ValidTo = ownershipValidity(validFrom, latestIsOpen, latestValidTo)
		ledger.recordAsserted(latestIsOpen != 0, epistemicStatus == contractsv1.ContextFabricEpistemicInferred)
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}

// ambiguousProjectKeysInCatalogStatement counts (provider, project_key)
// pairs that name MORE THAN ONE project in this organization's catalog.
//
// It is a FACT ABOUT THE CATALOG, and the log field says so:
// ambiguous_project_keys_in_catalog. It is deliberately NOT a claim about
// dropped edges, and the difference is the whole reason this replaced what
// was here before.
//
// What was here before tried to reconstruct, from aggregate SQL, which
// individual ownership rows had been eliminated -- membership tests against
// the scope catalog, per-ref attribution, a bound on the reconstruction. It
// was wrong in four consecutive review rounds, in both directions: it
// over-reported omissions that never happened, missed omissions that did,
// double-counted one disagreement as two, and claimed truncation at exactly
// its limit. Not bad arithmetic four times -- a component asked to recover
// per-row facts from an aggregation that had already destroyed them.
// CHAOS-4542's follow-up carries that work; this reports only what a plain
// catalog query can actually know.
//
// No ownership join, no scope expansion, no reconstruction. An operator
// reading this learns "this organization has N ambiguous keys", which is
// true, useful, and checkable -- and learns nothing false about edges.
const ambiguousProjectKeysInCatalogStatement = `SELECT count() AS ambiguous_keys
FROM (
	SELECT provider, project_key
	FROM (
		SELECT provider, ifNull(project_key, '') AS project_key
		FROM projects FINAL
		WHERE org_id = {org_id:String}
	)
	WHERE project_key != ''
	GROUP BY provider, project_key
	HAVING count() > 1
	LIMIT {census_limit:UInt32}
)`

// ambiguousProjectKeysCensusLimit bounds the catalog count so a pathological
// tenant cannot make this grow without limit. Reached means the number is a
// floor; the log says so rather than presenting a capped value as a total.
const ambiguousProjectKeysCensusLimit = 500

// ambiguityCensusTable is this read's label in the bounded classification and
// in its log line. It names the QUESTION the read answers rather than a
// physical table: an operator debugging a held checkpoint needs to know which
// read failed, and "projects" would not distinguish it from the producer's own.
const ambiguityCensusTable = "ambiguous project key catalog count"

// countAmbiguousProjectKeysInCatalog logs the catalog fact once per
// NextProjectionBatch. It reports nothing when the catalog is clean: zero
// ambiguous keys is the normal state and a line per batch saying so is noise,
// not signal.
func countAmbiguousProjectKeysInCatalog(ctx context.Context, client contextpacket.ClickHouseQueryClient, logger *slog.Logger, orgID string) error {
	if client == nil || logger == nil {
		return nil
	}
	rows, err := client.Query(ctx, ambiguousProjectKeysInCatalogStatement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "census_limit", Value: uint32(ambiguousProjectKeysCensusLimit)},
	})
	if err != nil {
		return err
	}
	defer rows.Close()
	var ambiguous uint64
	if rows.Next() {
		if err := rows.Scan(&ambiguous); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if ambiguous == 0 {
		return nil
	}
	logger.WarnContext(ctx, "devhealthsource organization catalog holds ambiguous project keys",
		"org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "source", TeamsProjectsSourceName,
		"reason", "a project_key naming more than one project cannot resolve an ownership row that carries only that key",
		"ambiguous_project_keys_in_catalog", ambiguous,
		"count_bounded_at", ambiguousProjectKeysCensusLimit)
	return nil
}

// ownershipValidity states an ownership edge's end and never its start.
// team_*_ownership.valid_from is the sync stamp of the assertion, not the
// moment ownership began, so projecting it as the edge's start would hide a
// synced ownership from every read of a window that ended before the stamp.
// The start is therefore explicitly absent (CHAOS-3785 R3-1: asserted nil, not
// left stale); the end follows the LATEST assertion -- open if that assertion
// left it open, otherwise its valid_to.
func ownershipValidity(_ time.Time, latestIsOpen uint8, latestValidTo time.Time) (*time.Time, *time.Time) {
	if latestIsOpen != 0 {
		return nil, nil
	}
	to := latestValidTo
	return nil, &to
}

// retractionReason is the CLOSED vocabulary of why an OWNED_BY_TEAM edge
// was retracted (CHAOS-4565). Closed because it is log-field and
// operator-facing: an open string would let a future branch invent a
// synonym for a cause that already has a name, and a reason vocabulary an
// operator cannot enumerate is not a vocabulary.
//
// The two members are the two -- and today the only two -- ways
// queryProjectTeams decides it cannot substantiate an ownership:
// a project_key naming more than one project, and a single ownership row
// whose project_id and project_key resolve to different projects.
type retractionReason string

const (
	retractionReasonAmbiguousKey        retractionReason = "ambiguous_key"
	retractionReasonConflictingIdentity retractionReason = "conflicting_identity"
)

// retractionTombstoneReason is the human sentence carried on the tombstone
// itself, one per closed reason.
//
// It is a switch over the enum with no default that guesses: a reason this
// function does not know is a programming error, and saying so in the
// tombstone is better than shipping an empty Reason -- which
// ContextFabricProjectionTombstone.Validate() rejects, wedging the batch
// on a string bug rather than a data one.
func retractionTombstoneReason(reason retractionReason) string {
	switch reason {
	case retractionReasonAmbiguousKey:
		return "ownership suppressed: project_key names more than one project, so this ownership can no longer be substantiated"
	case retractionReasonConflictingIdentity:
		return "ownership suppressed: the ownership row's project_id and project_key resolve to different projects"
	}
	return "ownership suppressed: unrecognised retraction reason " + string(reason)
}

// logRetractions reports OWNED_BY_TEAM edges WITHDRAWN from the graph by a
// tombstone this run.
//
// A separate line from logConflictingIdentities, and the distinction is the
// point of CHAOS-4565: that line says an ownership was NOT ASSERTED, this
// one says an ownership that HAD been asserted was TAKEN BACK. Before this
// change only the first could happen -- suppression left an already-projected
// edge live until a full rebuild -- so an operator reading the suppression
// count had no way to learn whether the graph still carried the edge. Folding
// the retraction into that count would restore exactly the ambiguity it
// exists to remove.
//
// TOMBSTONES EMITTED, NOT EDGES CONFIRMED DELETED, and the field names say
// so. A retraction is emitted UNCONDITIONALLY for every suppressed group,
// because this producer is backend-neutral and cannot ask the graph whether
// the edge is there -- the same unconditional shape, and the same reason, as
// queryWorkItemDependencies' ref-form healing. A tombstone for an edge that
// was never projected is a silent no-op, so this number is an upper bound on
// what was removed. Naming it "retracted_edges" would be a claim this
// producer cannot support, and an inaccurate count reads as coverage.
//
// Counts under the closed retractionReason vocabulary and a hashed org id
// only: never a project id, key, team id or row key, the same budget as its
// siblings. Silent when nothing was retracted -- zero retractions is the
// normal state and a per-run line saying so is noise.
func logRetractions(ctx context.Context, logger *slog.Logger, orgID string, ledger *ambiguityLedger) {
	if logger == nil {
		return
	}
	ambiguous := ledger.retractionCount(retractionReasonAmbiguousKey)
	conflicting := ledger.retractionCount(retractionReasonConflictingIdentity)
	if ambiguous == 0 && conflicting == 0 {
		return
	}
	logger.WarnContext(ctx, "devhealthsource tombstoned project ownership edges",
		"org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "source", TeamsProjectsSourceName,
		"reason", "an ownership can no longer be substantiated, so any OWNED_BY_TEAM edge a previous projection left behind is retracted rather than merely not re-asserted",
		"counts", "tombstones emitted; a tombstone for an edge that was never projected is a no-op, so these are an upper bound on edges removed",
		"ownership_edge_tombstones_"+string(retractionReasonAmbiguousKey), ambiguous,
		"ownership_edge_tombstones_"+string(retractionReasonConflictingIdentity), conflicting)
}

// logConflictingIdentities reports ownership rows suppressed because their
// project_id and project_key resolve to different projects.
//
// Separate from logAmbiguousProjectKeys on purpose. Both are omissions, but
// they answer different operator questions -- "this key names several
// projects" versus "this ROW names two projects and disagrees with itself" --
// and the second is a data-integrity signal about the writer, not about key
// reuse. One combined number would say something was dropped without saying
// which kind of wrong the data is.
//
// Fixed reason and a COUNT only, the same budget as its sibling: never a
// project id, key, team id or row key, all of which are tenant data.
func logConflictingIdentities(ctx context.Context, logger *slog.Logger, orgID string, suppressed int) {
	if logger == nil || suppressed == 0 {
		return
	}
	logger.WarnContext(ctx, "devhealthsource suppressed project ownership edges for conflicting identities",
		"org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "source", TeamsProjectsSourceName,
		"reason", "ownership row's project_id and project_key resolve to different projects", "suppressed_conflicting_identities", suppressed)
}

// projectTeamSourceRowIDOffset is the 1-based byte offset of the id segment
// in a project-team.v2 evidence ref: its text after the kind prefix, which is
// the stored group's row key (the same four segments, the same escape).
var projectTeamSourceRowIDOffset = strconv.Itoa(len(contractsv1.ContextFabricEvidenceRefPrefix) + len(contractsv1.ContextFabricEvidenceEntityProjectTeamV2) + len(":") + 1)

// ProjectTeamSourceRowQuery is the organization-level row read of one
// project-team.v2 group: the stored group the projector pages
// (project_team_ownership_groups_current), looked up by its sort key
// (org_id, row_key), the row key read off the ref the resolver binds. A group
// the projector emits is a row this read finds, with the edge's own
// observed_at; a group it suppresses (edge_suppressed: every contributing row
// conflicts or retracts) or a gone group is a row it never returns. The
// caller keys the row on (provider, project id, team, source), the ref's own
// segments, and re-mints the ref from them.
func ProjectTeamSourceRowQuery() contextpacket.OrganizationRowQuery {
	statement := contextpacket.OrganizationRowColumns("provider", "project_id", "team_id", "source") +
		` SELECT ` + evidenceref.SQL(contractsv1.ContextFabricEvidenceEntityProjectTeamV2, "g.provider", "g.project_id", "g.team_id", "g.source_name") + ` evidence_ref_id, 'dev_health' system, 'project_team' entity_type, ` +
		evidenceref.IDSQL(contractsv1.ContextFabricEvidenceEntityProjectTeamV2, "g.provider", "g.project_id", "g.team_id", "g.source_name") + ` entity_id, concat(g.project_id, ' owned by team ', g.team_id) display_label, '' safe_uri, if(g.source_name = 'native', 'native', 'heuristic') provenance, 1.0 confidence, ` +
		`concat('source=', g.source_name, ', provider=', g.provider) citation, g.observed_at observed_at, g.provider provider, g.project_id project_id, g.team_id team_id, g.source_name source ` +
		`FROM project_team_ownership_groups_current AS g FINAL WHERE g.org_id = {org_id:String} AND g.row_key = substring({evidence_locator:String}, ` + projectTeamSourceRowIDOffset + `) AND g.is_deleted = 0 AND g.edge_suppressed = 0 )`
	return contextpacket.OrganizationRowQuery{
		SourceQuery: contextpacket.SourceQuery{ID: contextpacket.ProjectTeamSourceRowQueryID, Source: "teams", Scope: contextpacket.EvidenceScopeRepo, Statement: statement},
		KeyColumns:  []string{"provider", "project_id", "team_id", "source"},
	}
}
