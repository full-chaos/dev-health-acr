package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// P1 (adversarial review): (*Engine).finalizeResult used to emit the
// observation-cover event INLINE, through e.telemetry, every time it ran.
// finalizeResult runs once per PASS -- the first synthesis and, on a budget
// retry, again -- so one served investigation logged TWO sets of cover
// lines, and the FIRST described an answer nobody received: pass one's
// document was discarded by stage 3 and replaced by the retry's.
//
// The fix does NOT suppress the discarded pass. The governing rule for this
// event is that a reader must be able to rebuild the whole decision graph
// from the trace, and a discarded pass -- why the first answer was too big,
// what it would have covered -- is part of that graph. So finalizeResult now
// APPENDS its events onto the pending assemblyTelemetry (tagged with which
// pass produced them) instead of emitting them itself, and (*Engine).emit
// publishes every pass's events exactly once, marking the FINAL pass's
// served=true and every earlier pass's served=false.
//
// This test drives a REAL forced retry through Engine.Investigate -- the
// same model TestStage3ReSynthesizesOnceWhenTheAnswerDoesNotFit uses
// (budgetStageCohort, budgetStageOptions, a synthesizer whose claim count
// scales with the cohort it is handed) -- with a frame and registry that
// also derive one real, servable READ requirement, so the cover events are
// the production path's, not a fixture-only value.
func TestObservationCoverKeepsTheDiscardedPassAndMarksTheServedOne(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	result, calls := investigateWithForcedRetry(t, telemetry)
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want exactly 2 -- this pin requires a forced retry", calls)
	}

	covers := telemetry.readRequirementObservationCovers
	if len(covers) != 2 {
		t.Fatalf("recorded %d observation-cover lines for one served investigation, want exactly 2 (one "+
			"per pass) -- got %+v", len(covers), covers)
	}

	var served, discarded *ReadRequirementObservationCoverEvent
	servedCount := 0
	for i := range covers {
		if covers[i].Served {
			servedCount++
			served = &covers[i]
		} else {
			discarded = &covers[i]
		}
	}
	if servedCount != 1 {
		t.Fatalf("%d of the 2 cover lines have served=true, want exactly 1: %+v", servedCount, covers)
	}
	if served == nil || discarded == nil {
		t.Fatalf("did not find both a served and a discarded cover line: %+v", covers)
	}
	if discarded.Pass >= served.Pass {
		t.Fatalf("discarded line's pass (%d) is not EARLIER than the served line's pass (%d)",
			discarded.Pass, served.Pass)
	}

	// The served=true line's numbers must match the row the RETURNED result
	// actually carries -- that is the whole point of "served": it describes
	// the document the caller received, not a discarded alternative.
	var row *RequirementOutcomeRow
	for i := range result.Completeness.Outcomes {
		candidate := result.Completeness.Outcomes[i]
		if candidate.Requirement == served.Requirement && candidate.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			row = &result.Completeness.Outcomes[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("no assembled-result row on the served document for requirement %q; the served cover line "+
			"names a row the caller never received", served.Requirement)
	}
	if row.Served != served.ServedCover {
		t.Fatalf("served cover line's ServedCover=%d, but the served document's own row reads Served=%d",
			served.ServedCover, row.Served)
	}
	if row.Declared != served.Declared {
		t.Fatalf("served cover line's Declared=%d, but the served document's own row reads Declared=%d",
			served.Declared, row.Declared)
	}
}

