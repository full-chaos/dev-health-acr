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

type certifyGraph struct {
	nodes map[string][]graphrank.CandidateNode
	err   error
}

func (certifyGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "certify", Epoch: 1}, nil
}

func (g certifyGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	if g.err != nil {
		return nil, g.err
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, g.nodes), nil
}

func (certifyGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

// The decision line, certified from the bytes the production SlogRecorder
// wrote for real gate decisions. It must carry no subject id, label or
// repository name.
func TestDirectReadAuthorizationLineCertifiesAgainstItsSpecification(t *testing.T) {
	inside := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:inside-secret-id", Label: "Inside Label"}
	outside := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:outside-secret-id"}
	project := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project:wild-secret-id"}
	graph := certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(inside):  {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}},
		graphrank.SubjectKey(outside): {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/outside-repo"}}}},
		graphrank.SubjectKey(project): {{Attributes: map[string]interface{}{"authorization_repositories": "*"}}},
	}}
	principal := storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/inside-repo"}}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")

	var buffer, failed bytes.Buffer
	directread.NewSubjectGate(graph, directread.NewSlogRecorder(slog.New(slog.NewJSONHandler(&buffer, nil)))).
		Authorize(ctx, principal, []contextfabric.SubjectRef{inside, outside, project, {Kind: "organization", CanonicalID: "org_2"}})
	directread.NewSubjectGate(certifyGraph{err: context.DeadlineExceeded}, directread.NewSlogRecorder(slog.New(slog.NewJSONHandler(&failed, nil)))).
		Authorize(ctx, principal, []contextfabric.SubjectRef{inside})

	for _, leak := range []string{"secret-id", "Inside Label", "acme/", "org_2"} {
		if strings.Contains(buffer.String()+failed.String(), leak) {
			t.Fatalf("decision line leaks %q:\n%s%s", leak, buffer.String(), failed.String())
		}
	}
	certifyLine(t, buffer.Bytes(), map[string]any{
		"org_id": "org_1", "principal_class": "restricted", "repository_scope_count": 1,
		"decision": "partial", "reason": "organization_mismatch", "subject_count": 4,
		"admitted_count": 1, "denied_count": 1, "absent_count": 0, "ownership_unproven_count": 1,
		"organization_mismatch_count": 1, "invalid_count": 0,
		"refused_kinds": []any{"organization", "project", "repository"}, "request_id": "req_0123456789abcdef0123456789abcdef",
	})
	certifyLine(t, failed.Bytes(), map[string]any{
		"org_id": "org_1", "principal_class": "restricted", "repository_scope_count": 1,
		"decision": "unavailable", "reason": "graph_read_failed", "subject_count": 1,
		"admitted_count": 0, "denied_count": 0, "absent_count": 0, "ownership_unproven_count": 0,
		"organization_mismatch_count": 0, "invalid_count": 0, "refused_kinds": []any{},
		"error_class": "deadline_exceeded", "request_id": "req_0123456789abcdef0123456789abcdef",
	})
}

func certifyLine(t *testing.T, line []byte, want map[string]any) {
	t.Helper()
	parsed, err := certify.Parse(line)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.DirectReadAuthorization, Want: want}); err != nil {
		t.Fatalf("certify: %v\n%s", err, line)
	}
}
