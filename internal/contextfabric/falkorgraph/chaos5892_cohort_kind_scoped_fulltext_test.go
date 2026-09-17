package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// CHAOS-5892: a cohort whose members are enumerated from an anchor-set
// expression ("which projects does the platform team own") must not be
// reported truncated/incomplete because an UNRELATED, more populous kind's
// lexical matches spent falkorgraph's shared full-text collect budget before
// the result was ever filtered to the cohort's own declared kind. This file
// proves the class through the real production entry point
// (*Adapter).DiscoverContext, the same way repository_cohort_test.go's suite
// does -- no test here builds the Cohort it asserts on.

// kindScopedFulltextRow is a fulltext-shaped row (see
// repository_cohort_test.go's repositoryFulltextRow) for an arbitrary
// declared kind, authorized under both the repository and team scopes this
// file's principal/anchor use, so a fixture never has to choose which
// authorization axis it is proving.
func kindScopedFulltextRow(kind, canonicalID, label string) row {
	return row{"node": &node{Properties: map[string]interface{}{
		propKind: kind, propCanonicalID: canonicalID, propLabel: label,
		propSearchText:               label,
		"authorization_repositories": []string{"full-chaos/dev-health-acr"},
		"authorization_teams":        []string{"team_platform"},
		"evidence_refs":              []string{"evidence_" + canonicalID},
	}}}
}

// scopedProjectExpression is "which projects does the platform team own" --
// a children_of_scope cohort variant declaring member kind=project, the same
// shape repository_cohort_test.go's scopedRepositoryExpression uses for
// member kind=repository. Both reach cohortExactNameCensusEligibility's
// anchor_set deny once ScopeAnchorResolved is true.
func scopedProjectExpression() contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionChildrenOfScope,
		Scoped: &contextfabric.ScopedSetExpression{
			AnchorTerms: []string{"platform"}, MemberKind: contextfabric.SubjectProject,
		},
	}
}

// kindCrowdedAnchorSetConn answers the general (kind-agnostic) full-text
// query with generalRows and a kind-scoped full-text query (runFulltextQuery
// called with a non-empty kindFilter, which binds params["kind"]) with
// kindRows[kind]. It fails the test if the exact-name/kind-scoped census ever
// runs -- for an anchor-set cohort that must stay denied (the anchor-set
// comparison carve-out), untouched by the lexical-arm admission below.
func kindCrowdedAnchorSetConn(t *testing.T, generalRows []row, kindRows map[string][]row) *fakeConn {
	t.Helper()
	return &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			if kind, ok := params["kind"].(string); ok {
				// Binding the $kind PARAMETER proves nothing on its own -- a
				// mutant that drops the Cypher predicate while leaving the
				// parameter bound would still reach here. The query's own
				// kind-scoping predicate (queries.go's runFulltextQuery,
				// propKind="subject_kind") must be present in the text too.
				if !strings.Contains(cypher, "node.subject_kind = $kind") {
					t.Fatalf("params[\"kind\"]=%q was bound but the Cypher carries no node.subject_kind = $kind predicate: %s", kind, cypher)
				}
				return kindRows[kind], nil
			}
			return generalRows, nil
		case strings.Contains(cypher, "$kinds"):
			t.Error("the exact-name/kind-scoped census ran for an anchor-set cohort -- the anchor-set comparison carve-out must stay denied; the lexical arm alone is in scope here, never the census gate")
			return nil, nil
		default:
			return nil, nil
		}
	}}
}

// errKindScopedReadFailed is the sentinel error kindScopedFulltextErroringConn
// returns for the kind-scoped query alone -- distinct from any error a
// production call site might itself construct, so a test asserting on it
// can tell "the fixture's own injected failure surfaced" from "some other
// error happened to match".
var errKindScopedReadFailed = errors.New("kind-scoped fulltext read failed (test fixture)")

// kindScopedFulltextErroringConn answers the general full-text query
// normally and the kind-scoped query with errKindScopedReadFailed, every
// other query with nil/nil -- the fixture for proving an AUXILIARY arm's
// own read failure degrades the cohort's completeness claim instead of
// aborting DiscoverContext.
func kindScopedFulltextErroringConn(generalRows []row) *fakeConn {
	return &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			if _, ok := params["kind"].(string); ok {
				return nil, errKindScopedReadFailed
			}
			return generalRows, nil
		default:
			return nil, nil
		}
	}}
}

// kindScopedFulltextContextErrorConn is kindScopedFulltextErroringConn's
// twin for the OTHER failure class: the kind-scoped query's own context is
// cancelled/expired mid-call (not a dependency read failure). Answers the
// general full-text query normally, the kind-scoped query with kindErr,
// every other query with nil/nil.
func kindScopedFulltextContextErrorConn(generalRows []row, kindErr error) *fakeConn {
	return &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			if _, ok := params["kind"].(string); ok {
				return nil, kindErr
			}
			return generalRows, nil
		default:
			return nil, nil
		}
	}}
}

