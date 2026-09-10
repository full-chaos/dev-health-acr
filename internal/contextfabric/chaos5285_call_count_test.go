package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestTheAdmittedGroupedSingleSynthesisPathMakesExactlyTwoFactServiceCalls is
// CHAOS-5285 test-table row 7.
//
// The bounded retry is the case this guarantee is actually about. Re-synthesis
// runs after the reads, on evidence already gathered, and a turn that re-read
// on the way round would double every provider's load for one answer -- and
// would do it invisibly, because the served document looks identical either
// way. Counting the CALLS is the only way to tell.
//
// SCOPE, STATED HONESTLY. This pin covers the SINGLE-SYNTHESIS path. The
// synthesizer's call count is logged, and in this fixture it is 1: every
// budget that leaves the first answer over the cap also leaves the narrowed
// one over it, so the turn takes the planned refusal instead of retrying. The
// retry arm of row 7 is NOT covered by this test; it is covered by
// TestTheGroupedRetryDoesNotReReadItsProviders, whose fixture sits above the
// grouped headroom so a real second synthesis runs. This name used to
// end in `AcrossARetry` while the synthesis count stayed at 1 -- a pin naming a
// path it never exercised -- and is now named for the path it does.
func TestTheAdmittedGroupedSingleSynthesisPathMakesExactlyTwoFactServiceCalls(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = []CanonicalFact{
			teamScopedFact("project_a", "team_security", "Security"),
			teamScopedFact("project_b", "team_security", "Security"),
			teamScopedFact("project_c", "team_platform", "Platform"),
			teamScopedFact("project_d", "team_platform", "Platform"),
		}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	synthesisCalls := 0
	// FOUR members in TWO groups, not two members in two groups. A grouped
	// cohort whose every group already holds exactly one member cannot narrow
	// without DROPPING a group, which the design forbids -- so it takes the
	// planned refusal and never retries at all. A retry pin built on that
	// shape measures a refusal, not a retry.
	members := []CohortMember{}
	for index, id := range []string{"project_a", "project_b", "project_c", "project_d"} {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	// A budget too small for the first answer forces the one bounded retry.
	options := EngineOptions{MaxItems: 6, SynthesisDeadlineReserve: time.Second}
	engine, request := groupReadEngineFixtureFull(t, &recordingTelemetry{}, recorder, members, nil, SubjectProject, &options, &synthesisCalls)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	for index := range recorder.requests {
		t.Logf("fact request %d: root kinds=%v roots=%v", index, recorder.rootKinds(index), recorder.rootIDs(index))
	}
	t.Logf("synthesis calls=%d fact-service calls=%d", synthesisCalls, len(recorder.requests))

	if len(recorder.requests) != 2 {
		t.Errorf("fact-service calls = %d, want exactly 2 on the admitted grouped path -- one member read and one group read, and neither repeated",
			len(recorder.requests))
	}
	if synthesisCalls != 1 {
		t.Errorf("synthesis calls = %d, want 1 -- this fixture is the SINGLE-SYNTHESIS arm, and a second synthesis here means the fixture changed shape and the comment above no longer describes it",
			synthesisCalls)
	}
	if grouped := recorder.groupRootedRequests(SubjectTeam); len(grouped) != 1 {
		t.Errorf("group-rooted calls at %v, want exactly one", grouped)
	}
}

// TestATurnThatAdmitsNoGroupMakesNoGroupProviderRequest is row 7's other half
// and row 15's zero case.
//
// Reading zero subjects is not a cheaper read -- it is a read that should not
// happen. A request issued with an empty root set still costs a provider round
// trip, still appears in its logs, and still has to be explained by whoever
// reads them.
func TestATurnThatAdmitsNoGroupMakesNoGroupProviderRequest(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	telemetry := &recordingTelemetry{}
	// EVERY proposed group denied.
	engine, request := groupReadEngineFixtureDenying(t, telemetry, recorder,
		TeamCanonicalID("team_security"), TeamCanonicalID("team_platform"))

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	for index := range recorder.requests {
		t.Logf("fact request %d: root kinds=%v roots=%v", index, recorder.rootKinds(index), recorder.rootIDs(index))
	}
	if len(recorder.requests) != 1 {
		t.Errorf("fact-service calls = %d, want exactly 1 -- with no group admitted there is nothing to read, and an empty-rooted request is a provider round trip that buys nothing",
			len(recorder.requests))
	}
	if grouped := recorder.groupRootedRequests(SubjectTeam); len(grouped) != 0 {
		t.Errorf("a group-rooted request was issued at %v with every group denied", grouped)
	}

	// AND IT SAYS SO. A turn that quietly reads nothing and a turn that
	// decided not to read are indistinguishable from the counts alone.
	if len(telemetry.cohortGroupReads) != 1 {
		t.Fatalf("group-read decisions emitted = %d, want 1", len(telemetry.cohortGroupReads))
	}
	decision := telemetry.cohortGroupReads[0]
	t.Logf("decision: proposed=%d admitted=%d denied=%d read=%v refusal=%q",
		decision.Proposed, decision.Admitted, decision.Denied, decision.Read, decision.Refusal)
	if decision.Refusal != GroupReadRefusalNoGroupAdmitted {
		t.Errorf("refusal = %q, want %q", decision.Refusal, GroupReadRefusalNoGroupAdmitted)
	}
	if decision.Admitted != 0 || decision.Denied != decision.Proposed || decision.Proposed == 0 {
		t.Errorf("proposed=%d admitted=%d denied=%d -- every proposed group was denied and the line must say so with real numbers",
			decision.Proposed, decision.Admitted, decision.Denied)
	}

	// The denied groups stay in the published list: unreadable is not absent.
	if result.Cohort == nil || len(result.Cohort.Groups) != decision.Proposed {
		got := 0
		if result.Cohort != nil {
			got = len(result.Cohort.Groups)
		}
		t.Errorf("served groups = %d, want the %d proposed -- a group nobody may read is still part of the population the answer speaks about",
			got, decision.Proposed)
	}
}

// TestAnUngroupedTurnMakesExactlyOneFactServiceCall is the DISCRIMINATING
// CONTROL for both pins above, and must pass at the parent as well.
//
// Without it, "exactly two on the grouped path" is satisfied by an
// implementation that makes two calls on EVERY path, which would double the
// provider load of every non-grouped question in the system while passing
// every assertion above.
func TestAnUngroupedTurnMakesExactlyOneFactServiceCall(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		// No group-naming column anywhere, so no group axis is built.
		bundle.Facts = []CanonicalFact{ungroupableFact("project_a"), ungroupableFact("project_b")}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for index := range recorder.requests {
		t.Logf("fact request %d: root kinds=%v roots=%v", index, recorder.rootKinds(index), recorder.rootIDs(index))
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("CONTROL BROKEN: an ungrouped turn made %d fact-service calls, want 1 -- if every path makes two, the grouped pins above are measuring nothing",
			len(recorder.requests))
	}
}

// TestTheGroupedRetryDoesNotReReadItsProviders is CHAOS-5285 test-table row 7's
// RETRY arm: "exactly two fact-service calls on the admitted grouped path,
// INCLUDING RETRIES".
//
// The retry is where the guarantee earns its keep. Re-synthesis runs after the
// reads, on evidence already gathered, and a turn that re-read on the way
// round would double every provider's load for one answer while producing a
// byte-identical document. Only the call count can tell.
//
// GETTING HERE TOOK AN EXECUTED SWEEP, and the arithmetic is worth stating
// because it is not obvious and it constrains any future fixture. A grouped
// plan reserves a synthesis headroom of 20 items, and the cohort's member
// allowance is `MaxItems - headroom`. For every budget at or below 20 that
// allowance clamps to one, stage 2's overlap-aware set cover reduces the
// cohort to ONE MEMBER PER GROUP, and stage 3 can then narrow nothing without
// dropping a group -- which the design forbids -- so the retry declines
// `nothing_to_narrow` at every such budget. The retry is reachable only above
// the headroom: the budget here leaves room for every member, and the answer
// goes over on CLAIMS instead, which narrowing does reduce.
//
// NOT t.Parallel(): it changes the fixture's claims-per-member.
func TestTheGroupedRetryDoesNotReReadItsProviders(t *testing.T) {
	previousClaims := groupReadClaimsPerMember
	groupReadClaimsPerMember = 5
	t.Cleanup(func() { groupReadClaimsPerMember = previousClaims })

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
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

	synthesisCalls := 0
	members := make([]CohortMember, 0, 6)
	for index, id := range groupReadRetryMemberIDs() {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	// Above the grouped headroom, so stage 2 leaves every member in place and
	// the first answer goes over on claims rather than on members.
	options := EngineOptions{MaxItems: 26, SynthesisDeadlineReserve: time.Hour}
	engine, request := groupReadEngineFixtureFull(t, &recordingTelemetry{}, recorder, members, nil, SubjectProject, &options, &synthesisCalls)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v -- this fixture must FIT after one retry, or it is measuring a refusal", err)
	}

	for index := range recorder.requests {
		t.Logf("fact request %d: root kinds=%v roots=%v", index, recorder.rootKinds(index), recorder.rootIDs(index))
	}
	t.Logf("synthesis calls=%d fact-service calls=%d", synthesisCalls, len(recorder.requests))

	// THE RETRY MUST BE REAL. Asserted first: a fixture that stopped
	// retrying would otherwise turn this into a duplicate of the
	// single-synthesis pin and its green would mean nothing.
	if synthesisCalls != 2 {
		t.Fatalf("synthesis calls = %d, want 2 -- the bounded retry did not run, so this test is not measuring a retry at all", synthesisCalls)
	}
	if len(recorder.requests) != 2 {
		t.Errorf("fact-service calls = %d across a retry, want exactly 2 -- a re-synthesis that re-reads doubles every provider's load for one answer, and the served document looks identical either way",
			len(recorder.requests))
	}
	if grouped := recorder.groupRootedRequests(SubjectTeam); len(grouped) != 1 {
		t.Errorf("group-rooted calls at %v across a retry, want exactly one", grouped)
	}
}

// groupReadRetryMemberIDs is six members across two groups of three, so
// narrowing can drop members without dropping a group.
func groupReadRetryMemberIDs() []string {
	return []string{"project_a", "project_b", "project_c", "project_d", "project_e", "project_f"}
}
