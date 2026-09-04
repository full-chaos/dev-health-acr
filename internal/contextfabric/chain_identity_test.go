package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"strings"

	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chainIdentityFixture builds the shape this whole ticket exists for: a turn
// that HAS a conversation but has NOTHING to redeem.
//
// The measured chain is the reason. An offer-driven client re-presents only
// what the latest response still offers. Turn 2 answers the kind question, so
// turn 3 is offered nothing, so turn 3 sends no receipt of any kind -- and a
// request with no receipt names no prior result, so every same-conversation
// carry misses with miss_no_reference and the server asks a question it
// already has the answer to. That miss was measured on three separate
// request_ids on the §5b row.
//
// A receipt cannot express "I am continuing this conversation", because a
// receipt is an ACCEPTANCE of a specific offer and there was nothing to
// accept. parent_result_id is the weaker statement the client can always
// make.
func chainIdentityFixture() (InvestigationRequest, *staticResultStore) {
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_chain_0001"
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_prior_chain_0000", "kindr_confirm0001"),
	}

	request := validInvestigationRequest()
	// The ENTIRE linkage. No receipts of any namespace -- deliberately, since
	// a fixture that also carried a receipt could not tell whether the field
	// or the receipt did the work.
	request.ParentResultID = priorResult.ResultID

	return request, &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
}

// TestChainIdentity_ATurnLinkedOnlyByParentResultIDCarriesTheConfirmedKind is
// the red-first test for the field's whole purpose.
//
// RED at the parent for the RIGHT reason: the field exists on the request
// (so this compiles) but nothing reads it, so carryReferencedResultIDs
// returns an empty frontier and the kind carry reports miss_no_reference --
// which is exactly the telemetry value measured on the live §5b chain.
func TestChainIdentity_ATurnLinkedOnlyByParentResultIDCarriesTheConfirmedKind(t *testing.T) {
	t.Parallel()

	request, store := chainIdentityFixture()
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &kindRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
	}}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	hit := false
	var outcomes []KindCarryOutcome
	for _, c := range telemetry.kindCarries {
		outcomes = append(outcomes, c.outcome)
		if c.outcome == KindCarryHit {
			hit = true
		}
	}
	if !hit {
		t.Errorf("kind carry outcomes = %v, want a hit: a turn naming its predecessor by parent_result_id must reach that predecessor's confirmed kind -- miss_no_reference here is the measured defect, not a fixture problem", outcomes)
	}

	carried := false
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind &&
			entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			carried = true
			if entry.PriorResultID == "" {
				t.Error("carried expected_kind entry has an empty prior_result_id: the origin must be disclosed, not merely applied")
			}
		}
	}
	if !carried {
		t.Errorf("ConfirmedStructure = %+v, want a source=carried expected_kind entry", result.ConfirmedStructure)
	}

	sawTeam := false
	for _, seen := range graph.seen {
		if seen != nil && seen.Kind == contractsv1.ContextFabricSubjectTeam {
			sawTeam = true
		}
	}
	if !sawTeam {
		t.Errorf("ResolveSubjects saw ConfirmedExpectedKind %+v, want a non-nil team: the carried kind must reach the pool filter, which is what makes the carry change an answer rather than only a disclosure", graph.seen)
	}
}

// TestChainIdentity_ParentResultIDNeverBindsThePriorSubjects is the mandatory
// guard from the design review, and it is the one that would be easiest to
// violate by accident: seeding the carry walk and seeding subject hints are
// one line apart.
//
// Naming a prior result says "this is the turn I follow". It must NOT say
// "and bind whatever subjects that turn committed". A receipt says the
// second thing; this field must never be allowed to, or a caller could
// silently inherit a subject they never chose.
func TestChainIdentity_ParentResultIDNeverBindsThePriorSubjects(t *testing.T) {
	t.Parallel()

	request, store := chainIdentityFixture()
	// Give the parent a committed subject worth stealing.
	prior := store.results[request.ParentResultID]
	prior.SubjectResolution = SubjectResolution{
		Committed: []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team_platform", Label: "Platform"}},
	}
	store.results[request.ParentResultID] = prior

	var seenHints [][]SubjectHint
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &hintRecordingGraphReader{
		graphReaderStub: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		record:          func(h []SubjectHint) { seenHints = append(seenHints, h) },
	}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if len(seenHints) == 0 {
		t.Fatal("ResolveSubjects was never called, so this test proved nothing about subject hints")
	}
	for _, hints := range seenHints {
		for _, hint := range hints {
			if hint.ID == "team_platform" || hint.Label == "Platform" {
				t.Fatalf("ResolveSubjects received SubjectHint %+v derived from parent_result_id: naming a prior result must seed the CARRIES ONLY, never bind that result's committed subjects into this turn", hint)
			}
		}
	}
}

