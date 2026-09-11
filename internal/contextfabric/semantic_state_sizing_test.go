package contextfabric_test

// FIXTURE SIZING for the 65,536-byte snapshot cap, against the LIVE
// requirement registry. Every frame of the requirement-trace corpus (built
// through the shipped frame layer) is given the declarations the live registry
// derives for it, captured as a snapshot, and measured. The cap is a design
// choice, not a measured production maximum; this is the measurement that says
// how far below it the corpus sits, and it fails if any corpus frame's
// snapshot would be rejected.

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestSemanticStateSizingAgainstTheLiveRegistry(t *testing.T) {
	capabilities := liveCapabilityList(t)
	seed := contextfabric.GenerateObligationSeed(capabilities)
	largest, largestID := 0, ""
	for _, tc := range traceFrames() {
		frame := tc.frame
		frame.Version = contextfabric.QuestionFrameVersion
		derived := contextfabric.DeriveRequirements(frame, seed, capabilities)
		group, _ := frame.SubjectExpression.GroupKind()
		state := contextfabric.BuildSemanticState(contextfabric.SemanticStateInput{
			Outcome: contextfabric.QuestionFamilyOutcome{
				Family: contextfabric.QuestionFamilySubjectInvestigation, Source: contextfabric.QuestionFamilySourceModel,
				Frame: &frame, Gate: contextfabric.FrameGate{Outcome: contextfabric.FrameGatePassed},
			},
			EmittedShape:  contextfabric.ShapeOpen,
			GroupKind:     group,
			FamilyVersion: contextfabric.QuestionFamilyTableVersion,
			Requirements:  derived,
		})
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.id, err)
		}
		_, encodeErr := contextfabric.EncodeSemanticState(state)
		t.Logf("frame %-6s %-40s requirements=%3d roles=%2d encoded=%6d bytes (%.1f%% of cap) err=%v",
			tc.id, tc.shape, len(state.Requirements), len(state.Roles), len(encoded), 100*float64(len(encoded))/float64(contextfabric.SemanticStateMaxEncodedBytes), encodeErr)
		if len(encoded) > contextfabric.SemanticStateMaxEncodedBytes {
			t.Errorf("frame %s: a corpus reading would be REJECTED by the cap (%d bytes)", tc.id, len(encoded))
		}
		if len(encoded) > largest {
			largest, largestID = len(encoded), tc.id
		}
	}
	if largest == 0 {
		t.Fatalf("no corpus frame was measured")
	}
	t.Logf("LARGEST corpus snapshot: frame %s at %d bytes, %.1f%% of the %d-byte cap", largestID, largest, 100*float64(largest)/float64(contextfabric.SemanticStateMaxEncodedBytes), contextfabric.SemanticStateMaxEncodedBytes)
}
