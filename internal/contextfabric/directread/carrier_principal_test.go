package directread_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13945: the internal call names the user behind a user-class credential in X-DH-Internal-Principal-Id (the
// ops principal wire id, prn_ and the base64url of the uuid), so the ops decision point judges that user. Every other
// caller names none.

const carrierUser = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b"

func wirePrincipal(t *testing.T, id string) string {
	t.Helper()
	parsed := uuid.MustParse(id)
	return "prn_" + base64.RawURLEncoding.EncodeToString(parsed[:])
}

func userClass(class storage.CredentialClientClass) storage.Principal {
	return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, OrgID: opOrgA, Subject: "u", CredentialID: "c",
		ClientClass: class, ApprovingUserID: carrierUser}
}

// RC11: only a user-class credential with an approving user names a principal.
func TestNamedPrincipalOnlyForUserClassCredentials(t *testing.T) {
	want := wirePrincipal(t, carrierUser)
	for _, class := range []storage.CredentialClientClass{storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice} {
		if got := directread.NamedPrincipal(userClass(class)); got != want {
			t.Errorf("%s: %q, want %q", class, got, want)
		}
	}
	for name, principal := range map[string]storage.Principal{
		"org bearer":        userClass(storage.CredentialClientClassOrgBearer),
		"operator":          userClass(storage.CredentialClientClassOperator),
		"workload":          userClass(storage.CredentialClientClassWorkload),
		"legacy credential": userClass(""),
		"no approving user": func() storage.Principal {
			p := userClass(storage.CredentialClientClassBrowser)
			p.ApprovingUserID = ""
			return p
		}(),
		"not a uuid": func() storage.Principal {
			p := userClass(storage.CredentialClientClassBrowser)
			p.ApprovingUserID = "user-1"
			return p
		}(),
		"web assertion": func() storage.Principal {
			p := userClass(storage.CredentialClientClassBrowser)
			p.AuthenticationMethod = storage.AuthenticationMethodWebAssertion
			return p
		}(),
	} {
		if got := directread.NamedPrincipal(principal); got != "" {
			t.Errorf("%s: %q, want none", name, got)
		}
	}
}

// The client sends the principal header exactly once when the call names one, and refuses to send a malformed one.
func TestQueryClientSendsThePrincipalHeaderWhenNamed(t *testing.T) {
	u := newOpUpstream(t, func(opRecorded) (int, string) { return 200, `{"data":{"x":1}}` })
	client, err := directread.NewHTTPQueryClient(u.server.URL+"/", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	principal := wirePrincipal(t, carrierUser)
	if _, err := client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }", PrincipalID: principal}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := u.requests()[0].Header.Values("X-DH-Internal-Principal-Id"); len(got) != 1 || got[0] != principal {
		t.Fatalf("principal header %v, want [%s]", got, principal)
	}
	for _, bad := range []string{"user-1", "prn_", principal + "A", "prn_AAAAAAAAAAAAAAAAAAAAAA"} {
		u.reset()
		_, err := client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }", PrincipalID: bad})
		if directread.QueryErrorClassOf(err) != directread.QueryErrorRequestInvalid || len(u.requests()) != 0 {
			t.Fatalf("PrincipalID %q: %v with %d requests, want request_invalid and nothing sent", bad, err, len(u.requests()))
		}
	}
}

// RC12: the run_operation path names the principal from the authenticated principal only: a user-class caller's
// approving user, and nothing for any other caller.
func TestOperationRunnerNamesThePrincipalFromTheCredentialOnly(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op := cat.Operations(directread.CallerUnrestricted)[0]
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	if needsTeam(op) {
		t.Skip("the first operation needs a team")
	}
	for name, c := range map[string]struct {
		principal storage.Principal
		want      []string
	}{
		"user class": {userClass(storage.CredentialClientClassNativeDevice), []string{wirePrincipal(t, carrierUser)}},
		"operator":   {userClass(storage.CredentialClientClassOperator), nil},
		"no class":   {opUnrestricted(opOrgA), nil},
	} {
		h.upstream.reset()
		resp := h.run(t, c.principal, op.Name, opMinimalVariables(t, op))
		reqs := h.upstream.requests()
		if resp.Call != directread.CallServed || len(reqs) != 1 {
			t.Fatalf("%s: %+v with %d requests, want one served call", name, resp, len(reqs))
		}
		got := reqs[0].Header.Values("X-DH-Internal-Principal-Id")
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Fatalf("%s: principal header %v, want %v", name, got, c.want)
		}
	}
}