// hintRecordingGraphReader records the SubjectHints each ResolveSubjects call
// received, so the guard above asserts on the engine's actual input rather
// than on a stub's scripted reply.
type hintRecordingGraphReader struct {
	graphReaderStub
	record func([]SubjectHint)
}

func (g *hintRecordingGraphReader) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.record(request.RequestedScope.SubjectHints)
	return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
}

// TestChainIdentity_ParentResultIDJoinsTheReuseBypass pins a guarantee this
// slice gets for FREE, which is exactly why it needs a test: nothing here
// was written to produce it, so nothing would notice if it stopped holding.
//
// The reuse bypass keys on carryReferencedResultIDs -- the carries' own seed
// population -- rather than on a list of fields. Teaching that function to
// read parent_result_id therefore joined the new field to the reuse bypass
// with no second edit. That is the intended payoff of population-keying, but
// an unasserted emergent property is one refactor away from silently
// disappearing, and the failure would be invisible: a turn that names its
// predecessor would be served a stored answer produced before that
// predecessor existed, with no error anywhere.
func TestChainIdentity_ParentResultIDJoinsTheReuseBypass(t *testing.T) {
	t.Parallel()

	request, store := chainIdentityFixture()
	_, candidate := reusableCandidate()
	reuseGateCalls := 0
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			reuseGateCalls++
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if reuseGateCalls != 0 {
		t.Errorf("ReuseGate was called %d times, want 0: a request naming a prior result may inherit a confirmed axis from it, and every carry runs after the reuse lookup, so it must never consult the cache", reuseGateCalls)
	}
	if result.Reused {
		t.Error("result.Reused = true: a turn naming its predecessor was served an answer produced before that predecessor's axes were confirmed")
	}
	found := false
	for _, reason := range telemetry.answerReuseBypasses {
		if reason == AnswerReuseBypassPriorResultReference {
			found = true
		}
	}
	if !found {
		t.Errorf("bypass reasons = %v, want %q: the bypass must be attributable to the prior-result reference, not merely to have happened", telemetry.answerReuseBypasses, AnswerReuseBypassPriorResultReference)
	}
}

// ancestryRecordingStore records the parentResultID every Save was handed,
// keyed by the result id being saved. Asserting on the Save ARGUMENT rather
// than on a round-tripped Get keeps this a test about the ENGINE (does it
// stamp ancestry on this path?) rather than about a store's persistence.
type ancestryRecordingStore struct {
	*staticResultStore
	saved map[string]string
}

func newAncestryRecordingStore(seed *staticResultStore) *ancestryRecordingStore {
	return &ancestryRecordingStore{staticResultStore: seed, saved: map[string]string{}}
}

func (s *ancestryRecordingStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, snap SourceWatermarkSnapshot, epoch RebuildEpoch, axisKey string, retrieval ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parentResultID string) error {
	s.saved[result.ResultID] = parentResultID
	return s.staticResultStore.Save(ctx, principal, result, snap, epoch, axisKey, retrieval, prompts, authorities, graphEpoch, parentResultID)
}

