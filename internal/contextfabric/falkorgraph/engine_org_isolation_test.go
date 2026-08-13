package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file is the falkorgraph twin of zepgraph/engine_org_isolation_test.go's
// TestEngineWithRealGraphReaderNeverLeaksCrossOrgSubjectFromHostileResultStore
// (Codex finding G6). It lives in package falkorgraph (not falkorgraph_test)
// for the same reason zepgraph's does: to get a real, unmocked
// contextfabric.Engine wired to a real *Adapter over a fake conn, exercising
// the actual authorization/lookup code path.
//
// falkorgraph derives node identity differently from zepgraph (subjectUUID,
// a plain kind+canonical_id pair scoped structurally by the graph key --
// see queries.go/identity.go -- not zepgraph's org-derived nodeUUID hash),
// so this needs its own proof: the security property under test is that
// GraphReader.ResolveSubjects re-authorizes every prior receipt against the
// CALLING principal's own graph identity.
//
// The fake conn below is deliberately HOSTILE with respect to the org query
// parameter: it ignores params["org"] entirely and returns the planted org B
// row for any kind+id match, regardless of what org the caller claims. A
// bare fakeConn cannot interpret Cypher (it never even inspects the cypher
// string), so it cannot prove the production query's org_id:$org predicate
// is honored -- only a real FalkorDB server can (see
// adapter_live_integration_test.go's cross-organization isolation step).
// What this fake CAN prove -- and is gated on -- is the actual mechanism
// available to a fake at this boundary: falkorgraph's graph-per-org design,
// where ResolveSubjects/ExactHint issue every query against
// graphKey(prefix, principal.OrgID), a server-derived key string computed
// fresh per call from the CALLING principal, never from anything the
// hostile prior result claimed. The fake returns the org B row only when
// queried under org B's own graph key -- exactly the physical separation a
// real FalkorDB server provides between graph keys, no predicate needed.
// Revert-verify: neutering that graph-key derivation (e.g. hardcoding it,
// or deriving it from something other than principal.OrgID) makes org A's
// lookup hit org B's key and this test fails.

type hostileResultStore struct {
	alwaysReturn contextfabric.InvestigationResult
}

func (s hostileResultStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot) error {
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
		ClaimedFacts:        []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "No confidently resolved subject was found in the authorized organization graph.", Warnings: []string{},
		Versions: contextfabric.VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		},
	}, nil
}

// TestEngineWithRealGraphReaderNeverLeaksCrossOrgSubjectFromHostileResultStore
// is the falkorgraph twin of zepgraph's same-named test (Codex finding G6):
// a hostile InvestigationResultStore that ignores org scoping and always
// returns a fixed, real subject planted only in org B's graph must never let
// that subject leak into org A's investigation, because
// GraphReader.ResolveSubjects re-authorizes every prior receipt against the
// calling principal's own identity before ever trusting the hostile store's
// claim.
func TestEngineWithRealGraphReaderNeverLeaksCrossOrgSubjectFromHostileResultStore(t *testing.T) {
	t.Parallel()

	orgBSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_org_b_secret", Label: "Org B Secret Project"}
	const fakeGraphPrefix = "acr-cf-fake" // must match newFakeAdapter's Config.GraphPrefix below
	orgBGraphKey := graphKey(fakeGraphPrefix, "org_b")

	// Hostile: gated on the graph key alone, never on params["org"] -- see
	// the doc comment above for why. Planted under org B's own graph key,
	// exactly where the real system's graph-per-org design would put it.
	fake := &fakeConn{queryFunc: func(ctx context.Context, gk, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if gk == orgBGraphKey && params["kind"] == string(orgBSubject.Kind) && params["id"] == orgBSubject.CanonicalID {
			return []row{fakeSubjectNodeRow(string(orgBSubject.Kind), orgBSubject.CanonicalID, orgBSubject.Label)}, nil
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

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

	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678",
		Question: "What is driving this?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer:             contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		PriorSubjectReceipts: []contextfabric.BoundSubjectReceipt{{ResultID: "result_from_org_b", ReceiptID: "receipt_cross_org1"}},
	}

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
