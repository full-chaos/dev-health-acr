package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestTwoIdenticalFirstCallsGiveOneInputDigest(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	rig.advance(7 * time.Minute)
	second := rig.firstCall(t)

	if first.ResultID == second.ResultID || first.RequestID == second.RequestID || first.GeneratedAt.Equal(second.GeneratedAt) {
		t.Fatalf("the two calls did not differ in their per-call values: result %q/%q request %q/%q generated %v/%v",
			first.ResultID, second.ResultID, first.RequestID, second.RequestID, first.GeneratedAt, second.GeneratedAt)
	}
	if first.SynthesisInput.InputSHA256 != second.SynthesisInput.InputSHA256 {
		t.Fatalf("input_sha256 differs across two identical calls: %s != %s\nfirst:  %s\nsecond: %s",
			first.SynthesisInput.InputSHA256, second.SynthesisInput.InputSHA256, first.SynthesisInput.Input, second.SynthesisInput.Input)
	}
	if !bytes.Equal(first.SynthesisInput.Input, second.SynthesisInput.Input) {
		t.Fatal("the two inputs differ in bytes under one digest")
	}
	sources := inputCoverageSources(t, first.SynthesisInput.Input)
	if len(sources) == 0 {
		t.Fatal("the input carries no coverage source")
	}
	sawGraph := false
	for _, source := range sources {
		if source["source"] == "context-fabric:graph" {
			sawGraph = true
		}
		if _, present := source["observed_at"]; present {
			t.Fatalf("coverage source %v carries observed_at in the input", source["source"])
		}
		if source["source"] == nil || source["state"] == nil {
			t.Fatalf("coverage source lost its identity or state: %v", source)
		}
	}
	if !sawGraph {
		t.Fatal("the graph source row is missing from the input")
	}
}

func TestWriteBackIsServedAfterTheClockMoved(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	savedAfterFirst := rig.store.count()
	rig.advance(11 * time.Minute)

	recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("write-back status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	served := decodeEnvelope(t, recorder)
	if served.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient || served.Status != contractsv1.ContextFabricInvestigationComplete {
		t.Fatalf("served source %q status %q, want client / complete", served.Versions.SynthesisSource, served.Status)
	}
	if len(served.Drivers) != 1 || served.Drivers[0].DriverID != "driver_writeback01" {
		t.Fatalf("drivers = %+v, want the draft's", served.Drivers)
	}
	if rig.store.count() != savedAfterFirst+1 {
		t.Fatalf("saved results = %d, want %d", rig.store.count(), savedAfterFirst+1)
	}
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = %d / %d, want none", interprets, synthesizes)
	}
}

func TestWriteBackAfterAFactChangeIsRefusedWithTheNewInput(t *testing.T) {
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_zebra_audit", Label: "Zebra Audit"}
	cases := map[string]func(rig *writeBackRouteRig, body *contractsv1.ContextFabricInvestigationRequest){
		"a metric value changed": func(rig *writeBackRouteRig, _ *contractsv1.ContextFabricInvestigationRequest) {
			changed := liveCanonicalFacts(rig.project)
			changed.Facts[1].Fields["release_ready"] = contextfabric.BooleanFactValue(true)
			rig.setFacts(changed)
		},
		"a member added": func(rig *writeBackRouteRig, _ *contractsv1.ContextFabricInvestigationRequest) {
			changed := liveCanonicalFacts(rig.project)
			added := changed.Facts[0]
			added.Subject = other
			added.EvidenceRefIDs = []string{"evidence_status_0002"}
			changed.Facts = append(changed.Facts, added)
			rig.setFacts(changed)
		},
		"a coverage state changed": func(rig *writeBackRouteRig, _ *contractsv1.ContextFabricInvestigationRequest) {
			changed := liveCanonicalFacts(rig.project)
			changed.Coverage.Sources[0].State = contextfabric.SourceStale
			changed.Coverage.Sources[0].Reason = "status rows are older than the freshness bound"
			rig.setFacts(changed)
		},
		"a source watermark changed": func(rig *writeBackRouteRig, _ *contractsv1.ContextFabricInvestigationRequest) {
			changed := liveCanonicalFacts(rig.project)
			changed.Coverage.Sources[1].Watermark = "2026-08-12T11:00:00Z"
			rig.setFacts(changed)
		},
		"a fact observation time changed": func(rig *writeBackRouteRig, _ *contractsv1.ContextFabricInvestigationRequest) {
			changed := liveCanonicalFacts(rig.project)
			at := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
			changed.Facts[1].ObservedAt = &at
			rig.setFacts(changed)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := rig.firstCall(t)
			rig.advance(3 * time.Minute)
			savedBefore := rig.store.count()
			recorder := rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
				change(rig, body)
				body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
					Output: writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), ModelOutputVersion: first.SynthesisInput.Contract.ModelOutputVersion,
					PromptVersion: first.SynthesisInput.Contract.PromptVersion, SystemSHA256: first.SynthesisInput.Contract.SystemSHA256,
					InputSHA256: first.SynthesisInput.InputSHA256, ClientModel: "writer-model-1",
				}
			})
			body := decodeRefusal(t, recorder, http.StatusConflict, "invalid_request")
			if got := detailString(t, body, "reason"); got != contractsv1.ContextFabricSuppliedSynthesisReasonInputChanged {
				t.Fatalf("reason = %q, want input_changed", got)
			}
			var bundle contractsv1.ContextFabricSynthesisInput
			if err := json.Unmarshal(body.Error.Details["synthesis_input"], &bundle); err != nil {
				t.Fatal(err)
			}
			if err := bundle.Validate(); err != nil {
				t.Fatal(err)
			}
			if bundle.InputSHA256 == first.SynthesisInput.InputSHA256 {
				t.Fatal("the new input carries call 1's digest")
			}
			if rig.store.count() != savedBefore {
				t.Fatalf("saves = %d, want %d", rig.store.count(), savedBefore)
			}
		})
	}
}

