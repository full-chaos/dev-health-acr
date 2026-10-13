package falkorgraph

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type organizationPullRequest struct {
	id, repo, state string
	reviewed        bool
}

// organizationPullRequests is n pull requests in id order: the first merged
// ones are reviewed, the rest open, and every third open one has a review.
func organizationPullRequests(n, merged int) []organizationPullRequest {
	pulls := make([]organizationPullRequest, 0, n)
	for i := 1; i <= n; i++ {
		p := organizationPullRequest{id: fmt.Sprintf("pull_request:acme:%03d", i), repo: "full-chaos/alpha", state: "merged", reviewed: true}
		if i > merged {
			p.state, p.reviewed = "open", (i-merged)%3 == 0
		}
		pulls = append(pulls, p)
	}
	return pulls
}

// statusCensusRows answers the status census read the way the store would:
// the state clause, the grant clause, id order, SKIP and LIMIT.
func statusCensusRows(pulls []organizationPullRequest, pullNode func(organizationPullRequest) *node, params map[string]interface{}) []row {
	states, _ := params["bstatus"].([]interface{})
	grants, restricted := params["grantRaw"].([]interface{})
	var rows []row
	for _, p := range pulls {
		if len(states) > 0 && !containsValue(states, p.state) {
			continue
		}
		if restricted && !containsValue(grants, p.repo) {
			continue
		}
		rows = append(rows, row{"b": pullNode(p)})
	}
	skip, _ := params["skip"].(int)
	limit, _ := params["limit"].(int)
	if skip >= len(rows) {
		return nil
	}
	rows = rows[skip:]
	if limit < len(rows) {
		rows = rows[:limit]
	}
	return rows
}

func containsValue(values []interface{}, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

type organizationStatusInterpreter struct {
	frame    *contextfabric.QuestionFrame
	anchored bool
}

func (o organizationStatusInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	sample := contextfabric.FamilySample{}
	if o.anchored {
		sample = contextfabric.FamilySample{ScopeAnchorTerm: "Acme", ScopeAnchorKind: contextfabric.SubjectOrganization}
	}
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "pull_request_status",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{},
	}, contextfabric.QuestionFamilyOutcome{
		Frame: o.frame, FrameObligations: o.frame.Obligations,
		Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel,
		WinningSample: sample,
	}, nil
}

// investigateOrganizationPullRequests asks Engine.Investigate for the pull
// requests holding status, over the real adapter: of the caller's organization
// when anchored, of no named anchor otherwise. Every pull request matches the
// question text, so the lexical arms return them in id order, and the kind
// census returns them in id order up to its LIMIT.
func investigateOrganizationPullRequests(t *testing.T, pulls []organizationPullRequest, status string, principal storage.Principal, anchored bool, scope ...contextfabric.RequestedScope) (contextfabric.InvestigationResult, *recordingTelemetry) {
	t.Helper()
	return investigateOrganizationPullRequestsWith(t, countingSynthesizer{}, pulls, status, principal, anchored, scope...)
}

