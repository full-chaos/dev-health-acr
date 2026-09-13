package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE ROLE DECISION, EXECUTED THROUGH THE ENGINE. Every case here drives
// Engine.Investigate as the API route does and reads the SERVED document and
// the EMITTED decision. The cases that must continue are driven a second turn:
// the caller redeems the anchor candidate the first turn offered, and the
// second turn must commit that anchor and read facts -- a clarification whose
// offer cannot be redeemed into progress is not a continuation.
//
// The fixtures are the corpus rows' frames and offer shapes, by row id; no
// question text is carried.

// roleInterpreter carries a validated frame and the winning sample's scope
// anchor kind out on the family outcome -- the two inputs the role decision
// reads from interpretation.
type roleInterpreter struct {
	frame      *QuestionFrame
	anchorKind SubjectKind
}

func (i roleInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}, QuestionFamilyOutcome{
		Family:             QuestionFamilyUnclassified,
		Source:             QuestionFamilySourceNone,
		Frame:              i.frame,
		Gate:               FrameGate{Outcome: FrameGatePassed},
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ScopeAnchorKind: i.anchorKind, ScopeAnchorTerm: "anchor-term"},
	}, nil
}

// roleGraph answers the first turn with a fixed resolution and offer material,
// and a turn that carries a prior-subject hint for the anchor with that anchor
// committed on a proven basis. dropped simulates graph authorization removing
// candidates on the first turn.
type roleGraph struct {
	mu            sync.Mutex
	first         SubjectResolution
	material      StructureOfferMaterial
	anchor        SubjectRef
	dropped       int
	hintsSeen     [][]SubjectHint
	anchorKinds   []SubjectKind
	discoverCalls int
}

func (g *roleGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "role-graph-key", Epoch: 0}, nil
}

func (g *roleGraph) ResolveSubjects(ctx context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, _ *QuestionFrame, anchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hintsSeen = append(g.hintsSeen, request.RequestedScope.SubjectHints)
	g.anchorKinds = append(g.anchorKinds, anchorKind)
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind == g.anchor.Kind && hint.ID == g.anchor.CanonicalID {
			committed := SubjectResolution{
				Candidates: []SubjectCandidate{{
					ReceiptID: "subr_role_anchor_committed", Subject: g.anchor, State: ResolutionCommitted,
					MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
				}},
				Committed: []SubjectRef{g.anchor},
			}
			return committed, StructureOfferMaterial{}, provenCommitBases(g.anchor), nil, nil
		}
	}
	if g.dropped > 0 {
		RecordSubjectCandidatesAuthzDropped(ctx, g.dropped)
	}
	return g.first, g.material, nil, nil, nil
}

