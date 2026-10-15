package devhealthfacts_test

// A team pull request question is served through the real Engine.Investigate
// path over a real ClickHouse and then through answerprojection.Project, the
// choke point every consumer reads. The pull_requests facts are written by the
// production providers and the production scope expander; only graph
// discovery and the model (interpretation and synthesis) are controlled.
//
// The expected evidence references are derived here from the seeded ids with
// the contract's own minting rule, never read back from the served answer, so
// a projection that drops or rewrites a cited reference fails these rows even
// when the synthesizer cites it.

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	servedPRTeamID    = "SERVEDPR-PLAT"
	servedPROpenCount = 3
	servedPRMerged    = 2
)

type servedPRGraph struct {
	t       *testing.T
	members []contextfabric.SubjectRef
}

func (*servedPRGraph) AuthorizeStoredSubjects(_ context.Context, p storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcome := contextfabric.StoredSubjectDenied
	if slices.Contains(p.RepositoryScopes, "acme/allowed") {
		outcome = contextfabric.StoredSubjectAdmitted
	}
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range outcomes {
		outcomes[i] = outcome
	}
	return outcomes, nil
}

func (*servedPRGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "served-pr-proof", Epoch: 1}, nil
}

func (g *servedPRGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Platform", graphrank.CandidateNode{UUID: "team-node", Name: "Platform", Attributes: map[string]interface{}{"subject_kind": "team", "canonical_id": teamSubject(servedPRTeamID).CanonicalID, "label": "Platform", "authorization_repositories": []string{"acme/allowed"}, "authorization_projects": "*", "authorization_teams": "*"}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
	if !ok {
		g.t.Fatal("NodeCandidate rejected the scope anchor")
	}
	candidate.State = contextfabric.ResolutionCommitted
	candidate.ReceiptID = "receipt_served_pr_proof"
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{candidate.Subject}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (g *servedPRGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	var cohort *contextfabric.Cohort
	if len(g.members) > 0 {
		members := make([]contextfabric.CohortMember, 0, len(g.members))
		for i, subject := range g.members {
			members = append(members, contextfabric.CohortMember{Subject: subject, Rank: i + 1, InclusionReasons: []string{"matched"}})
		}
		cohort = &contextfabric.Cohort{Kind: contextfabric.SubjectPullRequest, Rationale: "open pull requests of the anchor team", Members: members, Complete: true}
	}
	return contextfabric.GraphContext{
		Cohort: cohort,
		Paths:  []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type servedPRModel struct {
	frame *contextfabric.QuestionFrame
	// claimKind and claimField choose the one fact the draft restates.
	claimSubjectKind contextfabric.SubjectKind
	claimField       string
	facts            contextfabric.CanonicalFactBundle
	draftErr         error
}

func (m *servedPRModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFrame = m.frame
	shape := contextfabric.ShapeSingleSubject
	receipt.QuestionFamily = contextfabric.QuestionFamilySubjectInvestigation
	if scoped := m.frame.SubjectExpression.Scoped; scoped != nil {
		shape = contextfabric.ShapeDiscoveredCohort
		receipt.QuestionFamily = contextfabric.QuestionFamilyScopedCohortStatus
		receipt.ScopeAnchorKind = contextfabric.SubjectTeam
		receipt.ScopeAnchorTerm = "Platform"
		receipt.RequestedSubjectKind = scoped.MemberKind
	}
	return contextfabric.InterpretedQuestion{Shape: shape, RequestedJudgment: "pull_requests", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Platform"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}}}, receipt, nil
}

func (m *servedPRModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	m.facts = input.Facts
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: "Pull requests of the team.", CurrentState: "Pull requests of the team.", DeterministicAnswer: "Pull requests of the team.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	for _, fact := range input.Facts.Facts {
		if fact.Kind != contextfabric.FactPullRequests || fact.Subject.Kind != m.claimSubjectKind {
			continue
		}
		value := fact.Fields[m.claimField].String
		if value == nil {
			continue
		}
		draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: "claim_served_pr_0001", Kind: fact.Kind, Subject: fact.Subject, Field: m.claimField, Value: contextfabric.ScalarValue{String: value}})
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, fact.EvidenceRefIDs...)
		draft.Drivers = append(draft.Drivers, contextfabric.DriverJudgment{
			DriverID: "driver_served_pr_01", Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Pull requests", Summary: "Pull request state of the team.",
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: []string{"claim_served_pr_0001"},
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		})
		break
	}
	m.draftErr = draft.ValidateAgainst(input)
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

func seedServedPRTeam(t *testing.T, ctx context.Context, orgID string) (repo string) {
	t.Helper()
	query, direct := sharedClickHouseFixture(t)
	_ = query
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "projects", "team_repo_ownership") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	// devhealthschema:not-a-production-replica rows are inserted into tables whose schema comes only from devhealthschema.DDL, never defined here
	at := time.Now().UTC().Truncate(time.Second)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	repo = repoUUID("served-pr-" + servedPRTeamID)
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo, orgID, "acme/allowed", "github", at)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", servedPRTeamID, repo, "acme/allowed", "exact", "native", uint8(1), uint16(1), int32(1), at.Add(-time.Hour), nil, at)
	for number := 1; number <= servedPROpenCount; number++ {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			repo, orgID, uint32(number), "open pr", "open", at, at, "feat/x", "")
	}
	for number := servedPROpenCount + 1; number <= servedPROpenCount+servedPRMerged; number++ {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repo, orgID, uint32(number), "merged pr", "merged", at, at, at, at, "feat/y", "")
	}
	return repo
}

