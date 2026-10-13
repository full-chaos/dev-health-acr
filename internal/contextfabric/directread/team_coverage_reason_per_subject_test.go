package directread

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestMixedTeamCoverageReasonIsPerSubject(t *testing.T) {
	owner := subject(contractsv1.ContextFabricSubjectTeam, "team:owner")
	empty := subject(contractsv1.ContextFabricSubjectTeam, "team:empty")
	provider := &stubProvider{capability: deficiencyLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{
			State:          contextfabric.SourceAvailable,
			SubjectReasons: map[string]string{contextfabric.FactSubjectKey(query.Subjects[1]): "the team owns no repository in synced ownership"},
			Evaluation:     &contextfabric.FactEvaluationCoverage{Members: []contextfabric.FactEvaluationMember{{Subject: query.Subjects[0], State: "fired"}, {Subject: query.Subjects[1], State: "never_evaluated"}}},
			RequestNote:    "devhealthfacts: 1 of 2 requested teams own no repository in synced ownership, so no rollup applies to them; this is not a measured zero",
			Facts: []contextfabric.CanonicalFact{{Kind: contextfabric.FactOperationalDeficiencies, Subject: query.Subjects[0],
				Fields: map[string]contextfabric.FactValue{"rule_id": strValue("r1")}, EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, strings.TrimPrefix(owner.CanonicalID, "team:"))}, SourceState: contextfabric.SourceAvailable}},
		}, nil
	}}
	response, err := newTestFactsReader(t, teamGraph(owner, empty), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds:    []string{"operational_deficiencies"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: owner.CanonicalID}, {Kind: "team", CanonicalID: empty.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerRow := coverageOf(t, response, "operational_deficiencies", owner)
	if strings.Contains(ownerRow.Reason, "own no repository") || strings.Contains(ownerRow.Reason, "owns no repository") {
		t.Errorf("owner row served a false reason: %q", ownerRow.Reason)
	}
	emptyRow := coverageOf(t, response, "operational_deficiencies", empty)
	if !strings.Contains(emptyRow.Reason, "owns no repository") {
		t.Errorf("empty team row reason = %q, want its own", emptyRow.Reason)
	}
	if emptyRow.Outcome != OutcomeReadNoFact || ownerRow.Outcome != OutcomeFactServed {
		t.Errorf("outcomes = %q, %q", ownerRow.Outcome, emptyRow.Outcome)
	}
}

func TestOverlongKindReasonStillLeavesTheOwnerRowFreeOfTheRequestNote(t *testing.T) {
	owner := subject(contractsv1.ContextFabricSubjectTeam, "team:owner")
	empty := subject(contractsv1.ContextFabricSubjectTeam, "team:empty")
	long := strings.Repeat("x", contractsv1.ContextFabricSourceObservationReasonMaxLength+500)
	provider := &stubProvider{capability: deficiencyLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{
			State:          contextfabric.SourceAvailable,
			Reason:         long,
			RequestNote:    "1 of 2 requested teams own no repository in synced ownership",
			SubjectReasons: map[string]string{contextfabric.FactSubjectKey(query.Subjects[1]): "the team owns no repository in synced ownership"},
			Evaluation: &contextfabric.FactEvaluationCoverage{Members: []contextfabric.FactEvaluationMember{
				{Subject: query.Subjects[0], State: "fired"}, {Subject: query.Subjects[1], State: "never_evaluated"},
			}},
			Facts: []contextfabric.CanonicalFact{{Kind: contextfabric.FactOperationalDeficiencies, Subject: query.Subjects[0],
				Fields: map[string]contextfabric.FactValue{"rule_id": strValue("r1")}, EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "owner")}, SourceState: contextfabric.SourceAvailable}},
		}, nil
	}}
	response, err := newTestFactsReader(t, teamGraph(owner, empty), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds:    []string{"operational_deficiencies"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: owner.CanonicalID}, {Kind: "team", CanonicalID: empty.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := coverageOf(t, response, "operational_deficiencies", owner)
	if strings.Contains(row.Reason, "own no repository") || strings.Contains(row.Reason, "owns no repository") {
		t.Errorf("owner row carries an ownership sentence: %q", row.Reason)
	}
	if len(row.Reason) > contractsv1.ContextFabricSourceObservationReasonMaxLength {
		t.Errorf("owner row reason length %d exceeds the bound", len(row.Reason))
	}
}
