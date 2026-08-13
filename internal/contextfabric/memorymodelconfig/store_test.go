package memorymodelconfig_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memorymodelconfig"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func validWriteRequest() contractsv1.ContextFabricOrgModelConfigWriteRequest {
	return contractsv1.ContextFabricOrgModelConfigWriteRequest{
		SchemaVersion: contractsv1.ContextFabricOrgModelConfigWriteRequestSchema,
		Provider:      "acme-gateway",
		BaseURL:       "https://llm.acme-gateway.example/v1/",
		Model:         "acme-large",
		FallbackModel: "acme-large-fallback",
		Credential:    "sk-acme-live-a1b2c3d4e5f6wxyz",
	}
}

func principal(orgID string) storage.Principal {
	return storage.Principal{OrgID: orgID}
}

// TestUpsertThenGet_masksCredential is AC-3775-4's core proof: reading a
// stored configuration back through the store's own contract-facing method
// never returns the plaintext credential.
func TestUpsertThenGet_masksCredential(t *testing.T) {
	store := memorymodelconfig.NewStore(nil)
	written := validWriteRequest()
	if _, err := store.UpsertOrgModelConfig(context.Background(), principal("org-a"), written); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	got, err := store.GetOrgModelConfig(context.Background(), principal("org-a"))
	if err != nil {
		t.Fatalf("GetOrgModelConfig: %v", err)
	}
	if got.CredentialMasked == written.Credential {
		t.Fatal("GetOrgModelConfig returned the plaintext credential")
	}
	if got.CredentialMasked != contractsv1.MaskContextFabricOrgModelCredential(written.Credential) {
		t.Fatalf("CredentialMasked = %q, want the masked form", got.CredentialMasked)
	}
	if got.Provider != written.Provider || got.Model != written.Model {
		t.Fatalf("got %+v, want provider/model to match the write", got)
	}
}

// TestGetOrgModelConfig_returnsNotFound_forUnconfiguredOrg is the fixture
// for AC-3775-3's "no configuration -> deployment default" branch: the
// store must signal absence distinctly from any other failure.
func TestGetOrgModelConfig_returnsNotFound_forUnconfiguredOrg(t *testing.T) {
	store := memorymodelconfig.NewStore(nil)
	_, err := store.GetOrgModelConfig(context.Background(), principal("org-never-configured"))
	if !errors.Is(err, contextfabric.ErrOrgModelConfigNotFound) {
		t.Fatalf("err = %v, want ErrOrgModelConfigNotFound", err)
	}
}

// TestResolveOrgModelConfig_returnsPlaintextCredential locks that the
// resolver seam -- and only the resolver seam -- returns the credential a
// caller actually wrote, so modelruntimeresolver can hand it to
// modelprovider.New.
func TestResolveOrgModelConfig_returnsPlaintextCredential(t *testing.T) {
	store := memorymodelconfig.NewStore(nil)
	written := validWriteRequest()
	if _, err := store.UpsertOrgModelConfig(context.Background(), principal("org-a"), written); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	resolved, ok, err := store.ResolveOrgModelConfig(context.Background(), "org-a")
	if err != nil {
		t.Fatalf("ResolveOrgModelConfig: %v", err)
	}
	if !ok {
		t.Fatal("ResolveOrgModelConfig reported no configuration for a configured org")
	}
	if resolved.Credential != written.Credential {
		t.Fatalf("Credential = %q, want %q", resolved.Credential, written.Credential)
	}
}

// TestResolveOrgModelConfig_returnsFalse_forUnconfiguredOrg locks the
// AC-3775-3 fall-through signal: absence is (zero, false, nil), never an
// error -- an error would incorrectly make the caller treat "never
// configured" the same as "broken credential" (both are 503-worthy only
// for a configured-but-broken org, per the explicit no-silent-fallback
// prohibition).
func TestResolveOrgModelConfig_returnsFalse_forUnconfiguredOrg(t *testing.T) {
	store := memorymodelconfig.NewStore(nil)
	resolved, ok, err := store.ResolveOrgModelConfig(context.Background(), "org-never-configured")
	if err != nil {
		t.Fatalf("ResolveOrgModelConfig: %v", err)
	}
	if ok {
		t.Fatal("ResolveOrgModelConfig reported a configuration for an unconfigured org")
	}
	if resolved != (contextfabric.ResolvedOrgModelConfig{}) {
		t.Fatalf("resolved = %+v, want the zero value", resolved)
	}
}

