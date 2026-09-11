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
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xeipuuv/gojsonschema"

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

func (s *refusalStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, snap SourceWatermarkSnapshot, epoch RebuildEpoch, axisKey string, retrieval ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parentResultID string, semantic SemanticStateWrite) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.mu.Lock()
	s.savedParents = append(s.savedParents, parentResultID)
	s.savedResultID = append(s.savedResultID, result.ResultID)
	s.mu.Unlock()
	return s.staticResultStore.Save(ctx, principal, result, snap, epoch, axisKey, retrieval, prompts, authorities, graphEpoch, parentResultID, semantic)
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
	// legacyCarrier leaves the carrier without a snapshot; stateRead forces
	// its read status.
	legacyCarrier bool
	stateRead     SemanticStateReadStatus
	// carrier replaces the carrier's snapshot.
	carrier func(testing.TB, InvestigationResult) *PersistedSemanticState

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
			name:      "carrier unreadable on every read is the retryable window veto",
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
		// ---- A RECORDED STANDARD IS COMPARED EXACTLY. The contract requires a
		// non-empty family_version, so a blank-looking value is not a legacy
		// carrier: it is a stamp that is not the table in force.
		{
			name: "carrier recorded under a whitespace-only family table version",
			prior: func(p InvestigationResult) InvestigationResult {
				p.AnswerPlan.FamilyVersion = "   "
				return p
			},
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonContextVersionMismatch,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name: "carrier recorded under the table in force padded with whitespace",
			prior: func(p InvestigationResult) InvestigationResult {
				p.AnswerPlan.FamilyVersion = " " + QuestionFamilyTableVersion + "\t"
				return p
			},
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonContextVersionMismatch,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carried frame would be repaired by today's validation",
			priorFamily: QuestionFamilyGroupedCohortStatus, priorGroup: contractsv1.ContextFabricSubjectTeam,
			carrier:     strippedFramedCarrierState,
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonCompositionInvalid,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carried frame refused by today's gate",
			priorFamily: QuestionFamilyDiscoveredCohortRanking,
			carrier: func(t testing.TB, prior InvestigationResult) *PersistedSemanticState {
				frame, gate := unservableDiscoveredFrame(t.(*testing.T))
				return carriedStateFor(t.(*testing.T), prior.AnswerPlan.Family, "", &frame, gate)
			},
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonCompositionInvalid,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:          "legacy carrier with no persisted reading",
			legacyCarrier: true,
			interpreter:   forced,
			wantRefused:   true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonSemanticStateAbsent,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carrier snapshot of an unsupported format",
			stateRead:   SemanticStateReadUnsupportedVersion,
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonContextVersionMismatch,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carrier snapshot malformed",
			stateRead:   SemanticStateReadMalformed,
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonSemanticStateInvalid,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carrier snapshot oversized",
			stateRead:   SemanticStateReadOversized,
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonSemanticStateInvalid,
			wantDecisionEmitted: true, wantServedBasis: contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		},
		{
			name:        "carrier snapshot status unreported by the store",
			stateRead:   SemanticStateReadStatus("unreported-fixture"),
			interpreter: forced,
			wantRefused: true, wantDisposition: ContinuationWithheld, wantReason: ContinuationReasonSemanticStateInvalid,
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
				// REACHABLE: the mutated carrier is one the store would read back.
				if err := ValidateStoredResult(prior); err != nil {
					t.Fatalf("fixture defect: the mutated carrier fails the stored-result validator, so the cell is unreachable: %v", err)
				}
			}
			older := continuationPrior(t, continuationOlderID, base, QuestionFamilyDiscoveredCohortRanking, "")
			base := &staticResultStore{
				results:    map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older},
				graphEpoch: cell.storeEpoch,
			}
			if cell.carrier != nil {
				base.states = map[string]*PersistedSemanticState{prior.ResultID: cell.carrier(t, prior)}
			}
			if !cell.legacyCarrier {
				withCarrierStates(t, base)
			}
			if cell.stateRead != "" {
				base.stateReads = map[string]SemanticStateReadStatus{continuationPriorID: cell.stateRead}
			}
			store := newRefusalStore(base)
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
			store := newRefusalStore(withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: tc.storeEpoch}))
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
	store := newRefusalStore(withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: staleEpoch()}))
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
		// WHICH members are frame refusals is read from the ONE authority
		// that declares it, never restated here. A comparison against a
		// member by name is a second expected-member list: it is correct
		// only for the vocabulary it was written against, and it admits the
		// next member on the wrong side without anything failing at the
		// point the member is added.
		frameMember := contractsv1.ValidContextFabricFrameRefusalBasis(basis)
		sentence := contractsv1.ContextFabricRefusalBasisLimitation(contractsv1.ContextFabricSubjectTeam, basis)
		if got := contractsv1.IsContextFabricRefusalBasisLimitation(sentence); got != frameMember {
			t.Errorf("kind sentence with basis %q recognised=%v, want %v", basis, got, frameMember)
		}
		if got := contractsv1.IsContextFabricServiceAuthoredLimitation(sentence); got != frameMember {
			t.Errorf("kind sentence with basis %q service-authored=%v, want %v -- a false sentence must stay displaceable", basis, got, frameMember)
		}
		// Reading frameMember from the allow-list makes asserting the
		// allow-list against it a tautology, so that line is replaced by an
		// INDEPENDENT authority that must agree with it: a member that is NOT
		// a frame refusal carries its own fixed, service-authored sentence
		// naming it -- because the member-kind sentence would be false of it
		// -- and a frame member carries no such sentence of its own. Two
		// registries, one biconditional; a member filed on the wrong side of
		// either fails here.
		if got := hasOwnNamingSentence(basis); got == frameMember {
			t.Errorf("basis %q: frame member=%v but has its own naming sentence=%v -- a non-frame basis must carry its own sentence, and a frame basis must not", basis, frameMember, got)
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

// TestWindowContinuation_EveryRequestFieldIsDecidedByName enumerates the
// request's fields FROM THE TYPE and holds each to one executed decision
// against D-a's transition ("identical question bytes, one valid window
// receipt, no change to other semantic request inputs or addition of a
// subject, kind, anchor, handle or candidate selection"):
//
//   - key:          the transition's own inputs;
//   - disqualifier: stating it beside the receipt means this is NOT a
//     window-only continuation (never applied, never refused as one);
//   - exempt:       not a semantic reading input -- executed: the continuation
//     still applies with it changed.
//
// A request field added without a decision here fails, so a new way to change
// the reading cannot ride a window receipt unnoticed.
func TestWindowContinuation_EveryRequestFieldIsDecidedByName(t *testing.T) {
	t.Parallel()
	const unrecordedOption = "not recorded at turn one; compared in the stacked semantic-state digest"
	base := validInvestigationRequest().Question
	type decided struct {
		kind   string // key | disqualifier | exempt
		reason string
		mutate func(*InvestigationRequest)
	}
	receipt := BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: "rcpt_other_00000001"}
	decisions := map[string]decided{
		"question": {"key", "identity rule: a byte-different question is not a continuation", func(r *InvestigationRequest) { r.Question = driftQuestion }},
		"prior_window_receipts": {"key", "exactly one receipt: two are never window-only", func(r *InvestigationRequest) {
			r.PriorWindowReceipts = append(r.PriorWindowReceipts, BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: continuationReceiptID})
		}},
		"prior_subject_receipts": {"disqualifier", "a subject selection", func(r *InvestigationRequest) { r.PriorSubjectReceipts = []BoundSubjectReceipt{receipt} }},
		"prior_kind_receipts": {"disqualifier", "a kind selection", func(r *InvestigationRequest) {
			r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: continuationOlderID, ReceiptID: "kindr_other_0000001"}}
		}},
		"prior_anchor_receipts": {"disqualifier", "an anchor selection", func(r *InvestigationRequest) {
			r.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: continuationOlderID, ReceiptID: "ancr_other_00000001"}}
		}},
		"prior_handle_receipts": {"disqualifier", "a handle selection", func(r *InvestigationRequest) {
			r.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: continuationOlderID, ReceiptID: "handr_other_0000001"}}
		}},
		"prior_candidate_receipts": {"disqualifier", "a candidate selection", func(r *InvestigationRequest) {
			r.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: continuationOlderID, ReceiptID: "candr_other_0000001"}}
		}},
		"parent_result_id": {"disqualifier", "a second prior-result reference", func(r *InvestigationRequest) { r.ParentResultID = continuationOlderID }},
		"expected_kinds": {"disqualifier", "a stated kind", func(r *InvestigationRequest) {
			r.ExpectedKinds = []SubjectKind{contractsv1.ContextFabricSubjectProject}
		}},
		"subject_handles": {"disqualifier", "a stated handle", func(r *InvestigationRequest) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}
		}},
		"requested_scope.repository_slugs": {"disqualifier", "a stated repository scope", func(r *InvestigationRequest) { r.RequestedScope.RepositorySlugs = []string{"widget-service"} }},
		"requested_scope.project_ids":      {"disqualifier", "a stated project scope", func(r *InvestigationRequest) { r.RequestedScope.ProjectIDs = []string{"project_ask_dev"} }},
		"requested_scope.team_ids":         {"disqualifier", "a stated team scope", func(r *InvestigationRequest) { r.RequestedScope.TeamIDs = []string{"team_platform"} }},
		"requested_scope.subject_hints": {"disqualifier", "a stated subject hint", func(r *InvestigationRequest) {
			r.RequestedScope.SubjectHints = []contractsv1.ContextFabricSubjectHint{{Kind: contractsv1.ContextFabricSubjectTeam, Label: "platform", Source: "caller"}}
		}},
		"time_context": {"disqualifier", "a non-current axis beside a window receipt is the window veto", func(r *InvestigationRequest) {
			r.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: timePtr(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))}
		}},
		"schema_version": {"exempt", "the request contract's major, validated before the engine; not a reading input", nil},
		"request_id":     {"exempt", "correlation metadata (D-a: not semantic input)", func(r *InvestigationRequest) { r.RequestID = "request_87654321" }},
		// OPTIONS, FIELD BY FIELD. Every consumer sends every option on every
		// request, so an option disqualifies only by DIFFERING from turn one,
		// and turn one recorded exactly one of them: the effective byte budget.
		"options.max_serialized_bytes": {"disqualifier", "the effective byte budget turn one recorded on the carrier's plan differs", func(r *InvestigationRequest) {
			r.Options.MaxSerializedBytes = r.Options.MaxSerializedBytes / 2
		}},
		"options.max_subject_candidates": {"exempt", unrecordedOption, func(r *InvestigationRequest) { r.Options.MaxSubjectCandidates = 3 }},
		"options.max_cohort_members":     {"exempt", unrecordedOption, func(r *InvestigationRequest) { r.Options.MaxCohortMembers = 7 }},
		"options.max_relationship_paths": {"exempt", unrecordedOption, func(r *InvestigationRequest) { r.Options.MaxRelationshipPaths = 7 }},
		"options.max_drivers":            {"exempt", unrecordedOption, func(r *InvestigationRequest) { r.Options.MaxDrivers = 3 }},
		"options.max_evidence_refs":      {"exempt", unrecordedOption, func(r *InvestigationRequest) { r.Options.MaxEvidenceRefs = 7 }},
		"options.allow_clarification":    {"exempt", unrecordedOption, func(r *InvestigationRequest) { r.Options.AllowClarification = false }},
		"options.window_confirmation_mode": {"exempt", unrecordedOption, func(r *InvestigationRequest) {
			r.Options.WindowConfirmationMode = contractsv1.ContextFabricWindowConfirmationNudge
		}},
		"options.include_debug": {"exempt", "debug output only: executed, the continuation decision is unchanged", func(r *InvestigationRequest) { r.Options.IncludeDebug = true }},
		"consumer": {"exempt", "the caller's identity: who asks, not what is asked", func(r *InvestigationRequest) {
			r.Consumer = ConsumerInfo{Name: "test", Version: "1.0.0", Surface: "mcp"}
		}},
		"conversation": {"exempt", "a window-only continuation's conversation is the referenced turn's own exchange; a user change to what is asked arrives as different question bytes, which the identity rule refuses", func(r *InvestigationRequest) {
			r.Conversation = []contractsv1.ContextFabricConversationTurn{{TurnID: "turn_0001", Role: contractsv1.ContextFabricConversationUser, Content: base, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
		}},
	}
	var names []string
	requestType := reflect.TypeOf(InvestigationRequest{})
	for i := 0; i < requestType.NumField(); i++ {
		field := requestType.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Type == reflect.TypeOf(RequestedScope{}) || field.Type == reflect.TypeOf(InvestigationOptions{}) {
			for j := 0; j < field.Type.NumField(); j++ {
				names = append(names, tag+"."+strings.Split(field.Type.Field(j).Tag.Get("json"), ",")[0])
			}
			continue
		}
		names = append(names, tag)
	}
	for _, name := range names {
		d, ok := decisions[name]
		if !ok {
			t.Errorf("request field %q has no decision -- decide it: key, disqualifier or exempt", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if d.mutate == nil {
				t.Logf("%s: %s (%s) -- not mutable at the engine: %s", name, d.kind, d.reason, "the request contract rejects any other major before Investigate")
				return
			}
			request := continuationRequest(base)
			d.mutate(&request)
			if err := request.Validate(); err != nil {
				t.Fatalf("fixture defect: the mutated request fails the request contract, so the cell is unreachable: %v", err)
			}
			prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
			older := continuationPrior(t, continuationOlderID, base, QuestionFamilyDiscoveredCohortRanking, "")
			store := newRefusalStore(&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older}})
			telemetry := &recordingTelemetry{}
			engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, telemetry)
			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			disposition, reason := ContinuationDisposition("<no decision>"), ContinuationDecisionReason("")
			if len(telemetry.windowContinuationDecisions) == 1 {
				disposition, reason = telemetry.windowContinuationDecisions[0].Disposition, telemetry.windowContinuationDecisions[0].Reason
			}
			t.Logf("%-34s %-12s -> err=%v disposition=%s reason=%s refusal_basis=%q (%s)", name, d.kind, err != nil, disposition, reason, result.RefusalBasis, d.reason)
			switch d.kind {
			case "exempt":
				if disposition != ContinuationApplied {
					t.Errorf("an exempt field changed the decision: %s/%s -- it is a reading input after all, decide it", disposition, reason)
				}
			default:
				if disposition == ContinuationApplied {
					t.Errorf("%s %q beside the window receipt still APPLIED the continuation", d.kind, name)
				}
				if result.RefusalBasis == contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
					t.Errorf("%s %q was refused as a window-only continuation; it is not one", d.kind, name)
				}
			}
		})
	}
	for name := range decisions {
		if !slices.Contains(names, name) {
			t.Errorf("decision for %q names no request field", name)
		}
	}
}

