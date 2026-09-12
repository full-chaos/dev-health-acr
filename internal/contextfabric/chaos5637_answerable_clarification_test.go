package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// withheldPoolResolution is the resolution graphrank emits for a candidate
// pool emptied by the vector-only exclusion: zero candidates, and the prompt
// that says so. It is the ONE producer of an offer-less clarification
// (chaos5637_answerable_clarification.go), so every pin in this file starts
// from it rather than from a hand-built approximation.
func withheldPoolResolution() SubjectResolution {
	return SubjectResolution{
		Candidates:          []SubjectCandidate{},
		Committed:           []SubjectRef{},
		ClarificationPrompt: OfferPoolEmptiedClarificationPrompt,
	}
}

// A Missing row is not an offer. StructureNeedsWouldDisclose says yes to
// material carrying a Missing member and no options, by the standing
// zero-candidates ruling -- so a predicate that reused it would admit
// exactly the turn this invariant forbids.
func TestAMissingRowWithNoOptionsIsNotARedeemableOffer(t *testing.T) {
	t.Parallel()
	material := StructureOfferMaterial{
		Missing: []contractsv1.ContextFabricStructureNeedKind{
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
		},
	}
	if !StructureNeedsWouldDisclose(material) {
		t.Fatal("fixture defect: this material is meant to be disclosable, which is the whole trap")
	}
	if offerMaterialRedeemable(material) {
		t.Fatal("a Missing row with no options counted as redeemable; a caller has nothing to send back for it")
	}
}

// Each channel ALONE is enough. Asserted per channel rather than in one
// composite case: a predicate that had dropped a single channel would still
// pass a test that set them all.
func TestAnyOneOfferChannelMakesAClarificationAnswerable(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		resolution SubjectResolution
		material   StructureOfferMaterial
		window     *contractsv1.ContextFabricWindowClarification
	}{
		{
			name:       "a subject candidate",
			resolution: SubjectResolution{Candidates: []SubjectCandidate{{}}, Committed: []SubjectRef{}},
		},
		{
			name:       "a kind option",
			resolution: withheldPoolResolution(),
			material:   StructureOfferMaterial{KindOptions: []contractsv1.ContextFabricKindOption{{}}},
		},
		{
			name:       "an anchor option",
			resolution: withheldPoolResolution(),
			material:   StructureOfferMaterial{AnchorOptions: []contractsv1.ContextFabricAnchorOption{{}}},
		},
		{
			name:       "a handle option",
			resolution: withheldPoolResolution(),
			material:   StructureOfferMaterial{HandleOptions: []contractsv1.ContextFabricHandleOption{{}}},
		},
		{
			name:       "a candidate option",
			resolution: withheldPoolResolution(),
			material:   StructureOfferMaterial{CandidateOptions: []contractsv1.ContextFabricCandidateOption{{}}},
		},
		{
			name:       "a window option",
			resolution: withheldPoolResolution(),
			window:     &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if !clarificationOffersRedeemable(testCase.resolution, testCase.material, testCase.window) {
				t.Fatal("this channel alone did not count as an offer; a caller reading it has a move and the predicate says they do not")
			}
		})
	}
	t.Run("and nothing at all does not", func(t *testing.T) {
		t.Parallel()
		if clarificationOffersRedeemable(withheldPoolResolution(), StructureOfferMaterial{}, nil) {
			t.Fatal("an empty turn counted as answerable -- the control; without it every arm above passes vacuously")
		}
	})
	t.Run("nor does an empty window options list", func(t *testing.T) {
		t.Parallel()
		empty := &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{}}
		if clarificationOffersRedeemable(withheldPoolResolution(), StructureOfferMaterial{}, empty) {
			t.Fatal("a non-nil window clarification with zero options counted as an offer")
		}
	})
}

