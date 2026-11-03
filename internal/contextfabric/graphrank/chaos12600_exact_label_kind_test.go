package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12600: reach turns 6 and 10 ("Compare full-chaos/ask-dev and
// full-chaos/dev-health-web over the last 30 days") asked "a repository, a
// project or a team?" though each name matched exactly one repository by its
// full label. A unique exact full-label match of one kind on every side now
// resolves the kind; every other case still asks.

func bareNamePairFrame(a, b string) *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{a}}},
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{b}}},
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

const (
	labelAskDev = "full-chaos/ask-dev"
	labelWeb    = "full-chaos/dev-health-web"
)

func repoNode(id, label string) CandidateNode {
	return exactMatchNode(contextfabric.SubjectRepository, id, label)
}

func aliasOnlyNode(kind contextfabric.SubjectKind, id, label, alias string) CandidateNode {
	node := exactMatchNode(kind, id, label)
	node.Attributes["aliases"] = []interface{}{alias}
	return node
}

type exactLabelCell struct {
	name        string
	census      []CandidateNode
	truncated   bool
	wantCommit  bool
	wantRefusal ExactLabelKindRefusal
}

func exactLabelCells() []exactLabelCell {
	repoA, repoB := repoNode("repository:askdev", labelAskDev), repoNode("repository:web", labelWeb)
	return []exactLabelCell{
		{name: "both sides one repository (reach turns 6 and 10)", census: []CandidateNode{repoA, repoB}, wantCommit: true},
		{name: "case-folded full label", census: []CandidateNode{repoNode("repository:askdev", "Full-Chaos/Ask-Dev"), repoB}, wantCommit: true},
		{name: "one label also names a project", census: []CandidateNode{repoA, exactMatchNode(contextfabric.SubjectProject, "project:askdev", labelAskDev), repoB}, wantRefusal: ExactLabelKindRefusalSideSeveral},
		{name: "one label names two repositories", census: []CandidateNode{repoA, repoNode("repository:askdev2", labelAskDev), repoB}, wantRefusal: ExactLabelKindRefusalSideSeveral},
		{name: "one side has no exact label", census: []CandidateNode{repoA, repoNode("repository:web", "full-chaos/dev-health-web-legacy")}, wantRefusal: ExactLabelKindRefusalSideNoMatch},
		{name: "one side matches only by alias", census: []CandidateNode{repoA, aliasOnlyNode(contextfabric.SubjectRepository, "repository:web", "web-renamed", labelWeb)}, wantRefusal: ExactLabelKindRefusalSideNoMatch},
		{name: "mixed kinds across the sides", census: []CandidateNode{repoA, exactMatchNode(contextfabric.SubjectTeam, "team:web", labelWeb)}, wantRefusal: ExactLabelKindRefusalMixedKinds},
		{name: "truncated census", census: []CandidateNode{repoA, repoB}, truncated: true, wantRefusal: ExactLabelKindRefusalTruncated},
	}
}

func TestExactLabelPairKindDecides(t *testing.T) {
	t.Parallel()
	frame := bareNamePairFrame(labelAskDev, labelWeb)
	comparison := contextfabric.ClassifyComparisonOperands(frame)
	if comparison.Admission != contextfabric.ComparisonOutOfCutUnstatedKind {
		t.Fatalf("fixture admission = %q, want out_of_cut_unstated_kind", comparison.Admission)
	}
	for _, cell := range exactLabelCells() {
		census, truncated := cell.census, cell.truncated
		deps := ResolveDeps{ExactNameCandidates: func(context.Context) ([]CandidateNode, bool, error) { return census, truncated, nil }}
		resolved, matched, refusal, ok, err := exactLabelPairKind(context.Background(), deps, comparison)
		if err != nil {
			t.Fatalf("%s: error = %v", cell.name, err)
		}
		if ok != cell.wantCommit || refusal != cell.wantRefusal {
			t.Errorf("%s: ok=%v refusal=%q, want ok=%v refusal=%q", cell.name, ok, refusal, cell.wantCommit, cell.wantRefusal)
		}
		if ok && (resolved.Admission != contextfabric.ComparisonAdmittedNamedPair || resolved.Slots[0].Kind != contextfabric.SubjectRepository || resolved.Slots[1].Kind != contextfabric.SubjectRepository) {
			t.Errorf("%s: resolved = %+v, want an admitted pair of repositories", cell.name, resolved)
		}
		if ok != (len(matched) == 2) {
			t.Errorf("%s: matched = %v, want one subject per side exactly when resolved", cell.name, matched)
		}
	}
	// A stated pair and a pair without a census are not this rule's cases.
	stated := comparison
	stated.Admission = contextfabric.ComparisonAdmittedNamedPair
	if _, _, refusal, ok, _ := exactLabelPairKind(context.Background(), ResolveDeps{}, stated); ok || refusal != "" {
		t.Errorf("stated pair: ok=%v refusal=%q, want not applicable", ok, refusal)
	}
	if _, _, refusal, ok, _ := exactLabelPairKind(context.Background(), ResolveDeps{}, comparison); ok || refusal != ExactLabelKindRefusalNoCensus {
		t.Errorf("no census: ok=%v refusal=%q, want %q", ok, refusal, ExactLabelKindRefusalNoCensus)
	}
	failing := ResolveDeps{ExactNameCandidates: func(context.Context) ([]CandidateNode, bool, error) { return nil, false, errors.New("census down") }}
	if _, _, refusal, ok, err := exactLabelPairKind(context.Background(), failing, comparison); ok || err == nil || refusal != ExactLabelKindRefusalCensusFailed {
		t.Errorf("failed census: ok=%v err=%v refusal=%q, want the error and %q", ok, err, refusal, ExactLabelKindRefusalCensusFailed)
	}
}

