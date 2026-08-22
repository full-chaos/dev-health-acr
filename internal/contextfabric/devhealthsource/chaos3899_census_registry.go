package devhealthsource

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CensusPredicate is a fully-built, parameterized SQL WHERE fragment plus
// its bindings -- the composed D (design brief v5 §1.2) a census statement
// ANDs onto its base table's own org_id equality.
type CensusPredicate struct {
	SQL      string
	Bindings []contextpacket.ClickHouseBinding
}

// censusKindRegistryEntry is the closed, BASE-TABLE-ONLY per-kind census
// registry (design brief v5 §1.3(1), BLOCKING sol v3 #2). Every producer
// query this package already ships for these four tables
// (queryPullRequests, queryCIRuns, queryPullRequestReviews -- tables.go)
// INNER JOINs a parent (repos, or repos+git_pull_requests): a delayed or
// absent parent row would erase an already-ingested census subject, and
// D0's ingestion boundary cannot excuse that (the subject IS ingested).
// The census therefore never reuses those producer queries -- every entry
// here reads exactly ONE base table, no JOIN, ever.
type censusKindRegistryEntry struct {
	kind  graphrank.CensusKind
	table string // FROM <table> FINAL -- base table, no join
	alias string // this entry's own table alias; every predicate/column below is alias-qualified
	// orgColumn is the alias-qualified org_id equality column.
	orgColumn string
	// identityColumn is the row statement's own SELECT AND the aggregate
	// statement's own min(...) identity-witness expression (the fail-closed
	// aggregate/row-agreement check's natural key, brief §1.3(2); witness
	// requirement per the post-review v6 stamp -- see chaos3899_census.go's
	// RunCensus doc comment). It MUST be the base table's FULL TYPED
	// composite natural key -- exactly its own devhealthschema ORDER BY
	// sort key (org_id, repo_id, <kind-specific columns...>), never a
	// lossy single-column serialization -- concatenated with an
	// unambiguous ':' separator over toString(...)-wrapped non-String
	// columns. A lossy witness (e.g. a single opaque id column alone)
	// would reopen exactly the injectivity trap the witness exists to
	// close: two rows that legitimately differ in a column the witness
	// dropped could collide under it, silently.
	identityColumn string
	// handlePredicate builds the registry-pinned SQL predicate for a
	// grammar-bound handle VALUE -- "the exact SQL predicate... pinned
	// against the producer's own Go derivation" (brief §1.2). nil for a
	// kind with no handle grammar entry (pull_request_review).
	handlePredicate func(value string) (CensusPredicate, error)
	// anchorColumns maps an anchor's own subject Kind to THIS base table's
	// OWN FK column (brief §1.3(1): "anchors hit the base table's OWN FK
	// columns... no join to the parent table is ever needed"). A kind
	// requested for an anchor class this table has no FK column for is a
	// joined_column_discriminator refusal (BuildCensusDiscriminator), not
	// a silent join.
	anchorColumns map[contextfabric.SubjectKind]string
	// bridgeCanonicalID is CHAOS-3898 S3's hand-off to 3896 Slices B/C
	// (design brief v4.1 §6 S3 row; 3896 brief v6 §1.4's precondition): it
	// computes the GRAPH canonical id a Count==1 RunCensus result's own
	// SatisfierNaturalKey (this entry's identityColumn value) bridges to,
	// so the future keyed graph existence read (nodeByKindID) has an id to
	// look up. Exported via BridgeSatisfierToCanonicalID below -- this
	// field stays package-private, matching handlePredicate/anchorColumns'
	// own "registry owns the per-kind shape, callers go through the
	// exported dispatcher" convention.
	bridgeCanonicalID func(satisfierNaturalKey string) (canonicalID string, omitted bool, err error)
}

