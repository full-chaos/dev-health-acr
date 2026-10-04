package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// soleCommitRequest is a deployment-members request whose one committed
// subject carries the given commit basis and no term match: the shape a
// subject named by its label reaches the reader in.
func soleCommitRequest(anchor contextfabric.SubjectRef, basis contextfabric.CommitBasis) contextfabric.GraphDiscoveryRequest {
	request := ownershipRoutingRequest(deploymentMembersFrame(), anchor)
	request.Request.Options.MaxCohortMembers = 50
	request.Resolution.Candidates = nil
	request.Bases = contextfabric.CommitBasisSet{}
	if basis != contextfabric.CommitBasisUnknown {
		request.Bases.Record(anchor, basis)
	}
	return request
}

// TestEveryAnchorTheEngineAdmitsIsTheAnchorTheReaderServes quantifies over the
// anchor kinds the engine admits a deployment cohort under and over every
// commit basis: the one committed subject is the reader's anchor.
func TestEveryAnchorTheEngineAdmitsIsTheAnchorTheReaderServes(t *testing.T) {
	kinds := contextfabric.ScopedOnlyCohortAnchorKindsForAudit(contextfabric.SubjectDeployment)
	if len(kinds) == 0 {
		t.Fatal("no anchor kind is admitted for a deployment cohort: the quantifier is empty")
	}
	bases := []contextfabric.CommitBasis{
		contextfabric.CommitBasisUnknown, contextfabric.CommitBasisStatistical,
		contextfabric.CommitBasisAuthoritativeIdentity, contextfabric.CommitBasisCallerCanonicalID,
	}
	for _, kind := range kinds {
		for _, basis := range bases {
			anchor := contextfabric.SubjectRef{Kind: kind, CanonicalID: string(kind) + ":anchor", Label: "payments"}
			request := soleCommitRequest(anchor, basis)
			if admitted, ok := contextfabric.DeploymentCohortAnchor(request.Resolution.Committed); !ok || admitted != anchor {
				t.Fatalf("kind %s: the engine rule does not admit the one committed subject", kind)
			}
			got, _ := deploymentCohortAnchor(request)
			if got == nil || *got != anchor {
				t.Errorf("kind %s basis %q: reader anchor = %v, want the committed subject the engine admitted", kind, basis, got)
			}
		}
	}
}

func TestDeploymentCohortAnchorPrefersTheBoundSubjectAndRefusesSeveralUnbound(t *testing.T) {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID, Label: "payments"}
	stray := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/stray", Label: "acme/stray"}

	bound := projectDeploymentsRequest()
	bound.Resolution.Committed = []contextfabric.SubjectRef{stray, project}
	if got, basis := deploymentCohortAnchor(bound); got == nil || *got != project || basis != DeploymentAnchorBound {
		t.Errorf("bound project beside a stray repository: anchor = %v basis = %q, want the bound project", got, basis)
	}

	unbound := soleCommitRequest(project, contextfabric.CommitBasisStatistical)
	unbound.Resolution.Committed = []contextfabric.SubjectRef{stray, project}
	if got, basis := deploymentCohortAnchor(unbound); got != nil || basis != DeploymentAnchorNone {
		t.Errorf("two committed subjects, none bound: anchor = %v basis = %q, want none", got, basis)
	}

	sole := soleCommitRequest(project, contextfabric.CommitBasisStatistical)
	if got, basis := deploymentCohortAnchor(sole); got == nil || *got != project || basis != DeploymentAnchorSoleCommit {
		t.Errorf("one statistical commit: anchor = %v basis = %q, want the committed project as the sole commit", got, basis)
	}

	unservable := soleCommitRequest(contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:1", Label: "payments"}, contextfabric.CommitBasisCallerCanonicalID)
	if got, _ := deploymentCohortAnchor(unservable); got != nil {
		t.Errorf("an incident anchored a deployment cohort: %v", got)
	}
}

