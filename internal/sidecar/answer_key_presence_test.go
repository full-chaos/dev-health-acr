package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The sidecar client refuses an absent or null wire-required key on a hosted
// investigation result -- the same check the panel harness now runs, through
// the same walker. Pinned per key and form on the PUBLISHED unsupported
// example, whose answer sentence is legitimately present and empty: the
// control. The sibling keys are the ones that were already exposed wherever
// a client skipped the walker.
func TestInvestigationResultRefusesAnAbsentOrNullRequiredStringKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_unsupported.v1.json"))
	if err != nil {
		t.Fatalf("read the published unsupported example: %v", err)
	}
	var example struct {
		ResultID string `json:"result_id"`
	}
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	fetch := func(t *testing.T, edit func(map[string]any)) error {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		edit(doc)
		body, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		defer server.Close()
		client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.InvestigationResult(context.Background(), example.ResultID)
		return err
	}

	if err := fetch(t, func(map[string]any) {}); err != nil {
		t.Fatalf("CONTROL: the published unsupported example (answer sentence present and empty) was refused: %v", err)
	}
	for _, key := range []string{"deterministic_answer", "direct_judgment", "current_state"} {
		for _, form := range []string{"absent", "null"} {
			key, form := key, form
			t.Run(key+"/"+form, func(t *testing.T) {
				err := fetch(t, func(doc map[string]any) {
					if form == "absent" {
						delete(doc, key)
					} else {
						doc[key] = nil
					}
				})
				if err == nil {
					t.Fatalf("a hosted result with %s %s reached the caller; the published schema requires the key", key, form)
				}
			})
		}
	}
}
