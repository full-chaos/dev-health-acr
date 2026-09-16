package contextfabric

// THE HARM TESTS for requirement-outcome reconciliation, in their own file.
//
// Everything this file names exists on the parent commit, so it compiles
// there and fails at runtime: the parent serves the Class B document with no
// transition line, and serves a satisfied count that no served evidence backs.
// The internals beside it (requirement_outcome_reconciliation_test.go) name
// identifiers the parent does not have and cannot make that statement.
//
// The line is read from the bytes the PRODUCTION slog JSON handler wrote during
// a real Investigate call, never from a recorder.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// reconciliationTransitionMsg is the transition line's msg, spelled here as a
// literal so this file compiles on a tree that has no line at all.
const reconciliationTransitionMsg = "context fabric requirement outcome transition"

const reconciliationCountRequirement = "count/member/team"

// reconciliationAnchor is the scope anchor the Class B fixture commits: a
// repository, as on the diagnosed row.
func reconciliationAnchor() SubjectRef {
	return SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:ANCHOR", Label: "anchor"}
}

// reconciliationAnchorMembershipClaim is the one fact the diagnosed row served:
// a membership fact about the anchor repository. It is of the wrong subject kind
// for a count of teams, which is what makes it the unrelated fact.
func reconciliationAnchorMembershipClaim() ClaimedFact {
	return ClaimedFact{
		ClaimID: "claim_anchor_membership",
		Kind:    FactMembership,
		Subject: reconciliationAnchor(),
		Field:   "organization_id",
		Value:   ScalarValue{String: ptrString("org_1")},
	}
}

// newReconciliationEngine is the counting fixture with the retrieved member
// set, the served status and the synthesized claims under the test's control.
// The frame is children_of_scope over team members with a count goal, built
// through the shipped derivation (countingFrame).
func newReconciliationEngine(t *testing.T, cohort *Cohort, status InvestigationStatus, claims []ClaimedFact, telemetry EngineTelemetry, results InvestigationResultStore) *Engine {
	t.Helper()
	frame := countingFrame(SubjectTeam)
	anchor := reconciliationAnchor()
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}, Committed: []SubjectRef{anchor}},
		bases:      provenCommitBases(anchor),
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	if results == nil {
		results = &resultStoreStub{}
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "count",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{
				Frame: frame, FrameObligations: frame.Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
			},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			served := make([]ClaimedFact, len(claims))
			copy(served, claims)
			return InvestigationResult{
				Status: status, DirectJudgment: "Answered.",
				CurrentState: "Nominal.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        served,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Answered.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      results,
		Telemetry:    telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_57370001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// runReconciliation drives one investigation and fails unless it served.
func runReconciliation(t *testing.T, engine *Engine, maxMembers int) InvestigationResult {
	t.Helper()
	return runReconciliationWithContext(t, context.Background(), engine, maxMembers)
}

func runReconciliationWithContext(t *testing.T, ctx context.Context, engine *Engine, maxMembers int) InvestigationResult {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_57370001"
	request.Question = "reconciliation fixture"
	if maxMembers > 0 {
		request.Options.MaxCohortMembers = maxMembers
	}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// reconciliationRequestID is a canonical request id derived from the test name,
// so parallel runs keep their lines attributable. observability accepts only
// req_ + 32 lowercase hex.
func reconciliationRequestID(t *testing.T) string {
	digest := sha256.Sum256([]byte(t.Name()))
	return "req_" + hex.EncodeToString(digest[:16])
}

// runReconciliationLogged drives the fixture through the production slog JSON
// sink and returns the served result, the raw log and the request id.
func runReconciliationLogged(t *testing.T, cohort *Cohort, status InvestigationStatus, claims []ClaimedFact, maxMembers int) (InvestigationResult, *bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	engine := newReconciliationEngine(t, cohort, status, claims, telemetry, nil)
	requestID := reconciliationRequestID(t)
	result := runReconciliationWithContext(t, observability.WithRequestID(context.Background(), requestID), engine, maxMembers)
	return result, &buf, requestID
}

// reconciliationLines decodes every emitted JSON line carrying msg.
func reconciliationLines(t *testing.T, log *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("a production log line is not JSON: %v (%s)", err, raw)
		}
		if line["msg"] == msg {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestTheClassBServeLogsItsRequirementTransition carries the observability
// harm. The diagnosed row's shape by frame: the derivation predicts the team
// count served, assembly states it unavailable as `fact_pruned`, and the turn
// serves an unrelated membership fact about the anchor with status partial.
// The trace must say so, with the reason below the collapsed wire code.
func TestTheClassBServeLogsItsRequirementTransition(t *testing.T) {
	t.Parallel()
	result, log, requestID := runReconciliationLogged(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, 0)

	// The fixture must reach the diagnosed document, or the absence of a line
	// proves nothing.
	if result.Status != InvestigationPartial {
		t.Fatalf("status = %q, want partial", result.Status)
	}
	assembled := 0
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement != reconciliationCountRequirement {
			continue
		}
		switch row.Stage {
		case contractsv1.ContextFabricOutcomeStagePlanning:
			if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("planning row = %q, want the prediction satisfied", row.Outcome)
			}
		case contractsv1.ContextFabricOutcomeStageAssembledResult:
			assembled++
			if row.Outcome != contractsv1.ContextFabricRequirementUnavailable || row.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactPruned {
				t.Fatalf("assembled row = %s/%s, want unavailable/fact_pruned", row.Outcome, row.CauseCoverage)
			}
		default:
			if row.Outcome == contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("a %s row claims the count satisfied", row.Stage)
			}
		}
	}
	if assembled != 1 {
		t.Fatalf("the served document carries %d assembled count rows, want 1", assembled)
	}

	lines := reconciliationLines(t, log, reconciliationTransitionMsg)
	if len(lines) != 1 {
		t.Fatalf("emitted %d transition line(s), want exactly 1 -- a requirement predicted served that ends unavailable must be an observed transition, never a silent flip", len(lines))
	}
	want := map[string]any{
		"org_id": "org_1", "request_id": requestID,
		"requirement": reconciliationCountRequirement, "obligation": "count", "role": "member", "subject_kind": "team",
		"predicted": "served", "predicted_reason": "none",
		"assembled_outcome": "unavailable",
		"cause":             "computed_population_absent",
		"cause_coverage":    "fact_pruned", "cause_overrun": "none", "cause_narrowing": "none",
		"served": float64(0), "declared": float64(0), "served_fact_count": float64(0),
		"member_set_resolved": false,
		"index":               float64(1), "total": float64(1),
	}
	for key, value := range want {
		if got, ok := lines[0][key]; !ok || got != value {
			t.Errorf("line[%q] = %v (present=%v), want %v", key, got, ok, value)
		}
	}
}

// TestAServedCountLogsNoRequirementTransition is the control: the same frame
// over a resolved member set serves the count as predicted, and no line fires.
func TestAServedCountLogsNoRequirementTransition(t *testing.T) {
	t.Parallel()
	result, log, _ := runReconciliationLogged(t, countingCohort(SubjectTeam, 3), InvestigationComplete, nil, 0)
	satisfied := false
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == reconciliationCountRequirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			satisfied = row.Outcome == contractsv1.ContextFabricRequirementSatisfied
		}
	}
	if !satisfied {
		t.Fatal("the control did not serve the count; it controls nothing")
	}
	if lines := reconciliationLines(t, log, reconciliationTransitionMsg); len(lines) != 0 {
		t.Fatalf("emitted %d transition line(s) for a count served as predicted: %v", len(lines), lines)
	}
}