// THE ASSERTION, on the shape of document it actually guards.
func TestTheAnswerabilityAssertionFiresOnlyOnAnUnanswerableClarification(t *testing.T) {
	t.Parallel()
	offered := &contractsv1.ContextFabricStructureNeeds{
		WindowOptions: []contractsv1.ContextFabricWindowOption{{}},
	}
	for _, testCase := range []struct {
		name    string
		result  InvestigationResult
		wantErr bool
	}{
		{
			name: "a clarification with no offer anywhere",
			result: InvestigationResult{
				Status:            InvestigationClarificationRequired,
				SubjectResolution: withheldPoolResolution(),
			},
			wantErr: true,
		},
		{
			name: "a clarification that offers a window",
			result: InvestigationResult{
				Status:            InvestigationClarificationRequired,
				SubjectResolution: withheldPoolResolution(),
				StructureNeeds:    offered,
			},
		},
		{
			// THE CONTROL on the status test. A terminal owes no offer,
			// and an assertion that ignored the status would turn every
			// honest no_match into a stage error.
			name: "a no_match with no offer",
			result: InvestigationResult{
				Status:            InvestigationNoMatch,
				SubjectResolution: withheldPoolResolution(),
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := assertAnswerableClarification(testCase.result)
			if testCase.wantErr && !errors.Is(err, ErrUnanswerableClarification) {
				t.Fatalf("err = %v, want %v", err, ErrUnanswerableClarification)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

// THE WHOLE CLAIM, driven through the production entry point, because that
// is the only place the status and the offers are decided by the same pass.
//
// The unit pins above prove the predicate and the assertion each answer
// correctly. Neither proves the ENGINE consults them: the yardstick defect
// was precisely that the resolution already carried the right state and the
// terminal was chosen without reference to it. This drives Investigate with
// a withheld pool, an already-confirmed window (so no window offer is
// composed to rescue the turn) and empty offer material, and requires the
// served document to be a terminal.
//
// Red before this ticket: status is clarification_required, with six empty
// offer channels -- the shape measured 76 times on the 2026-09-12 yardstick.
func TestAWithheldPoolWithNothingToOfferTerminatesInOneTurn(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{
		resolution: withheldPoolResolution(),
		context:    emptyGraphContext(),
	}
	engine := buildWindowGateEngine(t,
		&countingInterpreter{interpretation: bootstrapInterpretation()},
		graph,
		newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(),
		validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if graph.resolveCalls == 0 {
		t.Fatal("the fixture never reached ResolveSubjects, so it proves nothing about the withheld pool")
	}
	if result.Status == InvestigationClarificationRequired {
		t.Fatalf("the turn asked for clarification while offering nothing: candidates=%d structure_needs=%v window=%v",
			len(result.SubjectResolution.Candidates), result.StructureNeeds, result.WindowClarification)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if resultOffersRedeemable(result) {
		t.Fatal("fixture defect: this turn DID carry an offer, so it never exercised the unanswerable case")
	}
	// The prompt survives the downgrade. It is what
	// subjectlessTerminalReason reads to report the withheld pool apart
	// from an empty one, and clearing it would trade a wire defect for a
	// telemetry one.
	if result.SubjectResolution.ClarificationPrompt == "" {
		t.Fatal("the downgrade cleared the prompt; the withheld pool is no longer distinguishable from an empty graph")
	}
	if got := subjectlessTerminalReason(FrameGate{}, result.SubjectResolution, 0); got != "offer_pool_emptied_by_exclusion" {
		t.Fatalf("terminal reason = %q, want %q", got, "offer_pool_emptied_by_exclusion")
	}
	// And it never reaches the answer sentence: a caller reading the
	// deterministic answer of a terminal must not be handed an ask.
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitationUnproven {
			t.Fatal("the withheld pool reported the unproven-absence limitation; retrieval found candidates and withheld them")
		}
	}
}

// THE CONTROL for the pin above, and the one that keeps this ticket from
// having simply deleted a feature: a withheld pool that DOES have something
// to offer still clarifies. Same fixture, same resolution -- the only
// difference is offer material the engine can put on the wire.
func TestAWithheldPoolThatCanOfferSomethingStillClarifies(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{
		resolution: withheldPoolResolution(),
		context:    emptyGraphContext(),
		material: StructureOfferMaterial{
			Missing: []contractsv1.ContextFabricStructureNeedKind{
				contractsv1.ContextFabricStructureNeedExpectedKind,
			},
			KindOptions: []contractsv1.ContextFabricKindOption{
				{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			},
		},
	}
	engine := buildWindowGateEngine(t,
		&countingInterpreter{interpretation: bootstrapInterpretation()},
		graph,
		newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(),
		validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q -- a withheld pool with a real offer must still ask", result.Status, InvestigationClarificationRequired)
	}
	if !resultOffersRedeemable(result) {
		t.Fatal("the clarification carried no redeemable offer, which is the state this ticket forbids")
	}
}

// THE INPUT DOMAIN OF THE GUARD, executed in one pass and printed as a table.
//
// resultOffersRedeemable is the predicate the assertion reads, and its input
// domain is six channels x {absent, empty container, populated} plus the
// container that holds five of them (StructureNeeds) being nil. Enumerated
// rather than sampled: a predicate that had dropped one channel, or that
// counted an empty slice as an offer, passes any test that sets the channels
// it happens to check.
func TestTheRedeemableOfferDomain(t *testing.T) {
	t.Parallel()
	populatedNeeds := func(mutate func(*contractsv1.ContextFabricStructureNeeds)) *contractsv1.ContextFabricStructureNeeds {
		needs := &contractsv1.ContextFabricStructureNeeds{}
		mutate(needs)
		return needs
	}
	for _, testCase := range []struct {
		cell   string
		result InvestigationResult
		want   bool
	}{
		{cell: "everything absent", result: InvestigationResult{}, want: false},
		{cell: "structure_needs nil", result: InvestigationResult{StructureNeeds: nil}, want: false},
		{cell: "structure_needs present, every list absent",
			result: InvestigationResult{StructureNeeds: &contractsv1.ContextFabricStructureNeeds{}}, want: false},
		{cell: "candidates empty container",
			result: InvestigationResult{SubjectResolution: SubjectResolution{Candidates: []SubjectCandidate{}}}, want: false},
		{cell: "candidates populated",
			result: InvestigationResult{SubjectResolution: SubjectResolution{Candidates: []SubjectCandidate{{}}}}, want: true},
		{cell: "window_clarification nil",
			result: InvestigationResult{WindowClarification: nil}, want: false},
		{cell: "window_clarification empty options",
			result: InvestigationResult{WindowClarification: &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{}}}, want: false},
		{cell: "window_clarification populated",
			result: InvestigationResult{WindowClarification: &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}}, want: true},
		{cell: "needs.window_options empty", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.WindowOptions = []contractsv1.ContextFabricWindowOption{}
		})}, want: false},
		{cell: "needs.window_options populated", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.WindowOptions = []contractsv1.ContextFabricWindowOption{{}}
		})}, want: true},
		{cell: "needs.kind_options empty", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.KindOptions = []contractsv1.ContextFabricKindOption{}
		})}, want: false},
		{cell: "needs.kind_options populated", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.KindOptions = []contractsv1.ContextFabricKindOption{{}}
		})}, want: true},
		{cell: "needs.anchor_options empty", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.AnchorOptions = []contractsv1.ContextFabricAnchorOption{}
		})}, want: false},
		{cell: "needs.anchor_options populated", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.AnchorOptions = []contractsv1.ContextFabricAnchorOption{{}}
		})}, want: true},
		{cell: "needs.handle_options empty", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.HandleOptions = []contractsv1.ContextFabricHandleOption{}
		})}, want: false},
		{cell: "needs.handle_options populated", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.HandleOptions = []contractsv1.ContextFabricHandleOption{{}}
		})}, want: true},
		{cell: "needs.candidate_options empty", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.CandidateOptions = []contractsv1.ContextFabricCandidateOption{}
		})}, want: false},
		{cell: "needs.candidate_options populated", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.CandidateOptions = []contractsv1.ContextFabricCandidateOption{{}}
		})}, want: true},
		{cell: "needs.missing populated, every option list absent", result: InvestigationResult{StructureNeeds: populatedNeeds(func(n *contractsv1.ContextFabricStructureNeeds) {
			n.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
		})}, want: false},
	} {
		got := resultOffersRedeemable(testCase.result)
		t.Logf("| %-52s | redeemable=%-5v | want=%-5v | %s |", testCase.cell, got, testCase.want,
			map[bool]string{true: "ok", false: "MISMATCH"}[got == testCase.want])
		if got != testCase.want {
			t.Errorf("cell %q: redeemable = %v, want %v", testCase.cell, got, testCase.want)
		}
	}
}

