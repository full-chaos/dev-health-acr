package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

const (
	reachInsideRepo  = "acme/alpha"
	reachOutsideRepo = "other/secret"
	repoLessItemID   = "work_item.v2:00000000-0000-0000-0000-000000000000:linear:CHAOS-11516"
)

// linkEdgeRow is one link edge of the fake graph.
type linkEdgeRow struct {
	item, tier string
	repo       string
}

// reachRows answers the reach statement from edges, by what the statement and
// its parameters carry: the tier it asks for and the grant it matches.
func reachRows(t *testing.T, edges []linkEdgeRow, cypher string, params map[string]interface{}) []row {
	t.Helper()
	wantsNative := strings.Contains(cypher, "$native")
	matchesGrant := strings.Contains(cypher, "$grantRaw")
	var rows []row
	for _, edge := range edges {
		if wantsNative && edge.tier != params["native"] {
			continue
		}
		if matchesGrant {
			raw, _ := params["grantRaw"].([]interface{})
			owners, _ := params["grantOwners"].([]interface{})
			matched := slices.Contains(raw, interface{}(edge.repo))
			for _, owner := range owners {
				matched = matched || strings.HasPrefix(edge.repo, fmt.Sprint(owner))
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, row{"id": edge.item, "repos": []interface{}{edge.repo}})
	}
	return rows
}

func repoLessNode(id string) *node {
	return &node{Properties: map[string]interface{}{
		propKind: string(contractsv1.ContextFabricSubjectWorkItem), propCanonicalID: id, propAuthzRepos: []string{noRepositoryScope},
		propLabel: "CHAOS-11516 build legs", "evidence_refs": []string{"evidence_identity_1234"},
	}}
}

// readNode runs one exact read through the wrapped conn, in a call for principal.
func readNode(t *testing.T, fake *fakeConn, principal storage.Principal, n *node) (graphrank.CandidateNode, context.Context) {
	t.Helper()
	adapter := newFakeAdapter(t, fake)
	ctx, err := adapter.withProjectReach(context.Background(), "k", principal)
	if err != nil {
		t.Fatal(err)
	}
	fake.queryFunc = func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return []row{{"n": n}}, nil
	}
	rows, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return toCandidateNode(rows[0]["n"].(*node)), ctx
}

func restrictedReachCaller(t *testing.T) storage.Principal {
	t.Helper()
	return userprincipal.Narrowed(t, "org_1", []string{reachInsideRepo}, []string{reachInsideRepo})
}

func TestTheLinkReachHoldsOnlyGrantedNativeLinks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		edges []linkEdgeRow
		want  bool
	}{
		{"native, granted", []linkEdgeRow{{repoLessItemID, "native", reachInsideRepo}}, true},
		{"explicit_text, granted", []linkEdgeRow{{repoLessItemID, "explicit_text", reachInsideRepo}}, false},
		{"heuristic, granted", []linkEdgeRow{{repoLessItemID, "heuristic", reachInsideRepo}}, false},
		{"native, outside", []linkEdgeRow{{repoLessItemID, "native", reachOutsideRepo}}, false},
		{"another item's link", []linkEdgeRow{{"work_item.v2:x:linear:CHAOS-1", "native", reachInsideRepo}}, false},
		{"a link outside beside one inside", []linkEdgeRow{{repoLessItemID, "native", reachOutsideRepo}, {repoLessItemID, "native", reachInsideRepo}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
				return reachRows(t, tc.edges, cypher, params), nil
			}}
			principal := restrictedReachCaller(t)
			candidate, _ := readNode(t, fake, principal, repoLessNode(repoLessItemID))
			if got := graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, candidate.Attributes); got != tc.want {
				t.Fatalf("admitted = %v, want %v (attributes %v)", got, tc.want, candidate.Attributes)
			}
			if linked, _ := candidate.Attributes[graphrank.AuthorizationLinkRepositoriesAttr].([]string); slices.Contains(linked, reachOutsideRepo) {
				t.Fatalf("attribute %v holds a repository outside the grant", linked)
			}
		})
	}
}

