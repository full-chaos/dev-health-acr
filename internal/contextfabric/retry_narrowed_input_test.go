package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	retryListedMembers   = 13
	retryCensusDeclared  = 40
	retryFindingsPerItem = 5
)

var retryAnchorTeam = SubjectRef{Kind: SubjectTeam, CanonicalID: "team_platform", Label: "Platform"}

// retryCensusGraph is the owned-projects listing with two relationship paths
// per member (team -> project, project -> repository) and a
// kind_census_truncated detail and degraded reason minted over the listed
// cohort, as falkorgraph's reader mints them.
type retryCensusGraph struct {
	ownedProjectsGraph
}

func (g retryCensusGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	graph, err := g.ownedProjectsGraph.DiscoverContext(ctx, principal, request)
	if err != nil {
		return graph, err
	}
	refs := []string{}
	for index, member := range graph.Cohort.Members {
		repository := SubjectRef{Kind: SubjectRepository, CanonicalID: fmt.Sprintf("repo_%03d", index), Label: fmt.Sprintf("repo %d", index)}
		for hop, ends := range [][2]SubjectRef{{retryAnchorTeam, member.Subject}, {member.Subject, repository}} {
			ref := fmt.Sprintf("ref_path_%02d_%d", index, hop)
			graph.Paths = append(graph.Paths, RelationshipPath{
				PathID: fmt.Sprintf("path_member_%02d_%d", index, hop),
				Nodes:  []SubjectRef{ends[0], ends[1]},
				Edges: []RelationshipEdge{{
					Type: contractsv1.ContextFabricRelationshipRelatedTo, From: ends[0], To: ends[1],
					Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, EvidenceRefIDs: []string{ref},
				}},
				WhyRelevant:    "The project is reached from the team that owns it.",
				EvidenceRefIDs: []string{ref},
			})
			refs = append(refs, ref)
		}
		refs = append(refs, member.EvidenceRefIDs...)
	}
	graph.EvidenceRefIDs = refs
	declared, served := retryCensusDeclared, len(graph.Cohort.Members)
	reason := KindCensusTruncatedReason(SubjectProject, declared, served)
	detail := CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: SubjectProject, Declared: &declared, Served: &served, Raw: reason,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	graph.Coverage.Details = append(graph.Coverage.Details, detail)
	graph.Coverage.DegradedReasons = append(graph.Coverage.DegradedReasons, reason)
	return graph, nil
}

// findingsComposer stands in for the model: it writes retryFindingsPerItem
// readiness findings per cohort member it was given, and the production
// composer (RuntimeAnswerSynthesizer.composeSynthesisResult, the step the
// model path runs on every validated draft) builds the result from that draft
// and the synthesis input. No model is called.
type findingsComposer struct {
	calls  *int
	inputs *[]SynthesisInput
}

func (s findingsComposer) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	*s.calls++
	*s.inputs = append(*s.inputs, input)
	draft := SynthesisDraft{
		Status: InvestigationComplete, StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
		Conflicts: []Finding{}, Limitations: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, EvidenceRefIDs: []string{},
	}
	for index, member := range input.Graph.Cohort.Members {
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, member.EvidenceRefIDs...)
		for n := 0; n < retryFindingsPerItem; n++ {
			draft.ReadinessGaps = append(draft.ReadinessGaps, Finding{
				FindingID: fmt.Sprintf("finding_gap_%02d_%d", index, n), Kind: "relationship",
				Summary: "A readiness gap appears open.", EvidenceRefIDs: slices.Clone(member.EvidenceRefIDs),
			})
		}
	}
	return RuntimeAnswerSynthesizer{}.composeSynthesisResult(ctx, principal, input, draft, ModelExecutionReceipt{}, nil, nil)
}