// TestChainIdentity_EverySaveBearingReturnRecordsAncestry is the design
// review's durability requirement, and it is the part of this ticket most
// likely to be got wrong by building only what the happy path needs.
//
// A request-only parent pointer is not durable chain identity. Ancestry is
// only walkable if EVERY turn recorded its parent -- and most engine paths
// return BEFORE any carry runs. Those are exactly the paths a reader forgets,
// because no carry happened on them and nothing about a carry is on screen.
//
// The failure they cause is not local. A chain whose MIDDLE turn recorded no
// parent has a hole in it, and every turn after the hole is cut off from
// everything before it -- so a clarification loop that hits one veto in the
// middle loses its whole history, which is precisely the situation where the
// history was worth keeping.
//
// ONE ARM PER SAVE-BEARING RETURN. The five arms below are the complete
// population, and the population came from the COMPILER, not from a grep:
// adding a required positional parameter to InvestigationResultStore.Save
// turned "did I find every save site?" into a build error. That also
// corrected the count -- structureSupersessionVetoResult delegates to
// structureVetoResult and shares its Save, so there are five sites, not the
// six a name-keyed sweep suggests.
//
// Three arms land on the same no_match status, so status is NOT what tells
// them apart -- each arm's independence is proven by the stamp-drop mutation
// matrix (drop ancestry at ONE Save site, exactly that arm goes red), which
// is recorded in the PR body rather than assumed here.
func TestChainIdentity_EverySaveBearingReturnRecordsAncestry(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	for _, tc := range []struct {
		// name identifies the Save-bearing return this arm exercises.
		name string
		site string
		// mutate shapes the request into one that reaches that return.
		mutate func(*InvestigationRequest)
		// subjectless routes through the subjectless terminal instead of a
		// committed-subject answer.
		subjectless bool
		// gated uses the interpretation that leaves this turn's window an
		// inferred class default, which is what the confirmation gate fires on.
		gated bool
		// wantStatus is a REACHABILITY GUARD, not the assertion. Without it an
		// arm whose fixture stopped reaching its intended path would silently
		// re-test the decisive path and still pass.
		wantStatus contractsv1.ContextFabricInvestigationStatus
	}{
		{
			name: "decisive answer", site: "engine.go Investigate",
			mutate: func(*InvestigationRequest) {}, wantStatus: InvestigationComplete,
		},
		{
			name: "structure veto", site: "structure.go structureVetoResult",
			mutate: func(r *InvestigationRequest) {
				r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_absent_00001", ReceiptID: "kindr_absent00001"}}
			},
			wantStatus: InvestigationNoMatch,
		},
		{
			name: "window veto", site: "window.go windowVetoResult",
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_absent_00001", ReceiptID: "winr_absent000001"}}
			},
			wantStatus: InvestigationNoMatch,
		},
		{
			name: "subjectless terminal", site: "unresolved.go terminalResult",
			mutate: func(*InvestigationRequest) {}, subjectless: true, wantStatus: InvestigationNoMatch,
		},
		{
			name: "window confirmation gate", site: "window.go windowConfirmationRequiredResult",
			mutate: func(*InvestigationRequest) {}, gated: true, wantStatus: InvestigationClarificationRequired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request, seed := chainIdentityFixture()
			tc.mutate(&request)
			store := newAncestryRecordingStore(seed)

			resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
			if tc.subjectless {
				resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
			}
			interpretation := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}
			if tc.gated {
				interpretation = bootstrapInterpretation()
			}
			fresh := validInvestigationResult()

			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: resolution},
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return fresh, nil
				}),
				Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					return interpretation, nil
				}),
				Results: store,
			})

			result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q -- this arm no longer reaches %s, so it proves nothing about that return", result.Status, tc.wantStatus, tc.site)
			}

			got, saved := store.saved[result.ResultID]
			if !saved {
				t.Fatalf("%s saved no row for %q, so no ancestry could be recorded", tc.site, result.ResultID)
			}
			if got != request.ParentResultID {
				t.Fatalf("%s recorded parent %q, want %q: a turn that returned early still has a real predecessor, and a later turn must be able to walk back THROUGH it", tc.site, got, request.ParentResultID)
			}
		})
	}
}

func ancestryTestEngine(t *testing.T, store InvestigationResultStore) *Engine {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
	return mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
	})
}

