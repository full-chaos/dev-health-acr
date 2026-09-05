package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5168, at the READER boundary.
//
// The defect: DiscoverContext discarded fulltextSearchNodes' truncation flag
// with a comment arguing DiscoverContext has no auto-commit decision to
// protect. It has no auto-commit decision; it does have a COMPLETENESS claim,
// and the discarded flag was the only evidence bearing on it. When the exact
// census is skipped -- which is exactly what happens once a subject is
// committed -- the full-text arm IS the candidate pool, so a pool it clipped
// from six matches to four produced a four-member cohort reporting
// Complete=true, Truncated=false, and the count step served `satisfied 4/4`
// over a population of six.
//
// The fixture below reproduces that shape mechanically rather than by
// arithmetic on a comment: `poolTruncationFulltextRows` returns one row MORE
// than the adapter's collect budget (which is what runFulltextQuery's
// `len(rows) > limit` reads), of which only a handful are team rows, so the
// cohort lands well BELOW MaxCohortMembers. Both facts are asserted in the
// tests rather than assumed, because a fixture that accidentally reached the
// member cap would pass on the pre-existing cap disclosure and prove nothing.

// poolTruncationFulltextCollectLimit mirrors newFakeAdapter's Config.MaxResults,
// which DiscoverContext reads as its collectLimit. Asserted against the live
// adapter in TestPoolTruncationFixtureMatchesTheAdaptersCollectBudget below,
// so a change to that Config cannot leave this file silently measuring
// nothing.
const poolTruncationFulltextCollectLimit = 25

// poolTruncationTeamCount is how many TEAM rows the fixture puts at the head
// of the full-text result. Small, and far below the fixture's
// MaxCohortMembers, so the member cap is never the reason for a truncation.
const poolTruncationTeamCount = 3

// poolTruncationFulltextRows builds a full-text result of `total` rows:
// poolTruncationTeamCount authorized teams first, then authorized repository
// rows as filler.
//
// The filler is a DIFFERENT KIND on purpose. It makes the row count exceed
// the collect budget (which is what the backend's truncation flag reads)
// without adding cohort members (which is what the member cap reads), so the
// two losses this file must tell apart are varied independently. Teams come
// first because runFulltextQuery keeps `rows[:limit]` when it truncates.
func poolTruncationFulltextRows(total int) []row {
	rows := make([]row, 0, total)
	for i := 0; i < poolTruncationTeamCount && len(rows) < total; i++ {
		r := fulltextRow("team", fmt.Sprintf("team_%d", i), fmt.Sprintf("Team %d", i), "teams struggling", nil)
		r["node"].(*node).Properties["authorization_repositories"] = "*"
		rows = append(rows, r)
	}
	for i := 0; len(rows) < total; i++ {
		r := fulltextRow("repository", fmt.Sprintf("repo_%d", i), fmt.Sprintf("Repo %d", i), "teams struggling", nil)
		r["node"].(*node).Properties["authorization_repositories"] = "*"
		rows = append(rows, r)
	}
	return rows
}

// poolTruncationAdapter answers the full-text query with `total` rows and
// every other query with nothing, so the full-text arm is provably the only
// source of cohort members.
func poolTruncationAdapter(t *testing.T, total int, telemetry GraphTelemetry) *Adapter {
	t.Helper()
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(cypher, "fulltext") {
			return poolTruncationFulltextRows(total), nil
		}
		return nil, nil
	}}
	if telemetry == nil {
		return newFakeAdapter(t, fake)
	}
	return newFakeAdapterWithTelemetry(t, fake, telemetry)
}

// poolTruncationRequest is the request shape the ticket names: a cohort
// question whose exact-name census is NOT admitted, so the full-text arm is
// the whole pool.
//
// It reaches that state through the production gate rather than by asserting
// it: cohortExactNameCensusEligibility refuses a cohort variant outside
// discovered_kind once a scope anchor resolved (chaos4622_cohort_census_gate.go),
// which is the same "the census was skipped" condition a committed subject
// produces at the other clause of censusAdmitted. Both routes are covered --
// this one here, the committed-subject one in the engine test beside this
// file.
func poolTruncationRequest() contextfabric.GraphDiscoveryRequest {
	request := cohortDiscoveryRequestWithScopeAnchor(contextfabric.ShapeExplicitCohort, true)
	request.Request.Options.MaxCohortMembers = 10
	return request
}