func (g *roleGraph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.discoverCalls++
	return GraphContext{
		Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
		EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

// roleEngine builds an engine over the doubles with a result store, so a
// second turn can redeem a receipt the first turn saved.
func roleEngine(t *testing.T, interpreter QuestionInterpreter, graph GraphReader, store InvestigationResultStore, telemetry EngineTelemetry, factReads *int) *Engine {
	t.Helper()
	var mu sync.Mutex
	next := 0
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			mu.Lock()
			*factReads++
			mu.Unlock()
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "The anchor is on track.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "The anchor is on track based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(400, 0).UTC() },
		NewResultID: func() string {
			mu.Lock()
			defer mu.Unlock()
			next++
			return fmt.Sprintf("result_role_turn_%02d", next)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// roleCandidate is one uncommitted candidate of kind.
func roleCandidate(receipt string, kind SubjectKind, id string) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID: receipt,
		Subject:   SubjectRef{Kind: kind, CanonicalID: id, Label: id},
		State:     ResolutionAmbiguous, MatchReasons: []string{"Exact canonical subject label match."}, Confidence: 1,
	}
}

// roleTwoTeamAnchors is the anchor pool both scoped corpus rows carried on
// their terminal turn: two team candidates, neither committed.
func roleTwoTeamAnchors() []SubjectCandidate {
	return []SubjectCandidate{
		roleCandidate("subr_role_team_a", SubjectTeam, "team:anchor-a"),
		roleCandidate("subr_role_team_b", SubjectTeam, "team:anchor-b"),
	}
}

func roleContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestTheScopedAnchorTurnClarifiesAndTheRedeemedAnchorServes is the regression
// class the role decision exists for, executed as two turns per case.
func TestTheScopedAnchorTurnClarifiesAndTheRedeemedAnchorServes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		cell       string
		frame      *QuestionFrame
		anchorKind SubjectKind
		resolution SubjectResolution
		material   StructureOfferMaterial
		redeem     SubjectCandidate
		roles      string
		advanced   string
		channel    string
	}{
		{
			// corpus row cv-scoped-projects-by-team-bounded, terminal turn:
			// window confirmed, two team anchor candidates, subject_anchor
			// missing with no option list.
			cell: "cv-scoped-projects-by-team-bounded terminal turn", frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam,
			resolution: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}, ClarificationPrompt: "Which subject did you mean: team:anchor-a, team:anchor-b?"},
			material:   StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}},
			redeem:     roleTwoTeamAnchors()[0],
			roles:      "anchor:team:open,member:project:population", advanced: "anchor:team", channel: "subject_candidate",
		},
		{
			// corpus row qb-scoped, terminal turn: the same anchor pool after a
			// kind receipt.
			cell: "qb-scoped refused turn", frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam,
			resolution: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}},
			material:   StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}},
			redeem:     roleTwoTeamAnchors()[1],
			roles:      "anchor:team:open,member:project:population", advanced: "anchor:team", channel: "subject_candidate",
		},
		{
			// corpus rows cv-scoped-projects-by-team-bounded / qb-scoped, turn 1:
			// team and pull_request candidates with a kind option list.
			cell: "scoped first turn with kind options", frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam,
			resolution: SubjectResolution{Candidates: append(roleTwoTeamAnchors(), roleCandidate("subr_role_pr", contractsv1.ContextFabricSubjectPullRequest, "pull_request:1")), Committed: []SubjectRef{}},
			material: StructureOfferMaterial{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor},
				KindOptions: []contractsv1.ContextFabricKindOption{
					{ReceiptID: "kindr_role_project", OptionID: "opt_kind_project", Label: "Project", Kind: SubjectProject, OfferSource: contractsv1.ContextFabricStructureOfferEngine},
					{ReceiptID: "kindr_role_team", OptionID: "opt_kind_team", Label: "Team", Kind: SubjectTeam, OfferSource: contractsv1.ContextFabricStructureOfferEngine},
				},
			},
			redeem: roleTwoTeamAnchors()[0],
			roles:  "anchor:team:open,member:project:population", advanced: "anchor:team", channel: "kind_option",
		},
		{
			cell: "team ownership with only a repository-anchor choice", frame: roleScopedFrame(SubjectTeam), anchorKind: SubjectRepository,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{
				roleCandidate("subr_role_repo_a", SubjectRepository, "repository:anchor-a"),
				roleCandidate("subr_role_repo_b", SubjectRepository, "repository:anchor-b"),
			}, Committed: []SubjectRef{}},
			redeem: roleCandidate("subr_role_repo_a", SubjectRepository, "repository:anchor-a"),
			roles:  "anchor:repository:open,member:team:population", advanced: "anchor:repository", channel: "subject_candidate",
		},
		{
			cell: "legitimate ambiguous anchors offered on the anchor channel", frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam,
			resolution: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}},
			material: StructureOfferMaterial{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
				AnchorOptions: []contractsv1.ContextFabricAnchorOption{
					{ReceiptID: "ancr_role_team_a", OptionID: "opt_anchor_a", Label: "team:anchor-a", Kind: SubjectTeam, CanonicalID: "team:anchor-a", MatchedTermHash: "0123456789abcdef01234567", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
				},
			},
			redeem: roleTwoTeamAnchors()[0],
			roles:  "anchor:team:open,member:project:population", advanced: "anchor:team", channel: "anchor_option",
		},
		{
			cell: "the sample stated no anchor kind", frame: roleScopedFrame(SubjectProject), anchorKind: "",
			resolution: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}},
			redeem:     roleTwoTeamAnchors()[0],
			roles:      "anchor:undeclared:open,member:project:population", advanced: "anchor:team", channel: "subject_candidate",
		},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			store := newMapResultStore()
			graph := &roleGraph{first: testCase.resolution, material: testCase.material, anchor: testCase.redeem.Subject}
			telemetry := &recordingTelemetry{}
			factReads := 0
			engine := roleEngine(t, roleInterpreter{frame: testCase.frame, anchorKind: testCase.anchorKind}, graph, store, telemetry, &factReads)
			principal := storage.Principal{OrgID: "org_role"}

			first, err := engine.Investigate(context.Background(), principal, validInvestigationRequestWithConfirmedWindow())
			if err != nil {
				t.Fatalf("turn 1 Investigate() error = %v", err)
			}
			if first.Status != InvestigationClarificationRequired {
				t.Fatalf("turn 1 status = %q (basis %q, limitations %#v), want clarification_required -- the offered anchor IS the unresolved need", first.Status, first.RefusalBasis, first.Limitations)
			}
			if first.RefusalBasis != "" || roleContains(first.Limitations, declaredKindTerminalLimitation) {
				t.Fatalf("turn 1 carries the declared-kind refusal (basis %q, limitations %#v)", first.RefusalBasis, first.Limitations)
			}
			if want := []string{"ambiguous"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
				t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
			}
			observed := telemetry.subjectlessTerminalAnswerability[0]
			if observed.EvaluatedRoles != testCase.roles || observed.AdvancedRole != testCase.advanced || observed.AdvancingChannel != testCase.channel {
				t.Fatalf("emitted evaluated/advanced/channel = %q/%q/%q, want %q/%q/%q", observed.EvaluatedRoles, observed.AdvancedRole, observed.AdvancingChannel, testCase.roles, testCase.advanced, testCase.channel)
			}
			if factReads != 0 {
				t.Fatalf("turn 1 read facts %d times without a committed subject", factReads)
			}

			second := validInvestigationRequestWithConfirmedWindow()
			second.RequestID = "request_role_turn_2"
			second.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: first.ResultID, ReceiptID: testCase.redeem.ReceiptID}}
			continued, err := engine.Investigate(context.Background(), principal, second)
			if err != nil {
				t.Fatalf("turn 2 Investigate() error = %v", err)
			}
			switch continued.Status {
			case InvestigationComplete, InvestigationPartial, InvestigationDegraded:
			default:
				t.Fatalf("turn 2 status = %q (limitations %#v), want a served answer after redeeming the offered anchor", continued.Status, continued.Limitations)
			}
			if len(continued.SubjectResolution.Committed) != 1 || continued.SubjectResolution.Committed[0] != testCase.redeem.Subject {
				t.Fatalf("turn 2 committed = %#v, want the redeemed anchor %#v", continued.SubjectResolution.Committed, testCase.redeem.Subject)
			}
			if factReads == 0 {
				t.Fatal("turn 2 served without reading facts for the committed anchor")
			}
		})
	}
}

