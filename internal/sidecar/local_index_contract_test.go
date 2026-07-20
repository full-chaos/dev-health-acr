package sidecar

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeLocalIndexProvider struct {
	capabilities LocalIndexCapabilities
	bundle       LocalEvidenceBundle
	evidence     map[string]LocalExpandedEvidence
}

func (p fakeLocalIndexProvider) Capabilities(ctx context.Context) (LocalIndexCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return LocalIndexCapabilities{}, err
	}
	return p.capabilities, nil
}

func (p fakeLocalIndexProvider) ContextForTask(ctx context.Context, request LocalContextRequest) (LocalEvidenceBundle, error) {
	if err := ctx.Err(); err != nil {
		return LocalEvidenceBundle{}, err
	}
	if err := ValidateLocalContextRequest(request); err != nil {
		return LocalEvidenceBundle{}, err
	}
	return NormalizeLocalEvidenceBundleForRequest(request, p.capabilities, p.bundle)
}

func (p fakeLocalIndexProvider) ResolveEvidence(ctx context.Context, id string) (LocalExpandedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return LocalExpandedEvidence{}, err
	}
	evidence, found := p.evidence[id]
	if !found {
		return LocalExpandedEvidence{}, ErrLocalEvidenceNotFound
	}
	return evidence, nil
}

func assertLocalIndexProviderContract(t *testing.T, provider LocalIndexProvider) {
	t.Helper()

	// Given
	ctx := context.Background()
	request := LocalContextRequest{TaskID: "task-1", Goal: "summarize the local evidence", MaxItems: 1, MaxOutputTokens: 128}

	// When
	capabilities, err := provider.Capabilities(ctx)

	// Then
	if err != nil && capabilities.Available {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if err := ValidateLocalIndexCapabilities(capabilities); err != nil {
		t.Fatalf("Capabilities() returned invalid capabilities: %v", err)
	}
	repeatedCapabilities, err := provider.Capabilities(ctx)
	if err != nil {
		t.Fatalf("second Capabilities() error = %v", err)
	}
	if !reflect.DeepEqual(capabilities, repeatedCapabilities) {
		t.Fatalf("Capabilities() is not deterministic: first=%#v second=%#v", capabilities, repeatedCapabilities)
	}
	_, err = provider.ContextForTask(ctx, LocalContextRequest{})
	if !errors.Is(err, ErrInvalidLocalContextRequest) {
		t.Fatalf("ContextForTask(invalid) error = %v, want ErrInvalidLocalContextRequest", err)
	}

	if capabilities.Available {
		bundle, err := provider.ContextForTask(ctx, request)
		if err != nil {
			t.Fatalf("ContextForTask() error = %v", err)
		}
		if err := ValidateLocalEvidenceBundleForRequest(request, capabilities, bundle); err != nil {
			t.Fatalf("ContextForTask() returned invalid bundle: %v", err)
		}
		if len(bundle.Evidence) == 0 {
			t.Fatal("available provider returned no evidence for contract fixture")
		}
		repeatedBundle, err := provider.ContextForTask(ctx, request)
		if err != nil {
			t.Fatalf("second ContextForTask() error = %v", err)
		}
		if !reflect.DeepEqual(bundle, repeatedBundle) {
			t.Fatalf("ContextForTask() is not deterministic: first=%#v second=%#v", bundle, repeatedBundle)
		}
		resolved, err := provider.ResolveEvidence(ctx, bundle.Evidence[0].Locator)
		if err != nil {
			t.Fatalf("ResolveEvidence() error = %v", err)
		}
		if err := ValidateLocalExpandedEvidence(resolved); err != nil {
			t.Fatalf("ResolveEvidence() returned invalid evidence: %v", err)
		}
		if resolved.ID != bundle.Evidence[0].ID || resolved.Locator != bundle.Evidence[0].Locator {
			t.Fatalf("ResolveEvidence() identity = %#v, want ID=%q Locator=%q", resolved, bundle.Evidence[0].ID, bundle.Evidence[0].Locator)
		}
		_, err = provider.ResolveEvidence(ctx, "missing")
		if !errors.Is(err, ErrLocalEvidenceNotFound) {
			t.Fatalf("ResolveEvidence(missing) error = %v, want ErrLocalEvidenceNotFound", err)
		}
	} else {
		var localErr *LocalIndexError
		if !errors.As(err, &localErr) || localErr.Status() != LocalIndexStatusUnavailable || localErr.Freshness() != LocalIndexFreshnessUnknown {
			t.Fatalf("Capabilities() unavailable error = %v, want typed unavailable state", err)
		}
		_, err := provider.ContextForTask(ctx, request)
		if !errors.Is(err, ErrLocalIndexUnavailable) {
			t.Fatalf("ContextForTask() error = %v, want ErrLocalIndexUnavailable", err)
		}
		_, err = provider.ResolveEvidence(ctx, "missing")
		if !errors.Is(err, ErrLocalEvidenceNotFound) {
			t.Fatalf("ResolveEvidence() error = %v, want ErrLocalEvidenceNotFound", err)
		}
	}

	// Given
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	// When / Then
	if _, err := provider.Capabilities(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Capabilities(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := provider.ContextForTask(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("ContextForTask(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := provider.ResolveEvidence(cancelled, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveEvidence(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestLocalIndexProviderContract_fakeProvider(t *testing.T) {
	// Given
	evidence := LocalExpandedEvidence{ID: "evidence-1", Locator: "locator-1", Title: "Relevant local symbol", Excerpt: "safe excerpt", EstimatedTokens: 1}
	provider := fakeLocalIndexProvider{
		capabilities: LocalIndexCapabilities{ProviderID: "fixture", ProviderVersion: "1.0.0", Available: true, MaxItems: 1, MaxOutputTokens: 128},
		bundle:       LocalEvidenceBundle{ProviderID: "fixture", ProviderVersion: "1.0.0", QueryID: "task-context", QueryVersion: "v1", Evidence: []LocalExpandedEvidence{evidence}},
		evidence:     map[string]LocalExpandedEvidence{evidence.Locator: evidence},
	}

	// When / Then
	assertLocalIndexProviderContract(t, provider)
}

func TestLocalIndexProviderContract_disabledProvider(t *testing.T) {
	// Given
	provider := NewDisabledLocalIndexProvider()

	// When
	_, err := provider.Capabilities(t.Context())

	// Then
	if !errors.Is(err, ErrLocalIndexUnavailable) {
		t.Fatalf("Capabilities() error = %v, want ErrLocalIndexUnavailable", err)
	}
}
