package panelharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A wire-required string key that may legitimately be EMPTY cannot be told
// apart from an absent or null key once encoding/json has decoded it: all
// three become "". The panel harness decodes the hosted API's result itself,
// so it must refuse a response the published schema refuses -- an absent or
// null required key -- before the typed result reaches a caller. The
// sibling keys (direct_judgment, current_state) had the same exposure before
// the answer sentence joined them on an unsupported result.
//
// The served document is the PUBLISHED unsupported example, so the control
// (every key present, the answer sentence legitimately empty) is the real
// widened form and must be accepted.
func TestClient_InvestigateRefusesAnAbsentOrNullRequiredStringKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_unsupported.v1.json"))
	if err != nil {
		t.Fatalf("read the published unsupported example: %v", err)
	}
	serve := func(t *testing.T, edit func(map[string]any)) error {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode example: %v", err)
		}
		edit(doc)
		body, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("encode example: %v", err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		defer server.Close()
		client, err := NewClient(server.URL, testBearerToken(9), 5*time.Second)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = client.Investigate(context.Background(), "request_test0009", validRequest())
		return err
	}

	if err := serve(t, func(map[string]any) {}); err != nil {
		t.Fatalf("CONTROL: the published unsupported example (answer sentence present and empty) was refused: %v", err)
	}
	for _, key := range []string{"deterministic_answer", "direct_judgment", "current_state"} {
		for _, form := range []string{"absent", "null"} {
			key, form := key, form
			t.Run(key+"/"+form, func(t *testing.T) {
				err := serve(t, func(doc map[string]any) {
					if form == "absent" {
						delete(doc, key)
					} else {
						doc[key] = nil
					}
				})
				if err == nil {
					t.Fatalf("a result with %s %s was accepted; the published schema requires the key", key, form)
				}
			})
		}
	}
}
