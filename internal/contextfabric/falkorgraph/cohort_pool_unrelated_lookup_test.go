package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const unrelatedSubjectID = "project:unrelated"

// withUnrelatedFailingWalk adds a committed subject whose only edge reaches an
// endpoint the backend cannot read.
func withUnrelatedFailingWalk(inner *fakeConn) *fakeConn {
	base := inner.queryFunc
	inner.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "UNION") && params["id"] == unrelatedSubjectID:
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_unrelated", "authorization_repositories": teamWalkAllowed}},
				"srcKind": "project", "srcId": unrelatedSubjectID, "dstKind": "team", "dstId": "team:unreadable",
			}}, nil
		case !strings.Contains(cypher, "UNION") && !strings.Contains(cypher, "UNWIND") && !strings.Contains(cypher, "fulltext"):
			if params["id"] == "team:unreadable" {
				return nil, errors.New("injected backend failure")
			}
			if params["id"] == unrelatedSubjectID {
				return []row{fakeSubjectNodeRow("project", unrelatedSubjectID, "Unrelated")}, nil
			}
		}
		return base(ctx, graphKey, cypher, params, readOnly)
	}
	return inner
}

func TestAnchoredCohortStaysCompleteWhenAnUnrelatedSubjectsLookupFails(t *testing.T) {
	t.Parallel()
	fixtures, _ := teamWalkFixtures()
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, withUnrelatedFailingWalk(fixtures[TeamAnchorWalkMembers]), telemetry)
	request := teamWalkRequest()
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: unrelatedSubjectID, Label: "Unrelated"})

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: teamWalkAllowed}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 10 {
		t.Fatalf("cohort = %+v, want the team's 10 repositories", result.Cohort)
	}
	if result.Cohort.Truncated || !result.Cohort.Complete {
		t.Errorf("Truncated=%v Complete=%v: an unrelated subject's failed lookup cannot have lost a member the team's own read returned",
			result.Cohort.Truncated, result.Cohort.Complete)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial = false: the failed lookup must still be disclosed")
	}
	if len(telemetry.neighborLookupFailures) != 1 {
		t.Fatalf("failure lines = %d, want 1", len(telemetry.neighborLookupFailures))
	}
	if got := telemetry.neighborLookupFailures[0].originCanonicalID; got != unrelatedSubjectID {
		t.Errorf("failure origin = %q, want the unrelated subject %q", got, unrelatedSubjectID)
	}
}

func withUnrelatedSubject(request contextfabric.GraphDiscoveryRequest) contextfabric.GraphDiscoveryRequest {
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: unrelatedSubjectID, Label: "Unrelated"})
	return request
}

func requireCompleteButDisclosed(t *testing.T, result contextfabric.GraphContext, telemetry *recordingTelemetry) {
	t.Helper()
	if result.Cohort == nil || len(result.Cohort.Members) == 0 {
		t.Fatalf("cohort = %+v, want the anchor's members", result.Cohort)
	}
	if result.Cohort.Truncated || !result.Cohort.Complete {
		t.Errorf("Truncated=%v Complete=%v: an unrelated subject's failed lookup cannot have lost an anchor member",
			result.Cohort.Truncated, result.Cohort.Complete)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial = false: the failed lookup must still be disclosed")
	}
	if len(telemetry.neighborLookupFailures) != 1 || telemetry.neighborLookupFailures[0].originCanonicalID != unrelatedSubjectID {
		t.Errorf("failure lines = %+v, want one naming the unrelated subject", telemetry.neighborLookupFailures)
	}
}

func TestOwnershipRoutedCohortStaysCompleteWhenAnUnrelatedSubjectsLookupFails(t *testing.T) {
	t.Parallel()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "$kinds"):
			owner := fakeSubjectNodeRow("team", "team:CHAOS", "Fullchaos")
			owner["n"].(*node).Properties[propAuthzRepos] = []string{"full-chaos/dev-health-acr"}
			return []row{owner}, nil
		}
		return nil, nil
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, withUnrelatedFailingWalk(fake), telemetry)
	request := withUnrelatedSubject(ownershipRoutingRequest(repositoryAnchorFrame(), anchor))

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	requireCompleteButDisclosed(t, result, telemetry)
}