// investigateOrganizationPullRequestsWith is investigateOrganizationPullRequests
// with the synthesizer the investigation answers through.
func investigateOrganizationPullRequestsWith(t *testing.T, synthesizer contextfabric.AnswerSynthesizer, pulls []organizationPullRequest, status string, principal storage.Principal, anchored bool, scope ...contextfabric.RequestedScope) (contextfabric.InvestigationResult, *recordingTelemetry) {
	t.Helper()
	byID := map[string]organizationPullRequest{}
	for _, p := range pulls {
		byID[p.id] = p
	}
	pullNode := func(p organizationPullRequest) *node {
		return &node{Properties: map[string]interface{}{propKind: "pull_request", propCanonicalID: p.id, propLabel: p.id,
			propAuthzRepos: []string{p.repo}, propPropertyPrefix + "state": p.state}}
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			var rows []row
			for _, p := range pulls {
				rows = append(rows, row{"node": pullNode(p), "score": 1.0})
			}
			return rows, nil
		case strings.Contains(cypher, "subject_kind = $toKind"):
			return statusCensusRows(pulls, pullNode, params), nil
		case strings.Contains(cypher, "IN $kinds"):
			kinds, _ := params["kinds"].([]string)
			if len(kinds) != 1 || kinds[0] != "pull_request" {
				return nil, nil
			}
			var rows []row
			for _, p := range pulls {
				rows = append(rows, row{"n": pullNode(p)})
			}
			var limit int
			if _, err := fmt.Sscanf(cypher[strings.LastIndex(cypher, "LIMIT"):], "LIMIT %d", &limit); err == nil && len(rows) > limit {
				rows = rows[:limit]
			}
			return rows, nil
		case cypher == pullRequestStateCypher():
			var rows []row
			ids, _ := params["ids"].([]interface{})
			for _, raw := range ids {
				if p, ok := byID[fmt.Sprint(raw)]; ok {
					rows = append(rows, row{"id": raw, "value": p.state})
				}
			}
			return rows, nil
		case strings.HasPrefix(cypher, "UNWIND $ids") && params["rel"] == string(pullRequestReviewStep.relation):
			var rows []row
			ids, _ := params["ids"].([]interface{})
			for _, raw := range ids {
				if p, ok := byID[fmt.Sprint(raw)]; ok && p.reviewed {
					review := &node{Properties: map[string]interface{}{propKind: "pull_request_review", propCanonicalID: p.id + ":review",
						propPropertyPrefix + "state": "APPROVED", propValidFromNs: int64(1)}}
					rows = append(rows, row{"id": raw, "b": review, "r": &edge{Properties: map[string]interface{}{}}})
				}
			}
			return rows, nil
		case strings.Contains(cypher, "UNION"), strings.HasPrefix(cypher, "UNWIND $ids"):
			return nil, nil
		}
		id, _ := params["id"].(string)
		if p, ok := byID[id]; ok {
			return []row{{"n": pullNode(p)}}, nil
		}
		if id == "organization:org-1" {
			return []row{fakeSubjectNodeRow("organization", id, "Acme")}, nil
		}
		return nil, nil
	}}
	frame := pullRequestStatusFrame(status)
	frame.Goals = []contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey}
	frame.SubjectExpression.Scoped.AnchorTerms = []string{"Acme"}
	derived := contextfabric.DeriveFrameObligations(*frame, nil)
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	var graph contextfabric.GraphReader = subjectlessGraphReader{Adapter: adapter}
	if anchored {
		graph = committedAnchorGraphReader{
			Adapter:   adapter,
			committed: contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org-1", Label: "Acme"},
		}
	}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: organizationStatusInterpreter{frame: &derived, anchored: anchored},
		Graph:       graph,
		Facts:       emptyFactReader{}, Synthesizer: synthesizer, Results: discardingResultStore{},
		Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_94390001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_94390001",
		Question: "which pull requests are " + status,
		TimeContext: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	if len(scope) > 0 {
		request.RequestedScope = scope[0]
	}
	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, telemetry
}

func servedCohortIDs(result contextfabric.InvestigationResult) []string {
	ids := []string{}
	if result.Cohort == nil {
		return ids
	}
	for _, m := range result.Cohort.Members {
		ids = append(ids, m.Subject.CanonicalID)
	}
	sort.Strings(ids)
	return ids
}

// TestOrganizationPullRequestStatusIsServedPastTheWalkCap: the organization's
// first 30 pull requests by id are merged and the last ten open. Every arm that
// reaches the organization's pull requests stops at 25, so a status applied
// after them sees only merged ones. The served answer lists the open pull
// requests and the ones awaiting review, complete.
func TestOrganizationPullRequestStatusIsServedPastTheWalkCap(t *testing.T) {
	pulls := organizationPullRequests(40, 30)
	want := map[string][]string{"open": {}, "awaiting_review": {}}
	for _, p := range pulls[30:] {
		want["open"] = append(want["open"], p.id)
		if !p.reviewed {
			want["awaiting_review"] = append(want["awaiting_review"], p.id)
		}
	}
	for status, ids := range want {
		t.Run(status, func(t *testing.T) {
			result, _ := investigateOrganizationPullRequests(t, pulls, status, storage.Principal{OrgID: "org-1"}, true)
			if got := servedCohortIDs(result); !reflect.DeepEqual(got, ids) {
				t.Fatalf("served %v (status %q, limitations %q), want %v", got, result.Status, result.Limitations, ids)
			}
			if !result.Cohort.Complete || result.Cohort.Truncated {
				t.Fatalf("cohort complete=%v truncated=%v, want a complete set", result.Cohort.Complete, result.Cohort.Truncated)
			}
		})
	}
}