// Through the real resolver: a resolved pair commits both sides on the first
// turn, carries the kind for the answer's disclosure, and reads the exact-name
// population once; every refused cell commits neither side and carries none.
func TestBareNamePairResolvesThroughTheResolver(t *testing.T) {
	t.Parallel()
	for _, cell := range exactLabelCells() {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeGraphBackend{
				searchResults:                map[string][]CandidateNode{},
				enableExactNameCandidates:    true,
				exactNameCandidates:          cell.census,
				exactNameCandidatesTruncated: cell.truncated,
			}
			sink := &recordingPolicySink{}
			deps := backend.deps()
			deps.OperandResolutionSink = sink
			resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
				slotGateRequest(), testInterpreted(labelAskDev, labelWeb), deps, nil, nil, bareNamePairFrame(labelAskDev, labelWeb), "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			// The decision basis is on the policy line either way.
			if len(sink.events) != 1 {
				t.Fatalf("policy lines = %d, want 1", len(sink.events))
			}
			wantBasis := ComparisonKindBasisNone
			if cell.wantCommit {
				wantBasis = ComparisonKindBasisExactLabel
			}
			if event := sink.events[0]; event.KindBasis != wantBasis || event.KindRefusal != cell.wantRefusal {
				t.Errorf("policy line kind_basis=%q kind_refusal=%q, want %q/%q", event.KindBasis, event.KindRefusal, wantBasis, cell.wantRefusal)
			}
			if cell.wantCommit {
				if len(resolution.Committed) != 2 || resolution.ExactLabelKind != contextfabric.SubjectRepository {
					t.Fatalf("committed = %v, exact-label kind = %q; want both repositories and kind repository (prompt %q)", resolution.Committed, resolution.ExactLabelKind, resolution.ClarificationPrompt)
				}
				if backend.exactNameCandidatesCalls != 1 {
					t.Errorf("exact-name population read %d times, want once for the check and the comparison", backend.exactNameCandidatesCalls)
				}
				return
			}
			if len(resolution.Committed) == 2 || resolution.ExactLabelKind != "" {
				t.Fatalf("refused cell committed %v with exact-label kind %q, want the kind question unchanged", resolution.Committed, resolution.ExactLabelKind)
			}
		})
	}
}

// Codex r1 on the CHAOS-12600 PR: both sides may name one subject. The rule
// resolves the kind, then the comparison refuses a pair whose sides are one
// subject and asks again: nothing commits, so there is no answer and no kind
// disclosure is owed. This pins that the stamp is absent because the turn
// asks, not because a committed answer lost it.
func TestBareNamePairNamingOneSubjectTwiceAsksAgainWithoutTheStamp(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		searchResults:             map[string][]CandidateNode{},
		enableExactNameCandidates: true,
		exactNameCandidates:       []CandidateNode{repoNode("repository:askdev", labelAskDev)},
	}
	sink := &recordingPolicySink{}
	deps := backend.deps()
	deps.OperandResolutionSink = sink
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
		slotGateRequest(), testInterpreted(labelAskDev, labelAskDev), deps, nil, nil, bareNamePairFrame(labelAskDev, labelAskDev), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].KindBasis != ComparisonKindBasisExactLabel {
		t.Fatalf("policy lines = %+v, want one with kind_basis exact_label", sink.events)
	}
	if len(resolution.Committed) != 0 || resolution.ClarificationPrompt == "" {
		t.Fatalf("committed = %v prompt = %q, want nothing committed and the pair asked again", resolution.Committed, resolution.ClarificationPrompt)
	}
	if resolution.ExactLabelKind != "" {
		t.Fatalf("exact-label kind = %q, want none: no answer reads the sides", resolution.ExactLabelKind)
	}
}