// The statement is the rule: native, the link relation, repo-less, the grant,
// no temporal term (validity in time is not an authorization input).
func TestTheLinkReachStatementIsAnchoredOnTheGrant(t *testing.T) {
	t.Parallel()
	var statement string
	var params map[string]interface{}
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, bound map[string]interface{}) ([]row, error) {
		statement, params = cypher, bound
		return nil, nil
	}}
	readNode(t, fake, restrictedReachCaller(t), repoLessNode(repoLessItemID))
	for _, want := range []string{"(w:Subject {org_id:$org,", "(p:Subject {org_id:$org,", "r.relation_type = $links", "r.property_link_provenance = $native", "$noRepository IN w.authorization_repositories", "$grantRaw", "LIMIT 20001"} {
		if !strings.Contains(statement, want) {
			t.Fatalf("statement %q lacks %q", statement, want)
		}
	}
	if params["native"] != "native" || params["links"] != string(contractsv1.ContextFabricRelationshipLinksPullRequest) || params["noRepository"] != contractsv1.ContextFabricNoRepositorySentinel {
		t.Fatalf("params = %v", params)
	}
	if strings.Contains(statement, "valid_") || strings.Contains(statement, "$asOf") {
		t.Fatalf("statement %q carries a temporal term: a merged pull request's link must still count", statement)
	}
}

// Rows outside the grant cannot fill the reach: the statement drops them before
// its LIMIT, so an item inside stays proven.
func TestOutsideLinksCannotCutTheReach(t *testing.T) {
	t.Parallel()
	edges := make([]linkEdgeRow, 0, linkReachMaxRows+2)
	for i := range linkReachMaxRows + 1 {
		edges = append(edges, linkEdgeRow{fmt.Sprintf("work_item.v2:z:linear:OUT-%d", i), "native", reachOutsideRepo})
	}
	edges = append(edges, linkEdgeRow{repoLessItemID, "native", reachInsideRepo})
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
		return reachRows(t, edges, cypher, params), nil
	}}
	principal := restrictedReachCaller(t)
	candidate, ctx := readNode(t, fake, principal, repoLessNode(repoLessItemID))
	if linkReachIncomplete(ctx) {
		t.Fatal("rows outside the grant cut the reach")
	}
	if !graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, candidate.Attributes) {
		t.Fatal("the inside item was not admitted")
	}
}

// An owner grant matches a prefix in the statement, a necessary condition only.
// A three-part slug is not a repository the gate admits for that owner.
func TestAnOwnerGrantDoesNotReachAThreePartSlug(t *testing.T) {
	t.Parallel()
	principal := userprincipal.Authenticated(t, "org_1", []string{"acme/*"})
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
		return reachRows(t, []linkEdgeRow{{repoLessItemID, "native", "acme/group/secret"}}, cypher, params), nil
	}}
	candidate, _ := readNode(t, fake, principal, repoLessNode(repoLessItemID))
	if _, held := candidate.Attributes[graphrank.AuthorizationLinkRepositoriesAttr]; held {
		t.Fatalf("attributes = %v, want no link for a slug the gate refuses", candidate.Attributes)
	}
}

// Ruling 4: a work item with a repository of its own is decided by it. A native
// link never widens a GitHub or GitLab item.
func TestANativeLinkNeverWidensAGitHubOrGitLabItem(t *testing.T) {
	t.Parallel()
	for provider, id := range map[string]string{"github": "work_item.v2:r-2:gh:other/secret#7", "gitlab": "work_item.v2:r-2:gitlab:other/secret#7"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
				return reachRows(t, []linkEdgeRow{{id, "native", reachInsideRepo}}, cypher, params), nil
			}}
			principal := restrictedReachCaller(t)
			n := &node{Properties: map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectWorkItem), propCanonicalID: id, propAuthzRepos: []string{reachOutsideRepo}}}
			candidate, _ := readNode(t, fake, principal, n)
			if _, held := candidate.Attributes[graphrank.AuthorizationLinkRepositoriesAttr]; held {
				t.Fatal("the read added a link to an item with its own repository")
			}
			if graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, candidate.Attributes) {
				t.Fatal("a native link widened an item whose own repository is outside the grant")
			}
		})
	}
}