// TestAProjectNamedByItsLabelRoutesTheWalk is the reader half of the route: a
// project committed on a basis that is not a proven identity still gets the
// project walk, and its members carry the walk's reason.
func TestAProjectNamedByItsLabelRoutesTheWalk(t *testing.T) {
	s := seedProject()
	for _, basis := range []contextfabric.CommitBasis{contextfabric.CommitBasisUnknown, contextfabric.CommitBasisStatistical} {
		telemetry := &recordingTelemetry{}
		adapter := newFakeAdapterWithTelemetry(t, seededGraphConn(s.nodes, s.edges), telemetry)
		anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID, Label: "payments"}
		result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, soleCommitRequest(anchor, basis))
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		if result.Cohort == nil || len(result.Cohort.Members) != len(s.served) {
			t.Fatalf("basis %q: cohort = %+v, want the %d deployments of the linked repositories", basis, result.Cohort, len(s.served))
		}
		for _, member := range result.Cohort.Members {
			if _, linked := s.served[member.Subject.CanonicalID]; !linked {
				t.Errorf("basis %q: member %s is not a deployment of a linked repository", basis, member.Subject.CanonicalID)
			}
			if !reflect.DeepEqual(member.InclusionReasons, []string{projectDeploymentInclusionReason}) {
				t.Errorf("basis %q: member %s inclusion reasons = %q, want the walk reason", basis, member.Subject.CanonicalID, member.InclusionReasons)
			}
		}
		if result.Cohort.Rationale != anchoredDeploymentCohortRationale {
			t.Errorf("basis %q: rationale = %q", basis, result.Cohort.Rationale)
		}
		want := ProjectDeploymentWalkDecision{
			Outcome: ProjectDeploymentWalkMembers, AnchorKind: contextfabric.SubjectProject, AnchorBasis: DeploymentAnchorSoleCommit,
			Committed: 1, Issues: 3, LinkedPullRequests: 3, Members: len(s.served),
		}
		if len(telemetry.projectDeploymentWalks) != 1 {
			t.Fatalf("basis %q: %d walk decisions, want one", basis, len(telemetry.projectDeploymentWalks))
		}
		got := telemetry.projectDeploymentWalks[0]
		if got != want {
			t.Errorf("basis %q: walk decision = %+v, want %+v", basis, got, want)
		}
	}
}

// walkOutcomeFixture is one request and graph that ends on a named outcome.
type walkOutcomeFixture struct {
	principal storage.Principal
	seed      func() ([]seededNode, []seededEdge)
	request   func() contextfabric.GraphDiscoveryRequest
	limit     int
	failRead  bool
	check     func(*testing.T, ProjectDeploymentWalkDecision)
}

func projectWithOneIssue(link bool, deployments int) ([]seededNode, []seededEdge) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	repoID := s.repository("acme/linked", deployments)
	if link {
		s.link("github", "work_item:gh:1", []string{"acme/linked"}, "work_item:ghpr:1", "pr", repoID, "acme/linked", false)
	} else {
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item:gh:1", label: "issue", repos: []string{"acme/linked"}, workItemType: "issue"})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_PROJECT", "work_item", "work_item:gh:1", "project", projectAnchorID})
	}
	return s.nodes, s.edges
}

