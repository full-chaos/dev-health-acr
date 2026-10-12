package contextfabric

import (
	"context"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func unboundTestFrame() *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectProject},
	}}
}

func unboundTestSubject(kind SubjectKind, id string) SubjectRef {
	return SubjectRef{Kind: kind, CanonicalID: string(kind) + ":" + id, Label: id}
}

func unboundTestCandidate(subject SubjectRef, term string, state ResolutionState) SubjectCandidate {
	return SubjectCandidate{Subject: subject, State: state, MatchedTerms: []string{term}}
}

func TestUnboundScopeAnchorDecisionTable(t *testing.T) {
	a, b := unboundTestSubject(SubjectTeam, "a"), unboundTestSubject(SubjectTeam, "b")
	repo := unboundTestSubject(SubjectRepository, "r")
	for _, tc := range []struct {
		name       string
		anchorKind SubjectKind
		resolution SubjectResolution
		want       bool
	}{
		{"two committed, no term match", SubjectTeam, SubjectResolution{
			Committed:  []SubjectRef{a, b},
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "x", ResolutionCommitted), unboundTestCandidate(b, "y", ResolutionCommitted)},
		}, true},
		{"two committed, one term match", SubjectTeam, SubjectResolution{
			Committed:  []SubjectRef{a, b},
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "platform", ResolutionCommitted), unboundTestCandidate(b, "y", ResolutionCommitted)},
		}, false},
		{"sole committed", SubjectTeam, SubjectResolution{
			Committed:  []SubjectRef{a},
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "x", ResolutionCommitted)},
		}, false},
		{"none committed, candidate of the kind", SubjectTeam, SubjectResolution{
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "platform", ResolutionAmbiguous)},
		}, true},
		{"none committed, candidate of another kind", SubjectTeam, SubjectResolution{
			Candidates: []SubjectCandidate{unboundTestCandidate(repo, "platform", ResolutionAmbiguous)},
		}, false},
		{"no candidate at all", SubjectTeam, SubjectResolution{}, false},
		{"anchor kind off the tree", SubjectIncident, SubjectResolution{
			Candidates: []SubjectCandidate{unboundTestCandidate(unboundTestSubject(SubjectIncident, "i"), "platform", ResolutionAmbiguous)},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unboundScopeAnchorNamed(unboundTestFrame(), tc.anchorKind, tc.resolution, CommitBasisSet{}); got != tc.want {
				t.Fatalf("unboundScopeAnchorNamed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnboundScopeAnchorTurnReadsAsItsOwnTerminalReason(t *testing.T) {
	a, b := unboundTestSubject(SubjectTeam, "a"), unboundTestSubject(SubjectTeam, "b")
	frame := unboundTestFrame()
	resolution := SubjectResolution{
		Committed:  []SubjectRef{a, b},
		Candidates: []SubjectCandidate{unboundTestCandidate(a, "x", ResolutionCommitted), unboundTestCandidate(b, "y", ResolutionCommitted)},
	}
	served := unboundScopeAnchorResolution(frame, SubjectTeam, resolution)
	if len(served.Committed) != 0 || len(served.Candidates) != 2 || served.ClarificationPrompt == "" {
		t.Fatalf("served resolution = %+v, want two ambiguous candidates, none committed, a prompt", served)
	}
	for _, candidate := range served.Candidates {
		if candidate.State != ResolutionAmbiguous {
			t.Fatalf("candidate %+v is not ambiguous", candidate)
		}
	}
	if got := unboundAnchorAwareTerminalReason(withUnboundScopeAnchorTurn(context.Background()), FrameGate{}, served, 0, declaredKindDecision{}); got != unboundScopeAnchorTerminalReason {
		t.Fatalf("terminal reason = %q, want %q", got, unboundScopeAnchorTerminalReason)
	}
	if got := unboundAnchorAwareTerminalReason(context.Background(), FrameGate{}, served, 0, declaredKindDecision{}); got != "ambiguous" {
		t.Fatalf("an unmarked ambiguous turn reads %q, want ambiguous", got)
	}
}

// turnSwitchInterpreter states no anchor kind on its first turn and the repository
// anchor kind on every later one.
type turnSwitchInterpreter struct {
	frame *QuestionFrame
	calls *int
}

func (i turnSwitchInterpreter) Interpret(ctx context.Context, p storage.Principal, r InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	*i.calls++
	kind := SubjectRepository
	if *i.calls == 1 {
		kind = ""
	}
	return roleInterpreter{frame: i.frame, anchorKind: kind}.Interpret(ctx, p, r)
}

// A receipt of a candidate the guard withdraws from the served resolution
// reads applied against the pre-guard resolution and must read the post-guard
// disposition.
func TestAReceiptOfACandidateTheUnboundAnchorGuardWithdrewIsNotReportedApplied(t *testing.T) {
	repo := roleCandidate("subr_role_team_withdrawn", SubjectTeam, "team:withdrawn")
	graph := &roleGraph{first: SubjectResolution{
		Candidates: []SubjectCandidate{
			roleCandidate("subr_role_repo_a", SubjectRepository, "repository:anchor-a"),
			roleCandidate("subr_role_repo_b", SubjectRepository, "repository:anchor-b"),
			repo,
		}, Committed: []SubjectRef{},
	}}
	calls := 0
	factReads := 0
	telemetry := &recordingTelemetry{}
	engine := roleEngine(t, turnSwitchInterpreter{frame: roleScopedFrame(SubjectProject), calls: &calls}, graph, newMapResultStore(), telemetry, &factReads)
	principal := storage.Principal{OrgID: "org_role"}

	first, err := engine.Investigate(context.Background(), principal, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("turn 1 Investigate() error = %v", err)
	}
	if !roleHasCandidate(first.SubjectResolution.Candidates, repo.ReceiptID) {
		t.Fatalf("turn 1 stored candidates %#v lack the non-anchor receipt", first.SubjectResolution.Candidates)
	}

	second := validInvestigationRequestWithConfirmedWindow()
	second.RequestID = "request_unbound_turn_2"
	second.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: first.ResultID, ReceiptID: repo.ReceiptID}}
	continued, err := engine.Investigate(context.Background(), principal, second)
	if err != nil {
		t.Fatalf("turn 2 Investigate() error = %v", err)
	}
	if got := telemetry.subjectlessTerminalReasons; len(got) != 2 || got[1] != unboundScopeAnchorTerminalReason {
		t.Fatalf("turn 2 did not end in the unbound-anchor guard: reasons %#v", got)
	}
	if roleHasCandidate(continued.SubjectResolution.Candidates, repo.ReceiptID) {
		t.Fatalf("the guard kept the non-anchor candidate: %#v", continued.SubjectResolution.Candidates)
	}
	want := []contractsv1.ContextFabricPriorSubjectReceiptEntry{{
		PriorResultID: first.ResultID, ReceiptID: repo.ReceiptID, Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth,
	}}
	if !reflect.DeepEqual(continued.SubjectResolution.PriorSubjectReceiptDispositions, want) {
		t.Fatalf("dispositions = %#v, want %#v", continued.SubjectResolution.PriorSubjectReceiptDispositions, want)
	}
}

func roleHasCandidate(candidates []SubjectCandidate, receipt string) bool {
	for _, candidate := range candidates {
		if candidate.ReceiptID == receipt {
			return true
		}
	}
	return false
}
