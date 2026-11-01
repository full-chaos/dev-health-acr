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

// CHAOS-12300: the stored-result line of a project-only reader carries the
// grant class, the project count, the reader's digest class and the
// grant-equality result, certified from the bytes production wrote. No
// project id and no digest value reaches it.
func TestAProjectOnlyStoredResultLineCarriesItsGrantBasis(t *testing.T) {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:p-1", Label: "P"}
	graph := certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(project): {{Attributes: map[string]interface{}{"subject_kind": "project", "canonical_id": "project.v2:linear:P-1"}}},
	}}
	result := contextfabric.InvestigationResult{}
	result.SubjectResolution.Committed = []contextfabric.SubjectRef{project}
	principal := storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:linear:P-1"}}
	digest := contextfabric.StoredResultGrantDigest(principal)
	decision := contextfabric.NewStoredResultGate(graph).Authorize(context.Background(), principal, contextfabric.StoredInvestigationResult{Result: result, GrantDigest: digest}, contextfabric.StoredResultSurfaceResultByID)
	var buffer bytes.Buffer
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buffer, nil))).RecordStoredResultAuthorization(ctx, principal, decision)
	for _, leak := range []string{"P-1", digest[2:]} {
		if bytes.Contains(buffer.Bytes(), []byte(leak)) {
			t.Fatalf("the line leaks %q:\n%s", leak, buffer.String())
		}
	}
	parsed, err := certify.Parse(buffer.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.StoredResultAuthorization, Want: map[string]any{
		"org_id": "org_1", "surface": "result_by_id", "principal_scope": "restricted",
		"grant_class": "project_only", "project_scope_count": 1, "grant_digest_class": "project",
		"stored_grant_digest_class": "project", "grant_digest_equal": true, "decision": "admitted",
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, buffer.String())
	}
}
