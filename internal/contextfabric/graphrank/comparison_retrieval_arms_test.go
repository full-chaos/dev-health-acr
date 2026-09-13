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
	lines []string
}

func (s *orderedComparisonSink) RecordComparisonPolicy(context.Context, ComparisonPolicyEvent) {
	s.lines = append(s.lines, "policy")
}
func (s *orderedComparisonSink) RecordOperandSlot(context.Context, OperandSlotEvent) {
	s.lines = append(s.lines, "slot")
}
func (s *orderedComparisonSink) RecordComparisonReceiptBinding(context.Context, ComparisonReceiptBindingEvent) {
	s.lines = append(s.lines, "binding")
}
func (s *orderedComparisonSink) RecordComparisonDecision(context.Context, ComparisonDecisionEvent) {
	s.lines = append(s.lines, "decision")
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

func TestThePolicyLineIsTheFirstLineOnEveryComparisonDispatch(t *testing.T) {
	t.Parallel()

	cells := []struct {
		name          string
		frame         *contextfabric.QuestionFrame
		wantAdmission contextfabric.ComparisonAdmission
		wantLines     []string
	}{
		{"admitted named pair", twoNamedSlotGateFrame(), contextfabric.ComparisonAdmittedNamedPair,
			[]string{"policy", "binding", "slot", "slot", "decision"}},
		{"scoped hold, before any retrieval", scopedSlotGateFrame(), contextfabric.ComparisonHeldScopedOperand,
			[]string{"policy", "slot", "slot", "binding", "decision"}},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			if got := contextfabric.ClassifyComparisonOperands(cell.frame).Admission; got != cell.wantAdmission {
				t.Fatalf("fixture admission = %q, want %q", got, cell.wantAdmission)
			}
			sink := &orderedComparisonSink{}
			backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{
				"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
				"beta":  {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")},
			}}
			deps := backend.deps()
			deps.OperandResolutionSink = sink
			if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
				slotGateRequest(), testInterpreted("alpha", "beta"), deps, nil, nil, cell.frame, ""); err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			if len(sink.lines) != len(cell.wantLines) {
				t.Fatalf("emitted %v, want %v", sink.lines, cell.wantLines)
			}
			for index := range cell.wantLines {
				if sink.lines[index] != cell.wantLines[index] {
					t.Errorf("line %d = %q, want %q (emitted %v) -- the policy line must open every dispatch, or a dispatch that never ran is indistinguishable from one that held", index, sink.lines[index], cell.wantLines[index], sink.lines)
				}
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
