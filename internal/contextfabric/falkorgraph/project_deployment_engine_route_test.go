package falkorgraph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The arms below drive a real Engine over the real Adapter, resolver included:
// the project is committed by its NAME, the way a hosted question commits it,
// never by a hand-set commit basis.

const (
	routeProjectAlpha = "project:alpha"
	routeProjectBravo = "project:bravo"
	// routeWalkMessage is the decision line of the project deployment walk.
	routeWalkMessage = "context_fabric: project deployment walk"
	// routeTeamReason is the inclusion reason of a deployment of a repository
	// the named team owns.
	routeTeamReason = "Deployment of a repository the named team owns."
	// routeReachReason is the inclusion reason of a deployment of a named
	// repository.
	routeReachReason = "Graph retrieval reached this deployment from the anchor the question names."
	// routeOwnershipMessage is the decision line of ownership routing.
	routeOwnershipMessage = "context_fabric: ownership routing"
	// routeWalkReason is the inclusion reason of a member the walk reached.
	routeWalkReason = "Deployment of a repository that a pull request linked to an issue of the named project belongs to."
)

// routeSeed is two projects, each with one issue linked to one pull request in
// its own repository, and two deployments per repository.
type routeSeed struct {
	nodes []seededNode
	edges []seededEdge
	// text is the indexed search text per node key.
	text map[string]string
	// reach is the ownership reach of a project, per project id.
	reach map[string][]string
	// deployments are the deployment ids per project id.
	deployments map[string][]string
}

func seedTwoLinkedProjects() routeSeed {
	s := routeSeed{text: map[string]string{}, reach: map[string][]string{}, deployments: map[string][]string{}}
	for _, name := range []string{"alpha", "bravo"} {
		projectID := "project:" + name
		slug := "acme/" + name + "-service"
		repoID := "repository:github:" + slug
		issueID := "work_item:linear:" + name + "-1"
		prID := "pull_request:ghpr:" + slug + "#1"
		s.nodes = append(s.nodes,
			seededNode{kind: "project", id: projectID, label: name},
			seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}},
			seededNode{kind: "work_item", id: issueID, label: issueID, repos: []string{noRepositoryScope}, workItemType: "issue"},
			seededNode{kind: "pull_request", id: prID, label: prID, repos: []string{slug}})
		s.text["project|"+projectID] = name
		s.edges = append(s.edges,
			seededEdge{"BELONGS_TO_PROJECT", "work_item", issueID, "project", projectID, ""},
			linkEdge(issueID, prID, "native"),
			seededEdge{"BELONGS_TO_REPOSITORY", "pull_request", prID, "repository", repoID, ""})
		for d := 0; d < 2; d++ {
			depID := fmt.Sprintf("deployment:%s:%d", slug, d)
			s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
			s.text["deployment|"+depID] = "deployments production"
			s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID, ""})
			s.deployments[projectID] = append(s.deployments[projectID], depID)
		}
	}
	return s
}

// conn answers the walk steps, node and edge reads from the topology, the
// full-text index from each node's search text, the project reach read and
// the stored-subject read of the committed-root recheck.
func (s routeSeed) conn() *fakeConn {
	base := seededGraphConn(s.nodes, s.edges)
	return &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case params["owned"] != nil:
			var rows []row
			for id, repos := range s.reach {
				rows = append(rows, row{"id": id, "repos": repos})
			}
			return rows, nil
		case isTeamCensus(params):
			var rows []row
			for _, n := range s.nodes {
				if n.kind != "team" {
					continue
				}
				r := fakeSubjectNodeRow(n.kind, n.id, n.label)
				r["n"].(*node).Properties[propAuthzRepos] = n.repos
				rows = append(rows, r)
			}
			return rows, nil
		case params["targets"] != nil:
			targets, _ := params["targets"].([]interface{})
			var rows []row
			for _, target := range targets {
				want, _ := target.(map[string]interface{})
				for _, n := range s.nodes {
					if n.kind != want["kind"] || n.id != want["id"] {
						continue
					}
					r := fakeSubjectNodeRow(n.kind, n.id, n.label)
					if len(n.repos) > 0 {
						r["n"].(*node).Properties[propAuthzRepos] = n.repos
					}
					rows = append(rows, r)
				}
			}
			return wildcardProjects(rows), nil
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			query, _ := params["query"].(string)
			tokens := map[string]bool{}
			for _, token := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') }) {
				tokens[token] = true
			}
			kind, _ := params["kind"].(string)
			var rows []row
			for _, n := range s.nodes {
				text, indexed := s.text[n.kind+"|"+n.id]
				if !indexed || (kind != "" && n.kind != kind) {
					continue
				}
				matched := false
				for _, word := range strings.Fields(text) {
					matched = matched || tokens[word]
				}
				if !matched {
					continue
				}
				score := 1.0
				r := fulltextRow(n.kind, n.id, n.label, text, &score)
				r["node"].(*node).Properties[propAuthzRepos] = n.repos
				rows = append(rows, r)
			}
			return wildcardProjects(rows), nil
		}
		rows, err := base.queryFunc(ctx, key, cypher, params, readOnly)
		return wildcardProjects(rows), err
	}}
}