// Only a work item takes a link: another kind holding the sentinel does not.
func TestOnlyAWorkItemTakesALink(t *testing.T) {
	t.Parallel()
	id := "project:linear:p-1"
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
		return reachRows(t, []linkEdgeRow{{id, "native", reachInsideRepo}}, cypher, params), nil
	}}
	n := &node{Properties: map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectProject), propCanonicalID: id, propAuthzRepos: []string{noRepositoryScope}}}
	candidate, _ := readNode(t, fake, restrictedReachCaller(t), n)
	if _, held := candidate.Attributes[graphrank.AuthorizationLinkRepositoriesAttr]; held {
		t.Fatal("a node of another kind took a link")
	}
}

// Unrestricted, universal and project-only callers read no reach: their
// statements are what they were.
func TestOnlyARestrictedCallerReadsTheReach(t *testing.T) {
	t.Parallel()
	for name, principal := range map[string]storage.Principal{
		"unrestricted": userprincipal.Authenticated(t, "org_1", nil),
		"universal":    userprincipal.Authenticated(t, "org_1", []string{"*"}),
		"project only": userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reads := 0
			fake := &fakeConn{linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
				reads++
				return nil, nil
			}}
			readNode(t, fake, principal, repoLessNode(repoLessItemID))
			if reads != 0 {
				t.Fatalf("reach reads = %d, want none", reads)
			}
		})
	}
	reads := 0
	fake := &fakeConn{linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
		reads++
		return nil, nil
	}}
	principal := restrictedReachCaller(t)
	if auth.ClassifyGrant(principal) != auth.GrantRestricted {
		t.Fatal("fixture is not restricted")
	}
	readNode(t, fake, principal, repoLessNode(repoLessItemID))
	if reads != 1 {
		t.Fatalf("restricted: reach reads = %d, want 1", reads)
	}
}

// The class check itself, apart from the project-reach gate in front of it.
func TestWithLinkReachBindsOnlyARestrictedCaller(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter(t, &fakeConn{})
	bound := func(principal storage.Principal) bool {
		_, ok := adapter.withLinkReach(context.Background(), "k", principal).Value(linkReachContextKey{}).(*lazyLinkReach)
		return ok
	}
	if !bound(restrictedReachCaller(t)) {
		t.Fatal("restricted: no reach bound")
	}
	for name, principal := range map[string]storage.Principal{
		"unrestricted": userprincipal.Authenticated(t, "org_1", nil),
		"universal":    userprincipal.Authenticated(t, "org_1", []string{"*"}),
		"project only": userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"}),
	} {
		if bound(principal) {
			t.Fatalf("%s: a reach bound", name)
		}
	}
	// A call inside a call reads the reach once.
	first := adapter.withLinkReach(context.Background(), "k", restrictedReachCaller(t))
	second := adapter.withLinkReach(first, "k", restrictedReachCaller(t))
	if first.Value(linkReachContextKey{}) != second.Value(linkReachContextKey{}) {
		t.Fatal("a nested call bound a second reach")
	}
}