func TestDeploymentAnchoredCohortStaysCompleteWhenAnUnrelatedSubjectsLookupFails(t *testing.T) {
	t.Parallel()
	conn := withUnrelatedFailingWalk(seededGraphConn(seedProject().nodes, seedProject().edges))
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, conn, telemetry)
	request := withUnrelatedSubject(projectDeploymentsRequest())

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	requireCompleteButDisclosed(t, result, telemetry)
}

func TestOrganizationStatusAnchoredCohortStaysCompleteWhenAnUnrelatedSubjectsLookupFails(t *testing.T) {
	t.Parallel()
	result, telemetry := discoverPoolSitePullRequestsWith(t,
		[]poolSitePullRequest{{"pull_request:acme:001", "full-chaos/alpha"}},
		func(inner *fakeConn) *fakeConn {
			failing := withUnrelatedFailingWalk(inner)
			base := failing.queryFunc
			census := organizationPullRequests(3, 0)
			failing.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				if strings.Contains(cypher, "subject_kind = $toKind") {
					return statusCensusRows(census, organizationPullRequestNode, params), nil
				}
				rows, err := base(ctx, graphKey, cypher, params, readOnly)
				for _, r := range rows {
					if e, ok := r["r"].(*edge); ok {
						e.Properties["authorization_repositories"] = []string{"full-chaos/alpha"}
					}
				}
				return rows, err
			}
			return failing
		}, true, "organization:org-1")
	if result.Cohort == nil || len(result.Cohort.Members) == 0 {
		t.Fatalf("cohort = %+v, want the organization's open pull requests", result.Cohort)
	}
	if result.Cohort.Truncated || !result.Cohort.Complete {
		t.Errorf("Truncated=%v Complete=%v: an unrelated subject's failed lookup cannot have lost a pull request the status census returned",
			result.Cohort.Truncated, result.Cohort.Complete)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial = false: the failed lookup must still be disclosed")
	}
	if len(telemetry.neighborLookupFailures) != 1 || telemetry.neighborLookupFailures[0].originCanonicalID != unrelatedSubjectID {
		t.Errorf("failure lines = %+v, want one naming the unrelated subject", telemetry.neighborLookupFailures)
	}
}

const textMatchedSubjectID = "project:textmatch"

// withFailingTextWalk makes the question text match one project whose only
// edge reaches an endpoint the backend cannot read, so the failure happens in
// the text walk and not in any committed subject's walk.
func withFailingTextWalk(inner *fakeConn) *fakeConn {
	base := inner.queryFunc
	inner.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return []row{{"node": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: textMatchedSubjectID, propLabel: "Text match", propAuthzRepos: teamWalkAllowed,
			}}, "score": 1.0}}, nil
		case strings.Contains(cypher, "UNION") && params["id"] == textMatchedSubjectID:
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_text", "authorization_repositories": teamWalkAllowed}},
				"srcKind": "project", "srcId": textMatchedSubjectID, "dstKind": "team", "dstId": "team:unreadable",
			}}, nil
		case !strings.Contains(cypher, "UNION") && !strings.Contains(cypher, "UNWIND"):
			if params["id"] == "team:unreadable" {
				return nil, errors.New("injected backend failure")
			}
			if params["id"] == textMatchedSubjectID {
				return []row{{"n": &node{Properties: map[string]interface{}{
					propKind: "project", propCanonicalID: textMatchedSubjectID, propLabel: "Text match", propAuthzRepos: teamWalkAllowed,
				}}}}, nil
			}
		}
		return base(ctx, graphKey, cypher, params, readOnly)
	}
	return inner
}

func TestAnchoredCohortStaysCompleteWhenATextWalkLookupFails(t *testing.T) {
	t.Parallel()
	fixtures, _ := teamWalkFixtures()
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, withFailingTextWalk(fixtures[TeamAnchorWalkMembers]), telemetry)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: teamWalkAllowed}, teamWalkRequest())
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 10 {
		t.Fatalf("cohort = %+v, want the team's 10 repositories", result.Cohort)
	}
	if result.Cohort.Truncated || !result.Cohort.Complete {
		t.Errorf("Truncated=%v Complete=%v: a text-matched subject's failed lookup cannot have lost a member the team's own read returned",
			result.Cohort.Truncated, result.Cohort.Complete)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial = false: the failed lookup must still be disclosed")
	}
	if len(telemetry.neighborLookupFailures) != 1 {
		t.Errorf("failure lines = %d, want 1", len(telemetry.neighborLookupFailures))
	}
}