// kindScopedFulltextOpaqueErrorAfterCancelConn proves the check reads
// ctx.Err() itself, not merely errors.Is on kindErr: the kind-scoped
// query's own context is cancelled by something else AT THE MOMENT it
// returns an ORDINARY (non-context-sentinel) error -- the two are
// unrelated by construction (an opaque fixture error, never
// context.Canceled/DeadlineExceeded), yet ctx.Err() is already non-nil by
// the time DiscoverContext looks. Answers the general full-text query
// normally, the kind-scoped query by calling cancel() then returning an
// opaque error, every other query with nil/nil.
func kindScopedFulltextOpaqueErrorAfterCancelConn(generalRows []row, cancel context.CancelFunc) *fakeConn {
	return &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			if _, ok := params["kind"].(string); ok {
				cancel()
				return nil, errKindScopedReadFailed
			}
			return generalRows, nil
		default:
			return nil, nil
		}
	}}
}

// scopedCohortRequest builds a DiscoverContext request for a children_of_scope
// cohort with a resolved scope anchor and no committed subject -- the exact
// state cv-scoped-projects-by-team-bounded/qb-scoped trace at (per diagnosis:
// pool_truncation_arms=fulltext alone, no hop_walk arm, so
// Resolution.Committed is empty for these rows; the anchor is resolved only
// at the frame/ScopeAnchorResolved layer).
func scopedCohortRequest(expression contextfabric.SubjectExpression, question string, maxCohortMembers int) contextfabric.GraphDiscoveryRequest {
	request := repositoryCohortRequest(expression, question, maxCohortMembers)
	request.ScopeAnchorResolved = true
	return request
}

// projectRows builds n authorized, distinct project fulltext rows.
func projectRows(n int) []row {
	rows := make([]row, 0, n)
	for i := 0; i < n; i++ {
		id := "project_" + string(rune('a'+i))
		rows = append(rows, kindScopedFulltextRow("project", id, id))
	}
	return rows
}

// decoyRows builds n authorized, distinct rows of an unrelated, populous kind
// -- ci_pipeline_run is the live-venue example named by diagnosis (57094
// nodes against 36 Project nodes).
func decoyRows(n int) []row {
	rows := make([]row, 0, n)
	for i := 0; i < n; i++ {
		id := "run_" + string(rune('a'+i))
		rows = append(rows, kindScopedFulltextRow("ci_pipeline_run", id, id))
	}
	return rows
}

// TestScopedProjectCohortNotFalselyTruncatedByMixedKindFulltextCrowdOut is
// the RED test on the parent commit. The general (kind-agnostic)
// full-text arm returns 5 real project rows plus 21 decoy rows of a more
// numerous, unrelated kind -- 26 total, one more than a.config.MaxResults
// (25, newFakeAdapter's fixed budget) -- so the query's own limit+1 sentinel
// (runFulltextQuery) reports truncated=true even though all 5 project rows
// are present in the kept prefix. On the parent commit that single
// kind-agnostic truncation flag is the ONLY fulltext signal
// cohortPoolTruncation ever sees, so it marks the cohort Truncated even
// though its own declared kind was never actually cut -- and the anchor-set
// basis denies the census that would otherwise cover the loss (the
// anchor-set comparison carve-out), so nothing rescues it.
// cohortPoolTruncation must instead be fed from a kind-scoped arm: querying
// kind=project alone returns exactly the 5
// real rows, under budget, so the cohort should report Complete=true,
// Truncated=false, and the exact-name/kind census must still show
// admitted=false, basis=cohort_expression_anchor_set (unchanged, honest).
func TestScopedProjectCohortNotFalselyTruncatedByMixedKindFulltextCrowdOut(t *testing.T) {
	projects := projectRows(5)
	general := append(append([]row{}, projects...), decoyRows(21)...) // 26 total: crowds the shared budget
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": projects})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	if result.Cohort == nil {
		t.Fatalf("Cohort = nil, want a project cohort with 5 members -- the fitting population must not starve because an unrelated kind crowded the shared full-text budget")
	}
	if len(result.Cohort.Members) != 5 {
		t.Fatalf("Cohort.Members has %d member(s), want 5: %+v", len(result.Cohort.Members), result.Cohort.Members)
	}
	if result.Cohort.Truncated {
		t.Errorf("Cohort.Truncated = true, want false -- all 5 project rows survived the crowded general query and the kind-scoped arm alone (5 rows) never approached the collect budget")
	}
	if !result.Cohort.Complete {
		t.Errorf("Cohort.Complete = false, want true")
	}

	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohortKindBases = %+v, want exactly 1 basis recorded", telemetry.cohortKindBases)
	}
	if got := telemetry.cohortKindBases[0].poolTruncation; got != CohortPoolTruncationNone {
		t.Errorf("pool_truncation = %q, want %q", got, CohortPoolTruncationNone)
	}
	if got := formatCohortPoolTruncationArms(telemetry.cohortKindBases[0].poolTruncationArms); got != "" {
		t.Errorf("pool_truncation_arms = %q, want empty", got)
	}

	if len(telemetry.cohortExactNameCensusGates) != 1 {
		t.Fatalf("cohortExactNameCensusGates = %+v, want exactly 1 gate decision", telemetry.cohortExactNameCensusGates)
	}
	if gate := telemetry.cohortExactNameCensusGates[0]; gate.admitted || gate.basis != CohortExactNameCensusBasisAnchorSet {
		t.Errorf("census gate = %+v, want {admitted:false basis:%q} -- the anchor-set comparison carve-out must stay denied, never widened", gate, CohortExactNameCensusBasisAnchorSet)
	}
}