// A reach is one caller's grant. A context that passes to another caller must
// not lend it: another scope, another organization or another graph reads again.
func TestAHeldReachIsReusedOnlyByTheSameCaller(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter(t, &fakeConn{})
	alpha := userprincipal.Narrowed(t, "org_1", []string{reachInsideRepo}, []string{reachInsideRepo})
	held := adapter.withLinkReach(context.Background(), "k", alpha)
	for name, tc := range map[string]struct {
		key       string
		principal storage.Principal
	}{
		"another scope":        {"k", userprincipal.Narrowed(t, "org_1", []string{"acme/beta"}, []string{"acme/beta"})},
		"a wider scope":        {"k", userprincipal.Narrowed(t, "org_1", []string{reachInsideRepo, "acme/beta"}, []string{reachInsideRepo, "acme/beta"})},
		"another organization": {"k", userprincipal.Narrowed(t, "org_2", []string{reachInsideRepo}, []string{reachInsideRepo})},
		"another graph":        {"other", alpha},
	} {
		if adapter.withLinkReach(held, tc.key, tc.principal).Value(linkReachContextKey{}) == held.Value(linkReachContextKey{}) {
			t.Fatalf("%s: the held reach was lent", name)
		}
	}
}

// One line per read, whatever else the call reads, and none for a caller with no
// reach. The line carries counts and a closed value only.
func TestTheReachReportsOneLinePerRead(t *testing.T) {
	t.Parallel()
	principal := restrictedReachCaller(t)
	full := make([]linkEdgeRow, 0, linkReachMaxRows+1)
	for i := range linkReachMaxRows + 1 {
		full = append(full, linkEdgeRow{fmt.Sprintf("work_item.v2:z:linear:KEY-%06d", i), "native", reachInsideRepo})
	}
	cases := []struct {
		name    string
		reach   func(context.Context, string, map[string]interface{}) ([]row, error)
		outcome WorkItemLinkReachOutcome
		rows    int
	}{
		{"read", func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
			return reachRows(t, []linkEdgeRow{{repoLessItemID, "native", reachInsideRepo}, {"work_item.v2:z:linear:OTHER-1", "native", reachInsideRepo}}, cypher, params), nil
		}, WorkItemLinkReachRead, 2},
		{"empty", func(context.Context, string, map[string]interface{}) ([]row, error) { return nil, nil }, WorkItemLinkReachRead, 0},
		{"cut", func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
			return reachRows(t, full, cypher, params), nil
		}, WorkItemLinkReachCut, linkReachMaxRows},
		{"failed", func(context.Context, string, map[string]interface{}) ([]row, error) {
			return nil, errors.New("timeout")
		}, WorkItemLinkReachFailed, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			fake := &fakeConn{linkReachFunc: tc.reach, queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) { return nil, nil }}
			adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
			ctx, err := adapter.withProjectReach(context.Background(), "k", principal)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true); err != nil {
					t.Fatal(err)
				}
			}
			want := []linkReachRecord{{orgID: "org_1", outcome: tc.outcome, rows: tc.rows}}
			if !slices.Equal(telemetry.linkReaches, want) {
				t.Fatalf("lines = %+v, want %+v", telemetry.linkReaches, want)
			}
		})
	}
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) { return nil, nil }}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	ctx, err := adapter.withProjectReach(context.Background(), "k", userprincipal.Authenticated(t, "org_1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true); err != nil {
		t.Fatal(err)
	}
	if len(telemetry.linkReaches) != 0 {
		t.Fatalf("unrestricted: lines = %+v, want none", telemetry.linkReaches)
	}
}

// The real producer into a real slog.JSONHandler, against the spec. A failed
// read is loud: it writes a Warn line as well. No id and no slug on any line.
func TestTheReachLineCertifiesFromTheRealProducer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome WorkItemLinkReachOutcome
		rows    int
		warn    bool
	}{{WorkItemLinkReachRead, 3, false}, {WorkItemLinkReachCut, linkReachMaxRows, false}, {WorkItemLinkReachFailed, 0, true}} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			telemetry.RecordWorkItemLinkReach(context.Background(), "org_1", tc.outcome, tc.rows, 1500*time.Millisecond)
			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse() error = %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.WorkItemLinkReach,
				Want:  map[string]any{"org_id": "org_1", "outcome": string(tc.outcome), "rows": tc.rows, "elapsed_ms": 1500},
			}); err != nil {
				t.Fatalf("certify.Certify() error = %v", err)
			}
			if got := strings.Contains(buf.String(), `"level":"WARN"`); got != tc.warn {
				t.Fatalf("warn line = %v, want %v: %s", got, tc.warn, buf.String())
			}
		})
	}
}