// canonicalIDValue recovers the RAW source id an anchor predicate compares
// against a base table's own FK column, given the anchor SUBJECT KIND the
// canonical id belongs to.
//
// Most anchor kinds (SubjectRepository, today's only other anchor kind in
// this registry) still use the pre-CHAOS-3898 "<kind>:"+id convention
// (tables.go/teams_projects.go's repositoryCanonicalID etc.), so the plain
// strings.Cut on the FIRST ':' still recovers the id correctly there.
//
// CHAOS-3898's changed kinds do NOT: identity.Lookup(string(anchorKind))
// reports whether anchorKind is one of the five, and for those,
// identity.Segments decodes the FULL "<kind>.v2:"+segments id and returns
// its LAST segment -- e.g. project.v2:<provider>:<id> -> <id>, matching
// work_items.project_id, which (this registry's own comment above records)
// "carries no provider". A naive single-Cut on the first ':' would instead
// return "<provider>:<id>" for a v2 project id -- a value that can never
// equal w.project_id, which is exactly the false would_no_match class this
// package's own doc comments forbid (a silent, permanent miss, not an
// error). Falling through to the legacy Cut when identity.Segments can't
// parse the id (a kind whose id hasn't been migrated yet, or a malformed
// value) keeps this function total, matching Cut's own "no separator
// found" fallback.
func canonicalIDValue(anchorKind contextfabric.SubjectKind, canonicalID string) string {
	if segments, ok := identity.Segments(string(anchorKind), canonicalID); ok && len(segments) > 0 {
		return segments[len(segments)-1]
	}
	_, raw, found := strings.Cut(canonicalID, ":")
	if !found {
		return canonicalID
	}
	return raw
}

