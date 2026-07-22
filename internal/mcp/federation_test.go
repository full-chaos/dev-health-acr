package mcp

import (
	"crypto/sha256"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestFederationMapsLocalEvidenceToValidatedObservedClaim(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := sidecar.LocalEvidenceBundle{ProviderID: "codegraph", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1", IndexedAt: &now, Warnings: []string{"indexed_commit_unknown"}, Evidence: []sidecar.LocalExpandedEvidence{{ID: "private", Locator: "symbol", Title: "Widget", Excerpt: "safe", EstimatedTokens: 10, QueryID: "query", Relation: "definition", RepositoryPath: "internal/widget.go", StartLine: 12}}}
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, sha256.Sum256)

	// When
	items, refs, err := runtime.mapBundle("acme/widgets", bundle, map[string]struct{}{})

	// Then
	if err != nil || len(items) != 1 || len(refs) != 1 {
		t.Fatalf("mapBundle() = items=%d refs=%d err=%v", len(items), len(refs), err)
	}
	if items[0].ClaimKind != contractsv1.ClaimObserved || items[0].Flags.UntrustedContent != true || refs[0].Source.System != "local_index" {
		t.Fatalf("mapped evidence = %#v %#v", items[0], refs[0])
	}
	if err := refs[0].Validate(); err != nil {
		t.Fatalf("mapped reference must validate: %v", err)
	}
}

func TestFederationCacheExpiresWithoutSlidingRefresh(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cache := newLocalEvidenceCache(1, time.Minute, clock)
	value := cachedLocalEvidence{ref: contractsv1.EvidenceRef{EvidenceRefID: "local:codegraph:v1:test", Metadata: map[string]any{"query_id": "q"}}}
	cache.putBatch([]cachedLocalEvidence{value})

	// When
	_, firstHit := cache.get(value.ref.EvidenceRefID)
	now = now.Add(time.Minute)
	_, expiredHit := cache.get(value.ref.EvidenceRefID)

	// Then
	if !firstHit || expiredHit {
		t.Fatalf("cache hit sequence = first:%t expired:%t", firstHit, expiredHit)
	}
}
