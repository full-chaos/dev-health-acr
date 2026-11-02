package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAUserClassCallerIsToldOfAMatchItsOwnGrantCovers (CHAOS-12446, lead ruling):
// the count runs on the principal a real request reaches the handler with, after
// the access read. A credential that names repositories is narrowed to the live
// inventory and is restricted: a match inside its grant counts, one outside it
// never does. CHAOS-12592: a "*" credential (every web user) stays "*" and reads
// the whole organization, so a match outside the inventory counts too.
func TestAUserClassCallerIsToldOfAMatchItsOwnGrantCovers(t *testing.T) {
	const inside, outsideGrant = "acme/worker", "acme/not-in-the-inventory"
	node := func(id, slug string) graphrank.CandidateNode {
		return graphrank.CandidateNode{UUID: "node-" + id, Name: slug, Relevance: graphrank.Normalized(0.8), Attributes: map[string]interface{}{
			"canonical_id": id, "subject_kind": string(contextfabric.SubjectRepository), "label": slug,
			"evidence_refs": []string{"evidence_identity_1234"}, "authorization_repositories": []string{slug}}}
	}
	for _, grant := range []struct {
		name    string
		scopes  []string
		class   auth.GrantClass
		outside int
	}{
		{"a grant that names repositories", []string{hostedTestRepository, inside}, auth.GrantRestricted, 1},
		{"a star grant", []string{"*"}, auth.GrantUniversal, 2},
	} {
		for _, class := range []storage.CredentialClientClass{storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice} {
			t.Run(grant.name+"/"+string(class), func(t *testing.T) {
				var seen *storage.Principal
				var outside int
				investigator := investigatorFunc(func(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
					seen = &principal
					request.RequestedScope = contextfabric.RequestedScope{RepositorySlugs: []string{hostedTestRepository}}
					interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"worker"},
						TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
					deps := graphrank.ResolveDeps{
						ExactHint: func(context.Context, contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
							return graphrank.CandidateNode{}, false, nil
						},
						Search: func(context.Context, string, int) ([]graphrank.CandidateNode, bool, bool, error) {
							return []graphrank.CandidateNode{node("repository:worker", inside), node("repository:hidden", outsideGrant)}, false, false, nil
						},
						IsInternal: func(contextfabric.SubjectRef) bool { return false },
					}
					resolution, _, err := graphrank.ResolveSubjects(ctx, principal, request, interpreted, deps, nil, nil)
					if err != nil {
						t.Errorf("ResolveSubjects() error = %v", err)
					}
					outside = resolution.OutsideRequestedRepositories
					return contextfabric.InvestigationResult{}, errors.New("the resolution is the point of this test")
				})
				app, token := newContextFabricTestApp(t, investigator)
				app.config.RequestTimeout = time.Minute
				reader := &fakePrincipalAccess{}
				access := grantedAccess()
				access.Repositories = []string{hostedTestRepository, inside}
				reader.set(access, nil)
				app.runtime.PrincipalAccess = reader
				authenticateAsCredential(t, app, class, grant.scopes)

				request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, strings.NewReader(`{"schema_version":"context_fabric_investigation_request.v1","request_id":"request_12446","question":"How is worker doing?","time_context":{"axis":"current"},"options":{"max_subject_candidates":10,"max_cohort_members":50,"max_relationship_paths":50,"max_drivers":10,"max_evidence_refs":100,"max_serialized_bytes":262144},"consumer":{"name":"test","version":"v1","surface":"test"}}`))
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-ACR-Client-Version", "0.1.0")
				recorder := httptest.NewRecorder()
				app.Handler().ServeHTTP(recorder, request)

				if seen == nil {
					t.Fatalf("the handler never ran: %d %s", recorder.Code, recorder.Body.String())
				}
				if got := auth.ClassifyGrant(*seen); got != grant.class {
					t.Fatalf("principal %q class %q, want %q", seen.RepositoryScopes, got, grant.class)
				}
				if grant.class == auth.GrantRestricted && (!slices.Contains(seen.RepositoryScopes, inside) || slices.Contains(seen.RepositoryScopes, "*")) {
					t.Fatalf("principal %q, want the grant within the live inventory", seen.RepositoryScopes)
				}
				if outside != grant.outside {
					t.Fatalf("outside %d, want %d", outside, grant.outside)
				}
			})
		}
	}
}