// TestScopedProjectCohortStillTruncatedWhenItsOwnKindOverflowsTheBudget is
// the FIRST negative control the ticket requires: a cohort genuinely over its
// own kind's collect budget must still disclose truncated/incomplete, never
// trade an honest degrade for a false "complete". Here the KIND-SCOPED arm
// itself returns 26 project rows -- one over budget -- so even with the fix
// in place the cohort must stay Truncated=true.
func TestScopedProjectCohortStillTruncatedWhenItsOwnKindOverflowsTheBudget(t *testing.T) {
	projects := projectRows(26)
	fake := kindCrowdedAnchorSetConn(t, projects, map[string][]row{"project": projects})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatalf("Cohort = nil, want a truncated project cohort")
	}
	if !result.Cohort.Truncated || result.Cohort.Complete {
		t.Errorf("Cohort = {Complete:%v Truncated:%v}, want {Complete:false Truncated:true} -- a cohort genuinely over its own kind's budget must stay disclosed as incomplete", result.Cohort.Complete, result.Cohort.Truncated)
	}
	if got := telemetry.cohortKindBases[0].poolTruncation; got != CohortPoolTruncationTruncated {
		t.Errorf("pool_truncation = %q, want %q", got, CohortPoolTruncationTruncated)
	}
	if got := formatCohortPoolTruncationArms(telemetry.cohortKindBases[0].poolTruncationArms); got != string(CohortPoolTruncationArmFulltext) {
		t.Errorf("pool_truncation_arms = %q, want %q", got, CohortPoolTruncationArmFulltext)
	}
}

// TestAlreadyCommittedCohortNotFalselyTruncatedByMixedKindFulltextCrowdOut is
// the sibling sweep the ticket requires: basis=already_committed (a
// discovered_cohort request that already carries a committed subject, e.g. a
// prior-turn carry-over) denies the census through the SAME `censusAdmitted`
// gate as anchor_set (reader.go: `censusAdmitted := shapeAnchorEligible &&
// len(request.Resolution.Committed) == 0`), so it is exposed to the
// identical budget-before-filter crowd-out and must be covered by the same
// fix, not by a second, row-specific patch.
func TestAlreadyCommittedCohortNotFalselyTruncatedByMixedKindFulltextCrowdOut(t *testing.T) {
	projects := projectRows(3)
	general := append(append([]row{}, projects...), decoyRows(23)...) // 26 total
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": projects})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := repositoryCohortRequest(
		contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind, Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectProject}},
		"which projects need attention", 30)
	// discovered_kind is always shapeAnchorEligible regardless of anchor
	// (cohortExactNameCensusEligibility); a non-empty Committed set is what
	// denies it here, via CohortExactNameCensusBasisAlreadyCommitted.
	request.Resolution.Committed = []contextfabric.SubjectRef{
		{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_carried", Label: "full-chaos/dev-health-acr"},
	}

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 3 {
		t.Fatalf("Cohort = %#v, want exactly 3 project members", result.Cohort)
	}
	if result.Cohort.Truncated {
		t.Errorf("Cohort.Truncated = true, want false -- the already_committed sibling must be covered by the same kind-scoped-arm fix")
	}
	if len(telemetry.cohortExactNameCensusGates) != 1 {
		t.Fatalf("cohortExactNameCensusGates = %+v, want exactly 1 gate decision", telemetry.cohortExactNameCensusGates)
	}
	if gate := telemetry.cohortExactNameCensusGates[0]; gate.admitted || gate.basis != CohortExactNameCensusBasisAlreadyCommitted {
		t.Errorf("census gate = %+v, want {admitted:false basis:%q}", gate, CohortExactNameCensusBasisAlreadyCommitted)
	}
}

// cohortMemberIDs extracts a cohort's member canonical ids, in order -- the
// comparison unit the equivalence/parity proofs below diff two runs on.
func cohortMemberIDs(cohort *contextfabric.Cohort) []string {
	if cohort == nil {
		return nil
	}
	ids := make([]string, len(cohort.Members))
	for i, m := range cohort.Members {
		ids[i] = m.Subject.CanonicalID
	}
	return ids
}

