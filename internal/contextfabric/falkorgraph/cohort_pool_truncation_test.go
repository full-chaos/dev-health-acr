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
	if got := telemetry.cohortKindBases[0].poolTruncation; got != CohortPoolTruncationFulltextCoveredByCensus {
		t.Errorf("pool truncation basis = %q, want %q -- reporting %q here would make \"the search was never clipped\" and \"the search was clipped and the census covered it\" the same line, which is the distinction an operator holding a suspicious member count needs",
			got, CohortPoolTruncationFulltextCoveredByCensus, CohortPoolTruncationNone)
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
// A classification over a closed vocabulary is an allow-list naming every
// member's class, never a deny-list with an else -- so the expectation table
// below is exhaustive by construction (2x2x2), and a case that reached the
// default arm of cohortPoolTruncation without being named here would show up
// as a missing key, not as a silent pass.
func TestCohortPoolTruncationClassifiesEveryInput(t *testing.T) {
	t.Parallel()
	type input struct{ fulltext, exactName, census bool }
	type expectation struct {
		basis     CohortPoolTruncationBasis
		truncated bool
	}
	// The two exactName=true, census=false rows are UNREACHABLE in
	// production (exactNameTruncated is only ever assigned inside the
	// censusAdmitted branch). They are classified anyway, conservatively, so
	// that a future caller which can reach them inherits a stated answer
	// rather than a default.
	want := map[input]expectation{
		{false, false, false}: {CohortPoolTruncationNone, false},
		{false, false, true}:  {CohortPoolTruncationNone, false},
		{true, false, false}:  {CohortPoolTruncationFulltext, true},
		{true, false, true}:   {CohortPoolTruncationFulltextCoveredByCensus, false},
		{false, true, false}:  {CohortPoolTruncationExactNameCensus, true},
		{false, true, true}:   {CohortPoolTruncationExactNameCensus, true},
		{true, true, false}:   {CohortPoolTruncationBothArms, true},
		{true, true, true}:    {CohortPoolTruncationBothArms, true},
	}
	if len(want) != 8 {
		t.Fatalf("expectation table has %d rows, want 8 -- the table must cover the whole input space or it is a sample", len(want))
	}
	for _, fulltext := range []bool{false, true} {
		for _, exactName := range []bool{false, true} {
			for _, census := range []bool{false, true} {
				key := input{fulltext, exactName, census}
				expected, named := want[key]
				if !named {
					t.Fatalf("input %+v is not classified in this table", key)
				}
				basis, truncated := cohortPoolTruncation(fulltext, exactName, census)
				if basis != expected.basis || truncated != expected.truncated {
					t.Errorf("cohortPoolTruncation(%v, %v, %v) = (%q, %v), want (%q, %v)",
						fulltext, exactName, census, basis, truncated, expected.basis, expected.truncated)
				}
			}
		}
	}
}

// TestCohortPoolTruncationBasesAreDistinct is the vocabulary's own guard: a
// telemetry enum whose members collide reports less than it claims to, and
// two identical string constants would make every table row above agree with
// a broken implementation.
func TestCohortPoolTruncationBasesAreDistinct(t *testing.T) {
	t.Parallel()
	all := CohortPoolTruncationBasisVocabulary()
	if len(all) == 0 {
		t.Fatal("CohortPoolTruncationBasisVocabulary() is empty -- the loop below cannot fail")
	}
	seen := make(map[CohortPoolTruncationBasis]struct{}, len(all))
	for _, basis := range all {
		if strings.TrimSpace(string(basis)) == "" {
			t.Errorf("a pool-truncation basis is empty; an empty telemetry value reads as an absent key")
		}
		if _, duplicate := seen[basis]; duplicate {
			t.Errorf("pool-truncation basis %q is declared twice", basis)
		}
		seen[basis] = struct{}{}
	}
}

// TestCohortKindBasisLineCarriesThePoolTruncation reads the EMITTED LINE, not
// a recorded struct: the production sink is where a key gets renamed or
// dropped, and a fake cannot see that.
func TestCohortKindBasisLineCarriesThePoolTruncation(t *testing.T) {
	t.Parallel()
	record := captureCohortKindBasisLineWithPoolTruncation(t,
		contextfabric.SubjectTeam, graphrank.CohortKindFromFrameMemberKind, true, CohortPoolTruncationFulltext)

	if got := record["pool_truncation"]; got != string(CohortPoolTruncationFulltext) {
		t.Errorf("pool_truncation = %v, want %q -- an operator reading this line must be able to tell a cohort that is everything the graph holds from one assembled out of a clipped pool", got, CohortPoolTruncationFulltext)
	}
	// The keys this line already carried must survive the addition; a new
	// field that displaced an old one is the same loss it was added to fix.
	if got := record["member_kind"]; got != string(contextfabric.SubjectTeam) {
		t.Errorf("member_kind = %v, want %q", got, contextfabric.SubjectTeam)
	}
	if got := record["discovered"]; got != true {
		t.Errorf("discovered = %v, want true", got)
	}
}
