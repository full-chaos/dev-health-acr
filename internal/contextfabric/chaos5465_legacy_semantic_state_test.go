package contextfabric

// A field ever written to semantic-state.v1 must stay decodable for the
// format's whole lifetime under DisallowUnknownFields -- a field taken out
// of use is tolerated on read (accepted, ignored, never consulted) and
// never written again, never simply deleted from the Go struct.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// legacyRowWithFieldOutOfUse takes a byte-for-byte real production encoding
// (the SAME BuildSemanticState + EncodeSemanticState path every other
// fixture in this package uses) and adds ONE key a field out of use once
// wrote, simulating exactly what an earlier binary's own encoder produced
// for a real row -- not a hand-authored JSON literal that merely resembles
// one.
func legacyRowWithFieldOutOfUse(t testing.TB, column []byte) []byte {
	t.Helper()
	var doc map[string]interface{}
	if err := json.Unmarshal(column, &doc); err != nil {
		t.Fatalf("fixture defect: real encoded column does not parse as JSON: %v", err)
	}
	anchor, ok := doc["scope_anchor"].(map[string]interface{})
	if !ok {
		t.Fatalf("fixture defect: real encoded column has no scope_anchor object: %v", doc)
	}
	anchor["member_source"] = "ownership"
	doc["scope_anchor"] = anchor
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("fixture defect: re-marshaling the legacy row failed: %v", err)
	}
	return out
}

// TestLegacySemanticStateRowWithMemberSourceFieldOutOfUseStillDecodesAndContinues
// pins the rule above: a v1 row carrying scope_anchor.member_source --
// exactly what every row saved before that field went out of use holds --
// must still decode, and window continuation must still admit it, never
// withhold it as malformed.
func TestLegacySemanticStateRowWithMemberSourceFieldOutOfUseStillDecodesAndContinues(t *testing.T) {
	t.Parallel()
	base := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
	state := framedCarrierState(t, prior, SubjectTeam)
	column, err := SemanticStateOf(state).EncodedColumn()
	if err != nil {
		t.Fatalf("fixture defect: framed carrier does not encode: %v", err)
	}
	legacyColumn := legacyRowWithFieldOutOfUse(t, column)

	// Prove the fixture itself is genuine before using it: decoding the
	// legacy bytes directly must succeed, never SemanticStateReadMalformed
	// -- DisallowUnknownFields must not reject a key a field out of use
	// still tolerates on read.
	decoded, status := DecodeSemanticState(legacyColumn)
	if status != SemanticStateReadAvailable {
		t.Fatalf("DecodeSemanticState(legacy row) status = %v, want %v -- a field taken out of use in SemanticScopeAnchor must stay decodable for rows an earlier binary wrote", status, SemanticStateReadAvailable)
	}
	if decoded == nil || decoded.ScopeAnchor.Kind != state.ScopeAnchor.Kind || decoded.ScopeAnchor.Term != state.ScopeAnchor.Term {
		t.Fatalf("decoded legacy row = %+v, want ScopeAnchor matching the original framed carrier %+v", decoded, state.ScopeAnchor)
	}

	carriers := &staticResultStore{
		results:         map[string]InvestigationResult{prior.ResultID: prior},
		noCarrierStates: true,
		legacyStates:    map[string][]byte{prior.ResultID: legacyColumn},
	}
	var buf bytes.Buffer
	sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	engine, _ := newRefusalEngine(t, carriers, forcedFamilyInterpreter{family: QuestionFamilyDiscoveredCohortRanking}, sink)
	binding := ResolvedGraphBinding{Epoch: 0}
	decision := engine.admitWindowContinuation(context.Background(), acceptancePrincipal(), continuationRequest(base),
		binding, nil, nil, contractsv1.ContextFabricTemporalCurrent)
	if decision.CarrierRead != ContinuationCarrierReadOK {
		t.Fatalf("carrier_read = %v, want %v -- a legacy row carrying the field taken out of use must be READABLE, not withheld as malformed", decision.CarrierRead, ContinuationCarrierReadOK)
	}
	if decision.Reason != ContinuationReasonNone {
		t.Fatalf("decision_reason = %v, want %v -- window continuation must APPLY over a legacy row, not refuse it", decision.Reason, ContinuationReasonNone)
	}
}
