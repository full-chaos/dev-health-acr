package credentiallifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNormalizeCreate_derivesClientClassPerIssuancePath pins CHAOS-12083's
// derivation: the issuance provenance names the path, a resource marks an
// OAuth exchange inside the device-authorization path, and each path admits
// only the provenance fields it can have.
func TestNormalizeCreate_derivesClientClassPerIssuancePath(t *testing.T) {
	const resource = "https://mcp.example.test/mcp"
	const client = "acrc_0123456789abcdef0123456789abcdef"
	cases := []struct {
		name   string
		modify func(*CreateInput)
		want   ClientClass
		wantOK bool
	}{
		{"operator", func(*CreateInput) {}, ClientClassOperator, true},
		{"operator refuses an approving user", func(in *CreateInput) { in.ApprovingUserID = "user_1" }, "", false},
		{"operator refuses a client id", func(in *CreateInput) { in.ClientID = client }, "", false},
		{"workload", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceWorkloadExchange
			in.WorkloadBindingID = "binding-1"
		}, ClientClassWorkload, true},
		{"workload refuses an approving user", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceWorkloadExchange
			in.WorkloadBindingID = "binding-1"
			in.ApprovingUserID = "user_1"
		}, "", false},
		{"workload refuses a client id", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceWorkloadExchange
			in.WorkloadBindingID = "binding-1"
			in.ClientID = client
		}, "", false},
		{"native device", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
			in.ApprovingUserID = "user_1"
		}, ClientClassNativeDevice, true},
		{"native device refuses a client id", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
			in.ApprovingUserID = "user_1"
			in.ClientID = client
		}, "", false},
		{"device redemption needs an approving user", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
		}, "", false},
		{"oauth exchange", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
			in.ApprovingUserID = "user_1"
			in.Resource = resource
			in.ClientID = client
		}, ClientClassOAuthUser, true},
		{"a caller class equal to the derived one is kept", func(in *CreateInput) {
			in.ClientClass = ClientClassOperator
		}, ClientClassOperator, true},
		{"a caller class that differs is refused", func(in *CreateInput) {
			in.ClientClass = ClientClassBrowser
		}, "", false},
		{"an oauth exchange without a client id is refused", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
			in.ApprovingUserID = "user_1"
			in.Resource = resource
		}, "", false},
		{"an unknown issuance provenance is refused", func(in *CreateInput) {
			in.IssuanceProvenance = "self_service"
		}, "", false},
		{"a control character in the approving user is refused", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
			in.ApprovingUserID = "user\n1"
		}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validResourceTestInput()
			tc.modify(&input)
			got, err := normalizeCreate(input)
			if !tc.wantOK {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("normalizeCreate() error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeCreate() error = %v", err)
			}
			if got.ClientClass != tc.want {
				t.Fatalf("ClientClass = %q, want %q", got.ClientClass, tc.want)
			}
		})
	}
}

func TestClientClass_vocabularyIsClosed(t *testing.T) {
	for _, class := range []ClientClass{ClientClassBrowser, ClientClassOAuthUser, ClientClassNativeDevice, ClientClassOperator, ClientClassWorkload} {
		if !class.Valid() {
			t.Fatalf("%q.Valid() = false", class)
		}
	}
	for _, class := range []ClientClass{"", "org_bearer", "Browser"} {
		if class.Valid() {
			t.Fatalf("%q.Valid() = true", class)
		}
	}
	for class, want := range map[ClientClass]bool{
		ClientClassBrowser: true, ClientClassOAuthUser: true, ClientClassNativeDevice: true,
		ClientClassOperator: false, ClientClassWorkload: false,
	} {
		if class.UserApproved() != want {
			t.Fatalf("%q.UserApproved() = %v, want %v", class, !want, want)
		}
	}
}

