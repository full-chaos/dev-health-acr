package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every hosted API call a forwarded-client derived client makes states the
// caller's address. The table enumerates the client's exported methods by
// reflection: a new method that is not listed here fails the test, so a call
// site can never be added that forgets the header.
func TestForwardedClientIsSentOnEveryClientCall(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" xff="+r.Header.Get("X-Forwarded-For"))
		mu.Unlock()
		writeRaw(w, http.StatusInternalServerError, `{}`)
	}))
	t.Cleanup(server.Close)
	base, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	client, err := base.WithForwardedClient("203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw := json.RawMessage(`{}`)
	investigationRequest := contractsv1.ContextFabricInvestigationRequest{
		Question:    "What is blocking the payments project?",
		TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10,
			MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
	}
	calls := map[string]func(){
		"Capabilities":     func() { _, _ = client.Capabilities(ctx) },
		"ContextPacket":    func() { _, _ = client.ContextPacket(ctx, validContextPacketRequest()) },
		"DataCatalog":      func() { _, _ = client.DataCatalog(ctx, nil) },
		"Evidence":         func() { _, _ = client.Evidence(ctx, "ev_1") },
		"EvidenceInResult": func() { _, _ = client.EvidenceInResult(ctx, "ev_1", "res_00001") },
		"FindSubjects": func() {
			_, _ = client.FindSubjects(ctx, contractsv1.MCPFindSubjectsRequest{Query: "acme/a", Kinds: []string{"repository"}, Limit: 5})
		},
		"Investigate": func() { _, _ = client.Investigate(ctx, investigationRequest) },
		"InvestigateWithRequestID": func() {
			_, _, _ = client.InvestigateWithRequestID(ctx, investigationRequest)
		},
		"InvestigationResult":     func() { _, _ = client.InvestigationResult(ctx, "res_00001") },
		"ReadDirectFacts":         func() { _, _ = client.ReadDirectFacts(ctx, raw) },
		"ReadDirectRelationships": func() { _, _ = client.ReadDirectRelationships(ctx, raw) },
		"RecordEpisode":           func() { _, _ = client.RecordEpisode(ctx, validAgentEpisodeCreate()) },
		"RunOperation":            func() { _, _ = client.RunOperation(ctx, contractsv1.MCPRunOperationRequest{Operation: "hotspots"}) },
	}
	// Not caller-scoped calls: derivation helpers, the unauthenticated
	// liveness probe, and the credential lifecycle commands (CLI only, never
	// reached from a hosted request).
	notCallerScoped := map[string]bool{
		"WithCredentialSource": true, "WithForwardedClient": true, "Reachable": true,
		"RotateOwnCredential": true, "RevokeOwnCredential": true, "RollbackOwnCredential": true,
	}
	var unlisted []string
	clientType := reflect.TypeOf(client)
	for i := 0; i < clientType.NumMethod(); i++ {
		name := clientType.Method(i).Name
		if _, ok := calls[name]; !ok && !notCallerScoped[name] {
			unlisted = append(unlisted, name)
		}
	}
	if len(unlisted) > 0 {
		sort.Strings(unlisted)
		t.Fatalf("client methods %v are not covered by the forwarded-client table: add each (or list it as not caller-scoped)", unlisted)
	}
	for name, call := range calls {
		mu.Lock()
		before := len(seen)
		mu.Unlock()
		call()
		mu.Lock()
		got := append([]string(nil), seen[before:]...)
		mu.Unlock()
		if len(got) == 0 {
			t.Errorf("%s: made no hosted API call (adjust the table's arguments)", name)
		}
		for _, line := range got {
			if want := " xff=203.0.113.9"; len(line) < len(want) || line[len(line)-len(want):] != want {
				t.Errorf("%s: %q, want X-Forwarded-For 203.0.113.9", name, line)
			}
		}
	}
}

func TestWithForwardedClientRefusesFreeText(t *testing.T) {
	base := newDataClient(t, func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, `{}`) })
	for _, bad := range []string{"", "unknown", "1.2.3.4, 5.6.7.8", "evil\nX: y", "203.0.113.9:443"} {
		if _, err := base.WithForwardedClient(bad); err == nil {
			t.Errorf("WithForwardedClient(%q) accepted a value that is not an IP literal", bad)
		}
	}
	mapped, err := base.WithForwardedClient("::ffff:203.0.113.9")
	if err != nil || mapped.forwardedClient != "203.0.113.9" {
		t.Fatalf("mapped IPv6 = %q, %v; want the unmapped IPv4", mapped.forwardedClient, err)
	}
}
