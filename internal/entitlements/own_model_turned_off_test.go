package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// ops marks the 404 for an org whose admin turned its saved BYO configuration
// off. It is a known answer, neither "no BYO", "unusable", "gate closed" nor
// "no investigate config", and it is cached like any other ops answer.
func TestOrgLLMConfigTurnedOffIsItsOwnAnswerAndCached(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Org-Llm-Config", "none")
		w.Header().Set("X-Org-Llm-Config-Reason", "byo_llm_turned_off")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found"}`))
	}))
	t.Cleanup(server.Close)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
		if found || err == nil {
			t.Fatalf("read %d: found = %v err = %v, want no config and its own error", i, found, err)
		}
		if errors.Is(err, entitlements.ErrLLMConfigUnusable) || errors.Is(err, entitlements.ErrLLMConfigGateClosed) || errors.Is(err, entitlements.ErrLLMConfigNoInvestigateConfig) {
			t.Fatalf("read %d: err = %v, want neither unusable, gate closed nor no investigate config", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ops calls = %d, want 1 (the answer is cached)", got)
	}
}
