package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
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

// reachRows answers the keyed read from edges, by what the statement and its
// parameters carry: the page it keys on, the tier it asks for and the grant it
// matches.
func reachRows(t *testing.T, edges []linkEdgeRow, cypher string, params map[string]interface{}) []row {
	t.Helper()
	keyed := strings.Contains(cypher, ".canonical_id IN $ids")
	wantsNative := strings.Contains(cypher, "$native")
	matchesGrant := strings.Contains(cypher, "$grantRaw")
	page, _ := params["ids"].([]interface{})
	var rows []row
	for _, edge := range edges {
		if keyed && !slices.Contains(page, interface{}(edge.item)) {
			continue
		}
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
	ctx, _, err := adapter.withProjectReach(context.Background(), "k", principal)
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

// The statement is the rule: the page's ids, native, the link relation, repo-less,
// the grant, no temporal term (validity in time is not an authorization input),
// and no cap.
func TestTheLinkReachStatementIsKeyedOnThePage(t *testing.T) {
	t.Parallel()
	var statement string
	var params map[string]interface{}
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, bound map[string]interface{}) ([]row, error) {
		statement, params = cypher, bound
		return nil, nil
	}}
	readNode(t, fake, restrictedReachCaller(t), repoLessNode(repoLessItemID))
	for _, want := range []string{"(w:Subject {org_id:$org,", "(p:Subject {org_id:$org,", "w.canonical_id IN $ids", "r.relation_type = $links", "r.property_link_provenance = $native", "$noRepository IN w.authorization_repositories", "$grantRaw"} {
		if !strings.Contains(statement, want) {
			t.Fatalf("statement %q lacks %q", statement, want)
		}
	}
	if params["native"] != "native" || params["links"] != string(contractsv1.ContextFabricRelationshipLinksPullRequest) || params["noRepository"] != contractsv1.ContextFabricNoRepositorySentinel {
		t.Fatalf("params = %v", params)
	}
	if !slices.Equal(params["ids"].([]interface{}), []interface{}{repoLessItemID}) {
		t.Fatalf("ids = %v", params["ids"])
	}
	if strings.Contains(statement, "valid_") || strings.Contains(statement, "$asOf") {
		t.Fatalf("statement %q carries a temporal term: a merged pull request's link must still count", statement)
	}
	if strings.Contains(statement, "LIMIT") {
		t.Fatalf("statement %q carries a cap", statement)
	}
}