// orgScopedResultStore is staticResultStore with the org check the real
// stores enforce and the shared test double does NOT.
//
// This matters more than a test-double detail. staticResultStore.Get ignores
// its principal entirely, so no test built on it can distinguish "the carry
// respects org isolation" from "the carry never checks". That was tolerable
// while every referenced result id arrived inside a receipt the caller had
// been shown; parent_result_id makes a result id a caller-supplied bearer
// reference, and org isolation stops being incidental. Both real
// implementations scope Get by org (pginvestigation's SELECT carries
// `AND org_id = $2`; memoryinvestigation compares stored.orgID), so this
// double matches production rather than inventing a stricter rule.
type orgScopedResultStore struct {
	*staticResultStore
	orgID string
}

func (s *orgScopedResultStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	if principal.OrgID != s.orgID {
		return StoredInvestigationResult{}, errors.New("investigation result not found")
	}
	return s.staticResultStore.Get(ctx, principal, resultID)
}

// TestChainIdentity_ContainmentGuardsApplyToTheCallerSuppliedRoot pins the
// guards the bearer model rests on, applied to the NEW seed path.
//
// parent_result_id is a bearer reference within an organization: a caller who
// names a result id inherits that result's confirmed axes. What keeps that
// acceptable is not that the id is secret -- it is that every hop re-checks
// what it is about to trust. The walk already applied these guards to
// receipt-borne ids; these arms prove the caller-supplied root does not get a
// weaker path to the same data, which is exactly the regression a new seed
// site invites.
//
// Both arms assert the carry MISSES and, more importantly, that it misses
// with the RIGHT reason -- a carry that failed for an unrelated reason would
// pass a bare "no hit" assertion while proving nothing about containment.
func TestChainIdentity_ContainmentGuardsApplyToTheCallerSuppliedRoot(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	build := func(t *testing.T, store InvestigationResultStore, telemetry *recordingTelemetry) *Engine {
		t.Helper()
		fresh := validInvestigationResult()
		return mustReuseTestEngine(t, EngineDependencies{
			Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
			Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return CanonicalFactBundle{}, nil
			}),
			Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				return fresh, nil
			}),
			Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
			}),
			Results:   store,
			Telemetry: telemetry,
		})
	}

	assertMiss := func(t *testing.T, telemetry *recordingTelemetry, result InvestigationResult, want KindCarryOutcome, why string) {
		t.Helper()
		var got []KindCarryOutcome
		for _, c := range telemetry.kindCarries {
			got = append(got, c.outcome)
			if c.outcome == KindCarryHit {
				t.Fatalf("kind carry HIT: %s", why)
			}
		}
		found := false
		for _, o := range got {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Errorf("kind carry outcomes = %v, want %q -- missing for the wrong reason proves nothing about containment", got, want)
		}
		for _, entry := range result.ConfirmedStructure {
			if entry.Source == contractsv1.ContextFabricStructureSourceCarried {
				t.Errorf("ConfirmedStructure carries %#v: %s", entry, why)
			}
		}
	}

	t.Run("a parent in another org is not reachable", func(t *testing.T) {
		t.Parallel()
		request, seed := chainIdentityFixture()
		store := &orgScopedResultStore{staticResultStore: seed, orgID: "org_somebody_else"}
		telemetry := &recordingTelemetry{}

		result, err := build(t, store, telemetry).Investigate(context.Background(), reusePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		assertMiss(t, telemetry, result, KindCarryMissUnloadable,
			"naming a result id belonging to another organization must never inherit that result's confirmed axes")
	})

	t.Run("a parent from a retired graph epoch is not trusted", func(t *testing.T) {
		t.Parallel()
		request, seed := chainIdentityFixture()
		stale := int64(41)
		seed.graphEpoch = &stale // this investigation binds epoch 0
		telemetry := &recordingTelemetry{}

		result, err := build(t, seed, telemetry).Investigate(context.Background(), reusePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		assertMiss(t, telemetry, result, KindCarryMissStaleGraphEpoch,
			"a parent generated under a different graph epoch describes a graph this turn is not reading, and must not be carried from")
	})
}

// TestChainIdentity_TheBypassReasonReachesTheRealSink closes the gap the
// prior slice's codex round identified and team-lead carried into this one.
//
// That slice verified the SINK in isolation (call RecordAnswerReuseBypass,
// assert the log line) and the ENGINE against a recording double. Neither
// exercised the wiring BETWEEN them, so the engine could have been handing
// the real sink nothing at all and both tests would still have passed --
// the reviewer said as much, and confirmed the wiring only by reading source.
//
// This drives a real Engine, with a real SlogEngineTelemetry, through a real
// bypass, and reads the bytes that came out the other end. That is the only
// version of this test that can fail if the wiring breaks.
func TestChainIdentity_TheBypassReasonReachesTheRealSink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// Info level: the bypass line is logged at Info, so a Warn-only handler
	// would drop it and this test would pass by never seeing anything.
	sink := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	request, seed := chainIdentityFixture()
	_, candidate := reusableCandidate()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()
	gateCalls := 0

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   seed,
		Telemetry: sink,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			gateCalls++
			return candidate, true, nil
		}),
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// Reachability guard: if the request stopped bypassing, there would be no
	// bypass line to find and the assertions below would be vacuous.
	if gateCalls != 0 {
		t.Fatalf("ReuseGate was called %d times, want 0 -- this request no longer bypasses, so it cannot exercise the bypass telemetry", gateCalls)
	}

	logged := buf.String()
	if !strings.Contains(logged, "context fabric answer reuse bypass") {
		t.Fatalf("no bypass line reached the real sink; engine output was:\n%s", logged)
	}
	if !strings.Contains(logged, `"reason":"`+string(AnswerReuseBypassPriorResultReference)+`"`) {
		t.Errorf("bypass line does not carry reason=%q -- the engine reached the sink but not with the arm that actually fired:\n%s", AnswerReuseBypassPriorResultReference, logged)
	}
}

