package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestHandleContextForTaskMapsRequestedCategories(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	var received contractsv1.ContextPacketRequest
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode hosted request: %v", err)
		}
		writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(received.RequestID))
	}
	boot := newFixtureBootstrap(t, fx)
	req := callToolRequest(t, map[string]any{
		"goal":                 "inspect local evidence",
		"repository":           map[string]any{"slug": "acme/widgets"},
		"requested_categories": []string{"evidence", "action"},
	})

	// When
	result, err := handleContextForTask(context.Background(), boot, req)

	// Then
	if err != nil || result.IsError {
		t.Fatalf("context_for_task must forward categories, result=%#v err=%v", result, err)
	}
	want := []contractsv1.PacketCategory{contractsv1.CategoryEvidence, contractsv1.CategoryAction}
	if !slices.Equal(received.Options.RequestedCategories, want) {
		t.Fatalf("hosted requested_categories = %v, want %v", received.Options.RequestedCategories, want)
	}
}