// TestPoolTruncationFixtureMatchesTheAdaptersCollectBudget is the fixture's
// own control.
//
// Every test in this file depends on `poolTruncationFulltextCollectLimit + 1`
// actually exceeding the budget DiscoverContext passes to the full-text
// query. If Config.MaxResults moves, the "truncated" fixture stops
// truncating, every assertion below still passes, and the file measures
// nothing -- the silent-green shape a constant copied from another file
// always eventually produces.
func TestPoolTruncationFixtureMatchesTheAdaptersCollectBudget(t *testing.T) {
	t.Parallel()
	adapter := poolTruncationAdapter(t, 1, nil)
	if got := adapter.config.MaxResults; got != poolTruncationFulltextCollectLimit {
		t.Fatalf("adapter collect budget = %d, fixture assumes %d -- update poolTruncationFulltextCollectLimit; until then every truncation fixture in this file is measuring nothing", got, poolTruncationFulltextCollectLimit)
	}
}

// TestDiscoverContextClippedFulltextPoolMakesTheCohortTruncated is the
// red-first test for the reported defect. It fails at the parent, where the
// flag is discarded at reader.go's `textNodes, _, err :=`.
func TestDiscoverContextClippedFulltextPoolMakesTheCohortTruncated(t *testing.T) {
	t.Parallel()
	adapter := poolTruncationAdapter(t, poolTruncationFulltextCollectLimit+1, nil)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, poolTruncationRequest())
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the team cohort the full-text arm found -- this fixture never reached cohort assembly and proves nothing")
	}
	if len(result.Cohort.Members) != poolTruncationTeamCount {
		t.Fatalf("members = %d, want %d -- the fixture's own shape moved", len(result.Cohort.Members), poolTruncationTeamCount)
	}
	if len(result.Cohort.Members) >= poolTruncationRequest().Request.Options.MaxCohortMembers {
		t.Fatalf("members (%d) reached MaxCohortMembers (%d): the pre-existing cap disclosure would carry this test and the pool signal would go unmeasured",
			len(result.Cohort.Members), poolTruncationRequest().Request.Options.MaxCohortMembers)
	}
	if !result.Cohort.Truncated {
		t.Error("Cohort.Truncated = false after the full-text search reported more matches than it returned -- the count step reads this field, and without it an exact count over a clipped pool is served as a census")
	}
	if result.Cohort.Complete {
		t.Error("Cohort.Complete = true over a clipped candidate pool")
	}
}

// TestDiscoverContextWholeFulltextPoolLeavesTheCohortComplete is the
// complement, on the SAME fixture with one row fewer.
//
// One row is the entire difference between the two tests: the row count sits
// exactly AT the budget rather than one past it, which is the boundary
// runFulltextQuery's own `len(rows) > limit` (never `>=`) draws. Same members,
// same cap, same request.
func TestDiscoverContextWholeFulltextPoolLeavesTheCohortComplete(t *testing.T) {
	t.Parallel()
	adapter := poolTruncationAdapter(t, poolTruncationFulltextCollectLimit, nil)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, poolTruncationRequest())
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the team cohort the full-text arm found")
	}
	if len(result.Cohort.Members) != poolTruncationTeamCount {
		t.Fatalf("members = %d, want %d -- the two directions must retain the same members or they are not comparable", len(result.Cohort.Members), poolTruncationTeamCount)
	}
	if result.Cohort.Truncated {
		t.Error("Cohort.Truncated = true for a search that returned everything it matched -- a flag that is always set discloses nothing, and this is the direction that catches an unconditional fix")
	}
	if !result.Cohort.Complete {
		t.Error("Cohort.Complete = false over a whole pool below the member cap")
	}
}

