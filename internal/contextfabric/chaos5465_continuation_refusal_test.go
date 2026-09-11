package contextfabric

// Pins for the continuation refusal: a window-only continuation whose carrier
// could not be established ends the turn above retrieval with the wire basis
// `continuation_context_unverifiable` and its fixed sentence.
//
// Every refusing cell is driven through Engine.Investigate with doubles that
// COUNT the downstream reads, so "no fact was read" is measured, not argued.
// Every conjunct of the refusal predicate has a cell where it alone is false.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// servedPlanAxes reads the served plan's family, provenance and group axis,
// nil-safe: a refused turn publishes no plan at all.
func servedPlanAxes(result InvestigationResult) (QuestionFamily, QuestionFamilySource, SubjectKind) {
	if result.AnswerPlan == nil {
		return "", "", ""
	}
	return result.AnswerPlan.Family, result.AnswerPlan.FamilySource, result.AnswerPlan.GroupKind
}

// servedPlanFamily, servedPlanSource and servedPlanGroup are servedPlanAxes'
// three halves, for pins that read one of them. Every pin in these files reads
// the served plan through them, so a refused turn (no plan) is a failed
// assertion rather than a panic that aborts the whole package.
func servedPlanFamily(result InvestigationResult) QuestionFamily {
	family, _, _ := servedPlanAxes(result)
	return family
}

func servedPlanSource(result InvestigationResult) QuestionFamilySource {
	_, source, _ := servedPlanAxes(result)
	return source
}

func servedPlanGroup(result InvestigationResult) SubjectKind {
	_, _, group := servedPlanAxes(result)
	return group
}

// assertContinuationRefused asserts the served document IS the continuation
// refusal, on both surfaces and in prose.
func assertContinuationRefused(t *testing.T, result InvestigationResult) {
	t.Helper()
	if result.Status != InvestigationNoMatch {
		t.Errorf("status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
		t.Errorf("refusal_basis = %q, want %q -- a withheld continuation must refuse, never answer under the fresh reading",
			result.RefusalBasis, contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable)
	}
	if result.Completeness.RefusalBasis != result.RefusalBasis {
		t.Errorf("completeness.refusal_basis = %q, want the mirror %q", result.Completeness.RefusalBasis, result.RefusalBasis)
	}
	if result.Completeness.TerminalReason != contractsv1.ContextFabricTerminalReasonLimitationDisclosed {
		t.Errorf("completeness.terminal_reason = %q, want %q", result.Completeness.TerminalReason, contractsv1.ContextFabricTerminalReasonLimitationDisclosed)
	}
	if len(result.Limitations) != 1 || result.Limitations[0] != contractsv1.ContextFabricContinuationContextUnverifiableLimitation {
		t.Errorf("limitations = %q, want exactly the fixed continuation sentence", result.Limitations)
	}
	if result.DeterministicAnswer != contractsv1.ContextFabricContinuationContextUnverifiableLimitation {
		t.Errorf("deterministic_answer = %q, want the fixed sentence", result.DeterministicAnswer)
	}
	if len(result.ClaimedFacts) != 0 || len(result.EvidenceRefIDs) != 0 {
		t.Errorf("claimed_facts=%d evidence_refs=%d, want 0/0 on a refusal", len(result.ClaimedFacts), len(result.EvidenceRefIDs))
	}
	if result.AnswerPlan != nil {
		t.Errorf("answer_plan = %+v, want nil: the refusal precedes the planning stage, and a plan here would publish the refused fresh reading", *result.AnswerPlan)
	}
	if result.Interpretation.RequestedJudgment != continuationRefusalPlaceholderJudgment {
		t.Errorf("interpretation.requested_judgment = %q, want the placeholder %q -- the fresh reading must not be published as this turn's own",
			result.Interpretation.RequestedJudgment, continuationRefusalPlaceholderJudgment)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("the served refusal does not validate: %v", err)
	}
}

// readCountingGraph counts every retrieval call. Resolution and discovery are
// the reads a refusal must never reach.
type readCountingGraph struct {
	graphReaderStub
	resolves  *atomic.Int64
	discovers *atomic.Int64
}

func (g readCountingGraph) ResolveSubjects(ctx context.Context, p storage.Principal, r InvestigationRequest, i InterpretedQuestion, b ResolvedGraphBinding, k *ConfirmedExpectedKind, a *ConfirmedAnchorSelection, f *QuestionFrame, s SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolves.Add(1)
	return g.graphReaderStub.ResolveSubjects(ctx, p, r, i, b, k, a, f, s)
}

func (g readCountingGraph) DiscoverContext(ctx context.Context, p storage.Principal, r GraphDiscoveryRequest) (GraphContext, error) {
	g.discovers.Add(1)
	return g.graphReaderStub.DiscoverContext(ctx, p, r)
}

// refusalStore wraps the static store. It records the ancestry parent Save
// received, can fail a later Get of one id (a carrier readable for the WINDOW
// and unreadable for admission), and can fail Save.
type refusalStore struct {
	*staticResultStore
	mu            sync.Mutex
	getsByID      map[string]int
	failGetOf     string
	failGetAfter  int
	saveErr       error
	savedParents  []string
	savedResultID []string
}

func newRefusalStore(base *staticResultStore) *refusalStore {
	return &refusalStore{staticResultStore: base, getsByID: map[string]int{}}
}

func (s *refusalStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	s.mu.Lock()
	s.getsByID[resultID]++
	count := s.getsByID[resultID]
	s.mu.Unlock()
	if s.failGetOf != "" && resultID == s.failGetOf && count > s.failGetAfter {
		return StoredInvestigationResult{}, errors.New("carrier unreadable (refusal fixture)")
	}
	return s.staticResultStore.Get(ctx, principal, resultID)
}

func (s *refusalStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, snap SourceWatermarkSnapshot, epoch RebuildEpoch, axisKey string, retrieval ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parentResultID string) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.mu.Lock()
	s.savedParents = append(s.savedParents, parentResultID)
	s.savedResultID = append(s.savedResultID, result.ResultID)
	s.mu.Unlock()
	return s.staticResultStore.Save(ctx, principal, result, snap, epoch, axisKey, retrieval, prompts, authorities, graphEpoch, parentResultID)
}