// projectAnchorPredicate builds the anchor equality fragment for an anchor
// column against anchorCanonicalID (CHAOS-4108).
//
// For every anchor kind except SubjectProject this is exactly the pre-4108
// single-arm equality: toString(column) = the raw id canonicalIDValue
// extracts. Unchanged.
//
// For SubjectProject it widens to BOTH arms of the SAME deliberate dual
// project-id space queryWorkItemProjects' own union-based join fix
// (CHAOS-4108, teams_projects_edges.go) addresses on the producer side: a
// gitlab work_item's project_id column holds projects.project_key (its
// rename-safe project_full_path), never projects.id -- so an anchor
// predicate built from projects.id alone (the ONLY thing canonicalIDValue
// can recover from a project.v2 canonical id) matched zero gitlab work
// items, live-verified.
//
// The safe join-key set is resolved via a small SCOPED lookup against
// `projects` -- the SAME base-table-adjacent pattern AnchorCollision
// (chaos3898_s3_census_bridge.go) already uses for a project-anchor-specific
// check -- not a FROM-clause JOIN on the census's own base table: this
// package's no-join discipline (censusKindRegistryEntry's own doc comment)
// exists because an INNER JOIN to a delayed/absent PARENT row could erase an
// already-ingested census SUBJECT row. An OR'd-in subquery on a column that
// is neither can only ADD a match (when a safe join key resolves) or
// contribute nothing (when it doesn't) -- it can never cause a work_items
// row that already satisfies D to be dropped, so the erasure risk the
// no-join rule guards against does not apply here. Every other anchor kind
// (SubjectRepository) is unaffected: repos carries no such dual-id-space
// defect.
//
// Four codex xhigh review findings, all fixed here (rounds 1-3):
//
// F1 (round 1) -- the whole widened expression is PARENTHESIZED.
// BuildCensusDiscriminator joins this fragment against a handle predicate
// (if bound) with " AND "; an unparenthesized "id_arm OR key_arm" would let
// SQL's AND-binds-tighter-than-OR precedence rewrite "handle AND id_arm OR
// key_arm" into "(handle AND id_arm) OR key_arm" -- silently dropping the
// handle requirement for any row that merely satisfies the project_key arm.
// Every anchor predicate this registry has ever built was a single equality
// with no internal OR, so this never mattered before; it is the first
// predicate here that itself disjoins, so it is the first one required to
// protect its own grouping.
//
// F2/F3/round-3 (rounds 1-3) -- three successive collision classes were
// each fixed with a NARROWER guard before this settled on the producer's own
// WIDER discipline: F2 was a project_key shared by two projects (project_key
// is only documented unique WITHIN a provider, teams_projects_edges.go's own
// THIRD note); F3 was a raw id shared by two providers' projects
// (AnchorCollision's own doc comment: "the anchor's raw FK value... carries
// no provider column of its own"); round 3 was the CROSS-space case neither
// prior guard covered -- one project's own id equal to a DIFFERENT project's
// own project_key (e.g. target id=P key=K, foreign project id=K with an
// empty key of its own) -- a value trusted as safe by an id-only or
// key-only ambiguity count can
// still collide against the OTHER space. All three are really one problem:
// id and project_key are not two independently-safe namespaces, they are
// ONE namespace once both are compared against the SAME column
// (work_items.project_id). The fix mirrors queryWorkItemProjects' own
// producer-side structure exactly (teams_projects_edges.go): UNION ALL each
// project's id AND (if non-empty) its project_key into one flat set of
// (provider, id, join_key) rows, DISTINCT (so a project whose id equals its
// own key is not double-counted), then count() OVER (PARTITION BY join_key)
// across that UNIFIED set, GLOBALLY (every project in the org, not just the
// anchor's own row) -- a join-key VALUE is trusted only if it names exactly
// one project ACROSS BOTH SPACES COMBINED, not just within whichever space
// it happened to originate from. The OUTER filter then narrows to the
// anchor's own (provider, id) -- never id alone, which is what let F3's raw
// id collide across providers in the first place; anchorCanonicalID already
// carries provider (project.v2:<provider>:<id>), so this costs nothing
// canonicalIDValue's own raw-id extraction did not already require.
//
// An ambiguous or foreign-colliding value is omitted, never guessed -- it
// simply does not appear in the safe set, the IN(...) arm cannot match it,
// and the anchor falls back to whatever OTHER safe join keys the target
// project still has (its own id, if that too is unambiguous) rather than
// risk a false-positive satisfier. If anchorCanonicalID cannot even be
// parsed for its provider segment (a pre-CHAOS-3898 id, the SAME "not yet
// migrated" case canonicalIDValue's own Cut fallback exists for), the whole
// union lookup is skipped and this degrades to the plain pre-4108 id-only
// equality -- narrower coverage, never a less-safe one.
func projectAnchorPredicate(anchorKind contextfabric.SubjectKind, column, anchorCanonicalID string) CensusPredicate {
	rawID := canonicalIDValue(anchorKind, anchorCanonicalID)
	predicate := CensusPredicate{
		SQL:      fmt.Sprintf("toString(%s) = {census_anchor_id:String}", column),
		Bindings: []contextpacket.ClickHouseBinding{{Name: "census_anchor_id", Value: rawID}},
	}
	if anchorKind != contextfabric.SubjectProject {
		return predicate
	}
	provider, ok := anchorProviderValue(anchorKind, anchorCanonicalID)
	if !ok {
		return predicate
	}
	// {census_org_id:String} is safe to reference here even though this
	// function never receives orgID itself: RunCensus (chaos3899_census.go)
	// is the ONLY path that ever executes a predicate this package builds,
	// and it unconditionally prepends a census_org_id binding to every
	// statement (aggregate, satisfier-set, and decisive row) before this
	// predicate's SQL is ANDed in.
	predicate.SQL = fmt.Sprintf(
		"toString(%s) IN (SELECT join_key FROM (SELECT provider, id, join_key, count() OVER (PARTITION BY join_key) AS key_count FROM"+
			" (SELECT DISTINCT provider, id, join_key FROM"+
			" (SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {census_org_id:String}"+
			" UNION ALL"+
			" SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {census_org_id:String} AND ifNull(project_key, '') != '')))"+
			" WHERE provider = {census_anchor_provider:String} AND id = {census_anchor_id:String} AND key_count = 1)",
		column,
	)
	predicate.Bindings = append(predicate.Bindings, contextpacket.ClickHouseBinding{Name: "census_anchor_provider", Value: provider})
	return predicate
}

