package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// defaultWindowSpy is universalClient that records every time-valued binding
// a statement carries.
type defaultWindowSpy struct {
	*universalClient
	mu    *sync.Mutex
	times *[]time.Time
}

func (c defaultWindowSpy) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.mu.Lock()
	for _, binding := range bindings {
		if instant, ok := binding.Value.(time.Time); ok {
			*c.times = append(*c.times, instant.UTC())
		}
	}
	c.mu.Unlock()
	return c.universalClient.Query(ctx, statement, bindings)
}

// TestServerDefaultWindowKindsAreTheProvidersThatReadTheDefaultWindow
// (CHAOS-11601): the engine states a server_default trailing_90d window only
// when a read of contextfabric.ServerDefaultWindowKinds completed. This reads
// every provider, for every subject kind it serves, on the current axis with
// no evidence window, and records which kinds bind both bounds of the platform
// default window ([start of today - 90 days, start of today) in UTC). The two
// sets must be equal: a provider that starts reading the default window
// without joining the list would be stated as a snapshot, and a listed kind
// that stops reading it would be stated as 90 days it never read.
func TestServerDefaultWindowKindsAreTheProvidersThatReadTheDefaultWindow(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 123456789, time.UTC)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	defaultStart := today.Add(-90 * 24 * time.Hour)
	restore := devhealthfacts.SetClockForTest(func() time.Time { return now })
	defer restore()

	reading := map[contextfabric.FactKind]bool{}
	read := map[contextfabric.FactKind]bool{}
	for _, base := range devhealthfacts.NewProviders(&universalClient{}) {
		capability := base.Capability()
		kind := capability.Kind
		for _, subjectKind := range capability.SupportedSubjectKinds {
			canonicalID, rowID := clockTestSubjectID(subjectKind)
			subject := contextfabric.SubjectRef{Kind: subjectKind, CanonicalID: canonicalID, Label: "subject-1"}
			var mu sync.Mutex
			var times []time.Time
			client := defaultWindowSpy{universalClient: &universalClient{subjectID: rowID, rows: 1, rules: clockTestRules()}, mu: &mu, times: &times}
			operations := sourceHealthHolder(&fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
				`{"provider":"github","scope":"all","lastSyncAt":"2026-09-20T11:00:00Z","lastFailure":null}`)}})
			provider := findProvider(t, devhealthfacts.NewProvidersWithOperations(client, operations), kind)
			registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatalf("%s/%s: registry: %v", kind, subjectKind, err)
			}
			bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-x", RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
				Question:     contextfabric.InterpretedQuestion{RequestedJudgment: "default window", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
				Subjects:     []contextfabric.SubjectRef{subject},
				Requirements: []contextfabric.FactRequirement{{Kind: kind, Subjects: []contextfabric.SubjectRef{subject}}},
			})
			if err != nil {
				t.Fatalf("%s/%s: ReadFacts: %v", kind, subjectKind, err)
			}
			if len(bundle.ReadSubjects[kind]) > 0 {
				read[kind] = true
			}
			mu.Lock()
			if slices.ContainsFunc(times, defaultStart.Equal) && slices.ContainsFunc(times, today.Equal) {
				reading[kind] = true
			}
			mu.Unlock()
		}
	}

	want := map[contextfabric.FactKind]bool{}
	for _, kind := range contextfabric.ServerDefaultWindowKinds() {
		want[kind] = true
		// A measurement that did not happen must fail: a listed kind whose
		// read never completed here proves nothing about its window.
		if !read[kind] {
			t.Errorf("%s: no read completed, so its default window is not measured", kind)
		}
	}
	var missing, extra []string
	for kind := range reading {
		if !want[kind] {
			missing = append(missing, string(kind))
		}
	}
	for kind := range want {
		if !reading[kind] {
			extra = append(extra, string(kind))
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("server_default window kinds drifted from the providers: read the default window but not listed [%s]; listed but did not read it [%s]",
			strings.Join(missing, ", "), strings.Join(extra, ", "))
	}
	if len(reading) == 0 {
		t.Fatal(fmt.Sprintf("no provider bound the default window %s..%s, so this test measures nothing", defaultStart, today))
	}
}