func inputCoverageSources(t *testing.T, input json.RawMessage) []map[string]any {
	t.Helper()
	var decoded struct {
		Coverage struct {
			Sources []map[string]any `json:"sources"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(input, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Coverage.Sources
}

// spanInterpretation is the rig's interpretation with its time context
// replaced. The rig clock starts at 2026-08-12T12:00:00Z.
func spanInterpretation(timeContext string) func(*contractsv1.ContextFabricInvestigationRequest) {
	return func(body *contractsv1.ContextFabricInvestigationRequest) {
		body.SuppliedInterpretation.Output = json.RawMessage(strings.Replace(writeBackOutput, `{"axis":"current"}`, timeContext, 1))
		body.TimeContext.EvidenceWindow = nil
	}
}

const (
	futureEndingRange = `{"axis":"range","start":"2026-08-01T00:00:00Z","end":"2026-08-31T23:59:59Z"}`
	futureAsOf        = `{"axis":"valid_time","as_of":"2026-08-12T18:00:00Z"}`
)

func TestASpanThatEndsInTheFutureGivesOneInputDigest(t *testing.T) {
	for name, timeContext := range map[string]string{"range": futureEndingRange, "as_of": futureAsOf} {
		for _, surface := range []string{"mcp", "workbench"} {
			t.Run(name+"/"+surface, func(t *testing.T) {
				rig := newWriteBackRouteRig(t)
				edit := func(body *contractsv1.ContextFabricInvestigationRequest) {
					spanInterpretation(timeContext)(body)
					body.Consumer.Surface = surface
				}
				first := decodeEnvelope(t, rig.post(t, edit))
				rig.advance(9 * time.Minute)
				second := decodeEnvelope(t, rig.post(t, edit))
				if first.SynthesisInput == nil || second.SynthesisInput == nil {
					t.Fatal("a first call returned no synthesis input")
				}
				if first.SynthesisInput.InputSHA256 != second.SynthesisInput.InputSHA256 {
					t.Fatalf("input_sha256 differs across two identical calls:\n%s\n%s", first.SynthesisInput.Input, second.SynthesisInput.Input)
				}
				var decoded struct {
					Interpretation struct {
						TimeContext map[string]any `json:"time_context"`
					} `json:"interpretation"`
				}
				if err := json.Unmarshal(first.SynthesisInput.Input, &decoded); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"end", "as_of"} {
					if _, present := decoded.Interpretation.TimeContext[key]; present {
						t.Fatalf("time_context carries the read time as %s: %v", key, decoded.Interpretation.TimeContext)
					}
				}
				if name == "range" && decoded.Interpretation.TimeContext["start"] != "2026-08-01T00:00:00Z" {
					t.Fatalf("time_context lost the stated start: %v", decoded.Interpretation.TimeContext)
				}
			})
		}
	}
}

// TestAClampedSpanIsOneStandingCommitmentByDesign pins today's rule: a span
// whose end is pulled back to the time of the read is one standing
// commitment. Its moving end is not part of the digest, so a draft written at
// call 1 is served at call 2 while the facts are equal, although call 2 read
// through a later end. Pinning one clock per write-back (call 2 re-runs on
// call 1's instant) would put the end back in the digest; when that lands,
// this test changes to expect the end in the input.
func TestAClampedSpanIsOneStandingCommitmentByDesign(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := decodeEnvelope(t, rig.post(t, spanInterpretation(futureEndingRange)))
	if first.SynthesisInput == nil {
		t.Fatal("call 1 returned no synthesis input")
	}
	rig.advance(13 * time.Minute)
	recorder := rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
		spanInterpretation(futureEndingRange)(body)
		body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
			Output: writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), ModelOutputVersion: first.SynthesisInput.Contract.ModelOutputVersion,
			PromptVersion: first.SynthesisInput.Contract.PromptVersion, SystemSHA256: first.SynthesisInput.Contract.SystemSHA256,
			InputSHA256: first.SynthesisInput.InputSHA256, ClientModel: "writer-model-1",
		}
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("write-back status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if served := decodeEnvelope(t, recorder); served.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient {
		t.Fatalf("synthesis_source = %q, want client", served.Versions.SynthesisSource)
	}
}

func TestWriteBackAfterAStatedSpanChangedIsRefused(t *testing.T) {
	cases := map[string]struct{ first, second string }{
		"the stated start moved":     {futureEndingRange, `{"axis":"range","start":"2026-08-02T00:00:00Z","end":"2026-08-31T23:59:59Z"}`},
		"a past end moved":           {`{"axis":"range","start":"2026-08-01T00:00:00Z","end":"2026-08-10T00:00:00Z"}`, `{"axis":"range","start":"2026-08-01T00:00:00Z","end":"2026-08-11T00:00:00Z"}`},
		"a past as-of moved":         {`{"axis":"valid_time","as_of":"2026-08-10T00:00:00Z"}`, `{"axis":"valid_time","as_of":"2026-08-11T00:00:00Z"}`},
		"a future end became a past": {futureEndingRange, `{"axis":"range","start":"2026-08-01T00:00:00Z","end":"2026-08-11T00:00:00Z"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := decodeEnvelope(t, rig.post(t, spanInterpretation(tc.first)))
			if first.SynthesisInput == nil {
				t.Fatal("call 1 returned no synthesis input")
			}
			rig.advance(2 * time.Minute)
			savedBefore := rig.store.count()
			recorder := rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
				spanInterpretation(tc.second)(body)
				body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
					Output: writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), ModelOutputVersion: first.SynthesisInput.Contract.ModelOutputVersion,
					PromptVersion: first.SynthesisInput.Contract.PromptVersion, SystemSHA256: first.SynthesisInput.Contract.SystemSHA256,
					InputSHA256: first.SynthesisInput.InputSHA256, ClientModel: "writer-model-1",
				}
			})
			body := decodeRefusal(t, recorder, http.StatusConflict, "invalid_request")
			if got := detailString(t, body, "reason"); got != contractsv1.ContextFabricSuppliedSynthesisReasonInputChanged {
				t.Fatalf("reason = %q, want input_changed", got)
			}
			if rig.store.count() != savedBefore {
				t.Fatal("a refused write-back saved a result")
			}
		})
	}
}