// TestDiscoverContextClippedFulltextUnderACompletedCensusStaysComplete is the
// case that keeps the fix from being over-conservative, and it is the one
// with a real cost if it is wrong: the census path is the ordinary
// subjectless cohort question ("which teams are struggling"), and marking
// every one of those incomplete because a long question clipped a lexical
// search would weaken every cohort answer the product serves.
//
// The census is a term-free fetch of every subject of every servable cohort
// kind, so when it completes it already holds anything the full-text arm
// dropped. The completeness claim survives, and the DECISION is still
// visible -- on the telemetry line, as its own vocabulary member, asserted
// below.
func TestDiscoverContextClippedFulltextUnderACompletedCensusStaysComplete(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return poolTruncationFulltextRows(poolTruncationFulltextCollectLimit + 1), nil
		case strings.Contains(cypher, "$kinds"):
			// A COMPLETED census: well under exactNameCandidateQueryLimit,
			// so chaos4348ExactNameCandidates reports truncated=false.
			r := fakeSubjectNodeRow("team", "team_census", "Team Census")
			r["n"].(*node).Properties["authorization_repositories"] = "*"
			return []row{r}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	// discovered_kind: the one shape the census gate admits unconditionally.
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.Options.MaxCohortMembers = 10

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortExactNameCensusGates) != 1 || !telemetry.cohortExactNameCensusGates[0].admitted {
		t.Fatalf("census gate = %+v, want exactly one ADMITTED decision -- without the census this test is measuring the plain full-text case, not the covered one", telemetry.cohortExactNameCensusGates)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the census-sourced team cohort")
	}
	if !result.Cohort.Complete || result.Cohort.Truncated {
		t.Errorf("Complete=%v Truncated=%v: a clipped lexical arm beneath a COMPLETED org-wide census loses no member, and reporting it as a loss would make every ordinary cohort answer incomplete",
			result.Cohort.Complete, result.Cohort.Truncated)
	}
	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohort kind basis lines = %d, want 1", len(telemetry.cohortKindBases))
	}
	if got := telemetry.cohortKindBases[0].poolTruncation; got != CohortPoolTruncationCoveredByCensus {
		t.Errorf("pool truncation basis = %q, want %q -- reporting %q here would make \"the search was never clipped\" and \"the search was clipped and the census covered it\" the same line, which is the distinction an operator holding a suspicious member count needs",
			got, CohortPoolTruncationCoveredByCensus, CohortPoolTruncationNone)
	}
}

// TestExactNameCensusCoversEveryServableCohortKind pins the coupling the
// covered-by-census decision rests on.
//
// That decision is sound ONLY because the census fetches every kind a cohort
// can be served for. The two sets live in different packages and neither
// mentions the other, so today's equality is a coincidence one edit away from
// ending -- and if it ends, a cohort of the new kind would be certified
// complete over a clipped lexical pool the census never covered, silently,
// with no test failing. This is that test.
func TestExactNameCensusCoversEveryServableCohortKind(t *testing.T) {
	t.Parallel()
	servable := graphrank.ServableCohortKindsForAudit()
	if len(servable) == 0 {
		t.Fatal("ServableCohortKindsForAudit() returned nothing -- the control this comparison needs is empty, so the comparison below cannot fail")
	}
	census := append([]string(nil), exactNameKinds...)
	sort.Strings(census)
	servableStrings := make([]string, 0, len(servable))
	for _, kind := range servable {
		servableStrings = append(servableStrings, string(kind))
	}
	sort.Strings(servableStrings)

	if strings.Join(census, ",") != strings.Join(servableStrings, ",") {
		t.Fatalf("exact-name census kinds %v != servable cohort kinds %v.\n"+
			"cohortPoolTruncation treats a completed census as covering a clipped full-text arm, which holds only while the census fetches every kind a cohort can be served for.\n"+
			"If a servable kind is NOT in the census, that kind's cohorts must carry the full-text truncation unconditionally -- change cohortPoolTruncation, do not relax this test.",
			census, servableStrings)
	}
}