type refusalReadCounts struct {
	resolves, discovers, facts, syntheses atomic.Int64
}

func newRefusalEngine(t *testing.T, store InvestigationResultStore, interpreter QuestionInterpreter, telemetry EngineTelemetry) (*Engine, *refusalReadCounts) {
	t.Helper()
	counts := &refusalReadCounts{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: readCountingGraph{
			graphReaderStub: graphReaderStub{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				bases:      provenCommitBases(project),
			},
			resolves:  &counts.resolves,
			discovers: &counts.discovers,
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			counts.facts.Add(1)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			counts.syntheses.Add(1)
			return fresh, nil
		}),
		Interpreter: interpreter,
		Results:     store,
		Telemetry:   telemetry,
	})
	return engine, counts
}

// gateRefusingInterpreter proposes a family whose frame the gate REFUSES on an
// invariant. It is the fresh-gate conjunct's driver.
type gateRefusingInterpreter struct{ family QuestionFamily }

func (i gateRefusingInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		QuestionFamilyOutcome{
			Family: i.family, Source: QuestionFamilySourceModel,
			Gate:               FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariant("i6")},
			WinningSampleIndex: 0, WinningSample: FamilySample{ModelFamily: i.family},
			Version: QuestionFamilyTableVersion,
		}, nil
}

// refusalCell is one engine-driven cell of the refusal matrix.
type refusalCell struct {
	name        string
	mutate      func(*InvestigationRequest)
	prior       func(InvestigationResult) InvestigationResult
	priorFamily QuestionFamily
	priorGroup  SubjectKind
	storeEpoch  *int64
	failGetOf   string
	// failGetAfter is how many reads of failGetOf succeed before every later
	// one fails.
	failGetAfter int
	interpreter  QuestionInterpreter

	wantRefused         bool
	wantDisposition     ContinuationDisposition
	wantReason          ContinuationDecisionReason
	wantDecisionEmitted bool
	// wantServedBasis is the basis the SERVED document carries, which on the
	// fresh-gate cell is the frame's own basis rather than the continuation's.
	wantServedBasis contractsv1.ContextFabricRefusalBasis
	// wantCarrierRead is what admission's read of the carrier must publish;
	// every window-only decision also names the carrier it is about.
	wantCarrierRead ContinuationCarrierRead
}

func staleEpoch() *int64 { e := int64(97); return &e }

