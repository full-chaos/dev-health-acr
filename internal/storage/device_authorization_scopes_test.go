package storage

import "testing"

func TestDeviceAuthorizationCredentialMatchesNarrowsNeverWidens(t *testing.T) {
	record := DeviceAuthorization{
		AuthorizedOrgID: "org", ApprovingSubject: "user", AuthorizedRepositoryScopes: []string{"org/repo"},
		AuthorizedScopes: []string{"context:read", "evidence:read"},
	}
	for _, tc := range []struct {
		scopes []string
		want   bool
	}{
		{[]string{"context:read", "evidence:read"}, true},
		{[]string{"context:read"}, true},
		{[]string{"evidence:read"}, true},
		{[]string{}, false},
		{nil, false},
		{[]string{"context:admin"}, false},
		{[]string{"context:read", "episode:write"}, false},
	} {
		input := CredentialCreateInput{OrgID: "org", ActorID: "user", RepositoryScopes: []string{"org/repo"}, Scopes: tc.scopes}
		if got := DeviceAuthorizationCredentialMatches(record, input); got != tc.want {
			t.Errorf("scopes %v: matches = %v, want %v", tc.scopes, got, tc.want)
		}
	}
}
