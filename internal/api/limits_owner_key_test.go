package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// limitKeyToken is a bearer of the valid shape: classCredentialStore answers
// every bearer with its row.
var limitKeyToken = auth.TokenPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))

// limitKeyCredential is one credential row, with its class and approving user.
func limitKeyCredential(id string, class storage.CredentialClientClass, user string) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{
		CredentialID: id, OrgID: "org_1", Scopes: []string{auth.ScopeContextRead}, RepositoryScopes: []string{hostedTestRepository},
		Provenance: contractsv1.CredentialProvenance{ClientClass: string(class), ApprovingUserID: user},
	}
}

// limitKeyCall sends one request as row through the real authenticator and the
// real limit middleware, on one shared manager.
func limitKeyCall(t *testing.T, manager *limits.Manager, row contractsv1.ClientCredential) int {
	t.Helper()
	authenticator, err := auth.NewAuthenticator(classCredentialStore{row: row}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	terminal := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := authenticator.Middleware(LimitMiddleware(manager, limits.RequestClassContext, terminal))
	request := httptest.NewRequest(http.MethodPost, "/agent-context", nil)
	request.Header.Set("Authorization", "Bearer "+limitKeyToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code
}

// A user's browser credentials are one owner: a refresh or a new Connect is a
// new credential row, and neither takes a new tracked subject. Before this, each
// took one, so 128 of them filled the organization's cap and new tokens were
// refused until the organization's window was swept.
func TestAUsersRotatedCredentialsTakeOneTrackedSubject(t *testing.T) {
	t.Parallel()
	manager, err := limits.NewManager(limits.Options{MaxCredentialsPerOrganization: 2, Policies: limits.PolicySet{Context: limits.ContextPolicy{Window: time.Hour, PerOrgLimit: 1 << 20}}})
	if err != nil {
		t.Fatal(err)
	}
	for rotation := range 200 {
		row := limitKeyCredential(fmt.Sprintf("cred_browser_%03d", rotation), storage.CredentialClientClassBrowser, "user-1")
		if code := limitKeyCall(t, manager, row); code != http.StatusNoContent {
			t.Fatalf("rotation %d: status %d, want one tracked subject for the user", rotation, code)
		}
	}
}

// The per-owner quota holds across a refresh and a new Connect.
func TestTheOwnerQuotaHoldsAcrossARefresh(t *testing.T) {
	t.Parallel()
	manager, err := limits.NewManager(limits.Options{Policies: limits.PolicySet{Context: limits.ContextPolicy{Window: time.Hour, PerOrgLimit: 1 << 20, PerCredentialLimit: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	for rotation, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		row := limitKeyCredential(fmt.Sprintf("cred_quota_%d", rotation), storage.CredentialClientClassBrowser, "user-1")
		if code := limitKeyCall(t, manager, row); code != want {
			t.Fatalf("request %d on a new credential: status %d, want %d", rotation+1, code, want)
		}
	}
}

// The key follows the owner rule, so only one user on one surface share a
// quota. Workload and org-bearer keys do not change.
func TestTheLimitKeySharesAQuotaOnlyInsideOneOwner(t *testing.T) {
	t.Parallel()
	browser := storage.CredentialClientClassBrowser
	workload := func(id, binding string) contractsv1.ClientCredential {
		row := limitKeyCredential(id, storage.CredentialClientClassWorkload, "")
		row.WorkloadBindingID = &binding
		return row
	}
	for _, tc := range []struct {
		name          string
		first, second contractsv1.ClientCredential
		shared        bool
	}{
		{"two credentials of one user on the browser", limitKeyCredential("a", browser, "user-1"), limitKeyCredential("b", browser, "user-1"), true},
		{"a browser and a device credential of one user are not", limitKeyCredential("a", browser, "user-1"), limitKeyCredential("b", storage.CredentialClientClassNativeDevice, "user-1"), false},
		{"a device and an OAuth user credential are one MCP surface", limitKeyCredential("a", storage.CredentialClientClassNativeDevice, "user-1"), limitKeyCredential("b", storage.CredentialClientClassOAuthUser, "user-1"), true},
		{"two users are not", limitKeyCredential("a", browser, "user-1"), limitKeyCredential("b", browser, "user-2"), false},
		{"two org bearers are not", limitKeyCredential("a", storage.CredentialClientClassOrgBearer, ""), limitKeyCredential("b", storage.CredentialClientClassOrgBearer, ""), false},
		{"two operators are not", limitKeyCredential("a", storage.CredentialClientClassOperator, ""), limitKeyCredential("b", storage.CredentialClientClassOperator, ""), false},
		{"two credentials of one workload binding are one", workload("a", "binding-1"), workload("b", "binding-1"), true},
		{"two workload bindings are not", workload("a", "binding-1"), workload("b", "binding-2"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manager, err := limits.NewManager(limits.Options{Policies: limits.PolicySet{Context: limits.ContextPolicy{Window: time.Hour, PerOrgLimit: 1 << 20, PerCredentialLimit: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			if code := limitKeyCall(t, manager, tc.first); code != http.StatusNoContent {
				t.Fatalf("first credential: status %d", code)
			}
			want := http.StatusNoContent
			if tc.shared {
				want = http.StatusTooManyRequests
			}
			if code := limitKeyCall(t, manager, tc.second); code != want {
				t.Fatalf("second credential: status %d, want %d", code, want)
			}
		})
	}
}

// The key is the owner rule: two principals share one exactly when
// contextfabric.SameSubmissionOwner holds for them.
func TestTheLimitKeyIsTheOwnerRule(t *testing.T) {
	t.Parallel()
	credential := func(id string, class storage.CredentialClientClass, user string) storage.Principal {
		return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: id, CredentialID: id, OrgID: "org_1", ClientClass: class, ApprovingUserID: user}
	}
	principals := []storage.Principal{
		credential("cred_a", storage.CredentialClientClassBrowser, "user-1"),
		credential("cred_b", storage.CredentialClientClassBrowser, "user-1"),
		credential("cred_c", storage.CredentialClientClassBrowser, "user-2"),
		credential("cred_d", storage.CredentialClientClassNativeDevice, "user-1"),
		credential("cred_e", storage.CredentialClientClassOAuthUser, "user-1"),
		credential("cred_f", storage.CredentialClientClassOrgBearer, ""),
		credential("cred_g", storage.CredentialClientClassOrgBearer, ""),
		credential("cred_h", storage.CredentialClientClassOperator, ""),
		credential("cred_i", storage.CredentialClientClassOperator, "user-1"),
		credential("cred_j", storage.CredentialClientClassBrowser, ""),
		{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-1", OrgID: "org_1"},
		{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-2", OrgID: "org_1"},
	}
	for _, a := range principals {
		for _, b := range principals {
			ownerA, err := contextfabric.SubmissionOwnerOf(a)
			if err != nil {
				t.Fatal(err)
			}
			ownerB, err := contextfabric.SubmissionOwnerOf(b)
			if err != nil {
				t.Fatal(err)
			}
			if same, shared := contextfabric.SameSubmissionOwner(ownerA, ownerB), limitSubjectOf(a) == limitSubjectOf(b); same != shared {
				t.Errorf("%s and %s: one owner = %t, one limit key = %t", a.Subject, b.Subject, same, shared)
			}
		}
	}
}

// The ticket's repro: users on the manager's default options, a new browser
// credential at each step and one request on it, for 48 hours, at the credential
// lifetime and at the most web's silent authorize allows (6 per 60 seconds). On
// the credential id, 128 credentials refused every new token: at 20 users, 47% of
// the requests at the first rate and 98% at the second.
func TestNoRotationRateFillsTheTrackedSubjects(t *testing.T) {
	t.Parallel()
	for _, rate := range []time.Duration{10 * time.Minute, 10 * time.Second} {
		for _, users := range []int{1, 5, 20} {
			now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
			manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			for step := range int(48 * time.Hour / rate) {
				for user := range users {
					id := fmt.Sprintf("cred_%d_%d", user, step)
					subject := limits.Subject{OrgID: "org_1", CredentialID: limitSubjectOf(storage.Principal{
						AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: id, CredentialID: id, OrgID: "org_1",
						ClientClass: storage.CredentialClientClassBrowser, ApprovingUserID: fmt.Sprintf("user-%d", user),
					})}
					claim, decision, err := manager.Claim(context.Background(), subject, limits.RequestClassAuth)
					if err != nil {
						t.Fatal(err)
					}
					if !decision.Allowed {
						t.Fatalf("rate %s, %d users: request %d refused: %s", rate, users, requests+1, decision.Reason)
					}
					claim.DoneClaim()
					requests++
				}
				now = now.Add(rate)
			}
			if requests == 0 {
				t.Fatal("no request was sent: the repro measured nothing")
			}
		}
	}
}