// isTeamCensus reports the term-free read of the team kind alone, the read
// ownership routing makes.
func isTeamCensus(params map[string]interface{}) bool {
	kinds, ok := params["kinds"].([]string)
	return ok && len(kinds) == 1 && kinds[0] == "team"
}

// wildcardProjects stamps every project node with the "*" authorization the
// projection writes, which a restricted caller's read replaces by the
// project's ownership reach.
func wildcardProjects(rows []row) []row {
	for _, r := range rows {
		for _, value := range r {
			if n, ok := value.(*node); ok && n != nil && n.Properties[propKind] == "project" {
				n.Properties[propAuthzRepos] = "*"
			}
		}
	}
	return rows
}

// projectDeploymentsInterpreter returns the reading of "which deployments
// belong to project <name>": the deployment members of a named project.
type projectDeploymentsInterpreter struct {
	name string
	// kind is the declared anchor kind; empty reads as project.
	kind contextfabric.SubjectKind
	// member is the member kind the question asks for; empty reads as
	// deployment.
	member contextfabric.SubjectKind
	// count makes the question a count ("how many"), not a listing.
	count bool
}

func (i projectDeploymentsInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	anchorKind := i.kind
	if anchorKind == "" {
		anchorKind = contextfabric.SubjectProject
	}
	member := i.member
	if member == "" {
		member = contextfabric.SubjectDeployment
	}
	goal := contextfabric.GoalAssessState
	if i.count {
		goal = contextfabric.GoalCountOrAggregate
	}
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{goal},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{i.name}, MemberKind: member},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: string(member),
		SubjectTerms:     []string{i.name},
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{},
	}, contextfabric.QuestionFamilyOutcome{
		Frame: &frame, FrameObligations: frame.Obligations,
		Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel,
		Gate:          contextfabric.DecideFrameGate(contextfabric.ValidateFrame(frame, nil, ""), true),
		WinningSample: contextfabric.FamilySample{ScopeAnchorKind: anchorKind, ScopeAnchorTerm: i.name},
	}, nil
}

// routeBasisRecorder keeps the commit basis the real resolver returned.
type routeBasisRecorder struct {
	*Adapter
	bases     contextfabric.CommitBasisSet
	committed []contextfabric.SubjectRef
}

func (r *routeBasisRecorder) ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, binding contextfabric.ResolvedGraphBinding, confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection, frame *contextfabric.QuestionFrame, scopeAnchorKind contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	resolution, offers, bases, digests, err := r.Adapter.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
	r.bases, r.committed = bases, append([]contextfabric.SubjectRef(nil), resolution.Committed...)
	return resolution, offers, bases, digests, err
}

// routeNoModelRuntime fails the test if a model call is made.
type routeNoModelRuntime struct{ t *testing.T }

