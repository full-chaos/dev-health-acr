package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// r1 P1, repro 1 (permanent): through the authenticated handler, a
// repository-restricted caller whose page withheld an edge gets a next_cursor
// from which the withheld relationship id cannot be read.
func TestChaos7074_R1_HTTPCursorNamesNoWithheldRelationship(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7074Reader(h, chaos7074RouteGraph())
	response := h.postRelationships(token, chaos7074ValidBody)
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	var page directread.RelationshipsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Withheld.EdgesNotVisible != 1 || page.Page.NextCursor == "" {
		t.Fatalf("fixture drifted: %s", response.Body.String())
	}
	readable := page.Page.NextCursor
	for _, part := range strings.Split(page.Page.NextCursor, ".") {
		if raw, err := base64.RawURLEncoding.DecodeString(part); err == nil {
			readable += "\n" + string(raw)
		}
	}
	if strings.Contains(readable, "rel-2") || strings.Contains(readable, "team:gone") {
		t.Fatalf("next_cursor reveals the withheld edge: %q", readable)
	}
}

// r1 P3, repro (permanent): the route refuses a types array the published
// schema forbids.
func TestChaos7074_R1_HTTPRefusesInvalidTypesArray(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7074Reader(h, chaos7074RouteGraph())
	thirteen := strings.TrimSuffix(strings.Repeat(`"OWNED_BY_TEAM",`, 13), ",")
	for _, types := range []string{thirteen, `"BLOCKS","BLOCKS"`} {
		body := `{"subject":{"kind":"repository","canonical_id":"repository:a"},"types":[` + types + `]}`
		assertErrorResponse(t, h.postRelationships(token, body), http.StatusBadRequest, "invalid_request")
	}
}