// TestOffersNoPickDecidesAreRefused holds the other direction: a kind match on
// a role no pick decides, a wrong-kind named-subject pool, and member-kind
// offers against a scope anchor are all refused on the first turn, with the
// decision naming the roles it evaluated and no advanced role.
func TestOffersNoPickDecidesAreRefused(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	for _, testCase := range []struct {
		cell       string
		frame      *QuestionFrame
		anchorKind SubjectKind
		resolution SubjectResolution
		material   StructureOfferMaterial
		roles      string
	}{
		{
			cell: "grouped: only group-kind candidates", frame: roleGroupedFrame(SubjectProject, SubjectTeam),
			resolution: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}},
			roles:      "member:project:population,group:team:population",
		},
		{
			cell: "grouped: kind options restating both declared axes", frame: roleGroupedFrame(SubjectProject, SubjectTeam),
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt},
			material: StructureOfferMaterial{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
				KindOptions: []contractsv1.ContextFabricKindOption{
					{ReceiptID: "kindr_role_g_project", OptionID: "opt_g_project", Label: "Project", Kind: SubjectProject, OfferSource: contractsv1.ContextFabricStructureOfferEngine},
					{ReceiptID: "kindr_role_g_team", OptionID: "opt_g_team", Label: "Team", Kind: SubjectTeam, OfferSource: contractsv1.ContextFabricStructureOfferEngine},
				},
			},
			roles: "member:project:population,group:team:population",
		},
		{
			cell: "named subject: wrong-kind options only", frame: chaos5660NamedFrame(&project),
			resolution: SubjectResolution{Candidates: chaos5660CIRunCandidates(2), Committed: []SubjectRef{}},
			material:   chaos5660MeasuredOffers(),
			roles:      "subject:project:open",
		},
		{
			cell: "scope anchor: member-kind candidates only", frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{
				roleCandidate("subr_role_project_a", SubjectProject, "project:a"),
				roleCandidate("subr_role_project_b", SubjectProject, "project:b"),
			}, Committed: []SubjectRef{}},
			roles: "anchor:team:open,member:project:population",
		},
		{
			// The anchor kind the sample stated is what separates these two
			// offers from an answerable anchor: neither is the member kind,
			// and neither is a team.
			cell: "scope anchor: offers of neither the anchor nor the member kind", frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam,
			resolution: SubjectResolution{Candidates: []SubjectCandidate{
				roleCandidate("subr_role_pr_a", contractsv1.ContextFabricSubjectPullRequest, "pull_request:a"),
				roleCandidate("subr_role_repo_x", SubjectRepository, "repository:x"),
			}, Committed: []SubjectRef{}},
			roles: "anchor:team:open,member:project:population",
		},
		{
			// With no anchor kind stated, I11 still excludes the member kind.
			cell: "scope anchor of unstated kind: member-kind candidates only", frame: roleScopedFrame(SubjectProject), anchorKind: "",
			resolution: SubjectResolution{Candidates: []SubjectCandidate{
				roleCandidate("subr_role_project_c", SubjectProject, "project:c"),
			}, Committed: []SubjectRef{}},
			roles: "anchor:undeclared:open,member:project:population",
		},
		{
			cell:       "explicit set: no offer of either operand's kind",
			frame:      roleExplicitFrame(roleNamedOperand(&project), roleNamedOperand(&project)),
			resolution: SubjectResolution{Candidates: chaos5660CIRunCandidates(2), Committed: []SubjectRef{}},
			roles:      "operand:project:open,operand:project:open",
		},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			factReads := 0
			graph := &roleGraph{first: testCase.resolution, material: testCase.material}
			engine := roleEngine(t, roleInterpreter{frame: testCase.frame, anchorKind: testCase.anchorKind}, graph, newMapResultStore(), telemetry, &factReads)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, validInvestigationRequestWithConfirmedWindow())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != InvestigationNoMatch || result.RefusalBasis != contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched {
				t.Fatalf("status/basis = %q/%q, want no_match/declared_kind_unmatched -- no offer advances a role a pick decides", result.Status, result.RefusalBasis)
			}
			if !roleContains(result.Limitations, declaredKindTerminalLimitation) {
				t.Fatalf("limitations = %#v, want the basis's own sentence", result.Limitations)
			}
			if want := []string{declaredKindTerminalReason}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
				t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
			}
			observed := telemetry.subjectlessTerminalAnswerability[0]
			if observed.EvaluatedRoles != testCase.roles || observed.AdvancedRole != "none" || observed.AdvancingChannel != "none" {
				t.Fatalf("emitted evaluated/advanced/channel = %q/%q/%q, want %q/none/none", observed.EvaluatedRoles, observed.AdvancedRole, observed.AdvancingChannel, testCase.roles)
			}
			if observed.OffersEvaluated == 0 || observed.OffersAdvancing != 0 {
				t.Fatalf("emitted offers evaluated/advancing = %d/%d, want >0/0", observed.OffersEvaluated, observed.OffersAdvancing)
			}
			if factReads != 0 {
				t.Fatalf("a refused turn read facts %d times", factReads)
			}
		})
	}
}

