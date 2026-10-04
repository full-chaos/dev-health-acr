package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAProviderDefaultWindowIsOneStandingCommitmentByDesign pins today's rule:
// a team rollup read with no evidence window uses the provider's own trailing
// window, resolved from its clock. That window is one standing commitment:
// its moving bounds reach the service's model input but not the client input,
// so two reads minutes apart over the same rows give one digest. Pinning one
// clock per write-back would put the bounds back in the digest; when that
// lands, this test changes to expect them in the client input.
func TestAProviderDefaultWindowIsOneStandingCommitmentByDesign(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 123456789, time.UTC)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t1", Label: "subject-1"}
	read := func(now time.Time) (server, client []byte) {
		t.Helper()
		restore := devhealthfacts.SetClockForTest(func() time.Time { return now })
		defer restore()
		provider := findProvider(t, devhealthfacts.NewProviders(&universalClient{subjectID: "t1", rows: 1, rules: clockTestRules()}), contextfabric.FactIncidents)
		registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		question := contextfabric.InterpretedQuestion{RequestedJudgment: "incidents", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-x", RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
			Question: question, Subjects: []contextfabric.SubjectRef{subject},
			Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactIncidents, Subjects: []contextfabric.SubjectRef{subject}}},
		})
		if err != nil || len(bundle.Facts) == 0 {
			t.Fatalf("read: %v, %d facts", err, len(bundle.Facts))
		}
		input := contextfabric.SynthesisInput{Request: contextfabric.InvestigationRequest{Question: "incidents"}, Interpretation: question, Facts: bundle}
		if server, err = synthesisprompt.UserPayload("org-x", input, 1<<20); err != nil {
			t.Fatal(err)
		}
		if client, err = synthesisprompt.ClientPayload("org-x", input, 1<<20); err != nil {
			t.Fatal(err)
		}
		return server, client
	}
	windowOf := func(payload []byte) (basis, start, end any) {
		t.Helper()
		var decoded struct {
			Facts []struct {
				Fields map[string]map[string]any `json:"fields"`
			} `json:"canonical_facts"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil || len(decoded.Facts) == 0 {
			t.Fatalf("decode: %v", err)
		}
		fields := decoded.Facts[0].Fields
		return fields["window_basis"]["string"], fields["window_start"]["string"], fields["window_end"]["string"]
	}
	server1, client1 := read(t0)
	server2, client2 := read(t0.Add(7 * time.Minute))
	basis, start1, end1 := windowOf(server1)
	_, start2, end2 := windowOf(server2)
	if basis != "default_trailing" || start1 == nil || start1 == start2 || end1 == end2 {
		t.Fatalf("service input window = %v %v..%v then %v..%v: the provider default window did not move with the clock, so this test proves nothing", basis, start1, end1, start2, end2)
	}
	if !bytes.Equal(client1, client2) {
		t.Fatalf("the client input moved with the provider's default window:\n%s\n%s", client1, client2)
	}
	if _, clientStart, clientEnd := windowOf(client1); clientStart != nil || clientEnd != nil {
		t.Fatalf("the client input carries the moving bounds %v..%v", clientStart, clientEnd)
	}
}