// No cap and no cut: a page past the old bound is read whole, and every item is
// proven.
func TestALargePageIsReadWhole(t *testing.T) {
	t.Parallel()
	const size = 20001
	page := make([]row, 0, size)
	for i := range size {
		page = append(page, row{"n": repoLessNode(fmt.Sprintf("work_item.v2:z:linear:KEY-%06d", i))})
	}
	principal := restrictedReachCaller(t)
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{
		linkReachFunc: func(_ context.Context, _ string, params map[string]interface{}) ([]row, error) {
			rows := make([]row, 0, size)
			for _, id := range params["ids"].([]interface{}) {
				rows = append(rows, row{"id": id, "repos": []interface{}{reachInsideRepo}})
			}
			return rows, nil
		},
		queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) { return page, nil },
	}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	ctx, finish, err := adapter.withProjectReach(context.Background(), "k", principal)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	for _, r := range rows {
		if !graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, toCandidateNode(r["n"].(*node)).Attributes) {
			t.Fatalf("%v was not admitted", r["n"])
		}
	}
	if want := []linkReachRecord{{orgID: "org_1", outcome: WorkItemLinkReachRead, reads: 1, ids: size, rows: size}}; !slices.Equal(telemetry.linkReaches, want) {
		t.Fatalf("lines = %+v, want %+v", telemetry.linkReaches, want)
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
			if got := (linkedRepositories{id: {reachInsideRepo}}).rewrite(n); got != interface{}(n) {
				t.Fatalf("rewrite changed an item with its own repository: %+v", got)
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
	if got := (linkedRepositories{id: {reachInsideRepo}}).rewrite(n); got != interface{}(n) {
		t.Fatalf("rewrite changed a node of another kind: %+v", got)
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
		_, ok := adapter.withLinkReach(context.Background(), "k", principal).Value(linkReachContextKey{}).(*linkReach)
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
	// A call inside a call keeps the caller's reach.
	first := adapter.withLinkReach(context.Background(), "k", restrictedReachCaller(t))
	second := adapter.withLinkReach(first, "k", restrictedReachCaller(t))
	if first.Value(linkReachContextKey{}) != second.Value(linkReachContextKey{}) {
		t.Fatal("a nested call bound a second reach")
	}
}

// A reach is one caller's grant. A context that passes to another caller must
// not lend it: another scope, another organization or another graph binds its own.
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

// reachSubjects is n work items for one AuthorizeStoredSubjects call: a stored
// batch holds 256, so 257 is two wrapped reads.
func reachSubjects(n int) []contextfabric.SubjectRef {
	subjects := make([]contextfabric.SubjectRef, 0, n)
	for i := range n {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item.v2:z:linear:KEY-%06d", i)})
	}
	return subjects
}

// One line per adapter call, adding up its reads, from the real call. It carries
// counts and a closed value only.
func TestTheReachReportsOneLinePerCall(t *testing.T) {
	t.Parallel()
	binding := contextfabric.ResolvedGraphBinding{GraphKey: "acr-cf-fake-key", Epoch: 1}
	repoLess := func(_ context.Context, _ string, _ string, params map[string]interface{}, _ bool) ([]row, error) {
		var rows []row
		for _, target := range params["targets"].([]interface{}) {
			rows = append(rows, row{"n": repoLessNode(target.(map[string]interface{})["id"].(string))})
		}
		return rows, nil
	}
	nothing := func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) { return nil, nil }
	linked := func(_ context.Context, _ string, params map[string]interface{}) ([]row, error) {
		var rows []row
		for _, id := range params["ids"].([]interface{}) {
			rows = append(rows, row{"id": id, "repos": []interface{}{reachInsideRepo}})
		}
		return rows, nil
	}
	failing := func(context.Context, string, map[string]interface{}) ([]row, error) {
		return nil, errors.New("timeout")
	}
	failsFirst := func() func(context.Context, string, map[string]interface{}) ([]row, error) {
		reads := 0
		return func(ctx context.Context, cypher string, params map[string]interface{}) ([]row, error) {
			if reads++; reads == 1 {
				return nil, errors.New("timeout")
			}
			return linked(ctx, cypher, params)
		}
	}
	cases := []struct {
		name  string
		page  func(context.Context, string, string, map[string]interface{}, bool) ([]row, error)
		reach func(context.Context, string, map[string]interface{}) ([]row, error)
		want  linkReachRecord
	}{
		{"read", repoLess, linked, linkReachRecord{orgID: "org_1", outcome: WorkItemLinkReachRead, reads: 2, ids: 257, rows: 257}},
		{"empty pages", nothing, linked, linkReachRecord{orgID: "org_1", outcome: WorkItemLinkReachRead, reads: 2}},
		{"every read failed", repoLess, failing, linkReachRecord{orgID: "org_1", outcome: WorkItemLinkReachFailed, reads: 2, ids: 257, failed: 2}},
		{"one read failed", repoLess, failsFirst(), linkReachRecord{orgID: "org_1", outcome: WorkItemLinkReachFailed, reads: 2, ids: 257, rows: 1, failed: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			adapter := newFakeAdapterWithTelemetry(t, &fakeConn{linkReachFunc: tc.reach, queryFunc: tc.page}, telemetry)
			_, _ = adapter.AuthorizeStoredSubjects(context.Background(), restrictedReachCaller(t), binding, reachSubjects(257))
			if want := []linkReachRecord{tc.want}; !slices.Equal(telemetry.linkReaches, want) {
				t.Fatalf("lines = %+v, want %+v", telemetry.linkReaches, want)
			}
		})
	}
	t.Run("a call that read nothing", func(t *testing.T) {
		t.Parallel()
		telemetry := &recordingTelemetry{}
		adapter := newFakeAdapterWithTelemetry(t, &fakeConn{}, telemetry)
		if _, err := adapter.AuthorizeStoredSubjects(context.Background(), restrictedReachCaller(t), binding, nil); err != nil {
			t.Fatal(err)
		}
		if len(telemetry.linkReaches) != 0 {
			t.Fatalf("lines = %+v, want none", telemetry.linkReaches)
		}
	})
	t.Run("unrestricted", func(t *testing.T) {
		t.Parallel()
		telemetry := &recordingTelemetry{}
		adapter := newFakeAdapterWithTelemetry(t, &fakeConn{queryFunc: repoLess}, telemetry)
		if _, err := adapter.AuthorizeStoredSubjects(context.Background(), userprincipal.Authenticated(t, "org_1", nil), binding, reachSubjects(2)); err != nil {
			t.Fatal(err)
		}
		if len(telemetry.linkReaches) != 0 {
			t.Fatalf("lines = %+v, want none", telemetry.linkReaches)
		}
	})
}

// The elapsed time is the sum of the reads, not the last one.
func TestTheLineAddsUpTheTimeOfTheReads(t *testing.T) {
	t.Parallel()
	reads := 0
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
		if reads++; reads == 1 {
			time.Sleep(40 * time.Millisecond)
		}
		return nil, nil
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	ctx, finish, err := adapter.withProjectReach(context.Background(), "k", restrictedReachCaller(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true); err != nil {
			t.Fatal(err)
		}
	}
	finish()
	if telemetry.linkReachElapsed < 40*time.Millisecond {
		t.Fatalf("elapsed = %v, want the first read's 40ms in it", telemetry.linkReachElapsed)
	}
}

// A call inside a call writes its own reads and the outer one the rest, so no
// read is counted twice. The failure stays past the inner line: the turn still
// owes its sentence.
func TestACallInsideACallWritesItsOwnReads(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	fake := &fakeConn{linkReachFunc: func(context.Context, string, map[string]interface{}) ([]row, error) {
		return nil, errors.New("timeout")
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := restrictedReachCaller(t)
	outer, finishOuter, err := adapter.withProjectReach(context.Background(), "k", principal)
	if err != nil {
		t.Fatal(err)
	}
	inner, finishInner, err := adapter.withProjectReach(outer, "k", principal)
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{inner, inner, outer} {
		if _, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true); err != nil {
			t.Fatal(err)
		}
	}
	finishInner()
	if linkReachError(outer) == nil {
		t.Fatal("the inner line cleared the failure")
	}
	if _, err := adapter.api.query(outer, "k", "MATCH (n) RETURN n", nil, true); err != nil {
		t.Fatal(err)
	}
	finishOuter()
	want := []linkReachRecord{
		{orgID: "org_1", outcome: WorkItemLinkReachFailed, reads: 3, failed: 3},
		{orgID: "org_1", outcome: WorkItemLinkReachFailed, reads: 1, failed: 1},
	}
	if !slices.Equal(telemetry.linkReaches, want) {
		t.Fatalf("lines = %+v, want %+v", telemetry.linkReaches, want)
	}
}

// A caller with no reach still gets a finish, and it writes nothing.
func TestFinishIsSafeForACallerWithNoReach(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, &fakeConn{}, telemetry)
	for _, principal := range []storage.Principal{userprincipal.Authenticated(t, "org_1", nil), userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"})} {
		_, finish, err := adapter.withProjectReach(context.Background(), "k", principal)
		if err != nil {
			t.Fatal(err)
		}
		finish()
	}
	if len(telemetry.linkReaches) != 0 {
		t.Fatalf("lines = %+v, want none", telemetry.linkReaches)
	}
}

// The real producer into a real slog.JSONHandler, against the spec. A call with
// a failed read is loud: it writes a Warn line as well. No id and no slug on any
// line.
func TestTheReachLineCertifiesFromTheRealProducer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tally   WorkItemLinkReachTally
		outcome WorkItemLinkReachOutcome
		warn    bool
	}{
		{WorkItemLinkReachTally{Reads: 27, IDs: 5, Rows: 3, Elapsed: 1500 * time.Millisecond}, WorkItemLinkReachRead, false},
		{WorkItemLinkReachTally{Reads: 27, IDs: 5, Rows: 3, Failed: 1, Elapsed: 1500 * time.Millisecond}, WorkItemLinkReachFailed, true},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			telemetry.RecordWorkItemLinkReach(context.Background(), "org_1", tc.tally)
			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse() error = %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.WorkItemLinkReach,
				Want:  map[string]any{"org_id": "org_1", "outcome": string(tc.outcome), "reads": 27, "ids": 5, "rows": 3, "failed": tc.tally.Failed, "elapsed_ms": 1500},
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

// The differential. One keyed read follows every wrapped read, whatever the page
// held, in one text. A hidden repo-less item changes the id list and nothing else.
func TestAHiddenItemChangesOnlyTheIDList(t *testing.T) {
	t.Parallel()
	type keyedRead struct {
		statement string
		params    map[string]interface{}
	}
	run := func(page []row) ([]keyedRead, []string) {
		var keyed []keyedRead
		var order []string
		fake := &fakeConn{
			linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
				keyed = append(keyed, keyedRead{cypher, params})
				order = append(order, "keyed")
				return nil, nil
			},
			queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
				order = append(order, "read")
				return page, nil
			},
		}
		adapter := newFakeAdapter(t, fake)
		ctx, _, err := adapter.withProjectReach(context.Background(), "k", restrictedReachCaller(t))
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true); err != nil {
				t.Fatal(err)
			}
		}
		return keyed, order
	}
	hiddenID := "work_item.v2:z:linear:HIDDEN-1"
	empty, emptyOrder := run(nil)
	hidden, hiddenOrder := run([]row{{"n": repoLessNode(hiddenID)}})
	repository, _ := run([]row{{"n": &node{Properties: map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectRepository), propCanonicalID: "repository:acme/alpha", propAuthzRepos: []string{reachInsideRepo}}}}})
	want := []string{"read", "keyed", "read", "keyed", "read", "keyed"}
	if !slices.Equal(emptyOrder, want) || !slices.Equal(hiddenOrder, want) {
		t.Fatalf("order: empty page %v, page with a hidden item %v, want %v", emptyOrder, hiddenOrder, want)
	}
	for index := range empty {
		for name, other := range map[string]keyedRead{"hidden": hidden[index], "repository": repository[index]} {
			if other.statement != empty[index].statement {
				t.Fatalf("%s: statement %q, empty page %q", name, other.statement, empty[index].statement)
			}
			left, right := maps.Clone(other.params), maps.Clone(empty[index].params)
			delete(left, "ids")
			delete(right, "ids")
			if !reflect.DeepEqual(left, right) {
				t.Fatalf("%s: params %v, empty page %v", name, left, right)
			}
		}
		ids, ok := empty[index].params["ids"].([]interface{})
		if !ok || ids == nil || len(ids) != 0 {
			t.Fatalf("empty page ids = %#v, want an empty list, not null", empty[index].params["ids"])
		}
		if !slices.Equal(hidden[index].params["ids"].([]interface{}), []interface{}{hiddenID}) {
			t.Fatalf("hidden ids = %v", hidden[index].params["ids"])
		}
	}
}