// TestUnauthorizedAnchorCandidatesAreNeverOffers drives graph authorization
// removing anchor candidates. Candidates the caller may not see are not
// offers: with all of them removed the turn is an authorization-narrowed
// empty pool, not a clarification and not a declared-kind refusal; with one
// authorized anchor left the turn clarifies on that anchor alone and the
// redeemed anchor serves.
func TestUnauthorizedAnchorCandidatesAreNeverOffers(t *testing.T) {
	t.Parallel()
	t.Run("every anchor candidate removed", func(t *testing.T) {
		t.Parallel()
		telemetry := &recordingTelemetry{}
		factReads := 0
		graph := &roleGraph{first: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, dropped: 2}
		engine := roleEngine(t, roleInterpreter{frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam}, graph, newMapResultStore(), telemetry, &factReads)
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, validInvestigationRequestWithConfirmedWindow())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Status != InvestigationNoMatch || result.RefusalBasis != "" {
			t.Fatalf("status/basis = %q/%q, want no_match with no refusal basis", result.Status, result.RefusalBasis)
		}
		if want := []string{"authz_filtered_to_empty"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
			t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
		}
		if observed := telemetry.subjectlessTerminalAnswerability[0]; observed.OffersEvaluated != 0 || observed.AdvancedRole != "none" {
			t.Fatalf("emitted offers_evaluated/advanced_role = %d/%q, want 0/none -- a removed candidate is not an offer", observed.OffersEvaluated, observed.AdvancedRole)
		}
	})
	t.Run("one authorized anchor left", func(t *testing.T) {
		t.Parallel()
		telemetry := &recordingTelemetry{}
		factReads := 0
		authorized := roleTwoTeamAnchors()[:1]
		graph := &roleGraph{first: SubjectResolution{Candidates: authorized, Committed: []SubjectRef{}}, anchor: authorized[0].Subject, dropped: 1}
		store := newMapResultStore()
		engine := roleEngine(t, roleInterpreter{frame: roleScopedFrame(SubjectProject), anchorKind: SubjectTeam}, graph, store, telemetry, &factReads)
		principal := storage.Principal{OrgID: "org_role"}
		first, err := engine.Investigate(context.Background(), principal, validInvestigationRequestWithConfirmedWindow())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if first.Status != InvestigationClarificationRequired || len(first.SubjectResolution.Candidates) != 1 {
			t.Fatalf("status/candidates = %q/%d, want clarification_required on the one authorized anchor", first.Status, len(first.SubjectResolution.Candidates))
		}
		if observed := telemetry.subjectlessTerminalAnswerability[0]; observed.OffersEvaluated != 1 || observed.AdvancedRole != "anchor:team" {
			t.Fatalf("emitted offers_evaluated/advanced_role = %d/%q, want 1/anchor:team", observed.OffersEvaluated, observed.AdvancedRole)
		}
		second := validInvestigationRequestWithConfirmedWindow()
		second.RequestID = "request_role_turn_2"
		second.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: first.ResultID, ReceiptID: authorized[0].ReceiptID}}
		continued, err := engine.Investigate(context.Background(), principal, second)
		if err != nil {
			t.Fatalf("turn 2 Investigate() error = %v", err)
		}
		if continued.Status == InvestigationNoMatch || continued.Status == InvestigationClarificationRequired || factReads == 0 {
			t.Fatalf("turn 2 status/fact reads = %q/%d, want a served answer on the redeemed anchor", continued.Status, factReads)
		}
	})
}