// The cohort retry re-synthesizes over half the members, and the answer it
// serves describes that half: no relationship path names a member the retry
// left out, and the census detail's served count, its raw text and the
// degraded reason that repeats it all state the served member count.
func TestTheCohortRetryServesNoFieldNamingTheMembersItLeftOut(t *testing.T) {
	var calls int
	var inputs []SynthesisInput
	telemetry := &recordingTelemetry{}
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: SubjectProject},
		},
		Temporal: TemporalIntentCurrent,
		Version:  QuestionFrameVersion,
	}
	validated := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}},
			outcome:     QuestionFamilyOutcome{Frame: &validated.Frame, Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel},
		},
		Graph: retryCensusGraph{ownedProjectsGraph{&capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}, Details: []CoverageDetail{}}},
		}, retryListedMembers}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer:  findingsComposer{calls: &calls, inputs: &inputs},
		Requirements: registryDeriver{},
		Telemetry:    telemetry,
	}, budgetStageOptions(serverItemCeiling, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxCohortMembers = memberCutCallerCap
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
	if err != nil {
		t.Fatalf("err = %v, want the retried answer served", err)
	}
	if calls != 2 || len(inputs) != 2 || result.Cohort == nil {
		t.Fatalf("synthesis calls = %d cohort = %+v, want the first pass and the one retry", calls, result.Cohort)
	}
	served := map[string]bool{}
	for _, member := range result.Cohort.Members {
		served[member.Subject.CanonicalID] = true
	}
	if len(served) == 0 || len(served) >= retryListedMembers {
		t.Fatalf("served members = %d, want the retry's narrowed cohort of fewer than %d", len(served), retryListedMembers)
	}
	left := []string{}
	for _, member := range inputs[0].Graph.Cohort.Members {
		if !served[member.Subject.CanonicalID] {
			left = append(left, member.Subject.CanonicalID)
		}
	}

	var failures []string
	for _, path := range result.Paths {
		for _, node := range path.Nodes {
			if slices.Contains(left, node.CanonicalID) {
				failures = append(failures, fmt.Sprintf("path %s names %s, a member the retry left out", path.PathID, node.CanonicalID))
			}
		}
	}
	want := KindCensusTruncatedReason(SubjectProject, retryCensusDeclared, len(served))
	census := 0
	for _, detail := range result.Coverage.Details {
		if detail.Code != contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
			continue
		}
		census++
		if detail.Served == nil || *detail.Served != len(served) {
			failures = append(failures, fmt.Sprintf("census served = %v, want %d", detail.Served, len(served)))
		}
		if detail.Raw != want {
			failures = append(failures, fmt.Sprintf("census raw = %q, want %q", detail.Raw, want))
		}
	}
	if census != 1 {
		failures = append(failures, fmt.Sprintf("census details = %d, want 1", census))
	}
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "kind_census_truncated:") && reason != want {
			failures = append(failures, fmt.Sprintf("degraded reason %q, want %q", reason, want))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("served %d of %d members; the answer still describes the members the retry left out:\n%s", len(served), retryListedMembers, strings.Join(failures, "\n"))
	}
}

