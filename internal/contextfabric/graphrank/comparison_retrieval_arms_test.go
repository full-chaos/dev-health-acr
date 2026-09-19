package graphrank

// EVERY PER-TERM RETRIEVAL ARM, PER OPERAND; THE POLICY LINE FIRST ON EVERY
// DISPATCH; AND RECEIPT BINDING THROUGH THE SAME IDENTITY AUTHORITY AS
// RETRIEVAL.
//
// An operand slot resolves from its own terms through every per-term arm the
// single-subject path runs -- ordinary search, the keyed alias read, the
// kind-scoped search for the operand's stated kind, and the exact-name arm --
// so an operand reachable through any one of them is in its own pool. The two
// request-scoped reads (the alias read and the exact-name population) happen
// once per comparison, whichever operand needs them.

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func committedIDs(resolution contextfabric.SubjectResolution) map[string]bool {
	ids := make(map[string]bool, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		ids[subject.CanonicalID] = true
	}
	return ids
}

func candidateIDSet(resolution contextfabric.SubjectResolution) map[string]bool {
	ids := make(map[string]bool, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		ids[candidate.Subject.CanonicalID] = true
	}
	return ids
}

func TestEveryOperandSlotRetrievesThroughEveryPerTermArm(t *testing.T) {
	t.Parallel()

	beta := exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")
	cells := []struct {
		name    string
		backend func() *fakeGraphBackend
		// wantCommitted: operand A reached through this arm alone resolves to
		// one subject of its stated kind, so the pair publishes.
		wantCommitted bool
		check         func(t *testing.T, backend *fakeGraphBackend)
	}{
		{
			name: "keyed alias read",
			backend: func() *fakeGraphBackend {
				alpha := candidateNode(contextfabric.SubjectTeam, "team_alpha", "Alpha Team", 1, "*")
				alpha.Mechanism = contextfabric.MatchAlias
				alpha.FromKeyedIdentityLookup = true
				return &fakeGraphBackend{
					searchResults:        map[string][]CandidateNode{"beta": {beta}},
					enableAliasLookup:    true,
					aliasLookupClaimants: map[string][]CandidateNode{"alpha": {alpha}},
					aliasLookupComplete:  true,
				}
			},
			wantCommitted: true,
			check: func(t *testing.T, backend *fakeGraphBackend) {
				if len(backend.aliasLookupCalls) != 1 {
					t.Fatalf("alias read ran %d times, want exactly 1 for the whole comparison -- it is declared at most once per request", len(backend.aliasLookupCalls))
				}
				if got := backend.aliasLookupCalls[0]; len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
					t.Errorf("alias read terms = %v, want [alpha beta] -- every operand's terms in one read", got)
				}
			},
		},
		{
			name: "kind-scoped search for the operand's stated kind",
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{
					searchResults:     map[string][]CandidateNode{"beta": {beta}},
					enableSearchKind:  true,
					searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{"alpha": {contextfabric.SubjectTeam: {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")}}},
				}
			},
			wantCommitted: true,
			check: func(t *testing.T, backend *fakeGraphBackend) {
				for _, call := range backend.searchKindCalls {
					if call.kind != contextfabric.SubjectTeam {
						t.Errorf("kind-scoped search ran for kind %q, want only the operands' stated kind %q", call.kind, contextfabric.SubjectTeam)
					}
				}
				if len(backend.searchKindCalls) != 2 {
					t.Errorf("kind-scoped search ran %d times, want 2 -- once per operand term", len(backend.searchKindCalls))
				}
			},
		},
		{
			name: "exact-name arm",
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{
					searchResults:             map[string][]CandidateNode{"beta": {beta}},
					enableExactNameCandidates: true,
					exactNameCandidates:       []CandidateNode{exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
				}
			},
			wantCommitted: true,
			check: func(t *testing.T, backend *fakeGraphBackend) {
				if backend.exactNameCandidatesCalls != 1 {
					t.Errorf("exact-name population fetched %d times, want 1 for the whole comparison", backend.exactNameCandidatesCalls)
				}
			},
		},
	}

	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			backend := cell.backend()
			resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
				slotGateRequest(), testInterpreted("alpha", "beta"), backend.deps(), nil, nil, twoNamedSlotGateFrame(), "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			// FIXTURE CONTROL: ordinary search cannot reach operand A, so only
			// the arm under test can put it in the pool.
			for _, term := range backend.searchCalls {
				if term == "alpha" && len(backend.searchResults["alpha"]) != 0 {
					t.Fatalf("ordinary search returns operand A, so this cell does not isolate its arm")
				}
			}
			if !candidateIDSet(resolution)["team_alpha"] && !committedIDs(resolution)["team_alpha"] {
				t.Fatalf("operand A reachable only through the %s is absent from the comparison: candidates=%v committed=%v",
					cell.name, resolution.Candidates, resolution.Committed)
			}
			if cell.wantCommitted {
				committed := committedIDs(resolution)
				if !committed["team_alpha"] || !committed["team_beta"] || len(committed) != 2 {
					t.Errorf("committed = %v, want both operands -- each resolves to one subject of its stated kind", resolution.Committed)
				}
			}
			cell.check(t, backend)
		})
	}
}

