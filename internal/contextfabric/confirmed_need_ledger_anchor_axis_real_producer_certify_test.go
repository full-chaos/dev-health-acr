package contextfabric

// The subject_anchor axis (anchor_agreement, applied_anchor_basis,
// anchor_disposition, capture_decision) already has a real producer for
// each of its members reachable on the confirmed-need-ledger line, in
// chaos5788_committed_anchor_carry_test.go and count_population_scope_test.go
// -- none of them certify the emitted line against
// eventspec.ConfirmedNeedLedger. This file reuses each one's own rig
// verbatim (never a struct literal) and returns the raw production JSON
// plus the field/value pairs that scenario's own fixture is built to
// produce, so confirmed_need_ledger_anchor_axis_real_producer_eventspec_certify_test.go
// can certify the ACTUAL emitted line from each real exit.
//
// anchor_disposition never reaches vetoed_stale or not_evaluated on this
// specific line: anchorDispositionForTelemetry is set exactly once, from
// anchorLedgerDisposition, whose own switch returns only "",
// superseded_by_caller, vetoed_conflict, vetoed_unresolved or applied --
// the declared field reuses contractsv1.ContextFabricStructureDispositionVocabulary
// in full (the shared type's whole vocabulary, the same reasoning
// repair_member_kind's own field reuses the full subject-kind vocabulary
// though not every kind is reachable there either), so those two members
// need no real-producer scenario on this line.

