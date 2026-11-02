package api

import (
	"net/http"
	"strings"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12619 (codex r3): a result saved before the reader-form labels holds raw
// source ids in its path nodes and why_relevant. Read by id, as the canonical
// document and as the projection (the MCP investigation_result tool reads the
// projection), it serves them in reader words. The stored row keeps them.
func TestAStoredResultReadByIDServesNoRawWorkItemID(t *testing.T) {
	itemID, _, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", "linear:CHAOS-2320"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectWorkItem, CanonicalID: itemID, Label: "linear:CHAOS-2320"}
	pr := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:920f9442-07df-4217-4dc4-c5833c0b8268:5063", Label: "PR #5063"}
	result := threeRollupProjectStatusResult("result_raw_path_12619")
	result.Paths = []v1.ContextFabricRelationshipPath{{
		PathID: "5b1c0a3e-8f61-4f3a-9b5e-0f2a7c1d9e44", Nodes: []v1.ContextFabricSubjectRef{item, pr},
		Edges: []v1.ContextFabricRelationshipEdge{{
			Type: v1.ContextFabricRelationshipLinksPullRequest, From: item, To: pr,
			Derivation: v1.ContextFabricDerivationGraphAssociated, EpistemicStatus: v1.ContextFabricEpistemicInferred, EvidenceRefIDs: []string{"evidence_1"},
		}},
		WhyRelevant: "linear:CHAOS-2320 links pull request PR #5063", EvidenceRefIDs: []string{"evidence_1"},
	}}
	result.Completeness = cf.ComputeAnswerCompleteness(result)
	store := memoryinvestigation.NewStore()
	seeded := seedResult3355(t, store, callerOrgID, result)
	app, token, _ := boundedLabelsApp(t, store, seeded)
	app.config.MaxSerializedBytes = 1 << 22
	for _, query := range []string{"", "view=projection"} {
		read := readByID(t, app, token, seeded.ResultID, query)
		if read.Code != http.StatusOK {
			t.Fatalf("%q: read by id: %d %s", query, read.Code, read.Body.String())
		}
		body := read.Body.String()
		if strings.Contains(body, `"linear:CHAOS-2320`) || !strings.Contains(body, "CHAOS-2320 links pull request PR #5063") {
			t.Errorf("%q: the read serves a raw work item id or loses the reader-form fact:\n%s", query, body)
		}
	}
	stored, err := store.Get(t.Context(), seedPrincipal(callerOrgID), seeded.ResultID)
	if err != nil || stored.Result.Paths[0].Nodes[0].Label != "linear:CHAOS-2320" {
		t.Fatalf("the stored row changed: %v %+v", err, stored.Result.Paths)
	}
}