// TestChainIdentity_CarryTelemetryAttributesHowTheChainWasLinked pins the
// measurement that makes this ticket's own rig result interpretable.
//
// Without a seed-source label, a rig run showing the loop closing cannot say
// WHY. A carry hit rate that improves after this ships is consistent with two
// completely different stories: the chain-identity field linked turns that
// previously named nothing, or those turns were carrying by receipt all along
// and something else changed. The first is this ticket working; the second is
// this ticket being irrelevant. One label separates them, and no amount of
// re-reading the outcome counts can.
//
// The label is derived from the REQUEST, never from whether the carry
// succeeded, so the measure stays independent of the outcome it explains.
// Both arms therefore assert the label on a turn that HITS and the label is
// checked against the linkage, not against the result.
func TestChainIdentity_CarryTelemetryAttributesHowTheChainWasLinked(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	run := func(t *testing.T, mutate func(*InvestigationRequest, *staticResultStore)) *recordingTelemetry {
		t.Helper()
		request, seed := chainIdentityFixture()
		mutate(&request, seed)
		telemetry := &recordingTelemetry{}
		fresh := validInvestigationResult()
		engine := mustReuseTestEngine(t, EngineDependencies{
			Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
			Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return CanonicalFactBundle{}, nil
			}),
			Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				return fresh, nil
			}),
			Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
			}),
			Results:   seed,
			Telemetry: telemetry,
		})
		if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return telemetry
	}

	assertSeed := func(t *testing.T, telemetry *recordingTelemetry, want CarrySeedSource) {
		t.Helper()
		if len(telemetry.kindCarries) == 0 {
			t.Fatal("no kind-carry telemetry was recorded, so this arm proves nothing about attribution")
		}
		for _, c := range telemetry.kindCarries {
			if c.seedSource != want {
				t.Errorf("kind carry recorded seed_source %q, want %q", c.seedSource, want)
			}
		}
	}

	t.Run("linked only by the field", func(t *testing.T) {
		t.Parallel()
		// chainIdentityFixture links by parent_result_id and nothing else --
		// the population that could not carry at all before this ticket.
		assertSeed(t, run(t, func(*InvestigationRequest, *staticResultStore) {}), CarrySeedParentField)
	})

	t.Run("linked only by a receipt", func(t *testing.T) {
		t.Parallel()
		assertSeed(t, run(t, func(r *InvestigationRequest, seed *staticResultStore) {
			// A window receipt that genuinely redeems: the prior result must
			// carry the matching WindowOption, or the request vetoes before
			// any carry runs and this arm silently measures nothing. The
			// guard in assertSeed caught exactly that on the first attempt.
			start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			prior := seed.results["result_prior_chain_0001"]
			prior.WindowClarification = &WindowClarification{Options: []WindowOption{
				{ReceiptID: "winr_confirm0001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &start, End: &end},
			}}
			seed.results["result_prior_chain_0001"] = prior

			r.ParentResultID = ""
			r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_chain_0001", ReceiptID: "winr_confirm0001"}}
		}), CarrySeedReceipt)
	})

	t.Run("linked by neither", func(t *testing.T) {
		t.Parallel()
		// The miss_no_reference population -- the baseline the field must
		// shrink, and the one a rig table is read against.
		assertSeed(t, run(t, func(r *InvestigationRequest, _ *staticResultStore) {
			r.ParentResultID = ""
		}), CarrySeedNone)
	})
}