// TestScopedProjectCohortByteIdenticalWithAndWithoutKindScopedQuery is
// admission-parity proof (a): for a small population well under the collect
// budget (no crowd-out), the kind-scoped arm's hits are a strict subset of
// what the general arm already returned -- every one of them is already
// `seenNode` by the time the kind-scoped loop runs, so it contributes
// NOTHING new and the served cohort is exactly what the general arm alone
// would have produced. Proved by construction (the dedup argument in the
// assertion below): the crowd-out tests above are what actually exercise the
// merge doing real work, this one exercises it doing NONE.
func TestScopedProjectCohortByteIdenticalWithAndWithoutKindScopedQuery(t *testing.T) {
	rows := []row{
		kindScopedFulltextRow("project", "project_alpha", "Alpha"),
		kindScopedFulltextRow("project", "project_beta", "Beta"),
	}
	fake := kindCrowdedAnchorSetConn(t, rows, map[string][]row{"project": rows})
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	wantIDs := []string{"project_alpha", "project_beta"}
	if gotIDs := cohortMemberIDs(result.Cohort); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("Cohort.Members ids = %v, want %v in this exact order -- the general arm's own input order, since the kind-scoped arm's identical rows are deduped away and contribute nothing", gotIDs, wantIDs)
	}
	if result.Cohort.Truncated || !result.Cohort.Complete {
		t.Errorf("Cohort = {Complete:%v Truncated:%v}, want {Complete:true Truncated:false}", result.Cohort.Complete, result.Cohort.Truncated)
	}
}

// TestScopedProjectCohortKindScopedArmRestoresExactlyWhatTheBudgetCrowdedOut
// is admission-parity proof (b): EQUIVALENCE. With the general arm's own
// collect budget raised high enough that nothing is crowded out, the general
// arm alone already carries every project row -- the kind-scoped arm's
// merge is then a pure no-op (proof (a)'s dedup argument), so that raised-
// budget cohort IS "the general arm only" in every observable sense. At the
// NORMAL budget the served cohort is byte-identical: the kind-scoped arm
// only RESTORES what the shared budget crowded out, never admits a member
// the general arm would not have admitted on its own.
func TestScopedProjectCohortKindScopedArmRestoresExactlyWhatTheBudgetCrowdedOut(t *testing.T) {
	projects := projectRows(5)
	// Decoys ranked FIRST -- this fake's list order is the query's own
	// return order. At the normal 25-item budget, runFulltextQuery's own
	// limit+1=26 sentinel keeps only the first 25 of these 30 rows: every
	// decoy, and EVERY project row crowded out of the general arm entirely.
	// At a raised budget (40 > 30) nothing is cut, so the general arm alone
	// already carries all 5 project rows.
	general := append(append([]row{}, decoyRows(25)...), projects...)
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": projects})
	question := "which projects does the platform team own"

	normalAdapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})
	normalResult, err := normalAdapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		scopedCohortRequest(scopedProjectExpression(), question, 30))
	if err != nil {
		t.Fatalf("DiscoverContext() error (normal budget) = %v", err)
	}

	raisedAdapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 40, PoolSize: 1, AllowInsecure: true, Telemetry: &recordingTelemetry{},
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	raisedResult, err := raisedAdapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		scopedCohortRequest(scopedProjectExpression(), question, 30))
	if err != nil {
		t.Fatalf("DiscoverContext() error (raised budget) = %v", err)
	}

	normalIDs, raisedIDs := cohortMemberIDs(normalResult.Cohort), cohortMemberIDs(raisedResult.Cohort)
	if !reflect.DeepEqual(normalIDs, raisedIDs) {
		t.Fatalf("member ids at normal budget = %v, at raised (uncrowded) budget = %v, want byte-identical", normalIDs, raisedIDs)
	}
	if normalResult.Cohort == nil || raisedResult.Cohort == nil {
		t.Fatalf("Cohort = %#v / %#v, want both non-nil", normalResult.Cohort, raisedResult.Cohort)
	}
	if normalResult.Cohort.Complete != raisedResult.Cohort.Complete || normalResult.Cohort.Truncated != raisedResult.Cohort.Truncated {
		t.Fatalf("Complete/Truncated diverge: normal={%v,%v} raised={%v,%v}", normalResult.Cohort.Complete, normalResult.Cohort.Truncated, raisedResult.Cohort.Complete, raisedResult.Cohort.Truncated)
	}
}

// TestScopedProjectCohortKindScopedArmNeverBypassesAuthorization is
// admission-parity proof (c): a node that fails AuthorizedAttributes (declared
// under a DIFFERENT repository scope than the requesting principal) must
// never enter the cohort via the kind-scoped arm -- exactly the same
// exclusion DiscoveredCohort's own AuthorizedAttributes conjunct already
// applies to the general arm today; the new arm adds no second, looser gate.
func TestScopedProjectCohortKindScopedArmNeverBypassesAuthorization(t *testing.T) {
	visible := kindScopedFulltextRow("project", "project_visible", "Visible")
	hidden := kindScopedFulltextRow("project", "project_hidden", "Hidden")
	hidden["node"].(*node).Properties["authorization_repositories"] = []string{"some-other-org/private"}
	rows := []row{visible, hidden}
	fake := kindCrowdedAnchorSetConn(t, rows, map[string][]row{"project": rows})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != "project_visible" {
		t.Fatalf("Cohort = %#v, want exactly one member (project_visible) -- an unauthorized node must never enter via the kind-scoped arm", result.Cohort)
	}
	if telemetry.cohortMembersAuthzDropped == 0 {
		t.Errorf("cohortMembersAuthzDropped = 0, want > 0 -- the unauthorized node's exclusion must be counted, not silently vanish")
	}
}

