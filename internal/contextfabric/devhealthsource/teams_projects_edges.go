package devhealthsource

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
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
// the SELECT beneath it. Embedded twice (once per UNION arm) rather than
// named once, matching this package's own established convention for
// SQL text that must be identical in two call sites (chaos4099_scope_
// expander.go's projectRepositories doc comment: "mirrors that
// computation exactly", inline, not shared via a function).
const resolvedProjectsSubquery = `(
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
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
const membershipIntervalsSubquery = `(
  WITH touches AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider, to_project_id AS project_id, occurred_at, event_id, 1 AS is_add
    FROM project_membership_transitions FINAL
    WHERE org_id = {org_id:String} AND to_project_id != '' AND to_project_id != from_project_id
    UNION ALL
    SELECT org_id, subject_kind, repo_id, subject_id, provider, from_project_id AS project_id, occurred_at, event_id, 0 AS is_add
    FROM project_membership_transitions FINAL
    WHERE org_id = {org_id:String} AND from_project_id != '' AND from_project_id != to_project_id
  ),
  classified AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at, event_id, is_add,
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
      row_number() OVER w2 AS rn,
      count() OVER (PARTITION BY org_id, subject_kind, repo_id, subject_id, provider, project_id) AS touch_count,
      leadInFrame(occurred_at, 1, occurred_at) OVER w2 AS next_occurred_at,
      leadInFrame(is_add, 1, is_add) OVER w2 AS next_is_add
    FROM classified
    WHERE NOT dup_flag
    WINDOW w2 AS (PARTITION BY org_id, subject_kind, repo_id, subject_id, provider, project_id ORDER BY occurred_at, event_id ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
  )
  SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at AS observed_at, event_id,
    'transition' AS source,
    if(rn < touch_count AND next_is_add = 0, next_occurred_at, NULL) AS valid_to,
    0 AS is_malformed, 0 AS is_duplicate_add
  FROM closed
  WHERE is_add = 1
  UNION ALL
  SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at AS observed_at, event_id,
    'transition' AS source,
    NULL AS valid_to,
    1 AS is_malformed, 0 AS is_duplicate_add
  FROM classified
  WHERE dangling_flag
  UNION ALL
  SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id, occurred_at AS observed_at, event_id,
    'transition' AS source,
    NULL AS valid_to,
    0 AS is_malformed, 1 AS is_duplicate_add
  FROM classified
  WHERE dup_flag
)`

func subjectProjectMembershipsQuery(telemetry *presenceTelemetryLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return querySubjectProjectMemberships(ctx, client, orgID, cursor, limit, telemetry)
	}
}

func querySubjectProjectMemberships(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, telemetry *presenceTelemetryLedger) ([]candidate, bool, error) {
	// event_id is part of the row key (codex xhigh review R1, HIGH,
	// confirmed real -- see membershipIntervalsSubquery's own doc comment):
	// two transition-arm intervals for the SAME (subject, project) pair CAN
	// legally share an observed_at down to the millisecond, and without
	// event_id in the tiebreaker, both the RelationshipID (below) and the
	// keyset-pagination cursor would collide on it.
	const rowKey = "concat(subject_kind, ':', repo_id_str, ':', subject_id, ':', provider, ':', project_id, ':', event_id)"
	// toDateTime64(0, 3, 'UTC') matches project_membership_transitions.
	// occurred_at's own declared scale (DateTime64(3)) -- the NULL branch of
	// transition arm's `valid_to` (membershipIntervalsSubquery) is derived
	// from that column via leadInFrame, so the ifNull default must share its
	// scale for the two UNION arms to agree on one column type.
	statement := `SELECT subject_kind, repo_id_str, subject_id, repo_slug, observed_at, event_id, source, provider, project_id, resolved_project_id, key_resolution_count, valid_to_present, valid_to_value, is_malformed, is_duplicate_add
