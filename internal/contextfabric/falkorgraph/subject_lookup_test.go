package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func lookupRow(org, kind, id, label string, extra map[string]interface{}) row {
	properties := map[string]interface{}{propOrgID: org, propKind: kind, propCanonicalID: id, propLabel: label, propAuthzRepos: []string{"acme/api"}}
	for k, v := range extra {
		properties[k] = v
	}
	return row{"n": &node{Properties: properties}}
}

var lookupBinding = contextfabric.ResolvedGraphBinding{GraphKey: "acr-cf-fake-key", Epoch: 1}

// The list read is a keyset page: ordered by canonical id, after the cursor
// id, one row more than the page size to learn whether more follows.
func TestListSubjectsByKindKeysetPage(t *testing.T) {
	var cyphers []string
	var afters []interface{}
	fake := &fakeConn{queryFunc: func(_ context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if key != lookupBinding.GraphKey || !readOnly || params["org"] != "org-1" || params["kind"] != "repository" {
			t.Fatalf("query key=%s readOnly=%t params=%v", key, readOnly, params)
		}
		cyphers = append(cyphers, cypher)
		afters = append(afters, params["after"])
		after, _ := params["after"].(string)
		var rows []row
		for _, id := range []string{"repository:a", "repository:b", "repository:c"} {
			if id > after {
				rows = append(rows, lookupRow("org-1", "repository", id, "label-"+id, nil))
			}
		}
		if len(rows) > 3 {
			rows = rows[:3]
		}
		return rows, nil
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	page, err := adapter.ListSubjectsByKind(context.Background(), principal, lookupBinding, "repository", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !page.More || len(page.Nodes) != 2 || page.Nodes[0].CanonicalID != "repository:a" || page.Nodes[1].CanonicalID != "repository:b" {
		t.Fatalf("page 1 = %+v", page)
	}
	if !strings.Contains(cyphers[0], "ORDER BY n.canonical_id LIMIT 3") || strings.Contains(cyphers[0], "$after") || afters[0] != nil {
		t.Fatalf("first query = %s after=%v", cyphers[0], afters[0])
	}
	node := page.Nodes[0]
	if node.Kind != "repository" || node.Label != "label-repository:a" || node.Match != "" || !slices.Equal(node.Attributes[propAuthzRepos].([]string), []string{"acme/api"}) {
		t.Fatalf("node = %+v (stored id and authorization attributes must travel)", node)
	}
	page, err = adapter.ListSubjectsByKind(context.Background(), principal, lookupBinding, "repository", "repository:b", 2)
	if err != nil || page.More || len(page.Nodes) != 1 || page.Nodes[0].CanonicalID != "repository:c" {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	if !strings.Contains(cyphers[1], "n.canonical_id > $after") || afters[1] != "repository:b" {
		t.Fatalf("second query = %s after=%v", cyphers[1], afters[1])
	}
}

// The read is keyed by organization: an organization sees only its own rows,
// and a row of another organization or kind that a store answers is dropped.
func TestListSubjectsByKindNeverReturnsAnotherOrganizationsNode(t *testing.T) {
	store := map[string][]row{
		"org-1": {lookupRow("org-1", "repository", "repository:one", "one", nil)},
		"org-2": {lookupRow("org-2", "repository", "repository:two", "two", nil)},
	}
	fake := &fakeConn{queryFunc: func(_ context.Context, _, _ string, params map[string]interface{}, _ bool) ([]row, error) {
		org := params["org"].(string)
		rows := append([]row{}, store[org]...)
		// A leaking store: it also answers the other organization's row and a
		// row of another kind.
		rows = append(rows, store["org-2"]...)
		rows = append(rows, lookupRow(org, "team", "team:x", "x", nil))
		return rows, nil
	}}
	adapter := newFakeAdapter(t, fake)
	page, err := adapter.ListSubjectsByKind(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "repository", "", 50)
	if err != nil || len(page.Nodes) != 1 || page.Nodes[0].CanonicalID != "repository:one" {
		t.Fatalf("org-1 list = %+v, %v", page, err)
	}
	found, err := adapter.FindSubjectsByExactName(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "two", []string{"repository"})
	if err != nil || len(found.Nodes) != 0 {
		t.Fatalf("org-1 name lookup of org-2's label = %+v, %v", found, err)
	}
}

func TestListSubjectsByKindInputDomain(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		t.Fatal("an invalid call reached the store")
		return nil, nil
	}})
	ctx := context.Background()
	if _, err := adapter.ListSubjectsByKind(ctx, storage.Principal{OrgID: " "}, lookupBinding, "repository", "", 5); !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Errorf("blank org error = %v", err)
	}
	principal := storage.Principal{OrgID: "org-1"}
	for _, size := range []int{0, -1, directread.MaxLookupPageSize + 1} {
		if _, err := adapter.ListSubjectsByKind(ctx, principal, lookupBinding, "repository", "", size); err == nil {
			t.Errorf("page size %d accepted", size)
		}
	}
	if _, err := adapter.ListSubjectsByKind(ctx, principal, lookupBinding, "person", "", 5); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := adapter.FindSubjectsByExactName(ctx, principal, lookupBinding, "x", []string{"person"}); err == nil {
		t.Error("unknown kind accepted by name read")
	}
	if page, err := adapter.FindSubjectsByExactName(ctx, principal, lookupBinding, "  ", nil); err != nil || len(page.Nodes) != 0 {
		t.Errorf("blank query = %+v, %v", page, err)
	}
	failing := newFakeAdapter(t, &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return nil, errors.New("store down")
	}})
	if _, err := failing.ListSubjectsByKind(ctx, principal, lookupBinding, "repository", "", 5); err == nil {
		t.Error("store error swallowed by list")
	}
	if _, err := failing.FindSubjectsByExactName(ctx, principal, lookupBinding, "x", []string{"repository"}); err == nil {
		t.Error("store error swallowed by name read")
	}
}