// anchorProviderValue recovers the PROVIDER segment of a CHAOS-3898 v2
// canonical id (project.v2:<provider>:<id>) -- the mirror image of
// canonicalIDValue, which keeps the LAST segment; this keeps the FIRST. ok
// is false under the exact same conditions canonicalIDValue's own
// identity.Segments call can fail (a pre-migration id, a different kind, or
// a malformed value): callers must treat that as "cannot safely
// provider-scope," never guess a provider.
func anchorProviderValue(anchorKind contextfabric.SubjectKind, canonicalID string) (string, bool) {
	segments, ok := identity.Segments(string(anchorKind), canonicalID)
	if !ok || len(segments) == 0 {
		return "", false
	}
	return segments[0], true
}

// pullRequestNumberPredicate is the pull_request handle registry entry's
// SQL: a direct equality on git_pull_requests.number (UInt32, tables.go's
// own CHAOS-3789 scan-then-convert note) -- the handle grammar already
// extracts the bare digits, so no inversion is needed here (unlike the
// work-item ticket-key class below).
func pullRequestNumberPredicate(value string) (CensusPredicate, error) {
	number, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return CensusPredicate{}, fmt.Errorf("devhealthsource: invalid pull_request handle %q: %w", value, err)
	}
	return CensusPredicate{
		SQL:      "p.number = {census_handle_pr_number:UInt32}",
		Bindings: []contextpacket.ClickHouseBinding{{Name: "census_handle_pr_number", Value: uint32(number)}},
	}, nil
}

// workItemTicketKeyPredicate is the work_item handle registry entry's SQL
// -- pinned as the EXACT inverse of embed_fields.go's ticketKeyAlias
// (brief §1.2's load-bearing example): ticketKeyAlias cuts work_item_id at
// its FIRST ':' via strings.Cut and returns the remainder ("" if no colon
// at all). ClickHouse's position() returns the 1-based index of the FIRST
// occurrence (0 if absent) and substring(s, n) returns everything from
// position n onward -- together, position(...)+1 as the start argument is
// the exact SQL transliteration of strings.Cut's "everything after the
// first separator" semantics. workItemHandleMatchesTicketKeyAlias (test
// file) is the pure-Go mirror of this SQL cross-tested directly against
// ticketKeyAlias over live id shapes.
func workItemTicketKeyPredicate(value string) (CensusPredicate, error) {
	if value == "" {
		return CensusPredicate{}, fmt.Errorf("devhealthsource: empty work_item handle")
	}
	return CensusPredicate{
		SQL:      "position(w.work_item_id, ':') > 0 AND substring(w.work_item_id, position(w.work_item_id, ':') + 1) = {census_handle_ticket_key:String}",
		Bindings: []contextpacket.ClickHouseBinding{{Name: "census_handle_ticket_key", Value: value}},
	}, nil
}

// ciRunIDPredicate is the ci_pipeline_run handle registry entry's SQL: a
// direct equality on ci_pipeline_runs.run_id (String, devhealthschema.go).
func ciRunIDPredicate(value string) (CensusPredicate, error) {
	if value == "" {
		return CensusPredicate{}, fmt.Errorf("devhealthsource: empty ci_pipeline_run handle")
	}
	return CensusPredicate{
		SQL:      "c.run_id = {census_handle_run_id:String}",
		Bindings: []contextpacket.ClickHouseBinding{{Name: "census_handle_run_id", Value: value}},
	}, nil
}