func TestTheReachOutcomeVocabularyIsTheProducers(t *testing.T) {
	t.Parallel()
	for _, field := range eventspec.WorkItemLinkReach.Fields {
		if field.Key == "outcome" && !slices.Equal(field.ClosedVocabulary, WorkItemLinkReachOutcomeVocabulary()) {
			t.Fatalf("spec %v, producer %v", field.ClosedVocabulary, WorkItemLinkReachOutcomeVocabulary())
		}
	}
}

// The reach runs at the first read, whatever that read returned, and once. So a
// hidden repo-less node in a page changes no statement.
func TestTheReachReadDoesNotDependOnWhatARowHolds(t *testing.T) {
	t.Parallel()
	statements := func(rows []row) []string {
		var seen []string
		fake := &fakeConn{
			linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
				seen = append(seen, "reach")
				return nil, nil
			},
			queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
				seen = append(seen, "read")
				return rows, nil
			},
		}
		adapter := newFakeAdapter(t, fake)
		ctx, err := adapter.withProjectReach(context.Background(), "k", restrictedReachCaller(t))
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true); err != nil {
				t.Fatal(err)
			}
		}
		return seen
	}
	empty := statements(nil)
	hidden := statements([]row{{"n": repoLessNode("work_item.v2:z:linear:HIDDEN-1")}})
	if !slices.Equal(empty, hidden) || !slices.Equal(empty, []string{"read", "reach", "read"}) {
		t.Fatalf("statements: empty page %v, page with a hidden item %v", empty, hidden)
	}
}

func TestAWriteDoesNotReadTheReach(t *testing.T) {
	t.Parallel()
	reads := 0
	fake := &fakeConn{linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
		reads++
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	ctx, err := adapter.withProjectReach(context.Background(), "k", restrictedReachCaller(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.api.query(ctx, "k", "CREATE (n)", nil, false); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("reach reads = %d after a write", reads)
	}
}

// A failed read admits nothing, keeps the call going, and is reported.
func TestAFailedReachDeniesAndKeepsTheCall(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
		return nil, errors.New("timeout")
	}}
	principal := restrictedReachCaller(t)
	candidate, ctx := readNode(t, fake, principal, repoLessNode(repoLessItemID))
	if graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, candidate.Attributes) {
		t.Fatal("a failed read admitted the item")
	}
	if !linkReachIncomplete(ctx) {
		t.Fatal("the failed read was not reported")
	}
}

// At the cap the ids read stay proven; the rest stay denied, and the read is
// reported as cut.
func TestACutReachKeepsWhatItProved(t *testing.T) {
	t.Parallel()
	edges := make([]linkEdgeRow, 0, linkReachMaxRows+1)
	for i := range linkReachMaxRows + 1 {
		edges = append(edges, linkEdgeRow{fmt.Sprintf("work_item.v2:z:linear:KEY-%06d", i), "native", reachInsideRepo})
	}
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
		return reachRows(t, edges, cypher, params), nil
	}}
	principal := restrictedReachCaller(t)
	first, ctx := readNode(t, fake, principal, repoLessNode(edges[0].item))
	if !graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, first.Attributes) {
		t.Fatal("an id the cut read holds was not admitted")
	}
	if !linkReachIncomplete(ctx) {
		t.Fatal("the cut was not reported")
	}
	last, _ := readNode(t, fake, principal, repoLessNode(edges[linkReachMaxRows].item))
	if graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, last.Attributes) {
		t.Fatal("the id past the cap was admitted")
	}
}