func walkOutcomeFixtures() map[ProjectDeploymentWalkOutcome]walkOutcomeFixture {
	open := storage.Principal{OrgID: "org-1"}
	return map[ProjectDeploymentWalkOutcome]walkOutcomeFixture{
		// One member is the smallest walk that has members.
		ProjectDeploymentWalkMembers: {
			principal: open, seed: func() ([]seededNode, []seededEdge) { return projectWithOneIssue(true, 1) }, request: projectDeploymentsRequest,
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Members != 1 || d.Issues != 1 || d.LinkedPullRequests != 1 || d.Truncated || d.AnchorBasis != DeploymentAnchorBound {
					t.Errorf("members decision = %+v, want 1 member from 1 issue and 1 link, bound, uncut", d)
				}
			},
		},
		ProjectDeploymentWalkUnlinked: {
			principal: open, seed: func() ([]seededNode, []seededEdge) { return projectWithOneIssue(false, 2) }, request: projectDeploymentsRequest,
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Members != 0 || d.Issues != 1 || d.LinkedPullRequests != 0 {
					t.Errorf("unlinked decision = %+v, want 1 issue and no link", d)
				}
			},
		},
		ProjectDeploymentWalkDenied: {
			principal: storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/somewhere-else"}},
			seed:      func() ([]seededNode, []seededEdge) { return projectWithOneIssue(true, 2) }, request: projectDeploymentsRequest,
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Members != 0 || d.Denied == 0 {
					t.Errorf("denied decision = %+v, want no member and a counted denial", d)
				}
			},
		},
		ProjectDeploymentWalkTruncated: {
			principal: open, limit: 3, request: projectDeploymentsRequest,
			seed: func() ([]seededNode, []seededEdge) { s := linklessThenLinked(6); return s.nodes, s.edges },
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Members != 0 || !d.Truncated {
					t.Errorf("truncated decision = %+v, want no member and a cut frontier", d)
				}
			},
		},
		ProjectDeploymentWalkNoDeployments: {
			principal: open, seed: func() ([]seededNode, []seededEdge) { return projectWithOneIssue(true, 0) }, request: projectDeploymentsRequest,
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Members != 0 || d.LinkedPullRequests != 1 || d.Truncated {
					t.Errorf("no_deployments decision = %+v, want a link and no member, uncut", d)
				}
			},
		},
		ProjectDeploymentWalkReadFailed: {
			principal: open, failRead: true, seed: func() ([]seededNode, []seededEdge) { return projectWithOneIssue(true, 2) }, request: projectDeploymentsRequest,
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Err == nil {
					t.Errorf("read_failed decision = %+v, want the failed read", d)
				}
			},
		},
		ProjectDeploymentWalkNotRouted: {
			principal: open, seed: func() ([]seededNode, []seededEdge) { s := seedParentDeployments("team"); return s.nodes, s.edges },
			request: func() contextfabric.GraphDiscoveryRequest {
				return soleCommitRequest(contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:payments", Label: "payments"}, contextfabric.CommitBasisStatistical)
			},
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.AnchorKind != contextfabric.SubjectTeam || d.AnchorBasis != DeploymentAnchorSoleCommit || d.Members != 0 || d.Issues != 0 {
					t.Errorf("not_routed decision = %+v, want the team anchor and no walk count", d)
				}
			},
		},
	}
}

// runWalkOutcome drives the real DiscoverContext for one fixture and returns
// the decisions the telemetry sink received and the JSON the slog sink wrote.
func runWalkOutcome(t *testing.T, fixture walkOutcomeFixture) ([]ProjectDeploymentWalkDecision, []byte) {
	t.Helper()
	nodes, edges := fixture.seed()
	var out [][]byte
	var decisions []ProjectDeploymentWalkDecision
	for _, sink := range []string{"recording", "slog"} {
		conn := seededGraphConn(nodes, edges)
		if fixture.failRead {
			inner := conn.queryFunc
			conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				if params["fromKind"] != nil {
					return nil, errors.New("connection reset")
				}
				return inner(ctx, key, cypher, params, readOnly)
			}
		}
		var buf bytes.Buffer
		recording := &recordingTelemetry{}
		var telemetry GraphTelemetry = recording
		if sink == "slog" {
			telemetry = SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
		}
		adapter := newFakeAdapterWithTelemetry(t, conn, telemetry)
		if fixture.limit > 0 {
			adapter.config.MaxResults = fixture.limit
		}
		_, err := adapter.DiscoverContext(context.Background(), fixture.principal, fixture.request())
		if (err != nil) != fixture.failRead {
			t.Fatalf("DiscoverContext() error = %v, want an error only for a failed read (%v)", err, fixture.failRead)
		}
		decisions = append(decisions, recording.projectDeploymentWalks...)
		out = append(out, buf.Bytes())
	}
	return decisions, out[1]
}