// TestASatisfiedCountWithNoServedEvidenceIsNotServed carries the invariant's
// harm: a served document whose assembled count row says satisfied while the
// document carries neither a team cardinality claim nor a team member set must
// be refused at the one point every serving path passes through.
func TestASatisfiedCountWithNoServedEvidenceIsNotServed(t *testing.T) {
	t.Parallel()
	engine := newReconciliationEngine(t, countingCohort(SubjectTeam, 3), InvestigationComplete, nil, &recordingTelemetry{}, nil)
	served := runReconciliation(t, engine, 0)

	forged := served
	forged.Cohort = countingCohort(SubjectRepository, 3)
	forged.ClaimedFacts = []ClaimedFact{reconciliationAnchorMembershipClaim()}
	stillSatisfied := false
	for _, row := range forged.Completeness.Outcomes {
		if row.Requirement == reconciliationCountRequirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult &&
			row.Outcome == contractsv1.ContextFabricRequirementSatisfied {
			stillSatisfied = true
		}
	}
	if !stillSatisfied {
		t.Fatal("the forged document no longer claims the count satisfied; it tests nothing")
	}

	_, err := engine.finalizeServed(context.Background(), storage.Principal{OrgID: "org_1"}, BudgetAssertDecisive, forged, nil, ResponseBudget{})
	if err == nil {
		t.Fatal("finalizeServed served a satisfied team count backed only by a repository member set and a membership fact about the anchor")
	}
	if !strings.Contains(err.Error(), "no served evidence of its kind and subject") || !strings.Contains(err.Error(), reconciliationCountRequirement) {
		t.Fatalf("finalizeServed refused for another reason: %v", err)
	}
}