// TestUnanchoredPullRequestStatusIsServedPastTheCensusBound: no anchor is
// named, so the kind census reads the organization's pull requests. The first
// 2080 by id are merged and the last 20 open; the census stops at 2000, so a
// status applied after it sees only merged ones.
func TestUnanchoredPullRequestStatusIsServedPastTheCensusBound(t *testing.T) {
	pulls := organizationPullRequests(2100, 2080)
	want := map[string][]string{"open": {}, "awaiting_review": {}}
	for _, p := range pulls[2080:] {
		want["open"] = append(want["open"], p.id)
		if !p.reviewed {
			want["awaiting_review"] = append(want["awaiting_review"], p.id)
		}
	}
	for status, ids := range want {
		t.Run(status, func(t *testing.T) {
			result, telemetry := investigateOrganizationPullRequests(t, pulls, status, storage.Principal{OrgID: "org-1"}, false)
			if len(telemetry.cohortKindCensuses) != 1 || telemetry.cohortKindCensuses[0].decision != CohortKindCensusRan || telemetry.cohortKindCensuses[0].truncated {
				t.Fatalf("kind census lines = %+v, want one uncut census", telemetry.cohortKindCensuses)
			}
			if got := servedCohortIDs(result); !reflect.DeepEqual(got, ids) {
				t.Fatalf("served %v (status %q, limitations %q), want %v", got, result.Status, result.Limitations, ids)
			}
			if !result.Cohort.Complete || result.Cohort.Truncated {
				t.Fatalf("cohort complete=%v truncated=%v, want a complete set", result.Cohort.Complete, result.Cohort.Truncated)
			}
		})
	}
}

// TestAStatusCensusCutAtItsPageCapSaysWhereItWasDecided: every open pull
// request in the first eight pages has a review, so none awaits one, and the
// read stops at its page cap with one awaiting pull request unread. Anchored,
// the pool is counted and the answer says it checked 208 of 209; unanchored,
// the pool is past the census bound, so the answer says only that it checked
// the first 208.
func TestAStatusCensusCutAtItsPageCapSaysWhereItWasDecided(t *testing.T) {
	read := memberStatusPageCap * 26
	for _, tc := range []struct {
		name     string
		anchored bool
		open     int
		pool     int
	}{
		{"organization anchor", true, read + 1, read + 1},
		{"no anchor", false, exactNameCandidateQueryLimit + 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pulls := organizationPullRequests(tc.open, 0)
			for i := range pulls {
				pulls[i].reviewed = i < read
			}
			result, _ := investigateOrganizationPullRequests(t, pulls, "awaiting_review", storage.Principal{OrgID: "org-1"}, tc.anchored)
			if got := servedCohortIDs(result); len(got) != 0 {
				t.Fatalf("served %v, want none: the awaiting pull requests were not read", got)
			}
			if result.Cohort == nil || result.Cohort.Complete || !result.Cohort.Truncated {
				t.Fatalf("cohort = %+v, want an empty, cut, incomplete pull request set", result.Cohort)
			}
			if result.Status != contextfabric.InvestigationPartial {
				t.Fatalf("status = %q, want partial: the read was cut", result.Status)
			}
			sentence := contractsv1.ContextFabricMemberStatusPoolCutLimitation(string(contextfabric.SubjectPullRequest), read, tc.pool)
			if !slices.Contains(result.Limitations, sentence) {
				t.Fatalf("limitations %q, want %q", result.Limitations, sentence)
			}
			assertNoEmptySearchClaim(t, result)
		})
	}
}