// TestTheEmittedTerminalLineCarriesTheRoleDecision reads the decision back
// out of the production sink, at Info, through the real JSON handler, on an
// engine drive -- one turn that advanced an anchor and one that advanced
// nothing -- so the pins hold on the line an operator reads, not on a
// recorder.
func TestTheEmittedTerminalLineCarriesTheRoleDecision(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		cell       string
		frame      *QuestionFrame
		resolution SubjectResolution
		want       map[string]any
	}{
		{
			cell: "anchor advanced", frame: roleScopedFrame(SubjectProject),
			resolution: SubjectResolution{Candidates: append(roleTwoTeamAnchors(), roleCandidate("subr_role_project_x", SubjectProject, "project:x")), Committed: []SubjectRef{}},
			want: map[string]any{
				"reason": "ambiguous", "refusal_basis": "none", "declared_kinds": "project", "offered_kinds": "team,project",
				"evaluated_roles": "anchor:team:open,member:project:population", "advanced_role": "anchor:team",
				"advancing_channel": "subject_candidate", "offers_evaluated": float64(3), "offers_advancing": float64(2),
			},
		},
		{
			cell: "nothing advanced", frame: roleGroupedFrame(SubjectProject, SubjectTeam),
			resolution: SubjectResolution{Candidates: roleTwoTeamAnchors(), Committed: []SubjectRef{}},
			want: map[string]any{
				"reason": declaredKindTerminalReason, "refusal_basis": "declared_kind_unmatched", "declared_kinds": "team,project", "offered_kinds": "team",
				"evaluated_roles": "member:project:population,group:team:population", "advanced_role": "none",
				"advancing_channel": "none", "offers_evaluated": float64(2), "offers_advancing": float64(0),
			},
		},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			telemetry := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			factReads := 0
			engine := roleEngine(t, roleInterpreter{frame: testCase.frame, anchorKind: SubjectTeam}, &roleGraph{first: testCase.resolution}, newMapResultStore(), telemetry, &factReads)
			if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			var line map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				var rec map[string]any
				if json.Unmarshal([]byte(raw), &rec) == nil && rec["msg"] == "context fabric subjectless terminal" {
					if line != nil {
						t.Fatal("more than one subjectless terminal line for one turn")
					}
					line = rec
				}
			}
			if line == nil {
				t.Fatalf("no subjectless terminal line emitted; log: %s", buf.String())
			}
			if line["level"] != "INFO" {
				t.Fatalf("level = %v, want INFO", line["level"])
			}
			for key, want := range testCase.want {
				if line[key] != want {
					t.Errorf("%s = %#v, want %#v", key, line[key], want)
				}
			}
		})
	}
}