// A client-ID metadata document client id is an https URL of up to 2048
// bytes (internal/storage maxOAuthURILength); an OAuth redemption for such a
// client must not fail on the credential's provenance.
func TestNormalizeCreate_acceptsAMetadataDocumentClientIDOfAnyAdmittedLength(t *testing.T) {
	input := validResourceTestInput()
	input.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
	input.ApprovingUserID = "user_1"
	input.Resource = "https://mcp.example.test/mcp"
	prefix := "https://client.example.test/"
	input.ClientID = prefix + strings.Repeat("a", 2048-len(prefix))
	got, err := normalizeCreate(input)
	if err != nil {
		t.Fatalf("a 2048-byte metadata-document client id is refused: %v", err)
	}
	if got.ClientID != input.ClientID || got.ClientClass != ClientClassOAuthUser {
		t.Fatalf("normalized = client %d bytes class %q", len(got.ClientID), got.ClientClass)
	}
	input.ClientID += "a"
	if _, err := normalizeCreate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a 2049-byte client id error = %v, want ErrInvalidInput", err)
	}
}

// TestNormalizeCreate_browserClass pins CHAOS-12123: the browser class is
// named by the OAuth service on an OAuth exchange only, and it carries the
// session, the family and both expiries; no other class carries them. An
// OAuth exchange always names its client.
func TestNormalizeCreate_browserClass(t *testing.T) {
	expires := time.Date(2026, 10, 30, 12, 10, 0, 0, time.UTC)
	family := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	browser := func(in *CreateInput) {
		in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
		in.ApprovingUserID = "user_1"
		in.Resource = "https://acr.example.test"
		in.ClientID = "https://www.example.test/acr/client.json"
		in.ClientClass = ClientClassBrowser
		in.SessionID = "sid_1"
		in.FamilyID = "fam_1"
		in.ExpiresAt = &expires
		in.FamilyExpiresAt = &family
	}
	cases := []struct {
		name   string
		modify func(*CreateInput)
		want   ClientClass
	}{
		{"browser", browser, ClientClassBrowser},
		{"browser without a session", func(in *CreateInput) { browser(in); in.SessionID = "" }, ""},
		{"browser without a family", func(in *CreateInput) { browser(in); in.FamilyID = "" }, ""},
		{"browser without a family expiry", func(in *CreateInput) { browser(in); in.FamilyExpiresAt = nil }, ""},
		{"browser without an expiry", func(in *CreateInput) { browser(in); in.ExpiresAt = nil }, ""},
		{"browser family ending before the credential", func(in *CreateInput) {
			browser(in)
			early := expires.Add(-time.Minute)
			in.FamilyExpiresAt = &early
		}, ""},
		{"browser on the native device path", func(in *CreateInput) { browser(in); in.Resource = ""; in.ClientID = "" }, ""},
		{"browser on the operator path", func(in *CreateInput) {
			browser(in)
			in.IssuanceProvenance, in.ApprovingUserID, in.Resource, in.ClientID = "", "", "", ""
		}, ""},
		{"a session on oauth_user", func(in *CreateInput) { browser(in); in.ClientClass = ""; in.FamilyID = ""; in.FamilyExpiresAt = nil }, ""},
		{"a family on oauth_user", func(in *CreateInput) { browser(in); in.ClientClass = ""; in.SessionID = "" }, ""},
		{"an oauth exchange without a client id", func(in *CreateInput) {
			in.IssuanceProvenance = IssuanceProvenanceDeviceAuthorization
			in.ApprovingUserID = "user_1"
			in.Resource = "https://mcp.example.test/mcp"
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validResourceTestInput()
			tc.modify(&input)
			got, err := normalizeCreate(input)
			if tc.want == "" {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("normalizeCreate() error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil || got.ClientClass != tc.want || got.SessionID != "sid_1" || got.FamilyID != "fam_1" {
				t.Fatalf("normalizeCreate() = class %q session %q family %q, %v", got.ClientClass, got.SessionID, got.FamilyID, err)
			}
		})
	}
}
