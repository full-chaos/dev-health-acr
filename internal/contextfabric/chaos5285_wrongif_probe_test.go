package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The plan of record states a "wrong if" for each stage. These are the
// executed probes for the ones the stage-2 seam can violate. A probe that
// FIRES is a defect; a probe that passes stays as the pin for that clause.

var errGroupReadInjected = errors.New("injected group fact read failure")

// TestWrongIf_AReturnedFactNamesAnUnadmittedSubject is stage 2's second
// "wrong if": *a returned fact names an unadmitted subject.*
//
// Authorization filters what is ASKED FOR. Nothing filters what comes BACK.
// A provider that answers about a team the turn never admitted -- a widened
// query, a shared cache, a rollup that resolves one id to several -- hands
// synthesis evidence for a subject this principal was never cleared to see,
// and the group read's own admission list is the only place that can be
// checked against.
//
// The fixture returns a fact for `team_unadmitted`, which was never proposed
// and never authorized.
func TestWrongIf_AReturnedFactNamesAnUnadmittedSubject(t *testing.T) {
	// NOT t.Parallel(): it reads the shared synthesis-fact capture.
	groupReadSynthesisFacts = nil
	t.Cleanup(func() { groupReadSynthesisFacts = nil })

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				// One fact for an admitted group, one for a group nobody
				// asked about.
				bundle.Facts = []CanonicalFact{
					groupFact("team_security"),
					groupFact("team_unadmitted"),
				}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	admitted := map[string]bool{}
	for _, group := range result.Cohort.Groups {
		admitted[group.Subject.CanonicalID] = true
	}

	// ASSERTED ON WHAT SYNTHESIS RECEIVED, not on what the answer cited. The
	// first version of this probe read result.ClaimedFacts and passed --
	// vacuously, because the fixture's synthesizer builds its claims from the
	// cohort and never looks at the bundle, so no group fact could ever have
	// appeared there however badly the filter behaved.
	var groupFactsSeen int
	for _, fact := range groupReadSynthesisFacts {
		if fact.Subject.Kind != SubjectTeam {
			continue
		}
		groupFactsSeen++
		t.Logf("synthesis received a fact for group %s/%s", fact.Subject.Kind, fact.Subject.CanonicalID)
		if !admitted[fact.Subject.CanonicalID] {
			t.Errorf("synthesis received a fact naming group %q, which this turn never admitted -- authorization filters what is ASKED FOR and nothing filters what comes BACK, so a provider answering about a team the principal was never cleared for reaches the evidence the answer is built from",
				fact.Subject.CanonicalID)
		}
	}
	if groupFactsSeen == 0 {
		t.Fatalf("synthesis received NO group facts at all, so this probe cannot distinguish a working filter from a group read that returned nothing -- the fixture, not the filter, is what passed")
	}
}

// TestWrongIf_AFailedGroupReadDisappearsFromTheDisclosure is stage 3's
// "wrong if": *either read's failure, disclosure or evidence disappears in
// composition.*
//
// The group read is additive and must not fail the turn -- that is deliberate,
// and the member evidence is still a true answer to most of the question. But
// "did not fail the turn" is not "did not happen": a read that errored is a
// failure, and if the decision line reports it refused WITHOUT A NAMED REASON
// then the failure has disappeared into an unnamed refusal that no consumer
// can group on or count.
//
// This is the same class #492's own review found twice: a fail-closed
// sentinel reaching the emitter on a live path.
func TestWrongIf_AFailedGroupReadDisappearsFromTheDisclosure(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	failing := &groupReadFailingReader{inner: recorder}
	telemetry := &recordingTelemetry{}
	engine, request := groupReadEngineFixtureFull(t, telemetry, failing, []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}, nil, SubjectProject, nil, nil)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v -- a failed GROUP read must not fail the turn; the member evidence is still a true answer to most of the question", err)
	}
	if result.Cohort == nil || len(result.Cohort.Groups) == 0 {
		t.Fatalf("the member evidence did not survive a failed group read")
	}

	if len(telemetry.cohortGroupReads) != 1 {
		t.Fatalf("group-read decisions = %d, want 1", len(telemetry.cohortGroupReads))
	}
	decision := telemetry.cohortGroupReads[0]
	t.Logf("decision after a failed group read: read=%v refused=%v refusal=%q admitted=%d",
		decision.Read, decision.Refused, decision.Refusal, decision.Admitted)

	if !decision.Refused {
		t.Errorf("a group read that ERRORED reports refused=false -- the failure is not on the line at all")
	}
	if decision.Refusal == GroupReadRefusalNone {
		t.Errorf("a group read that ERRORED reports refusal=%q, the absence-of-refusal member -- the failure disappeared into an unnamed refusal that no consumer can group on or count, which is the fail-closed-sentinel-on-a-live-path class this programme has already paid for twice",
			GroupReadRefusalNone)
	}
	if !ValidGroupReadRefusal(decision.Refusal) {
		t.Errorf("refusal %q is outside the closed vocabulary", decision.Refusal)
	}
}

// groupReadFailingReader answers the member read and FAILS the group read, so
// the failure is isolated to the second call rather than breaking the turn.
type groupReadFailingReader struct{ inner *groupReadRecorder }

func (r *groupReadFailingReader) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	for _, subject := range request.Subjects {
		if subject.Kind == SubjectTeam {
			r.inner.requests = append(r.inner.requests, request)
			return CanonicalFactBundle{}, errGroupReadInjected
		}
	}
	return r.inner.ReadFacts(ctx, principal, request)
}