// TestEveryProjectDeploymentWalkOutcomeIsEmittedByTheRealProducer quantifies
// over the outcome vocabulary: each outcome has a fixture, the real
// DiscoverContext emits exactly one decision with it, and the line the real
// slog sink writes certifies against the declared event.
func TestEveryProjectDeploymentWalkOutcomeIsEmittedByTheRealProducer(t *testing.T) {
	fixtures := walkOutcomeFixtures()
	vocabulary := ProjectDeploymentWalkOutcomeVocabulary()
	if len(fixtures) != len(vocabulary) {
		t.Fatalf("%d fixtures for %d declared outcomes", len(fixtures), len(vocabulary))
	}
	for _, outcome := range vocabulary {
		fixture, ok := fixtures[outcome]
		if !ok {
			t.Fatalf("outcome %q has no fixture", outcome)
		}
		t.Run(string(outcome), func(t *testing.T) {
			decisions, logged := runWalkOutcome(t, fixture)
			if len(decisions) != 1 || decisions[0].Outcome != outcome {
				t.Fatalf("decisions = %+v, want exactly one with outcome %q", decisions, outcome)
			}
			fixture.check(t, decisions[0])

			log, err := certify.Parse(logged)
			if err != nil {
				t.Fatalf("certify.Parse() error = %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.ProjectDeploymentWalk,
				Want:  map[string]any{"org_id": "org-1", "outcome": string(outcome), "committed": 1},
			}); err != nil {
				t.Fatalf("certify.Certify() error = %v", err)
			}
			lines := log.LinesWithMsg(eventspec.ProjectDeploymentWalk.Msg)
			if len(lines) != 1 {
				t.Fatalf("%d lines, want one", len(lines))
			}
			for _, key := range []string{"issues", "linked_pull_requests", "members", "denied", "truncated"} {
				measured := outcome != ProjectDeploymentWalkNotRouted && outcome != ProjectDeploymentWalkReadFailed
				if _, present := lines[0][key]; present != measured {
					t.Errorf("line %v: %s present = %v; a walk count rides only on a walk that finished", lines[0], key, present)
				}
			}
			if _, present := lines[0]["error"]; present != (outcome == ProjectDeploymentWalkReadFailed) {
				t.Errorf("line %v: error present = %v", lines[0], present)
			}
			for key, value := range lines[0] {
				if text, isText := value.(string); isText && key != "error" && (strings.Contains(text, "payments") || strings.Contains(text, "acme/")) {
					t.Errorf("line field %s = %q names a subject; the line carries counts and closed values only", key, text)
				}
			}
		})
	}
}

func TestProjectDeploymentWalkClosedVocabulariesMatchEventspec(t *testing.T) {
	declared := map[string][]string{}
	for _, f := range eventspec.ProjectDeploymentWalk.Fields {
		declared[f.Key] = f.ClosedVocabulary
	}
	var outcomes, bases []string
	for _, o := range ProjectDeploymentWalkOutcomeVocabulary() {
		outcomes = append(outcomes, string(o))
	}
	for _, b := range DeploymentAnchorBasisVocabulary() {
		bases = append(bases, string(b))
	}
	if !reflect.DeepEqual(declared["outcome"], outcomes) {
		t.Errorf("eventspec declares outcome %v, the producer %v", declared["outcome"], outcomes)
	}
	if !reflect.DeepEqual(declared["anchor_basis"], bases) {
		t.Errorf("eventspec declares anchor_basis %v, the producer %v", declared["anchor_basis"], bases)
	}
}

// TestADiscoveryThatAsksNoDeploymentMembersWritesNoWalkLine: the decision line
// belongs to a deployment-members discovery only.
func TestADiscoveryThatAsksNoDeploymentMembersWritesNoWalkLine(t *testing.T) {
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, &fakeConn{}, telemetry)
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/api", Label: "acme/api"}
	for name, request := range map[string]contextfabric.GraphDiscoveryRequest{
		"team members of a repository": ownershipRoutingRequest(repositoryAnchorFrame(), anchor),
		"no frame":                     fakeDiscoveryRequest(anchor, 10),
	} {
		if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
			t.Fatalf("%s: DiscoverContext() error = %v", name, err)
		}
		if len(telemetry.projectDeploymentWalks) != 0 {
			t.Fatalf("%s: walk decisions = %+v, want none", name, telemetry.projectDeploymentWalks)
		}
	}
}

// TestSeveralUnboundCommitsWriteANotRoutedLineWithNoAnchor certifies the line
// of a deployment-members discovery that has no anchor at all.
func TestSeveralUnboundCommitsWriteANotRoutedLineWithNoAnchor(t *testing.T) {
	var buf bytes.Buffer
	adapter := newFakeAdapterWithTelemetry(t, &fakeConn{}, SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))})
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID, Label: "payments"}
	request := soleCommitRequest(project, contextfabric.CommitBasisStatistical)
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/stray", Label: "acme/stray"})
	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.ProjectDeploymentWalk,
		Want:  map[string]any{"org_id": "org-1", "outcome": "not_routed", "anchor_kind": "none", "anchor_basis": "none", "committed": 2},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
}

