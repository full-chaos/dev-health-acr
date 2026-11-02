package memory

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func newListFixture(t *testing.T) (*AuditStore, *auth.Service) {
	t.Helper()
	audit := NewAuditStore()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	store, err := NewCredentialStoreWithOptions(CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(store, auth.ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return audit, service
}

func listCreate(t *testing.T, service *auth.Service, org string) string {
	t.Helper()
	issued, err := service.Create(context.Background(), auth.CreateCredentialRequest{OrgID: org, Name: "list cell", RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Credential.CredentialID
}

func TestListCredentialLifecycleKeysetOrgStatusAndRevision(t *testing.T) {
	audit, service := newListFixture(t)
	ctx := context.Background()
	var ours []string
	for range 5 {
		ours = append(ours, listCreate(t, service, "org_1"))
	}
	other := listCreate(t, service, "org_2")
	if _, err := service.Revoke(ctx, "org_1", ours[1], "actor"); err != nil {
		t.Fatal(err)
	}

	var all []contractsv1.CredentialLifecycleItem
	after := ""
	for pages := 0; pages < 10; pages++ {
		page, err := audit.ListCredentialLifecycle(ctx, "org_1", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 2 {
			t.Fatalf("page of %d with limit 2", len(page))
		}
		all = append(all, page...)
		after = page[len(page)-1].CredentialID
	}
	if len(all) != 5 {
		t.Fatalf("walk returned %d items, want 5: %+v", len(all), all)
	}
	for index, item := range all {
		if index > 0 && item.CredentialID <= all[index-1].CredentialID {
			t.Fatalf("not strictly ascending at %d: %+v", index, all)
		}
		if item.CredentialID == other {
			t.Fatal("another organization's credential was listed")
		}
		want, wantRevision := contractsv1.CredentialLifecycleStatusActive, 1
		if item.CredentialID == ours[1] {
			want, wantRevision = contractsv1.CredentialLifecycleStatusRevoked, 2
		}
		if item.Status != want || item.Revision != wantRevision {
			t.Errorf("%s = %+v, want %s revision %d", item.CredentialID, item, want, wantRevision)
		}
	}
	none, err := audit.ListCredentialLifecycle(ctx, "org_3", "", 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty org = %v, %v", none, err)
	}
}