type orderedComparisonSink struct {
	lines     []string
	outcomes  []operandSlotState
	decisions []string
}

func (s *orderedComparisonSink) RecordComparisonPolicy(context.Context, ComparisonPolicyEvent) {
	s.lines = append(s.lines, "policy")
}
func (s *orderedComparisonSink) RecordOperandSlot(_ context.Context, event OperandSlotEvent) {
	s.lines = append(s.lines, "slot")
	s.outcomes = append(s.outcomes, event.Outcome)
}
func (s *orderedComparisonSink) RecordComparisonReceiptBinding(context.Context, ComparisonReceiptBindingEvent) {
	s.lines = append(s.lines, "binding")
}
func (s *orderedComparisonSink) RecordComparisonDecision(_ context.Context, event ComparisonDecisionEvent) {
	s.lines = append(s.lines, "decision")
	s.decisions = append(s.decisions, event.Decision)
}

func scopedSlotGateFrame() *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{
					Terms: []string{"alpha"}, ExpectedKind: slotGateKindPointer(contextfabric.SubjectTeam)}},
				{Kind: contextfabric.SubjectOperandScoped, Scoped: &contextfabric.ScopedSetExpression{
					AnchorTerms: []string{"infrastructure"}, MemberKind: contextfabric.SubjectTeam}},
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

func slotGateHint(subject contextfabric.SubjectRef) contextfabric.SubjectHint {
	return contextfabric.SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "candidate"}
}

