package contextfabric

// Production drivers for the transition line's other members: a requirement the
// derivation predicted UNAVAILABLE that assembly still narrowed, for each of the
// two derivation reasons a `state` cell can carry, and a reused answer.
//
// THE SHAPE. The candidate reduction attaches its narrowing to the first
// planning `state` row, whatever that row's outcome. A named team frame whose
// registry cannot serve `state` seeds that row unavailable, and an item
// overrun on unresolved candidates then appends a narrowed row for it. Which
// derivation reason the seed carries is decided by the registry the engine is
// given: none supporting teams (subject_kind_unsupported, wire `fact_pruned`)
// or one supporting teams without declaring `state` (no_declaring_producer,
// wire `fact_unconfigured`).

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const narrowedStateRequirement = "state/subject/team"

// teamStatusWithoutStateCapabilities is a registry that reaches teams but does
// not declare `state` for them, so the derivation reports
// no_declaring_producer for state/subject/team and serves
// principal_drivers/subject/team from the status kind.
func teamStatusWithoutStateCapabilities() []FactCapability {
	return []FactCapability{{
		Kind:                  FactStatus,
		SupportedSubjectKinds: []SubjectKind{SubjectTeam},
		Obligations:           map[SubjectKind][]AnswerObligation{SubjectTeam: {ObligationPrincipalDrivers}},
	}}
}