// TestCohortPoolTruncationClassifiesEveryInput quantifies over the whole
// closed input space rather than over the rows someone remembered.
//
// Sixteen rows, not eight: the third retrieval arm (hop walk) is what made the
// old single-vocabulary design untenable, and the table is what keeps its
// addition honest. A classification over a closed space is an allow-list
// naming every member's class, so an input that reached the classifier's
// default arm without being named here shows up as a MISSING KEY, never as a
// silent pass.
func TestCohortPoolTruncationClassifiesEveryInput(t *testing.T) {
	t.Parallel()
	type input struct{ fulltext, hopWalk, exactName, census bool }
	type expectation struct {
		basis     CohortPoolTruncationBasis
		arms      string
		truncated bool
	}
	const (
		ft   = "fulltext"
		hw   = "hop_walk"
		enc  = "exact_name_census"
		none = ""
	)
	// Rows where exactName is true but census is false, and rows where hopWalk
	// and census are both true, are UNREACHABLE in production: exactNameTruncated
	// is only assigned inside the census branch, and censusAdmitted requires zero
	// committed subjects while hopWalk requires at least one. They are classified
	// anyway, conservatively, so a future caller that reaches one inherits a
	// stated answer rather than a default.
	want := map[input]expectation{
		{false, false, false, false}: {CohortPoolTruncationNone, none, false},
		{false, false, false, true}:  {CohortPoolTruncationNone, none, false},
		{true, false, false, false}:  {CohortPoolTruncationTruncated, ft, true},
		{true, false, false, true}:   {CohortPoolTruncationCoveredByCensus, ft, false},
		{false, true, false, false}:  {CohortPoolTruncationTruncated, hw, true},
		{false, true, false, true}:   {CohortPoolTruncationCoveredByCensus, hw, false},
		{true, true, false, false}:   {CohortPoolTruncationTruncated, ft + "," + hw, true},
		{true, true, false, true}:    {CohortPoolTruncationCoveredByCensus, ft + "," + hw, false},
		{false, false, true, false}:  {CohortPoolTruncationTruncated, enc, true},
		{false, false, true, true}:   {CohortPoolTruncationTruncated, enc, true},
		{true, false, true, false}:   {CohortPoolTruncationTruncated, ft + "," + enc, true},
		{true, false, true, true}:    {CohortPoolTruncationTruncated, ft + "," + enc, true},
		{false, true, true, false}:   {CohortPoolTruncationTruncated, hw + "," + enc, true},
		{false, true, true, true}:    {CohortPoolTruncationTruncated, hw + "," + enc, true},
		{true, true, true, false}:    {CohortPoolTruncationTruncated, ft + "," + hw + "," + enc, true},
		{true, true, true, true}:     {CohortPoolTruncationTruncated, ft + "," + hw + "," + enc, true},
	}
	if len(want) != 16 {
		t.Fatalf("expectation table has %d rows, want 16 -- the table must cover the whole input space or it is a sample", len(want))
	}
	for _, fulltext := range []bool{false, true} {
		for _, hopWalk := range []bool{false, true} {
			for _, exactName := range []bool{false, true} {
				for _, census := range []bool{false, true} {
					key := input{fulltext, hopWalk, exactName, census}
					expected, named := want[key]
					if !named {
						t.Fatalf("input %+v is not classified in this table", key)
					}
					basis, arms, truncated := cohortPoolTruncation(fulltext, hopWalk, exactName, census)
					got := formatCohortPoolTruncationArms(arms)
					if basis != expected.basis || truncated != expected.truncated || got != expected.arms {
						t.Errorf("cohortPoolTruncation(%v, %v, %v, %v) = (%q, %q, %v), want (%q, %q, %v)",
							fulltext, hopWalk, exactName, census, basis, got, truncated, expected.basis, expected.arms, expected.truncated)
					}
				}
			}
		}
	}
}