// countingResultStore counts Get calls so the memo claim ("the gate costs no
// extra store round-trip") is measured rather than asserted.
type countingResultStore struct {
	*staticResultStore
	gets int
}

func (s *countingResultStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	s.gets++
	return s.staticResultStore.Get(ctx, principal, resultID)
}

// TestChainIdentity_QuestionDriftGatesTheCallerSuppliedRootOnly pins the
// two-tier rule, and the second arm is the more important of the two.
//
// A RECEIPT is an ACCEPTANCE of a specific offer the server showed this
// caller. A caller who redeems one and then asks a genuinely different
// follow-up -- "what about last quarter?" against the same chain -- is doing
// something that works today, and gating receipts on question equality would
// break it. parent_result_id is the weaker claim ("this is the turn I
// follow") and a bearer reference: any result id in the caller's own org is
// nameable, with no offer behind it that the server chose to show. Question
// equality is the cheapest available evidence that the named result is part
// of THIS conversation rather than an unrelated investigation whose confirmed
// axes would be inherited by accident.
//
// So the asymmetry is deliberate. The control arm exists to make sure the
// gate never grows to cover receipts by accident -- that regression would be
// invisible in the drift arm alone, which would keep passing.
func TestChainIdentity_QuestionDriftGatesTheCallerSuppliedRootOnly(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}

	run := func(t *testing.T, mutate func(*InvestigationRequest, *staticResultStore)) (*recordingTelemetry, InvestigationResult, *countingResultStore) {
		t.Helper()
		request, seed := chainIdentityFixture()
		mutate(&request, seed)
		store := &countingResultStore{staticResultStore: seed}
		telemetry := &recordingTelemetry{}
		fresh := validInvestigationResult()
		engine := mustReuseTestEngine(t, EngineDependencies{
			Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
			Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return CanonicalFactBundle{}, nil
			}),
			Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				return fresh, nil
			}),
			Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
			}),
			Results:   store,
			Telemetry: telemetry,
		})
		result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return telemetry, result, store
	}

	t.Run("a parent answering a different question is dropped", func(t *testing.T) {
		t.Parallel()
		telemetry, result, _ := run(t, func(r *InvestigationRequest, seed *staticResultStore) {
			prior := seed.results[r.ParentResultID]
			prior.Question = "how many pull requests did the platform team merge last quarter?"
			seed.results[r.ParentResultID] = prior
		})

		drift := false
		var got []KindCarryOutcome
		for _, c := range telemetry.kindCarries {
			got = append(got, c.outcome)
			if c.outcome == KindCarryMissQuestionDrift {
				drift = true
			}
			if c.outcome == KindCarryHit {
				t.Fatal("kind carry HIT: an unrelated investigation's confirmed kind was inherited by a turn that merely named its id")
			}
		}
		if !drift {
			t.Errorf("kind carry outcomes = %v, want %q -- missing for a vaguer reason would hide WHY the carry was refused", got, KindCarryMissQuestionDrift)
		}
		for _, entry := range result.ConfirmedStructure {
			if entry.Source == contractsv1.ContextFabricStructureSourceCarried {
				t.Errorf("ConfirmedStructure carries %#v from a drifted parent", entry)
			}
		}
	})

	t.Run("a receipt against a different question still carries", func(t *testing.T) {
		t.Parallel()
		// THE BEHAVIOUR-PRESERVING CONTROL. Same drift, linked by a redeemed
		// receipt instead of the field: this must keep working exactly as it
		// does today.
		telemetry, _, _ := run(t, func(r *InvestigationRequest, seed *staticResultStore) {
			start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			prior := seed.results[r.ParentResultID]
			prior.Question = "how many pull requests did the platform team merge last quarter?"
			prior.WindowClarification = &WindowClarification{Options: []WindowOption{
				{ReceiptID: "winr_confirm0001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &start, End: &end},
			}}
			seed.results[r.ParentResultID] = prior

			r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: r.ParentResultID, ReceiptID: "winr_confirm0001"}}
			r.ParentResultID = ""
		})

		hit := false
		var got []KindCarryOutcome
		for _, c := range telemetry.kindCarries {
			got = append(got, c.outcome)
			if c.outcome == KindCarryHit {
				hit = true
			}
			if c.outcome == KindCarryMissQuestionDrift {
				t.Fatal("the drift gate fired on a RECEIPT-linked chain: redeeming an offer and then asking a different follow-up is legitimate today and must keep working")
			}
		}
		if !hit {
			t.Errorf("kind carry outcomes = %v, want a hit -- a receipt-linked chain must be unaffected by the drift gate", got)
		}
	})

	t.Run("the gate adds no store read", func(t *testing.T) {
		t.Parallel()
		// The memo claim, MEASURED rather than asserted.
		//
		// The number below is 2, not 1, and the difference matters. Two reads
		// of the parent happen on this path: one the drift gate and the carry
		// walk SHARE through the per-request carry memo, and one that is
		// pre-existing and unrelated to either. I measured the baseline by
		// short-circuiting the gate and re-running this exact fixture: two
		// reads with the gate, two without. The gate contributes ZERO.
		//
		// So this pins "the gate is free", which is the property the ruling
		// asked for. Pinning an absolute 1 would have pinned an unrelated
		// read out of existence and failed for a reason that has nothing to
		// do with this code -- which is exactly what it did on first writing.
		// If the gate ever starts loading outside the memo, this goes to 3.
		_, _, store := run(t, func(*InvestigationRequest, *staticResultStore) {})
		if store.gets != 2 {
			t.Errorf("store.Get called %d times, want 2 (one memo-shared read plus one pre-existing): a change here means the drift gate stopped reading through the per-request carry memo", store.gets)
		}
	})
}