// censusKindRegistryEntries is the closed slice-1 census registry (brief
// §0/§1.3(4)): the four stall kinds with existing source tables. Anchor FK
// columns and the row statement's identity column both come straight from
// devhealthschema's own declared types (schema.go) -- work_items.repo_id/
// git_pull_requests.repo_id/ci_pipeline_runs.repo_id/
// git_pull_request_reviews.repo_id are all UUID, so every anchor equality
// wraps its column in toString(...), the SAME idiom tables.go's own SELECT
// lists already use for reading these columns.
// devhealthschema:not-a-production-replica this is the CENSUS REGISTRY -- it pairs each closed
// census kind with its base table name, alias, and predicate builders. It mirrors no column type,
// engine or sort key of its own (those live in devhealthschema.ProductionColumns and are read from
// there by every producer this package ships), so it cannot drift from production the way a rival
// schema declaration would; devhealthschema remains the only physical source of truth.
var censusKindRegistryEntries = map[graphrank.CensusKind]censusKindRegistryEntry{
	contextfabric.SubjectPullRequest: {
		kind: contextfabric.SubjectPullRequest, table: "git_pull_requests", alias: "p",
		// identityColumn is git_pull_requests' OWN declared sort key
		// (org_id, repo_id, number) -- devhealthschema.go's
		// "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id,
		// number)" -- verbatim, not just the (repo_id, number) pair.
		orgColumn: "p.org_id", identityColumn: "concat(p.org_id, ':', toString(p.repo_id), ':', toString(p.number))",
		handlePredicate:   pullRequestNumberPredicate,
		anchorColumns:     map[contextfabric.SubjectKind]string{contextfabric.SubjectRepository: "p.repo_id"},
		bridgeCanonicalID: bridgePullRequestSatisfier,
	},
	contextfabric.SubjectWorkItem: {
		// work_items.project_id is the CHAOS-3898 injectivity-precondition
		// column (brief §1.4: "carries no provider" -- a PRE-EXISTING
		// defect, not solved here). It is still a valid BASE-TABLE FK
		// column for census purposes -- the census only needs it to narrow
		// existence within THIS organization's own base-table rows, never
		// to bridge to a graph canonical id (that bridge is Slice C,
		// blocked on 3898).
		//
		// SubjectRepository is DELIBERATELY ABSENT from anchorColumns
		// (adversarial review finding, corrected): tables.go's own
		// queryWorkItems doc comment records that a Linear-sourced work
		// item carries repo_id = the ZERO UUID at ingest (Linear issues
		// are not tied to a single git repo) -- live-verified there at
		// 3282 of 3288 rows for the trial org. A repository-anchored
		// work_item census (`toString(w.repo_id) = {a real repo's id}`)
		// would therefore return 0 for nearly every real Linear work
		// item, which is exactly the false-would_no_match class D0/§3
		// forbid -- not a race or an edge case, a near-certain miss on
		// the dominant provider shape.
		//
		// project_id DOES carry a known defect for THIS purpose
		// (CHAOS-4108, corrected -- this comment previously claimed
		// otherwise): projects live in a deliberate DUAL id space
		// (teams_projects_edges.go's own "SECOND -- the id-space trap"
		// note), and w.project_id can hold EITHER arm -- projects.id
		// (Linear, and gitlab pre-CHAOS-4108-fix) or projects.project_key
		// (gitlab's actual writer output, ops normalize.py:165-176). A
		// project anchor built from projects.id alone (canonicalIDValue's
		// raw extraction) therefore matched ZERO gitlab work items,
		// live-verified: CensusCount for kind work_item on a gitlab
		// project anchor read 0 before this fix. projectAnchorPredicate
		// below widens the anchor to try both arms, mirroring
		// queryWorkItemProjects' own OR predicate fix -- so project_id
		// remains the sole work_item anchor column for Slice A, but the
		// predicate built from it no longer silently proves absence.
		kind: contextfabric.SubjectWorkItem, table: "work_items", alias: "w",
		// identityColumn is work_items' OWN declared sort key (org_id,
		// repo_id, work_item_id) -- devhealthschema.go's
		// "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id,
		// work_item_id)" -- work_item_id alone is NOT the table's natural
		// key, repo_id is part of it too (even though repo_id is the zero
		// UUID for a Linear item -- that value still participates in the
		// composite honestly, it is simply constant across every
		// Linear-sourced row rather than absent).
		orgColumn: "w.org_id", identityColumn: "concat(w.org_id, ':', toString(w.repo_id), ':', w.work_item_id)",
		handlePredicate: workItemTicketKeyPredicate,
		anchorColumns: map[contextfabric.SubjectKind]string{
			contextfabric.SubjectProject: "w.project_id",
		},
		bridgeCanonicalID: bridgeWorkItemSatisfier,
	},
	contractsv1.ContextFabricSubjectCIRun: {
		kind: contractsv1.ContextFabricSubjectCIRun, table: "ci_pipeline_runs", alias: "c",
		// identityColumn is ci_pipeline_runs' OWN declared sort key
		// (org_id, repo_id, run_id) -- devhealthschema.go's
		// "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id,
		// run_id)" -- the exact three-column composite the v6 stamp's own
		// CI-run example names.
		orgColumn: "c.org_id", identityColumn: "concat(c.org_id, ':', toString(c.repo_id), ':', c.run_id)",
		handlePredicate:   ciRunIDPredicate,
		anchorColumns:     map[contextfabric.SubjectKind]string{contextfabric.SubjectRepository: "c.repo_id"},
		bridgeCanonicalID: bridgeCIPipelineRunSatisfier,
	},
	contractsv1.ContextFabricSubjectPullRequestReview: {
		// No handle grammar entry maps to pull_request_review (brief §1.2/
		// §8: "3 handle patterns" over 4 census kinds) -- this kind is only
		// ever reachable via an anchor discriminator in Slice A.
		kind: contractsv1.ContextFabricSubjectPullRequestReview, table: "git_pull_request_reviews", alias: "r",
		// identityColumn is git_pull_request_reviews' OWN declared sort
		// key (org_id, repo_id, number, review_id) --
		// devhealthschema.go's "ReplacingMergeTree(last_synced) ORDER BY
		// (org_id, repo_id, number, review_id)" -- the FULL four-column
		// composite, not review_id alone even though review_id is itself
		// a provider-issued, likely-already-unique string: the witness
		// must reflect the table's own declared identity, not this
		// package's guess about which subset of it happens to be unique
		// in practice.
		orgColumn: "r.org_id", identityColumn: "concat(r.org_id, ':', toString(r.repo_id), ':', toString(r.number), ':', r.review_id)",
		handlePredicate:   nil,
		anchorColumns:     map[contextfabric.SubjectKind]string{contextfabric.SubjectRepository: "r.repo_id"},
		bridgeCanonicalID: bridgePullRequestReviewSatisfier,
	},
	// devhealthschema:not-a-production-replica registry TAIL -- the same census-kind-to-table
	// pairing continues above, past the reach of the marker on the var's own declaration; still no
	// column type, engine or sort key is mirrored here, only table/alias/column NAMES already
	// declared in devhealthschema.
}