// A stored-subject decision does not read a failed read as a denial.
func TestAFailedReachAtTheStoredGateIsNoDenial(t *testing.T) {
	t.Parallel()
	principal := restrictedReachCaller(t)
	fake := &fakeConn{
		linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
			return nil, errors.New("timeout")
		},
		queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
			return []row{{"n": repoLessNode(repoLessItemID)}}, nil
		},
	}
	subjects := []contextfabric.SubjectRef{{Kind: contextfabric.SubjectWorkItem, CanonicalID: repoLessItemID}}
	binding := contextfabric.ResolvedGraphBinding{GraphKey: "acr-cf-fake-key", Epoch: 1}
	if _, err := newFakeAdapter(t, fake).AuthorizeStoredSubjects(context.Background(), principal, binding, subjects); err == nil {
		t.Fatal("a failed link read decided a repo-less item")
	}
	// A pull request's decision does not depend on the link read.
	fake.queryFunc = func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return []row{{"n": &node{Properties: map[string]interface{}{propKind: string(contextfabric.SubjectPullRequest), propCanonicalID: "pr:1", propAuthzRepos: []string{reachInsideRepo}}}}}, nil
	}
	outcomes, err := newFakeAdapter(t, fake).AuthorizeStoredSubjects(context.Background(), principal, binding, []contextfabric.SubjectRef{{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pr:1"}})
	if err != nil || len(outcomes) != 1 || outcomes[0] != contextfabric.StoredSubjectAdmitted {
		t.Fatalf("pull request: outcomes %v err %v, want it admitted", outcomes, err)
	}
}

// The stored gate reads the reach, so a committed linked item survives the
// recheck.
func TestAStoredLinkedItemIsAdmitted(t *testing.T) {
	t.Parallel()
	principal := restrictedReachCaller(t)
	fake := &fakeConn{
		linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
			return reachRows(t, []linkEdgeRow{{repoLessItemID, "native", reachInsideRepo}}, cypher, params), nil
		},
		queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
			return []row{{"n": repoLessNode(repoLessItemID)}}, nil
		},
	}
	binding := contextfabric.ResolvedGraphBinding{GraphKey: "acr-cf-fake-key", Epoch: 1}
	outcomes, err := newFakeAdapter(t, fake).AuthorizeStoredSubjects(context.Background(), principal, binding, []contextfabric.SubjectRef{{Kind: contextfabric.SubjectWorkItem, CanonicalID: repoLessItemID}})
	if err != nil || len(outcomes) != 1 || outcomes[0] != contextfabric.StoredSubjectAdmitted {
		t.Fatalf("outcomes %v err %v, want it admitted", outcomes, err)
	}
}

// The attribute is an input of the gate. It never reaches a candidate, a trace
// or a served subject.
func TestTheReachNeverLeavesTheProcess(t *testing.T) {
	t.Parallel()
	turn := runTriggerTurn(t, triggerRow{id: "pull_request:r-inside:5063", repo: "acme/alpha", number: 5063}, nil)
	if strings.Contains(fmt.Sprintf("%+v", turn), graphrank.AuthorizationLinkRepositoriesAttr) {
		t.Fatalf("turn = %+v", turn)
	}
	principal := restrictedReachCaller(t)
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
		return reachRows(t, []linkEdgeRow{{repoLessItemID, "native", reachInsideRepo}}, cypher, params), nil
	}}
	candidate, _ := readNode(t, fake, principal, repoLessNode(repoLessItemID))
	subject, ok := graphrank.NodeCandidate(principal, contextfabric.RequestedScope{}, "CHAOS-11516", candidate, func(contextfabric.SubjectRef) bool { return false }, true, nil, "")
	if !ok {
		t.Fatal("the linked item was not admitted")
	}
	if strings.Contains(fmt.Sprintf("%+v", subject), reachInsideRepo) {
		t.Fatalf("candidate carries the linked repository: %+v", subject)
	}
}
