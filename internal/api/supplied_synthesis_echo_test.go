package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The rejection of a draft names a closed reason only. The identifiers the
// caller wrote into the draft, which the engine's own rejection text quotes,
// reach neither the response nor a log line.
func TestWriteBackRejectionEchoesNoIdentifierOfTheDraft(t *testing.T) {
	project := writeBackProject()
	for name, mutate := range map[string]func(*contextfabric.SynthesisDraft){
		"unknown evidence id": func(d *contextfabric.SynthesisDraft) {
			d.EvidenceRefIDs = append(d.EvidenceRefIDs, "evidence_"+writeBackMarker)
		},
		"unobserved claim field": func(d *contextfabric.SynthesisDraft) {
			d.ClaimedFacts[0].Field = "field_" + writeBackMarker
		},
		"unknown driver path": func(d *contextfabric.SynthesisDraft) {
			d.Drivers[0].PathIDs = append(d.Drivers[0].PathIDs, "path_"+writeBackMarker)
		},
		"unknown driver evidence id": func(d *contextfabric.SynthesisDraft) {
			d.Drivers[0].EvidenceRefIDs = append(d.Drivers[0].EvidenceRefIDs, "evidence_"+writeBackMarker)
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := rig.firstCall(t)
			rig.logs.Reset()
			draft := writeBackDraft(project, "plain text")
			if len(draft.ClaimedFacts) == 0 || len(draft.Drivers) == 0 {
				t.Fatal("fixture defect: the draft has no claimed fact or no driver")
			}
			mutate(&draft)
			recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, draft), nil)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d body=%s, want 422", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), writeBackMarker) {
				t.Fatalf("the refusal echoes an identifier of the draft: %s", recorder.Body.String())
			}
			logged := rig.logs.String()
			if logged == "" {
				t.Fatal("the turn wrote no log line: the capture is not wired")
			}
			if strings.Contains(logged, writeBackMarker) {
				t.Fatalf("a log line carries an identifier of the draft: %s", logged)
			}
		})
	}
}
