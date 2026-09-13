package contextfabric

// CHAOS-5582: ONE VALID WINDOW RECEIPT MAKES THE FRESH AXIS DIAGNOSTIC.
//
// THE DEFECT, measured on served corpus rows before this file existed: turn one
// committed axis=current and offered a window; turn two sent the identical
// question bytes, exactly one valid window receipt and no explicit
// evidence_window; the fresh interpretation of turn two moved the axis to
// `range` on some replicates of an identical decoding seed and not on others.
// Every replicate that drifted was refused with the axis-conflict limitation,
// logged as `veto_conflict`, and the plan carry was withheld. The veto was
// decided from the fresh interpretation ALONE -- no receipt comparison, no
// explicit window -- which is the falsification shape the design of record
// names for keeping the plural/explicit receipt conflict separate from
// non-window continuation drift.
//
// WHAT THESE PINS HOLD. With one verified receipt and no explicit window that
// disagrees beyond skew, an admitted continuation executes under the axis the
// carrier recorded; the fresh axis reaches the decision line as a diagnostic.
// The receipt conflicts (plural receipts, an explicit window beyond skew) still
// veto, a request with no receipt still follows its fresh interpretation, and a
// receipt that cannot import the prior context (a changed question, a carrier
// with nothing to continue) still ends at the axis-conflict veto.
//
// Every pin reads TWO things and keeps them apart: what was SERVED (status,
// limitation, plan provenance, applied window, executed axis) and what the
// decision LINE says, emitted through the production slog JSON handler at Info.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The engine clock in mustReuseTestEngine is time.Unix(200, 0). Every fresh
// bound below is answerable and in the past, so the axis-conflict veto -- not
// the unanswerable-bound refusal -- is the only thing that can end the turn.
var (
	axis5582RangeStart = time.Unix(40, 0).UTC()
	axis5582RangeEnd   = time.Unix(150, 0).UTC()
	axis5582AsOf       = time.Unix(120, 0).UTC()
)

// The receipt's frozen bounds, byte-identical to continuationPrior's offer.
var (
	axis5582FrozenStart = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	axis5582FrozenEnd   = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
)

// freshAxisInterpreter proposes a stated family (or none) under a stated fresh
// time context -- the one input this ticket is about.
type freshAxisInterpreter struct {
	unclassified bool
	family       QuestionFamily
	groupKind    SubjectKind
	timeContext  TimeContext
}

func (f freshAxisInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	outcome := QuestionFamilyOutcome{
		Family:             f.family,
		Source:             QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: f.family, GroupKind: f.groupKind},
		Version:            QuestionFamilyTableVersion,
	}
	if f.unclassified {
		outcome = QuestionFamilyOutcome{
			Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone,
			WinningSampleIndex: 0, WinningSample: FamilySample{},
			Version: QuestionFamilyTableVersion,
		}
	}
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: f.timeContext}, outcome, nil
}

// teeTelemetry records every event structurally AND writes the two lines this
// ticket reads -- the continuation decision and the window canonicalization
// outcome -- through the PRODUCTION emitter into a JSON handler at Info.
type teeTelemetry struct {
	*recordingTelemetry
	slog SlogEngineTelemetry
}

func (t teeTelemetry) RecordWindowContinuationDecision(ctx context.Context, principal storage.Principal, decision windowContinuationDecision) {
	t.recordingTelemetry.RecordWindowContinuationDecision(ctx, principal, decision)
	t.slog.RecordWindowContinuationDecision(ctx, principal, decision)
}

func (t teeTelemetry) RecordWindowCanonicalization(ctx context.Context, principal storage.Principal, outcome WindowCanonicalizationOutcome) {
	t.recordingTelemetry.RecordWindowCanonicalization(ctx, principal, outcome)
	t.slog.RecordWindowCanonicalization(ctx, principal, outcome)
}

type axis5582Run struct {
	result    InvestigationResult
	err       error
	telemetry *recordingTelemetry
	log       *bytes.Buffer
	requestID string
	graph     *frameRecordingGraphReader
}