func refusalCells() []refusalCell {
	forced := forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}
	return []refusalCell{
		// ---- REFUSING CELLS: every withheld admission and composition path.
		{
			name:        "carrier from another graph epoch",
			storeEpoch:  staleEpoch(),
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonInvalidContext,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
			wantCarrierRead: ContinuationCarrierReadOK,
		},
		// ---- ONE READ OF THE CARRIER PER REQUEST. A store read that fails
		// is never a persisted "cannot verify" refusal: it fails the window
		// redemption (retryable veto), and a redemption that succeeded is the
		// read admission uses, so there is no second read to fail.
		{
			name: "carrier read once for the window is not re-read for admission",
			// Every read of the carrier after the first fails.
			failGetOf: continuationPriorID, failGetAfter: 1,
			interpreter: forced,
			wantRefused: false, wantDisposition: ContinuationApplied, wantReason: ContinuationReasonNone,
			wantDecisionEmitted: true, wantCarrierRead: ContinuationCarrierReadOK,
		},
		{
			name: "carrier unreadable on every read is the retryable window veto",
			failGetOf: continuationPriorID, failGetAfter: 0,
			interpreter: forced,
			wantRefused: false, wantDisposition: ContinuationNotApplicable, wantReason: ContinuationReasonWindowVeto,
			wantDecisionEmitted: true, wantCarrierRead: ContinuationCarrierNotRead,
		},
		{
			name: "carrier recorded under a family table not in force",
			prior: func(p InvestigationResult) InvestigationResult {
				p.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
				return p
			},
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonContextVersionMismatch,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carried axis cannot be expressed by the fresh frame",
			priorFamily: QuestionFamilyGroupedCohortStatus, priorGroup: contractsv1.ContextFabricSubjectTeam,
			interpreter: r1UngroupedInterpreter{family: QuestionFamilyDiscoveredCohortRanking},
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonCompositionInvalid,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name: "composed frame violates a frame invariant",
			// The carried group axis equals the fresh frame's MEMBER kind, so
			// the composition is grouped-by-itself (i6).
			priorFamily: QuestionFamilyGroupedCohortStatus, priorGroup: SubjectRepository,
			interpreter: frameBearingInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam, frameGroup: contractsv1.ContextFabricSubjectTeam},
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonCompositionInvalid,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},

		// ---- EACH CONJUNCT FALSE ALONE, from the stale-epoch base.
		{
			name:        "conjunct observed=false: no window receipt at all",
			mutate:      func(r *InvestigationRequest) { r.PriorWindowReceipts = nil },
			storeEpoch:  staleEpoch(),
			interpreter: forced,
			wantRefused: false, wantDecisionEmitted: false,
		},
		{
			name:        "conjunct window_only=false: a parent reference beside the window receipt",
			mutate:      func(r *InvestigationRequest) { r.ParentResultID = continuationOlderID },
			storeEpoch:  staleEpoch(),
			interpreter: forced,
			wantRefused: false, wantDisposition: ContinuationNotApplicable, wantReason: ContinuationReasonNotWindowOnly,
			wantDecisionEmitted: true,
		},
		{
			name:        "conjunct withheld=false: the continuation applies",
			interpreter: forced,
			wantRefused: false, wantDisposition: ContinuationApplied, wantReason: ContinuationReasonNone,
			wantDecisionEmitted: true,
		},
		{
			name:        "conjunct withheld=false: the question changed, nothing to withhold",
			prior:       func(p InvestigationResult) InvestigationResult { p.Question = driftQuestion; return p },
			interpreter: forced,
			wantRefused: false, wantDisposition: ContinuationNotApplicable, wantReason: ContinuationReasonChangedQuestion,
			wantDecisionEmitted: true,
		},
		{
			name:        "conjunct fresh_gate_passes=false: the fresh frame's own refusal stands",
			storeEpoch:  staleEpoch(),
			interpreter: gateRefusingInterpreter{family: QuestionFamilyGroupedCohortStatus},
			wantRefused: false, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonInvalidContext,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisFrameInvariantViolated,
		},
	}
}

