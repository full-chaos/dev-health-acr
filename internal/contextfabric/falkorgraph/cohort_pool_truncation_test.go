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
			// A census that MODELS what the production query is: term-free,
			// org-wide, over the servable kinds -- so its membership is a
			// STRICT SUPERSET of anything the lexical arm found.
			//
			// The previous fixture returned ONE unrelated team while the
			// lexical arm found three, so it did not model the census the
			// `covered_by_census` claim rests on, and an EMPTY census would
			// have passed it just as well. A fixture that cannot fail for the
			// reason the claim depends on is not evidence for the claim.
			rows := make([]row, 0, poolTruncationTeamCount+1)
			for i := 0; i < poolTruncationTeamCount; i++ {
				r := fakeSubjectNodeRow("team", fmt.Sprintf("team_%d", i), fmt.Sprintf("Team %d", i))
				r["n"].(*node).Properties["authorization_repositories"] = "*"
				rows = append(rows, r)
			}
			r := fakeSubjectNodeRow("team", "team_census_only", "Census Only")
			r["n"].(*node).Properties["authorization_repositories"] = "*"
			return append(rows, r), nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.Options.MaxCohortMembers = 50

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortExactNameCensusGates) != 1 || !telemetry.cohortExactNameCensusGates[0].admitted {
		t.Fatalf("census gate = %+v, want exactly one ADMITTED decision", telemetry.cohortExactNameCensusGates)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want the census-sourced team cohort")
	}
	members := map[string]bool{}
	for _, m := range result.Cohort.Members {
		members[m.Subject.CanonicalID] = true
	}
	// THE CLAIM, ASSERTED: every member the clipped lexical arm found is in
	// the answer via the census, plus one the lexical arm never saw. This is
	// what "the census covers the bounded arm" MEANS, and without it the
	// completeness assertion below rests on nothing.
	for i := 0; i < poolTruncationTeamCount; i++ {
		id := fmt.Sprintf("team_%d", i)
		if !members[id] {
			t.Fatalf("%s is absent (%v) -- the census does not in fact cover what the lexical arm found, so this fixture cannot support a covered_by_census verdict", id, members)
		}
	}
	if !members["team_census_only"] {
		t.Fatalf("the census-only member is absent (%v) -- the census is not a strict superset here", members)
	}
	if !result.Cohort.Complete || result.Cohort.Truncated {
		t.Errorf("Complete=%v Truncated=%v: a clipped lexical arm beneath a COMPLETED census that demonstrably contains its members loses nothing",
			result.Cohort.Complete, result.Cohort.Truncated)
	}
	if got := telemetry.cohortKindBases[0].poolTruncation; got != CohortPoolTruncationCoveredByCensus {
		t.Errorf("pool truncation basis = %q, want %q", got, CohortPoolTruncationCoveredByCensus)
	}
}

