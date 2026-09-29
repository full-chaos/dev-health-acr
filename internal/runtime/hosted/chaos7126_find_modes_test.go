package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7126: the production graph serves both new modes' graph reads; a
// graph that cannot, or no ClickHouse client, leaves the mode off and says
// so loudly.
func TestChaos7126ComposeFindModes(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	var _ directread.EdgeGraph = adapter
	var _ directread.SubjectNodeReader = adapter
	gate := directread.NewSubjectGate(adapter, nil)
	var logs bytes.Buffer
	composeFindModes(directread.NewSubjectLookup(adapter, gate, nil), chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if strings.Contains(logs.String(), "mode=owned_by") || !strings.Contains(logs.String(), "mode=handle") {
		t.Fatalf("no ClickHouse client: want handle off and owned_by on, logs %q", logs.String())
	}
	logs.Reset()
	composeFindModes(directread.NewSubjectLookup(adapter, gate, nil), chaos7071Investigator{graph: chaos7071PlainGraph{}}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if !strings.Contains(logs.String(), "mode=owned_by") {
		t.Fatalf("graph without edge pages: logs %q", logs.String())
	}
	composeFindModes(nil, nil, nil, nil) // no lookup: nothing to compose, no panic
}

// chaos7126Graph is an engine graph that also serves the direct reads: one
// team owning one repository through one OWNED_BY_TEAM edge.
type chaos7126Graph struct{ contextfabric.GraphReader }

func (chaos7126Graph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, nil
}
func (chaos7126Graph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range out {
		out[i] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}
func (chaos7126Graph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}
func (chaos7126Graph) ListSubjectsByKind(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, string, int) (directread.LookupPage, error) {
	return directread.LookupPage{}, nil
}
func (chaos7126Graph) FindSubjectsByExactName(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, []string) (directread.LookupPage, error) {
	return directread.LookupPage{}, nil
}
func (chaos7126Graph) ReadSubjectNodes(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, []contextfabric.SubjectRef) ([]directread.LookupNode, error) {
	return nil, nil
}
func (chaos7126Graph) DirectEdgePage(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, query directread.EdgePageQuery) (directread.EdgePage, error) {
	if query.After != nil {
		return directread.EdgePage{}, nil
	}
	return directread.EdgePage{Edges: []directread.EdgeCandidate{{
		Key: directread.EdgeKey{RelationshipID: "rel-1"}, RelationType: "OWNED_BY_TEAM",
		Attributes: map[string]interface{}{"authorization_repositories": "*"},
		From:       directread.EdgeEnd{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:a"}, Attributes: map[string]interface{}{"label": "acme/a"}},
		To:         directread.EdgeEnd{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t"}},
	}}}, nil
}

// r1 P3 (permanent): the composition is proven by what it serves, not by its
// log: owned_by answers through the composed lookup; handle, composed with no
// ClickHouse client, answers unavailable.
func TestChaos7126_R1_ComposedLookupServesOwnedBy(t *testing.T) {
	graph := chaos7126Graph{}
	gate := directread.NewSubjectGate(graph, nil)
	lookup := directread.NewSubjectLookup(graph, gate, nil)
	composeFindModes(lookup, chaos7071Investigator{graph: graph, facts: chaos7071Facts{}}, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	principal := storage.Principal{OrgID: "org", Subject: "u", CredentialID: "c"}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	response, err := lookup.Find(ctx, principal, directread.FindRequest{OwnedBy: "team:t"})
	if err != nil || response.Status != directread.FindComplete || len(response.Subjects) != 1 || response.Subjects[0].CanonicalID != "repository:a" {
		t.Fatalf("owned_by through the composed lookup: %v %+v", err, response)
	}
	if _, err := lookup.Find(ctx, principal, directread.FindRequest{Handle: "PR 1"}); !errors.Is(err, directread.ErrFindUnavailable) {
		t.Fatalf("handle without a census: %v", err)
	}
}