func axis5582Investigate(t *testing.T, results InvestigationResultStore, interpreter QuestionInterpreter, request InvestigationRequest) axis5582Run {
	t.Helper()
	// EVERY CARRIER A PRODUCTION TURN SAVES NOW CARRIES ITS SNAPSHOT. These
	// pins were written before the reading was persisted, so their stores held
	// results with none -- under the persisted reading that is a carrier with
	// nothing to continue (semantic_state_absent), and every axis cell here
	// would measure that instead of the axis. Stamped in ONE place rather than
	// at eight call sites, through the same producer every other fixture uses.
	if store, ok := results.(*staticResultStore); ok && !store.noCarrierStates {
		withCarrierStates(t, store)
	}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	recording := &recordingTelemetry{}
	var buf bytes.Buffer
	graph := &frameRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		bases:      provenCommitBases(project),
	}}
	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreter,
		Results:     results,
		Telemetry: teeTelemetry{
			recordingTelemetry: recording,
			slog:               NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))),
		},
	})
	// observability accepts only the generator's own shape (req_ + 32 lowercase
	// hex); a per-test digest keeps every parallel run's line attributable.
	digest := sha256.Sum256([]byte(t.Name()))
	requestID := "req_" + hex.EncodeToString(digest[:16])
	ctx := observability.WithRequestID(context.Background(), requestID)
	result, err := engine.Investigate(ctx, acceptancePrincipal(), request)
	return axis5582Run{result: result, err: err, telemetry: recording, log: &buf, requestID: requestID, graph: graph}
}

// linesWithMsg decodes every emitted JSON line carrying msg.
func (r axis5582Run) linesWithMsg(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(r.log.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("emitted line is not JSON: %v: %s", err, raw)
		}
		if line["msg"] == msg {
			out = append(out, line)
		}
	}
	return out
}

func (r axis5582Run) soleDecisionLine(t *testing.T) map[string]any {
	t.Helper()
	lines := r.linesWithMsg(t, "context fabric window continuation decision")
	if len(lines) != 1 {
		t.Fatalf("got %d continuation decision lines, want exactly 1; log:\n%s", len(lines), r.log.String())
	}
	return lines[0]
}

// assertLine checks every wanted key BY VALUE, and the level, on the emitted
// line. A key that is absent is reported as absent, never as a zero.
func assertLine(t *testing.T, line map[string]any, want map[string]any) {
	t.Helper()
	if line["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", line["level"])
	}
	for key, value := range want {
		got, present := line[key]
		if !present {
			t.Errorf("line has no %q key (want %v)", key, value)
			continue
		}
		wantJSON, _ := json.Marshal(value)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%s = %s, want %s", key, gotJSON, wantJSON)
		}
	}
}

func axisConflictLimitationServed(result InvestigationResult) bool {
	sentence := windowVetoLimitation(windowVetoAxisConflict)
	for _, limitation := range result.Limitations {
		if limitation == sentence {
			return true
		}
	}
	return result.DeterministicAnswer == sentence
}

func canonicalizationOutcomes(recording *recordingTelemetry, outcome WindowCanonicalizationOutcome) int {
	n := 0
	for _, got := range recording.windowCanonicalizationOutcomes {
		if got == outcome {
			n++
		}
	}
	return n
}

type axis5582FreshAxis struct {
	name string
	time TimeContext
}

func axis5582DriftedAxes() []axis5582FreshAxis {
	return []axis5582FreshAxis{
		{"range", TimeContext{Axis: TemporalRange, Start: &axis5582RangeStart, End: &axis5582RangeEnd}},
		{"valid_time", TimeContext{Axis: TemporalValidTime, AsOf: &axis5582AsOf}},
		{"observed_time", TimeContext{Axis: TemporalObservedTime, AsOf: &axis5582AsOf}},
	}
}

type axis5582Carrier struct {
	name   string
	family QuestionFamily
	group  SubjectKind
}

func axis5582Carriers() []axis5582Carrier {
	return []axis5582Carrier{
		{"discovered", QuestionFamilyDiscoveredCohortRanking, ""},
		{"grouped_team", QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam},
	}
}

// freshProposals are the three ways turn two's family can read against the
// carrier: agreeing, disagreeing, and classifying nothing.
func axis5582FreshProposals(carrier axis5582Carrier, timeContext TimeContext) map[string]freshAxisInterpreter {
	other := axis5582Carrier{"grouped_team", QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam}
	if carrier.family == QuestionFamilyGroupedCohortStatus {
		other = axis5582Carrier{"discovered", QuestionFamilyDiscoveredCohortRanking, ""}
	}
	return map[string]freshAxisInterpreter{
		"fresh_agrees":       {family: carrier.family, groupKind: carrier.group, timeContext: timeContext},
		"fresh_disagrees":    {family: other.family, groupKind: other.group, timeContext: timeContext},
		"fresh_unclassified": {unclassified: true, timeContext: timeContext},
	}
}

