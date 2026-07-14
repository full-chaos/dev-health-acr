package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCredentialStore_RotateRejectsInactiveSourceWithoutAudit(t *testing.T) {
	ctx := context.Background()
	audit := NewAuditStore()
	store := mustCredentialStore(t, audit)
	source, err := store.CreateCredential(ctx, validCredentialCreateInput("source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeCredential(ctx, storage.CredentialRevocationInput{OrgID: source.OrgID, CredentialID: source.CredentialID, ActorID: "actor_1"}); err != nil {
		t.Fatal(err)
	}

	beforeAudits := len(audit.Events())
	_, err = store.RotateCredential(ctx, validCredentialRotationInput(source, "replacement", strings.Repeat("b", 64), true))

	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Rotate() error = %v, want not found", err)
	}
	if events := audit.Events(); len(events) != beforeAudits {
		t.Fatalf("audit recorded for rejected rotation: %#v", events)
	}
	credentials, listErr := store.List(ctx, source.OrgID)
	if listErr != nil || len(credentials) != 1 || credentials[0].CredentialID != source.CredentialID {
		t.Fatalf("rotation changed credential state: %#v %v", credentials, listErr)
	}
}
