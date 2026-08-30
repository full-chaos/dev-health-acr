package devhealthsource

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
func projectTeamRelationshipID(provider, projectID, teamID, source string) string {
	return "relationship:project_team:" + provider + ":" + projectID + ":" + teamID + ":" + source
}

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
// projectTeamsOwnershipArm builds ONE arm of queryProjectTeams' row-level
// union: ownership rows resolved to a project by `match`, narrowed by
// `where`, tagged with the retraction_only literal '0' or '1'.
//
// A package-level function and not the closure it used to be (CHAOS-4565),
// for a testability reason worth stating. The invariant that matters here is
// per arm -- an arm that can ASSERT an edge must not resolve through
// p.project_key, which every scope row carries (CHAOS-4542 defect 6), while
// the retraction arm must. A test cannot check that by splitting the
// assembled statement on "UNION ALL": the identity expansion contains one of
// its own inside every arm, so the split yields six fragments and any guard
// written over them is asserting about the wrong strings. Building arms
// through a named function lets the test hold a real arm.
//
// `where` carries any scope_kind or ambiguity restriction. It is a WHERE and
// not part of the ON because 24.8 rejects an ON that is not a plain column
// equality.
func projectTeamsOwnershipArm(projects, match, where, retractionOnly string) string {
	return `
		SELECT p.id AS project_id, p.provider AS provider,
		       o.project_ref AS ownership_ref, o.project_key AS ownership_key,
		       o.team_id AS team_id, o.source_name AS source_name, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at,
		       p.project_updated_at AS project_updated_at, toUInt8(` + retractionOnly + `) AS retraction_only
		FROM ` + projects + `
		INNER JOIN (
			SELECT provider, ` + readers.ProjectOwnershipJoinColumn + ` AS project_ref, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name, valid_from, valid_to, updated_at
			FROM team_project_ownership FINAL
			WHERE org_id = {org_id:String}
		) AS o ON o.provider = p.provider AND ` + match + `
		INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = o.team_id` + where
}