func TestContinuationRefusal_TheRefusalMatrixThroughTheEngine(t *testing.T) {
	t.Parallel()
	base := validInvestigationRequest().Question
	for _, cell := range refusalCells() {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			request := continuationRequest(base)
			if cell.mutate != nil {
				cell.mutate(&request)
			}
			family, group := cell.priorFamily, cell.priorGroup
			if family == "" {
				family = QuestionFamilyDiscoveredCohortRanking
			}
			prior := continuationPrior(t, continuationPriorID, base, family, group)
			if cell.prior != nil {
				prior = cell.prior(prior)
			}
			older := continuationPrior(t, continuationOlderID, base, QuestionFamilyDiscoveredCohortRanking, "")
			store := newRefusalStore(&staticResultStore{
				results:    map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older},
				graphEpoch: cell.storeEpoch,
			})
			if cell.failGetOf != "" {
				store.failGetOf, store.failGetAfter = cell.failGetOf, cell.failGetAfter
			}
			telemetry := &recordingTelemetry{}
			engine, counts := newRefusalEngine(t, store, cell.interpreter, telemetry)

			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v -- a refusal is a served document, never an error", err)
			}
			t.Logf("cell=%q status=%q refusal_basis=%q resolves=%d discovers=%d facts=%d syntheses=%d carrier_gets=%d",
				cell.name, result.Status, result.RefusalBasis, counts.resolves.Load(), counts.discovers.Load(),
				counts.facts.Load(), counts.syntheses.Load(), store.getsByID[continuationPriorID])

			if result.RefusalBasis != cell.wantServedBasis {
				t.Errorf("served refusal_basis = %q, want %q", result.RefusalBasis, cell.wantServedBasis)
			}
			if cell.wantRefused {
				assertContinuationRefused(t, result)
				// NO DOWNSTREAM READ. Counted on the doubles, not inferred from
				// the result's empty arrays.
				if n := counts.resolves.Load() + counts.discovers.Load() + counts.facts.Load() + counts.syntheses.Load(); n != 0 {
					t.Errorf("the refusal reached %d retrieval/synthesis call(s) (resolve=%d discover=%d facts=%d synth=%d), want 0 -- it must stop above retrieval",
						n, counts.resolves.Load(), counts.discovers.Load(), counts.facts.Load(), counts.syntheses.Load())
				}
				// PERSISTED, and the refused carrier is not its ancestry.
				if len(store.savedResultID) != 1 || store.savedResultID[0] != result.ResultID {
					t.Errorf("saved result ids = %q, want exactly the served refusal %q", store.savedResultID, result.ResultID)
				}
				if len(store.savedParents) == 1 && store.savedParents[0] == continuationPriorID {
					t.Errorf("the refusal recorded the refused carrier %q as its ancestry parent -- a reference this turn proved unusable is laundering material, not history", continuationPriorID)
				}
				if len(store.savedParents) != 1 || store.savedParents[0] != "" {
					t.Errorf("saved ancestry parents = %q, want exactly one empty parent (the only reference was the refused carrier)", store.savedParents)
				}
				assertBudgetStage(t, telemetry, BudgetAssertContinuationRefusal)
			} else {
				if result.RefusalBasis == contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
					t.Errorf("a cell with one conjunct false still refused as a continuation")
				}
				for _, limitation := range result.Limitations {
					if limitation == contractsv1.ContextFabricContinuationContextUnverifiableLimitation {
						t.Errorf("the continuation sentence reached a document that was not the continuation refusal")
					}
				}
			}

			if !cell.wantDecisionEmitted {
				if len(telemetry.windowContinuationDecisions) != 0 {
					t.Errorf("emitted %d decisions for a request with no window receipt", len(telemetry.windowContinuationDecisions))
				}
				return
			}
			if len(telemetry.windowContinuationDecisions) != 1 {
				t.Fatalf("got %d decisions, want exactly 1", len(telemetry.windowContinuationDecisions))
			}
			decision := telemetry.windowContinuationDecisions[0]
			if decision.Disposition != cell.wantDisposition || decision.Reason != cell.wantReason {
				t.Errorf("decision = %q/%q, want %q/%q", decision.Disposition, decision.Reason, cell.wantDisposition, cell.wantReason)
			}
			wantDecisionBasis := contractsv1.ContextFabricRefusalBasis("")
			if cell.wantRefused {
				wantDecisionBasis = contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable
			}
			if decision.RefusalBasis != wantDecisionBasis {
				t.Errorf("decision refusal_basis = %q, want %q -- the line must name exactly the refusal the caller received", decision.RefusalBasis, wantDecisionBasis)
			}
			if cell.wantRefused && decision.Accepted != nil {
				t.Errorf("a refused continuation publishes accepted context %+v", *decision.Accepted)
			}
			if decision.WindowOnlyShape && decision.ReferencedResultID != continuationPriorID {
				t.Errorf("decision referenced_result_id = %q, want the carrier the request names %q", decision.ReferencedResultID, continuationPriorID)
			}
			if cell.wantCarrierRead != "" && decision.ObservableCarrierRead() != cell.wantCarrierRead {
				t.Errorf("decision carrier_read = %q, want %q", decision.ObservableCarrierRead(), cell.wantCarrierRead)
			}
		})
	}
}

