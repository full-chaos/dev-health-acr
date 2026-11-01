package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const narrowActor = "actor_1"

type narrowFixture struct {
	audit   *memory.AuditStore
	store   *storage.CredentialLifecycle
	service *Service
}

func newNarrowFixture(t *testing.T) narrowFixture {
	t.Helper()
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	return narrowFixture{audit: audit, store: store, service: newTestService(t, store, audit, now)}
}

func (f narrowFixture) parent(t *testing.T, repositories, scopes []string) contractsv1.ClientCredential {
	t.Helper()
	issued, err := f.service.Create(context.Background(), CreateCredentialRequest{
		OrgID: deviceFlowTestOrgID, Name: "narrow parent", RepositoryScopes: repositories, Scopes: scopes, CreatedBy: narrowActor,
	})
	require.NoError(t, err)
	return issued.Credential
}

func TestRotate_refusesASuccessorWiderThanItsParent(t *testing.T) {
	for name, tc := range map[string]struct {
		parentRepos, parentScopes   []string
		requestRepos, requestScopes []string
	}{
		"scope added":                       {[]string{"acme/widgets"}, []string{ScopeContextRead}, nil, []string{ScopeContextRead, ScopeEvidenceRead}},
		"another scope":                     {[]string{"acme/widgets"}, []string{ScopeContextRead}, nil, []string{ScopeEpisodeWrite}},
		"data scope added":                  {[]string{"acme/widgets"}, []string{ScopeContextRead, ScopeEvidenceRead}, nil, []string{ScopeContextRead, ScopeDataRead}},
		"owner wildcard over a repository":  {[]string{"acme/widgets"}, []string{ScopeContextRead}, []string{"acme/*"}, nil},
		"everything over an owner wildcard": {[]string{"acme/*"}, []string{ScopeContextRead}, []string{"*"}, nil},
		"everything over a repository":      {[]string{"acme/widgets"}, []string{ScopeContextRead}, []string{"*"}, nil},
		"another owner":                     {[]string{"acme/widgets"}, []string{ScopeContextRead}, []string{"other/widgets"}, nil},
		"another owner wildcard":            {[]string{"acme/*"}, []string{ScopeContextRead}, []string{"other/*"}, nil},
		"same count, different repository":  {[]string{"acme/a", "acme/b"}, []string{ScopeContextRead}, []string{"acme/a", "acme/c"}, nil},
		"a repository added":                {[]string{"acme/a"}, []string{ScopeContextRead}, []string{"acme/a", "acme/b"}, nil},
		"narrower scopes but a wider repo":  {[]string{"acme/a"}, []string{ScopeContextRead, ScopeEvidenceRead}, []string{"acme/*"}, []string{ScopeContextRead}},
		"narrower repo but a wider scope":   {[]string{"acme/*"}, []string{ScopeContextRead}, []string{"acme/a"}, []string{ScopeContextRead, ScopeEvidenceRead}},
		"wider after case and spacing fold": {[]string{"acme/widgets"}, []string{ScopeContextRead}, []string{" ACME/* "}, nil},
	} {
		for _, overlap := range []time.Duration{0, time.Minute} {
			f := newNarrowFixture(t)
			parent := f.parent(t, tc.parentRepos, tc.parentScopes)
			before := len(f.audit.Events())
			_, err := f.service.Rotate(context.Background(), RotateCredentialRequest{
				OrgID: parent.OrgID, CredentialID: parent.CredentialID, CreatedBy: narrowActor,
				RepositoryScopes: tc.requestRepos, Scopes: tc.requestScopes, Overlap: overlap,
			})
			require.ErrorIs(t, err, ErrRotationWiderThanParent, "%s (overlap %s)", name, overlap)
			require.ErrorIs(t, err, ErrInvalidCredential, "%s: existing callers keep matching ErrInvalidCredential", name)
			list, listErr := f.store.List(context.Background(), parent.OrgID)
			require.NoError(t, listErr)
			require.Len(t, list, 1, "%s: no successor", name)
			require.Nil(t, list[0].RevokedAt, "%s: the parent stays live", name)
			require.Equal(t, parent.ExpiresAt, list[0].ExpiresAt, "%s: no overlap cut", name)
			require.Len(t, f.audit.Events(), before, "%s: no audit row", name)
		}
	}
}

func TestRotate_refusalTextIsFixed(t *testing.T) {
	require.EqualError(t, ErrRotationWiderThanParent, "invalid credential request: a rotation never widens its parent; issue a new credential")
}

