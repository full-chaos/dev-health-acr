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
// the count runs on the principal a real request reaches the handler with: a
// user-class credential issued "*", after the access read has narrowed it to
// the live inventory. That caller is restricted, never unrestricted, which is
// what kept the CHAOS-12360 sentence from every web user while tests that built
// the principal by hand passed.
func TestAUserClassCallerIsToldOfAMatchItsOwnGrantCovers(t *testing.T) {
	const inside, outsideGrant = "acme/worker", "acme/not-in-the-inventory"
	node := func(id, slug string) graphrank.CandidateNode {
		return graphrank.CandidateNode{UUID: "node-" + id, Name: slug, Relevance: graphrank.Normalized(0.8), Attributes: map[string]interface{}{
			"canonical_id": id, "subject_kind": string(contextfabric.SubjectRepository), "label": slug,
			"evidence_refs": []string{"evidence_identity_1234"}, "authorization_repositories": []string{slug}}}
	}
	for _, class := range []storage.CredentialClientClass{storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice} {
		t.Run(string(class), func(t *testing.T) {
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
			authenticateAsCredential(t, app, class, []string{"*"})

			request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, strings.NewReader(`{"schema_version":"context_fabric_investigation_request.v1","request_id":"request_12446","question":"How is worker doing?","time_context":{"axis":"current"},"options":{"max_subject_candidates":10,"max_cohort_members":50,"max_relationship_paths":50,"max_drivers":10,"max_evidence_refs":100,"max_serialized_bytes":262144},"consumer":{"name":"test","version":"v1","surface":"test"}}`))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-ACR-Client-Version", "0.1.0")
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, request)

			if seen == nil {
				t.Fatalf("the handler never ran: %d %s", recorder.Code, recorder.Body.String())
			}
			if got := auth.ClassifyGrant(*seen); got != auth.GrantRestricted || !slices.Contains(seen.RepositoryScopes, inside) || slices.Contains(seen.RepositoryScopes, "*") {
				t.Fatalf("principal %q class %q, want the live inventory and restricted: the shape this test exists for", seen.RepositoryScopes, got)
			}
			if outside != 1 {
				t.Fatalf("outside %d, want 1: the match inside the grant counts, and the one outside the grant never does", outside)
			}
		})
	}
}