import (
	"bytes"
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CaptureAxisFieldRealProducerScenarios names every subject_anchor-axis
// scenario this file can run.
func CaptureAxisFieldRealProducerScenarios() []string {
	return []string{
		"anchor_committed_agree_applied_engine_committed",
		"vetoed_conflict_disagree",
		"vetoed_unresolved_absent",
		"superseded_by_caller",
		"anchor_ambiguous",
		"organization_scope",
	}
}

// RunCaptureAxisFieldRealProducerScenarioForTest drives ONE named scenario
// through its own already-established real producer and returns the
// production slog JSON bytes it wrote, the org id it ran under, and the
// field/value pairs that scenario's own fixture is built to produce (a
// subset of the emitted line, certified via certify.Assertion.Want).
func RunCaptureAxisFieldRealProducerScenarioForTest(t *testing.T, scenario string) (log []byte, orgID string, want map[string]string) {
	t.Helper()
	orgID = "org_acceptance"

	switch scenario {
	case "anchor_committed_agree_applied_engine_committed":
		// TestCommittedAnchorSurvivesTheConfirmationTurn's own turn two: an
		// engine-committed anchor from turn one, restated by an identity-
		// proven commit of the SAME subject on turn two.
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_anchor_agree_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		two := needTurnRequest("request_5802_anchor_agree_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		twoResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		committedAnchorTurn(t, engine, graph, store, two, twoResponse)
		return buf.Bytes(), orgID, map[string]string{
			"anchor_agreement": string(ConfirmedAnchorAgreementAgree), "anchor_disposition": string(contractsv1.ContextFabricStructureDispositionApplied),
			"applied_anchor_basis": string(ConfirmedNeedBasisEngineCommitted), "capture_decision": string(CountPopulationScopeAnchorCommitted),
		}

	case "vetoed_conflict_disagree":
		// TestVetoedTurnsLedgerLineReadsThePostVetoLedger's own turn two:
		// turn two's own resolution proves a DIFFERENT identity of the
		// carried anchor's own kind.
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_anchor_disagree_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		two := needTurnRequest("request_5802_anchor_disagree_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		twoResponse := needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepoOther}},
			bases:      CommitBasisSet{SubjectMapKey(committedAnchorRepoOther): CommitBasisCallerCanonicalID},
		}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		committedAnchorTurn(t, engine, graph, store, two, twoResponse)
		return buf.Bytes(), orgID, map[string]string{
			"anchor_agreement": string(ConfirmedAnchorAgreementDisagree), "anchor_disposition": string(contractsv1.ContextFabricStructureDispositionVetoedConflict),
		}

	case "vetoed_unresolved_absent":
		// TestAbsentCarryIsDroppedNotDisclosedAsApplied's own turn two: this
		// turn's own resolution commits only the MEMBER kind, nothing of the
		// carried anchor's own kind at all.
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_anchor_absent_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		two := needTurnRequest("request_5802_anchor_absent_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		memberOnly := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:5802-absent-turn-member"}
		twoResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{memberOnly}}}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		committedAnchorTurn(t, engine, graph, store, two, twoResponse)
		return buf.Bytes(), orgID, map[string]string{
			"anchor_agreement": string(ConfirmedAnchorAgreementAbsent), "anchor_disposition": string(contractsv1.ContextFabricStructureDispositionVetoedUnresolved),
			"capture_decision": string(CountPopulationScopeAnchorUnresolved),
		}

	case "superseded_by_caller":
		// TestCallerHintOfTheSameKindSupersedesTheEngineCommittedCarry's own
		// turn two: a caller redeeming a prior offer of the carry's own kind.
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_superseded_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		callerPicked := SubjectRef{Kind: committedAnchorRepo.Kind, CanonicalID: "repository:5802-caller-picked-hint", Label: "caller picked"}
		offerResult := validInvestigationResult()
		offerResult.ResultID = "result_5802_superseded_offer"
		offerResult.SubjectResolution = SubjectResolution{
			Candidates: []SubjectCandidate{{ReceiptID: "receipt_5802_superseded_pick", Subject: callerPicked, State: ResolutionAmbiguous, MatchReasons: []string{"offered"}, Confidence: 0.5}},
		}
		store.results[offerResult.ResultID] = offerResult

		two := needTurnRequest("request_5802_superseded_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		two.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: offerResult.ResultID, ReceiptID: "receipt_5802_superseded_pick"}}
		twoResponse := needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{callerPicked}},
			bases:      provenCommitBases(callerPicked),
		}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		committedAnchorTurn(t, engine, graph, store, two, twoResponse)
		return buf.Bytes(), orgID, map[string]string{
			"anchor_disposition": string(contractsv1.ContextFabricStructureDispositionSupersededByCaller),
		}

	case "anchor_ambiguous":
		// TestUnresolvedAnchorCarriesNothing's own turn one: two candidates
		// of the anchor's own kind, nothing committed.
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_ambiguous_one", true)
		oneResponse := needTurnResponse{
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{scopeCandidate(committedAnchorRepo, "receipt_5802_a"), scopeCandidate(committedAnchorRepoOther, "receipt_5802_b")},
				Committed:  []SubjectRef{},
			},
		}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		committedAnchorTurn(t, engine, graph, store, one, oneResponse)
		return buf.Bytes(), orgID, map[string]string{
			"capture_decision": string(CountPopulationScopeAnchorAmbiguous),
		}

	case "organization_scope":
		// count_population_scope_test.go's own "organization-level discovered
		// count, nothing committed" cell: a discovered-kind frame has no
		// anchor to resolve at all, so the population is organization-wide.
		graph := &needTurnGraph{response: needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}}
		store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
		var buf bytes.Buffer
		telemetry := NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		engine, err := NewEngine(EngineDependencies{
			Interpreter: familyInterpreter{
				interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
				outcome: QuestionFamilyOutcome{
					Frame:  frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, discoveredExpression(SubjectTeam)),
					Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel,
				},
			},
			Graph: graph,
			Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
			}),
			Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				return validInvestigationResult(), nil
			}),
			Results: store, Telemetry: telemetry,
		}, EngineOptions{ServiceVersion: "chaos-5802-organization-scope-certify", Now: func() time.Time { return time.Unix(700, 0).UTC() }, NewResultID: func() string { return "result_5802_org_scope" }})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), needTurnRequest("request_5802_org_scope", true)); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), acceptancePrincipal().OrgID, map[string]string{
			"capture_decision": string(CountPopulationScopeOrganization),
		}

	default:
		t.Fatalf("unknown CaptureAxisFieldRealProducer scenario %q", scenario)
		return nil, "", nil
	}
}