FROM (
  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, m.event_id AS event_id, m.source AS source, m.provider AS provider, m.project_id AS project_id, p.id AS resolved_project_id, p.key_resolution_count AS key_resolution_count,
    isNotNull(m.valid_to) AS valid_to_present, ifNull(m.valid_to, toDateTime64(0, 3, 'UTC')) AS valid_to_value, m.is_malformed AS is_malformed, m.is_duplicate_add AS is_duplicate_add
  FROM ` + membershipIntervalsSubquery + ` AS m
  LEFT JOIN ` + resolvedProjectsSubquery + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
  UNION ALL
  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,
    m.observed_at AS observed_at, '' AS event_id, m.source AS source, m.provider AS provider, m.project_id AS project_id, p.id AS resolved_project_id, p.key_resolution_count AS key_resolution_count,
    0 AS valid_to_present, toDateTime64(0, 3, 'UTC') AS valid_to_value, 0 AS is_malformed, 0 AS is_duplicate_add
  FROM project_membership_presence AS m
  LEFT JOIN ` + resolvedProjectsSubquery + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
  LEFT JOIN repos AS r FINAL ON r.id = m.repo_id AND r.org_id = m.org_id
  WHERE m.org_id = {org_id:String} AND m.source = 'work_item_column'
)
WHERE 1 = 1` + sincePredicate(cursor, "observed_at", rowKey) + orderBy("observed_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var subjectKind, repoID, subjectID, repoSlug, eventID, source, provider, projectID, resolvedProjectID string
		var observedAt, validToValue time.Time
		var keyResolutionCount uint64
		var validToPresent, isMalformed, isDuplicateAdd uint8
		if err := r.Scan(&subjectKind, &repoID, &subjectID, &repoSlug, &observedAt, &eventID, &source, &provider, &projectID, &resolvedProjectID, &keyResolutionCount, &validToPresent, &validToValue, &isMalformed, &isDuplicateAdd); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := subjectKind + ":" + repoID + ":" + subjectID + ":" + provider + ":" + projectID + ":" + eventID
		telemetry.recordRead(source, subjectKind)
		if isDuplicateAdd != 0 {
			// See membershipIntervalsSubquery's own doc comment: an ADD
			// immediately preceded by another ADD, no REMOVE between --
			// #1896's discard-and-replay path and an ordinary re-sync both
			// legitimately produce this for a subject that never left.
			// Counted, but NOT treated as malformed and NOT dropped from
			// attribution: the interval this touch belongs to is already
			// emitted by the FIRST add in the run (this row's own
			// occurred_at is strictly later than that interval's
			// ValidFrom), so attribution continues unchanged -- this row
			// itself just has no boundary of its own to contribute.
			telemetry.recordDuplicateAdd(provider, projectID)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
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
			telemetry.recordUnresolved(provider, projectID)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		case keyResolutionCount > 1:
			telemetry.recordAmbiguous(provider, projectID)
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
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
		var relationshipIDPrefix, evidenceRefID string
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
			fromSubject = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: workItemCanonicalID, Label: subjectID}
			relationshipIDPrefix = "relationship:work_item_project:"
			evidenceRefID = "acr:v1:work-item:" + repoID + ":" + subjectID
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
			pullRequestCanonicalID := fmt.Sprintf("pull_request:%s:%d", repoID, number)
			fromSubject = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: pullRequestCanonicalID, Label: fmt.Sprintf("PR #%d", number)}
			relationshipIDPrefix = "relationship:pull_request_project:"
			evidenceRefID = "acr:v1:pull-request:" + repoID + ":" + subjectID
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
			return nil, &ProducerRejection{Reason: fmt.Sprintf("project_membership_presence returned unknown subject_kind %q", subjectKind)}
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
		default:
			return nil, &ProducerRejection{Reason: fmt.Sprintf("project_membership_presence returned unknown source %q", source)}
		}

		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID:  relationshipIDPrefix + repoID + ":" + subjectID + ":" + provider + ":" + projectID + relationshipIDIntervalSuffix,
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
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}

// queryWorkItemTeams projects work_item -> team (OWNED_BY_TEAM) from
// work_item_team_attributions, restricted to the primary attribution.
//
// is_primary = 1 is what makes this a well-defined edge rather than a fan-out:
// live, 3304 work items carry a primary team attribution, every one resolves
// against work_items, and ZERO work items carry more than one primary team,
// more than one repo_id, or more than one source among their primaries
// (all four counts verified directly). Without the is_primary filter the same
// work item carries up to five attributions from different sources
// (native_team, assignee_membership, issue_project, linked_issue,
// project_ownership), which would collapse onto duplicate relationship IDs
// and fail ContextFabricProjectionBatch.Validate() outright.
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
func queryWorkItemTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(w.repo_id), ':', a.work_item_id)"
	statement := `SELECT a.work_item_id, ifNull(a.team_id, ''), toString(a.source), toString(a.confidence), toString(w.repo_id), ifNull(r.repo, ''), a.computed_at
