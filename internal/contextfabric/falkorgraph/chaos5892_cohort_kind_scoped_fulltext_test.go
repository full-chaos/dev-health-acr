package falkorgraph

import (
	"bytes"
	"context"
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
	// request_id is present but empty when the context carries no request
	// id -- RecordCohortKindFulltext's own doc comment explains why this
	// line cannot drop the key the way its siblings do (the scanner-safe
	// single-spread shape leaves no room to post-process the generated
	// args). eventspec.CohortKindFulltext still declares it
	// PresenceConditional, so an empty value here is not a contract breach.
	for _, line := range log.LinesWithMsg(eventspec.CohortKindFulltext.Msg) {
		if got, present := line["request_id"]; !present || got != "" {
			t.Errorf("line %+v request_id = %v (present=%v), want present and empty (no request id was ever supplied to this call)", line, got, present)
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

// TestScopedProjectCohortKindScopedArmMergesDeterministically proves the
// determinism discipline the kind-scoped arm's own doc comment claims
// (reader.go, beside its sortCandidateNodesBySubjectKey call): the arm's OWN
// return order must not leak into cohort member rank order. The general arm
// is crowded out entirely (only decoys), so every member below arrives
// through the kind-scoped arm alone, and the fake hands them back
// deliberately OUT of canonical order.
func TestScopedProjectCohortKindScopedArmMergesDeterministically(t *testing.T) {
	general := decoyRows(25) // crowds every project row out of the general arm
	outOfOrder := []row{
		kindScopedFulltextRow("project", "project_c", "C"),
		kindScopedFulltextRow("project", "project_a", "A"),
		kindScopedFulltextRow("project", "project_b", "B"),
	}
	fake := kindCrowdedAnchorSetConn(t, general, map[string][]row{"project": outOfOrder})
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})

	request := scopedCohortRequest(scopedProjectExpression(), "which projects does the platform team own", 30)
	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	wantIDs := []string{"project_a", "project_b", "project_c"}
	if gotIDs := cohortMemberIDs(result.Cohort); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("member ids = %v, want %v (canonical subject-key order, independent of the arm's own return order)", gotIDs, wantIDs)
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
