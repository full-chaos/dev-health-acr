package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

func TestTheCensusGrantIsBoundByGrantClass(t *testing.T) {
	t.Parallel()
	restricted := userprincipal.Narrowed(t, "org_1", []string{"acme/*"}, []string{"acme/api", "acme/web"})
	bound, ok := CensusGrantFrom(ScopeCensusToCaller(context.Background(), restricted))
	if !ok || !slices.Equal(bound, []string{"acme/api", "acme/web"}) {
		t.Fatalf("restricted caller: grant = %v bound %v, want its effective repositories", bound, ok)
	}
	projectOnly := userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"})
	bound, ok = CensusGrantFrom(ScopeCensusToCaller(context.Background(), projectOnly))
	if !ok || !slices.Equal(bound, []string{contractsv1.ProjectOnlyRepositoryScope}) {
		t.Fatalf("project-only caller: grant = %v bound %v, want the reserved value every census refuses", bound, ok)
	}
	for name, principal := range map[string]storage.Principal{
		"unrestricted": userprincipal.Authenticated(t, "org_1", nil),
		"universal":    userprincipal.Authenticated(t, "org_1", []string{"*"}),
	} {
		if bound, ok := CensusGrantFrom(ScopeCensusToCaller(context.Background(), principal)); ok {
			t.Fatalf("%s caller: grant %v bound, want nothing bound", name, bound)
		}
	}
}

func TestABoundGrantWithNoUsableListStaysBound(t *testing.T) {
	t.Parallel()
	if _, ok := CensusGrantFrom(WithCensusGrant(context.Background(), nil)); !ok {
		t.Fatal("an empty grant read as no grant: the census would run on the organization")
	}
	if _, ok := CensusGrantFrom(context.Background()); ok {
		t.Fatal("no grant bound read as a grant")
	}
}

func TestTheBoundGrantDoesNotAliasTheCallersList(t *testing.T) {
	t.Parallel()
	list := []string{"acme/api"}
	ctx := WithCensusGrant(context.Background(), list)
	list[0] = "*"
	if bound, _ := CensusGrantFrom(ctx); bound[0] != "acme/api" {
		t.Fatalf("grant = %v, want the list as it was when bound", bound)
	}
}

// A handr_ redemption reads the census with the caller's grant bound: its yes
// or no is an existence answer. Both sites: the fresh receipt and the replay
// of a remembered need.
func TestARedemptionReadsTheCensusInsideTheGrant(t *testing.T) {
	t.Parallel()
	restricted := userprincipal.Narrowed(t, reusePrincipal().OrgID, []string{"acme/*"}, []string{"acme/api"})
	projectOnly := userprincipal.ProjectOnly(t, reusePrincipal().OrgID, []string{"project.v2:github:P-1"})
	unrestricted := userprincipal.Authenticated(t, reusePrincipal().OrgID, nil)
	cases := []struct {
		name      string
		principal storage.Principal
		want      []string
		bound     bool
	}{
		{"restricted", restricted, []string{"acme/api"}, true},
		{"project only", projectOnly, []string{contractsv1.ProjectOnlyRepositoryScope}, true},
		{"unrestricted", unrestricted, nil, false},
	}
	verifier := func(grant *[]string, bound *bool, calls *int) HandleVerifier {
		return func(ctx context.Context, _ string, _ contractsv1.ContextFabricSubjectKind, _, _ string) (bool, HandleVerificationReason) {
			*calls++
			*grant, *bound = CensusGrantFrom(ctx)
			return true, HandleVerificationValid
		}
	}
	for _, tc := range cases {
		t.Run(tc.name+" fresh receipt", func(t *testing.T) {
			t.Parallel()
			priorResult, request := handleReceiptTestSetup()
			var grant []string
			var bound bool
			calls := 0
			engine := mustReuseTestEngine(t, EngineDependencies{
				Results:        &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
				HandleVerifier: verifier(&grant, &bound, &calls),
			})
			engine.canonicalizeStructure(context.Background(), tc.principal, request, ResolvedGraphBinding{})
			if calls != 1 {
				t.Fatalf("verifier calls = %d, want 1", calls)
			}
			if bound != tc.bound || !slices.Equal(grant, tc.want) {
				t.Fatalf("grant = %v bound %v, want %v bound %v", grant, bound, tc.want, tc.bound)
			}
		})
		t.Run(tc.name+" remembered need", func(t *testing.T) {
			t.Parallel()
			var grant []string
			var bound bool
			calls := 0
			engine := &Engine{handleVerifier: verifier(&grant, &bound, &calls)}
			entry := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: SubjectPullRequest, PatternID: "pull_request_number", AppliedValue: "532"}
			drop, ok := engine.reverifyRememberedNeed(context.Background(), tc.principal, InvestigationRequest{}, ResolvedGraphBinding{}, InvestigationResultSchemaV1, entry)
			if !ok || drop != "" || calls != 1 {
				t.Fatalf("reverify = %q %v calls %d, want one verified read", drop, ok, calls)
			}
			if bound != tc.bound || !slices.Equal(grant, tc.want) {
				t.Fatalf("grant = %v bound %v, want %v bound %v", grant, bound, tc.want, tc.bound)
			}
		})
	}
}
