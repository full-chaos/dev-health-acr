package episode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestGetByID_authorizedRoundTripAndCrossTenantNotFound(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	writer := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeWrite)
	created, _, err := service.Create(context.Background(), writer, episodeCreate())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	reader := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeRead)
	got, err := service.GetByID(context.Background(), reader, created.EpisodeID)
	if err != nil || got.EpisodeID != created.EpisodeID {
		t.Fatalf("get by id = (%#v, %v)", got, err)
	}

	foreignReader := readPrincipal("00000000-0000-0000-0000-000000000002", auth.ScopeEpisodeRead)
	if _, err := service.GetByID(context.Background(), foreignReader, created.EpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-tenant get error = %v, want ErrNotFound", err)
	}
}

func TestGetByID_requiresEpisodeReadScopeIndependentOfWriteScope(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return time.Now() }}))
	if err != nil {
		t.Fatal(err)
	}
	// A credential holding only episode:write (e.g. a recorder-only agent)
	// must not be able to read episodes back just because it can create them.
	writerOnly := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeWrite)
	if _, err := service.GetByID(context.Background(), writerOnly, "any-id"); !errors.Is(err, auth.ErrInsufficientScope) {
		t.Fatalf("write-only get error = %v, want ErrInsufficientScope", err)
	}
	if _, err := service.List(context.Background(), writerOnly, "", 0); !errors.Is(err, auth.ErrInsufficientScope) {
		t.Fatalf("write-only list error = %v, want ErrInsufficientScope", err)
	}
}

func TestGetByID_requiresEntitlementAndRepositoryScope(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return time.Now() }}))
	if err != nil {
		t.Fatal(err)
	}
	noEntitlement := storage.Principal{OrgID: "org_1", CredentialID: "cred_01", RepositoryScopes: []string{"owner/repo"}, Permissions: []string{auth.ScopeEpisodeRead}}
	if _, err := service.GetByID(context.Background(), noEntitlement, "any-id"); !errors.Is(err, ErrEntitlementRequired) {
		t.Fatalf("no-entitlement get error = %v, want ErrEntitlementRequired", err)
	}

	noRepoScope := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeRead)
	noRepoScope.RepositoryScopes = nil
	if _, err := service.GetByID(context.Background(), noRepoScope, "any-id"); !errors.Is(err, auth.ErrRepositoryForbidden) {
		t.Fatalf("no-repo-scope get error = %v, want ErrRepositoryForbidden", err)
	}
}

func TestList_returnsNewestFirstScopedToCallerAndFiltersByRepository(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	writer := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeWrite)
	create := episodeCreate()
	if _, _, err := service.Create(context.Background(), writer, create); err != nil {
		t.Fatalf("create: %v", err)
	}

	reader := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeRead)
	results, err := service.List(context.Background(), reader, create.Repository.Slug, 10)
	if err != nil || len(results) != 1 || results[0].ClientEpisodeID != create.ClientEpisodeID {
		t.Fatalf("list = (%#v, %v)", results, err)
	}

	otherRepoReader := readPrincipal("00000000-0000-0000-0000-000000000001", auth.ScopeEpisodeRead)
	if results, err := service.List(context.Background(), otherRepoReader, "owner/unrelated", 10); err != nil || len(results) != 0 {
		t.Fatalf("filtered-out list = (%#v, %v), want empty", results, err)
	}
}

func readPrincipal(orgID, scope string) storage.Principal {
	return storage.Principal{
		OrgID: orgID, CredentialID: "cred_01", RepositoryScopes: []string{"owner/repo"},
		Permissions: []string{scope}, ProductEntitlements: []string{"agent_context_runtime"},
	}
}