// TestARestrictedCallerCountsOnlyThePullRequestsItMayRead: the caller may read
// full-chaos/alpha only. The open pull requests of acme/hidden come first by
// id; the census reads past them, serves only the readable ones, and its read
// carries the caller's grants.
func TestARestrictedCallerCountsOnlyThePullRequestsItMayRead(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/alpha"}}
	for _, anchored := range []bool{true, false} {
		t.Run(fmt.Sprintf("anchored=%v", anchored), func(t *testing.T) {
			pulls := organizationPullRequests(60, 0)
			want := []string{}
			for i := range pulls {
				if i < 50 {
					pulls[i].repo = "acme/hidden"
					continue
				}
				want = append(want, pulls[i].id)
			}
			result, telemetry := investigateOrganizationPullRequests(t, pulls, "open", principal, anchored)
			if got := servedCohortIDs(result); !reflect.DeepEqual(got, want) {
				t.Fatalf("served %v, want only the readable %v", got, want)
			}
			if !result.Cohort.Complete || result.Cohort.Truncated {
				t.Fatalf("cohort complete=%v truncated=%v, want a complete set", result.Cohort.Complete, result.Cohort.Truncated)
			}
			for _, line := range telemetry.pullRequestStatusFilters {
				if line.Candidates != len(want) || line.Kept != len(want) {
					t.Fatalf("status filter line = %+v, want %d readable pull requests counted", line, len(want))
				}
			}
		})
	}
}

// TestNoReadablePullRequestOfTheOrganizationRevealsNone: the caller can read
// none of the organization's pull requests, by its grants (which the read
// carries) or by the scope it requested (which only the per-node decision
// applies). Nothing is served, no empty complete set says pull requests were
// reached, and no line counts one.
func TestNoReadablePullRequestOfTheOrganizationRevealsNone(t *testing.T) {
	granted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/alpha"}}
	requested := contextfabric.RequestedScope{RepositorySlugs: []string{"full-chaos/alpha"}}
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		scope     []contextfabric.RequestedScope
	}{
		{"grants", granted, nil},
		{"requested scope", storage.Principal{OrgID: "org-1"}, []contextfabric.RequestedScope{requested}},
	} {
		for _, anchored := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/anchored=%v", tc.name, anchored), func(t *testing.T) {
				pulls := organizationPullRequests(40, 20)
				for i := range pulls {
					pulls[i].repo = "acme/hidden"
				}
				result, telemetry := investigateOrganizationPullRequests(t, pulls, "open", tc.principal, anchored, tc.scope...)
				if result.Cohort != nil && (len(result.Cohort.Members) != 0 || result.Cohort.Complete || result.Cohort.Rationale == pullRequestStatusNoMatchRationale) {
					t.Fatalf("cohort = %+v, want no member set that says pull requests were reached", result.Cohort)
				}
				for _, line := range telemetry.pullRequestStatusFilters {
					if line.Candidates != 0 || line.Kept != 0 {
						t.Fatalf("status filter line = %+v, want no hidden pull request counted", line)
					}
				}
			})
		}
	}
}

// assertNoEmptySearchClaim: pull requests were read, so the served answer
// never says retrieval found no candidate.
func assertNoEmptySearchClaim(t *testing.T, result contextfabric.InvestigationResult) {
	t.Helper()
	for _, limitation := range result.Limitations {
		if strings.HasPrefix(limitation, "Retrieval found no candidate") {
			t.Fatalf("limitations %q claim an empty search over pull requests that were read", result.Limitations)
		}
	}
}