func serveTeamPullRequestQuestion(t *testing.T, ctx context.Context, members []contextfabric.SubjectRef, frame *contextfabric.QuestionFrame, claimSubjectKind contextfabric.SubjectKind, claimField, question string) (contextfabric.InvestigationResult, *servedPRModel, string) {
	t.Helper()
	orgID := sharedTestOrgID(t)
	repo := seedServedPRTeam(t, ctx, orgID)
	query, _ := sharedClickHouseFixture(t)
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatal(err)
	}
	model := &servedPRModel{frame: frame, claimSubjectKind: claimSubjectKind, claimField: claimField}
	at := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        &servedPRGraph{t: t, members: members},
		Facts:        registry,
		Requirements: registry,
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "served-pr-proof", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:      memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "served-pr-proof", Now: func() time.Time { return at }, NewResultID: func() string { return "result_served_pr_proof" }})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_served_pr_proof", Question: question, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/allowed"}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "served-pr-proof", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	return result, model, repo
}

func servedPRFrame(memberKind contextfabric.SubjectKind, qualifier contextfabric.MemberQualifier, value string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: memberKind, MemberQualifier: qualifier, MemberQualifierValue: value},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
	}
}

// An aggregate question about the team's pull requests is answered from the
// team fact alone: the served projection cites the team evidence reference,
// carries the claim on the team subject, and no pull request is served.
func TestServedAnswerToATeamAggregatePullRequestQuestionCitesTheTeamEvidenceRef(t *testing.T) {
	ctx := context.Background()
	result, model, _ := serveTeamPullRequestQuestion(t, ctx, nil,
		&contextfabric.QuestionFrame{
			Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalCountOrAggregate},
			SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"Platform"}}},
			Temporal:          contextfabric.TemporalIntentCurrent,
		},
		contextfabric.SubjectTeam, "pull_requests_by_state_basis",
		"How are the pull requests of the Platform team doing?")
	if result.Status != contextfabric.InvestigationComplete {
		t.Fatalf("status = %s, want complete (limitations %v, warnings %v, draft validation %v)", result.Status, result.Limitations, result.Warnings, model.draftErr)
	}
	teamRef := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, servedPRTeamID)

	teamFacts := 0
	for _, fact := range model.facts.Facts {
		if fact.Subject.Kind == contextfabric.SubjectPullRequest {
			t.Fatalf("a per pull request fact reached the synthesis input of an aggregate question: %+v", fact.Subject)
		}
		if fact.Kind == contextfabric.FactPullRequests && fact.Subject.Kind == contextfabric.SubjectTeam {
			teamFacts++
			if fact.Fields["pull_requests_by_state"].Rows == nil {
				t.Fatalf("the team fact carries no pull_requests_by_state table: %+v", fact.Fields)
			}
			if !slices.Contains(fact.EvidenceRefIDs, teamRef) {
				t.Fatalf("the team fact cites %v, want %s", fact.EvidenceRefIDs, teamRef)
			}
		}
	}
	if teamFacts != 1 {
		t.Fatalf("team pull_requests facts at the synthesis input = %d, want 1", teamFacts)
	}

	projection := answerprojection.Project(result, answerprojection.DefaultBudget)
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection does not validate: %v", err)
	}
	if !slices.Contains(projection.EvidenceRefIDs, teamRef) {
		t.Fatalf("served projection evidence refs = %v, want it to cite the team ref %s", projection.EvidenceRefIDs, teamRef)
	}
	cited := false
	for _, driver := range projection.PrincipalDrivers {
		cited = cited || slices.Contains(driver.EvidenceRefIDs, teamRef)
	}
	if !cited {
		t.Fatalf("no served driver cites the team ref %s: %+v", teamRef, projection.PrincipalDrivers)
	}
	claimed := false
	for _, fact := range projection.KeyFacts {
		if fact.Subject.Kind == contractsv1.ContextFabricSubjectKind(contextfabric.SubjectPullRequest) {
			t.Fatalf("an aggregate answer served a pull request fact: %+v", fact)
		}
		claimed = claimed || (fact.Kind == contractsv1.ContextFabricFactKind(contextfabric.FactPullRequests) && fact.Subject.Kind == contractsv1.ContextFabricSubjectKind(contextfabric.SubjectTeam))
	}
	if !claimed {
		t.Fatalf("the served key facts carry no team pull_requests claim: %+v", projection.KeyFacts)
	}
}