func (r routeNoModelRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	r.t.Error("the interpretation model was called")
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

func (r routeNoModelRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	r.t.Error("the synthesis model was called for a client-synthesis turn")
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

type routeAnswer struct {
	result contextfabric.InvestigationResult
	// basis is the commit basis of the one committed subject.
	basis contextfabric.CommitBasis
	// committed is what the resolver committed, before any retraction.
	committed []contextfabric.SubjectRef
	// walkLines are the decoded decision lines of the walk.
	walkLines []map[string]any
}

// askProjectDeployments runs "which deployments belong to project <name>"
// through Engine.Investigate with the real adapter as its graph reader.
func askProjectDeployments(t *testing.T, s routeSeed, principal storage.Principal, name string) routeAnswer {
	t.Helper()
	return askAnchorDeployments(t, s, principal, contextfabric.SubjectProject, name)
}

// askAnchorDeployments is the same question over an anchor of any kind.
func askAnchorDeployments(t *testing.T, s routeSeed, principal storage.Principal, kind contextfabric.SubjectKind, name string) routeAnswer {
	t.Helper()
	return investigateAnchorDeployments(t, newFakeAdapter(t, s.conn()), principal, kind, name, "which deployments belong to "+string(kind)+" "+name)
}

// investigateAnchorDeployments runs one deployment-members question through
// Engine.Investigate with adapter as the graph reader, and returns the served
// document beside the commit the resolver made and the walk's decision lines.
func investigateAnchorDeployments(t *testing.T, adapter *Adapter, principal storage.Principal, kind contextfabric.SubjectKind, name, question string) routeAnswer {
	t.Helper()
	return investigateAnchorMembers(t, adapter, principal, contextfabric.SubjectDeployment, kind, name, question, routeWalkMessage)
}

// investigateAnchorMembers is the same drive for any member kind; lineMessage
// names the decision line it collects.
func investigateAnchorMembers(t *testing.T, adapter *Adapter, principal storage.Principal, member, kind contextfabric.SubjectKind, name, question, lineMessage string) routeAnswer {
	t.Helper()
	return investigateAnchor(t, adapter, principal, projectDeploymentsInterpreter{name: name, kind: kind, member: member}, question, lineMessage)
}

// investigateAnchor drives Engine.Investigate with the given interpreter.
func investigateAnchor(t *testing.T, adapter *Adapter, principal storage.Principal, interpreter projectDeploymentsInterpreter, question, lineMessage string) routeAnswer {
	t.Helper()
	return investigateAnchorReusing(t, adapter, principal, interpreter, question, lineMessage, nil)
}

// investigateAnchorReusing is investigateAnchor with answer reuse on, over the
// given store, when it is not nil.
func investigateAnchorReusing(t *testing.T, adapter *Adapter, principal storage.Principal, interpreter projectDeploymentsInterpreter, question, lineMessage string, store *routeStore) routeAnswer {
	t.Helper()
	var logs bytes.Buffer
	adapter.config.Telemetry = SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	graph := &routeBasisRecorder{Adapter: adapter}
	deps := contextfabric.EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		Facts:       emptyFactReader{},
		// The shipped synthesizer in the mode a hosted client that writes its
		// own answer uses: the served document is assembled by the service
		// and no model is called.
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{
			Runtime:         routeNoModelRuntime{t: t},
			Options:         contextfabric.RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
			ClientSynthesis: synthesisprompt.ClientAssembly(),
		},
		Results:      discardingResultStore{},
		Requirements: productionRequirementDeriver{},
	}
	synthesisMode := contextfabric.SynthesisModeClient
	if store != nil {
		// A client-written answer is never stored as reusable, so a reuse
		// drive uses the service-written answer.
		deps.Results, deps.ReuseGate, deps.Synthesizer = store, store, countingSynthesizer{}
		synthesisMode = ""
	}
	engine, err := contextfabric.NewEngine(deps, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_83000001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	ctx, _ := contextfabric.WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, principal, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_83000001",
		Question:      question,
		SynthesisMode: synthesisMode,
		TimeContext: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	})
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	answer := routeAnswer{result: result, committed: graph.committed}
	if len(graph.committed) == 1 {
		answer.basis = graph.bases.For(graph.committed[0])
	}
	scanner := bufio.NewScanner(&logs)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) == nil && line["msg"] == lineMessage {
			answer.walkLines = append(answer.walkLines, line)
		}
	}
	return answer
}