func TestWriteBackAfterOnlyTheWindowChangedIsRefused(t *testing.T) {
	stated := func(start, end string) *contractsv1.ContextFabricRequestedEvidenceWindow {
		from, err := time.Parse(time.RFC3339, start)
		if err != nil {
			t.Fatal(err)
		}
		to, err := time.Parse(time.RFC3339, end)
		if err != nil {
			t.Fatal(err)
		}
		return &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &from, End: &to}
	}
	cases := map[string]struct {
		first, second *contractsv1.ContextFabricRequestedEvidenceWindow
	}{
		"trailing 90 days to trailing 30 days": {
			&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
			&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing30D},
		},
		"a relative window to a stated one": {
			&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing30D},
			stated("2026-07-13T12:00:00Z", "2026-08-12T12:00:00Z"),
		},
		"a stated window that moved": {
			stated("2026-07-01T00:00:00Z", "2026-08-01T00:00:00Z"),
			stated("2026-07-02T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := decodeEnvelope(t, rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
				body.TimeContext.EvidenceWindow = tc.first
			}))
			if first.SynthesisInput == nil {
				t.Fatal("call 1 returned no synthesis input")
			}
			rig.advance(4 * time.Minute)
			savedBefore := rig.store.count()
			recorder := rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
				body.TimeContext.EvidenceWindow = tc.second
				body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
					Output: writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), ModelOutputVersion: first.SynthesisInput.Contract.ModelOutputVersion,
					PromptVersion: first.SynthesisInput.Contract.PromptVersion, SystemSHA256: first.SynthesisInput.Contract.SystemSHA256,
					InputSHA256: first.SynthesisInput.InputSHA256, ClientModel: "writer-model-1",
				}
			})
			body := decodeRefusal(t, recorder, http.StatusConflict, "invalid_request")
			if got := detailString(t, body, "reason"); got != contractsv1.ContextFabricSuppliedSynthesisReasonInputChanged {
				t.Fatalf("reason = %q, want input_changed", got)
			}
			if rig.store.count() != savedBefore {
				t.Fatal("a refused write-back saved a result")
			}
		})
	}
}