// THE SIBLING SWEEP, executed rather than argued.
//
// Every serving path funnels through finalizeServed -- seven callers, its own
// header says so -- so a guard placed at individual exits is a guard with
// holes. The two an exit-by-exit guard is likeliest to miss are the DECISIVE path
// (status is the model's own word, and the synthesis contract admits
// clarification_required) and the REUSE path. Both are exercised here through
// the shared chokepoint, with the same document shape, so the claim "every
// exit is covered" rests on an executed cell per stage rather than on the call
// graph being read correctly.
func TestEveryServingStageRefusesAnUnanswerableClarification(t *testing.T) {
	t.Parallel()
	// The fact reader and synthesizer are the shared fixture's own
	// t.Fatal stubs: this test calls finalizeServed directly, so neither is
	// reachable, and a stub that fails loudly if that ever stops being true
	// is the honest double.
	engine := buildWindowGateEngine(t,
		&countingInterpreter{interpretation: bootstrapInterpretation()},
		&acceptanceGraphReader{resolution: withheldPoolResolution(), context: emptyGraphContext()},
		newMapResultStore())
	unanswerable := InvestigationResult{
		Status:            InvestigationClarificationRequired,
		SubjectResolution: withheldPoolResolution(),
	}
	for _, stage := range []BudgetAssertStage{
		BudgetAssertDecisive,
		BudgetAssertReuse,
		BudgetAssertSubjectlessTerminal,
		BudgetAssertWindowConfirmationRequired,
		BudgetAssertWindowVeto,
		BudgetAssertStructureVeto,
		BudgetAssertContinuationRefusal,
		BudgetAssertInterpretedTimeBound,
	} {
		t.Run(string(stage), func(t *testing.T) {
			_, err := engine.finalizeServed(context.Background(), acceptancePrincipal(), stage, unanswerable, nil, ResponseBudget{})
			t.Logf("| stage=%-34s | err=%v |", stage, err)
			if !errors.Is(err, ErrUnanswerableClarification) {
				t.Fatalf("stage %q served an unanswerable clarification: err = %v", stage, err)
			}
		})
	}
	t.Run("and an answerable one passes at every stage", func(t *testing.T) {
		answerable := unanswerable
		answerable.WindowClarification = &contractsv1.ContextFabricWindowClarification{
			Options: []contractsv1.ContextFabricWindowOption{{}},
		}
		if _, err := engine.finalizeServed(context.Background(), acceptancePrincipal(),
			BudgetAssertDecisive, answerable, nil, ResponseBudget{}); errors.Is(err, ErrUnanswerableClarification) {
			t.Fatal("the guard refused a clarification that carried a window offer -- it is not discriminating")
		}
	})
}