func (a routeAnswer) members() []string {
	var ids []string
	if a.result.Cohort != nil {
		for _, m := range a.result.Cohort.Members {
			ids = append(ids, m.Subject.CanonicalID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (a routeAnswer) detail(code contractsv1.ContextFabricCoverageDetailCode) *contextfabric.CoverageDetail {
	for i := range a.result.Coverage.Details {
		if a.result.Coverage.Details[i].Code == code {
			return &a.result.Coverage.Details[i]
		}
	}
	return nil
}

// requireNameCommit proves the fixture reaches the route under test: the
// resolver committed the named project, on a basis that is not a proven
// identity, which is the basis a project named by its label always gets.
func requireNameCommit(t *testing.T, answer routeAnswer, projectID string) {
	t.Helper()
	if len(answer.committed) != 1 || answer.committed[0].Kind != contextfabric.SubjectProject || answer.committed[0].CanonicalID != projectID {
		t.Fatalf("resolver committed %+v, want exactly the named project %s", answer.committed, projectID)
	}
	if answer.basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("commit basis = %q, want %q: a project named by its label commits on the exact-label tier", answer.basis, contextfabric.CommitBasisStatistical)
	}
}

func TestNamedProjectServesItsOwnDeploymentsThroughTheEngine(t *testing.T) {
	s := seedTwoLinkedProjects()
	principal := storage.Principal{OrgID: "org-1"}
	answers := map[string]routeAnswer{}
	for _, name := range []string{"alpha", "bravo"} {
		projectID := "project:" + name
		answer := askProjectDeployments(t, s, principal, name)
		requireNameCommit(t, answer, projectID)
		answers[projectID] = answer

		want := append([]string(nil), s.deployments[projectID]...)
		sort.Strings(want)
		if got := answer.members(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("project %s served %v, want exactly its own deployments %v", name, got, want)
		}
		if answer.result.Cohort.Kind != contextfabric.SubjectDeployment || !answer.result.Cohort.Complete {
			t.Errorf("project %s cohort kind=%q complete=%v, want a complete deployment cohort", name, answer.result.Cohort.Kind, answer.result.Cohort.Complete)
		}
		for _, member := range answer.result.Cohort.Members {
			if len(member.InclusionReasons) != 1 || member.InclusionReasons[0] != routeWalkReason {
				t.Errorf("project %s member %s inclusion reasons = %q, want the walk's own reason", name, member.Subject.CanonicalID, member.InclusionReasons)
			}
		}
		if len(answer.walkLines) != 1 {
			t.Fatalf("project %s: %d walk decision lines, want exactly one", name, len(answer.walkLines))
		}
		line := answer.walkLines[0]
		if line["outcome"] != "members" || line["members"] != float64(2) || line["issues"] != float64(1) || line["linked_pull_requests"] != float64(1) || line["truncated"] != false {
			t.Errorf("project %s walk line = %v, want outcome=members members=2 issues=1 linked_pull_requests=1 truncated=false", name, line)
		}
		for key, value := range line {
			if text, ok := value.(string); ok && key != "msg" && (strings.Contains(text, name) || strings.Contains(text, "acme/")) {
				t.Errorf("walk line field %s = %q names a subject; the line carries counts only", key, text)
			}
		}
	}
	if alpha, bravo := answers[routeProjectAlpha].members(), answers[routeProjectBravo].members(); strings.Join(alpha, ",") == strings.Join(bravo, ",") {
		t.Fatalf("two different projects served the same deployments %v", alpha)
	}
}

func TestRestrictedCallerOfANamedProjectGetsTheNeutralReasonThroughTheEngine(t *testing.T) {
	s := seedTwoLinkedProjects()
	// The caller sees the project through its ownership reach and is granted
	// a repository that holds deployments but that the project does not reach.
	s.reach[routeProjectAlpha] = []string{"acme/bravo-service"}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/bravo-service"}}
	answer := askProjectDeployments(t, s, principal, "alpha")
	requireNameCommit(t, answer, routeProjectAlpha)

	if got := answer.members(); len(got) != 0 {
		t.Fatalf("restricted caller was served %v for a project whose linked repository it is not granted", got)
	}
	denied := answer.detail(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization)
	if denied == nil || denied.Count == nil || *denied.Count != 0 {
		t.Fatalf("coverage details = %+v, want the neutral denied reason with no count", answer.result.Coverage.Details)
	}
	if answer.detail(contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked) != nil {
		t.Fatal("a restricted caller must not learn whether the project links a pull request")
	}
	if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "denied" {
		t.Fatalf("walk lines = %v, want one line with outcome=denied", answer.walkLines)
	}
}

// teamOwningAlpha adds a team that owns the alpha repository only.
func (s *routeSeed) teamOwningAlpha() {
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:tango", label: "tango", repos: []string{"acme/alpha-service"}})
	s.text["team|team:tango"] = "tango"
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", "repository:github:acme/alpha-service", "team", "team:tango", ""})
}

func TestNamedTeamServesOnlyTheDeploymentsItReachesThroughTheEngine(t *testing.T) {
	s := seedTwoLinkedProjects()
	s.teamOwningAlpha()
	// A repository the team does not own matches the question text.
	s.text["repository|repository:github:acme/bravo-service"] = "belong"
	answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectTeam, "tango")
	if len(answer.committed) != 1 || answer.committed[0].CanonicalID != "team:tango" || answer.basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("resolver committed %+v on basis %q, want the named team on the exact-label tier", answer.committed, answer.basis)
	}
	want := append([]string(nil), s.deployments[routeProjectAlpha]...)
	sort.Strings(want)
	if got := answer.members(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("team served %v, want only the deployments it reaches %v: a deployment the question text matched is not a member", got, want)
	}
	for _, member := range answer.result.Cohort.Members {
		if len(member.InclusionReasons) != 1 || member.InclusionReasons[0] != routeTeamReason {
			t.Errorf("member %s inclusion reasons = %q, want the team reason", member.Subject.CanonicalID, member.InclusionReasons)
		}
	}
	reached := map[string]bool{}
	for _, path := range answer.result.Paths {
		for _, ref := range path.Nodes {
			if ref.Kind == contextfabric.SubjectDeployment {
				reached[ref.CanonicalID] = true
			}
		}
	}
	for _, id := range s.deployments[routeProjectBravo] {
		if reached[id] {
			t.Errorf("paths carry %s, a deployment of a repository the team does not own", id)
		}
	}
	for _, id := range want {
		if !reached[id] {
			t.Errorf("paths do not carry %s, a deployment the team reaches", id)
		}
	}
	if len(answer.walkLines) != 1 {
		t.Fatalf("%d walk decision lines, want exactly one", len(answer.walkLines))
	}
	line := answer.walkLines[0]
	if line["outcome"] != "members" || line["anchor_kind"] != "team" || line["anchor_basis"] != "sole_commit" || line["members"] != float64(len(want)) {
		t.Errorf("walk line = %v, want outcome=members anchor_kind=team anchor_basis=sole_commit members=%d", line, len(want))
	}
	for _, key := range []string{"issues", "linked_pull_requests"} {
		if _, measured := line[key]; measured {
			t.Errorf("walk line %v carries %s for a team anchor, which reads no issue", line, key)
		}
	}
}

// crowdLexicalArm adds more deployments of a third repository than one
// full-text read returns, each matching the question text.
func (s *routeSeed) crowdLexicalArm(count int) {
	slug := "acme/crowd-service"
	repoID := "repository:github:" + slug
	s.nodes = append(s.nodes, seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}})
	for d := 0; d < count; d++ {
		depID := fmt.Sprintf("deployment:%s:%02d", slug, d)
		s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
		s.text["deployment|"+depID] = "deployments production"
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID, ""})
	}
}

func TestACutLexicalArmDoesNotTruncateANamedAnchorsDeployments(t *testing.T) {
	for _, anchor := range []struct {
		kind contextfabric.SubjectKind
		name string
	}{{contextfabric.SubjectProject, "alpha"}, {contextfabric.SubjectTeam, "tango"}} {
		t.Run(string(anchor.kind), func(t *testing.T) {
			s := seedTwoLinkedProjects()
			s.teamOwningAlpha()
			// More lexical matches than the adapter's read bound of 25.
			s.crowdLexicalArm(40)
			answer := askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, anchor.kind, anchor.name)
			want := append([]string(nil), s.deployments[routeProjectAlpha]...)
			sort.Strings(want)
			if got := answer.members(); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("served %v, want %v", got, want)
			}
			if !answer.result.Cohort.Complete || answer.result.Cohort.Truncated {
				t.Fatalf("cohort complete=%v truncated=%v, want a complete cohort: a cut lexical arm adds no member to an anchored deployment cohort and removes none", answer.result.Cohort.Complete, answer.result.Cohort.Truncated)
			}
		})
	}
}