FROM work_item_team_attributions AS a FINAL
INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.org_id = a.org_id
INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = ifNull(a.team_id, '')
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE a.org_id = {org_id:String} AND a.is_primary = 1 AND ifNull(a.team_id, '') != ''` + sincePredicate(cursor, "a.computed_at", rowKey) + orderBy("a.computed_at", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, teamID, source, confidence, repoID, repoSlug string
		var observedAt time.Time
		if err := r.Scan(&workItemID, &teamID, &source, &confidence, &repoID, &repoSlug, &observedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		rowSortKey := repoID + ":" + workItemID
		workItemCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
		if err != nil {
			return nil, err
		}
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		derivation, epistemicStatus := workItemTeamAttributionDerivation(source)
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID:  "relationship:work_item_team:" + repoID + ":" + workItemID + ":" + teamID,
			Type:            contractsv1.ContextFabricRelationshipOwnedByTeam,
			From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: workItemCanonicalID, Label: workItemID},
			To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: teamCanonicalID(teamID), Label: teamID},
			Properties:      attributionProperties(source, confidence),
			Derivation:      derivation,
			EpistemicStatus: epistemicStatus,
			Authorization:   workItemAuthorization(repoID, repoSlug),
			EvidenceRefIDs:  []string{"acr:v1:work-item-team:" + repoID + ":" + workItemID + ":" + teamID},
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
// (valid_from, valid_to IS NULL, ifNull(valid_to, epoch)):
//
//   - latest valid_from wins -- the latest-assertion rule;
//   - on a tie, OPEN outranks CLOSED, so a same-instant assertion of ongoing
//     ownership is never hidden by a simultaneous closure;
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

// projectTeamsRowKey is the pagination tiebreaker for queryProjectTeams,
// shared by the keyset predicate and the ORDER BY so the two cannot drift.
const projectTeamsRowKey = "concat(o.project_id, ':', o.team_id, ':', o.source_name)"

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
func projectTeamsStatement(cursor cursorState) string {
	// TWO equality-joined arms, UNION ALL'd at ROW level, then aggregated
	// on the RESOLVED projects.id.
	//
	//  A. o.project_id  = p.scope        -- the SCOPE arm. It matches scope
	//     rows of BOTH kinds deliberately and must NOT be given a
	//     scope_kind restriction: project_id is not an id column, it is
	//     whichever id space that row uses, and today's GitLab rows hold a
	//     project KEY there. CHAOS-4530's UUID-keyed rows match the id row,
	//     today's GitLab rows match the key row.
	//  B. o.project_key = p.scope        -- the KEY arm, and the ONLY arm
	//     that names scope_kind. It matches the key SCOPE ROW rather than
	//     p.project_key, a column EVERY scope row carries: joining that
	//     column let an id row satisfy a key-shaped guard, and two projects
	//     sharing a key both matched an ownership row naming neither
	//     (CHAOS-4542 defect 6). An ambiguous key now has no scope row at
	//     all -- readers v0.5.5 applies the filter inside the expansion --
	//     so neither arm can resolve one, and no guard here can be
	//     forgotten. This arm is otherwise the ORIGINAL join, kept. An
	//     ownership row may carry a project_id that correlates with nothing
	//     while its project_key is the only column tying it to a project.
	//     Dropping this arm loses those rows entirely -- which is exactly
	//     what the "tied assertions resolve deterministically" fixture
	//     caught: it seeds project_id 'ownership-row-open'/'-closed' against
	//     project_key 'TIE-KEY', so arm A matches neither.
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
	ownershipRows := func(match, where string) string {
		return `
		SELECT p.id AS project_id, p.provider AS provider,
		       o.team_id AS team_id, o.source_name AS source_name, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at
		FROM ` + readers.ProjectIdentityCatalogSQL() + `
		INNER JOIN (
			SELECT provider, ` + readers.ProjectOwnershipJoinColumn + ` AS project_ref, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name, valid_from, valid_to, updated_at
			FROM team_project_ownership FINAL
			WHERE org_id = {org_id:String}
		) AS o ON o.provider = p.provider AND ` + match + `
		INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = o.team_id` + where
	}
	return `SELECT o.project_id, o.team_id, o.source_name,
       min(o.valid_from) AS first_valid_from,
       argMax(tuple(o.valid_to), (o.valid_from, o.valid_to IS NULL, ifNull(o.valid_to, toDateTime64(0, 3, 'UTC')))).1 IS NULL AS latest_is_open,
       ifNull(argMax(tuple(o.valid_to), (o.valid_from, o.valid_to IS NULL, ifNull(o.valid_to, toDateTime64(0, 3, 'UTC')))).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
       max(o.updated_at) AS updated_at, o.provider
FROM (` + ownershipRows(readers.ProjectIdentityMatchSQL("o", "project_ref"), "") + `

		UNION ALL
` + ownershipRows("o.project_key = p.scope", "\n\t\tWHERE p.scope_kind = 'key'") + `
) AS o
GROUP BY o.project_id, o.provider, o.team_id, o.source_name` + havingSincePredicate(cursor, "max(o.updated_at)", projectTeamsRowKey) + orderBy("max(o.updated_at)", projectTeamsRowKey)
}

func queryProjectTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, omissions *ambiguityLedger) ([]candidate, bool, error) {
	const rowKey = projectTeamsRowKey
	// Record ambiguity BEFORE reading the page. The rows this telemetry
	// describes are precisely the ones the page will NOT contain, so there
	// is nothing in the result set to infer them from.
	if err := recordAmbiguousProjectKeys(ctx, client, orgID, omissions); err != nil {
		return nil, false, err
	}
	statement := projectTeamsStatement(cursor)
	rows, truncated, err := fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var projectID, teamID, source, provider string
		var validFrom, latestValidTo, observedAt time.Time
		var latestIsOpen uint8
		if err := r.Scan(&projectID, &teamID, &source, &validFrom, &latestIsOpen, &latestValidTo, &observedAt, &provider); err != nil {
			return nil, err
		}
		observedAt, validFrom, latestValidTo = observedAt.UTC(), validFrom.UTC(), latestValidTo.UTC()
		rowSortKey := projectID + ":" + teamID + ":" + source
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
		projectCanonicalID, projectOmitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil {
			return nil, err
		}
		if projectOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship:project_team:" + provider + ":" + projectID + ":" + teamID + ":" + source,
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
			EvidenceRefIDs:  []string{"acr:v1:project-team:" + provider + ":" + projectID + ":" + teamID},
			ObservedAt:      observedAt,
			SourceVersion:   TeamsProjectsSourceVersion,
		}
		relationship.ValidFrom, relationship.ValidTo = ownershipValidity(validFrom, latestIsOpen, latestValidTo)
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
	if err != nil {
		return nil, false, err
	}
	return rows, truncated, nil
}

// ambiguousProjectKeysStatement lists the (provider, project_key) pairs that
// name MORE THAN ONE project and that some ownership row actually references.
//
// This exists because the omission it reports is invisible everywhere else.
// An ambiguous key is excluded inside ProjectIdentityJoinSQL -- the key scope
// row is emitted only for key_resolution_count = 1 -- so a key-shaped
// ownership row whose key resolves to two projects joins to nothing and never
// reaches queryProjectTeams' scan at all. Correct (neither project may be
// guessed) and quiet, and quiet is the half that is not acceptable: an
// unrecorded omission is indistinguishable from an ownership that simply does
// not exist, which is the shape of silence this wave keeps removing.
//
// The INNER JOIN to team_project_ownership is what makes this an omission
// report rather than a data-quality census: an ambiguous key that no
// ownership row references cost nobody an edge, and counting it would inflate
// the signal toward alarm exactly as page-scoping once deflated it toward
// health.
//
// Deliberately NOT a third copy of the identity join. It answers a different
// question -- "which keys are ambiguous", not "which project does this row
// mean" -- and reusing the join here would couple the telemetry to the very
// resolution whose gaps it exists to report.
//
// Equality-only ON with no function call, because ClickHouse 24.8 (the CI
// fixture pin) rejects anything else outright with Code: 403 while 26.7
// accepts it -- CHAOS-4542 class 4, twice.
const ambiguousProjectKeysStatement = `SELECT p.provider, p.project_key, count() AS project_count
FROM (
	SELECT provider, ifNull(project_key, '') AS project_key, id
	FROM projects FINAL
	WHERE org_id = {org_id:String}
) AS p
INNER JOIN (
	SELECT DISTINCT provider, ifNull(project_key, '') AS project_key
	FROM team_project_ownership FINAL
	WHERE org_id = {org_id:String}
) AS o ON o.provider = p.provider AND o.project_key = p.project_key
WHERE p.project_key != ''
GROUP BY p.provider, p.project_key
HAVING count() > 1`

// recordAmbiguousProjectKeys feeds the run-scoped ledger the keys whose
// ownership edges the SQL dropped.
//
// Run per page rather than once per run: an incremental catch-up never starts
// from an empty cursor, so first-page-only would report nothing for exactly
// the runs that do most of the work. The ledger deduplicates on
// (provider, project_key), so repeating the statement across pages cannot
// double-count -- it costs one bounded aggregate per page and buys telemetry
// that is otherwise unrecoverable.
func recordAmbiguousProjectKeys(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, omissions *ambiguityLedger) error {
	if omissions == nil {
		return nil
	}
	rows, err := client.Query(ctx, ambiguousProjectKeysStatement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var provider, projectKey string
		var projectCount uint64
		if err := rows.Scan(&provider, &projectKey, &projectCount); err != nil {
			return err
		}
		omissions.add(provider, projectKey)
	}
	return rows.Err()
}

// logAmbiguousProjectKeys surfaces omitted ownership edges as a bounded
// signal, counting DISTINCT (provider, project_key) keys accumulated over the
// whole source run rather than one page's slice keyed by something else. Without it, an omission is indistinguishable from an
// ownership that simply does not exist, which is the shape of silence this
// wave keeps removing. The message carries a fixed reason and a COUNT only --
// never a project key, project id or team id, which are tenant data; the
// organization is hashed the same way logOrphanedWorkItems hashes it.
func logAmbiguousProjectKeys(ctx context.Context, logger *slog.Logger, orgID string, omitted int) {
	if logger == nil || omitted == 0 {
		return
	}
	logger.WarnContext(ctx, "devhealthsource omitted project ownership edges for ambiguous project keys",
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
		"reason", "project_key resolves to more than one project within its provider", "omitted_ambiguous_project_keys", omitted)
}

// ownershipValidity states a project->team edge's window explicitly in both
// directions, the same owned-write discipline queryTeams/queryProjects apply
// to entities (CHAOS-3785 R3-1). Ownership begins at the earliest assertion
// ever observed for the edge and ends per the LATEST assertion -- open if that
// assertion left it open, otherwise at its valid_to.
func ownershipValidity(validFrom time.Time, latestIsOpen uint8, latestValidTo time.Time) (*time.Time, *time.Time) {
	from := validFrom
	if latestIsOpen != 0 {
		return &from, nil
	}
	to := latestValidTo
	return &from, &to
}