// TestThePolicyLineOpensEveryComparisonDecisionPath enumerates every way a
// comparison is decided -- each hold reason publishable() and state() can
// produce, and the published pair -- and requires the policy line first and
// exactly once on each. A slot count other than two is not a cell: the
// classifier admits only a pair, so no dispatch can reach it.
func TestThePolicyLineOpensEveryComparisonDecisionPath(t *testing.T) {
	t.Parallel()

	alpha := exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")
	beta := exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")
	alphaSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "alpha"}
	twinSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha_twin", Label: "alpha"}
	gammaSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_gamma", Label: "gamma"}

	cells := []struct {
		name         string
		frame        *contextfabric.QuestionFrame
		backend      func() *fakeGraphBackend
		hints        []contextfabric.SubjectHint
		wantDecision string
		// wantOutcome is a slot outcome the fixture must actually produce, so
		// the cell cannot pass on a hold it did not construct.
		wantOutcome operandSlotState
	}{
		{
			name: "published pair", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": {alpha}, "beta": {beta}}}
			},
			wantDecision: comparisonDecisionPublished, wantOutcome: operandSlotResolved,
		},
		{
			name: "hold: scoped operand, before any retrieval", frame: scopedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": {alpha}}}
			},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotScoped,
		},
		{
			name: "hold: an operand with no candidate", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{searchResults: map[string][]CandidateNode{"beta": {beta}}}
			},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotNoCandidate,
		},
		{
			name: "hold: an ambiguous operand", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{searchResults: map[string][]CandidateNode{
					"alpha": {
						candidateNode(contextfabric.SubjectTeam, "team_alpha_one", "Alpha One", 0.68, "*"),
						candidateNode(contextfabric.SubjectTeam, "team_alpha_two", "Alpha Two", 0.68, "*"),
					},
					"beta": {beta},
				}}
			},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotAmbiguous,
		},
		{
			name: "hold: an over-committed operand", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				twin := exactMatchNode(contextfabric.SubjectTeam, "team_alpha_twin", "alpha")
				return &fakeGraphBackend{
					searchResults: map[string][]CandidateNode{"alpha": {alpha, twin}, "beta": {beta}},
					exactHints:    map[string]CandidateNode{SubjectKey(alphaSubject): alpha, SubjectKey(twinSubject): twin},
				}
			},
			hints:        []contextfabric.SubjectHint{slotGateHint(alphaSubject), slotGateHint(twinSubject)},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotOverCommitted,
		},
		{
			name: "hold: an operand committed to another kind", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{searchResults: map[string][]CandidateNode{
					"alpha": {exactMatchNode(contextfabric.SubjectProject, "project_alpha", "alpha")},
					"beta":  {beta},
				}}
			},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotWrongKind,
		},
		{
			name: "hold: one subject answering both operands", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				shared := candidateNode(contextfabric.SubjectTeam, "team_shared", "Shared Team", 1, "*")
				shared.Mechanism = contextfabric.MatchAlias
				shared.FromKeyedIdentityLookup = true
				return &fakeGraphBackend{
					enableAliasLookup:    true,
					aliasLookupClaimants: map[string][]CandidateNode{"alpha": {shared}, "beta": {shared}},
					aliasLookupComplete:  true,
				}
			},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotResolved,
		},
		{
			name: "hold: a selection bound to neither operand", frame: twoNamedSlotGateFrame(),
			backend: func() *fakeGraphBackend {
				return &fakeGraphBackend{
					searchResults: map[string][]CandidateNode{"alpha": {alpha}, "beta": {beta}},
					exactHints:    map[string]CandidateNode{SubjectKey(gammaSubject): exactMatchNode(contextfabric.SubjectTeam, "team_gamma", "gamma")},
				}
			},
			hints:        []contextfabric.SubjectHint{slotGateHint(gammaSubject)},
			wantDecision: comparisonDecisionHeld, wantOutcome: operandSlotResolved,
		},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			sink := &orderedComparisonSink{}
			deps := cell.backend().deps()
			deps.OperandResolutionSink = sink
			request := slotGateRequest()
			request.RequestedScope.SubjectHints = cell.hints
			if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
				request, testInterpreted("alpha", "beta"), deps, nil, nil, cell.frame, ""); err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			if len(sink.decisions) != 1 || sink.decisions[0] != cell.wantDecision {
				t.Fatalf("decisions = %v, want [%s] -- the fixture did not reach this decision path", sink.decisions, cell.wantDecision)
			}
			reached := false
			for _, outcome := range sink.outcomes {
				if outcome == cell.wantOutcome {
					reached = true
				}
			}
			if !reached {
				t.Fatalf("slot outcomes = %v, want one %q -- the fixture did not construct this hold", sink.outcomes, cell.wantOutcome)
			}
			policies := 0
			for _, line := range sink.lines {
				if line == "policy" {
					policies++
				}
			}
			if len(sink.lines) == 0 || sink.lines[0] != "policy" || policies != 1 {
				t.Errorf("lines = %v, want the policy line first and exactly once -- a decision path without it is indistinguishable from a dispatch that never ran", sink.lines)
			}
		})
	}
}