// TestContinuationRefusal_TheServedRefusalValidatesAgainstEveryPublishedSchema
// is the runtime half of the enum-site sweep: the refusal the engine actually
// serves -- not a hand-written document -- is validated by a JSON Schema
// validator against the canonical result schema, the answer projection schema
// and the MCP response schema it travels in, and by the stored-result
// validator a later read applies.
func TestContinuationRefusal_TheServedRefusalValidatesAgainstEveryPublishedSchema(t *testing.T) {
	t.Parallel()
	served := servedContinuationRefusal(t)
	if err := ValidateStoredResult(served); err != nil {
		t.Fatalf("the stored-result validator rejects the refusal it would read back: %v", err)
	}
	validate := func(t *testing.T, schemaFile string, doc any) {
		t.Helper()
		encoded, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		loader := gojsonschema.NewSchemaLoader()
		common := filepath.Join(root, "contracts/jsonschema/v1/context_fabric_common.v1.schema.json")
		if err := loader.AddSchemas(gojsonschema.NewReferenceLoader("file://" + common)); err != nil {
			t.Fatalf("add common schema: %v", err)
		}
		compiled, err := loader.Compile(gojsonschema.NewReferenceLoader("file://" + filepath.Join(root, schemaFile)))
		if err != nil {
			t.Fatalf("compile %s: %v", schemaFile, err)
		}
		report, err := compiled.Validate(gojsonschema.NewBytesLoader(encoded))
		if err != nil {
			t.Fatalf("validate %s: %v", schemaFile, err)
		}
		t.Logf("%s: valid=%v (%d bytes)", schemaFile, report.Valid(), len(encoded))
		for _, e := range report.Errors() {
			t.Errorf("%s: %s", schemaFile, e)
		}
	}
	projection := answerprojection.Project(served, answerprojection.DefaultBudget)
	validate(t, "contracts/jsonschema/v1/context_fabric_investigation_result.v1.schema.json", served)
	validate(t, "contracts/jsonschema/v1/context_fabric_answer_projection.v1.schema.json", projection)
	validate(t, "internal/mcp/schemas/mcp_investigate_question_response.v1.schema.json", contractsv1.MCPInvestigateQuestionResponse{
		SchemaVersion:    contractsv1.MCPInvestigateQuestionResponseSchema,
		Structured:       projection,
		FullResult:       &served,
		RenderedMarkdown: contractsv1.MCPRenderedMarkdown{Markdown: "x", Untrusted: true},
		UntrustedContent: contractsv1.MCPUntrustedContent{Untrusted: true, Notice: contractsv1.MCPUntrustedContentNotice, Fields: contractsv1.MCPInvestigateQuestionUntrustedFields},
	})
	if served.RefusalBasis != contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
		t.Fatalf("fixture defect: the served document is not the continuation refusal (%q)", served.RefusalBasis)
	}
}