// TestChainIdentity_AWalkContinuesThroughATurnThatCarriedNothing is the test
// that should have existed from the start, and its absence is why persisted
// ancestry could be written by every Save and read by nothing while a five-arm
// durability suite stayed green.
//
// Those arms assert what the Save DOUBLE received. That proves stamping. It
// does not prove TRAVERSAL, and traversal is the whole point: ancestry exists
// so a later turn can walk back THROUGH a turn that carried nothing. Writing a
// parent that no walk reads is a half-mechanism that looks complete from the
// write side.
//
// The chain here is the shape the design review described:
//
//	turn 1  confirms expected_kind=team
//	turn 2  follows turn 1, is VETOED, carries nothing, so it has NO
//	        ConfirmedStructure entry pointing anywhere -- its only link back
//	        is the durable parent the store recorded
//	turn 3  follows turn 2
//
// Turn 3 can only reach turn 1's confirmed kind by reading turn 2's stored
// ancestry. Expanding the frontier from ConfirmedStructure alone stops dead at
// turn 2, which is exactly the hole this ticket claims to close.
func TestChainIdentity_AWalkContinuesThroughATurnThatCarriedNothing(t *testing.T) {
	t.Parallel()

	turn1 := validInvestigationResult()
	turn1.ResultID = "result_turn_one_00001"
	turn1.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero_0001", "kindr_confirm0001"),
	}

	// Turn 2 carried NOTHING: no ConfirmedStructure at all, so the walk has no
	// wire-visible edge to follow out of it. Its link to turn 1 exists only as
	// store metadata -- which is precisely the condition being tested.
	turn2 := validInvestigationResult()
	turn2.ResultID = "result_turn_two_00001"
	turn2.ConfirmedStructure = nil

	store := &ancestryLinkedStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{
			turn1.ResultID: turn1,
			turn2.ResultID: turn2,
		}},
		parents: map[string]string{turn2.ResultID: turn1.ResultID},
	}

	request := validInvestigationRequest()
	request.ParentResultID = turn2.ResultID

	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &kindRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
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
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// Guard: turn 2 must genuinely be reachable and genuinely carry nothing,
	// or this test is measuring a different chain than it describes.
	if len(store.gotIDs) == 0 {
		t.Fatal("no prior result was loaded at all, so this test proves nothing about traversal")
	}

	var got []KindCarryOutcome
	hit := false
	for _, c := range telemetry.kindCarries {
		got = append(got, c.outcome)
		if c.outcome == KindCarryHit {
			hit = true
		}
	}
	if !hit {
		t.Errorf("kind carry outcomes = %v, want a hit: turn 2 carried nothing, so the ONLY route from turn 3 to turn 1's confirmed kind is turn 2's persisted ancestry -- a walk that expands only from ConfirmedStructure stops dead at turn 2, which is the hole this ticket claims to close", got)
	}

	carried := false
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind &&
			entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			carried = true
		}
	}
	if !carried {
		t.Errorf("ConfirmedStructure = %+v, want a source=carried expected_kind entry inherited from turn 1", result.ConfirmedStructure)
	}
}