// TestTheEmittedCoverLinesCarryEachPassesOwnPassAndServedValues is the pin for
// a SURVIVOR the hosted battery found: dropping `"pass"` from the emitter
// changed no test result, because the per-pass pin above reads the telemetry
// DOUBLE, never the emitted line.
//
// It drives the SAME forced retry through Engine.Investigate, but the engine's
// telemetry is the production sink -- NewSlogEngineTelemetry around a JSON
// handler at Info, the shape cmd/acr-api/main.go builds under the default
// ACR_LOG_LEVEL=info -- and every assertion reads the bytes that handler wrote.
//
// NON-TRIVIAL VALUES, and no coincidence. A single-pass fixture emits pass=0,
// the zero value: an emitter that hard-coded 0, or published any other field
// reading 0 there under the `pass` key, would pass it. Two passes make `pass`
// take TWO values and `served` take both booleans, so a field swapped in for
// either must track it on BOTH lines to survive -- and the test asserts that
// no other field on the line does.
func TestTheEmittedCoverLinesCarryEachPassesOwnPassAndServedValues(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if _, calls := investigateWithForcedRetry(t, telemetry); calls != 2 {
		t.Fatalf("synthesizer called %d times, want exactly 2 -- this pin requires a forced retry", calls)
	}

	var servedLine, discardedLine map[string]any
	emitted := 0
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) != nil || line["msg"] != "context fabric observation cover" {
			continue
		}
		emitted++
		if got := line["level"]; got != "INFO" {
			t.Fatalf("level = %v, want INFO", got)
		}
		if _, ok := line["pass"].(float64); !ok {
			t.Fatalf("the emitted line carries no numeric `pass` (present: %v)", keysOf(line))
		}
		served, ok := line["served"].(bool)
		if !ok {
			t.Fatalf("the emitted line carries no boolean `served` (present: %v)", keysOf(line))
		}
		if served {
			servedLine = line
		} else {
			discardedLine = line
		}
	}
	if emitted != 2 || servedLine == nil || discardedLine == nil {
		t.Fatalf("the production handler received %d cover lines, want exactly one served and one discarded:\n%s",
			emitted, buf.String())
	}
	// The VALUES, read from the handler's bytes: the discarded first pass is
	// pass 0, the served retry is pass 1.
	if got := discardedLine["pass"]; got != float64(answerPassFirst) {
		t.Fatalf("discarded line pass = %v, want %d", got, answerPassFirst)
	}
	if got := servedLine["pass"]; got != float64(answerPassSecond) {
		t.Fatalf("served line pass = %v, want %d -- the served document is the retry's", got, answerPassSecond)
	}

	// Both passes of a retry EVALUATE (the retry synthesizes a fresh document),
	// so evaluated_pass equals pass on both lines by contract. It is asserted
	// here at those values and excluded from the coincidence sweep below; the
	// candidate-rescue pin is where the two differ, and asserts both.
	if discardedLine["evaluated_pass"] != float64(answerPassFirst) || servedLine["evaluated_pass"] != float64(answerPassSecond) {
		t.Fatalf("evaluated_pass = %v / %v, want %d / %d -- both retry passes evaluated",
			discardedLine["evaluated_pass"], servedLine["evaluated_pass"], answerPassFirst, answerPassSecond)
	}

	// THE DIFFERENCE IS THE ASSERTION. No other field may track `pass` or
	// `served` across the two lines; if one did, an emitter publishing it
	// under that key would pass every assertion above.
	for field := range servedLine {
		if field == "pass" || field == "served" || field == "evaluated_pass" {
			continue
		}
		for _, key := range []string{"pass", "served"} {
			if servedLine[field] == servedLine[key] && discardedLine[field] == discardedLine[key] {
				t.Fatalf("field %q equals %q on BOTH lines (%v, %v); the fixture cannot tell them apart",
					field, key, discardedLine[field], servedLine[field])
			}
		}
	}
}

// investigateWithForcedRetry runs one served investigation forced through
// exactly one budget retry, reporting to telemetry, and returns the served
// result and the synthesizer call count.
func investigateWithForcedRetry(t *testing.T, telemetry EngineTelemetry) (InvestigationResult, int) {
	t.Helper()
	frame := teamStateFrame(t)
	deriver := registryDeriver{capabilities: []FactCapability{
		stateCapability("health", FactHealth),
	}}

	calls := 0
	cohort := budgetStageCohort(6)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			},
			outcome: QuestionFamilyOutcome{
				Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel,
			},
		},
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort,
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			// DELIBERATELY EMPTY: this pin does not need real evidence for the
			// `state` requirement, only that the requirement is DECLARED and
			// SERVABLE, so finalizeResult reaches the observation-cover
			// evaluator on every pass. Zero facts means every pass reads
			// `observed 0`, which is itself a real cover event (see
			// readRequirementOutcomeRow's `evidence.Observed == 0` arm).
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			calls++
			claims := []ClaimedFact{}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					for claim := 0; claim < 2; claim++ {
						claims = append(claims, ClaimedFact{
							ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
							Kind:    FactStatus, Subject: member.Subject, Field: "status",
							Value: ScalarValue{String: ptrString("green")},
						})
					}
				}
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Requirements: deriver,
		Telemetry:    telemetry,
	}, budgetStageOptions(12, time.Second))
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, calls
}