// TestWindowContinuation_TheRecordedByteBudgetIsComparedAsTheEffectiveBudget
// holds the one answer-shaping option turn one records. First the echo: a real
// turn through the engine stamps its plan with the EFFECTIVE budget (service
// ceiling narrowed by the caller's max_serialized_bytes), never the raw option.
// Then the comparison, under a service ceiling below the caller's option: a
// turn two whose raw option differs but whose effective budget is the same
// still continues; a turn two whose effective budget differs takes the fresh
// path.
func TestWindowContinuation_TheRecordedByteBudgetIsComparedAsTheEffectiveBudget(t *testing.T) {
	t.Parallel()
	const ceiling = 300_000
	base := validInvestigationRequest().Question
	t.Run("a real turn records the effective budget", func(t *testing.T) {
		t.Parallel()
		store := newRefusalStore(&staticResultStore{results: map[string]InvestigationResult{}})
		engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyDiscoveredCohortRanking}, &recordingTelemetry{})
		engine.maxSerializedBytes = ceiling
		request := validInvestigationRequestWithConfirmedWindow()
		request.Question = base
		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil || result.AnswerPlan == nil {
			t.Fatalf("turn one: err=%v plan=%v", err, result.AnswerPlan != nil)
		}
		t.Logf("request max_serialized_bytes=%d service ceiling=%d -> recorded plan budget=%d effective=%d",
			request.Options.MaxSerializedBytes, ceiling, result.AnswerPlan.Budget.MaxSerializedBytes, engine.effectiveResponseBudget(request).MaxSerializedBytes)
		if result.AnswerPlan.Budget.MaxSerializedBytes != engine.effectiveResponseBudget(request).MaxSerializedBytes || result.AnswerPlan.Budget.MaxSerializedBytes != ceiling {
			t.Errorf("the plan recorded %d, want the effective budget %d", result.AnswerPlan.Budget.MaxSerializedBytes, ceiling)
		}
	})
	for _, tc := range []struct {
		name   string
		option int
		want   ContinuationDecisionReason
	}{
		{"the same option", validInvestigationRequest().Options.MaxSerializedBytes, ContinuationReasonNone},
		{"a different raw option narrowed to the same effective budget", 2 * ceiling, ContinuationReasonNone},
		{"an option that narrows the effective budget", ceiling / 2, ContinuationReasonAnswerBudgetChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
			prior.AnswerPlan.Budget.MaxSerializedBytes = ceiling
			store := newRefusalStore(&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}})
			telemetry := &recordingTelemetry{}
			engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, telemetry)
			engine.maxSerializedBytes = ceiling
			request := continuationRequest(base)
			request.Options.MaxSerializedBytes = tc.option
			if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			if len(telemetry.windowContinuationDecisions) != 1 {
				t.Fatalf("decisions = %d", len(telemetry.windowContinuationDecisions))
			}
			d := telemetry.windowContinuationDecisions[0]
			t.Logf("%s: option=%d effective=%d recorded=%d -> %s/%s", tc.name, tc.option, engine.effectiveResponseBudget(request).MaxSerializedBytes, ceiling, d.Disposition, d.Reason)
			if d.Reason != tc.want {
				t.Errorf("reason = %s, want %s", d.Reason, tc.want)
			}
			if (tc.want == ContinuationReasonNone) != (d.Disposition == ContinuationApplied) {
				t.Errorf("disposition = %s for reason %s", d.Disposition, d.Reason)
			}
		})
	}
}

// hasOwnNamingSentence reports whether the service-authored registry holds a
// fixed sentence that names this basis by its token.
func hasOwnNamingSentence(basis contractsv1.ContextFabricRefusalBasis) bool {
	for _, sentence := range contractsv1.ContextFabricServiceAuthoredLimitations() {
		if strings.Contains(sentence, string(basis)) {
			return true
		}
	}
	return false
}
