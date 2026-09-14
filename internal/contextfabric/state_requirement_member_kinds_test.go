package contextfabric_test

// A pin of the CURRENT state, not of a rule: the `state` requirement over
// pull_request and incident MEMBERS, derived against the live registry.
//
// The invariant this is named for is that a state requirement over a member
// kind is served by the producer that reads that kind's own state. Today it is
// not: the pull_request and incident producers declare only
// `principal_drivers` for their own subject kinds, so the derivation reports
// `state/member/pull_request` and `state/member/incident` as
// `no_declaring_producer` while those producers read the members' state. This
// test asserts that current state exactly, so the change that declares the
// obligation turns it red at the right line and inverts it in the same commit.
//
// It lives in the external test package for the reason the requirement trace
// does: devhealthfacts imports contextfabric. liveCapabilityList reads
// Capability() declarations only and starts nothing.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestStateOverPullRequestAndIncidentMembersIsServedByTheirOwnProducer_CurrentStateIsNoDeclaringProducer(t *testing.T) {
	capabilities := liveCapabilityList(t)
	seed := contextfabric.GenerateObligationSeed(capabilities)

	for _, member := range []struct {
		kind     contextfabric.SubjectKind
		producer contextfabric.FactKind
	}{
		{contextfabric.SubjectPullRequest, contextfabric.FactPullRequests},
		{contextfabric.SubjectIncident, contextfabric.FactIncidents},
	} {
		member := member
		t.Run(string(member.kind), func(t *testing.T) {
			// The producer that reads this member kind exists and supports it.
			// Without this, `no_declaring_producer` below would be the honest
			// reason rather than the defect the pin documents.
			supported := false
			for _, capability := range capabilities {
				if capability.Kind != member.producer {
					continue
				}
				for _, kind := range capability.SupportedSubjectKinds {
					if kind == member.kind {
						supported = true
					}
				}
			}
			if !supported {
				t.Fatalf("no registered %q capability supports %q; the pin's premise does not hold", member.producer, member.kind)
			}

			frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
				Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{
					Kind:       contextfabric.SubjectExpressionDiscoveredKind,
					Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: member.kind},
				},
				Temporal: contextfabric.TemporalIntentCurrent,
				Version:  contextfabric.QuestionFrameVersion,
			}, nil)

			var state *contextfabric.DerivedRequirement
			rows := contextfabric.DeriveRequirements(frame, seed, capabilities)
			for index := range rows {
				row := rows[index]
				if row.Obligation == contextfabric.ObligationState && row.Role == contextfabric.SubjectRoleMember && row.Subject == member.kind {
					state = &rows[index]
				}
			}
			if state == nil {
				t.Fatalf("the frame derives no state/member/%s requirement; the fixture does not reach the cell", member.kind)
			}
			// CURRENT STATE. When the producer declares `state` for its own kind,
			// this reads served and the pin is inverted in that change.
			if state.Unavailable != contextfabric.RequirementReasonNoDeclaringProducer {
				t.Fatalf("state/member/%s derives unavailable=%q (served=%v); the pinned current state is %q -- if the %q producer now declares state, invert this pin in the same change",
					member.kind, state.Unavailable, state.Served(), contextfabric.RequirementReasonNoDeclaringProducer, member.producer)
			}
		})
	}
}