// ancestryLinkedStore returns durable parents on Get, which staticResultStore
// does not model. Without it no test could exercise traversal at all -- the
// shared double reports every stored result as having no ancestry, so a walk
// that ignores ancestry entirely is indistinguishable from one that honours it.
type ancestryLinkedStore struct {
	*staticResultStore
	parents map[string]string
}

func (s *ancestryLinkedStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	stored, err := s.staticResultStore.Get(ctx, principal, resultID)
	if err != nil {
		return stored, err
	}
	stored.ParentResultID = s.parents[resultID]
	return stored, nil
}

// TestChainIdentity_AValidatedReceiptIsRecordedAsAncestryOnATerminal covers
// the receipt FALLBACK on a path that returns early.
//
// parent_result_id is the ancestry root when supplied; receipt-derived roots
// are the fallback, so that existing clients which link by redeeming an offer
// still build walkable history instead of ancestry existing only for callers
// who adopted the new field. That fallback was being dropped on three
// post-validation terminals, which passed nil for the validated receipts even
// though the caller had them in scope -- so precisely the clients the fallback
// exists for got no ancestry on any turn that ended in a veto or a terminal.
//
// The reasoning that produced the bug is worth recording: "these paths run
// before receipt validation, so nil is correct" is TRUE for the structure
// veto and FALSE for these three. A single sentence covering five call sites
// was right about two of them.
func TestChainIdentity_AValidatedReceiptIsRecordedAsAncestryOnATerminal(t *testing.T) {
	t.Parallel()

	prior := validInvestigationResult()
	prior.ResultID = "result_receipt_parent_1"
	// The receipt must match a CANDIDATE in the prior result, not merely a
	// committed subject: an unmatched receipt classifies skipped_no_match and
	// is deliberately never validated, so it must not seed ancestry either.
	// My first fixture set only Committed and the test failed -- correctly,
	// because the receipt never validated. That is the behaviour working, not
	// a second bug.
	ops := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_ops", Label: "Ops"}
	prior.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_abc12345678", Subject: ops, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{ops},
	}
	seed := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	store := newAncestryRecordingStore(seed)

	request := validInvestigationRequest()
	// Linked by a prior-subject receipt ONLY -- no parent_result_id, which is
	// exactly the existing-client shape the fallback serves.
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_abc12345678"}}

	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		// Empty committed set routes through the subjectless terminal, one of
		// the three paths that was dropping the fallback.
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// Reachability guard: this must genuinely be the subjectless terminal, or
	// the arm is re-testing the decisive path.
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want %q -- this fixture no longer reaches the subjectless terminal", result.Status, InvestigationNoMatch)
	}
	got, saved := store.saved[result.ResultID]
	if !saved {
		t.Fatalf("the terminal saved no row for %q", result.ResultID)
	}
	if got != prior.ResultID {
		t.Fatalf("terminal recorded parent %q, want %q: a client linking by a validated receipt must build walkable history on early-return paths too, not only on the decisive one", got, prior.ResultID)
	}
}