// TestCohortPoolTruncationReportsEveryCutArm is the arms vocabulary's own
// coverage check: each arm, alone, must appear in the reported list. An arm
// that can be cut and never named is invisible exactly when it matters.
func TestCohortPoolTruncationReportsEveryCutArm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		arm                                  CohortPoolTruncationArm
		fulltext, hopWalk, exactName, census bool
	}{
		{CohortPoolTruncationArmFulltext, true, false, false, false},
		{CohortPoolTruncationArmHopWalk, false, true, false, false},
		{CohortPoolTruncationArmExactNameCensus, false, false, true, true},
	}
	if len(cases) != len(CohortPoolTruncationArmVocabulary()) {
		t.Fatalf("%d cases for %d declared arms -- an arm with no case is unmeasured", len(cases), len(CohortPoolTruncationArmVocabulary()))
	}
	for _, tc := range cases {
		_, arms, truncated := cohortPoolTruncation(tc.fulltext, tc.hopWalk, tc.exactName, tc.census)
		if got := formatCohortPoolTruncationArms(arms); got != string(tc.arm) {
			t.Errorf("arm %q alone reported as %q", tc.arm, got)
		}
		if !truncated {
			t.Errorf("arm %q alone did not truncate the pool", tc.arm)
		}
	}
}

// TestCohortPoolTruncationBasesAreDistinct is the vocabulary's own guard: a
// telemetry enum whose members collide reports less than it claims to, and two
// identical string constants would make every table row above agree with a
// broken implementation. Both vocabularies are checked, since either can
// collide independently.
func TestCohortPoolTruncationBasesAreDistinct(t *testing.T) {
	t.Parallel()
	bases := CohortPoolTruncationBasisVocabulary()
	arms := CohortPoolTruncationArmVocabulary()
	if len(bases) == 0 || len(arms) == 0 {
		t.Fatal("a vocabulary is empty -- the loops below cannot fail")
	}
	seenBasis := make(map[CohortPoolTruncationBasis]struct{}, len(bases))
	for _, basis := range bases {
		if strings.TrimSpace(string(basis)) == "" {
			t.Errorf("a pool-truncation basis is empty; an empty telemetry value reads as an absent key")
		}
		if _, dup := seenBasis[basis]; dup {
			t.Errorf("pool-truncation basis %q is declared twice", basis)
		}
		seenBasis[basis] = struct{}{}
	}
	seenArm := make(map[CohortPoolTruncationArm]struct{}, len(arms))
	for _, arm := range arms {
		if strings.TrimSpace(string(arm)) == "" {
			t.Errorf("a pool-truncation arm is empty")
		}
		if _, dup := seenArm[arm]; dup {
			t.Errorf("pool-truncation arm %q is declared twice", arm)
		}
		seenArm[arm] = struct{}{}
		// The two vocabularies ride on different keys of the same line; a
		// value shared between them would make a grep on one match the other.
		if _, collides := seenBasis[CohortPoolTruncationBasis(arm)]; collides {
			t.Errorf("arm %q collides with a decision-vocabulary member", arm)
		}
	}
}