// TestNoPullRequestHoldingTheStatusIsACountOfZero: every pull request of the
// organization is merged, so none is open. Whether or not the organization is
// named, the served answer says no pull request the read reached is open,
// never that retrieval found nothing.
func TestNoPullRequestHoldingTheStatusIsACountOfZero(t *testing.T) {
	for _, anchored := range []bool{true, false} {
		t.Run(fmt.Sprintf("anchored=%v", anchored), func(t *testing.T) {
			result, _ := investigateOrganizationPullRequests(t, organizationPullRequests(40, 40), "open", storage.Principal{OrgID: "org-1"}, anchored)
			if result.Status != contextfabric.InvestigationComplete {
				t.Fatalf("status = %q (limitations %q), want complete: a measured zero is an answer", result.Status, result.Limitations)
			}
			if result.Cohort == nil || len(result.Cohort.Members) != 0 || !result.Cohort.Complete || result.Cohort.Truncated {
				t.Fatalf("cohort = %+v, want an empty, complete pull request set", result.Cohort)
			}
			want := pullRequestStatusNoMatchRationale
			if !anchored {
				want = "No pull request the read reached holds the status asked for now; that is a count of matches, not a statement about health."
			}
			if result.Cohort.Rationale != want {
				t.Fatalf("rationale = %q, want %q", result.Cohort.Rationale, want)
			}
			assertNoEmptySearchClaim(t, result)
		})
	}
}

// TestAnUnqualifiedOrganizationQuestionReadsNoStatusCensus: with no status the
// organization's pull requests stay on the generic walk.
func TestAnUnqualifiedOrganizationQuestionReadsNoStatusCensus(t *testing.T) {
	var census int
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "subject_kind = $toKind") {
			census++
		}
		if id, _ := params["id"].(string); id == "organization:org-1" {
			return []row{fakeSubjectNodeRow("organization", id, "Acme")}, nil
		}
		return nil, nil
	}}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org-1", Label: "Acme"}
	frame := pullRequestStatusFrame("")
	frame.SubjectExpression.Scoped.AnchorTerms = []string{"Acme"}
	request := ownershipRoutingRequest(frame, anchor)
	request.ScopeAnchorKind = contextfabric.SubjectOrganization
	request.Request.Question = "which pull requests of Acme are there?"
	if _, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if census != 0 {
		t.Fatalf("status census reads = %d, want none for a question with no status", census)
	}
}