// TestContinuationRefusal_AdmissionPublishesAnUnreadableCarrierAsItsOwnValue
// drives admission WITHOUT the per-request memo (the only way its read can
// fail independently of window redemption): the failure is still withheld,
// but the line says the carrier could not be read, beside a stale-epoch
// carrier that was read and proved invalid under the same decision_reason.
func TestContinuationRefusal_AdmissionPublishesAnUnreadableCarrierAsItsOwnValue(t *testing.T) {
	t.Parallel()
	base := validInvestigationRequest().Question
	for _, tc := range []struct {
		name       string
		failGet    bool
		epoch      *int64
		wantRead   ContinuationCarrierRead
		wantReason ContinuationDecisionReason
	}{
		{"store read fails", true, nil, ContinuationCarrierReadFailed, ContinuationReasonInvalidContext},
		{"carrier from another graph epoch", false, staleEpoch(), ContinuationCarrierReadOK, ContinuationReasonInvalidContext},
		{"carrier admitted", false, nil, ContinuationCarrierReadOK, ContinuationReasonNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
			store := newRefusalStore(&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: tc.epoch})
			if tc.failGet {
				store.failGetOf, store.failGetAfter = continuationPriorID, 0
			}
			var buf bytes.Buffer
			sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus}, sink)
			binding := ResolvedGraphBinding{Epoch: 1}
			if tc.epoch == nil {
				binding.Epoch = 0
			}
			decision := engine.admitWindowContinuation(context.Background(), acceptancePrincipal(), continuationRequest(base),
				binding, nil, nil, contractsv1.ContextFabricTemporalCurrent)
			sink.RecordWindowContinuationDecision(context.Background(), acceptancePrincipal(), decision)
			line := decisionLine(t, buf.String())
			t.Logf("%s -> disposition=%v decision_reason=%v carrier_read=%v referenced_result_id=%v",
				tc.name, line["continuation_disposition"], line["decision_reason"], line["carrier_read"], line["referenced_result_id"])
			if line["carrier_read"] != string(tc.wantRead) || line["decision_reason"] != string(tc.wantReason) {
				t.Errorf("line carrier_read=%v decision_reason=%v, want %q/%q", line["carrier_read"], line["decision_reason"], tc.wantRead, tc.wantReason)
			}
			if line["referenced_result_id"] != continuationPriorID {
				t.Errorf("line referenced_result_id = %v, want %q", line["referenced_result_id"], continuationPriorID)
			}
		})
	}
}

// TestContinuationRefusal_AModelEchoOfAnyFixedDisclosureNeverFailsAnOrdinaryTurn
// is the class pin behind the validator's one-directional sentence rule: model
// caveats reach Limitations unfiltered, and the turn the continuation sentence
// asks for carries it verbatim in the conversation the model reads. For EVERY
// fixed service disclosure, an ordinary turn whose model caveat reproduces it
// is served, never turned into an error.
func TestContinuationRefusal_AModelEchoOfAnyFixedDisclosureNeverFailsAnOrdinaryTurn(t *testing.T) {
	t.Parallel()
	for _, disclosure := range contractsv1.ContextFabricServiceAuthoredLimitations() {
		disclosure := disclosure
		name := disclosure
		if len(name) > 48 {
			name = name[:48]
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
			fresh := validInvestigationResult()
			fresh.Limitations = append(append([]string(nil), fresh.Limitations...), disclosure)
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{
					resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
					bases:      provenCommitBases(project),
				},
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return fresh, nil
				}),
				Interpreter: forcedFamilyInterpreter{family: QuestionFamilyDiscoveredCohortRanking},
				Results:     &staticResultStore{results: map[string]InvestigationResult{}},
				Telemetry:   &recordingTelemetry{},
			})
			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
			t.Logf("echo -> err=%v status=%q refusal_basis=%q", err, result.Status, result.RefusalBasis)
			if err != nil {
				t.Fatalf("an ordinary turn failed because a model caveat reproduced a fixed disclosure: %v", err)
			}
			if result.RefusalBasis != "" {
				t.Errorf("an echoed disclosure made an ordinary turn a refusal: %q", result.RefusalBasis)
			}
		})
	}
}

func assertBudgetStage(t *testing.T, telemetry *recordingTelemetry, want BudgetAssertStage) {
	t.Helper()
	for _, event := range telemetry.budgetAssertions {
		if event.Stage == want {
			return
		}
	}
	t.Errorf("no budget assertion recorded for stage %q; got %+v", want, telemetry.budgetAssertions)
}

