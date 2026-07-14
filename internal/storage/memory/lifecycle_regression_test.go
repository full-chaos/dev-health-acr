package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCredentialStore_allows_only_one_overlap_rotation(t *testing.T) {
	// Given
	ctx := context.Background()
	audit := NewAuditStore()
	store := mustCredentialStore(t, audit)
	source, err := store.CreateCredential(ctx, validCredentialCreateInput("source"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, replacement := range []storage.CredentialRotationInput{
		validCredentialRotationInput(source, "replacement-a", strings.Repeat("b", 64), false),
		validCredentialRotationInput(source, "replacement-b", strings.Repeat("c", 64), false),
	} {
		go func(replacement storage.CredentialRotationInput) {
			start.Wait()
			_, err := store.RotateCredential(ctx, replacement)
			results <- err
		}(replacement)
	}

	// When
	start.Done()
	first, second := <-results, <-results

	// Then
	if (first == nil) == (second == nil) {
		t.Fatalf("rotation results = %v, %v; want exactly one success", first, second)
	}
	if !errors.Is(first, storage.ErrConflict) && !errors.Is(second, storage.ErrConflict) {
		t.Fatalf("rotation failure = %v, %v; want conflict", first, second)
	}
	credentials, err := store.List(ctx, "org_1")
	if err != nil || len(credentials) != 2 {
		t.Fatalf("credentials = %#v, %v; want source and one replacement", credentials, err)
	}
	rotations := 0
	for _, event := range audit.Events() {
		if event.Action == "credential_rotated" {
			rotations++
		}
	}
	if rotations != 1 {
		t.Fatalf("rotation audits = %d, want 1", rotations)
	}
}

func TestCredentialStore_validatesEveryCreateInputAtBoundary(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Second)
	tests := []struct {
		name   string
		mutate func(*storage.CredentialCreateInput)
	}{
		{name: "credential id", mutate: func(input *storage.CredentialCreateInput) { input.CredentialID = " " }},
		{name: "organization", mutate: func(input *storage.CredentialCreateInput) { input.OrgID = "\t" }},
		{name: "name", mutate: func(input *storage.CredentialCreateInput) { input.Name = " " }},
		{name: "token prefix", mutate: func(input *storage.CredentialCreateInput) { input.TokenPrefix = "secret" }},
		{name: "token hash", mutate: func(input *storage.CredentialCreateInput) { input.TokenHash = "not-a-hash" }},
		{name: "repository scope", mutate: func(input *storage.CredentialCreateInput) { input.RepositoryScopes = []string{"../repo"} }},
		{name: "scope", mutate: func(input *storage.CredentialCreateInput) { input.Scopes = []string{"root"} }},
		{name: "actor", mutate: func(input *storage.CredentialCreateInput) { input.ActorID = " \n" }},
		{name: "expiry", mutate: func(input *storage.CredentialCreateInput) { input.ExpiresAt = &past }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			audit := NewAuditStore()
			_, store, err := newCredentialStore(audit, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			input := validCredentialCreateInput("invalid")
			test.mutate(&input)

			// When
			_, err = store.CreateCredential(context.Background(), input)

			// Then
			if err == nil {
				t.Fatal("CreateCredential() accepted invalid direct input")
			}
			if len(audit.Events()) != 0 {
				t.Fatalf("invalid create recorded audit events: %#v", audit.Events())
			}
		})
	}
}

func TestCredentialStore_rejectsRepeatedRevokeWithoutDuplicateAudit(t *testing.T) {
	// Given
	audit := NewAuditStore()
	store := mustCredentialStore(t, audit)
	created, err := store.CreateCredential(context.Background(), validCredentialCreateInput("revoke-once"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: created.OrgID, CredentialID: created.CredentialID, ActorID: "actor_1"}); err != nil {
		t.Fatal(err)
	}

	// When
	_, err = store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: created.OrgID, CredentialID: created.CredentialID, ActorID: "actor_1"})

	// Then
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second RevokeCredential() error = %v, want conflict", err)
	}
	revokes := 0
	for _, event := range audit.Events() {
		if event.Action == "credential_revoked" {
			revokes++
		}
	}
	if revokes != 1 {
		t.Fatalf("revoke audits = %d, want exactly one", revokes)
	}
}