// TestOnlyTheCallersOrganizationAnchorsTheStatusCensus: another organization,
// or an anchor of another kind, is not read by the census.
func TestOnlyTheCallersOrganizationAnchorsTheStatusCensus(t *testing.T) {
	status := memberStatusPredicate{kind: contextfabric.SubjectPullRequest, status: "open"}
	frame := pullRequestStatusFrame("open")
	frame.SubjectExpression.Scoped.AnchorTerms = []string{"Acme"}
	for _, tc := range []struct {
		anchor contextfabric.SubjectRef
		want   bool
	}{
		{contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org-1", Label: "Acme"}, true},
		{contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:org-2", Label: "Acme"}, false},
		{contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:acme-1", Label: "Acme"}, false},
	} {
		request := ownershipRoutingRequest(frame, tc.anchor)
		request.ScopeAnchorKind = tc.anchor.Kind
		got := organizationStatusAnchor(request, contextfabric.SubjectPullRequest, status, storage.Principal{OrgID: "org-1"}) != nil
		if got != tc.want {
			t.Errorf("anchor %s: census anchor = %v, want %v", tc.anchor.CanonicalID, got, tc.want)
		}
		if organizationStatusAnchor(request, contextfabric.SubjectPullRequest, memberStatusPredicate{}, storage.Principal{OrgID: "org-1"}) != nil {
			t.Errorf("anchor %s: a question with no status anchored the census", tc.anchor.CanonicalID)
		}
	}
}

// TestTheStatusCensusCarriesTheStatusAndTheGrantsInsideItsRead: the state and,
// for a restricted caller, the grants narrow the read before its LIMIT.
func TestTheStatusCensusCarriesTheStatusAndTheGrantsInsideItsRead(t *testing.T) {
	clause := memberStatusPredicate{kind: contextfabric.SubjectPullRequest, status: "awaiting_review"}.clause()
	cypher := statusCensusCypher(clause, temporalFilter{}, true, false)
	limit := strings.Index(cypher, "LIMIT")
	for _, want := range []string{"b." + propPropertyPrefix + "state IN $bstatus", "$grantRaw"} {
		at := strings.Index(cypher, want)
		if at < 0 || at > limit {
			t.Fatalf("census read %q does not carry %q before its LIMIT", cypher, want)
		}
	}
	if strings.Contains(statusCensusCypher(clause, temporalFilter{}, false, false), "$grantRaw") {
		t.Fatal("an unrestricted caller's census read carries a grant clause")
	}
	if got := clause.values; !reflect.DeepEqual(got, []interface{}{contextfabric.PullRequestStatusOpen}) {
		t.Fatalf("awaiting_review clause values = %v, want the open state: the review rows decide the rest", got)
	}
}

// TestAStatusQuestionThatReadNothingStaysNoMatch: the organization holds no
// pull request, so the census read none; with no anchor that is still an empty
// search.
func TestAStatusQuestionThatReadNothingStaysNoMatch(t *testing.T) {
	result, _ := investigateOrganizationPullRequests(t, nil, "open", storage.Principal{OrgID: "org-1"}, false)
	if result.Status != contextfabric.InvestigationNoMatch || result.Cohort != nil {
		t.Fatalf("status = %q cohort = %+v, want no_match and no member set", result.Status, result.Cohort)
	}
}

// TestAMeasuredZeroIsNeverServedAsNoMatch: the synthesizer answers no_match
// over a status read that read pull requests and kept none. The served status
// is the read's: complete over a complete read, partial over a cut one.
func TestAMeasuredZeroIsNeverServedAsNoMatch(t *testing.T) {
	read := memberStatusPageCap * 26
	cut := organizationPullRequests(read+1, 0)
	for i := range cut {
		cut[i].reviewed = i < read
	}
	for _, anchored := range []bool{true, false} {
		for _, tc := range []struct {
			name   string
			pulls  []organizationPullRequest
			status string
			want   contextfabric.InvestigationStatus
		}{
			{"complete read", organizationPullRequests(40, 40), "open", contextfabric.InvestigationComplete},
			{"cut read", cut, "awaiting_review", contextfabric.InvestigationPartial},
		} {
			t.Run(fmt.Sprintf("%s/anchored=%v", tc.name, anchored), func(t *testing.T) {
				result, _ := investigateOrganizationPullRequestsWith(t, noMatchSynthesizer{}, tc.pulls, tc.status, storage.Principal{OrgID: "org-1"}, anchored)
				if result.Status != tc.want {
					t.Fatalf("status = %q (limitations %q), want %q: a measured zero is an answer", result.Status, result.Limitations, tc.want)
				}
				if result.Cohort == nil || len(result.Cohort.Members) != 0 {
					t.Fatalf("cohort = %+v, want the empty pull request set", result.Cohort)
				}
			})
		}
	}
}

// TestMoreMatchesThanTheCapAreACappedSetNotACut: 40 open pull requests hold
// the status and 25 are served. The status was decided for every pull request
// read, so the set is truncated by the member cap and no sentence says the
// status was decided within a cut read.
func TestMoreMatchesThanTheCapAreACappedSetNotACut(t *testing.T) {
	for _, anchored := range []bool{true, false} {
		t.Run(fmt.Sprintf("anchored=%v", anchored), func(t *testing.T) {
			result, _ := investigateOrganizationPullRequests(t, organizationPullRequests(60, 20), "open", storage.Principal{OrgID: "org-1"}, anchored)
			if result.Cohort == nil || len(result.Cohort.Members) != 25 || !result.Cohort.Truncated {
				t.Fatalf("cohort = %+v, want 25 open pull requests, truncated", result.Cohort)
			}
			for _, limitation := range result.Limitations {
				if contractsv1.IsContextFabricMemberStatusPoolCutLimitation(limitation) {
					t.Fatalf("limitations %q state a cut status read over a read that decided every pull request", result.Limitations)
				}
			}
		})
	}
}