func axis5582Surfaces() []string { return []string{"context-fabric-workbench", "mcp"} }

func axis5582Request(question, surface string) InvestigationRequest {
	request := continuationRequest(question)
	request.Consumer.Surface = surface
	if surface == "mcp" {
		request.Consumer.Name = "context-fabric-mcp"
	}
	return request
}

// PIN 1 -- the served corpus shape, generated over both consumer surfaces,
// both carrier families, every fresh proposal and every drifted axis.
func TestCHAOS5582_OneValidReceiptMakesTheFreshAxisDiagnostic(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	for _, surface := range axis5582Surfaces() {
		for _, carrier := range axis5582Carriers() {
			for _, axis := range axis5582DriftedAxes() {
				for proposalName, interpreter := range axis5582FreshProposals(carrier, axis.time) {
					name := surface + "/" + carrier.name + "/" + proposalName + "/" + axis.name
					surface, carrier, axis, interpreter := surface, carrier, axis, interpreter
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						request := axis5582Request(question, surface)
						prior := continuationPrior(t, continuationPriorID, question, carrier.family, carrier.group)
						run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter, request)
						if run.err != nil {
							t.Fatalf("Investigate() error = %v", run.err)
						}

						t.Run("served", func(t *testing.T) {
							if run.result.Status == InvestigationNoMatch || axisConflictLimitationServed(run.result) {
								t.Errorf("REFUSED: status=%q limitations=%q -- one valid receipt and no explicit window, and the fresh %s axis alone ended the turn",
									run.result.Status, run.result.Limitations, axis.name)
							}
							if n := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict) + canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoAxisConflict); n != 0 {
								t.Errorf("window canonicalization reported a veto %d time(s) for a single agreeing receipt: %v", n, run.telemetry.windowCanonicalizationOutcomes)
							}
							if run.result.AnswerPlan == nil || run.result.AnswerPlan.FamilySource != QuestionFamilySourceCarried || run.result.AnswerPlan.Family != carrier.family {
								t.Errorf("served plan = %+v, want family=%q family_source=carried", run.result.AnswerPlan, carrier.family)
							}
							if w := run.result.EffectiveEvidenceWindow; w == nil || w.Start == nil || !w.Start.Equal(axis5582FrozenStart) || w.End == nil || !w.End.Equal(axis5582FrozenEnd) || w.Provenance != WindowClarificationConfirmed {
								t.Errorf("served effective window = %+v, want the receipt's frozen bounds with clarification_confirmed provenance", w)
							}
							if got := run.result.Interpretation.TimeContext.Axis; got != TemporalCurrent {
								t.Errorf("served interpretation axis = %q, want %q (the axis the carrier recorded and the window was confirmed under)", got, TemporalCurrent)
							}
							for _, o := range run.telemetry.planCarryOutcomes {
								if o.outcome == PlanCarryMissContinuationWithheld {
									t.Errorf("plan carry outcome = %q with source=%q -- the admitted carrier was withheld", o.outcome, o.sourceResultID)
								}
							}
							if continuationAppliedCarries(run.telemetry) != 1 {
								t.Errorf("applied-carry emits attributable to the continuation = %d, want 1", continuationAppliedCarries(run.telemetry))
							}
						})

						t.Run("line", func(t *testing.T) {
							assertLine(t, run.soleDecisionLine(t), map[string]any{
								"request_id":               run.requestID,
								"continuation_disposition": "applied",
								"decision_reason":          "none",
								"family_accepted":          string(carrier.family),
								"window_receipt_count":     1,
								"explicit_window_present":  false,
								"interpreted_axis":         axis.name,
								"carried_axis":             "current",
								"executed_axis":            "current",
								"interpreted_axis_outcome": "overridden_by_receipt",
							})
						})
					})
				}
			}
		}
	}
}