// newNarrowedStateEngine is the candidate reduction's own acceptance shape --
// 18 unresolved candidates, 12 status claims and 5 drivers against a 30-item
// ceiling -- with a named team frame and the requirement deriver under the
// test's control.
func newNarrowedStateEngine(t *testing.T, deriver RequirementDeriver, telemetry EngineTelemetry) *Engine {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "org:linear:CHAOS", Label: "CHAOS"}
	// assess_state derives the `state` cell the reduction attaches to;
	// explain_drivers derives principal_drivers, a READ the status kind serves
	// under teamStatusWithoutStateCapabilities.
	frame := frameWith([]InvestigationGoal{GoalAssessState, GoalExplainDrivers}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "status",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}},
			},
			outcome: QuestionFamilyOutcome{
				Frame: &frame, FrameObligations: frame.Obligations,
				Family: QuestionFamilySubjectInvestigation, Source: QuestionFamilySourceModel,
			},
		},
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: outcomeAssemblyCandidates(18), Committed: []SubjectRef{team}},
			context: GraphContext{
				Cohort: nil, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			claims := make([]ClaimedFact, 0, 12)
			for index := 0; index < 12; index++ {
				claims = append(claims, ClaimedFact{
					ClaimID: "claim_workload_" + string(rune('a'+index)),
					Kind:    FactStatus, Subject: team, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Throughput held roughly flat.",
				CurrentState:       "Within the band observed over the window.",
				StrongestPressures: []string{}, Drivers: outcomeAssemblyDrivers(5, team, claimIDs(claims)), RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{outcomeAssemblyEvidenceRef}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Throughput held roughly flat over the requested window.",
				Warnings:            []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry:    telemetry,
		Requirements: deriver,
	}, budgetStageOptions(30, time.Second))
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func runNarrowedState(t *testing.T, ctx context.Context, engine *Engine) InvestigationResult {
	t.Helper()
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// runNarrowedStateLogged drives the shape through the production slog JSON sink.
func runNarrowedStateLogged(t *testing.T, deriver RequirementDeriver) (InvestigationResult, *bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	requestID := reconciliationRequestID(t)
	result := runNarrowedState(t, observability.WithRequestID(context.Background(), requestID), newNarrowedStateEngine(t, deriver, telemetry))
	return result, &buf, requestID
}

// transitionFor returns the one recorded transition for an identity.
func transitionFor(t *testing.T, events []RequirementOutcomeTransitionEvent, identity string) RequirementOutcomeTransitionEvent {
	t.Helper()
	var found []RequirementOutcomeTransitionEvent
	for _, event := range events {
		if event.Requirement == identity {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("recorded %d transition(s) for %q, want 1: %+v", len(found), identity, events)
	}
	return found[0]
}

// assertTransitionSetIsNumbered pins the bound every line carries: index covers
// 1..total exactly once and total is the number of lines.
func assertTransitionSetIsNumbered(t *testing.T, events []RequirementOutcomeTransitionEvent) {
	t.Helper()
	seen := map[int]bool{}
	for _, event := range events {
		if event.Total != len(events) {
			t.Fatalf("line %q carries total=%d, the request emitted %d", event.Requirement, event.Total, len(events))
		}
		if event.Index < 1 || event.Index > event.Total || seen[event.Index] {
			t.Fatalf("line %q carries index=%d outside 1..%d or twice", event.Requirement, event.Index, event.Total)
		}
		seen[event.Index] = true
	}
}

// TestAnUnservableStateNarrowedAtAssemblyNamesItsDerivationReason drives both
// derivation reasons a `state` seed can carry to the same assembled narrowing,
// and pins that the line names each reason where the wire code names two
// different collapsed codes.
func TestAnUnservableStateNarrowedAtAssemblyNamesItsDerivationReason(t *testing.T) {
	t.Parallel()
	for _, cell := range []struct {
		name     string
		deriver  RequirementDeriver
		reason   RequirementUnavailableReason
		seedCode contractsv1.ContextFabricCoverageDetailCode
	}{
		{"no producer reaches teams", registryDeriver{}, RequirementReasonSubjectKindUnsupported, contractsv1.ContextFabricCoverageDetailFactPruned},
		{"teams reached, state undeclared", registryDeriver{capabilities: teamStatusWithoutStateCapabilities()}, RequirementReasonNoDeclaringProducer, contractsv1.ContextFabricCoverageDetailFactUnconfigured},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			result := runNarrowedState(t, context.Background(), newNarrowedStateEngine(t, cell.deriver, telemetry))

			seed := RequirementOutcomeRow{}
			for _, row := range result.Completeness.Outcomes {
				if row.Requirement == narrowedStateRequirement && row.Stage == contractsv1.ContextFabricOutcomeStagePlanning {
					seed = row
				}
			}
			if seed.Outcome != contractsv1.ContextFabricRequirementUnavailable || seed.CauseCoverage != cell.seedCode {
				t.Fatalf("seed = %s/%s, want unavailable/%s -- the fixture did not seed the reason under test", seed.Outcome, seed.CauseCoverage, cell.seedCode)
			}

			assertTransitionSetIsNumbered(t, telemetry.requirementOutcomeTransitions)
			got := transitionFor(t, telemetry.requirementOutcomeTransitions, narrowedStateRequirement)
			if got.Predicted != RequirementPredictedUnavailable || got.PredictedReason != cell.reason {
				t.Fatalf("predicted = %q/%q, want unavailable/%q", got.Predicted, got.PredictedReason, cell.reason)
			}
			if got.AssembledOutcome != contractsv1.ContextFabricRequirementNarrowed || got.CauseOverrun != contractsv1.ContextFabricBudgetOverrunItems {
				t.Fatalf("assembled = %q overrun=%q, want narrowed/items", got.AssembledOutcome, got.CauseOverrun)
			}
			if got.Served != 13 || got.Declared != 18 {
				t.Fatalf("served/declared = %d/%d, want 13/18", got.Served, got.Declared)
			}
			if got.AssemblyReason != RequirementAssemblyReasonNone || got.MemberSetResolved {
				t.Fatalf("cause=%q member_set_resolved=%v, want none/false", got.AssemblyReason, got.MemberSetResolved)
			}
			// An unservable requirement declares no fact kind, so no claim serves
			// it, however many status claims about the team were served.
			if got.ServedFactCount != 0 {
				t.Fatalf("served_fact_count = %d, want 0 for a requirement that declares no fact kind", got.ServedFactCount)
			}
		})
	}
}

// TestAServedReadThatAssemblyFoundUnreadCountsTheClaimsOfItsKind pins
// served_fact_count at a value that is neither zero nor one: the served
// principal_drivers read found no observation at assembly, while the document
// carries twelve claims of its declared kind about its subject kind.
func TestAServedReadThatAssemblyFoundUnreadCountsTheClaimsOfItsKind(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	runNarrowedState(t, context.Background(), newNarrowedStateEngine(t, registryDeriver{capabilities: teamStatusWithoutStateCapabilities()}, telemetry))

	got := transitionFor(t, telemetry.requirementOutcomeTransitions, "principal_drivers/subject/team")
	if got.Predicted != RequirementPredictedServed || got.PredictedReason != "" {
		t.Fatalf("predicted = %q/%q, want served", got.Predicted, got.PredictedReason)
	}
	if got.AssembledOutcome != contractsv1.ContextFabricRequirementUnavailable ||
		got.CauseCoverage != contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned {
		t.Fatalf("assembled = %q/%q, want unavailable/requirement_read_not_planned", got.AssembledOutcome, got.CauseCoverage)
	}
	if got.ServedFactCount != 12 {
		t.Fatalf("served_fact_count = %d, want 12 status claims about the team", got.ServedFactCount)
	}
	if got.AssemblyReason != RequirementAssemblyReasonNone {
		t.Fatalf("cause = %q, want none -- this row's wire code already names its mechanism", got.AssemblyReason)
	}
}

// TestTheTwoFactPrunedReasonsAreToldApartOnTheLine is the split-cause driver
// for both `fact_pruned` seeds: subject_kind_unsupported (a derivation reason)
// and computed_population_absent (an assembly reason) reach the wire as the
// same code, and the line names them as different reasons.
func TestTheTwoFactPrunedReasonsAreToldApartOnTheLine(t *testing.T) {
	t.Parallel()
	classB := &recordingTelemetry{}
	runReconciliation(t, newReconciliationEngine(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, classB, nil), 0)
	unsupported := &recordingTelemetry{}
	runNarrowedState(t, context.Background(), newNarrowedStateEngine(t, registryDeriver{}, unsupported))

	count := transitionFor(t, classB.requirementOutcomeTransitions, reconciliationCountRequirement)
	state := transitionFor(t, unsupported.requirementOutcomeTransitions, narrowedStateRequirement)

	pruned := contractsv1.ContextFabricCoverageDetailFactPruned
	if count.CauseCoverage != pruned || unavailableRequirementCause(state.PredictedReason) != pruned {
		t.Fatalf("both reasons must reach the wire as %q: count=%q state seed=%q", pruned, count.CauseCoverage, unavailableRequirementCause(state.PredictedReason))
	}
	if count.AssemblyReason != RequirementAssemblyReasonComputedPopulationAbsent {
		t.Fatalf("count line cause = %q, want computed_population_absent", count.AssemblyReason)
	}
	if state.PredictedReason != RequirementReasonSubjectKindUnsupported {
		t.Fatalf("state line predicted_reason = %q, want subject_kind_unsupported", state.PredictedReason)
	}
	if string(count.AssemblyReason) == string(state.PredictedReason) {
		t.Fatal("the two fact_pruned reasons read the same on the line")
	}
}

// TestAReusedClassBAnswerServesTheSameRowsAndNoTransition pins the reuse half:
// a stored Class B document re-served from the reuse path carries the same
// outcome rows, reconciles to the same transition, passes the invariant, and
// emits no transition line, because no assembly ran on this request.
func TestAReusedClassBAnswerServesTheSameRowsAndNoTransition(t *testing.T) {
	t.Parallel()
	fresh := runReconciliation(t, newReconciliationEngine(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, &recordingTelemetry{}, nil), 0)

	project, candidate := reusableCandidate()
	candidate.Status = fresh.Status
	candidate.AnswerPlan = fresh.AnswerPlan
	candidate.Cohort = nil
	candidate.ClaimedFacts = append([]ClaimedFact{}, fresh.ClaimedFacts...)
	candidate.Completeness.Outcomes = append([]RequirementOutcomeRow{}, fresh.Completeness.Outcomes...)
	candidate.Completeness = ComputeAnswerCompleteness(candidate)

	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project, reconciliationAnchor()}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		Telemetry: telemetry,
	})
	served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !served.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	if !reflect.DeepEqual(served.Completeness.Outcomes, fresh.Completeness.Outcomes) {
		t.Fatalf("the reused document's outcome rows differ from the stored ones:\n  reused: %+v\n  fresh:  %+v", served.Completeness.Outcomes, fresh.Completeness.Outcomes)
	}
	if got, want := ReconcileRequirementOutcomes(served), ReconcileRequirementOutcomes(fresh); !reflect.DeepEqual(got, want) || len(want) != 1 {
		t.Fatalf("reconciling the reused document gives %+v, the fresh one %+v (want one transition in both)", got, want)
	}
	if n := len(telemetry.requirementOutcomeTransitions); n != 0 {
		t.Fatalf("the reuse serve emitted %d transition line(s); no assembly ran on this request", n)
	}
	if len(telemetry.completenessAuthorities) == 0 || telemetry.completenessAuthorities[len(telemetry.completenessAuthorities)-1].ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("the reuse serve's authority observation does not derive degraded from the same rows: %+v", telemetry.completenessAuthorities)
	}
}
