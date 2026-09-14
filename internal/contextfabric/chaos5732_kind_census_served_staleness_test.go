package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5732 (D47): a
// kind_census_truncated detail's Served is minted by falkorgraph's
// DiscoverContext at graph-discovery time -- BEFORE stage 3's own
// response-budget narrowing can shrink the served cohort further. Drives a
// REAL stage-3 retry through Engine.Investigate (the same production seam
// TestTheRetryNarrowingKeepsCommittedAnchors exercises) and asserts the
// SERVED document's own coverage detail names the cohort the document
// actually carries, not the pre-retry one.

// kindCensusStaleServedDetail is what falkorgraph's reader.go would have
// minted at discovery time: Served equal to the FIRST (pre-retry) cohort
// size, deliberately stale by construction so a passing assertion proves the
// correction ran, not that the numbers coincided.
func kindCensusStaleServedDetail(declared, staleServed int) CoverageDetail {
	d := CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph",
		Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated, Degrading: true,
		Kind: SubjectProject, Declared: &declared, Served: &staleServed,
		Raw: "kind_census_truncated:project:0:0",
	}
	d.Label = contractsv1.ComposeCoverageDetailLabel(d)
	return d
}

func kindCensusTruncatedDetailIn(details []CoverageDetail) (CoverageDetail, bool) {
	for _, d := range details {
		if d.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
			return d, true
		}
	}
	return CoverageDetail{}, false
}

// TestKindCensusTruncatedServedMatchesTheFinalRetryNarrowedCohort is the
// executed repro: the SAME retry fixture TestTheRetryNarrowingKeepsCommittedAnchors
// drives (6 members, 2 groups, a claims-per-member budget forcing exactly one
// stage-3 retry), with a kind_census_truncated detail seeded at a STALE
// (pre-retry) Served count. The served document's own detail must report the
// FINAL, post-retry member count -- never the discovery-time one.
//
// NOT t.Parallel(): shares groupReadClaimsPerMember/groupReadSynthesisPasses
// with the other retry-narrowing tests in this package.
func TestKindCensusTruncatedServedMatchesTheFinalRetryNarrowedCohort(t *testing.T) {
	previousClaims := groupReadClaimsPerMember
	groupReadClaimsPerMember = 5
	t.Cleanup(func() { groupReadClaimsPerMember = previousClaims })
	groupReadSynthesisPasses = nil
	t.Cleanup(func() { groupReadSynthesisPasses = nil })

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		facts := make([]CanonicalFact, 0, 6)
		for index, id := range groupReadRetryMemberIDs() {
			team := "team_security"
			if index >= 3 {
				team = "team_platform"
			}
			facts = append(facts, teamScopedFact(id, team, team))
		}
		bundle.Facts = facts
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	members := make([]CohortMember, 0, 6)
	for index, id := range groupReadRetryMemberIDs() {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	// Declared is the census figure the discovery-time census observed --
	// unaffected by this fix, since it never depends on the served cohort.
	// Served is seeded STALE at 0: no passing assertion below can be
	// satisfied by the seeded value surviving unchanged.
	const declared = 40
	options := EngineOptions{MaxItems: 26, SynthesisDeadlineReserve: time.Hour}
	telemetry := &recordingTelemetry{}
	synthesisCalls := 0
	engine, request := groupReadEngineFixtureConfigured(t, telemetry, recorder, members, nil, SubjectProject, &options, &synthesisCalls,
		func(config *groupReadFixtureConfig) {
			config.graph.context.Coverage.Details = []CoverageDetail{kindCensusStaleServedDetail(declared, 0)}
			config.graph.context.Coverage.DegradedReasons = []string{"kind_census_truncated:project:0:0"}
		})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if synthesisCalls != 2 {
		t.Fatalf("synthesis calls = %d, want 2 -- the retry did not run, so this test does not exercise stage 3's narrowing at all", synthesisCalls)
	}
	first := groupReadSynthesisPasses[0]
	last := groupReadSynthesisPasses[len(groupReadSynthesisPasses)-1]
	if len(last.Members) >= len(first.Members) {
		t.Fatalf("the retry did not narrow the cohort (%d -> %d members) -- this run proves nothing about staleness", len(first.Members), len(last.Members))
	}
	if result.Cohort == nil {
		t.Fatal("served result carries no cohort")
	}
	detail, found := kindCensusTruncatedDetailIn(result.Coverage.Details)
	if !found {
		t.Fatalf("served result.Coverage.Details = %+v, want the kind_census_truncated row to survive the merge", result.Coverage.Details)
	}
	if detail.Declared == nil || *detail.Declared != declared {
		t.Errorf("detail declared = %v, want %d (declared is a discovery-time figure, unaffected by retry)", detail.Declared, declared)
	}
	if detail.Served == nil {
		t.Fatal("detail served is nil")
	}
	if *detail.Served != len(result.Cohort.Members) {
		t.Fatalf("detail served = %d, want %d (the FINAL served cohort's own member count, post-retry) -- got the stale pre-retry/seeded value instead",
			*detail.Served, len(result.Cohort.Members))
	}
	if *detail.Served != len(last.Members) {
		t.Fatalf("detail served = %d, want %d (the last synthesis pass's own member count)", *detail.Served, len(last.Members))
	}
}

// TestKindCensusTruncatedServedUnaffectedWithoutARetry is the discriminating
// control: the SAME fixture with a budget that narrows nothing, so exactly
// one synthesis call runs. Served must equal that one cohort's member count
// -- proving the correction is a no-op (not a corruption) on the ordinary,
// no-retry path.
func TestKindCensusTruncatedServedUnaffectedWithoutARetry(t *testing.T) {
	previousClaims := groupReadClaimsPerMember
	groupReadClaimsPerMember = 1
	t.Cleanup(func() { groupReadClaimsPerMember = previousClaims })
	groupReadSynthesisPasses = nil
	t.Cleanup(func() { groupReadSynthesisPasses = nil })

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	members := make([]CohortMember, 0, 6)
	for index, id := range groupReadRetryMemberIDs() {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	const declared = 40
	options := EngineOptions{MaxItems: 200, SynthesisDeadlineReserve: time.Hour}
	telemetry := &recordingTelemetry{}
	synthesisCalls := 0
	engine, request := groupReadEngineFixtureConfigured(t, telemetry, recorder, members, nil, SubjectProject, &options, &synthesisCalls,
		func(config *groupReadFixtureConfig) {
			config.graph.context.Coverage.Details = []CoverageDetail{kindCensusStaleServedDetail(declared, len(members))}
			config.graph.context.Coverage.DegradedReasons = []string{"kind_census_truncated:project:0:0"}
		})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if synthesisCalls != 1 {
		t.Fatalf("synthesis calls = %d, want 1 -- CONTROL BROKEN, this run is no longer a no-retry baseline", synthesisCalls)
	}
	detail, found := kindCensusTruncatedDetailIn(result.Coverage.Details)
	if !found {
		t.Fatal("served result carries no kind_census_truncated detail")
	}
	if detail.Served == nil || *detail.Served != len(members) {
		t.Fatalf("detail served = %v, want %d", detail.Served, len(members))
	}
}
