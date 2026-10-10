package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A caller names an inactive team by its bare id; an active team of the same
// name exists. Through the real adapter: the hint answers team_inactive with
// the twin id, nothing is committed, and no search read runs.
func TestAdapterResolveSubjectsInactiveTeamHintDoesNotServeTheActiveTwin(t *testing.T) {
	var otherReads []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if _, byKey := params["targets"]; byKey {
			return []row{boundaryTeamRow("team:platform", false, "acme/api")}, nil
		}
		if id, ok := params["id"]; ok && id == "team:platform" {
			return []row{boundaryTeamRow("team:platform", false, "acme/api")}, nil
		}
		otherReads = append(otherReads, cypher)
		return []row{boundaryTeamRow("team:jira:platform", true, "acme/api")}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: "How is team Platform doing?",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
		Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectTeam, ID: "team:platform", Label: "Platform", Source: "workbench"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Platform"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, lookupBinding, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 0 || len(resolution.Candidates) != 0 {
		t.Fatalf("committed=%v candidates=%v: the active twin was served", resolution.Committed, resolution.Candidates)
	}
	if len(resolution.InactiveHints) != 1 || resolution.InactiveHints[0].Answer != "team_inactive" || resolution.InactiveHints[0].ActiveCanonicalID != "team:jira:platform" {
		t.Fatalf("inactive_hints = %#v", resolution.InactiveHints)
	}
	if len(otherReads) != 1 {
		t.Fatalf("reads besides the keyed hint reads = %d, want exactly the one twin name read and no search", len(otherReads))
	}
}

// A hint the graph does not know is not an inactive team: no hint answer.
func TestAdapterResolveSubjectsAbsentTeamHintIsNotAnInactiveAnswer(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) { return nil, nil }})
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: "How is team Platform doing?",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
		Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectTeam, ID: "team:nobody", Label: "Nobody", Source: "workbench"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Nobody"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, lookupBinding, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.InactiveHints) != 0 {
		t.Fatalf("inactive_hints = %#v for a team the graph does not hold", resolution.InactiveHints)
	}
}