// TestABoundRepositoryAnchorKeepsAStrayCommitsDeploymentsOut: the reach rule
// holds for a repository or team anchor as it does for a project.
func TestABoundRepositoryAnchorKeepsAStrayCommitsDeploymentsOut(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	anchorRepo := s.repository("acme/anchor", 2)
	strayRepo := s.repository("acme/stray", 1)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: anchorRepo, Label: "payments"}
	request := ownershipRoutingRequest(deploymentMembersFrame(), anchor)
	request.Request.Options.MaxCohortMembers = 50
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: strayRepo, Label: "acme/stray"})
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	var got []string
	if result.Cohort != nil {
		for _, m := range result.Cohort.Members {
			got = append(got, m.Subject.CanonicalID)
		}
	}
	if strings.Join(got, ",") != "deployment:acme/anchor:0,deployment:acme/anchor:1" {
		t.Fatalf("members = %v, want the two deployments of the bound repository only", got)
	}
	anchored := 0
	for _, path := range result.Paths {
		for _, ref := range path.Nodes {
			if ref.CanonicalID == "deployment:acme/stray:0" {
				t.Fatalf("paths carry the stray commit's deployment: %+v", path)
			}
			if ref.Kind == contextfabric.SubjectDeployment {
				anchored++
			}
		}
	}
	if anchored != 2 {
		t.Fatalf("paths carry %d deployments of the anchor, want its 2", anchored)
	}
}

// TestARestrictedCallerWithACutFrontierStillGetsTheNeutralReason: a cut
// frontier does not replace the neutral reason of a restricted caller.
func TestARestrictedCallerWithACutFrontierStillGetsTheNeutralReason(t *testing.T) {
	s := linklessThenLinked(6)
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, seededGraphConn(s.nodes, s.edges), telemetry)
	adapter.config.MaxResults = 3
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/somewhere-else"}}
	result, err := adapter.DiscoverContext(context.Background(), principal, projectDeploymentsRequest())
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.projectDeploymentWalks) != 1 || telemetry.projectDeploymentWalks[0].Outcome != ProjectDeploymentWalkDenied || !telemetry.projectDeploymentWalks[0].Truncated {
		t.Fatalf("walk decisions = %+v, want one denied decision that records the cut", telemetry.projectDeploymentWalks)
	}
	if deniedDetail(result) == nil || unlinkedDetail(result) != nil || cutDetail(result) != nil || len(result.Coverage.Details) != 1 {
		t.Fatalf("details = %+v, want only the neutral denied reason: a truncation detail tells a restricted caller the hidden frontier was larger than the read", result.Coverage.Details)
	}
}

func cutDetail(result contextfabric.GraphContext) *contextfabric.CoverageDetail {
	for i := range result.Coverage.Details {
		if result.Coverage.Details[i].Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
			return &result.Coverage.Details[i]
		}
	}
	return nil
}

// TestAnAnchorWhoseCutReadReachedNoDeploymentIsPartial: a repository or team
// anchor whose two-hop read was cut before it reached a deployment serves no
// cohort, and the answer must say the read was cut.
func TestAnAnchorWhoseCutReadReachedNoDeploymentIsPartial(t *testing.T) {
	for _, kind := range []string{"repository", "team"} {
		t.Run(kind, func(t *testing.T) {
			s := projectSeed{served: map[string]string{}}
			anchorID := kind + ":anchor"
			s.nodes = append(s.nodes, seededNode{kind: kind, id: anchorID, label: "payments", repos: []string{"acme/anchor"}})
			// More issues of the anchor than the read budget, each ranked
			// ahead of the one deployment the anchor reaches.
			for i := 0; i < 6; i++ {
				id := fmt.Sprintf("work_item:gh:%d", i)
				s.nodes = append(s.nodes, seededNode{kind: "work_item", id: id, label: id, repos: []string{"acme/anchor"}, workItemType: "issue"})
				s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "work_item", id, kind, anchorID})
			}
			s.nodes = append(s.nodes, seededNode{kind: "deployment", id: "deployment:anchor:0", label: "deployment", repos: []string{"acme/anchor"}})
			s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", "deployment:anchor:0", kind, anchorID})
			adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
			adapter.config.MaxResults = 3
			anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: anchorID, Label: "payments"}
			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, soleCommitRequest(anchor, contextfabric.CommitBasisStatistical))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if result.Cohort != nil {
				t.Fatalf("cohort = %+v, want none: the fixture must cut the read before the deployment", result.Cohort)
			}
			if !result.Coverage.Partial || cutDetail(result) == nil {
				t.Fatalf("partial = %v, details = %+v, want partial coverage and the truncation detail", result.Coverage.Partial, result.Coverage.Details)
			}
		})
	}
}