func TestRotate_allowsEqualAndNarrowerSuccessors(t *testing.T) {
	for name, tc := range map[string]struct {
		parentRepos, parentScopes   []string
		requestRepos, requestScopes []string
		wantRepos, wantScopes       []string
	}{
		"equal sets":                         {[]string{"acme/widgets"}, []string{ScopeContextRead}, []string{"acme/widgets"}, []string{ScopeContextRead}, []string{"acme/widgets"}, []string{ScopeContextRead}},
		"empty request copies the parent":    {[]string{"acme/widgets"}, []string{ScopeContextRead}, nil, nil, []string{"acme/widgets"}, []string{ScopeContextRead}},
		"narrower scopes":                    {[]string{"acme/widgets"}, []string{ScopeContextRead, ScopeEvidenceRead}, nil, []string{ScopeContextRead}, []string{"acme/widgets"}, []string{ScopeContextRead}},
		"repository under an owner wildcard": {[]string{"acme/*"}, []string{ScopeContextRead}, []string{"acme/widgets"}, nil, []string{"acme/widgets"}, []string{ScopeContextRead}},
		"owner wildcard under itself":        {[]string{"acme/*"}, []string{ScopeContextRead}, []string{"acme/*"}, nil, []string{"acme/*"}, []string{ScopeContextRead}},
		"repository under everything":        {[]string{"*"}, []string{ScopeContextRead}, []string{"acme/widgets"}, nil, []string{"acme/widgets"}, []string{ScopeContextRead}},
		"owner wildcard under everything":    {[]string{"*"}, []string{ScopeContextRead}, []string{"acme/*"}, nil, []string{"acme/*"}, []string{ScopeContextRead}},
		"fewer repositories":                 {[]string{"acme/a", "acme/b"}, []string{ScopeContextRead}, []string{"acme/b"}, nil, []string{"acme/b"}, []string{ScopeContextRead}},
		"case and spacing fold":              {[]string{"acme/widgets"}, []string{ScopeContextRead}, []string{" ACME/Widgets ", "acme/widgets"}, nil, []string{"acme/widgets"}, []string{ScopeContextRead}},
		"defaulted parent copied":            {[]string{"acme/widgets"}, nil, nil, nil, []string{"acme/widgets"}, []string{ScopeContextRead, ScopeEvidenceRead}},
	} {
		for _, overlap := range []time.Duration{0, time.Minute} {
			f := newNarrowFixture(t)
			parent := f.parent(t, tc.parentRepos, tc.parentScopes)
			issued, err := f.service.Rotate(context.Background(), RotateCredentialRequest{
				OrgID: parent.OrgID, CredentialID: parent.CredentialID, CreatedBy: narrowActor,
				RepositoryScopes: tc.requestRepos, Scopes: tc.requestScopes, Overlap: overlap,
			})
			require.NoError(t, err, "%s (overlap %s)", name, overlap)
			require.ElementsMatch(t, tc.wantRepos, issued.Credential.RepositoryScopes, name)
			require.ElementsMatch(t, tc.wantScopes, issued.Credential.Scopes, name)
		}
	}
}

// emptyScopeParent reports every credential with an empty scope list, as a
// legacy row would, and counts the rotations that reach the store.
type emptyScopeParent struct {
	*storage.CredentialLifecycle
	rotations *int
}

func (s emptyScopeParent) GetByID(ctx context.Context, orgID, id string) (contractsv1.ClientCredential, error) {
	credential, err := s.CredentialLifecycle.GetByID(ctx, orgID, id)
	credential.Scopes = nil
	return credential, err
}

func (s emptyScopeParent) RotateCredential(ctx context.Context, input storage.CredentialRotationInput) (contractsv1.ClientCredential, error) {
	*s.rotations++
	return s.CredentialLifecycle.RotateCredential(ctx, input)
}

func TestRotate_aParentWithNoScopesIsNeverWidenedByAnEmptyOrANonEmptyRequest(t *testing.T) {
	for name, requestScopes := range map[string][]string{"empty request": nil, "non-empty request": {ScopeContextRead}} {
		f := newNarrowFixture(t)
		parent := f.parent(t, []string{"acme/widgets"}, []string{ScopeContextRead})
		rotations := 0
		now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
		wrapped, err := newService(emptyScopeParent{CredentialLifecycle: f.store, rotations: &rotations}, ServiceOptions{Now: func() time.Time { return now }})
		require.NoError(t, err)
		_, err = wrapped.Rotate(context.Background(), RotateCredentialRequest{
			OrgID: parent.OrgID, CredentialID: parent.CredentialID, CreatedBy: narrowActor, Scopes: requestScopes,
		})
		require.ErrorIs(t, err, ErrRotationWiderThanParent, name+": empty never reads as all")
		require.Zero(t, rotations, name+": the store is never reached")
	}
}

func TestRotateSelf_stillCopiesTheParentsScopesExactly(t *testing.T) {
	f := newNarrowFixture(t)
	parent := f.parent(t, []string{"acme/*"}, []string{ScopeContextRead})
	rotation, err := f.service.RotateSelf(context.Background(), selfPrincipal(parent))
	require.NoError(t, err)
	require.Equal(t, []string{ScopeContextRead}, rotation.Issued.Credential.Scopes)
	require.Equal(t, []string{"acme/*"}, rotation.Issued.Credential.RepositoryScopes)
}

func TestRepositoryScopeCovers(t *testing.T) {
	for _, tc := range []struct {
		parent, requested string
		want              bool
	}{
		{"*", "*", true}, {"*", "acme/*", true}, {"*", "acme/a", true},
		{"acme/*", "*", false}, {"acme/*", "acme/*", true}, {"acme/*", "acme/a", true}, {"acme/*", "other/*", false}, {"acme/*", "other/a", false}, {"acme/*", "acmeco/a", false},
		{"acme/a", "*", false}, {"acme/a", "acme/*", false}, {"acme/a", "acme/a", true}, {"acme/a", "acme/b", false}, {"acme/a", "other/a", false},
	} {
		require.Equal(t, tc.want, repositoryScopeCovers(tc.parent, tc.requested), "%s covers %s", tc.parent, tc.requested)
	}
}
