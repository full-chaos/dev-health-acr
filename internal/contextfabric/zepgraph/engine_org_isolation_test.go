package zepgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file wires a real contextfabric.Engine against the real zepgraph
// Adapter (fake transport) rather than a test double for GraphReader. It
// lives in package zepgraph -- not contextfabric -- specifically so it can
// import contextfabric (zepgraph already depends on contextfabric; the
// reverse is never true, so this does not create an import cycle) and get
// the real authorization/lookup code path, which a contextfabric-package
// stub cannot exercise.

// hostileResultStore is an InvestigationResultStore that violates the
// org-scoping binding precondition documented on the port (ports.go):
// Get ignores the calling principal entirely and always returns the same
// fixed result, as if it read a differently-organization-scoped record
// (or none at all) without checking who asked.
type hostileResultStore struct {
	alwaysReturn contextfabric.InvestigationResult
}

func (s hostileResultStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult) error {
	return nil
}

func (s hostileResultStore) Get(context.Context, storage.Principal, string) (contextfabric.InvestigationResult, error) {
	return s.alwaysReturn, nil
}

type fixedInterpreter struct {
	interpretation contextfabric.InterpretedQuestion
}

func (f fixedInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, error) {
	return f.interpretation, nil
}

type emptyFactReader struct{}

func (emptyFactReader) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{
		Facts: []contextfabric.CanonicalFact{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[contextfabric.FactKind]string{}, Watermarks: map[contextfabric.FactKind]string{},
	}, nil
}

type noMatchSynthesizer struct{}

func (noMatchSynthesizer) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	return contextfabric.InvestigationResult{
		Status: contextfabric.InvestigationNoMatch, StrongestPressures: []string{}, Drivers: []contextfabric.DriverJudgment{},
		RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Paths: []contextfabric.RelationshipPath{},
		Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "No confidently resolved subject was found in the authorized organization graph.", Warnings: []string{},
		Versions: contextfabric.VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		},
	}, nil
}

// TestEngineWithRealGraphReaderNeverLeaksCrossOrgSubjectFromHostileResultStore
// is the proof requested for Codex finding G6. ContextFabricInvestigationResult
// carries no organization discriminator (ports.go documents this as the
// InvestigationResultStore.Get binding precondition: implementations MUST
// scope Get to principal.OrgID), so Engine cannot defensively verify a
// returned prior result's organization by inspecting the value itself. The
// real defense is downstream: every subject a PriorSubjectReceipts entry
// resolves to is re-authorized through GraphReader.ResolveSubjects's
// exact-hint path, and that path looks the subject up by a UUID
// deterministically keyed on the *calling* principal's own organization
// (nodeUUID(principal.OrgID, subject)), never by anything read back from
// the prior result. This test proves that closes the leak even against an
// InvestigationResultStore that actively violates its own binding
// precondition: a hostile store returns a real org B subject for any
// principal, but org A's Engine.Investigate call still never resolves or
// surfaces it, because that subject was never projected under org A's
// graph identity.
func TestEngineWithRealGraphReaderNeverLeaksCrossOrgSubjectFromHostileResultStore(t *testing.T) {
	t.Parallel()
	api := newFakeAPI()
	adapter := mustAdapter(t, api)

	// A real, canonical subject that genuinely exists -- but only in
	// organization "org_b"'s graph.
	orgBSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_org_b_secret", Label: "Org B Secret Project"}
	api.nodes[nodeUUID("org_b", orgBSubject)] = graphNode(nodeUUID("org_b", orgBSubject), orgBSubject.Kind, orgBSubject.CanonicalID, orgBSubject.Label, "*", 1)

	hostilePrior := contextfabric.InvestigationResult{
		SubjectResolution: contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_cross_org1", Subject: orgBSubject, State: contextfabric.ResolutionCommitted,
				MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
			}},
			Committed: []contextfabric.SubjectRef{orgBSubject},
		},
	}
	store := hostileResultStore{alwaysReturn: hostilePrior}

	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: fixedInterpreter{interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		}},
		Graph: adapter, Facts: emptyFactReader{}, Synthesizer: noMatchSynthesizer{}, Results: store,
	}, contextfabric.EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_11111111" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validRequest()
	request.PriorSubjectReceipts = []contextfabric.BoundSubjectReceipt{{ResultID: "result_from_org_b", ReceiptID: "receipt_cross_org1"}}

	// The calling principal is a genuinely different organization from the
	// hostile store's fixed response.
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_a"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.SubjectResolution.Committed) != 0 || len(result.SubjectResolution.Candidates) != 0 {
		t.Fatalf("result subject resolution = %#v, want empty: org B's subject must never leak into org A's investigation via a hostile result store", result.SubjectResolution)
	}
}
