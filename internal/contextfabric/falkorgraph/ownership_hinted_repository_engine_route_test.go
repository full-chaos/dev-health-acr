package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAHintedRepositoryDoesNotTakeOverAProjectAnchoredTeamQuestion: the
// question asks for the teams of project alpha and the caller's request also
// carries the alpha repository as a hint. The repository is committed on the
// caller's id, which binds without a term match; it is still not the anchor
// the question names, so ownership is not routed through it and the project's
// teams stay.
func TestAHintedRepositoryDoesNotTakeOverAProjectAnchoredTeamQuestion(t *testing.T) {
	s := seedOwnedRepository()
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:delivery", label: "delivery"})
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "project", routeProjectAlpha, "team", "team:delivery", ""})
	telemetry := &recordingTelemetry{}
	graph := &routeBasisRecorder{Adapter: newFakeAdapterWithTelemetry(t, s.conn(), telemetry)}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: projectDeploymentsInterpreter{name: "alpha", kind: contextfabric.SubjectProject, member: contextfabric.SubjectTeam},
		Graph:       graph, Facts: emptyFactReader{},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{
			Runtime: routeNoModelRuntime{t: t}, Options: contextfabric.RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
			ClientSynthesis: synthesisprompt.ClientAssembly(),
		},
		Results: discardingResultStore{}, Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_86130021" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	ctx, _ := contextfabric.WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_86130021",
		Question: "which teams work on project alpha", SynthesisMode: contextfabric.SynthesisModeClient,
		RequestedScope: contextfabric.RequestedScope{SubjectHints: []contextfabric.SubjectHint{{
			Kind: contextfabric.SubjectRepository, ID: routeOwnedRepository, Label: routeOwnedSlug, Source: "caller",
		}}},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
		Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	})
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	hinted := false
	for _, c := range graph.committed {
		if c.CanonicalID == routeOwnedRepository && graph.bases.For(c) == contextfabric.CommitBasisCallerCanonicalID {
			hinted = true
		}
	}
	if !hinted {
		t.Fatalf("resolver committed %+v, want the hinted repository on the caller's id: the fixture must reach the case", graph.committed)
	}
	for _, d := range telemetry.ownershipRoutings {
		if d.Outcome != OwnershipRoutingNotRouted {
			t.Fatalf("ownership decisions = %+v, want no ownership read: the question names a project", telemetry.ownershipRoutings)
		}
	}
	if result.Cohort != nil {
		for _, m := range result.Cohort.Members {
			if m.Subject.CanonicalID == "team:owner" && len(m.InclusionReasons) == 1 && m.InclusionReasons[0] == ownershipInclusionReason {
				t.Fatalf("the hinted repository's owner was served as an owner for a project question")
			}
		}
	}
}
