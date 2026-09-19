package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// certifyGraph holds one node per subject key; the decision is the shared
// graphrank predicate.
type certifyGraph struct {
	contextfabric.GraphReader
	nodes map[string][]graphrank.CandidateNode
}

func (certifyGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "certify", Epoch: 1}, nil
}

func (g certifyGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, g.nodes), nil
}

// The engine-side decision line, certified from the bytes the production
// SlogEngineTelemetry wrote for a real gate decision on a prior result.
func TestStoredResultAuthorizationEngineLineCertifiesAgainstItsSpecification(t *testing.T) {
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:out", Label: "Out"}
	org := contextfabric.SubjectRef{Kind: "organization", CanonicalID: "organization:org_1", Label: "Org"}
	graph := certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(member): {{Attributes: map[string]interface{}{"authorization_repositories": []string{"other-org/secret"}}}},
	}}
	result := contextfabric.InvestigationResult{}
	result.SubjectResolution.Committed = []contextfabric.SubjectRef{member, org}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools", "acme/web", "acme/api"}}
	decision := contextfabric.NewStoredResultGate(graph).Authorize(context.Background(), principal, contextfabric.StoredInvestigationResult{Result: result}, contextfabric.StoredResultSurfacePriorResult)

	var buffer bytes.Buffer
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buffer, nil))).RecordStoredResultAuthorization(ctx, principal, decision)
	parsed, err := certify.Parse(buffer.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.StoredResultAuthorization, Want: map[string]any{
		"org_id": "org_1", "surface": "prior_result", "principal_scope": "restricted", "repository_scope_count": 3,
		"decision": "denied", "reason": "subject_denied", "subject_count": 2, "graph_subject_count": 1, "unkinded_subject_count": 0,
		"admitted_count": 0, "denied_count": 1, "absent_count": 0, "organization_subject_count": 1, "organization_mismatch_count": 0,
		"group_count": 0, "group_unproven_count": 0, "refused_kinds": []any{"repository"}, "request_id": "req_0123456789abcdef0123456789abcdef",
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, buffer.String())
	}
}