// TestCohortKindFulltextCertifiesFromTheRealProducerNotTruncated is a
// real-producer certification: drives the REAL
// SlogTelemetry.RecordCohortKindFulltext (falkorgraph/config.go), through
// the REAL DiscoverContext production path, into a REAL slog.JSONHandler
// sink -- never a hand-built eventspec.CohortKindFulltextFields struct
// literal -- and certifies the actual emitted JSON against
// eventspec.CohortKindFulltext (spec.go). members=2 is the kind-scoped arm's
// OWN raw retrieval count -- see RecordCohortKindFulltext's own doc comment
// for why that count is measured before the seenNode admission narrows it.
func TestCohortKindFulltextCertifiesFromTheRealProducerNotTruncated(t *testing.T) {
	kindRows := []row{
		kindScopedFulltextRow("project", "project_alpha", "Alpha"),
		kindScopedFulltextRow("project", "project_beta", "Beta"),
	}
	fake := kindCrowdedAnchorSetConn(t, kindRows, map[string][]row{"project": kindRows})

	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v (real slog JSON output failed to parse)", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.CohortKindFulltext,
		Want: map[string]any{
			"org_id":      "org-1",
			"member_kind": "project",
			"members":     2,
			"truncated":   false,
		},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
	// request_id must be ABSENT (never emitted empty) when the context
	// carries no request id -- the "missing != measured zero" invariant
	// RecordCohortKindFulltext's own doc comment states.
	for _, line := range log.LinesWithMsg(eventspec.CohortKindFulltext.Msg) {
		if _, present := line["request_id"]; present {
			t.Errorf("line %+v carries request_id, want the key entirely absent (no request id was ever supplied to this call)", line)
		}
	}
}

// cohortKindFulltextTestRequestID is a syntactically valid request id
// (observability.parseRequestID requires the literal "req_" prefix plus
// exactly 32 lowercase hex characters) -- WithRequestID silently discards
// anything else, which is precisely the trap a first draft of this test hit.
const cohortKindFulltextTestRequestID = "req_" + "deadbeefdeadbeefdeadbeefdeadbeef"