// roleOrgFrameWithGoals is an organization_scope frame with the given goals.
func roleOrgFrameWithGoals(member *SubjectKind, goals ...InvestigationGoal) *QuestionFrame {
	frame := roleOrgFrame(member)
	frame.Goals = goals
	return frame
}

// TestOrganizationScopeQuestionsRefuseOnTheirOwnBasis pins the D48 terminal by
// the corpus rows it names (ids only): an organization-scope question that
// counts nothing is refused on organization_scope_unsupported, with that
// basis's own sentence, on every turn shape the rows produced -- with offers,
// without offers, and for a caller that declined clarification. A counting
// question keeps the role decision, and a refusing frame gate keeps its own
// basis.
func TestOrganizationScopeQuestionsRefuseOnTheirOwnBasis(t *testing.T) {
	t.Parallel()
	organization := SubjectOrganization
	project := SubjectProject
	mixedPool := SubjectResolution{Candidates: []SubjectCandidate{
		roleCandidate("subr_role_review", contractsv1.ContextFabricSubjectPullRequestReview, "pull_request_review:1"),
		roleCandidate("subr_role_ci", contractsv1.ContextFabricSubjectCIRun, "ci_pipeline_run:1"),
		roleCandidate("subr_role_project", SubjectProject, "project:1"),
	}, Committed: []SubjectRef{}}
	for _, testCase := range []struct {
		cell               string
		frame              *QuestionFrame
		gate               FrameGate
		resolution         SubjectResolution
		allowClarification bool
		basis              contractsv1.ContextFabricRefusalBasis
		limitation         string
		reason             string
	}{
		{
			// corpus row cv-b5-org-health, second turn: an organization member
			// kind and a pool of reviews, CI runs and projects.
			cell: "cv-b5-org-health: offers of other kinds", frame: roleOrgFrameWithGoals(&organization, GoalAssessState),
			resolution: mixedPool, allowClarification: true,
			basis: organizationScopeTerminalBasis, limitation: organizationScopeTerminalLimitation, reason: organizationScopeTerminalReason,
		},
		{
			// corpus row cv-b5-org-health, first turn: no member kind, the
			// offer pool emptied by the vector-only exclusion.
			cell: "cv-b5-org-health: offer pool emptied", frame: roleOrgFrameWithGoals(nil, GoalAssessState),
			resolution:         SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt},
			allowClarification: true,
			basis:              organizationScopeTerminalBasis, limitation: organizationScopeTerminalLimitation, reason: organizationScopeTerminalReason,
		},
		{
			// corpus row cv-c7-org-drivers: an organization-scope drivers
			// question.
			cell: "cv-c7-org-drivers: offers of other kinds", frame: roleOrgFrameWithGoals(nil, GoalExplainDrivers),
			resolution: mixedPool, allowClarification: true,
			basis: organizationScopeTerminalBasis, limitation: organizationScopeTerminalLimitation, reason: organizationScopeTerminalReason,
		},
		{
			cell: "a caller that declined clarification is refused the same way", frame: roleOrgFrameWithGoals(nil, GoalAssessState),
			resolution: mixedPool, allowClarification: false,
			basis: organizationScopeTerminalBasis, limitation: organizationScopeTerminalLimitation, reason: organizationScopeTerminalReason,
		},
		{
			// The control: counts ARE supported, so a counting question keeps
			// the role decision and its declared-kind basis.
			cell: "a counting question keeps the role decision", frame: roleOrgFrameWithGoals(&project, GoalCountOrAggregate),
			resolution: mixedPool, allowClarification: true,
			basis: declaredKindTerminalBasis, limitation: declaredKindTerminalLimitation, reason: declaredKindTerminalReason,
		},
		{
			// A count beside a non-count goal is outside the served envelope:
			// the non-count goal is the unsupported part the sentence names.
			cell: "a count beside an assessment is refused on the organization basis", frame: roleOrgFrameWithGoals(&project, GoalCountOrAggregate, GoalAssessState),
			resolution: mixedPool, allowClarification: true,
			basis: organizationScopeTerminalBasis, limitation: organizationScopeTerminalLimitation, reason: organizationScopeTerminalReason,
		},
		{
			cell: "a refusing frame gate keeps its own basis", frame: roleOrgFrameWithGoals(nil, GoalAssessState),
			gate:       FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable},
			resolution: mixedPool, allowClarification: true,
			basis: contractsv1.ContextFabricRefusalBasisMemberKindUnservable, limitation: contractsv1.ContextFabricFrameInvariantRefusalLimitation, reason: "frame_gate_refused",
		},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			factReads := 0
			interpreter := roleGateInterpreter{roleInterpreter: roleInterpreter{frame: testCase.frame}, gate: testCase.gate}
			engine := roleEngine(t, interpreter, &roleGraph{first: testCase.resolution}, newMapResultStore(), telemetry, &factReads)
			request := validInvestigationRequestWithConfirmedWindow()
			request.Options.AllowClarification = testCase.allowClarification
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != InvestigationNoMatch || result.RefusalBasis != testCase.basis {
				t.Fatalf("status/basis = %q/%q, want no_match/%q", result.Status, result.RefusalBasis, testCase.basis)
			}
			if result.Completeness.RefusalBasis != testCase.basis {
				t.Fatalf("completeness.refusal_basis = %q, want %q", result.Completeness.RefusalBasis, testCase.basis)
			}
			if !roleContains(result.Limitations, testCase.limitation) {
				t.Fatalf("limitations = %#v, want %q", result.Limitations, testCase.limitation)
			}
			if testCase.basis != organizationScopeTerminalBasis && roleContains(result.Limitations, organizationScopeTerminalLimitation) {
				t.Fatalf("limitations = %#v carry the organization-scope sentence on a turn refused on %q", result.Limitations, testCase.basis)
			}
			if want := []string{testCase.reason}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
				t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
			}
			if want := []string{string(testCase.basis)}; !stringSlicesEqual(telemetry.subjectlessTerminalRefusalBases, want) {
				t.Fatalf("emitted refusal_basis = %#v, want %#v", telemetry.subjectlessTerminalRefusalBases, want)
			}
			if factReads != 0 {
				t.Fatalf("a refused turn read facts %d times", factReads)
			}
		})
	}
}

// roleGateInterpreter is roleInterpreter with an explicit frame gate.
type roleGateInterpreter struct {
	roleInterpreter
	gate FrameGate
}

func (i roleGateInterpreter) Interpret(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	interpretation, outcome, err := i.roleInterpreter.Interpret(ctx, principal, request)
	if i.gate.Outcome != "" {
		outcome.Gate = i.gate
	}
	return interpretation, outcome, err
}
