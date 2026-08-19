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
		// the dominant provider shape. project_id has no such known
		// defect for THIS purpose (existence narrowing, not graph
		// bridging), so it remains the sole work_item anchor column for
		// Slice A.
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
		fragments = append(fragments, fmt.Sprintf("toString(%s) = {census_anchor_id:String}", column))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "census_anchor_id", Value: canonicalIDValue(anchorKind, anchorCanonicalID)})
	}
	if len(fragments) == 0 {
		return CensusPredicate{}, fmt.Errorf("devhealthsource: no discriminator bound for %s census", kind)
	}
	return CensusPredicate{SQL: strings.Join(fragments, " AND "), Bindings: bindings}, nil
}
