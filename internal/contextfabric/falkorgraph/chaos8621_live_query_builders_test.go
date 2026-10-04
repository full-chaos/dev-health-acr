package falkorgraph

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// These tests run the query builders that used to run only over the
// connection double against a real graph store. A double does not parse the
// query, so invalid Cypher passed every test. Like every live test in this
// package, they start a container through testcontainers and FAIL when it
// cannot start: a missing container is a red test, never a skip.

// builderFixtureBatch projects, for one organization: three projects (two
// named for auth, one for billing, with embeddings by keyword), one team and
// one repository. Projected out of canonical-id order on purpose.
func builderFixtureBatch(orgID string, observed time.Time) contextfabric.ProjectionBatch {
	authz := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	entity := func(kind contextfabric.SubjectKind, id, label string, aliases []string) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: label},
			Aliases: aliases, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: authz, EvidenceRefIDs: []string{"evidence_builders_" + strings.ReplaceAll(id, ":", "_")},
			ObservedAt: observed, SourceVersion: "v1",
		}
	}
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_builders_00000001", OrgID: orgID,
		Source: "builders-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			entity(contextfabric.SubjectProject, "project:c", "Billing Ledger", nil),
			entity(contextfabric.SubjectProject, "project:a", "Authentication Service", []string{"login-service"}),
			entity(contextfabric.SubjectTeam, "team:a", "Auth Team", nil),
			entity(contextfabric.SubjectRepository, "repository:a", "acme/api", nil),
			entity(contextfabric.SubjectProject, "project:b", "Auth Gateway", nil),
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

// keywordEmbedder maps a text to an axis by keyword: auth -> x, billing -> y,
// anything else -> w, orthogonal to both.
type keywordEmbedder struct{}

func (keywordEmbedder) Identity() contextfabric.EmbedderIdentity {
	return contextfabric.EmbedderIdentity{Provider: "keyword", Model: "keyword-probe", Dimension: 4}
}

func (keywordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		lower := strings.ToLower(text)
		switch {
		case strings.Contains(lower, "auth") || strings.Contains(lower, "login"):
			out[i] = []float32{1, 0, 0, 0}
		case strings.Contains(lower, "billing"):
			out[i] = []float32{0, 1, 0, 0}
		default:
			out[i] = []float32{0, 0, 0, 1}
		}
	}
	return out, nil
}

func TestTheBuilderFixtureIsAValidProjectionBatch(t *testing.T) {
	if err := builderFixtureBatch("org-1", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)).Validate(); err != nil {
		t.Fatalf("fixture is not a valid projection batch: %v", err)
	}
}

func ids(nodes []directread.LookupNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.CanonicalID)
	}
	return out
}

func requireIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// TestLiveSubjectReadBuildersReturnTheSeededRows runs ListSubjectsByKind,
// FindSubjectsByExactName (exactNameKindPool), ReadSubjectNodes and CountKind
// on a real graph store and asserts the rows.
func TestLiveSubjectReadBuildersReturnTheSeededRows(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-builders-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	if _, err := adapter.ApplyProjectionBatch(ctx, builderFixtureBatch(orgID, time.Now().UTC())); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}

	// ListSubjectsByKind: a keyset page in canonical-id order, then the rest.
	page, err := adapter.ListSubjectsByKind(ctx, principal, binding, "project", "", 2)
	if err != nil {
		t.Fatalf("ListSubjectsByKind(first page) error = %v", err)
	}
	requireIDs(t, "first page", ids(page.Nodes), "project:a", "project:b")
	if !page.More {
		t.Fatal("first page More = false, want true: a third project follows")
	}
	if page.Nodes[0].Label != "Authentication Service" || page.Nodes[0].Kind != "project" {
		t.Fatalf("first node = %+v, want the stored label and kind", page.Nodes[0])
	}
	page, err = adapter.ListSubjectsByKind(ctx, principal, binding, "project", "project:b", 2)
	if err != nil {
		t.Fatalf("ListSubjectsByKind(after cursor) error = %v", err)
	}
	requireIDs(t, "second page", ids(page.Nodes), "project:c")
	if page.More {
		t.Fatal("second page More = true, want false")
	}
	page, err = adapter.ListSubjectsByKind(ctx, principal, binding, "team", "", 5)
	if err != nil {
		t.Fatalf("ListSubjectsByKind(team) error = %v", err)
	}
	requireIDs(t, "team page", ids(page.Nodes), "team:a")

	// FindSubjectsByExactName: label equality and alias equality, in the
	// requested kinds only.
	found, err := adapter.FindSubjectsByExactName(ctx, principal, binding, "auth gateway", []string{"project"})
	if err != nil {
		t.Fatalf("FindSubjectsByExactName(label) error = %v", err)
	}
	requireIDs(t, "label match", ids(found.Nodes), "project:b")
	if found.Truncated || found.Nodes[0].Match != "exact" {
		t.Fatalf("label match = %+v, want exact and not truncated", found)
	}
	found, err = adapter.FindSubjectsByExactName(ctx, principal, binding, "login-service", []string{"project"})
	if err != nil {
		t.Fatalf("FindSubjectsByExactName(alias) error = %v", err)
	}
	requireIDs(t, "alias match", ids(found.Nodes), "project:a")
	if found.Nodes[0].Match != "alias" {
		t.Fatalf("alias match class = %q, want alias", found.Nodes[0].Match)
	}
	found, err = adapter.FindSubjectsByExactName(ctx, principal, binding, "Auth Team", []string{"project"})
	if err != nil || len(found.Nodes) != 0 {
		t.Fatalf("a team name searched in the project kind = %+v, %v; want no node", found, err)
	}
	found, err = adapter.FindSubjectsByExactName(ctx, principal, binding, "Auth Team", nil)
	if err != nil {
		t.Fatalf("FindSubjectsByExactName(default kinds) error = %v", err)
	}
	requireIDs(t, "default kinds match", ids(found.Nodes), "team:a")

	// ReadSubjectNodes: present subjects come back, a missing one is absent.
	nodes, err := adapter.ReadSubjectNodes(ctx, principal, binding, []contextfabric.SubjectRef{
		{Kind: contextfabric.SubjectProject, CanonicalID: "project:c"},
		{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:a"},
		{Kind: contextfabric.SubjectProject, CanonicalID: "project:missing"},
		{Kind: contextfabric.SubjectTeam, CanonicalID: "project:a"}, // right id, wrong kind
	})
	if err != nil {
		t.Fatalf("ReadSubjectNodes() error = %v", err)
	}
	got := ids(nodes)
	sort.Strings(got)
	requireIDs(t, "read nodes", got, "project:c", "repository:a")
	for _, n := range nodes {
		if n.CanonicalID == "project:c" && (n.Label != "Billing Ledger" || n.Kind != "project") {
			t.Fatalf("read node = %+v, want the stored label and kind", n)
		}
	}

	// CountKind: an aggregate per kind; an absent kind counts zero.
	for kind, want := range map[contextfabric.SubjectKind]int64{
		contextfabric.SubjectProject: 3, contextfabric.SubjectTeam: 1, contextfabric.SubjectRepository: 1, contextfabric.SubjectIncident: 0,
	} {
		count, err := adapter.CountKind(ctx, orgID, kind)
		if err != nil || count != want {
			t.Fatalf("CountKind(%s) = %d, %v; want %d", kind, count, err, want)
		}
	}
}

