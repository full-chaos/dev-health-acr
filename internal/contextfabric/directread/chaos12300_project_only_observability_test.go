package directread_test

import (
	"bytes"
	"context"
	"log/slog"
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

const c4Project = "project.v2:linear:P-1"

// CHAOS-12300: the subject gate's Info line carries the grant class and the
// project count beside the decision class, so a project-only decision is
// never read as an ordinary restricted one. Certified from the bytes the
// production recorder wrote; the project id never reaches the line.
func TestAProjectOnlyGateDecisionNamesItsGrantClassAtInfo(t *testing.T) {
	project := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project:p-1"}
	graph := certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(project): {{Attributes: map[string]interface{}{"subject_kind": "project", "canonical_id": c4Project}}},
	}}
	principal := storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c", ProjectScopes: []string{c4Project}}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	var buffer bytes.Buffer
	directread.NewSubjectGate(graph, directread.NewSlogRecorder(slog.New(slog.NewJSONHandler(&buffer, nil)))).
		Authorize(ctx, principal, []contextfabric.SubjectRef{project})
	if bytes.Contains(buffer.Bytes(), []byte("P-1")) {
		t.Fatalf("the line leaks the project id:\n%s", buffer.String())
	}
	certifyLine(t, buffer.Bytes(), map[string]any{
		"org_id": "org_1", "principal_class": "restricted", "grant_class": "project_only", "project_scope_count": 1,
		"decision": "admitted", "admitted_count": 1,
	})
	var restricted bytes.Buffer
	directread.NewSubjectGate(graph, directread.NewSlogRecorder(slog.New(slog.NewJSONHandler(&restricted, nil)))).
		Authorize(ctx, storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/a"}}, []contextfabric.SubjectRef{project})
	certifyLine(t, restricted.Bytes(), map[string]any{"org_id": "org_1", "principal_class": "restricted", "grant_class": "restricted", "project_scope_count": 0})
}

// CHAOS-12300: the relationships line counts each E1/E2 basis apart, and
// carries the grant class.
func TestTheRelationshipsLineCountsEachProjectOnlyEdgeBasis(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1", ProjectScopes: []string{c4Project, "project.v2:linear:P-2"}}
	record := directread.RelationshipsReadRecord{
		Status: directread.RelationshipsPartial, SubjectKind: "project", Direction: "both", WindowMode: "current",
		EdgesWithheld: map[directread.EdgeWithheldReason]int{
			directread.EdgeWithheldAttributionRelation: 2, directread.EdgeWithheldProjectList: 3, directread.EdgeWithheldTarget: 1,
		},
	}
	var buffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&buffer, nil)).Info(directread.RelationshipsReadLogMessage, directread.RelationshipsReadLogArgs(principal, record)...)
	parsed, err := certify.Parse(buffer.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.DirectRelationshipsRead, Want: map[string]any{
		"org_id": "org_1", "grant_class": "project_only", "project_scope_count": 2, "edges_not_visible": 6,
		"edges_withheld_attribution_relation": 2, "edges_withheld_project_list": 3,
		"edges_withheld_target": 1, "edges_withheld_attributes": 0,
		"edges_withheld_reasons": []any{"attribution_relation", "edge_project_list", "target_not_visible"},
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, buffer.String())
	}
}

// CHAOS-12300: the edge gate names the E1 project-list basis.
func TestTheEdgeGateNamesAProjectListRefusal(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1", ProjectScopes: []string{c4Project}}
	foreign := map[string]interface{}{"authorization_projects": []string{"project.v2:linear:OTHER"}}
	if got := directread.EdgeGate(principal, string(contractsv1.ContextFabricRelationshipOwnedByTeam), foreign, true, true); got != directread.EdgeWithheldProjectList {
		t.Fatalf("EdgeGate = %q, want %q", got, directread.EdgeWithheldProjectList)
	}
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/a"}}
	if got := directread.EdgeGate(restricted, string(contractsv1.ContextFabricRelationshipOwnedByTeam), map[string]interface{}{"authorization_repositories": []string{"acme/b"}}, true, true); got != directread.EdgeWithheldAttributes {
		t.Fatalf("a restricted caller's refused edge = %q, want %q", got, directread.EdgeWithheldAttributes)
	}
}
