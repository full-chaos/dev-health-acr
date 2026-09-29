package directread_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type certifyEdgeGraph struct {
	certifyGraph
	edges []directread.EdgeCandidate
}

func (g certifyEdgeGraph) DirectEdgePage(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, query directread.EdgePageQuery) (directread.EdgePage, error) {
	page := directread.EdgePage{}
	for _, e := range g.edges {
		if query.After != nil && !query.After.Less(e.Key) {
			continue
		}
		if len(page.Edges) == query.Limit {
			page.More = true
			break
		}
		page.Edges = append(page.Edges, e)
	}
	return page, nil
}

// The read line, certified from the bytes the production recorder wrote for
// a real read: a restricted caller, one served edge, one edge withheld by its
// own attributes, one by a hidden source node, a next cursor issued. It must
// carry no subject id, label or relationship id.
func TestDirectRelationshipsReadLineCertifiesAgainstItsSpecification(t *testing.T) {
	root := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:root-secret-id"}
	seen := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item.v2:seen-secret-id"}
	hidden := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item.v2:hidden-secret-id"}
	graph := certifyEdgeGraph{certifyGraph: certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(root):   {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}},
		graphrank.SubjectKey(seen):   {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}},
		graphrank.SubjectKey(hidden): {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/outside-repo"}}}},
	}}}
	edge := func(id string, from contextfabric.SubjectRef, repo string) directread.EdgeCandidate {
		return directread.EdgeCandidate{
			Key: directread.EdgeKey{RelationshipID: id}, RelationType: "BELONGS_TO_REPOSITORY",
			Attributes: map[string]interface{}{"authorization_repositories": []string{repo}, "fact": "Secret Label belongs"},
			From:       directread.EdgeEnd{Subject: from}, To: directread.EdgeEnd{Subject: root},
		}
	}
	graph.edges = []directread.EdgeCandidate{
		edge("rel-secret-1", seen, "acme/inside-repo"),
		edge("rel-secret-2", seen, "acme/outside-repo"),
		edge("rel-secret-3", hidden, "acme/inside-repo"),
		edge("rel-secret-4", seen, "acme/inside-repo"),
	}
	principal := storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/inside-repo"}}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	var buffer bytes.Buffer
	reader, err := directread.NewRelationshipsReader(directread.NewSubjectGate(graph, nil), graph,
		directread.NewSlogRelationshipsRecorder(slog.New(slog.NewJSONHandler(&buffer, nil))),
		directread.CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := reader.Read(ctx, principal, directread.RelationshipsRequest{
		Subject: directread.RelationshipsSubject{Kind: "repository", CanonicalID: root.CanonicalID}, Limit: 3,
	})
	if err != nil || len(response.Edges) != 1 || response.Page.NextCursor == "" {
		t.Fatalf("read: %v %+v", err, response)
	}
	for _, leak := range []string{"secret", "Secret", "acme/"} {
		if strings.Contains(buffer.String(), leak) {
			t.Fatalf("read line leaks %q:\n%s", leak, buffer.String())
		}
	}
	parsed, err := certify.Parse(buffer.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.DirectRelationshipsRead, Want: map[string]any{
		"tool": "read_relationships", "org_id": "org_1", "principal_class": "restricted", "status": "partial",
		"subject_kind": "repository", "depth": 1, "hop": 1, "type_count": 0, "direction": "both", "window_mode": "current",
		"edges_examined": 3, "edges_returned": 1, "edges_not_visible": 2,
		"edges_withheld_reasons":    []any{"edge_attributes", "source_not_visible"},
		"edges_withheld_attributes": 1, "edges_withheld_source": 1, "edges_withheld_target": 0,
		"evidence_refs_withheld": 0, "end_nodes_gated": 3, "end_nodes_refused": 1,
		"cursor_out": "issued", "request_id": "req_0123456789abcdef0123456789abcdef",
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, buffer.String())
	}
}