func TestAReceiptBindsThroughTheKeyedIdentityReadAsWellAsGraphAliases(t *testing.T) {
	t.Parallel()

	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "Alpha Team"}
	other := candidateNode(contextfabric.SubjectTeam, "team_gamma", "Gamma Team", 1, "*")
	slots := []contextfabric.ComparisonOperandSlot{
		{Position: 0, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"}},
		{Position: 1, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{"beta"}},
	}
	cells := []struct {
		name        string
		claimants   map[string][]CandidateNode
		wantSlot0   int
		wantUnbound int
		why         string
	}{
		{"no witness at all", nil, 0, 1, "a selection with no identity evidence in either operand stays unbound"},
		{"claimed for operand A's term", map[string][]CandidateNode{"alpha": {candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")}}, 1, 0,
			"the keyed read that admits the subject to operand A's pool is a witness for binding it there"},
		{"claimed under a differently cased spelling", map[string][]CandidateNode{"ALPHA": {candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")}}, 1, 0,
			"the read keys a normalization class by its first spelling, so the operand's own spelling must still find it"},
		{"claimed for both operands' terms", map[string][]CandidateNode{
			"alpha": {candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")},
			"beta":  {candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")},
		}, 0, 1, "a selection witnessed for both operands answers neither and must not be guessed into one"},
		{"a different subject claimed", map[string][]CandidateNode{"alpha": {other}}, 0, 1,
			"a claimant for the term that is not the selected subject is no witness for it"},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			request := slotGateRequest()
			request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "candidate"}}
			// The graph node carries NO alias attribute, so graph aliases can
			// never be the witness in any cell here.
			node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")
			backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{SubjectKey(subject): node}}
			bound, unbound, err := bindReceiptsToSlots(context.Background(), storage.Principal{OrgID: "org-1"}, request, backend.deps(),
				slots, nil, comparisonAliasClaimants{claimantsByTerm: cell.claimants, complete: true})
			if err != nil {
				t.Fatalf("bindReceiptsToSlots() error = %v", err)
			}
			if len(bound[0]) != cell.wantSlot0 || unbound != cell.wantUnbound || len(bound[1]) != 0 {
				t.Errorf("slot0=%d slot1=%d unbound=%d, want slot0=%d slot1=0 unbound=%d -- %s",
					len(bound[0]), len(bound[1]), unbound, cell.wantSlot0, cell.wantUnbound, cell.why)
			}
		})
	}
}

func TestAReceiptWitnessedOnlyByTheAliasReadBindsThroughTheRealDispatch(t *testing.T) {
	t.Parallel()

	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "Alpha Team"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")
	claimant := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")
	claimant.Mechanism = contextfabric.MatchAlias
	claimant.FromKeyedIdentityLookup = true
	backend := &fakeGraphBackend{
		exactHints:           map[string]CandidateNode{SubjectKey(subject): node},
		searchResults:        map[string][]CandidateNode{"beta": {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")}},
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"alpha": {claimant}},
		aliasLookupComplete:  true,
	}
	sink := &recordingBindingSink{}
	deps := backend.deps()
	deps.OperandResolutionSink = sink
	request := slotGateRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "candidate"}}

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
		request, testInterpreted("alpha", "beta"), deps, nil, nil, twoNamedSlotGateFrame(), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(sink.bindings) != 1 || sink.bindings[0].BoundCount != 1 || sink.bindings[0].UnboundCount != 0 ||
		len(sink.bindings[0].BoundSlotPositions) != 1 || sink.bindings[0].BoundSlotPositions[0] != 0 {
		t.Fatalf("binding lines = %+v, want one line binding the selection to slot 0 with none unbound", sink.bindings)
	}
	committed := committedIDs(resolution)
	if !committed["team_alpha"] || !committed["team_beta"] {
		t.Errorf("committed = %v, want both operands -- the selection answers operand A and operand B resolves on its own", resolution.Committed)
	}
}

type recordingBindingSink struct {
	bindings []ComparisonReceiptBindingEvent
}

func (s *recordingBindingSink) RecordComparisonPolicy(context.Context, ComparisonPolicyEvent) {}
func (s *recordingBindingSink) RecordOperandSlot(context.Context, OperandSlotEvent)           {}
func (s *recordingBindingSink) RecordComparisonReceiptBinding(_ context.Context, event ComparisonReceiptBindingEvent) {
	s.bindings = append(s.bindings, event)
}
func (s *recordingBindingSink) RecordComparisonDecision(context.Context, ComparisonDecisionEvent) {}

// TestTheAliasReadsCompletenessReachesEachOperandsGates pins that the keyed
// read's own completeness claim is what each operand's commit gates see, in
// both directions. A slot handed a constant would either claim a uniqueness
// nobody proved or withhold the identity fast path the read earned, and the
// committed set alone cannot show which: the commit digest records the value
// the gates decided under.
func TestTheAliasReadsCompletenessReachesEachOperandsGates(t *testing.T) {
	t.Parallel()

	alphaSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "Alpha Team"}
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprintf("read complete=%t", complete), func(t *testing.T) {
			t.Parallel()
			alpha := candidateNode(contextfabric.SubjectTeam, "team_alpha", "Alpha Team", 1, "*")
			alpha.Mechanism = contextfabric.MatchAlias
			alpha.FromKeyedIdentityLookup = true
			backend := &fakeGraphBackend{
				searchResults:        map[string][]CandidateNode{"beta": {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")}},
				enableAliasLookup:    true,
				aliasLookupClaimants: map[string][]CandidateNode{"alpha": {alpha}},
				aliasLookupComplete:  complete,
			}
			resolution, _, _, digests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
				slotGateRequest(), testInterpreted("alpha", "beta"), backend.deps(), nil, nil, twoNamedSlotGateFrame(), "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			digest := digests.For(alphaSubject)
			if complete {
				if !committedIDs(resolution)["team_alpha"] {
					t.Fatalf("committed = %v, want operand A committed on the complete keyed read", resolution.Committed)
				}
				if !digest.AliasLookupComplete {
					t.Errorf("operand A digest = %+v, want AliasLookupComplete -- the gates must decide under the read's own completeness claim", digest)
				}
				return
			}
			if digest.AliasLookupComplete || digest.CommitGate == "identity_fast_path" {
				t.Errorf("operand A digest = %+v, want no completeness claim and no identity fast path -- the read did not enumerate the population", digest)
			}
		})
	}
}
