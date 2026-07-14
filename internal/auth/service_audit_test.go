package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCredentialServiceDoesNotMutateWhenLifecycleContextIsCanceled(t *testing.T) {
	clock := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)

	for _, operation := range []string{"create", "rotate", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			// Given
			audit := memory.NewAuditStore()
			credentialStore := newMemoryCredentialStore(t, audit)
			service := newTestService(t, credentialStore, audit, clock)
			original, err := service.Create(context.Background(), CreateCredentialRequest{
				OrgID: "org_1", Name: "first", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1",
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			beforeAudits := len(audit.Events())

			// When
			switch operation {
			case "create":
				_, err = service.Create(ctx, CreateCredentialRequest{OrgID: "org_1", Name: "second", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1"})
			case "rotate":
				_, err = service.Rotate(ctx, RotateCredentialRequest{OrgID: "org_1", CredentialID: original.Credential.CredentialID, CreatedBy: "actor_1"})
			case "revoke":
				_, err = service.Revoke(ctx, "org_1", original.Credential.CredentialID, "admin")
			}

			// Then
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s error = %v, want context cancellation", operation, err)
			}
			items, listErr := credentialStore.List(context.Background(), "org_1")
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(items) != 1 {
				t.Fatalf("%s changed credential count = %d, want 1", operation, len(items))
			}
			if len(audit.Events()) != beforeAudits {
				t.Fatalf("%s changed audit count = %d, want %d", operation, len(audit.Events()), beforeAudits)
			}
		})
	}
}
