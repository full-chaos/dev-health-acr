package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
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
		ProjectDeploymentWalkMembers: {
			principal: open, seed: func() ([]seededNode, []seededEdge) { return projectWithOneIssue(true, 2) }, request: projectDeploymentsRequest,
			check: func(t *testing.T, d ProjectDeploymentWalkDecision) {
				if d.Members != 2 || d.Issues != 1 || d.LinkedPullRequests != 1 || d.Truncated || d.AnchorBasis != DeploymentAnchorBound {
					t.Errorf("members decision = %+v, want 2 members from 1 issue and 1 link, bound, uncut", d)
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
				if _, present := lines[0][key]; present != (outcome != ProjectDeploymentWalkNotRouted) {
					t.Errorf("line %v: %s present = %v; a walk count rides only on a walk that ran", lines[0], key, present)
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