// TestOrgIsolation_upsertingOneOrgNeverAffectsAnother is AC-3775-1/AC-3775-2's
// store-level proof: two organizations' configurations are fully
// independent.
func TestOrgIsolation_upsertingOneOrgNeverAffectsAnother(t *testing.T) {
	store := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	a := validWriteRequest()
	b := validWriteRequest()
	b.Provider = "other-gateway"
	b.Model = "other-model"
	b.Credential = "sk-other-live-zzzzzzzz9999"
	if _, err := store.UpsertOrgModelConfig(ctx, principal("org-a"), a); err != nil {
		t.Fatalf("UpsertOrgModelConfig org-a: %v", err)
	}
	if _, err := store.UpsertOrgModelConfig(ctx, principal("org-b"), b); err != nil {
		t.Fatalf("UpsertOrgModelConfig org-b: %v", err)
	}
	if err := store.DeleteOrgModelConfig(ctx, principal("org-a")); err != nil {
		t.Fatalf("DeleteOrgModelConfig org-a: %v", err)
	}
	if _, err := store.GetOrgModelConfig(ctx, principal("org-a")); !errors.Is(err, contextfabric.ErrOrgModelConfigNotFound) {
		t.Fatalf("org-a err = %v, want ErrOrgModelConfigNotFound after delete", err)
	}
	gotB, err := store.GetOrgModelConfig(ctx, principal("org-b"))
	if err != nil {
		t.Fatalf("GetOrgModelConfig org-b: %v", err)
	}
	if gotB.Provider != b.Provider || gotB.Model != b.Model {
		t.Fatalf("org-b config was affected by org-a's delete: %+v", gotB)
	}
}

// TestUpsert_overwritesPriorConfiguration locks full-replace semantics: a
// second PUT for the same org fully replaces the first, and preserves the
// original created_at while advancing updated_at.
func TestUpsert_overwritesPriorConfiguration(t *testing.T) {
	fixed := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	tick := 0
	store := memorymodelconfig.NewStore(func() time.Time {
		tick++
		return fixed.Add(time.Duration(tick) * time.Minute)
	})
	ctx := context.Background()
	first, err := store.UpsertOrgModelConfig(ctx, principal("org-a"), validWriteRequest())
	if err != nil {
		t.Fatalf("first UpsertOrgModelConfig: %v", err)
	}
	second := validWriteRequest()
	second.Model = "acme-large-v2"
	updated, err := store.UpsertOrgModelConfig(ctx, principal("org-a"), second)
	if err != nil {
		t.Fatalf("second UpsertOrgModelConfig: %v", err)
	}
	if updated.Model != "acme-large-v2" {
		t.Fatalf("Model = %q, want the replaced value", updated.Model)
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed on update: %v vs %v", updated.CreatedAt, first.CreatedAt)
	}
	if !updated.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt did not advance: %v vs %v", updated.UpdatedAt, first.UpdatedAt)
	}
}

// TestUpsertOrgModelConfig_rejectsInvalidRequest locks that store-level
// writes still enforce ContextFabricOrgModelConfigWriteRequest.Validate(),
// not just the HTTP layer -- defense in depth against a future caller that
// skips the route's own validation.
func TestUpsertOrgModelConfig_rejectsInvalidRequest(t *testing.T) {
	store := memorymodelconfig.NewStore(nil)
	invalid := validWriteRequest()
	invalid.Credential = ""
	if _, err := store.UpsertOrgModelConfig(context.Background(), principal("org-a"), invalid); err == nil {
		t.Fatal("UpsertOrgModelConfig accepted a request with no credential")
	}
}
