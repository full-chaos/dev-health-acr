package pginvestigation_test

// A window-only continuation through the PRODUCTION stored-result path on REAL
// PostgreSQL (testcontainers, all migrations applied): a FRESH engine and a
// FRESH store handle per turn, and the shipped slog sink. Turn one proposes and
// saves a reading; every later turn's interpreter is forced to DISAGREE, and a
// requirement registry that changed between turns would derive different
// declarations. The continued turn must serve the carried reading whole --
// family, frame, group axis and the carried declarations -- and persist it as
// its own complete snapshot, so the next turn reads one hop only.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contextfabric "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"log/slog"
)

const extQuestion = "Which teams have repositories whose status is slipping?"

type extGraph struct{}

func (extGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "ext-key", Epoch: 0}, nil
}

func (extGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (extGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

type extFacts struct{}

func (extFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{}, nil
}

type extSynth struct{}

func (extSynth) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	panic("synthesis is not reachable on a subjectless turn")
}

// extInterpreter proposes one validated frame and family.
type extInterpreter struct {
	family contextfabric.QuestionFamily
	frame  contextfabric.QuestionFrame
}

func (i extInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	result := contextfabric.ValidateFrame(i.frame, nil, contextfabric.ShapeOpen)
	if result.Outcome != contextfabric.FrameValidationOutcomeValid {
		panic("fixture defect: interpreter frame invalid: " + string(result.Failure.Invariant))
	}
	frame := result.Frame
	group, _ := frame.SubjectExpression.GroupKind()
	// A trend-assessment class with high confidence: with no window on the
	// request, the class table's default window is inferred, and the gate
	// stops the turn to OFFER it -- the offer turn two redeems.
	return contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			WindowClass: contextfabric.WindowClassTrendAssessment, WindowConfidence: contextfabric.WindowConfidenceHigh,
		},
		contextfabric.QuestionFamilyOutcome{
			Family: i.family, Source: contextfabric.QuestionFamilySourceModel,
			Frame: &frame, FrameObligations: frame.Obligations, Gate: contextfabric.DecideFrameGate(result, true),
			WinningSampleIndex: 0, WinningSample: contextfabric.FamilySample{ModelFamily: i.family, GroupKind: group},
			Version: contextfabric.QuestionFamilyTableVersion,
		}, nil
}

// extDeriver is a requirement registry. The seed decides what it declares; a
// registry "changed between turns" is simply a different seed.
type extDeriver struct {
	calls  *atomic.Int64
	derive func(contextfabric.QuestionFrame) []contextfabric.DerivedRequirement
}

func (d extDeriver) DeriveRequirements(frame contextfabric.QuestionFrame) []contextfabric.DerivedRequirement {
	d.calls.Add(1)
	return d.derive(frame)
}

func extEngine(t *testing.T, store contextfabric.InvestigationResultStore, interpreter contextfabric.QuestionInterpreter, deriver contextfabric.RequirementDeriver, resultID string, sink *bytes.Buffer) *contextfabric.Engine {
	t.Helper()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Graph: extGraph{}, Facts: extFacts{}, Synthesizer: extSynth{}, Interpreter: interpreter,
		Results: store, Requirements: deriver,
		Telemetry: contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))),
	}, contextfabric.EngineOptions{
		ServiceVersion: "semantic-ext-test",
		Now:            func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return resultID },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func extRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_semantic_ext_01",
		Question:      extQuestion,
		TimeContext:   contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
}

func extLines(buf *bytes.Buffer, message string) []map[string]any {
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(raw, message) {
			continue
		}
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil {
			out = append(out, line)
		}
	}
	return out
}

// windowReceipt returns the first window offer's receipt on a result.
func windowReceipt(t *testing.T, result contextfabric.InvestigationResult) string {
	t.Helper()
	if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
		t.Fatalf("result %s offers no window receipt (status %s)", result.ResultID, result.Status)
	}
	return result.WindowClarification.Options[0].ReceiptID
}

