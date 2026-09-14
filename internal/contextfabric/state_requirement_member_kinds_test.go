package contextfabric_test

// State requirements over pull-request and incident members are served by
// the producers that read each member kind's canonical state/status.
// The external package reads actual provider Capability declarations.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestStateOverPullRequestAndIncidentMembersIsServedByTheirOwnProducer(t *testing.T) {
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
			// The declaration must come from that producer, not a parent proxy.
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
			if !state.Served() || state.Unavailable != "" || len(state.FactKinds) != 1 || state.FactKinds[0] != member.producer {
				t.Fatalf("state/member/%s derives %+v; want its own canonical %s producer", member.kind, state, member.producer)
			}
		})
	}
}
