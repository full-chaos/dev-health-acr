package memoryinvestigation

import (
	"context"
	"strings"
	"testing"

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

	if err := store.Save(context.Background(), principal, valid); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Get(context.Background(), principal, valid.ResultID)
	if err != nil {
		t.Fatalf("Get() error = %v, want a valid stored result to be returned", err)
	}
	if got.ResultID != valid.ResultID {
		t.Fatalf("Get() result_id = %q, want %q", got.ResultID, valid.ResultID)
	}
}
