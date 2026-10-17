package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func tieredLink(rid string, issue, pull contextfabric.SubjectRef, tier string, rank int64, observed time.Time) contextfabric.RelationshipProjection {
	edge := columnEdge(rid, issue, pull, observed, nil)
	edge.Type = contractsv1.ContextFabricRelationshipLinksPullRequest
	edge.Properties = map[string]contractsv1.ContextFabricScalarValue{
		linkTierProperty: {String: &tier},
		linkRankProperty: {Integer: &rank},
	}
	return edge
}

// linkEdges is every LINKS_PULL_REQUEST edge of the graph as "rid=tier".
func linkEdges(t *testing.T, ctx context.Context, a *Adapter, key string) []string {
	t.Helper()
	rows, err := a.api.query(ctx, key, fmt.Sprintf("MATCH ()-[r:%s]->() WHERE r.%s = 'LINKS_PULL_REQUEST' RETURN r.%s AS rid, r.%s%s AS tier",
		labelRelation, propRelationType, propRelationshipID, propPropertyPrefix, linkTierProperty), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%v=%v", r["rid"], r["tier"]))
	}
	sort.Strings(out)
	return out
}

// A native link retracts every lower-tier link of its issue and of its pull
// request, whenever it arrives and whatever their stamps; it leaves the
// lower-tier links of other issues and pull requests, and an edge without a
// tier, as they are.
func TestLiveNativeLinkRetractsOutrankedLinksOfItsIssueAndPullRequest(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	org := "live-outranked-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })

	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ref := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	issueA, issueB, issueC, issueD := ref(contextfabric.SubjectWorkItem, "work_item:A"), ref(contextfabric.SubjectWorkItem, "work_item:B"), ref(contextfabric.SubjectWorkItem, "work_item:C"), ref(contextfabric.SubjectWorkItem, "work_item:D")
	pullP, pullQ, pullR := ref(contextfabric.SubjectPullRequest, "pull_request:r:1"), ref(contextfabric.SubjectPullRequest, "pull_request:r:2"), ref(contextfabric.SubjectPullRequest, "pull_request:r:3")
	apply := func(id string, rels ...contextfabric.RelationshipProjection) {
		t.Helper()
		if _, err := adapter.ApplyProjectionBatch(ctx, columnEdgeBatch(org, id, t0, rels, []contextfabric.ProjectionTombstone{})); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	untiered := columnEdge("link-B-P-untiered", issueB, pullP, t0, nil)
	untiered.Type = contractsv1.ContextFabricRelationshipLinksPullRequest

	apply("batch-0001",
		tieredLink("link-B-P", issueB, pullP, "heuristic", 1, t0),
		tieredLink("link-A-Q", issueA, pullQ, "explicit_text", 2, t0.Add(time.Hour)),
		tieredLink("link-C-R", issueC, pullR, "heuristic", 1, t0),
		tieredLink("link-D-R", issueD, pullR, "explicit_text", 2, t0),
		untiered,
	)
	if got := fmt.Sprint(linkEdges(t, ctx, adapter, key)); got != "[link-A-Q=explicit_text link-B-P-untiered=<nil> link-B-P=heuristic link-C-R=heuristic link-D-R=explicit_text]" {
		t.Fatalf("seed: %s", got)
	}

	// A -> P arrives native, stamped before the lower-tier edges it covers.
	apply("batch-0002", tieredLink("link-A-P", issueA, pullP, "native", 3, t0.Add(-time.Hour)))
	if got := fmt.Sprint(linkEdges(t, ctx, adapter, key)); got != "[link-A-P=native link-B-P-untiered=<nil> link-C-R=heuristic link-D-R=explicit_text]" {
		t.Fatalf("after the native link A -> P: %s, want B -> P (same pull request) and A -> Q (same issue) retracted, the rest kept", got)
	}

	// A lower-tier link on the pair itself becomes native under its own id.
	apply("batch-0003", tieredLink("link-C-R", issueC, pullR, "native", 3, t0))
	if got := fmt.Sprint(linkEdges(t, ctx, adapter, key)); got != "[link-A-P=native link-B-P-untiered=<nil> link-C-R=native]" {
		t.Fatalf("after C -> R turned native: %s, want C -> R kept as native and D -> R retracted", got)
	}

	// The count the debug line reports is the edges deleted, each once.
	apply("batch-0005", tieredLink("link-B-Q", issueB, pullQ, "heuristic", 1, t0), tieredLink("link-D-Q", issueD, pullQ, "explicit_text", 2, t0))
	native := columnEdgeBatch(org, "batch-0006", t0, []contextfabric.RelationshipProjection{tieredLink("link-B-R", issueB, pullR, "native", 3, t0)}, []contextfabric.ProjectionTombstone{})
	if natives, retracted, err := adapter.deleteOutrankedLinks(ctx, key, native); err != nil || natives != 1 || retracted != 1 {
		t.Fatalf("deleteOutrankedLinks = %d natives, %d retracted, %v; want 1 native and B -> Q retracted once", natives, retracted, err)
	}
	if got := fmt.Sprint(linkEdges(t, ctx, adapter, key)); got != "[link-A-P=native link-B-P-untiered=<nil> link-C-R=native link-D-Q=explicit_text]" {
		t.Fatalf("after B's native link: %s", got)
	}

	// A native link never retracts another native link; D's own lower-tier
	// link to Q goes.
	apply("batch-0007", tieredLink("link-D-R", issueD, pullR, "native", 3, t0))
	if got := fmt.Sprint(linkEdges(t, ctx, adapter, key)); got != "[link-A-P=native link-B-P-untiered=<nil> link-C-R=native link-D-R=native]" {
		t.Fatalf("a second native link of R: %s, want C -> R kept and D -> Q retracted", got)
	}
}