// PIN 2 -- the design of record's own falsification test, kept permanent: a
// `veto_conflict` produced solely by a fresh non-window interpretation despite
// ONE valid receipt and AGREEING explicit bounds. Agreement is exercised at the
// skew boundary on both bounds, not only at equality.
func TestCHAOS5582_DesignFalsification_AgreeingExplicitBoundsNeverVetoOnAFreshAxis(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	skewStart := axis5582FrozenStart.Add(futureSkewTolerance)
	skewEnd := axis5582FrozenEnd.Add(-futureSkewTolerance)
	explicitWindows := map[string]*RequestedEvidenceWindow{
		"relative_id_only":             {RelativeID: RelativeWindowTrailing90D},
		"exact_bounds":                 {Start: &axis5582FrozenStart, End: &axis5582FrozenEnd},
		"relative_id_and_bounds":       {RelativeID: RelativeWindowTrailing90D, Start: &axis5582FrozenStart, End: &axis5582FrozenEnd},
		"bounds_at_skew_boundary":      {Start: &skewStart, End: &skewEnd},
		"relative_id_bounds_at_skew_0": {RelativeID: RelativeWindowTrailing90D, Start: &skewStart, End: &axis5582FrozenEnd},
	}
	for _, surface := range axis5582Surfaces() {
		for explicitName, explicit := range explicitWindows {
			for _, axis := range axis5582DriftedAxes() {
				surface, explicit, axis := surface, explicit, axis
				t.Run(surface+"/"+explicitName+"/"+axis.name, func(t *testing.T) {
					t.Parallel()
					request := axis5582Request(question, surface)
					request.TimeContext.EvidenceWindow = explicit
					prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
					run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
						freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: axis.time}, request)
					if run.err != nil {
						t.Fatalf("Investigate() error = %v", run.err)
					}
					t.Run("served", func(t *testing.T) {
						if n := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict); n != 0 {
							t.Errorf("FALSIFIED: veto_conflict reported %d time(s) with one valid receipt and agreeing explicit bounds (fresh axis %s)", n, axis.name)
						}
						if canonicalizationOutcomes(run.telemetry, WindowCanonicalizationReceiptConfirmed) == 0 {
							t.Errorf("the receipt never reached receipt_confirmed; outcomes=%v", run.telemetry.windowCanonicalizationOutcomes)
						}
						if run.result.Status == InvestigationNoMatch || axisConflictLimitationServed(run.result) {
							t.Errorf("REFUSED: status=%q limitations=%q", run.result.Status, run.result.Limitations)
						}
						if run.result.AnswerPlan == nil || run.result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
							t.Errorf("served plan = %+v, want family_source=carried", run.result.AnswerPlan)
						}
					})
					t.Run("line", func(t *testing.T) {
						assertLine(t, run.soleDecisionLine(t), map[string]any{
							"request_id":               run.requestID,
							"continuation_disposition": "applied",
							"window_receipt_count":     1,
							"explicit_window_present":  true,
							"interpreted_axis":         axis.name,
							"carried_axis":             "current",
							"executed_axis":            "current",
							"interpreted_axis_outcome": "overridden_by_receipt",
						})
					})
				})
			}
		}
	}
}

