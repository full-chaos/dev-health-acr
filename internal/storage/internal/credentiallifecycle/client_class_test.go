package credentiallifecycle

import (
	"errors"
	"testing"
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