// TestContinuationRefusal_ThePredicateTruthTable walks all sixteen combinations
// of the four conjuncts, plus every disposition member and two non-members.
func TestContinuationRefusal_ThePredicateTruthTable(t *testing.T) {
	t.Parallel()
	passing := FrameGate{Outcome: FrameGatePassed}
	refusing := FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariant("i6")}
	for mask := 0; mask < 16; mask++ {
		observed, shape, withheld, gatePasses := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
		d := windowContinuationDecision{Observed: observed, WindowOnlyShape: shape, Disposition: ContinuationApplied}
		if withheld {
			d.Disposition = ContinuationWithheld
		}
		gate := refusing
		if gatePasses {
			gate = passing
		}
		want := observed && shape && withheld && gatePasses
		if got := d.refusesTurn(gate); got != want {
			t.Errorf("observed=%v window_only=%v withheld=%v gate_passes=%v: refusesTurn=%v, want %v", observed, shape, withheld, gatePasses, got, want)
		}
	}
	for _, disposition := range append(continuationDispositions(), "", ContinuationDisposition("invented")) {
		d := windowContinuationDecision{Observed: true, WindowOnlyShape: true, Disposition: disposition}
		want := disposition == ContinuationWithheld
		if got := d.refusesTurn(passing); got != want {
			t.Errorf("disposition=%q: refusesTurn=%v, want %v", disposition, got, want)
		}
	}
	// Every gate outcome, through the gate's own Refuses().
	for _, gate := range []FrameGate{{}, {Outcome: FrameGateNotProposed}, passing, refusing, {Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable}, {Outcome: FrameGateOutcome("invented")}} {
		d := windowContinuationDecision{Observed: true, WindowOnlyShape: true, Disposition: ContinuationWithheld}
		if got, want := d.refusesTurn(gate), !gate.Refuses(); got != want {
			t.Errorf("gate=%q: refusesTurn=%v, want %v", gate.Observable(), got, want)
		}
	}
}

// TestContinuationRefusal_TheDecisionLineNamesTheServedBasis reads the REAL sink
// at Info: the refusal, the applied control, and a refusal whose own save failed.
func TestContinuationRefusal_TheDecisionLineNamesTheServedBasis(t *testing.T) {
	t.Parallel()
	base := validInvestigationRequest().Question
	for _, tc := range []struct {
		name       string
		storeEpoch *int64
		saveErr    error
		wantErr    bool
		wantFields map[string]string
	}{
		{
			name: "refused", storeEpoch: staleEpoch(),
			wantFields: map[string]string{
				"refusal_basis": "continuation_context_unverifiable", "continuation_disposition": "withheld",
				"decision_reason": "invalid_context", "family_accepted": "", "accepted_context_id": "",
			},
		},
		{
			name: "applied control",
			wantFields: map[string]string{
				"refusal_basis": "none", "continuation_disposition": "applied", "decision_reason": "none",
				"accepted_context_id": continuationPriorID,
			},
		},
		{
			name: "refusal whose save failed", storeEpoch: staleEpoch(), saveErr: errors.New("save failed (fixture)"), wantErr: true,
			wantFields: map[string]string{
				"refusal_basis": "none", "continuation_disposition": "withheld", "decision_reason": "invalid_context",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
			store := newRefusalStore(&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: tc.storeEpoch})
			store.saveErr = tc.saveErr
			engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, sink)
			_, err := engine.Investigate(context.Background(), acceptancePrincipal(), continuationRequest(base))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Investigate() error = %v, wantErr %v", err, tc.wantErr)
			}
			line := decisionLine(t, buf.String())
			for key, want := range tc.wantFields {
				got, ok := line[key]
				if !ok {
					t.Errorf("line is missing %q; line=%v", key, line)
					continue
				}
				if s, _ := got.(string); s != want {
					t.Errorf("%s = %v, want %q; line=%v", key, got, want, line)
				}
			}
			if line["level"] != "INFO" {
				t.Errorf("level = %v, want INFO", line["level"])
			}
		})
	}
}

func decisionLine(t *testing.T, output string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.Contains(raw, "context fabric window continuation decision") {
			continue
		}
		if found != nil {
			t.Fatalf("more than one continuation decision line:\n%s", output)
		}
		if err := json.Unmarshal([]byte(raw), &found); err != nil {
			t.Fatalf("decode line: %v", err)
		}
	}
	if found == nil {
		t.Fatalf("no continuation decision line at Info:\n%s", output)
	}
	return found
}

