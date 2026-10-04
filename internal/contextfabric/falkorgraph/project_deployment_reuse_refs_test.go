package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const reuseNarrowedReason = "no longer visible to you"

// reuseTurn is one turn of the project question with answer reuse on. The
// node refs are a stable function of the node id plus refSuffix, so a test can
// change a ref at the source between turns.
func reuseTurn(t *testing.T, s routeSeed, store *routeStore, principal storage.Principal, turn int, refSuffix string) contextfabric.InvestigationResult {
	t.Helper()
	base := s.conn()
	conn := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
		rows, err := base.queryFunc(ctx, key, cypher, params, ro)
		for _, r := range rows {
			for _, value := range r {
				if n, ok := value.(*node); ok && n != nil {
					id, _ := n.Properties[propCanonicalID].(string)
					n.Properties[propEvidenceRefs] = []string{stableNodeRef(id) + refSuffix}
				}
			}
		}
		return rows, err
	}}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: projectDeploymentsInterpreter{name: "alpha", kind: contextfabric.SubjectProject},
		Graph:       newFakeAdapter(t, conn), Facts: emptyFactReader{}, Synthesizer: countingSynthesizer{},
		Results: store, ReuseGate: store, Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return fmt.Sprintf("result_8619000%d", turn) }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Investigate(context.Background(), principal, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: fmt.Sprintf("request_8619000%d", turn),
		Question:    "which deployments belong to project alpha",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 30, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
		Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	})
	if err != nil {
		t.Fatalf("turn %d: Investigate() error = %v", turn, err)
	}
	return result
}

// reuseSeed hides the project deployments from the recheck's lexical arm.
func reuseSeed() routeSeed {
	s := seedTwoLinkedProjects()
	for key := range s.text {
		if strings.HasPrefix(key, "deployment|deployment:acme/alpha-service") {
			s.text[key] = "release production"
		}
	}
	return s
}

func memberRefs(r contextfabric.InvestigationResult) map[string][]string {
	out := map[string][]string{}
	if r.Cohort != nil {
		for _, m := range r.Cohort.Members {
			refs := append([]string(nil), m.EvidenceRefIDs...)
			sort.Strings(refs)
			out[m.Subject.CanonicalID] = refs
		}
	}
	return out
}

func narrowedReason(r contextfabric.InvestigationResult) bool {
	for _, reason := range r.Coverage.DegradedReasons {
		if strings.Contains(reason, reuseNarrowedReason) {
			return true
		}
	}
	return false
}

// (a) access unchanged: the reused answer keeps every member ref and does not
// claim any was removed.
func TestAReusedProjectAnswerKeepsMemberRefsWhenAccessIsUnchanged(t *testing.T) {
	s := reuseSeed()
	store := &routeStore{}
	principal := storage.Principal{OrgID: "org-1"}
	first := reuseTurn(t, s, store, principal, 1, "")
	second := reuseTurn(t, s, store, principal, 2, "")
	if !second.Reused {
		t.Fatalf("second turn was not served from the stored answer")
	}
	want := memberRefs(first)
	if len(want) != 2 {
		t.Fatalf("first turn members = %v, want 2", want)
	}
	for id, refs := range want {
		if len(refs) == 0 {
			t.Fatalf("fixture: first turn member %s has no evidence refs", id)
		}
	}
	if got := memberRefs(second); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reused member refs = %v, want %v", got, want)
	}
	if narrowedReason(second) {
		t.Errorf("reused answer claims refs are no longer visible: %v", second.Coverage.DegradedReasons)
	}
}