// A status question (which pull requests of the team are open) reads the pull
// requests themselves: the synthesis input carries one fact per open pull
// request, and the served projection cites the evidence reference of the pull
// request it restates, derived from the seeded repository and number.
func TestServedAnswerToATeamPullRequestStatusQuestionCitesThePullRequestEvidenceRef(t *testing.T) {
	ctx := context.Background()
	repo := repoUUID("served-pr-" + servedPRTeamID)
	var open []contextfabric.SubjectRef
	for number := 1; number <= servedPROpenCount; number++ {
		open = append(open, pullRequestSubject(repo, strconv.Itoa(number)))
	}
	result, model, _ := serveTeamPullRequestQuestion(t, ctx, open,
		servedPRFrame(contextfabric.SubjectPullRequest, contextfabric.MemberQualifierStatus, contextfabric.PullRequestStatusOpen),
		contextfabric.SubjectPullRequest, "state",
		"Which pull requests of the Platform team are open?")
	if result.Status != contextfabric.InvestigationComplete {
		t.Fatalf("status = %s, want complete (limitations %v, warnings %v, draft validation %v)", result.Status, result.Limitations, result.Warnings, model.draftErr)
	}
	wantRefs := map[string]bool{}
	for number := 1; number <= servedPROpenCount; number++ {
		wantRefs[contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, repo+":"+strconv.Itoa(number))] = false
	}
	for _, fact := range model.facts.Facts {
		if fact.Kind != contextfabric.FactPullRequests || fact.Subject.Kind != contextfabric.SubjectPullRequest {
			continue
		}
		for _, ref := range fact.EvidenceRefIDs {
			if _, open := wantRefs[ref]; open {
				wantRefs[ref] = true
			}
		}
	}
	for ref, served := range wantRefs {
		if !served {
			t.Fatalf("the synthesis input of a status question carries no fact citing the open pull request %s", ref)
		}
	}

	projection := answerprojection.Project(result, answerprojection.DefaultBudget)
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection does not validate: %v", err)
	}
	restated := false
	for _, fact := range projection.KeyFacts {
		if fact.Kind != contractsv1.ContextFabricFactKind(contextfabric.FactPullRequests) || fact.Subject.Kind != contractsv1.ContextFabricSubjectKind(contextfabric.SubjectPullRequest) {
			continue
		}
		restated = true
		if fact.Field != "state" || fact.Value.String == nil || *fact.Value.String != "open" {
			t.Fatalf("the served pull request claim is %+v, want state open", fact)
		}
	}
	if !restated {
		t.Fatalf("the served key facts carry no pull request claim: %+v", projection.KeyFacts)
	}
	cited := false
	for ref := range wantRefs {
		cited = cited || slices.Contains(projection.EvidenceRefIDs, ref)
	}
	if !cited {
		t.Fatalf("served projection evidence refs = %v, want it to cite an open pull request ref of %v", projection.EvidenceRefIDs, wantRefs)
	}
}