// THE ROLLOUT CELL, driven through tryReuse itself.
//
// Rows written by the build this ticket corrects are already in the reuse
// store, and they are the one shape the write-side structure-bearing
// exclusion does not catch: clarification_required, no candidates,
// StructureNeeds nil. Serving one after this change would turn a stale
// cached row into a 5xx.
//
// WHAT THIS MUST DRIVE, and why nothing weaker measures the filter. The
// filter is a line inside tryReuse, reached only when a reuse gate actually
// hands back a candidate. A test that asserts resultOffersRedeemable by
// hand measures the predicate, not the filter; a test that drives
// Investigate against an empty store with no gate wired never reaches
// tryReuse at all, leaves the filter's lines at zero executions, and stays
// green with the filter deleted. So this drives tryReuse directly, under a
// gate that returns the candidate -- the same shape this package's own
// graph-not-projected miss test uses on reuseAuthorizationStillHolds rather
// than inferring the outcome from an Investigate result.
func TestAStaleUnanswerableClarificationIsNeverServedFromReuse(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		mutate    func(*InvestigationResult)
		wantReuse bool
	}{
		{
			name: "an unanswerable stored clarification misses",
			mutate: func(candidate *InvestigationResult) {
				candidate.Status = InvestigationClarificationRequired
				candidate.SubjectResolution = withheldPoolResolution()
				candidate.StructureNeeds = nil
				candidate.WindowClarification = nil
			},
			wantReuse: false,
		},
		{
			// THE CONTROL. The filter must reject only the UNANSWERABLE
			// clarification, never every cached clarification. Without
			// this arm a filter that dropped the offer test and rejected
			// on status alone would stay green.
			name: "an answerable stored clarification is still reusable",
			mutate: func(candidate *InvestigationResult) {
				candidate.Status = InvestigationClarificationRequired
				candidate.SubjectResolution = withheldPoolResolution()
				candidate.StructureNeeds = nil
				candidate.WindowClarification = &contractsv1.ContextFabricWindowClarification{
					Options: []contractsv1.ContextFabricWindowOption{{}},
				}
			},
			wantReuse: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			project, candidate := reusableCandidate()
			testCase.mutate(&candidate)
			if got := resultOffersRedeemable(candidate); got != testCase.wantReuse {
				t.Fatalf("fixture defect: redeemable = %v, want %v for this arm", got, testCase.wantReuse)
			}

			gateCalls := 0
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{
					resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
					bases:      provenCommitBases(project),
				},
				Results: &resultStoreStub{},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					gateCalls++
					return candidate, true, nil
				}),
			})

			reused, ok := engine.tryReuse(context.Background(), reusePrincipal(), validInvestigationRequest(),
				TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable,
				ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
			if gateCalls == 0 {
				t.Fatal("the reuse gate was never consulted, so the filter under test never ran")
			}
			if ok != testCase.wantReuse {
				t.Fatalf("tryReuse hit = %v, want %v (status %q, redeemable %v)",
					ok, testCase.wantReuse, candidate.Status, resultOffersRedeemable(candidate))
			}
			if ok && resultOffersRedeemable(reused) != true {
				t.Fatal("a reuse hit served a document with no redeemable offer")
			}
		})
	}
}

