package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12091: the canonical owner is derived from authentication only, and
// an owner matches by its legacy pair or by its canonical user on one surface.

func TestSubmissionOwnerOf_canonicalOwnerPerPrincipal(t *testing.T) {
	credential := func(class storage.CredentialClientClass, approver string) storage.Principal {
		return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: "cred_1", CredentialID: "cred_1", OrgID: "org_1", ClientClass: class, ApprovingUserID: approver}
	}
	cases := []struct {
		name        string
		principal   storage.Principal
		wantUser    string
		wantSurface SubmissionOwnerSurface
	}{
		{"web assertion", storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user_1", OrgID: "org_1"}, "user_1", SubmissionSurfaceBrowser},
		{"browser class", credential(storage.CredentialClientClassBrowser, "user_1"), "user_1", SubmissionSurfaceBrowser},
		{"oauth user", credential(storage.CredentialClientClassOAuthUser, "user_1"), "user_1", SubmissionSurfaceMCP},
		{"native device", credential(storage.CredentialClientClassNativeDevice, "user_1"), "user_1", SubmissionSurfaceMCP},
		{"operator", credential(storage.CredentialClientClassOperator, ""), "", ""},
		{"workload", credential(storage.CredentialClientClassWorkload, ""), "", ""},
		{"legacy credential, no class", credential("", ""), "", ""},
		{"a class with no approving user", credential(storage.CredentialClientClassOAuthUser, ""), "", ""},
		{"an approving user on an operator class", credential(storage.CredentialClientClassOperator, "user_1"), "", ""},
		{"an unknown class", credential("org_bearer", "user_1"), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, err := SubmissionOwnerOf(tc.principal)
			if err != nil {
				t.Fatal(err)
			}
			if owner.UserID != tc.wantUser || owner.Surface != tc.wantSurface {
				t.Fatalf("canonical owner = (%q, %q), want (%q, %q)", owner.UserID, owner.Surface, tc.wantUser, tc.wantSurface)
			}
			method, subject := tc.principal.AuditActor()
			if owner.AuthenticationMethod != method || owner.Subject != subject {
				t.Fatalf("legacy owner = (%q, %q), want the audit actor (%q, %q)", owner.AuthenticationMethod, owner.Subject, method, subject)
			}
		})
	}
	if _, err := SubmissionOwnerOf(storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, OrgID: "org_1"}); err == nil {
		t.Fatal("a principal with no subject has an owner")
	}
}

func TestSubmissionOwnerMatchOf(t *testing.T) {
	first := SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_1", UserID: "user_1", Surface: SubmissionSurfaceMCP}
	for name, tc := range map[string]struct {
		request SubmissionOwner
		want    SubmissionOwnerMatch
	}{
		"same pair":                {first, SubmissionOwnerMatchLegacy},
		"rotated credential":       {SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_2", UserID: "user_1", Surface: SubmissionSurfaceMCP}, SubmissionOwnerMatchCanonical},
		"same user, other surface": {SubmissionOwner{AuthenticationMethod: "web_assertion", Subject: "user_1", UserID: "user_1", Surface: SubmissionSurfaceBrowser}, SubmissionOwnerMatchNone},
		"other user":               {SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_3", UserID: "user_2", Surface: SubmissionSurfaceMCP}, SubmissionOwnerMatchNone},
	} {
		if got := SubmissionOwnerMatchOf(first, tc.request); got != tc.want {
			t.Errorf("%s: SubmissionOwnerMatchOf = %q, want %q", name, got, tc.want)
		}
	}
}

func TestSameSubmissionOwner(t *testing.T) {
	legacy := SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_1"}
	first := SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_1", UserID: "user_1", Surface: SubmissionSurfaceMCP}
	rotated := SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_2", UserID: "user_1", Surface: SubmissionSurfaceMCP}
	browser := SubmissionOwner{AuthenticationMethod: "web_assertion", Subject: "user_1", UserID: "user_1", Surface: SubmissionSurfaceBrowser}
	other := SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_3", UserID: "user_2", Surface: SubmissionSurfaceMCP}
	operator := SubmissionOwner{AuthenticationMethod: "credential", Subject: "cred_4"}
	cases := []struct {
		name string
		a, b SubmissionOwner
		want bool
	}{
		{"the same legacy pair", first, legacy, true},
		{"a rotated credential of the same user", first, rotated, true},
		{"the same user on another surface", rotated, browser, false},
		{"another user", first, other, false},
		{"an operator credential", first, operator, false},
		{"two owners with no user and different pairs", legacy, operator, false},
		{"an empty canonical user never matches", SubmissionOwner{AuthenticationMethod: "credential", Subject: "a", Surface: SubmissionSurfaceMCP}, SubmissionOwner{AuthenticationMethod: "credential", Subject: "b", Surface: SubmissionSurfaceMCP}, false},
		{"an empty surface never matches", SubmissionOwner{AuthenticationMethod: "credential", Subject: "a", UserID: "user_1"}, SubmissionOwner{AuthenticationMethod: "credential", Subject: "b", UserID: "user_1"}, false},
		{"an empty legacy subject never matches", SubmissionOwner{AuthenticationMethod: "credential"}, SubmissionOwner{AuthenticationMethod: "credential"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameSubmissionOwner(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameSubmissionOwner = %t, want %t", got, tc.want)
			}
			if got := SameSubmissionOwner(tc.b, tc.a); got != tc.want {
				t.Fatalf("SameSubmissionOwner (swapped) = %t, want %t", got, tc.want)
			}
		})
	}
}