// TestContinuationRefusal_TheValidatorHoldsBothHalvesTogether applies the
// validator's input domain to the document the engine actually serves.
func TestContinuationRefusal_TheValidatorHoldsBothHalvesTogether(t *testing.T) {
	t.Parallel()
	served := servedContinuationRefusal(t)
	sentence := contractsv1.ContextFabricContinuationContextUnverifiableLimitation
	for _, tc := range []struct {
		name   string
		mutate func(*InvestigationResult)
		// want is "" for accept, otherwise a fragment of the rejecting message.
		want string
	}{
		{"canonical refusal", func(*InvestigationResult) {}, ""},
		// The sentence ALONE is not a refusal claim the validator can hold: a
		// model can echo it (the class pin
		// TestContinuationRefusal_AModelEchoOfAnyFixedDisclosureNeverFailsAnOrdinaryTurn),
		// so it validates as a caveat.
		{"basis absent, sentence present", func(r *InvestigationResult) { r.RefusalBasis = "" }, ""},
		{"sentence absent, basis present", func(r *InvestigationResult) { r.Limitations = []string{noMatchLimitationUnproven} }, "requires its fixed limitation sentence"},
		{"no limitation at all, basis present", func(r *InvestigationResult) { r.Limitations = []string{}; r.Warnings = []string{"w"} }, "requires its fixed limitation sentence"},
		// A frame basis beside the continuation sentence: the sentence is a
		// caveat here like anywhere else the basis is not its own.
		{"sentence beside a frame basis", func(r *InvestigationResult) {
			r.RefusalBasis = contractsv1.ContextFabricRefusalBasisMemberKindUnservable
		}, ""},
		// Surrounding whitespace is refused by the EXISTING narrative bound
		// before this rule is reached; pinned so the cell stays rejected
		// whichever rule gets there first.
		{"sentence with a trailing space", func(r *InvestigationResult) { r.Limitations = []string{sentence + " "} }, "violate v1 bounds"},
		{"sentence with one character changed", func(r *InvestigationResult) {
			r.Limitations = []string{strings.Replace(sentence, "unverifiable.", "unverifiable!", 1)}
		}, "requires its fixed limitation sentence"},
		{"sentence without its final period", func(r *InvestigationResult) { r.Limitations = []string{strings.TrimSuffix(sentence, ".")} }, "requires its fixed limitation sentence"},
		{"sentence upper-cased", func(r *InvestigationResult) { r.Limitations = []string{strings.ToUpper(sentence)} }, "requires its fixed limitation sentence"},
		{"lookalike sentence and no basis is an ordinary caveat", func(r *InvestigationResult) {
			r.RefusalBasis = ""
			r.Limitations = []string{strings.ToUpper(sentence)}
		}, ""},
		{"sentence beside a model caveat", func(r *InvestigationResult) { r.Limitations = []string{"A model caveat.", sentence} }, ""},
		{"basis a near-miss spelling", func(r *InvestigationResult) {
			r.RefusalBasis = contractsv1.ContextFabricRefusalBasis("continuation_context_unverifiable_ish")
		}, "is not a vocabulary member"},
		{"basis on a complete status", func(r *InvestigationResult) { r.Status = InvestigationComplete; r.DirectJudgment = "j" }, ""},
		{"completeness mirror disagrees", func(r *InvestigationResult) {
			r.Completeness.RefusalBasis = contractsv1.ContextFabricRefusalBasisMemberKindUnservable
		}, "must equal the result's refusal_basis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := served
			r.Limitations = append([]string(nil), served.Limitations...)
			tc.mutate(&r)
			if tc.name != "completeness mirror disagrees" {
				r.Completeness = ComputeAnswerCompleteness(r)
			}
			err := r.Validate()
			if tc.name == "basis on a complete status" {
				// Rejected by an EXISTING rule or by a sibling rule before it;
				// either way it must not validate.
				if err == nil {
					t.Fatalf("a complete answer carrying a refusal basis validated")
				}
				t.Logf("complete+basis rejected: %v", err)
				return
			}
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("rejected a document the contract accepts: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("accepted a document that must be rejected (%s)", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("rejected for the wrong reason:\n got:  %v\n want: %q", err, tc.want)
			}
			t.Logf("cell=%q -> err=%v", tc.name, err)
		})
	}
}

// servedContinuationRefusal drives the engine to the refusal and returns the
// served document.
func servedContinuationRefusal(t *testing.T) InvestigationResult {
	t.Helper()
	base := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
	store := newRefusalStore(&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: staleEpoch()})
	engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), continuationRequest(base))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
		t.Fatalf("fixture defect: the driver did not reach the refusal (basis %q)", result.RefusalBasis)
	}
	return result
}