// The ids are the page's repo-less work items, distinct and sorted, read through
// lists. A work item with a repository, another kind and a nil node are not in it.
func TestThePageIDsAreTheRepoLessWorkItems(t *testing.T) {
	t.Parallel()
	first, second, third := "work_item.v2:z:linear:A-1", "work_item.v2:z:linear:A-2", "work_item.v2:z:linear:A-3"
	withRepository := &node{Properties: map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectWorkItem), propCanonicalID: "work_item.v2:z:gh:acme/alpha#1", propAuthzRepos: []string{reachInsideRepo}}}
	project := &node{Properties: map[string]interface{}{propKind: string(contractsv1.ContextFabricSubjectProject), propCanonicalID: "project:p-1", propAuthzRepos: []string{noRepositoryScope}}}
	var missing *node
	got := repoLessWorkItemIDs([]row{
		{"n": repoLessNode(third), "path": []interface{}{repoLessNode(first), repoLessNode(third), withRepository, []interface{}{repoLessNode(second), project}}},
		{"n": repoLessNode(first), "m": missing, "count": 3},
	})
	if want := []interface{}{first, second, third}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if empty := repoLessWorkItemIDs(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty page = %#v, want an empty list, not null", empty)
	}
}

// A linked item inside a path takes the attribute, and the node read is not
// touched: a cached or shared value stays as it was.
func TestRewriteReadsListsAndCopiesTheNode(t *testing.T) {
	t.Parallel()
	original := repoLessNode(repoLessItemID)
	got := linkedRepositories{repoLessItemID: {reachInsideRepo}}.rewrite([]interface{}{[]interface{}{original}})
	copied := got.([]interface{})[0].([]interface{})[0].(*node)
	if linked, _ := copied.Properties[graphrank.AuthorizationLinkRepositoriesAttr].([]string); !slices.Equal(linked, []string{reachInsideRepo}) {
		t.Fatalf("attribute = %v", copied.Properties)
	}
	if _, held := original.Properties[graphrank.AuthorizationLinkRepositoriesAttr]; held || copied == original {
		t.Fatal("rewrite mutated the node it read")
	}
}

