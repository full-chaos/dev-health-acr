package contextfabric

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAnAuthorizerThatCannotAnswerIssuesNoGroupRead pins the stage's FAIL
// CLOSED branch, which no pin reached before: the authorizer returned an
// ERROR rather than a denial.
//
// An authorizer that could not answer is not an authorizer that said yes. If
// the stage treated the error as "no opinion" and read anyway, every group
// would reach a provider on the one turn nobody could vouch for; if it treated
// it as a denial, the line would say `no_group_admitted` and send an operator
// to a policy when the fault is a dependency.
func TestAnAuthorizerThatCannotAnswerIssuesNoGroupRead(t *testing.T) {
	t.Parallel()

	recorder := groupReadServing("team_security", "team_platform")
	telemetry := &recordingTelemetry{}
	engine, request := groupReadEngineFixtureConfigured(t, telemetry, recorder, []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}, nil, SubjectProject, nil, nil, func(config *groupReadFixtureConfig) {
		config.graph.authorizationErr = errors.New("injected: authorizer unavailable")
	})

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v -- an unavailable authorizer must cost the group axis, not the turn", err)
	}
	if grouped := recorder.groupRootedRequests(SubjectTeam); len(grouped) != 0 {
		t.Errorf("a group-rooted read was issued at %v although the authorizer could not answer -- fail closed means no group reaches a provider", grouped)
	}
	if len(telemetry.cohortGroupReads) != 1 {
		t.Fatalf("group-read decisions = %d, want 1", len(telemetry.cohortGroupReads))
	}
	decision := telemetry.cohortGroupReads[0]
	t.Logf("decision: proposed=%d admitted=%d denied=%d read=%v refused=%v refusal=%q",
		decision.Proposed, decision.Admitted, decision.Denied, decision.Read, decision.Refused, decision.Refusal)
	if !decision.Refused || decision.Refusal != GroupReadRefusalAuthorizationUnavailable || decision.Read {
		t.Errorf("decision = refused=%v refusal=%q read=%v, want refused=true refusal=%q read=false",
			decision.Refused, decision.Refusal, decision.Read, GroupReadRefusalAuthorizationUnavailable)
	}
	if decision.Proposed != 2 || decision.Admitted != 0 {
		t.Errorf("proposed=%d admitted=%d, want 2 and 0", decision.Proposed, decision.Admitted)
	}
}

// TestTheGroupReadAsksOnlyForTheGroupRowsKinds pins WHAT the second read asks
// for, not only whom: the distinct kinds of the served `read`/`each_group`
// rows, and none of the member row's.
//
// Asking providers for the member row's kinds about a TEAM would request
// facts no row of this answer owes -- and at the combined cap they would take
// capacity from the group kinds that are owed.
func TestTheGroupReadAsksOnlyForTheGroupRowsKinds(t *testing.T) {
	t.Parallel()

	recorder := groupReadServing("team_security", "team_platform")
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	grouped := recorder.groupRootedRequests(SubjectTeam)
	if len(grouped) != 1 {
		t.Fatalf("group-rooted requests = %v, want exactly one", grouped)
	}
	kinds := make([]string, 0, 6)
	for _, requirement := range recorder.requests[grouped[0]].Requirements {
		kinds = append(kinds, string(requirement.Kind))
	}
	sort.Strings(kinds)
	t.Logf("group read requirements = %v", kinds)
	want := []string{"flow", "health", "investment", "landscape", "readiness", "workload"}
	if len(kinds) != len(want) {
		t.Fatalf("group read requirements = %v, want exactly the each_group row's kinds %v", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Errorf("group read requirements = %v, want %v -- the member row's kinds (metrics) must stay with the member read", kinds, want)
			break
		}
	}
}

// TestADroppedUnadmittedFactIsCountedOnTheDecision pins the COUNT the S2b
// filter reports. The filter itself is pinned by the wrong-if probe; this is
// the half that makes a provider answering out of scope visible rather than
// silently absorbed every turn.
func TestADroppedUnadmittedFactIsCountedOnTheDecision(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Facts = []CanonicalFact{
					groupKindFact("team_security", FactHealth),
					groupKindFact("team_unadmitted", FactHealth),
					groupKindFact("team_unadmitted", FactWorkload),
				}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	telemetry := &recordingTelemetry{}
	engine, request := groupReadEngineFixture(t, telemetry, recorder)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.cohortGroupReads) != 1 {
		t.Fatalf("group-read decisions = %d, want 1", len(telemetry.cohortGroupReads))
	}
	decision := telemetry.cohortGroupReads[0]
	t.Logf("decision: returned=%d unadmitted_dropped=%d merged=%d", decision.FactsReturned, decision.UnadmittedFactsDropped, decision.FactsMerged)
	// Two dropped, one kept: three distinct numbers, so an emitter reporting
	// one in the other's place is wrong rather than coincidentally right.
	if decision.UnadmittedFactsDropped != 2 {
		t.Errorf("unadmitted_facts_dropped = %d, want 2 -- the provider answered about a team nobody admitted, twice", decision.UnadmittedFactsDropped)
	}
	// RETURNED IS THE PROVIDER'S ANSWER, three facts; merged is what survived
	// the filter, one. This pin once asserted returned=1, which counted the
	// two dropped facts out of `returned` AND into `unadmitted_dropped`
	// (round 2, P1-5).
	if decision.FactsReturned != 3 || decision.FactsMerged != 1 {
		t.Errorf("returned=%d merged=%d, want 3 and 1 -- the provider sent three facts, and only the admitted team's one survives the filter", decision.FactsReturned, decision.FactsMerged)
	}
}