// (b)+(c) the caller lost access to a member's node between the turns: no
// ref of that node is served, and the member is not served.
func TestAReusedProjectAnswerDropsAMemberTheCallerLostAccessTo(t *testing.T) {
	s := reuseSeed()
	store := &routeStore{}
	first := reuseTurn(t, s, store, storage.Principal{OrgID: "org-1"}, 1, "")
	lost := s.deployments[routeProjectAlpha][0]
	kept := s.deployments[routeProjectAlpha][1]
	lostRefs := memberRefs(first)[lost]
	if len(lostRefs) == 0 {
		t.Fatalf("fixture: member %s has no refs", lost)
	}
	// Turn 2: the caller holds only the repository of the kept member.
	s2 := s
	s2.nodes = append([]seededNode(nil), s.nodes...)
	for i := range s2.nodes {
		if s2.nodes[i].id == lost {
			s2.nodes[i].repos = []string{"acme/other-service"}
		}
	}
	second := reuseTurn(t, s2, store, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/alpha-service"}}, 2, "")
	for id := range memberRefs(second) {
		if id == lost {
			t.Errorf("member %s the caller can no longer see is served", id)
		}
	}
	served := fmt.Sprint(second.EvidenceRefIDs, memberRefs(second), second.EvidenceRefLabels)
	for _, ref := range lostRefs {
		if strings.Contains(served, ref) {
			t.Errorf("a ref of an invisible node (%s) is served on the second turn", ref)
		}
	}
	_ = kept
}

// (d) a member whose node no longer exists is not served.
func TestAReusedProjectAnswerDropsAMemberWhoseNodeIsGone(t *testing.T) {
	s := reuseSeed()
	store := &routeStore{}
	first := reuseTurn(t, s, store, storage.Principal{OrgID: "org-1"}, 1, "")
	gone := s.deployments[routeProjectAlpha][0]
	goneRefs := memberRefs(first)[gone]
	s2 := s
	s2.nodes = nil
	for _, n := range s.nodes {
		if n.id != gone {
			s2.nodes = append(s2.nodes, n)
		}
	}
	second := reuseTurn(t, s2, store, storage.Principal{OrgID: "org-1"}, 2, "")
	if _, served := memberRefs(second)[gone]; served {
		t.Errorf("member %s whose node is gone is served", gone)
	}
	served := fmt.Sprint(second.EvidenceRefIDs, memberRefs(second), second.EvidenceRefLabels)
	for _, ref := range goneRefs {
		if strings.Contains(served, ref) {
			t.Errorf("ref %s of a deleted node is served", ref)
		}
	}
}

// A ref removed at the source (the node now carries a different ref) is not
// trusted from the stored answer.
func TestAReusedProjectAnswerDoesNotServeARefTheNodeNoLongerCarries(t *testing.T) {
	s := reuseSeed()
	store := &routeStore{}
	first := reuseTurn(t, s, store, storage.Principal{OrgID: "org-1"}, 1, "")
	stale := memberRefs(first)
	second := reuseTurn(t, s, store, storage.Principal{OrgID: "org-1"}, 2, "_v2")
	served := fmt.Sprint(second.EvidenceRefIDs, memberRefs(second), second.EvidenceRefLabels)
	for id, refs := range stale {
		for _, ref := range refs {
			if strings.Contains(served, ref+" ") || strings.Contains(served, ref+"]") || strings.Contains(served, ref+":") {
				t.Errorf("stale ref %s of %s is served though the node no longer carries it", ref, id)
			}
		}
	}
}

// A cohort larger than the subject-candidate cap: the recheck's discovery
// carries at most MaxSubjectCandidates of the hinted members as candidates, so
// the refs of the others are not on its surface. Their nodes are read live.
func TestAReusedLargeProjectAnswerKeepsEveryMemberRef(t *testing.T) {
	s := reuseSeed()
	const slug = "acme/alpha-service"
	repoID := "repository:github:" + slug
	for d := 2; d < 14; d++ {
		depID := fmt.Sprintf("deployment:%s:%d", slug, d)
		s.nodes = append(s.nodes, seededNode{kind: "deployment", id: depID, label: depID, repos: []string{slug}})
		s.text["deployment|"+depID] = "release production"
		s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "deployment", depID, "repository", repoID})
		s.deployments[routeProjectAlpha] = append(s.deployments[routeProjectAlpha], depID)
	}
	store := &routeStore{}
	principal := storage.Principal{OrgID: "org-1"}
	first := reuseTurn(t, s, store, principal, 1, "")
	second := reuseTurn(t, s, store, principal, 2, "")
	if !second.Reused {
		t.Fatalf("second turn was not served from the stored answer")
	}
	want := memberRefs(first)
	if len(want) != 14 {
		t.Fatalf("first turn served %d members, want 14", len(want))
	}
	if got := memberRefs(second); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reused member refs = %v, want %v", got, want)
	}
	if narrowedReason(second) {
		t.Errorf("reused answer claims refs are no longer visible: %v", second.Coverage.DegradedReasons)
	}
}