func TestTheSameWindowGivesOneInputDigestAcrossAMovingClock(t *testing.T) {
	from, to := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for name, window := range map[string]*contractsv1.ContextFabricRequestedEvidenceWindow{
		"relative": {RelativeID: contextfabric.RelativeWindowTrailing30D},
		"stated":   {Start: &from, End: &to},
		"all time": {RelativeID: contractsv1.ContextFabricRelativeWindowAllTime},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			edit := func(body *contractsv1.ContextFabricInvestigationRequest) { body.TimeContext.EvidenceWindow = window }
			first := decodeEnvelope(t, rig.post(t, edit))
			rig.advance(6 * time.Minute)
			second := decodeEnvelope(t, rig.post(t, edit))
			if first.SynthesisInput == nil || second.SynthesisInput == nil {
				t.Fatal("a first call returned no synthesis input")
			}
			if first.SynthesisInput.InputSHA256 != second.SynthesisInput.InputSHA256 {
				t.Fatalf("input_sha256 differs:\n%s\n%s", first.SynthesisInput.Input, second.SynthesisInput.Input)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(first.SynthesisInput.Input, &decoded); err != nil {
				t.Fatal(err)
			}
			if _, present := decoded["evidence_window"]; !present {
				t.Fatalf("the input does not name its window: %s", first.SynthesisInput.Input)
			}
		})
	}
}

func TestAPeriodTheQuestionNamesGivesOneInputDigestAcrossAMovingClock(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	edit := func(body *contractsv1.ContextFabricInvestigationRequest) {
		spanInterpretation(futureEndingRange)(body)
		body.Question = "Why was the payments ledger service not ready to ship last month?"
		body.Consumer.Surface = "mcp"
	}
	first := decodeEnvelope(t, rig.post(t, edit))
	rig.advance(8 * time.Minute)
	second := decodeEnvelope(t, rig.post(t, edit))
	if first.SynthesisInput == nil || second.SynthesisInput == nil {
		t.Fatal("a first call returned no synthesis input")
	}
	window := first.EffectiveEvidenceWindow
	if window == nil || window.RelativeID != "" || window.Start == nil || !window.Start.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) || window.End == nil || !window.End.Before(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("effective window = %+v: the turn did not take the period from the interpretation", window)
	}
	if first.SynthesisInput.InputSHA256 != second.SynthesisInput.InputSHA256 {
		t.Fatalf("input_sha256 differs:\n%s\n%s", first.SynthesisInput.Input, second.SynthesisInput.Input)
	}
	var decoded struct {
		EvidenceWindow map[string]any `json:"evidence_window"`
	}
	if err := json.Unmarshal(first.SynthesisInput.Input, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.EvidenceWindow["start"] != "2026-08-01T00:00:00Z" || decoded.EvidenceWindow["end"] != nil {
		t.Fatalf("evidence_window = %v, want the stated start and no end", decoded.EvidenceWindow)
	}
}