// The grouped cohort retry narrows through the same tail: the graph input it
// re-synthesizes from names only the members it keeps -- every group lists
// kept members only and keeps at least one, no relationship path names a
// member it left out, and the census states the kept member count.
func TestAGroupedCohortRetryInputNamesOnlyTheMembersItKeeps(t *testing.T) {
	previousClaims := groupReadClaimsPerMember
	groupReadClaimsPerMember = 5
	groupReadSynthesisPasses = nil
	t.Cleanup(func() {
		groupReadClaimsPerMember = previousClaims
		groupReadSynthesisPasses = nil
	})
	ids := groupReadRetryMemberIDs()
	teamOf := func(index int) string {
		if index >= 3 {
			return "team_platform"
		}
		return "team_security"
	}
	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				return bundle
			}
		}
		for index, id := range ids {
			bundle.Facts = append(bundle.Facts, teamScopedFact(id, teamOf(index), teamOf(index)))
		}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	members := make([]CohortMember, 0, len(ids))
	for index, id := range ids {
		members = append(members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1, InclusionReasons: []string{"matched"},
		})
	}
	synthesisCalls := 0
	options := EngineOptions{MaxItems: 26, SynthesisDeadlineReserve: time.Hour}
	engine, request := groupReadEngineFixtureConfigured(t, &recordingTelemetry{}, recorder, members, nil, SubjectProject, &options, &synthesisCalls, func(config *groupReadFixtureConfig) {
		graph := &config.graph.context
		for index, member := range members {
			team := SubjectRef{Kind: SubjectTeam, CanonicalID: teamOf(index), Label: teamOf(index)}
			ref := fmt.Sprintf("ref_group_path_%02d", index)
			graph.Paths = append(graph.Paths, RelationshipPath{
				PathID: fmt.Sprintf("path_group_member_%02d", index), Nodes: []SubjectRef{team, member.Subject},
				Edges: []RelationshipEdge{{
					Type: contractsv1.ContextFabricRelationshipRelatedTo, From: team, To: member.Subject,
					Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, EvidenceRefIDs: []string{ref},
				}},
				WhyRelevant: "The team owns the project.", EvidenceRefIDs: []string{ref},
			})
			graph.EvidenceRefIDs = append(graph.EvidenceRefIDs, ref)
		}
		declared, served := retryCensusDeclared, len(members)
		reason := KindCensusTruncatedReason(SubjectProject, declared, served)
		detail := CoverageDetail{
			DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
			Degrading: true, Kind: SubjectProject, Declared: &declared, Served: &served, Raw: reason,
		}
		detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
		graph.Coverage.Details = append(graph.Coverage.Details, detail)
		graph.Coverage.DegradedReasons = append(graph.Coverage.DegradedReasons, reason)
	})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want the grouped retry served", err)
	}
	if synthesisCalls != 2 || len(groupReadSynthesisPasses) != 2 {
		t.Fatalf("synthesis calls = %d (passes %d), want the first pass and the one retry", synthesisCalls, len(groupReadSynthesisPasses))
	}
	retry := groupReadSynthesisPasses[1].Graph
	if retry.Cohort == nil || len(retry.Cohort.Groups) < 2 || len(retry.Cohort.Members) >= len(members) {
		t.Fatalf("retry cohort = %+v, want a grouped cohort narrowed below %d members", retry.Cohort, len(members))
	}
	kept := map[string]bool{}
	for _, member := range retry.Cohort.Members {
		kept[member.Subject.CanonicalID] = true
	}
	var failures []string
	for _, group := range retry.Cohort.Groups {
		if len(group.MemberCanonicalIDs) == 0 {
			failures = append(failures, fmt.Sprintf("group %s lists no member", group.Subject.CanonicalID))
		}
		for _, id := range group.MemberCanonicalIDs {
			if !kept[id] {
				failures = append(failures, fmt.Sprintf("group %s lists %s, a member the retry left out", group.Subject.CanonicalID, id))
			}
		}
	}
	for _, path := range retry.Paths {
		for _, node := range path.Nodes {
			if node.Kind == SubjectProject && !kept[node.CanonicalID] {
				failures = append(failures, fmt.Sprintf("path %s names %s, a member the retry left out", path.PathID, node.CanonicalID))
			}
		}
	}
	if len(retry.Paths) != len(kept) {
		failures = append(failures, fmt.Sprintf("paths = %d, want one per kept member (%d)", len(retry.Paths), len(kept)))
	}
	want := KindCensusTruncatedReason(SubjectProject, retryCensusDeclared, len(kept))
	for _, detail := range retry.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && (detail.Raw != want || detail.Served == nil || *detail.Served != len(kept)) {
			failures = append(failures, fmt.Sprintf("census = served %v raw %q, want %d and %q", detail.Served, detail.Raw, len(kept), want))
		}
	}
	if !slices.Contains(retry.Coverage.DegradedReasons, want) {
		failures = append(failures, fmt.Sprintf("degraded reasons %v, want %q", retry.Coverage.DegradedReasons, want))
	}
	if result.Cohort == nil || len(result.Cohort.Members) != len(kept) {
		failures = append(failures, fmt.Sprintf("served cohort %+v, want the %d kept members", result.Cohort, len(kept)))
	}
	if len(failures) > 0 {
		t.Fatalf("the grouped retry's input still describes members it left out:\n%s", strings.Join(failures, "\n"))
	}
}