// TestContinuationRefusal_TheRecognisersAndTheGateNeverSpeakForTheCarrier sweeps
// every consumer of the widened vocabulary that composes, recognises or maps a
// basis: the member-kind sentence's composer and recogniser, the frame gate's
// wire mapping, and the service-authored registry.
func TestContinuationRefusal_TheRecognisersAndTheGateNeverSpeakForTheCarrier(t *testing.T) {
	t.Parallel()
	for _, basis := range contractsv1.ContextFabricRefusalBasisVocabulary() {
		frameMember := basis != contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable
		sentence := contractsv1.ContextFabricRefusalBasisLimitation(contractsv1.ContextFabricSubjectTeam, basis)
		if got := contractsv1.IsContextFabricRefusalBasisLimitation(sentence); got != frameMember {
			t.Errorf("kind sentence with basis %q recognised=%v, want %v", basis, got, frameMember)
		}
		if got := contractsv1.IsContextFabricServiceAuthoredLimitation(sentence); got != frameMember {
			t.Errorf("kind sentence with basis %q service-authored=%v, want %v -- a false sentence must stay displaceable", basis, got, frameMember)
		}
		if got := contractsv1.ValidContextFabricFrameRefusalBasis(basis); got != frameMember {
			t.Errorf("ValidContextFabricFrameRefusalBasis(%q)=%v, want %v", basis, got, frameMember)
		}
		// The frame gate maps only frame members; a gate basis spelling the
		// carrier member is drift and surfaces as `unspecified`.
		gate := FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortDiscoverability(basis), DeclaredMemberKind: contractsv1.ContextFabricSubjectTeam}
		wantGate := basis
		if !frameMember {
			wantGate = contractsv1.ContextFabricRefusalBasisUnspecified
		}
		if got := gate.RefusalBasis(); got != wantGate {
			t.Errorf("gate with RefuseBasis %q maps to %q, want %q", basis, got, wantGate)
		}
		if got := refusalLimitation(gate, basis); !frameMember && got != contractsv1.ContextFabricFrameInvariantRefusalLimitation {
			t.Errorf("refusalLimitation composed %q for the carrier basis, want the invariant fallback", got)
		}
	}
	for _, value := range []contractsv1.ContextFabricRefusalBasis{"", "invented", "Member_Kind_Unservable", "continuation_context_unverifiable "} {
		if contractsv1.ValidContextFabricFrameRefusalBasis(value) {
			t.Errorf("ValidContextFabricFrameRefusalBasis(%q) = true for a non-member", value)
		}
	}
	fixed := contractsv1.ContextFabricContinuationContextUnverifiableLimitation
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(fixed) {
		t.Errorf("the fixed continuation sentence is not service-authored, so the engine's displacement rule may drop it")
	}
	if !strings.Contains(fixed, string(contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable)) {
		t.Errorf("the fixed sentence does not name its basis token, so a reader cannot join it to the field and the log line")
	}
	for _, variant := range []string{fixed + " ", strings.TrimSuffix(fixed, "."), strings.ToUpper(fixed), fixed[:len(fixed)/2]} {
		if contractsv1.IsContextFabricServiceAuthoredLimitation(variant) {
			t.Errorf("a variant of the fixed sentence is service-authored: %q", variant)
		}
	}
}

// TestContinuationRefusal_TheProjectionKeepsBothHalvesAtTheCap projects the
// served refusal, and the same refusal at the canonical limitation cap with the
// sentence LAST, through the shared answer projection.
func TestContinuationRefusal_TheProjectionKeepsBothHalvesAtTheCap(t *testing.T) {
	t.Parallel()
	served := servedContinuationRefusal(t)
	atCap := served
	atCap.Limitations = make([]string, 0, contractsv1.ContextFabricLimitationsMaxCount)
	for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount-1; i++ {
		atCap.Limitations = append(atCap.Limitations, "Model caveat number "+strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26)+".")
	}
	atCap.Limitations = append(atCap.Limitations, contractsv1.ContextFabricContinuationContextUnverifiableLimitation)
	atCap.Completeness = ComputeAnswerCompleteness(atCap)
	if err := atCap.Validate(); err != nil {
		t.Fatalf("fixture defect: the at-cap refusal does not validate: %v", err)
	}
	for _, tc := range []struct {
		name   string
		result InvestigationResult
	}{{"served", served}, {"at the canonical cap, sentence last", atCap}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, budget := range []answerprojection.Budget{{}, answerprojection.DefaultBudget} {
				projection := answerprojection.Project(tc.result, budget)
				if err := projection.Validate(); err != nil {
					t.Fatalf("projection does not validate: %v", err)
				}
				if projection.Completeness.RefusalBasis != contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
					t.Errorf("projection completeness.refusal_basis = %q, want the continuation member", projection.Completeness.RefusalBasis)
				}
				kept := false
				for _, limitation := range projection.Limitations {
					if limitation == contractsv1.ContextFabricContinuationContextUnverifiableLimitation {
						kept = true
					}
				}
				if !kept {
					t.Errorf("the projection dropped the fixed sentence (%d limitations projected, omitted=%d)", len(projection.Limitations), projection.ProjectionBudget.LimitationsOmitted)
				}
				t.Logf("projected limitations=%d omitted=%d basis=%q", len(projection.Limitations), projection.ProjectionBudget.LimitationsOmitted, projection.Completeness.RefusalBasis)
			}
		})
	}
}