// TestFormatCohortPoolTruncationArmsNormalizes pins the renderer's own
// contract, at the level of the function rather than through a log line.
//
// The order is part of what this key promises: two lines describing the same
// cut arms must be byte-identical, or an operator diffing them sees a
// difference that is only iteration order. Asserting it here as well as on the
// emitted line is deliberate -- the line test proves the production path is
// correct today, this one proves the renderer cannot be made wrong by a future
// caller handing it a different order.
func TestFormatCohortPoolTruncationArmsNormalizes(t *testing.T) {
	t.Parallel()
	vocabulary := CohortPoolTruncationArmVocabulary()
	if len(vocabulary) < 2 {
		t.Fatal("fewer than two arms declared -- ordering cannot be tested")
	}
	canonical := formatCohortPoolTruncationArms(vocabulary)

	reversed := make([]CohortPoolTruncationArm, 0, len(vocabulary))
	for i := len(vocabulary) - 1; i >= 0; i-- {
		reversed = append(reversed, vocabulary[i])
	}
	if got := formatCohortPoolTruncationArms(reversed); got != canonical {
		t.Errorf("reversed input rendered as %q, want %q -- the caller's order reached the line", got, canonical)
	}

	// A repeated arm collapses: a line naming the same arm twice would read as
	// two independent losses.
	doubled := append(append([]CohortPoolTruncationArm{}, vocabulary...), vocabulary[0])
	if got := formatCohortPoolTruncationArms(doubled); got != canonical {
		t.Errorf("duplicated arm rendered as %q, want %q", got, canonical)
	}

	// An undeclared VALUE never reaches the line, but its PRESENCE does — see
	// TestFormatCohortPoolTruncationArmsMarksAnUndeclaredArm. This assertion
	// used to require the undeclared case to render identically to the clean
	// one, i.e. to be dropped silently; that was the behaviour before the
	// marker landed, and leaving it would have been two of this package's own
	// tests asserting opposite things about one function.
	withUnknown := append(append([]CohortPoolTruncationArm{}, vocabulary...), CohortPoolTruncationArm("not_a_declared_arm"))
	got := formatCohortPoolTruncationArms(withUnknown)
	if !strings.HasPrefix(got, canonical) {
		t.Errorf("undeclared arm rendered as %q, want it to still begin with the canonical declared list %q", got, canonical)
	}
	if strings.Contains(got, "not_a_declared_arm") {
		t.Errorf("the undeclared value itself appears in %q -- only the marker may", got)
	}
	if !strings.Contains(got, cohortPoolTruncationUnknownArm) {
		t.Errorf("rendered %q with no unknown-arm marker -- a dropped signal cannot be told from one never produced", got)
	}

	if formatCohortPoolTruncationArms(nil) != "" {
		t.Error("nil arms rendered non-empty")
	}
}

// TestFormatCohortPoolTruncationArmsMarksAnUndeclaredArm is r2 finding 5.
//
// The renderer used to DROP an undeclared arm. That is the shape this whole
// change exists to remove: a signal that disappears is indistinguishable from
// a signal that was never produced, so a caller emitting a value the
// vocabulary cannot name looked exactly like a caller emitting nothing.
//
// It now renders one fixed token instead. The token is deliberately NOT a
// vocabulary member — asserted below, because making the malformed case a
// legal classification would defeat the point.
func TestFormatCohortPoolTruncationArmsMarksAnUndeclaredArm(t *testing.T) {
	t.Parallel()
	got := formatCohortPoolTruncationArms([]CohortPoolTruncationArm{
		CohortPoolTruncationArmFulltext, CohortPoolTruncationArm("not_a_declared_arm"),
	})
	if !strings.Contains(got, cohortPoolTruncationUnknownArm) {
		t.Errorf("rendered %q, want it to carry %q -- an undeclared value must be visible, not silently dropped",
			got, cohortPoolTruncationUnknownArm)
	}
	if strings.Contains(got, "not_a_declared_arm") {
		t.Errorf("rendered %q -- the undeclared VALUE itself must never reach the line, only the marker", got)
	}
	if !strings.Contains(got, string(CohortPoolTruncationArmFulltext)) {
		t.Errorf("rendered %q -- a declared arm alongside an undeclared one must still be reported", got)
	}
	for _, arm := range CohortPoolTruncationArmVocabulary() {
		if string(arm) == cohortPoolTruncationUnknownArm {
			t.Fatalf("the unknown-arm marker %q is a vocabulary member -- it must not be, or the malformed case becomes a legal classification", cohortPoolTruncationUnknownArm)
		}
	}
	// The marker must never appear when nothing undeclared was handed over,
	// or it stops meaning anything.
	if clean := formatCohortPoolTruncationArms(CohortPoolTruncationArmVocabulary()); strings.Contains(clean, cohortPoolTruncationUnknownArm) {
		t.Errorf("rendered %q for an all-declared input -- the marker fires on nothing", clean)
	}
}