// TestARestrictedCallerWithVisibleMembersIsAMembersOutcome: denied is the
// outcome of a restricted caller with NO member, never of one who sees some.
func TestARestrictedCallerWithVisibleMembersIsAMembersOutcome(t *testing.T) {
	s := seedProject()
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, seededGraphConn(s.nodes, s.edges), telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/github-linked", "acme/linear-linked"}}
	if _, err := adapter.DiscoverContext(context.Background(), principal, projectDeploymentsRequest()); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.projectDeploymentWalks) != 1 {
		t.Fatalf("%d walk decisions, want one", len(telemetry.projectDeploymentWalks))
	}
	got := telemetry.projectDeploymentWalks[0]
	if got.Outcome != ProjectDeploymentWalkMembers || got.Members != 4 || got.Denied == 0 {
		t.Fatalf("walk decision = %+v, want outcome members with the 4 granted deployments and the hidden hops counted", got)
	}
}

// anchorWithIssues seeds an anchor of kind with issues issues of its own,
// each ranked ahead of its deployments, and deployments deployments.
func anchorWithIssues(s *projectSeed, kind, anchorID, slug string, issues, deployments int) {
	s.nodes = append(s.nodes, seededNode{kind: kind, id: anchorID, label: anchorID, repos: []string{slug}})
	for i := 0; i < issues; i++ {
		id := fmt.Sprintf("work_item:%s:%d", anchorID, i)
		s.nodes = append(s.nodes, seededNode{kind: "work_item", id: id, label: id, repos: []string{slug}, workItemType: "issue"})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "work_item", id, kind, anchorID})
	}
	for d := 0; d < deployments; d++ {
		id := fmt.Sprintf("deployment:%s:%d", anchorID, d)
		s.nodes = append(s.nodes, seededNode{kind: "deployment", id: id, label: id, repos: []string{slug}})
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", id, kind, anchorID})
	}
}

// TestACutAnchorReadWithMembersCarriesTheCutOnTheCohortOnly: when the cut
// read still reached members, the cohort says it is truncated and no empty
// truncation detail contradicts the members it carries.
func TestACutAnchorReadWithMembersCarriesTheCutOnTheCohortOnly(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	anchorWithIssues(&s, "repository", "repository:anchor", "acme/anchor", 0, 6)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	adapter.config.MaxResults = 3
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:anchor", Label: "payments"}
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, soleCommitRequest(anchor, contextfabric.CommitBasisStatistical))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) == 0 || !result.Cohort.Truncated {
		t.Fatalf("cohort = %+v, want members from the cut read and a truncated cohort", result.Cohort)
	}
	if cutDetail(result) != nil {
		t.Fatalf("details = %+v, want no empty truncation detail beside a cohort that has members", result.Coverage.Details)
	}
}

// TestAStrayCommitsCutReadIsNotTheAnchorsCut: the cut that makes an empty
// anchored cohort partial is the anchor's own, never another committed
// subject's.
func TestAStrayCommitsCutReadIsNotTheAnchorsCut(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	anchorWithIssues(&s, "repository", "repository:anchor", "acme/anchor", 0, 0)
	anchorWithIssues(&s, "repository", "repository:stray", "acme/stray", 6, 0)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	adapter.config.MaxResults = 3
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:anchor", Label: "payments"}
	request := ownershipRoutingRequest(deploymentMembersFrame(), anchor)
	request.Request.Options.MaxCohortMembers = 50
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:stray", Label: "acme/stray"})
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("cohort = %+v, want none: the bound anchor reaches no deployment", result.Cohort)
	}
	if cutDetail(result) != nil {
		t.Fatalf("details = %+v, want no truncation detail: the anchor's own read was not cut", result.Coverage.Details)
	}
}