// TestCohortKindFulltextCertifiesRequestIDFromContext is the sibling of the
// test above: proves request_id carries the REAL id from ctx
// (observability.WithRequestID) when one is present, not a hardcoded or
// stale value -- the absent case alone cannot tell a genuine read from a
// constant "".
func TestCohortKindFulltextCertifiesRequestIDFromContext(t *testing.T) {
	rows := []row{kindScopedFulltextRow("project", "project_alpha", "Alpha")}
	fake := kindCrowdedAnchorSetConn(t, rows, map[string][]row{"project": rows})

	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	ctx := observability.WithRequestID(context.Background(), cohortKindFulltextTestRequestID)
	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(ctx, repositoryPrincipal(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.CohortKindFulltext,
		Want: map[string]any{
			"org_id": "org-1", "member_kind": "project", "members": 1, "truncated": false,
			"request_id": cohortKindFulltextTestRequestID,
		},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
}

// TestCohortKindFulltextCertifiesTruncatedFromTheRealProducer is the
// honest-truncation half: when the kind-scoped arm itself overflows its
// budget, the real producer must certify truncated=true -- the same negative
// control TestScopedProjectCohortStillTruncatedWhenItsOwnKindOverflowsTheBudget
// proves at the Cohort level, certified here at the trace level instead.
func TestCohortKindFulltextCertifiesTruncatedFromTheRealProducer(t *testing.T) {
	projects := projectRows(26)
	fake := kindCrowdedAnchorSetConn(t, projects, map[string][]row{"project": projects})

	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.CohortKindFulltext,
		Want: map[string]any{
			"org_id":      "org-1",
			"member_kind": "project",
			"members":     25,
			"truncated":   true,
		},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
}

// TestScopedProjectCohortKindScopedArmPreservesQueryRelevanceOrder proves
// ORDER PARITY with the general arm: runFulltextQuery (queries.go) already
// returns candidates in a TOTAL, deterministic order (score DESC, subject
// kind ASC, canonical id ASC) -- the SAME query-building authority the
// general arm uses and never re-orders after retrieval. The kind-scoped
// arm's own contribution must reach the cohort in exactly that order, not
// re-sorted by canonical id alone. The general arm is crowded out entirely
// (only decoys), so every member below arrives through the kind-scoped arm
// alone, and the fake hands them back in descending-relevance order
// (already NOT alphabetical) -- the same shape a real ORDER BY score DESC
// query returns when relevance and canonical id disagree.
func TestScopedProjectCohortKindScopedArmPreservesQueryRelevanceOrder(t *testing.T) {
	general := decoyRows(25) // crowds every project row out of the general arm
	byRelevance := []row{
		kindScopedFulltextRow("project", "project_c", "C"), // highest-ranked
		kindScopedFulltextRow("project", "project_a", "A"),
		kindScopedFulltextRow("project", "project_b", "B"), // lowest-ranked
	}
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": byRelevance})
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	wantIDs := []string{"project_c", "project_a", "project_b"}
	if gotIDs := cohortMemberIDs(result.Cohort); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("member ids = %v, want %v (the query's own relevance order, unmodified)", gotIDs, wantIDs)
	}
}

// TestScopedProjectCohortKindScopedArmCapCutKeepsTopRankedSurvivors is the
// order-parity defect's own reproduction, pinned as a permanent regression
// test: when the kind-scoped arm's contribution exceeds MaxCohortMembers,
// the survivors must be the query's own TOP-ranked candidates, never an
// artifact of re-sorting by canonical id. A re-sort here would let an
// alphabetically-first, lower-relevance candidate win the capped slot over
// the highest-ranked one.
func TestScopedProjectCohortKindScopedArmCapCutKeepsTopRankedSurvivors(t *testing.T) {
	general := decoyRows(25) // crowds every project row out of the general arm
	byRelevance := []row{
		kindScopedFulltextRow("project", "project_z", "Z"), // highest-ranked
		kindScopedFulltextRow("project", "project_y", "Y"),
		kindScopedFulltextRow("project", "project_x", "X"), // lowest-ranked, alphabetically first
	}
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": byRelevance})
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 1)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	wantIDs := []string{"project_z"}
	if gotIDs := cohortMemberIDs(result.Cohort); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("member ids = %v, want %v (the top-ranked candidate, not the alphabetically-first one)", gotIDs, wantIDs)
	}
	if result.Cohort == nil || !result.Cohort.Truncated || result.Cohort.Complete {
		t.Fatalf("Cohort = %#v, want {Complete:false Truncated:true} -- the cap cut a genuine population and must disclose it", result.Cohort)
	}
}

// TestScopedProjectCohortDedupAcrossArmsKeepsFirstEncounteredRank proves a
// node both arms return is admitted exactly once, at the position the
// GENERAL arm (which runs first) gave it -- the kind-scoped arm's own
// occurrence of the same node is a harmless, order-preserving no-op, never a
// second entry or a reordering.
func TestScopedProjectCohortDedupAcrossArmsKeepsFirstEncounteredRank(t *testing.T) {
	shared := kindScopedFulltextRow("project", "project_shared", "Shared")
	onlyGeneral := kindScopedFulltextRow("project", "project_general_only", "GeneralOnly")
	general := []row{shared, onlyGeneral} // general arm's own relevance order
	kindScoped := []row{shared}           // same node, found again by the kind-scoped arm
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": kindScoped})
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	wantIDs := []string{"project_shared", "project_general_only"}
	if gotIDs := cohortMemberIDs(result.Cohort); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("member ids = %v, want %v exactly once each, in the general arm's own order", gotIDs, wantIDs)
	}
}

// TestCohortKindFulltextDeclaresMemberKindAsAClosedVocabulary asserts
// eventspec.CohortKindFulltext's "member_kind" field carries a non-empty
// ClosedVocabulary. This is what makes an unregistered value at the real
// producer a certify FAILURE rather than a silently-accepted open string.
func TestCohortKindFulltextDeclaresMemberKindAsAClosedVocabulary(t *testing.T) {
	for _, f := range eventspec.CohortKindFulltext.Fields {
		if f.Key != "member_kind" {
			continue
		}
		if len(f.ClosedVocabulary) == 0 {
			t.Fatal("member_kind carries no ClosedVocabulary -- an unregistered SubjectKind value would certify successfully")
		}
		return
	}
	t.Fatal("eventspec.CohortKindFulltext declares no member_kind field")
}

// TestScopedProjectCohortSkipsKindScopedArmWhenCensusIsAdmitted proves the
// kind-scoped full-text arm never runs at all when the exact-name/kind-scoped
// census is admitted (not merely that its contribution would be redundant,
// already shown by the byte-identical test above): the fake here fails the
// test outright if a kind-scoped fulltext query is ever issued, and the
// census alone still serves a complete cohort. This is what makes a
// transient failure in the (now-skipped) lexical arm harmless whenever the
// census could complete the call on its own.
func TestScopedProjectCohortSkipsKindScopedArmWhenCensusIsAdmitted(t *testing.T) {
	censusRow := fakeSubjectNodeRow("project", "project_from_census", "FromCensus")
	censusRow["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			if _, ok := params["kind"]; ok {
				t.Fatal("the kind-scoped full-text arm ran even though the exact-name census was admitted -- it must be skipped entirely, not merely redundant")
			}
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			return []row{censusRow}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})

	// discovered_kind: "no term to match, give me the kind's whole census"
	// -- always census-eligible, and with no committed subject the census
	// is admitted (cohortExactNameCensusEligibility).
	request := repositoryCohortRequest(
		contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind, Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectProject}},
		"which projects need attention", 30)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != "project_from_census" {
		t.Fatalf("Cohort = %#v, want exactly one member (project_from_census) served by the census alone", result.Cohort)
	}
}