// BuildCensusDiscriminator composes the base-table WHERE fragment for kind
// from whichever discriminator classes are bound (handle and/or anchor).
// It does NOT itself enforce D2(a) (brief §3(4): window+kind alone never
// proves a decisive outcome) -- that is the shadow-round orchestrator's
// job, since only it also knows about the window class. This function's
// own job is registry correctness: an unknown kind, a kind with no
// registered handle grammar, a malformed handle value, or an anchor kind
// with no FK column on this base table (joined_column_discriminator, brief
// §1.3(1)) are all reported as errors rather than silently degraded.
func BuildCensusDiscriminator(kind graphrank.CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool) (CensusPredicate, error) {
	entry, ok := censusKindRegistryEntries[kind]
	if !ok {
		return CensusPredicate{}, fmt.Errorf("devhealthsource: %s is not a registered census kind", kind)
	}
	var fragments []string
	var bindings []contextpacket.ClickHouseBinding
	if handleBound {
		if entry.handlePredicate == nil {
			return CensusPredicate{}, fmt.Errorf("devhealthsource: %s has no registered handle grammar", kind)
		}
		predicate, err := entry.handlePredicate(handleValue)
		if err != nil {
			return CensusPredicate{}, err
		}
		fragments = append(fragments, predicate.SQL)
		bindings = append(bindings, predicate.Bindings...)
	}
	if anchorBound {
		column, ok := entry.anchorColumns[anchorKind]
		if !ok {
			return CensusPredicate{}, fmt.Errorf("devhealthsource: joined_column_discriminator -- %s has no base-table FK column for anchor kind %s", kind, anchorKind)
		}
		predicate := projectAnchorPredicate(anchorKind, column, anchorCanonicalID)
		fragments = append(fragments, predicate.SQL)
		bindings = append(bindings, predicate.Bindings...)
	}
	if len(fragments) == 0 {
		return CensusPredicate{}, fmt.Errorf("devhealthsource: no discriminator bound for %s census", kind)
	}
	return CensusPredicate{SQL: strings.Join(fragments, " AND "), Bindings: bindings}, nil
}
