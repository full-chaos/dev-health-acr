package sidecar

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeLocalEvidenceBundle_normalizesNilEvidenceWithoutMutatingInput(t *testing.T) {
	// Given
	bundle := LocalEvidenceBundle{ProviderID: "fixture", ProviderVersion: "1.0.0", QueryID: "task-context", QueryVersion: "v1"}

	// When
	normalized, err := NormalizeLocalEvidenceBundle(bundle)

	// Then
	if err != nil {
		t.Fatalf("NormalizeLocalEvidenceBundle() error = %v", err)
	}
	if normalized.Evidence == nil {
		t.Fatal("normalized Evidence is nil")
	}
	if bundle.Evidence != nil {
		t.Fatal("NormalizeLocalEvidenceBundle() mutated the input bundle")
	}
}

func TestLocalIndexProviderContract_OversizedBundle(t *testing.T) {
	// Given
	const canary = "/private/acr/secret-index-path"
	bundle := LocalEvidenceBundle{
		ProviderID:      "fixture",
		ProviderVersion: "1.0.0",
		QueryID:         "task-context",
		QueryVersion:    "v1",
		Evidence: []LocalExpandedEvidence{{
			ID:              "evidence-1",
			Locator:         "locator-1",
			Title:           "Relevant local symbol",
			Excerpt:         strings.Repeat("x", maxLocalEvidenceBundlePayloadBytes) + canary,
			EstimatedTokens: 1,
		}},
	}

	// When
	_, err := NormalizeLocalEvidenceBundle(bundle)

	// Then
	if !errors.Is(err, ErrInvalidLocalEvidenceBundle) {
		t.Fatalf("NormalizeLocalEvidenceBundle() error = %v, want ErrInvalidLocalEvidenceBundle", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("validation error leaked evidence content: %q", err)
	}
}

func TestLocalIndexProviderContract_InvalidBundle(t *testing.T) {
	// Given
	bundle := LocalEvidenceBundle{
		ProviderID:      "fixture",
		ProviderVersion: "1.0.0",
		QueryID:         "task-context",
		QueryVersion:    "v1",
		Evidence:        []LocalExpandedEvidence{{Locator: "locator-1", Title: "Missing opaque evidence ID", EstimatedTokens: 1}},
	}

	// When
	_, err := NormalizeLocalEvidenceBundle(bundle)

	// Then
	if !errors.Is(err, ErrInvalidLocalEvidenceBundle) {
		t.Fatalf("NormalizeLocalEvidenceBundle() error = %v, want ErrInvalidLocalEvidenceBundle", err)
	}
}

func TestLocalIndexProviderContract_Cancellation(t *testing.T) {
	// Given
	provider := NewDisabledLocalIndexProvider()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := LocalContextRequest{TaskID: "task-1", Task: "summarize", MaxItems: 1, MaxOutputTokens: 128}

	// When / Then
	if _, err := provider.Capabilities(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Capabilities() error = %v, want context.Canceled", err)
	}
	if _, err := provider.ContextForTask(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("ContextForTask() error = %v, want context.Canceled", err)
	}
	if _, err := provider.ResolveEvidence(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveEvidence() error = %v, want context.Canceled", err)
	}
}

func TestLocalIndexProviderContract_NotFound(t *testing.T) {
	// Given
	provider := NewDisabledLocalIndexProvider()

	// When
	_, err := provider.ResolveEvidence(context.Background(), "missing")

	// Then
	if !errors.Is(err, ErrLocalEvidenceNotFound) {
		t.Fatalf("ResolveEvidence() error = %v, want ErrLocalEvidenceNotFound", err)
	}
}

func TestValidateLocalContextRequest_rejectsUnboundedRequest(t *testing.T) {
	// Given
	request := LocalContextRequest{TaskID: "task-1", Task: "summarize", MaxItems: maxLocalEvidenceItems + 1, MaxOutputTokens: 128}

	// When
	err := ValidateLocalContextRequest(request)

	// Then
	if !errors.Is(err, ErrInvalidLocalContextRequest) {
		t.Fatalf("ValidateLocalContextRequest() error = %v, want ErrInvalidLocalContextRequest", err)
	}
}

func TestNormalizeLocalEvidenceBundleForRequest_rejectsSmallerRequestLimitsBeforeCopy(t *testing.T) {
	// Given
	capabilities := LocalIndexCapabilities{ProviderID: "fixture", ProviderVersion: "1.0.0", Available: true, MaxItems: 8, MaxOutputTokens: 128}
	evidence := LocalExpandedEvidence{ID: "evidence-1", Locator: "locator-1", Title: "Relevant local symbol", Excerpt: "safe excerpt", EstimatedTokens: 1}
	cases := []struct {
		name    string
		request LocalContextRequest
		bundle  LocalEvidenceBundle
	}{
		{
			name:    "items",
			request: LocalContextRequest{TaskID: "task-1", Task: "summarize", MaxItems: 1, MaxOutputTokens: 128},
			bundle: LocalEvidenceBundle{
				ProviderID: "fixture", ProviderVersion: "1.0.0", QueryID: "task-context", QueryVersion: "v1",
				Evidence: []LocalExpandedEvidence{evidence, {ID: "evidence-2", Locator: "locator-2", Title: "Second local symbol", Excerpt: "safe excerpt", EstimatedTokens: 1}},
			},
		},
		{
			name:    "tokens",
			request: LocalContextRequest{TaskID: "task-1", Task: "summarize", MaxItems: 1, MaxOutputTokens: 1},
			bundle: LocalEvidenceBundle{
				ProviderID: "fixture", ProviderVersion: "1.0.0", QueryID: "task-context", QueryVersion: "v1",
				Evidence: []LocalExpandedEvidence{{ID: "evidence-1", Locator: "locator-1", Title: "Relevant local symbol", Excerpt: "safe excerpt", EstimatedTokens: 2}},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// When
			_, err := NormalizeLocalEvidenceBundleForRequest(testCase.request, capabilities, testCase.bundle)

			// Then
			if !errors.Is(err, ErrInvalidLocalEvidenceBundle) {
				t.Fatalf("NormalizeLocalEvidenceBundleForRequest() error = %v, want ErrInvalidLocalEvidenceBundle", err)
			}
		})
	}
}

func TestNormalizeLocalEvidenceBundleForRequest_rejectsMetadataMismatch(t *testing.T) {
	// Given
	request := LocalContextRequest{TaskID: "task-1", Task: "summarize", MaxItems: 1, MaxOutputTokens: 128}
	capabilities := LocalIndexCapabilities{ProviderID: "fixture", ProviderVersion: "1.0.0", Available: true, MaxItems: 1, MaxOutputTokens: 128}
	bundle := LocalEvidenceBundle{ProviderID: "fixture", ProviderVersion: "2.0.0", QueryID: "task-context", QueryVersion: "v1"}

	// When
	_, err := NormalizeLocalEvidenceBundleForRequest(request, capabilities, bundle)

	// Then
	if !errors.Is(err, ErrInvalidLocalEvidenceBundle) {
		t.Fatalf("NormalizeLocalEvidenceBundleForRequest() error = %v, want ErrInvalidLocalEvidenceBundle", err)
	}
}