// TestScopedProjectCohortKindScopedArmReadFailureDegradesInsteadOfAborting is
// the reproduction: the kind-scoped arm's OWN read fails (census not
// admitted, so this arm is the only route to the declared kind), and
// DiscoverContext must still return a served, honestly-truncated cohort
// (the general arm's own contribution, if any) rather than aborting the
// call the way every other retrieval arm's own failure does -- a call this
// arm exists to rescue must never end up worse off than the code it
// replaces.
func TestScopedProjectCohortKindScopedArmReadFailureDegradesInsteadOfAborting(t *testing.T) {
	general := []row{kindScopedFulltextRow("project", "project_from_general", "FromGeneral")}
	fake := kindScopedFulltextErroringConn(general)
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v, want no error -- an auxiliary arm's own read failure must degrade, never abort", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != "project_from_general" {
		t.Fatalf("Cohort = %#v, want exactly one member (project_from_general) -- the general arm's own contribution must still be served", result.Cohort)
	}
	if !result.Cohort.Truncated || result.Cohort.Complete {
		t.Fatalf("Cohort = {Complete:%v Truncated:%v}, want {Complete:false Truncated:true} -- an unmeasured arm can never claim completeness", result.Cohort.Complete, result.Cohort.Truncated)
	}
	if len(telemetry.cohortKindFulltexts) != 1 {
		t.Fatalf("cohortKindFulltexts = %+v, want exactly 1 record", telemetry.cohortKindFulltexts)
	}
	got := telemetry.cohortKindFulltexts[0]
	if got.decision != CohortKindFulltextReadFailed || got.readErr == nil {
		t.Errorf("cohortKindFulltexts[0] = %+v, want {decision:%q readErr:non-nil}", got, CohortKindFulltextReadFailed)
	}
}

// TestScopedProjectCohortKindScopedArmPropagatesContextCancellationInsteadOfDegrading
// is the degrade rule's OWN exception: a cancelled/expired context is the
// caller giving up, not a dependency read failure this arm can degrade
// around -- every other abort site in this method already propagates it
// like any other error, and there is nothing to serve toward once the
// caller has stopped waiting. The kind-scoped arm must propagate it
// exactly the same way, never swallow it into a served-but-degraded
// answer.
func TestScopedProjectCohortKindScopedArmPropagatesContextCancellationInsteadOfDegrading(t *testing.T) {
	general := []row{kindScopedFulltextRow("project", "project_from_general", "FromGeneral")}
	fake := kindScopedFulltextContextErrorConn(general, context.Canceled)
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverContext() error = %v, want context.Canceled propagated, never degraded", err)
	}
	if len(telemetry.cohortKindFulltexts) != 0 {
		t.Fatalf("cohortKindFulltexts = %+v, want zero records -- a propagated cancellation is not a decision the arm reports", telemetry.cohortKindFulltexts)
	}
}

// TestScopedProjectCohortKindScopedArmPropagatesContextDeadlineInsteadOfDegrading
// is the cancellation test's twin for the OTHER context-done sentinel: a
// deadline exceeded mid-call must propagate exactly like a cancellation,
// never degrade.
func TestScopedProjectCohortKindScopedArmPropagatesContextDeadlineInsteadOfDegrading(t *testing.T) {
	general := []row{kindScopedFulltextRow("project", "project_from_general", "FromGeneral")}
	fake := kindScopedFulltextContextErrorConn(general, context.DeadlineExceeded)
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DiscoverContext() error = %v, want context.DeadlineExceeded propagated, never degraded", err)
	}
	if len(telemetry.cohortKindFulltexts) != 0 {
		t.Fatalf("cohortKindFulltexts = %+v, want zero records -- a propagated deadline is not a decision the arm reports", telemetry.cohortKindFulltexts)
	}
}

