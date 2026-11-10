package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-go/authverify"
)

const (
	liveOrgsLive   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	liveOrgsGone   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	liveOrgsOutage = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// liveOrgsEntitlement is the remote entitlement client's organization surface:
// the coded 404 for the gone organization, an unreadable answer for the outage one.
type liveOrgsEntitlement struct{}

func (liveOrgsEntitlement) HasEntitlement(context.Context, string, string) (bool, error) {
	return true, nil
}

func (e liveOrgsEntitlement) OrganizationKnown(ctx context.Context, orgID string) error {
	if gone, err := e.OrganizationUnknown(ctx, orgID); err != nil || gone {
		if gone {
			return entitlements.ErrOrganizationUnknown
		}
		return err
	}
	return nil
}

func (liveOrgsEntitlement) OrganizationUnknown(_ context.Context, orgID string) (bool, error) {
	switch orgID {
	case liveOrgsGone:
		return true, nil
	case liveOrgsOutage:
		return false, errors.New("entitlement service unavailable")
	}
	return false, nil
}

type liveOrgsValidator struct{}

func (liveOrgsValidator) Validate(context.Context, string) (authverify.SubjectIdentity, error) {
	return authverify.SubjectIdentity{TrustDomain: "cluster.local", Namespace: "dev-health", ServiceAccountName: "go-api", ServiceAccountUID: "uid-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// The exchange path end to end, in memory: the real resolver over the
// entitlement organization check, the real issuer over the credential store.
func TestTokenExchange_liveOrgsBindingAnswersPerOrganizationState(t *testing.T) {
	bindings := memory.NewWorkloadBindingStore()
	bindings.Put(storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "dev-health", ServiceAccountName: "go-api", ServiceAccountUID: "uid-1"},
		storage.WorkloadBinding{BindingID: "wlb_go_api_revoke", OrgScope: storage.WorkloadOrgScopeLiveOrgs, Role: "revoke", RepositoryScopes: []string{}})
	resolver, err := auth.NewGrantResolver(bindings, OrganizationPresenceFor(liveOrgsEntitlement{}))
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := memory.NewCredentialStore()
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := auth.NewWorkloadAccessTokenIssuer(service, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := authverify.NewWorkloadTokenExchangeService(liveOrgsValidator{}, resolver, issuer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exchange.Exchange(auth.WithWorkloadOrgSelector(context.Background(), liveOrgsLive), "subject-token", nil); err != nil {
		t.Fatalf("direct exchange for the live org: %v", err)
	}
	app := newTokenExchangeTestApp(t, exchange)
	post := func(org string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm(contractsv1.TokenExchangeResourceOrgPrefix+org)))
		return response
	}
	rows := func(org string) int {
		listed, err := credentials.List(context.Background(), org)
		if err != nil {
			t.Fatal(err)
		}
		return len(listed)
	}

	response := post(liveOrgsLive)
	if response.Code != http.StatusOK {
		t.Fatalf("live org: status = %d body = %s", response.Code, response.Body.String())
	}
	var token contractsv1.TokenExchangeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	if token.Scope != auth.ScopeCredentialLifecycleRevoke {
		t.Fatalf("live org: scope = %q, want the revoke scope only", token.Scope)
	}
	stored, err := credentials.FindByTokenHash(context.Background(), auth.HashToken(token.AccessToken))
	if err != nil {
		t.Fatal(err)
	}
	if stored.OrgID != liveOrgsLive || !reflect.DeepEqual(stored.Scopes, []string{auth.ScopeCredentialLifecycleRevoke}) || !reflect.DeepEqual(stored.RepositoryScopes, []string{"*"}) {
		t.Fatalf("live org: credential org = %q scopes = %v repository = %v", stored.OrgID, stored.Scopes, stored.RepositoryScopes)
	}

	status, code := exchangeErrorCode(t, post(liveOrgsGone))
	if status != http.StatusBadRequest || code != contractsv1.TokenExchangeErrorInvalidTarget || rows(liveOrgsGone) != 0 {
		t.Fatalf("gone org: status = %d code = %q rows = %d, want 400 invalid_target and no credential", status, code, rows(liveOrgsGone))
	}

	response = post(liveOrgsOutage)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "upstream_unavailable") || rows(liveOrgsOutage) != 0 {
		t.Fatalf("outage: status = %d body = %s rows = %d, want 503 upstream_unavailable and no credential", response.Code, response.Body.String(), rows(liveOrgsOutage))
	}
}

func TestTokenExchange_organizationCheckUnavailableIsUpstreamUnavailable(t *testing.T) {
	app := newTokenExchangeTestApp(t, &selectorRecordingExchanger{err: errors.Join(auth.ErrWorkloadOrgCheckUnavailable, errors.New("timeout"))})
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm(contractsv1.TokenExchangeResourceOrgPrefix+liveOrgsLive)))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "upstream_unavailable") {
		t.Fatalf("status = %d body = %s, want 503 upstream_unavailable", response.Code, response.Body.String())
	}
}
