package contextfabric

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// anchorBindingCertifyScenarios are the scripted conversations the transition
// line is certified from. Only the LAST turn's output is returned.
func anchorBindingCertifyScenarios() map[string][]anchorProbeStep {
	return map[string][]anchorProbeStep{
		"decisive identity proven": {
			{request: firstTurnRequest("request_cert_decisive", true), response: identityProvenResponse(probeAlpha)},
		},
		"window gated pending": {
			{request: firstTurnRequest("request_cert_gated", false), windowed: true, response: identityProvenResponse(probeAlpha)},
		},
		"natural follow-up carried silent": {
			{request: firstTurnRequest("request_cert_silent_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_silent_two", "And how many teams contribute to it?", nil), response: emptyProbeResponse()},
		},
		"follow-up alias contested": {
			{request: firstTurnRequest("request_cert_alias_one", true), response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_alias_two", "And how many teams contribute to it?", nil), response: identityProvenResponse(probeBeta)},
		},
		"model kind conflict": {
			{request: firstTurnRequest("request_cert_kind_one", true), anchorKind: SubjectRepository, response: identityProvenResponse(probeAlpha)},
			{request: followUp("request_cert_kind_two", "", nil), anchorKind: SubjectProject, response: needTurnResponse{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}},
				bases:      provenCommitBases(probeAlpha),
			}},
		},
	}
}

// RunAnchorBindingScenarioForTest drives one certify scenario through the real
// engine with the production slog JSON handler and returns the last turn's
// Info output and request id.
func RunAnchorBindingScenarioForTest(t *testing.T, scenario string) (log []byte, requestID string) {
	t.Helper()
	steps, ok := anchorBindingCertifyScenarios()[scenario]
	if !ok {
		t.Fatalf("unknown anchor binding scenario %q", scenario)
	}
	var buf bytes.Buffer
	rig := newAnchorProbeRig(t, false)
	rig.engine.telemetry = NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	var turns []anchorProbeTurn
	for i, step := range steps {
		rig.interpreter.read(step.anchorKind, step.windowed)
		rig.graph.response = step.response
		request := step.request(turns)
		buf.Reset()
		requestID = fmt.Sprintf("req_%032x", 0x58840000+i)
		result, err := rig.engine.Investigate(observability.WithRequestID(context.Background(), requestID), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
		}
		rig.store.results[result.ResultID] = *rig.store.saved
		if rig.store.savedSemantic != nil && rig.store.savedSemantic.State != nil {
			rig.store.states[result.ResultID] = rig.store.savedSemantic.State
		}
		turns = append(turns, anchorProbeTurn{result: result})
	}
	return append([]byte(nil), buf.Bytes()...), requestID
}
