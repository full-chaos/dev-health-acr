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
		resolved, refusal, ok, err := exactLabelPairKind(context.Background(), deps, comparison)
		if err != nil {
			t.Fatalf("%s: error = %v", cell.name, err)
		}
		if ok != cell.wantCommit || refusal != cell.wantRefusal {
			t.Errorf("%s: ok=%v refusal=%q, want ok=%v refusal=%q", cell.name, ok, refusal, cell.wantCommit, cell.wantRefusal)
		}
		if ok && (resolved.Admission != contextfabric.ComparisonAdmittedNamedPair || resolved.Slots[0].Kind != contextfabric.SubjectRepository || resolved.Slots[1].Kind != contextfabric.SubjectRepository) {
			t.Errorf("%s: resolved = %+v, want an admitted pair of repositories", cell.name, resolved)
		}
	}
	// A stated pair and a pair without a census are not this rule's cases.
	stated := comparison
	stated.Admission = contextfabric.ComparisonAdmittedNamedPair
	if _, refusal, ok, _ := exactLabelPairKind(context.Background(), ResolveDeps{}, stated); ok || refusal != "" {
		t.Errorf("stated pair: ok=%v refusal=%q, want not applicable", ok, refusal)
	}
	if _, refusal, ok, _ := exactLabelPairKind(context.Background(), ResolveDeps{}, comparison); ok || refusal != ExactLabelKindRefusalNoCensus {
		t.Errorf("no census: ok=%v refusal=%q, want %q", ok, refusal, ExactLabelKindRefusalNoCensus)
	}
	failing := ResolveDeps{ExactNameCandidates: func(context.Context) ([]CandidateNode, bool, error) { return nil, false, errors.New("census down") }}
	if _, refusal, ok, err := exactLabelPairKind(context.Background(), failing, comparison); ok || err == nil || refusal != ExactLabelKindRefusalCensusFailed {
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
