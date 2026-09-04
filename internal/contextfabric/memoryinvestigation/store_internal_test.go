package memoryinvestigation

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This is a white-box test (package memoryinvestigation, not
// memoryinvestigation_test) because it must plant a stored payload that
// never went through Save. Save validates on write, so the only way to
// reach Get's own validation is to write s.results directly -- which is
// exactly the situation Get's check defends against: a row that got into
// storage some other way (a different binary, a hand-edited row, a future
// schema). Testing through the public API alone cannot construct that
// state, so the guard would sit unprobed. The repository already uses
// same-package test files where a test must observe internals (see
// internal/contextpacket).
func TestGetRejectsStoredResultThatFailsValidation(t *testing.T) {
	t.Parallel()
	const resultID = "result-id-corrupt"
	principal := storage.Principal{OrgID: "org-1"}
	store := NewStore()

	// A syntactically valid but SEMANTICALLY invalid stored result: it
	// decodes cleanly into an InvestigationResult, so json.Unmarshal
	// cannot catch it -- only InvestigationResult.Validate() can. Missing
	// schema version, status, interpretation, coverage and every required
	// collection.
	store.results[resultID] = entry{
		orgID:   principal.OrgID,
		payload: []byte(`{"result_id":"result-id-corrupt","question":"what happened?"}`),
	}

	_, err := store.Get(context.Background(), principal, resultID)
	if err == nil {
		t.Fatal("Get() error = nil, want a stored result that fails Validate() to be rejected rather than returned to a caller")
	}
	if !strings.Contains(err.Error(), "stored investigation result is invalid") {
		t.Fatalf("Get() error = %v, want it to identify the stored result as invalid", err)
	}
}

// TestGetReturnsStoredResultThatPassesValidation is the over-blocking
// guard for the test above: Get's validation must not reject a legitimately
// stored result. Without this, deleting the decode step entirely (or making
// Validate() always fail) would still leave the test above green.
func TestGetReturnsStoredResultThatPassesValidation(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org-1"}
	store := NewStore()
	valid := paritytest.ValidResult("result-id-valid", "is the rollout healthy?")

	if err := store.Save(context.Background(), principal, valid, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, ""); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Get(context.Background(), principal, valid.ResultID)
	if err != nil {
		t.Fatalf("Get() error = %v, want a valid stored result to be returned", err)
	}
	if got.Result.ResultID != valid.ResultID {
		t.Fatalf("Get() result_id = %q, want %q", got.Result.ResultID, valid.ResultID)
	}
}

// TestGetRejectsStoredResultWithExplicitNullDegradedReasons is the P2 fix
// (Codex delta review, CHAOS-3755): Coverage.DegradedReasons is `omitempty`
// in Go and optional (not `null`) in the JSON Schema -- when PRESENT it
// must be an array. encoding/json collapses an OMITTED field and an
// EXPLICIT `null` to the identical Go nil slice on Unmarshal, so
// Validate()'s relaxed nil-check (which correctly accepts the omitted
// case) cannot tell them apart post-decode. A stored row with literal
// `"degraded_reasons": null` is wire-invalid and must be rejected before
// or independent of the struct decode, not silently accepted because it
// happens to decode to the same value as a legitimately absent field.
func TestGetRejectsStoredResultWithExplicitNullDegradedReasons(t *testing.T) {
	t.Parallel()
	const resultID = "result-id-explicit-null"
	principal := storage.Principal{OrgID: "org-1"}
	store := NewStore()
	valid := paritytest.ValidResult(resultID, "is the rollout healthy?")
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	// Plant the EXACT same payload but with degraded_reasons forced to
	// explicit null, simulating a row written by a binary/path that
	// serializes differently (or a hand-edited row).
	tainted := bytes.Replace(encoded, []byte(`"sources":[]`), []byte(`"sources":[],"degraded_reasons":null`), 1)
	if bytes.Equal(tainted, encoded) {
		t.Fatal("test setup: expected substring not found in fixture JSON")
	}
	store.results[resultID] = entry{orgID: principal.OrgID, payload: tainted}

	_, err = store.Get(context.Background(), principal, resultID)
	if err == nil {
		t.Fatal("Get() error = nil, want a stored result with explicit degraded_reasons:null to be rejected")
	}
	if !strings.Contains(err.Error(), "degraded_reasons") || !strings.Contains(err.Error(), "null") {
		t.Fatalf("Get() error = %v, want it to identify the explicit-null degraded_reasons field", err)
	}
}

// TestStore_explicitNullDegradedReasonsParity runs the SHARED explicit-null
// table. Each store carries its own copy of the raw-bytes check, so this
// is what stops the two from drifting apart -- the per-store tests above
// prove this store's behavior, but only the shared table proves both
// stores agree. It lives in the white-box file because seeding a raw row
// needs access to s.results.
func TestStore_explicitNullDegradedReasonsParity(t *testing.T) {
	t.Parallel()
	paritytest.RunExplicitNullDegradedReasonsSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.RawSeed) {
		store := NewStore()
		return store, func(t *testing.T, orgID, resultID string, payload []byte) {
			t.Helper()
			store.mu.Lock()
			defer store.mu.Unlock()
			store.results[resultID] = entry{orgID: orgID, payload: payload}
		}
	})
}
