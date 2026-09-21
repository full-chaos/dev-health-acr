package credentiallifecycle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validResourceTestInput() CreateInput {
	return CreateInput{
		CredentialID:     "cred_resource_test",
		OrgID:            "org-resource-test",
		Name:             "resource test credential",
		TokenPrefix:      "fcacr_resourcetest",
		TokenHash:        strings.Repeat("a", 64),
		RepositoryScopes: []string{"acme/widgets"},
		Scopes:           []string{"context:read"},
		ActorID:          "actor-resource-test",
	}
}

// TestValidateCreateInput_Resource is the Resource validation table required
// by the credential resource-binding contract: absent/empty is always fine,
// only https (or http loopback) absolute URIs without fragment/userinfo are
// accepted up to 2048 bytes, and Resource is refused outright alongside
// workload token exchange provenance.
func TestValidateCreateInput_Resource(t *testing.T) {
	longPath := strings.Repeat("a", 2048)
	cases := []struct {
		name    string
		mutate  func(CreateInput) CreateInput
		wantErr bool
	}{
		{
			name:   "absent",
			mutate: func(input CreateInput) CreateInput { return input },
		},
		{
			name: "empty",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = ""
				return input
			},
		},
		{
			name: "https ok",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = "https://example.com/resource"
				return input
			},
		},
		{
			name: "http loopback ok",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = "http://127.0.0.1:8080/resource"
				return input
			},
		},
		{
			name: "http non-loopback refused",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = "http://example.com/resource"
				return input
			},
			wantErr: true,
		},
		{
			name: "fragment refused",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = "https://example.com/resource#fragment"
				return input
			},
			wantErr: true,
		},
		{
			name: "userinfo refused",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = "https://user:pass@example.com/resource"
				return input
			},
			wantErr: true,
		},
		{
			name: "over length refused",
			mutate: func(input CreateInput) CreateInput {
				input.Resource = "https://example.com/" + longPath
				return input
			},
			wantErr: true,
		},
		{
			name: "with workload_exchange refused",
			mutate: func(input CreateInput) CreateInput {
				input.IssuanceProvenance = IssuanceProvenanceWorkloadExchange
				input.WorkloadBindingID = "binding-resource-test"
				input.Resource = "https://example.com/resource"
				return input
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			input := tc.mutate(validResourceTestInput())

			// When
			err := ValidateCreateInput(input)

			// Then
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidInput)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