// Each read proves its own page. Nothing one read learned admits a node of the
// next, and a failed read denies its own page only. The failure stays on the call.
func TestEachReadProvesItsOwnPage(t *testing.T) {
	t.Parallel()
	principal := restrictedReachCaller(t)
	first, second := repoLessItemID, "work_item.v2:z:linear:CHAOS-2"
	edges := []linkEdgeRow{{first, "native", reachInsideRepo}, {second, "native", reachInsideRepo}}
	var pages [][]interface{}
	failFirst := true
	fake := &fakeConn{linkReachFunc: func(_ context.Context, cypher string, params map[string]interface{}) ([]row, error) {
		pages = append(pages, params["ids"].([]interface{}))
		if failFirst {
			failFirst = false
			return nil, errors.New("timeout")
		}
		return reachRows(t, edges, cypher, params), nil
	}}
	adapter := newFakeAdapter(t, fake)
	ctx, _, err := adapter.withProjectReach(context.Background(), "k", principal)
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) bool {
		t.Helper()
		fake.queryFunc = func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
			return []row{{"n": repoLessNode(id)}}, nil
		}
		rows, err := adapter.api.query(ctx, "k", "MATCH (n) RETURN n", nil, true)
		if err != nil {
			t.Fatal(err)
		}
		return graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, toCandidateNode(rows[0]["n"].(*node)).Attributes)
	}
	if read(first) {
		t.Fatal("a failed read admitted its page")
	}
	if !read(second) {
		t.Fatal("a failed read denied the next page")
	}
	if !read(first) {
		t.Fatal("the failure of an earlier read denied this page")
	}
	if linkReachError(ctx) == nil {
		t.Fatal("the failed read was not kept for the disclosure")
	}
	if want := [][]interface{}{{first}, {second}, {first}}; !reflect.DeepEqual(pages, want) {
		t.Fatalf("pages = %v, want %v", pages, want)
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
	ctx, _, err := adapter.withProjectReach(context.Background(), "k", restrictedReachCaller(t))
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
	if linkReachError(ctx) == nil {
		t.Fatal("the failed read was not reported")
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
