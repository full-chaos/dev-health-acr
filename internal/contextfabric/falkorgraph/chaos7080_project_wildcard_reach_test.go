package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestRepositoryScopedPrincipalDoesNotResolveAnOutOfGrantProject is the
// project-node twin of CHAOS-4390's team proof, driven through the real
// adapter.ResolveSubjects path (the resolver investigate_question calls).
//
// The node's authorization attributes are built by the real write-path
// conversion (authorizationValue, via subjectAuthorizationAttrsForTest) from
// the exact scope devhealthsource.projectAuthorizationScope emits for every
// project: ProjectIDs only, no RepositorySlugs. authorizationValue turns that
// empty repository list into "*".
//
// Correct state: a principal granted only acme/allowed must not resolve,
// commit, or even see as a candidate a project whose work lives outside that
// grant. On baseline cd41441b this FAILS: the "*" admits the caller.
func TestRepositoryScopedPrincipalDoesNotResolveAnOutOfGrantProject(t *testing.T) {
	assertOutOfGrantProjectHidden(t, contextfabric.AuthorizationScope{ProjectIDs: []string{"jira:PAY"}})
}

// TestControlOwnershipScopedProjectIsHidden is the control: the SAME harness
// with an ownership-derived repository list (the CHAOS-4390 team shape) on
// the project node passes, so the failure above is the "*" and not the
// harness.
func TestControlOwnershipScopedProjectIsHidden(t *testing.T) {
	assertOutOfGrantProjectHidden(t, contextfabric.AuthorizationScope{ProjectIDs: []string{"jira:PAY"}, RepositorySlugs: []string{"acme/other"}})
}

func assertOutOfGrantProjectHidden(t *testing.T, scope contextfabric.AuthorizationScope) {
	t.Helper()
	attrs := subjectAuthorizationAttrsForTest(scope)
	props := map[string]interface{}{
		propKind: "project", propCanonicalID: "jira:PAY", propLabel: "Payments",
		propSearchText: "Payments",
	}
	for key, value := range attrs {
		props[key] = value
	}
	project := &node{Properties: props}

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.fulltext.queryNodes") {
			return []row{{"node": project, "score": 1.0}}, nil
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	request := contextfabric.InvestigationRequest{
		Question: "how is the Payments project doing",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"Payments"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/allowed"}}

	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	t.Logf("project node authorization_repositories=%v", props[propAuthzRepos])
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "jira:PAY" {
			t.Errorf("out-of-grant project surfaced as a candidate to a principal scoped to acme/allowed: %+v", candidate.Subject)
		}
	}
	for _, committed := range resolution.Committed {
		if committed.CanonicalID == "jira:PAY" {
			t.Errorf("out-of-grant project COMMITTED for a principal scoped to acme/allowed: %+v -- its project facts are read next (engine.go ReadFacts)", committed)
		}
	}
}

// resolvePaymentsProject runs the same resolver path with a fake whose
// project reach query answers reachRepos for jira:PAY (nil: no owning team).
func resolvePaymentsProject(t *testing.T, principal storage.Principal, reachRepos []string) (contextfabric.SubjectResolution, int) {
	t.Helper()
	props := map[string]interface{}{propKind: "project", propCanonicalID: "jira:PAY", propLabel: "Payments", propSearchText: "Payments"}
	for key, value := range subjectAuthorizationAttrsForTest(contextfabric.AuthorizationScope{ProjectIDs: []string{"jira:PAY"}}) {
		props[key] = value
	}
	project := &node{Properties: props}
	reachQueries := 0
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{{"node": project, "score": 1.0}}, nil
		case strings.Contains(cypher, "RETURN p."+propCanonicalID+" AS id"):
			reachQueries++
			if reachRepos == nil {
				return nil, nil
			}
			return []row{{"id": "jira:PAY", "repos": reachRepos}}, nil
		}
		return nil, nil
	}}
	request := contextfabric.InvestigationRequest{
		Question: "how is the Payments project doing",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"Payments"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}
	resolution, _, _, _, err := newFakeAdapter(t, fake).ResolveSubjects(context.Background(), principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	return resolution, reachQueries
}

func paymentsSurfaced(resolution contextfabric.SubjectResolution) bool {
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "jira:PAY" {
			return true
		}
	}
	return false
}

// CHAOS-7080: the project reach rule on the resolver path, all three
// principal classes. A restricted caller sees the project exactly when its
// live ownership reach holds a granted repository; unrestricted and universal
// callers see it unchanged and trigger no reach read.
func TestChaos7080ResolverAppliesTheProjectReachRule(t *testing.T) {
	restricted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/allowed"}}
	if resolution, reads := resolvePaymentsProject(t, restricted, []string{"acme/allowed"}); !paymentsSurfaced(resolution) || reads != 1 {
		t.Errorf("restricted caller whose grant the project reaches: surfaced=%v reach reads=%d, want true/1", paymentsSurfaced(resolution), reads)
	}
	if resolution, reads := resolvePaymentsProject(t, restricted, []string{"acme/other"}); paymentsSurfaced(resolution) || reads != 1 {
		t.Errorf("restricted caller the project does not reach: surfaced=%v reach reads=%d, want false/1", paymentsSurfaced(resolution), reads)
	}
	if resolution, _ := resolvePaymentsProject(t, restricted, nil); paymentsSurfaced(resolution) {
		t.Error("restricted caller, project with no owning team: surfaced")
	}
	for _, principal := range []storage.Principal{{OrgID: "org-1"}, {OrgID: "org-1", RepositoryScopes: []string{"*"}}} {
		if resolution, reads := resolvePaymentsProject(t, principal, nil); !paymentsSurfaced(resolution) || reads != 0 {
			t.Errorf("scopes %v: surfaced=%v reach reads=%d, want true/0 (unchanged)", principal.RepositoryScopes, paymentsSurfaced(resolution), reads)
		}
	}
}

// The reach read itself: current edges and current teams only, the
// OWNED_BY_TEAM relation only, the caller's organization bound; and a failed
// reach read fails the call closed instead of admitting or silently hiding.
func TestChaos7080ProjectReachReadIsBoundAndFailsClosed(t *testing.T) {
	props := map[string]interface{}{propKind: "project", propCanonicalID: "jira:PAY", propLabel: "Payments", propSearchText: "Payments", propAuthzRepos: "*"}
	project := &node{Properties: props}
	var reachCypher string
	var reachParams map[string]interface{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{{"node": project, "score": 1.0}}, nil
		case strings.Contains(cypher, "RETURN p."+propCanonicalID+" AS id"):
			reachCypher, reachParams = cypher, params
			return nil, errors.New("falkordb: connection reset")
		}
		return nil, nil
	}}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/allowed"}}
	request := contextfabric.InvestigationRequest{
		Question: "how is the Payments project doing",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"Payments"}, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
	if _, _, _, _, err := newFakeAdapter(t, fake).ResolveSubjects(context.Background(), principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, ""); err == nil {
		t.Fatal("a failed project reach read did not fail the resolution")
	}
	for _, want := range []string{"r." + propRelationType + " = $owned", "r." + propValidToNs, "t." + propValidToNs, "{" + propOrgID + ":$org"} {
		if !strings.Contains(reachCypher, want) {
			t.Errorf("reach cypher lacks %q: %s", want, reachCypher)
		}
	}
	if reachParams["org"] != "org-1" || reachParams["owned"] != "OWNED_BY_TEAM" || reachParams[temporalParamStart] == nil {
		t.Errorf("reach params %v", reachParams)
	}
}