// THE READ SIDE. finalizeServed covers every path that COMPOSES a result; it
// does not cover the one that hands back a row composed by an earlier build,
// which the result-by-id route serves and the MCP tool forwards. This pins
// the repair at the unit the route calls, including the arms that must NOT
// fire -- an answerable clarification, an answer-bearing result, and nil.
func TestALegacyUnanswerableClarificationIsRepairedOnRead(t *testing.T) {
	t.Parallel()

	t.Run("an unanswerable clarification becomes a terminal", func(t *testing.T) {
		t.Parallel()
		stored := InvestigationResult{
			Status:              InvestigationClarificationRequired,
			SubjectResolution:   withheldPoolResolution(),
			Limitations:         []string{clarificationRequiredLimitationOne},
			DeterministicAnswer: "Clarification is required before this question can be answered.",
		}
		if !RepairLegacyUnanswerableClarification(&stored) {
			t.Fatal("the repair did not fire on the shape it exists for")
		}
		if stored.Status != InvestigationNoMatch {
			t.Fatalf("status = %q, want %q", stored.Status, InvestigationNoMatch)
		}
		if err := assertAnswerableClarification(stored); err != nil {
			t.Fatalf("the repaired row still violates the invariant: %v", err)
		}
		for _, limitation := range stored.Limitations {
			if limitation == clarificationRequiredLimitationOne {
				t.Fatal("the repaired row still carries the clarification limitation")
			}
		}
		if stored.Limitations[0] != noMatchLimitationOfferPoolEmptied {
			t.Fatalf("limitation = %q, want the withheld-pool prose", stored.Limitations[0])
		}
		if stored.SubjectResolution.ClarificationPrompt == "" {
			t.Fatal("the repair cleared the prompt; a withheld pool is no longer distinguishable from an empty one")
		}
	})

	t.Run("a clarification that carries an offer is untouched", func(t *testing.T) {
		t.Parallel()
		stored := InvestigationResult{
			Status:            InvestigationClarificationRequired,
			SubjectResolution: withheldPoolResolution(),
			WindowClarification: &contractsv1.ContextFabricWindowClarification{
				Options: []contractsv1.ContextFabricWindowOption{{}},
			},
		}
		before := stored
		if RepairLegacyUnanswerableClarification(&stored) {
			t.Fatal("the repair fired on an answerable clarification")
		}
		if stored.Status != before.Status {
			t.Fatal("an answerable clarification was mutated")
		}
	})

	t.Run("a non-clarification is untouched", func(t *testing.T) {
		t.Parallel()
		stored := InvestigationResult{Status: InvestigationComplete, DeterministicAnswer: "done"}
		if RepairLegacyUnanswerableClarification(&stored) {
			t.Fatal("the repair fired on an answer-bearing result")
		}
		if stored.DeterministicAnswer != "done" {
			t.Fatal("an answer-bearing result was rewritten")
		}
	})

	t.Run("a nil result is a no-op", func(t *testing.T) {
		t.Parallel()
		if RepairLegacyUnanswerableClarification(nil) {
			t.Fatal("the repair reported a change on a nil result")
		}
	})
}