// TestDiscoverContextClippedFulltextUnderAnEmptyCensusIsNotCovered is the
// complement r3 finding 4 asked for, and it is the one that makes the test
// above mean something.
//
// An EMPTY census admitted by the gate covers nothing — there is no member in
// it to stand in for what the lexical arm dropped. Before this, a fixture whose
// census returned nothing would still have been accepted as `covered_by_census`.
func TestDiscoverContextClippedFulltextUnderAnEmptyCensusIsNotCovered(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return poolTruncationFulltextRows(poolTruncationFulltextCollectLimit + 1), nil
		default:
			// The census runs and returns NOTHING.
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.Options.MaxCohortMembers = 50

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortExactNameCensusGates) != 1 || !telemetry.cohortExactNameCensusGates[0].admitted {
		t.Fatalf("census gate = %+v, want exactly one ADMITTED decision -- the census must have RUN for this to be the empty-census case rather than the census-skipped one", telemetry.cohortExactNameCensusGates)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil -- the lexical arm's own teams should still form a cohort")
	}
	// The lexical arm still supplies members; the census supplies none. So the
	// clipped lexical arm is the ONLY source, and its truncation stands.
	if !result.Cohort.Truncated || result.Cohort.Complete {
		t.Errorf("Complete=%v Truncated=%v: an empty census covers nothing, so the clipped lexical arm's loss is the whole story",
			result.Cohort.Complete, result.Cohort.Truncated)
	}
	if got := telemetry.cohortKindBases[0].poolTruncation; got == CohortPoolTruncationCoveredByCensus {
		t.Errorf("pool truncation basis = %q -- an empty census must never read as covering anything", got)
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
	type input struct{ fulltext, hopWalk, exactName, lookupFailed, census bool }
	const (
		ft   = "fulltext"
		hw   = "hop_walk"
		enc  = "exact_name_census"
		elf  = "endpoint_lookup_failed"
		none = ""
	)
	// Rather than 32 hand-typed rows -- which is where a table stops being a
	// specification and becomes a transcription of the implementation -- the
	// expectation is DERIVED FROM THE STATED RULES, and the loop below checks
	// the classifier against them. The rules are the contract:
	//
	//   arms      = every cut arm, in vocabulary order
	//   truncated = an arm was cut AND nothing covers it
	//   covered   = ONLY bounded arms (fulltext/hop_walk) were cut, a census
	//               ran, and the census itself was not cut
	//   a failed lookup is NEVER covered: the census returns a subject only if
	//               its own read succeeds, and this arm reports a read that
	//               did not
	expected := func(in input) (CohortPoolTruncationBasis, string, bool) {
		var arms []string
		if in.fulltext {
			arms = append(arms, ft)
		}
		if in.hopWalk {
			arms = append(arms, hw)
		}
		if in.exactName {
			arms = append(arms, enc)
		}
		if in.lookupFailed {
			arms = append(arms, elf)
		}
		joined := strings.Join(arms, ",")
		switch {
		case len(arms) == 0:
			return CohortPoolTruncationNone, none, false
		case in.exactName, in.lookupFailed:
			return CohortPoolTruncationTruncated, joined, true
		case in.census:
			return CohortPoolTruncationCoveredByCensus, joined, false
		default:
			return CohortPoolTruncationTruncated, joined, true
		}
	}

	seenBases := map[CohortPoolTruncationBasis]bool{}
	rows := 0
	for _, fulltext := range []bool{false, true} {
		for _, hopWalk := range []bool{false, true} {
			for _, exactName := range []bool{false, true} {
				for _, lookupFailed := range []bool{false, true} {
					for _, census := range []bool{false, true} {
						rows++
						in := input{fulltext, hopWalk, exactName, lookupFailed, census}
						wantBasis, wantArms, wantTrunc := expected(in)
						basis, arms, truncated := cohortPoolTruncation(fulltext, hopWalk, exactName, lookupFailed, census)
						got := formatCohortPoolTruncationArms(arms)
						if basis != wantBasis || truncated != wantTrunc || got != wantArms {
							t.Errorf("cohortPoolTruncation(%+v) = (%q, %q, %v), want (%q, %q, %v)",
								in, basis, got, truncated, wantBasis, wantArms, wantTrunc)
						}
						seenBases[basis] = true
					}
				}
			}
		}
	}
	if rows != 32 {
		t.Fatalf("covered %d rows, want 32 -- the loop must span the whole input space or it is a sample", rows)
	}
	// Every declared decision must be REACHABLE over that space. A member the
	// producer can never emit is dead vocabulary, and a dead member on a
	// telemetry line reads to an operator as a state the system can reach.
	for _, basis := range CohortPoolTruncationBasisVocabulary() {
		if !seenBases[basis] {
			t.Errorf("decision %q is never produced across the whole input space", basis)
		}
	}
}

// TestCohortPoolTruncationNeverCoversAFailedLookup is the one rule above that
// is a JUDGEMENT rather than bookkeeping, so it is asserted on its own.
//
// The census covers a BOUNDED arm: a subject that exists and was simply not
// fetched, which a term-free org-wide fetch returns anyway. A failed read is
// different — the backend did not answer for that subject, and nothing about
// the census says a second read of it would have. Treating it as covered would
// turn a backend fault into a completeness claim.
func TestCohortPoolTruncationNeverCoversAFailedLookup(t *testing.T) {
	t.Parallel()
	basis, arms, truncated := cohortPoolTruncation(false, false, false, true, true)
	if basis == CohortPoolTruncationCoveredByCensus || !truncated {
		t.Errorf("a failed lookup under an admitted census classified as (%q, %v) -- a backend fault must never read as covered", basis, truncated)
	}
	if got := formatCohortPoolTruncationArms(arms); got != string(CohortPoolTruncationArmEndpointLookupFailed) {
		t.Errorf("arms = %q, want %q", got, CohortPoolTruncationArmEndpointLookupFailed)
	}
	// The complement: the same census DOES cover a bounded arm, or the
	// assertion above would hold trivially for a classifier that never covers
	// anything.
	if b, _, tr := cohortPoolTruncation(true, false, false, false, true); b != CohortPoolTruncationCoveredByCensus || tr {
		t.Errorf("a bounded arm under the same census classified as (%q, %v), want covered -- without this the test above proves nothing", b, tr)
	}
}

// TestCohortPoolTruncationReportsEveryCutArm is the arms vocabulary's own
// coverage check: each arm, alone, must appear in the reported list. An arm
// that can be cut and never named is invisible exactly when it matters.
func TestCohortPoolTruncationReportsEveryCutArm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		arm                                                CohortPoolTruncationArm
		fulltext, hopWalk, exactName, lookupFailed, census bool
	}{
		{CohortPoolTruncationArmFulltext, true, false, false, false, false},
		{CohortPoolTruncationArmHopWalk, false, true, false, false, false},
		{CohortPoolTruncationArmExactNameCensus, false, false, true, false, true},
		{CohortPoolTruncationArmEndpointLookupFailed, false, false, false, true, false},
	}
	if len(cases) != len(CohortPoolTruncationArmVocabulary()) {
		t.Fatalf("%d cases for %d declared arms -- an arm with no case is unmeasured", len(cases), len(CohortPoolTruncationArmVocabulary()))
	}
	for _, tc := range cases {
		_, arms, truncated := cohortPoolTruncation(tc.fulltext, tc.hopWalk, tc.exactName, tc.lookupFailed, tc.census)
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