// Codex r1 on the CHAOS-12600 PR: a census that fails ends the resolution with
// its error, and the policy line naming census_failed is written first, so the
// refusal is in the run's own artifacts.
func TestBareNamePairCensusFailureWritesItsPolicyLine(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		searchResults:             map[string][]CandidateNode{},
		enableExactNameCandidates: true,
		exactNameCandidatesErr:    errors.New("census down"),
	}
	sink := &recordingPolicySink{}
	deps := backend.deps()
	deps.OperandResolutionSink = sink
	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
		slotGateRequest(), testInterpreted(labelAskDev, labelWeb), deps, nil, nil, bareNamePairFrame(labelAskDev, labelWeb), ""); err == nil {
		t.Fatal("ResolveSubjectsWithCommitBasis() error = nil, want the census error")
	}
	if len(sink.events) != 1 {
		t.Fatalf("policy lines = %d, want 1", len(sink.events))
	}
	if event := sink.events[0]; event.KindBasis != ComparisonKindBasisNone || event.KindRefusal != ExactLabelKindRefusalCensusFailed {
		t.Errorf("policy line kind_basis=%q kind_refusal=%q, want none/%q", event.KindBasis, event.KindRefusal, ExactLabelKindRefusalCensusFailed)
	}
}

// Codex r3 on the CHAOS-12600 PR: an operand with several terms is not a bare
// name. The rule reads one term per side, so a side with more terms keeps
// today's path (the kind question), with no refusal reason: not this rule's
// case.
func TestASideWithSeveralTermsIsNotABareName(t *testing.T) {
	t.Parallel()
	frame := bareNamePairFrame(labelAskDev, labelWeb)
	frame.SubjectExpression.Explicit.Operands[1].Named.Terms = []string{labelWeb, "web app"}
	comparison := contextfabric.ClassifyComparisonOperands(frame)
	census := []CandidateNode{repoNode("repository:askdev", labelAskDev), repoNode("repository:web", labelWeb)}
	deps := ResolveDeps{ExactNameCandidates: func(context.Context) ([]CandidateNode, bool, error) { return census, false, nil }}
	_, matched, refusal, ok, err := exactLabelPairKind(context.Background(), deps, comparison)
	if err != nil || ok || refusal != "" || matched != nil {
		t.Fatalf("ok=%v refusal=%q matched=%v err=%v, want not this rule's case", ok, refusal, matched, err)
	}
}

// Codex r3 on the CHAOS-12600 PR: a resolved exact-label pair whose turn ends
// on an inactive caller team hint still writes its policy line, with
// kind_basis exact_label.
func TestAnExactLabelPairEndingOnAnInactiveHintWritesItsPolicyLine(t *testing.T) {
	t.Parallel()
	const teamA, teamB = "Platform Core", "Delivery Ops"
	retired := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:retired", Label: "Retired"}
	backend := &fakeGraphBackend{
		searchResults:             map[string][]CandidateNode{},
		enableExactNameCandidates: true,
		exactNameCandidates:       []CandidateNode{exactMatchNode(contextfabric.SubjectTeam, "team:core", teamA), exactMatchNode(contextfabric.SubjectTeam, "team:ops", teamB)},
		exactInactive:             map[string]string{SubjectKey(retired): "team:core"},
	}
	sink := &recordingPolicySink{}
	deps := backend.deps()
	deps.OperandResolutionSink = sink
	request := slotGateRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: retired.Kind, ID: retired.CanonicalID, Label: retired.Label, Source: "workbench"}}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
		request, testInterpreted(teamA, teamB), deps, nil, nil, bareNamePairFrame(teamA, teamB), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.InactiveHints) == 0 {
		t.Fatalf("premise: inactive hints = %v, want the turn to end on the inactive team hint", resolution.InactiveHints)
	}
	if len(sink.events) != 1 || sink.events[0].KindBasis != ComparisonKindBasisExactLabel {
		t.Fatalf("policy lines = %+v, want one with kind_basis exact_label", sink.events)
	}
}