// TestScopedProjectCohortKindScopedArmPropagatesAnOpaqueErrorWhenTheContextIsAlreadyDone
// proves the check reads ctx.Err() itself, not merely errors.Is on
// kindErr: the fixture's own error is ordinary (never a context
// sentinel), but the context is already cancelled by the time
// DiscoverContext looks -- ctx.Err() must still take priority over
// degrading around an error that merely coincides with it.
func TestScopedProjectCohortKindScopedArmPropagatesAnOpaqueErrorWhenTheContextIsAlreadyDone(t *testing.T) {
	general := []row{kindScopedFulltextRow("project", "project_from_general", "FromGeneral")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := kindScopedFulltextOpaqueErrorAfterCancelConn(general, cancel)
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(ctx, repositoryPrincipal(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverContext() error = %v, want context.Canceled (ctx.Err()) propagated, never degraded around the fixture's own opaque error", err)
	}
	if len(telemetry.cohortKindFulltexts) != 0 {
		t.Fatalf("cohortKindFulltexts = %+v, want zero records -- a propagated cancellation is not a decision the arm reports", telemetry.cohortKindFulltexts)
	}
}

// TestCohortKindFulltextCertifiesReadFailedFromTheRealProducer certifies the
// decision=read_failed shape through the real producer: only org_id,
// decision, member_kind and error are present -- members/truncated/
// added_by_kind_arm/duplicates_with_general carry no meaning for a read
// that never completed and must be genuinely absent, not zero.
func TestCohortKindFulltextCertifiesReadFailedFromTheRealProducer(t *testing.T) {
	fake := kindScopedFulltextErroringConn(nil)
	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.CohortKindFulltext,
		Want: map[string]any{
			"org_id": "org-1", "decision": string(CohortKindFulltextReadFailed), "member_kind": "project",
		},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
	for _, line := range log.LinesWithMsg(eventspec.CohortKindFulltext.Msg) {
		for _, absentKey := range []string{"members", "truncated", "added_by_kind_arm", "duplicates_with_general"} {
			if _, present := line[absentKey]; present {
				t.Errorf("line %+v carries %q, want it absent on decision=read_failed (a failed read measured nothing)", line, absentKey)
			}
		}
		if _, present := line["error"]; !present {
			t.Errorf("line %+v carries no error key, want the failure reason present", line)
		}
	}
}

// TestScopedProjectCohortKindScopedArmReportsMergeDelta proves
// added_by_kind_arm/duplicates_with_general: 2 of the kind-scoped arm's 3
// candidates are genuinely new (the general arm never saw them, crowded
// out), 1 is a duplicate the general arm already found.
func TestScopedProjectCohortKindScopedArmReportsMergeDelta(t *testing.T) {
	shared := kindScopedFulltextRow("project", "project_shared", "Shared")
	newA := kindScopedFulltextRow("project", "project_new_a", "NewA")
	newB := kindScopedFulltextRow("project", "project_new_b", "NewB")
	general := []row{shared} // the general arm already found "shared"
	kindScoped := []row{shared, newA, newB}
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": kindScoped})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortKindFulltexts) != 1 {
		t.Fatalf("cohortKindFulltexts = %+v, want exactly 1 record", telemetry.cohortKindFulltexts)
	}
	got := telemetry.cohortKindFulltexts[0]
	if got.addedByKindArm != 2 || got.duplicatesWithGeneral != 1 {
		t.Errorf("cohortKindFulltexts[0] = %+v, want {addedByKindArm:2 duplicatesWithGeneral:1}", got)
	}
}

// TestCohortKindFulltextCertifiesMergeDeltaFromTheRealProducer certifies
// added_by_kind_arm/duplicates_with_general through the real producer, the
// same fixture TestScopedProjectCohortKindScopedArmReportsMergeDelta uses.
func TestCohortKindFulltextCertifiesMergeDeltaFromTheRealProducer(t *testing.T) {
	shared := kindScopedFulltextRow("project", "project_shared", "Shared")
	newA := kindScopedFulltextRow("project", "project_new_a", "NewA")
	general := []row{shared}
	kindScoped := []row{shared, newA}
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": kindScoped})

	var buf bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	if _, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.CohortKindFulltext,
		Want: map[string]any{
			"org_id": "org-1", "decision": string(CohortKindFulltextRan), "member_kind": "project",
			"added_by_kind_arm": 1, "duplicates_with_general": 1,
		},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
}

// TestCohortKindFulltextDecisionClosedVocabularyMatchesEventspec proves the
// real producer's own decision vocabulary (CohortKindFulltextDecisionVocabulary)
// never drifts from eventspec.CohortKindFulltext's declared closed vocabulary
// for "decision" -- the same cross-package parity CohortKindCensusDecision
// already needs, checked here for this sibling event too.
func TestCohortKindFulltextDecisionClosedVocabularyMatchesEventspec(t *testing.T) {
	var declared []string
	for _, f := range eventspec.CohortKindFulltext.Fields {
		if f.Key == "decision" {
			declared = f.ClosedVocabulary
		}
	}
	if declared == nil {
		t.Fatal("eventspec.CohortKindFulltext declares no closed vocabulary for \"decision\"")
	}
	var real []string
	for _, d := range CohortKindFulltextDecisionVocabulary() {
		real = append(real, string(d))
	}
	if !reflect.DeepEqual(declared, real) {
		t.Fatalf("eventspec declares %v, CohortKindFulltextDecisionVocabulary() returns %v -- must match exactly, in order", declared, real)
	}
}