// TestLiveConfirmedKindVectorCensusReadsTheSeededVectors runs the census's
// count and fetch reads and the whole census on a real graph store with the
// vector index the image supports (the same image and path as
// TestLiveFetchEmbedderFenceCorpusScopesToOrgAndIdentity).
func TestLiveConfirmedKindVectorCensusReadsTheSeededVectors(t *testing.T) {
	ctx := context.Background()
	adapter := startVectorLiveAdapter(t, keywordEmbedder{}, 0.55)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 100
	orgID := "live-census-builders-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	if _, err := adapter.ApplyProjectionBatch(ctx, builderFixtureBatch(orgID, time.Now().UTC())); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	key := graphKey(adapter.config.GraphPrefix, orgID)
	identity := adapter.stampedEmbedderIdentity(keywordEmbedder{}.Identity())

	count, err := adapter.countKindEmbedderFenceCorpus(ctx, key, orgID, "project", identity)
	if err != nil || count != 3 {
		t.Fatalf("countKindEmbedderFenceCorpus(project) = %d, %v; want 3 (the team's vector is another kind)", count, err)
	}
	if count, err = adapter.countKindEmbedderFenceCorpus(ctx, key, orgID, "project", "other/identity"); err != nil || count != 0 {
		t.Fatalf("countKindEmbedderFenceCorpus(other identity) = %d, %v; want 0", count, err)
	}
	corpus, enumerated, malformed, err := adapter.fetchKindEmbedderFenceCorpus(ctx, key, orgID, "project", identity)
	if err != nil || enumerated != 3 || malformed != 0 || len(corpus) != 3 {
		t.Fatalf("fetchKindEmbedderFenceCorpus(project) = %d rows, enumerated %d, malformed %d, %v; want 3, 3, 0", len(corpus), enumerated, malformed, err)
	}
	var corpusIDs []string
	for _, v := range corpus {
		if len(v.Vector) != 4 || v.Kind != "project" {
			t.Fatalf("decoded row = %+v, want a 4-wide project vector", v)
		}
		corpusIDs = append(corpusIDs, v.CanonicalID)
	}
	requireIDs(t, "fetched ids", corpusIDs, "project:a", "project:b", "project:c")

	outcome := adapter.confirmedKindVectorCensus(ctx, key, orgID, contextfabric.SubjectProject, []string{"auth"})
	if outcome.State != graphrank.ConfirmedKindVectorScopeComplete || outcome.PopulationCount != 3 || outcome.EnumeratedCount != 3 ||
		outcome.QueriesScored != 1 || outcome.RivalCountAboveTau != 2 || !outcome.SnapshotStable {
		t.Fatalf("census = %+v, want complete over 3 projects, 1 query scored, 2 rivals above the floor", outcome)
	}
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 2
	outcome = adapter.confirmedKindVectorCensus(ctx, key, orgID, contextfabric.SubjectProject, []string{"auth"})
	if outcome.State != graphrank.ConfirmedKindVectorScopeOverBudget || outcome.PopulationCount != 3 || outcome.ComparisonCount != 3 {
		t.Fatalf("census over budget = %+v, want over_budget with population 3, comparisons 3", outcome)
	}
}

// cypherShape is a coarse grammar check for the read the builder sent: the
// brackets balance, no two node patterns touch (the defect of the walk read),
// and the clause order is MATCH ... RETURN.
func requireOnePathMatch(t *testing.T, what, cypher string) {
	t.Helper()
	for _, pair := range [][2]string{{"(", ")"}, {"{", "}"}, {"[", "]"}} {
		if strings.Count(cypher, pair[0]) != strings.Count(cypher, pair[1]) {
			t.Fatalf("%s: unbalanced %s%s in %q", what, pair[0], pair[1], cypher)
		}
	}
	if regexp.MustCompile(`\)\s*\(`).MatchString(cypher) {
		t.Fatalf("%s: two node patterns touch in %q", what, cypher)
	}
	if !regexp.MustCompile(`MATCH \(n:Subject( \{[^{}()]*\})?\)( WHERE [^{}]*)? RETURN `).MatchString(cypher) {
		t.Fatalf("%s: read = %q, want MATCH (n:Subject ...) [WHERE ...] RETURN", what, cypher)
	}
}

// TestSubjectReadBuildersSendOnePathMatch pins the grammar of each builder's
// read where no graph store runs; the live test above is the proof of record.
func TestSubjectReadBuildersSendOnePathMatch(t *testing.T) {
	var sent []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		sent = append(sent, cypher)
		return []row{{"total": int64(0)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	ctx := context.Background()
	if _, err := adapter.ListSubjectsByKind(ctx, principal, lookupBinding, "project", "", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListSubjectsByKind(ctx, principal, lookupBinding, "project", "project:a", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.FindSubjectsByExactName(ctx, principal, lookupBinding, "x", []string{"project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CountKind(ctx, "org-1", contextfabric.SubjectProject); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ReadSubjectNodes(ctx, principal, lookupBinding, []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project:a"}}); err != nil {
		t.Fatal(err)
	}
	if len(sent) < 5 {
		t.Fatalf("sent %d reads, want 5", len(sent))
	}
	for i, cypher := range sent {
		requireOnePathMatch(t, fmt.Sprintf("read %d", i), cypher)
	}
}