// projectTeamsArms is the ordered arm list projectTeamsStatement unions, and
// the seam a test reads to check which arms may assert.
func projectTeamsArms() []string {
	resolved := projectIdentityWithWatermarkSQL()
	return []string{
		projectTeamsOwnershipArm(resolved, readers.ProjectIdentityMatchSQL("o", "project_ref"), "", "0"),
		projectTeamsOwnershipArm(resolved, "o.project_key = p.scope", "\n\t\tWHERE p.scope_kind = 'key'", "0"),
		projectTeamsOwnershipArm(ambiguousProjectIdentitySQL(), "o.project_key = p.project_key", "\n\t\tWHERE p.key_project_count > 1 AND o.project_key != ''", "1"),
	}
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
func ambiguousProjectIdentitySQL() string {
	return `(
	SELECT pa.provider AS provider, pa.id AS id, pa.project_key AS project_key, pa.project_updated_at AS project_updated_at,
	       count() OVER (PARTITION BY pa.provider, pa.project_key) AS key_project_count
	FROM (SELECT * FROM ` + projectIdentityWithWatermarkSQL() + `) AS pa
	WHERE pa.scope_kind = 'id' AND pa.project_key != ''
) AS p`
}

func projectIdentityWithWatermarkSQL() string {
	return `(
	SELECT pi.*, w.project_updated_at AS project_updated_at
	FROM (SELECT * FROM ` + readers.ProjectIdentityCatalogSQL() + `) AS pi
	INNER JOIN (
		SELECT provider, id, updated_at AS project_updated_at
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
	//
	// retractionOnly is the literal '0' or '1' (CHAOS-4565). A retraction
	// arm's rows exist ONLY so a group can be SEEN and retracted; they can
	// never assert an edge -- see the RETRACTION ARM note below.
	// FAIL CLOSED on conflicting identities (codex R2 P2-1).
	//
	// The two arms resolve INDEPENDENTLY, so one ownership row whose
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
	// THE RETRACTION ARM (CHAOS-4565), arm C, and why the other two could
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
	//   - CONFLICTING IDENTITY is decided in the SCAN. Both arms produce a
	//     row, the group sees them, and edge_suppressed already reports it.
	//     A group that reaches the scan can be retracted from the scan.
	//   - An AMBIGUOUS KEY is decided UPSTREAM, in SQL:
	//     ProjectIdentityJoinSQL emits no key scope row at all for
	//     key_resolution_count > 1. Arm B therefore matches nothing, arm A
	//     matches nothing for a key-only ownership row, and the row
	//     produces NO RESULT ROW. There is nothing for the scan to see, so
	//     a scan-side retraction alone would silently fix only half the
	//     defect -- and the ambiguity half is the OLDER one, live since v7.
	//
	// Arm C makes the invisible half visible WITHOUT re-admitting it as an
	// assertion: it resolves an ownership key across the AMBIGUOUS key
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
	//   o.project_key != '' is defence in depth -- the empty-key partition
	//   already has key_resolution_count = 0, so it cannot reach > 1 -- but
	//   an empty key matching every keyless project is precisely the defect
	//   class this file has shipped before, so it is spelled out.
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
	// row's own identity -- and arms A/B/C are unioned BEFORE it -- so
	// "every project this row can reach" is not a second query, it is
	// exactly the rows already present. A projects-side edit therefore
	// pushes the affected groups past the cursor on the very next tick,
	// with no rebuild and no checkpoint reset.
	//
	// retraction_only is IN the partition key. Without it, arm C's extra
	// resolved projects would land inside arm A/B's own min()/max()
	// comparison and be read as an identity conflict, so an ownership row
	// with a perfectly good project_id would be suppressed the moment its
	// UNUSED project_key became ambiguous -- turning a retraction feature
	// into an edge-deletion bug. Splitting the partition keeps arm A/B's
	// conflict test byte-identical to what it was.
	const identityPartition = " OVER (PARTITION BY provider, ownership_ref, ownership_key, retraction_only)"
	return `SELECT o.project_id, o.team_id, o.source_name,
       minIf(o.valid_from, o.unassertable = 0) AS first_valid_from,
       argMaxIf(tuple(o.valid_to), (o.valid_from, o.valid_to IS NULL, ifNull(o.valid_to, toDateTime64(0, 3, 'UTC'))), o.unassertable = 0).1 IS NULL AS latest_is_open,
       ifNull(argMaxIf(tuple(o.valid_to), (o.valid_from, o.valid_to IS NULL, ifNull(o.valid_to, toDateTime64(0, 3, 'UTC'))), o.unassertable = 0).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
       ` + projectTeamsWatermark + ` AS observed_at, o.provider,
       toUInt8(countIf(o.unassertable = 0) = 0) AS edge_suppressed,
       groupUniqArrayIf(concat(o.ownership_ref, '\0', o.ownership_key, '\0', o.team_id, '\0', o.source_name), o.unassertable = 1 AND o.retraction_only = 0) AS conflict_identities,
       toUInt8(countIf(o.unassertable = 1 AND o.retraction_only = 0) > 0) AS conflicting_identity_present
FROM (
	SELECT project_id, provider, ownership_ref, ownership_key, team_id, source_name, valid_from, valid_to, retraction_only,
	       greatest(updated_at, max(project_updated_at)` + identityPartition + `) AS row_watermark,
	       toUInt8(retraction_only = 1 OR min(project_id)` + identityPartition + ` != max(project_id)` + identityPartition + `) AS unassertable
	FROM (` + strings.Join(projectTeamsArms(), "\n\n\t\tUNION ALL\n") + `
	)
) AS o
GROUP BY o.project_id, o.provider, o.team_id, o.source_name` + havingSincePredicate(cursor, projectTeamsWatermark, projectTeamsRowKey) + orderBy(projectTeamsWatermark, projectTeamsRowKey)
}

func queryProjectTeams(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int, omissions *ambiguityLedger) ([]candidate, bool, error) {
	const rowKey = projectTeamsRowKey
	statement := projectTeamsStatement(cursor)
	rows, truncated, err := fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var projectID, teamID, source, provider string
		var validFrom, latestValidTo, observedAt time.Time
		var latestIsOpen, edgeSuppressed, conflictingIdentityPresent uint8
		var conflictIdentities []string
		if err := r.Scan(&projectID, &teamID, &source, &validFrom, &latestIsOpen, &latestValidTo, &observedAt, &provider, &edgeSuppressed, &conflictIdentities, &conflictingIdentityPresent); err != nil {
			return nil, err
		}
		observedAt, validFrom, latestValidTo = observedAt.UTC(), validFrom.UTC(), latestValidTo.UTC()
		rowSortKey := projectID + ":" + teamID + ":" + source
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
				CanonicalID:   projectTeamRelationshipID(provider, projectID, teamID, source),
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
		projectCanonicalID, projectOmitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil {
			return nil, err
		}
		if projectOmitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: projectTeamRelationshipID(provider, projectID, teamID, source),
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
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
		"reason", "a project_key naming more than one project cannot resolve an ownership row that carries only that key",
		"ambiguous_project_keys_in_catalog", ambiguous,
		"count_bounded_at", ambiguousProjectKeysCensusLimit)
	return nil
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
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
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
		"org_id", redactOrg(orgID), "source", TeamsProjectsSourceName,
		"reason", "ownership row's project_id and project_key resolve to different projects", "suppressed_conflicting_identities", suppressed)
}