// freshStore is a NEW store handle over the database: nothing a previous turn
// held in memory reaches the next one.
func freshStore(t *testing.T, db *sql.DB) contextfabric.InvestigationResultStore {
	t.Helper()
	store, err := pginvestigation.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestSemanticContinuation_TheCarriedReadingSurvivesFreshEnginesAndARegistryChange(t *testing.T) {
	db := newInvestigationTestDatabase(t, context.Background())
	store := freshStore(t, db)
	principal := storage.Principal{OrgID: "org_semantic_ext"}
	carriedFrame := contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:    contextfabric.SubjectExpressionGroupedMembers,
			Grouped: &contextfabric.GroupedSetExpression{GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectRepository},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
	}
	// The DISAGREEING reading every later turn's model proposes.
	freshFrame := contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind, Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectProject}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	turnOneCalls, laterCalls := &atomic.Int64{}, &atomic.Int64{}
	// Turn one's registry declares the real derivation's rows; the registry
	// in force later declares NOTHING -- a continuation that re-derived would
	// serve an empty requirement array.
	turnOneDeriver := extDeriver{calls: turnOneCalls, derive: func(f contextfabric.QuestionFrame) []contextfabric.DerivedRequirement {
		return contextfabric.DeriveRequirements(f, contextfabric.ObligationSeed{}, nil)
	}}
	laterDeriver := extDeriver{calls: laterCalls, derive: func(contextfabric.QuestionFrame) []contextfabric.DerivedRequirement { return nil }}

	// TURN ONE: no window on the request, so the class-default gate stops it
	// to offer one -- and it SAVES its accepted reading.
	var sink1 bytes.Buffer
	turnOne, err := extEngine(t, store, extInterpreter{family: contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus, frame: carriedFrame}, turnOneDeriver, "result_semantic_ext_turn1", &sink1).
		Investigate(context.Background(), principal, extRequest())
	if err != nil {
		t.Fatalf("turn one: %v", err)
	}
	storedOne, err := store.Get(context.Background(), principal, turnOne.ResultID)
	if err != nil {
		t.Fatalf("turn one Get: %v", err)
	}
	// THE ASSERTION COMES BEFORE THE LOG, and the log reads nothing the
	// assertion has not established. A mutant that stops the snapshot reaching
	// the row leaves SemanticState nil here, and a log line that dereferenced
	// it first turned a clean assertion failure into a panic -- which aborts
	// the whole package binary, so the tests after this one never run and the
	// arm reads as "the run did not cover the package list" instead of as the
	// kill it is.
	t.Logf("TURN ONE status=%s read=%s snapshot=%v", turnOne.Status, storedOne.SemanticStateRead, storedOne.SemanticState != nil)
	if storedOne.SemanticStateRead != contextfabric.SemanticStateReadAvailable || storedOne.SemanticState == nil {
		t.Fatalf("turn one did not persist a readable reading: read=%s snapshot=%v", storedOne.SemanticStateRead, storedOne.SemanticState != nil)
	}
	t.Logf("TURN ONE family=%s requirements=%d roles=%d", storedOne.SemanticState.Family, len(storedOne.SemanticState.Requirements), len(storedOne.SemanticState.Roles))
	if storedOne.SemanticState.Frame == nil || len(storedOne.SemanticState.Requirements) == 0 {
		t.Fatalf("turn one did not persist a complete reading: frame=%v requirements=%d", storedOne.SemanticState.Frame != nil, len(storedOne.SemanticState.Requirements))
	}
	persist1 := extLines(&sink1, "context fabric semantic state persistence")
	if len(persist1) != 1 || persist1[0]["decision"] != "persisted" || persist1[0]["site"] != "window_confirmation_required" {
		t.Fatalf("turn one persistence line = %v", persist1)
	}

	// TURN TWO: a FRESH engine, same store, identical question bytes, one
	// window receipt -- and a model that disagrees on everything.
	var sink2 bytes.Buffer
	request2 := extRequest()
	request2.PriorWindowReceipts = []contextfabric.BoundSubjectReceipt{{ResultID: turnOne.ResultID, ReceiptID: windowReceipt(t, turnOne)}}
	turnTwo, err := extEngine(t, freshStore(t, db), extInterpreter{family: contractsv1.ContextFabricQuestionFamilyDiscoveredCohortRanking, frame: freshFrame}, laterDeriver, "result_semantic_ext_turn2", &sink2).
		Investigate(context.Background(), principal, request2)
	if err != nil {
		t.Fatalf("turn two: %v", err)
	}
	decision := extLines(&sink2, "context fabric window continuation decision")
	if len(decision) != 1 {
		t.Fatalf("turn two decision lines = %d", len(decision))
	}
	t.Logf("TURN TWO status=%s plan=%+v decision=%v", turnTwo.Status, turnTwo.AnswerPlan, decision[0]["continuation_disposition"])
	if decision[0]["continuation_disposition"] != "applied" || decision[0]["carried_state_read"] != "available" {
		t.Fatalf("turn two did not continue the persisted reading: %v", decision[0])
	}
	if turnTwo.AnswerPlan == nil || turnTwo.AnswerPlan.Family != contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus ||
		turnTwo.AnswerPlan.FamilySource != contractsv1.ContextFabricQuestionFamilySourceCarried {
		t.Fatalf("turn two served %+v, want the carried grouped_cohort_status", turnTwo.AnswerPlan)
	}
	// THE CARRIED DECLARATIONS, NOT A RE-DERIVATION: the registry in force
	// declares nothing, and the served plan still carries turn one's rows.
	wantRows := contextfabric.PlanRequirementsFromDerived(storedOne.SemanticState.DerivedRequirements())
	gotRows, _ := json.Marshal(turnTwo.AnswerPlan.Requirements)
	wantJSON, _ := json.Marshal(wantRows)
	if string(gotRows) != string(wantJSON) {
		t.Fatalf("turn two plan requirements = %s, want the carried %s", gotRows, wantJSON)
	}
	storedTwo, err := store.Get(context.Background(), principal, turnTwo.ResultID)
	if err != nil {
		t.Fatalf("turn two Get: %v", err)
	}
	// MATERIALIZED: turn two saved the carried reading as its own snapshot.
	if storedTwo.SemanticState == nil {
		t.Fatalf("turn two persisted no reading: read=%s", storedTwo.SemanticStateRead)
	}
	if storedTwo.SemanticStateRead != contextfabric.SemanticStateReadAvailable ||
		!sameValue(storedTwo.SemanticState.Frame, storedOne.SemanticState.Frame) ||
		!sameValue(storedTwo.SemanticState.Requirements, storedOne.SemanticState.Requirements) ||
		!sameValue(storedTwo.SemanticState.Roles, storedOne.SemanticState.Roles) ||
		storedTwo.SemanticState.Family != storedOne.SemanticState.Family ||
		storedTwo.SemanticState.GroupKind != storedOne.SemanticState.GroupKind ||
		storedTwo.SemanticState.FamilySource != contractsv1.ContextFabricQuestionFamilySourceCarried {
		t.Fatalf("turn two did not materialize the carried reading: %+v", storedTwo.SemanticState)
	}
	// The trace carries both readings' VALUES on the decision line.
	carried, _ := decision[0]["carried_state"].(map[string]any)
	fresh, _ := decision[0]["fresh_state"].(map[string]any)
	if carried["family"] != "grouped_cohort_status" || carried["subject_group_kind"] != "team" || fresh["family"] != "discovered_cohort_ranking" || fresh["subject_member_kind"] != "project" {
		t.Fatalf("decision line readings: carried=%v fresh=%v", carried, fresh)
	}
	if reqs, _ := carried["requirements"].([]any); len(reqs) != len(storedOne.SemanticState.Requirements) {
		t.Fatalf("decision line carried requirements = %v, want %d closed tokens", carried["requirements"], len(storedOne.SemanticState.Requirements))
	}
	if turnOneCalls.Load() == 0 {
		t.Fatalf("fixture defect: turn one's registry was never consulted")
	}
	t.Logf("TURN TWO offers a window receipt: %v", turnTwo.WindowClarification != nil && len(turnTwo.WindowClarification.Options) > 0)

	// AN UNAVAILABLE CARRIER IS REFUSED, NEVER RE-DERIVED. Turn one's row is
	// rewritten in place to each unavailable shape -- the column a legacy row
	// has (NULL), a malformed document, a format this build does not read --
	// and the same continuation, through a fresh engine and store, refuses with
	// the reason that names the shape and reads no fact.
	for i, tc := range []struct {
		name       string
		column     any
		wantRead   string
		wantReason string
	}{
		{"legacy row with no snapshot", nil, "absent", "semantic_state_absent"},
		{"malformed snapshot", `{"format_version":"semantic-state.v1","family":"not-a-family"}`, "malformed", "semantic_state_invalid"},
		{"unsupported snapshot format", `{"format_version":"semantic-state.v9"}`, "unsupported_version", "context_version_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A FRESH turn one per cell: window receipts are single-use, so
			// the one turn two redeemed cannot be redeemed again.
			var sink0 bytes.Buffer
			carrier, err := extEngine(t, freshStore(t, db), extInterpreter{family: contractsv1.ContextFabricQuestionFamilyGroupedCohortStatus, frame: carriedFrame}, turnOneDeriver, "result_semantic_ext_carrier_"+string(rune('a'+i)), &sink0).
				Investigate(context.Background(), principal, extRequest())
			if err != nil {
				t.Fatalf("carrier turn: %v", err)
			}
			if _, err := db.ExecContext(context.Background(), `UPDATE acr.context_fabric_investigation_results SET semantic_state = $1::jsonb WHERE result_id = $2`, tc.column, carrier.ResultID); err != nil {
				t.Fatalf("rewrite the carrier's column: %v", err)
			}
			var sink bytes.Buffer
			request := extRequest()
			request.PriorWindowReceipts = []contextfabric.BoundSubjectReceipt{{ResultID: carrier.ResultID, ReceiptID: windowReceipt(t, carrier)}}
			request.RequestID = "request_semantic_ext_refused_" + string(rune('a'+i))
			refused, err := extEngine(t, freshStore(t, db), extInterpreter{family: contractsv1.ContextFabricQuestionFamilyDiscoveredCohortRanking, frame: freshFrame}, laterDeriver, "result_semantic_ext_refused_"+string(rune('a'+i)), &sink).
				Investigate(context.Background(), principal, request)
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			lines := extLines(&sink, "context fabric window continuation decision")
			if len(lines) != 1 {
				t.Fatalf("decision lines = %d", len(lines))
			}
			line := lines[0]
			t.Logf("%s -> status=%s refusal_basis=%s | line: disposition=%v reason=%v carried_state_read=%v refusal_basis=%v",
				tc.name, refused.Status, refused.RefusalBasis, line["continuation_disposition"], line["decision_reason"], line["carried_state_read"], line["refusal_basis"])
			if refused.RefusalBasis != contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable || refused.AnswerPlan != nil {
				t.Fatalf("served %s/%q plan=%v, want the continuation refusal with no plan", refused.Status, refused.RefusalBasis, refused.AnswerPlan)
			}
			if line["continuation_disposition"] != "withheld" || line["decision_reason"] != tc.wantReason || line["carried_state_read"] != tc.wantRead || line["refusal_basis"] != "continuation_context_unverifiable" {
				t.Fatalf("decision line = %v", line)
			}
			persist := extLines(&sink, "context fabric semantic state persistence")
			if len(persist) != 1 || persist[0]["absence"] != "continuation_refused" || persist[0]["site"] != "continuation_refusal" {
				t.Fatalf("the refusal's own persistence line = %v", persist)
			}
		})
	}
}

func sameValue(a, b any) bool {
	ae, _ := json.Marshal(a)
	be, _ := json.Marshal(b)
	return string(ae) == string(be)
}