// PIN 3 -- the receipt conflicts the design keeps as vetoes still veto, each
// under a DRIFTING fresh interpreter, so a fix that suppressed them to make a
// continuation succeed is visible here.
func TestCHAOS5582_TheReceiptConflictVetoesAreRetained(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	beyondStart := axis5582FrozenStart.Add(futureSkewTolerance + time.Second)
	beyondEnd := axis5582FrozenEnd.Add(-(futureSkewTolerance + time.Second))
	for _, tc := range []struct {
		name         string
		mutate       func(*InvestigationRequest)
		receiptCount int
		explicit     bool
	}{
		{
			name: "plural_receipts",
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = append(r.PriorWindowReceipts, BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: continuationReceiptID})
			},
			receiptCount: 2,
		},
		{
			name: "explicit_start_beyond_skew",
			mutate: func(r *InvestigationRequest) {
				r.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{Start: &beyondStart, End: &axis5582FrozenEnd}
			},
			receiptCount: 1, explicit: true,
		},
		{
			name: "explicit_end_beyond_skew",
			mutate: func(r *InvestigationRequest) {
				r.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{Start: &axis5582FrozenStart, End: &beyondEnd}
			},
			receiptCount: 1, explicit: true,
		},
		{
			name: "explicit_relative_id_disagrees",
			mutate: func(r *InvestigationRequest) {
				r.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
			},
			receiptCount: 1, explicit: true,
		},
	} {
		for _, surface := range axis5582Surfaces() {
			tc, surface := tc, surface
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				request := axis5582Request(question, surface)
				tc.mutate(&request)
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
				older := continuationPrior(t, continuationOlderID, question, QuestionFamilyDiscoveredCohortRanking, "")
				run := axis5582Investigate(t,
					&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older}},
					freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, timeContext: axis5582DriftedAxes()[0].time},
					request)
				if run.err != nil {
					t.Fatalf("Investigate() error = %v", run.err)
				}
				t.Run("served", func(t *testing.T) {
					if run.result.Status != InvestigationNoMatch {
						t.Errorf("status = %q, want no_match -- a receipt conflict must still veto", run.result.Status)
					}
					// The design's own receipt conflicts keep their label, and never
					// the axis-conflict one.
					if got := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict); got != 1 {
						t.Errorf("veto_conflict outcomes = %d, want 1; outcomes=%v", got, run.telemetry.windowCanonicalizationOutcomes)
					}
					if got := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoAxisConflict); got != 0 {
						t.Errorf("veto_axis_conflict outcomes = %d, want 0 on a receipt conflict; outcomes=%v", got, run.telemetry.windowCanonicalizationOutcomes)
					}
					if run.result.DeterministicAnswer != windowVetoLimitation(windowVetoConfirmationConflict) {
						t.Errorf("served answer = %q, want the confirmation-conflict limitation", run.result.DeterministicAnswer)
					}
					if run.result.AnswerPlan != nil && run.result.AnswerPlan.FamilySource == QuestionFamilySourceCarried {
						t.Errorf("a vetoed request served a carried reading")
					}
				})
				t.Run("line", func(t *testing.T) {
					assertLine(t, run.soleDecisionLine(t), map[string]any{
						"request_id":               run.requestID,
						"decision_reason":          "window_veto",
						"window_receipt_count":     tc.receiptCount,
						"explicit_window_present":  tc.explicit,
						"interpreted_axis":         "",
						"carried_axis":             "",
						"executed_axis":            "",
						"interpreted_axis_outcome": "not_evaluated",
					})
				})
			})
		}
	}
}

// PIN 4 -- no drift: a fresh `current` axis is unchanged by this ticket.
func TestCHAOS5582_AFreshCurrentAxisIsUnchanged(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	current := TimeContext{Axis: TemporalCurrent}
	for _, surface := range axis5582Surfaces() {
		for _, carrier := range axis5582Carriers() {
			for proposalName, interpreter := range axis5582FreshProposals(carrier, current) {
				surface, carrier, interpreter := surface, carrier, interpreter
				t.Run(surface+"/"+carrier.name+"/"+proposalName, func(t *testing.T) {
					t.Parallel()
					request := axis5582Request(question, surface)
					prior := continuationPrior(t, continuationPriorID, question, carrier.family, carrier.group)
					run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter, request)
					if run.err != nil {
						t.Fatalf("Investigate() error = %v", run.err)
					}
					t.Run("served", func(t *testing.T) {
						if run.result.Status == InvestigationNoMatch || axisConflictLimitationServed(run.result) {
							t.Errorf("REFUSED: status=%q limitations=%q", run.result.Status, run.result.Limitations)
						}
						if run.result.AnswerPlan == nil || run.result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
							t.Errorf("served plan = %+v, want family_source=carried", run.result.AnswerPlan)
						}
						if run.result.EffectiveEvidenceWindow == nil {
							t.Errorf("no effective window served")
						}
						if got := run.result.Interpretation.TimeContext.Axis; got != TemporalCurrent {
							t.Errorf("served axis = %q, want current", got)
						}
					})
					t.Run("line", func(t *testing.T) {
						assertLine(t, run.soleDecisionLine(t), map[string]any{
							"request_id":               run.requestID,
							"continuation_disposition": "applied",
							"window_receipt_count":     1,
							"explicit_window_present":  false,
							"interpreted_axis":         "current",
							"carried_axis":             "current",
							"executed_axis":            "current",
							"interpreted_axis_outcome": "agreed",
						})
					})
				})
			}
		}
	}
}