// Name read: the equality and its classes are graphrank's exact-name rule
// (label, alias, provider key; trimmed and case folded); a near name is no
// match; results come back ordered by canonical id with the stored id intact.
func TestFindSubjectsByExactNameClassesAndOrder(t *testing.T) {
	var queried []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		queried = append(queried, params["kind"].(string))
		low := strings.ToLower(cypher)
		if strings.Contains(low, "vector") || strings.Contains(low, "embedding") || strings.Contains(low, "fulltext") {
			t.Fatalf("name read used a model or ranked arm: %s", cypher)
		}
		switch params["kind"] {
		case "repository":
			return []row{
				lookupRow("org-1", "repository", "repository:z-label", "Acme/API", nil),
				lookupRow("org-1", "repository", "repository:m-alias", "other", map[string]interface{}{propAliases: []string{"acme/api"}}),
				lookupRow("org-1", "repository", "repository:a-prov", "another", map[string]interface{}{propProviderAliases: []string{"ACME/API"}}),
				lookupRow("org-1", "repository", "repository:near", "acme/api-2", nil),
			}, nil
		case "project":
			return []row{lookupRow("org-1", "project", "project.v2:p", "acme/api", nil)}, nil
		}
		return nil, nil
	}}
	page, err := newFakeAdapter(t, fake).FindSubjectsByExactName(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "  acme/API ", []string{"repository", "project", "repository"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(queried, []string{"repository", "project"}) {
		t.Fatalf("queried kinds = %v (one bounded read per distinct kind)", queried)
	}
	type got struct{ id, match string }
	var have []got
	for _, node := range page.Nodes {
		have = append(have, got{node.CanonicalID, node.Match})
	}
	want := []got{{"project.v2:p", "exact"}, {"repository:a-prov", "provider_key"}, {"repository:m-alias", "alias"}, {"repository:z-label", "exact"}}
	if !slices.Equal(have, want) || page.Truncated {
		t.Fatalf("nodes = %v truncated=%t, want %v", have, page.Truncated, want)
	}
}

// A kind pool past the census bound is disclosed as truncated.
func TestFindSubjectsByExactNameDisclosesTruncation(t *testing.T) {
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if !strings.Contains(cypher, fmt.Sprintf("LIMIT %d", exactNameCandidateQueryLimit+1)) {
			t.Fatalf("pool is not bounded: %s", cypher)
		}
		rows := make([]row, 0, exactNameCandidateQueryLimit+1)
		for index := 0; index <= exactNameCandidateQueryLimit; index++ {
			rows = append(rows, lookupRow("org-1", "repository", fmt.Sprintf("repository:%05d", index), "n", nil))
		}
		return rows, nil
	}}
	page, err := newFakeAdapter(t, fake).FindSubjectsByExactName(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "n", []string{"repository"})
	if err != nil || !page.Truncated || len(page.Nodes) != exactNameCandidateQueryLimit {
		t.Fatalf("truncated pool: %d nodes truncated=%t, %v", len(page.Nodes), page.Truncated, err)
	}
}

// One default kind set on both sides of the seam.
func TestDefaultExactNameKindsAgreeAcrossTheSeam(t *testing.T) {
	if !slices.Equal(DefaultExactNameKinds(), directread.DefaultLookupNameKinds()) || !slices.Equal(DefaultExactNameKinds(), exactNameKinds) {
		t.Fatalf("defaults differ: %v %v %v", DefaultExactNameKinds(), directread.DefaultLookupNameKinds(), exactNameKinds)
	}
	kinds := DefaultExactNameKinds()
	kinds[0] = "mutated"
	if exactNameKinds[0] == "mutated" {
		t.Fatal("DefaultExactNameKinds returns the shared slice")
	}
}

// The name read pushes the equality into the store query: the term travels as
// a parameter, the predicate names label, aliases and provider aliases, and
// the read is bounded by matches, so no window of the kind decides the answer.
func TestFindSubjectsByExactNamePushesTheEqualityIntoTheQuery(t *testing.T) {
	var cypher string
	var params map[string]interface{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _, c string, p map[string]interface{}, _ bool) ([]row, error) {
		cypher, params = c, p
		return nil, nil
	}}
	if _, err := newFakeAdapter(t, fake).FindSubjectsByExactName(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "  Acme/API ", []string{"repository"}); err != nil {
		t.Fatal(err)
	}
	if params["term"] != "Acme/API" || params["termLower"] != "acme/api" {
		t.Fatalf("params = %v", params)
	}
	for _, want := range []string{"$term", "$termLower", "n.label", "n.aliases", "n.provider_aliases", fmt.Sprintf("LIMIT %d", exactNameCandidateQueryLimit+1)} {
		if !strings.Contains(cypher, want) {
			t.Fatalf("query lacks %q: %s", want, cypher)
		}
	}
}
