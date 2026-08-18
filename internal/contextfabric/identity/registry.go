package identity

import "fmt"

// Kind constants for the five changed kinds the design brief fixes (§1.2).
// D-8 (episode split identity) is a separate ticket (CHAOS-3901) and is not
// part of this registry.
const (
	KindCIPipelineRun     = "ci_pipeline_run"
	KindPullRequestReview = "pull_request_review"
	KindDeployment        = "deployment"
	KindWorkItem          = "work_item"
	KindProject           = "project"
)

// MaxNaturalKeyBytes is the registry's OWN byte-length guard (design brief
// H6-minor / §5 item 6 / D10): 256 BYTES, matching
// internal/contracts/v1.ContextFabricSubjectRefCanonicalIDMaxLength's
// numeric value, but counted differently and DELIBERATELY not implemented
// by reusing that package's stringLengthBetween. stringLengthBetween
// (internal/contracts/v1/validation_helpers.go:13) counts UTF-8 code
// points via utf8.RuneCountInString; this bound counts bytes, because bytes
// are the actual wire/storage cost and a multi-byte-rune natural key can
// sit at or under 256 code points while its UTF-8 byte length exceeds 256
// (registry_bound_test.go pins the difference with a multi-byte-rune case).
const MaxNaturalKeyBytes = 256

// Registration is one derivation rule: a kind, the ClickHouse table its
// schema is declared on (devhealthschema.EngineFull's key -- the anchor
// registry_parity_test.go checks against), and its natural-key segments in
// ORDER BY order with org_id already stripped.
type Registration struct {
	Kind    string
	Table   string
	Columns []string
}

// devhealthschema:not-a-production-replica these five table names are an INDEX pointing at devhealthschema.EngineFull (see
// registry_parity_test.go), not a second declaration of column shape or
// engine -- Registration carries no CREATE TABLE facet at all, only which
// table's ORDER BY key the kind's segments must match.
//
// Registry is the ordered, closed set of derivation rules -- "one
// derivation rule per kind" (design brief §6), "zero exemptions" (§1.3). A
// slice, not a map, so iteration order is deterministic in tests and
// future diffs.
var Registry = []Registration{
	{Kind: KindCIPipelineRun, Table: "ci_pipeline_runs", Columns: []string{"repo_id", "run_id"}},
	{Kind: KindPullRequestReview, Table: "git_pull_request_reviews", Columns: []string{"repo_id", "number", "review_id"}},
	{Kind: KindDeployment, Table: "deployments", Columns: []string{"repo_id", "deployment_id"}},
	{Kind: KindWorkItem, Table: "work_items", Columns: []string{"repo_id", "work_item_id"}},
	{Kind: KindProject, Table: "projects", Columns: []string{"provider", "id"}},
}

// Lookup finds a Registration by kind.
func Lookup(kind string) (Registration, bool) {
	for _, r := range Registry {
		if r.Kind == kind {
			return r, true
		}
	}
	return Registration{}, false
}

// Derive computes the `<kind>.v2:` canonical id (design brief §1.1) from
// kind's natural-key segment VALUES, supplied in the same order as
// Registration.Columns.
//
// This is a pure function. S1 ships it unwired: no producer in
// internal/contextfabric/devhealthsource calls Derive yet, and calling it
// changes nothing about a live graph key or serving behavior. S2 rewires
// each cited derivation site through Derive (design brief §6).
//
// Every segment is passed through EncodeSegment, including segments that
// happen never to contain ':' or '%' in live data today -- uniformity is
// what makes the codec's injectivity hold for every present and future
// kind, not just the ones currently colon-free (§1.1).
//
// A natural key whose derived id would exceed MaxNaturalKeyBytes is
// refused rather than truncated: the caller must omit the WHOLE row
// (never commit a collision-prone prefix) and, if ledger is non-nil, the
// omission is recorded there.
func Derive(kind string, values []string, ledger *Ledger) (id string, omitted bool, err error) {
	reg, ok := Lookup(kind)
	if !ok {
		return "", false, fmt.Errorf("identity: unregistered kind %q", kind)
	}
	if len(values) != len(reg.Columns) {
		return "", false, fmt.Errorf("identity: kind %q wants %d segments %v, got %d", kind, len(reg.Columns), reg.Columns, len(values))
	}
	id = kind + ".v2:" + JoinSegments(values...)
	if len(id) > MaxNaturalKeyBytes {
		if ledger != nil {
			ledger.Record(kind, values, len(id))
		}
		return "", true, nil
	}
	return id, false, nil
}