// PIN 5 -- the fix is not "ignore the interpretation". Without a receipt the
// fresh axis still governs; with a receipt whose transition is not established
// (a changed question), the axis-conflict veto still ends the turn.
func TestCHAOS5582_WithoutAnAdmittedReceiptTheFreshAxisStillGoverns(t *testing.T) {
	t.Parallel()
	question := validInvestigationRequest().Question
	drift := axis5582DriftedAxes()[0]

	t.Run("no_receipt_stated_window_still_vetoes_on_axis_conflict", func(t *testing.T) {
		t.Parallel()
		request := validInvestigationRequest()
		request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
		run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{}},
			freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, timeContext: drift.time}, request)
		if run.err != nil {
			t.Fatalf("Investigate() error = %v", run.err)
		}
		if !axisConflictLimitationServed(run.result) || run.result.Status != InvestigationNoMatch {
			t.Errorf("status=%q limitations=%q, want the axis-conflict veto -- a stated window with no receipt follows the fresh interpretation", run.result.Status, run.result.Limitations)
		}
		// The mislabel the design names: this veto was produced solely by a
		// fresh interpretation, so it is NOT the receipt conflict's label.
		if got := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoAxisConflict); got != 1 {
			t.Errorf("veto_axis_conflict outcomes = %d, want 1; outcomes=%v", got, run.telemetry.windowCanonicalizationOutcomes)
		}
		if got := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict); got != 0 {
			t.Errorf("veto_conflict outcomes = %d, want 0 for a fresh-axis veto; outcomes=%v", got, run.telemetry.windowCanonicalizationOutcomes)
		}
		if got := run.result.Interpretation.TimeContext.Axis; got != TemporalRange {
			t.Errorf("served axis = %q, want the fresh %q", got, TemporalRange)
		}
		if n := len(run.linesWithMsg(t, "context fabric window continuation decision")); n != 0 {
			t.Errorf("a request with no window receipt emitted %d continuation lines, want 0", n)
		}
	})

	t.Run("no_receipt_no_window_serves_the_fresh_axis", func(t *testing.T) {
		t.Parallel()
		request := validInvestigationRequest()
		run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{}},
			freshAxisInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, timeContext: drift.time}, request)
		if run.err != nil {
			t.Fatalf("Investigate() error = %v", run.err)
		}
		if axisConflictLimitationServed(run.result) {
			t.Errorf("a request with no window at all was refused on axis conflict")
		}
		if got := run.result.Interpretation.TimeContext.Axis; got != TemporalRange {
			t.Errorf("served axis = %q, want the fresh %q -- with nothing to continue, the interpretation governs", got, TemporalRange)
		}
	})

	for _, tc := range []struct {
		name       string
		prior      func(InvestigationResult) InvestigationResult
		wantReason string
	}{
		{"receipt_with_changed_question", func(p InvestigationResult) InvestigationResult {
			p.Question = "What was the status of Ask Dev last quarter and what drove it?"
			return p
		}, "changed_question"},
		{"receipt_with_indeterminate_identity", func(p InvestigationResult) InvestigationResult {
			p.Question = "!!"
			return p
		}, "indeterminate_identity"},
	} {
		for _, surface := range axis5582Surfaces() {
			tc, surface := tc, surface
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				request := axis5582Request(question, surface)
				prior := tc.prior(continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam))
				run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
					freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: drift.time}, request)
				if run.err != nil {
					t.Fatalf("Investigate() error = %v", run.err)
				}
				t.Run("served", func(t *testing.T) {
					if !axisConflictLimitationServed(run.result) || run.result.Status != InvestigationNoMatch {
						t.Errorf("status=%q limitations=%q, want the axis-conflict veto -- this receipt cannot import the prior reading", run.result.Status, run.result.Limitations)
					}
					if a, c := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoAxisConflict), canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict); a != 1 || c != 0 {
						t.Errorf("veto_axis_conflict=%d veto_conflict=%d, want 1 and 0; outcomes=%v", a, c, run.telemetry.windowCanonicalizationOutcomes)
					}
					if got := run.result.Interpretation.TimeContext.Axis; got != TemporalRange {
						t.Errorf("served axis = %q, want the fresh %q", got, TemporalRange)
					}
				})
				t.Run("line", func(t *testing.T) {
					assertLine(t, run.soleDecisionLine(t), map[string]any{
						"request_id":               run.requestID,
						"decision_reason":          tc.wantReason,
						"window_receipt_count":     1,
						"explicit_window_present":  false,
						"interpreted_axis":         "range",
						"executed_axis":            "range",
						"interpreted_axis_outcome": "vetoed",
					})
				})
			})
		}
	}
}