func TestCredentialStore_rejectsTokenLookupAfterRevoke(t *testing.T) {
	// Given
	store := mustCredentialStore(t, NewAuditStore())
	input := validCredentialCreateInput("lookup")
	created, err := store.CreateCredential(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: created.OrgID, CredentialID: created.CredentialID, ActorID: "actor_1"}); err != nil {
		t.Fatal(err)
	}

	// When
	_, err = store.FindByTokenHash(context.Background(), input.TokenHash)

	// Then
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("FindByTokenHash() error = %v, want not found after revoke", err)
	}
}

func TestCredentialStore_rejectsInvalidUsageMetadataAtBoundary(t *testing.T) {
	// Given
	store := mustCredentialStore(t, NewAuditStore())
	created, err := store.CreateCredential(context.Background(), validCredentialCreateInput("usage"))
	if err != nil {
		t.Fatal(err)
	}

	// When
	err = store.TouchLastUsed(context.Background(), created.CredentialID, "not-an-ip", "agent\nforged", time.Now())

	// Then
	if !errors.Is(err, storage.ErrInvalidCredentialInput) {
		t.Fatalf("TouchLastUsed() error = %v, want invalid credential input", err)
	}
}

func TestCredentialStore_zeroValueReturnsErrorInsteadOfPanicking(t *testing.T) {
	// Given
	var store storage.CredentialLifecycle

	// When
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("zero-value CredentialStore panicked: %v", recovered)
		}
	}()
	_, err := store.CreateCredential(context.Background(), validCredentialCreateInput("zero"))

	// Then
	if err == nil {
		t.Fatal("zero-value CredentialStore accepted lifecycle mutation")
	}
}

func TestCredentialStore_rejectsUnboundedOrImplicitImmediateOverlap(t *testing.T) {
	// Given
	ctx := context.Background()
	store := mustCredentialStore(t, NewAuditStore())
	source, err := store.CreateCredential(ctx, validCredentialCreateInput("source"))
	if err != nil {
		t.Fatal(err)
	}

	// When
	zero := validCredentialRotationInput(source, "zero", strings.Repeat("b", 64), false)
	zero.Replacement.Overlap = 0
	excessive := validCredentialRotationInput(source, "excessive", strings.Repeat("c", 64), false)
	excessive.Replacement.Overlap = 15*time.Minute + time.Nanosecond
	_, zeroErr := store.RotateCredential(ctx, zero)
	_, excessiveErr := store.RotateCredential(ctx, excessive)

	// Then
	if zeroErr == nil || excessiveErr == nil {
		t.Fatalf("RotateCredential() errors = %v, %v; want invalid overlap rejected", zeroErr, excessiveErr)
	}
}

func TestCredentialStore_rejects_audit_store_reuse(t *testing.T) {
	// Given
	audit := NewAuditStore()
	_, err := NewCredentialStore(audit)
	if err != nil {
		t.Fatal(err)
	}

	// When
	second, err := NewCredentialStore(audit)

	// Then
	if second != nil || !errors.Is(err, storage.ErrConflict) {
		t.Fatal("NewCredentialStore() accepted an audit store already bound to another credential store")
	}
}

func validCredentialCreateInput(suffix string) storage.CredentialCreateInput {
	return storage.CredentialCreateInput{
		CredentialID:     "cred_" + suffix,
		OrgID:            "org_1",
		Name:             "credential " + suffix,
		TokenPrefix:      "fcacr_abcdefghij",
		TokenHash:        strings.Repeat("a", 64),
		RepositoryScopes: []string{"owner/repo"},
		Scopes:           []string{"context:read"},
		ActorID:          "actor_1",
	}
}

func validCredentialRotationInput(source contractsv1.ClientCredential, suffix, hash string, immediate bool) storage.CredentialRotationInput {
	overlap := time.Minute
	if immediate {
		overlap = 0
	}
	return storage.CredentialRotationInput{
		OrgID: source.OrgID, SourceCredentialID: source.CredentialID, ActorID: "actor_1",
		Replacement: storage.CredentialRotationReplacement{
			CredentialID: "cred_" + suffix, Name: "credential " + suffix, TokenPrefix: "fcacr_abcdefghij", TokenHash: hash,
			RepositoryScopes: []string{"owner/repo"}, Scopes: []string{"context:read"}, Overlap: overlap, Immediate: immediate,
		},
	}
}

func mustCredentialStore(t *testing.T, audits ...*AuditStore) *storage.CredentialLifecycle {
	t.Helper()
	store, err := NewCredentialStore(audits...)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