// PIN 6 -- the served corpus replicate that still refused at the first cut of
// this fix (cv-c1-grouped-trend rep8, t2 req_2e9d36e91540bc4364a329b0c12a00a4):
// turn one classified explicit_comparison, turn two's fresh proposal was a
// valid grouped team frame on a `range` axis, the carried family could not be
// composed onto that frame (withheld, composition_invalid) and a rule keyed on
// the APPLIED reading let the sampled axis refuse the confirmed question with
// the axis-conflict veto. The transition was established, so the confirmed axis
// holds and the SAMPLED axis never decides the turn. What ends it is the
// withheld carrier's own refusal (continuation_context_unverifiable), with its
// own basis -- never the axis-conflict limitation or its canonicalization label.
func TestCHAOS5582_ACarriedReadingOnAnEstablishedTransitionKeepsTheConfirmedAxis(t *testing.T) {
	// FLIPPED BY THE PERSISTED READING, and this is the cell that flipped.
	//
	// It was written when the carried reading had to be substituted into THIS
	// turn's fresh frame: a carrier whose family groups nothing, read beside a
	// fresh grouped proposal, had nowhere to put its axis, so composition
	// failed (carried_axis_unexpressible) and the turn ended on the carrier's
	// own refusal. The reading is now persisted WITH ITS FRAME, so composition
	// runs on what turn one actually validated and never consults the fresh
	// proposal's shape at all -- an established transition cannot fail here
	// for want of somewhere to put the axis.
	//
	// The claim in the name is unchanged and is what this cell still protects:
	// the SAMPLED axis never decides an established transition. What changed is
	// the ending -- the confirmed reading is SERVED rather than refused, so the
	// turn executes, and executed_axis is the confirmed `current` rather than
	// empty. The axis-veto assertions below are untouched.
	t.Parallel()
	question := validInvestigationRequest().Question
	for _, surface := range axis5582Surfaces() {
		for _, axis := range axis5582DriftedAxes() {
			surface, axis := surface, axis
			t.Run(surface+"/"+axis.name, func(t *testing.T) {
				t.Parallel()
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyExplicitComparison, "")
				interpreter := frameBearingInterpreter{
					family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam,
					frameGroup: contractsv1.ContextFabricSubjectTeam, timeContext: axis.time,
				}
				run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter, axis5582Request(question, surface))
				if run.err != nil {
					t.Fatalf("Investigate() error = %v", run.err)
				}
				line := run.soleDecisionLine(t)
				t.Run("served", func(t *testing.T) {
					if axisConflictLimitationServed(run.result) {
						t.Errorf("AXIS VETO: the sampled %s axis ended an established transition: limitations=%q", axis.name, run.result.Limitations)
					}
					if n := canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoAxisConflict) + canonicalizationOutcomes(run.telemetry, WindowCanonicalizationVetoConflict); n != 0 {
						t.Errorf("a veto canonicalization outcome was recorded %d time(s): %v", n, run.telemetry.windowCanonicalizationOutcomes)
					}
					if run.result.Status == InvestigationNoMatch || run.result.RefusalBasis != "" {
						t.Errorf("served status=%q refusal_basis=%q, want the confirmed reading served -- the carried frame is durable, so composition has nothing to fail on", run.result.Status, run.result.RefusalBasis)
					}
					if run.result.AnswerPlan == nil || run.result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
						t.Errorf("the confirmed reading was not served as carried: %+v", run.result.AnswerPlan)
					}
				})
				t.Run("line", func(t *testing.T) {
					assertLine(t, line, map[string]any{
						"request_id":               run.requestID,
						"continuation_disposition": "applied",
						"decision_reason":          "none",
						"family_carried":           "explicit_comparison",
						"family_accepted":          "explicit_comparison",
						"family_source":            "carried",
						"interpreted_axis":         axis.name,
						"carried_axis":             "current",
						// THE CONFIRMED AXIS, EXECUTED. The fresh axis still
						// does not govern -- `carried_axis` is current and the
						// outcome is overridden_by_receipt, which is the claim
						// in this test's name -- and now the turn actually
						// executes under it, so the line says so. Empty here
						// would be the line denying an execution that happened.
						"executed_axis":            "current",
						"interpreted_axis_outcome": "overridden_by_receipt",
						"refusal_basis":            "none",
					})
					if line["composition_failed_invariant"] != "" {
						t.Errorf("composition_failed_invariant = %q on a composition that succeeded", line["composition_failed_invariant"])
					}
				})
			})
		}
	}
}
