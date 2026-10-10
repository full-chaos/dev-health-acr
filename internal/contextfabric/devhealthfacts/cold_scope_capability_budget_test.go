package devhealthfacts_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// delayClient makes each statement take a fixed time (honouring the caller's
// context) and records when each branch started and ended.
type delayClient struct {
	inner  *fakeClient
	delays map[string]time.Duration
	t0     time.Time
	log    []string
}

func (c *delayClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for match, d := range c.delays {
		if strings.Contains(statement, match) {
			start := time.Since(c.t0).Round(time.Millisecond)
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				c.log = append(c.log, match+" start="+start.String()+" ABORTED at "+time.Since(c.t0).Round(time.Millisecond).String()+": "+ctx.Err().Error())
				return nil, ctx.Err()
			case <-timer.C:
			}
			c.log = append(c.log, match+" start="+start.String()+" end="+time.Since(c.t0).Round(time.Millisecond).String())
			break
		}
	}
	return c.inner.Query(ctx, statement, bindings)
}

func coldScopeRegistry(t *testing.T, client *delayClient) (*contextfabric.FactCapabilityRegistry, contextfabric.CanonicalFactRequest) {
	t.Helper()
	var provider contextfabric.FactProvider
	for _, p := range devhealthfacts.NewProviders(client) {
		if p.Capability().Kind == contextfabric.FactInvestment {
			provider = p
		}
	}
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requirements := []contextfabric.FactRequirement{{Kind: contextfabric.FactInvestment, Parameters: map[string]string{}}}
	return registry, contextfabric.CanonicalFactRequest{
		Question: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: requirements,
		},
		Subjects:     []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:jira:PRJ", Label: "PRJ"}},
		Requirements: requirements,
	}
}

func coldScopeTables() []fakeTable {
	return []fakeTable{
		{match: "sum(cityHash64("},
		{match: projectScopePhaseZero},
		{match: scopeUnitsMatch, rows: [][]any{{[]string{"wu-1", "wu-2"}}}},
		{match: scopeRunMatch, rows: [][]any{{"run-1"}}},
	}
}

func readColdScope(t *testing.T, registry *contextfabric.FactCapabilityRegistry, client *delayClient, request contextfabric.CanonicalFactRequest, org string) []contextfabric.SourceObservation {
	t.Helper()
	client.t0, client.log = time.Now(), nil
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: org}, request)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	t.Logf("total=%s timeline=%v", time.Since(client.t0).Round(time.Millisecond), client.log)
	return bundle.Coverage.Sources
}

func TestFirstProjectInvestmentReadOnAColdMembershipScopeDoesNotSpendTheCapabilityBudget(t *testing.T) {
	client := &delayClient{inner: &fakeClient{tables: coldScopeTables()}, delays: map[string]time.Duration{
		"groupUniqArray(100001)(work_unit_id)": 3500 * time.Millisecond,
		projectScopePhaseZero:                  2 * time.Second,
	}}
	registry, request := coldScopeRegistry(t, client)
	first := readColdScope(t, registry, client, request, "org-cold-scope")
	second := readColdScope(t, registry, client, request, "org-cold-scope")
	if len(first) == 0 {
		t.Fatal("no coverage source: measurement did not happen")
	}
	for i, s := range first {
		if strings.Contains(s.Reason, "timed out") {
			t.Errorf("first read: %s reason=%q", s.Source, s.Reason)
		}
		if s.State != second[i].State || s.Reason != second[i].Reason {
			t.Errorf("first read %s=%s/%q differs from the warm read %s/%q", s.Source, s.State, s.Reason, second[i].State, second[i].Reason)
		}
	}
}

func TestColdMembershipScopeLoadFailureNeverReportsACapabilityTimeout(t *testing.T) {
	tables := coldScopeTables()
	for i := range tables {
		if tables[i].match == scopeUnitsMatch {
			tables[i].err = errors.New("scope load failed")
		}
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	client := &delayClient{inner: &fakeClient{tables: tables}, delays: map[string]time.Duration{}}
	registry, request := coldScopeRegistry(t, client)
	got := readColdScope(t, registry, client, request, "org-cold-scope-fail")
	if len(got) == 0 {
		t.Fatal("no coverage source: measurement did not happen")
	}
	for _, s := range got {
		if strings.Contains(s.Reason, "timed out") {
			t.Errorf("%s reason=%q", s.Source, s.Reason)
		}
	}
	if !strings.Contains(logs.String(), "devhealthfacts.membership_scope_fallback") || !strings.Contains(logs.String(), "reason=load_failed") {
		t.Fatalf("a failed scope load must emit the scope fallback WARN with reason=load_failed:\n%s", logs.String())
	}
	phase0 := phaseZeroStatements(client.inner)
	if len(phase0) == 0 || !strings.Contains(phase0[0].statement, "work_unit_membership") {
		t.Fatal("a failed scope load must degrade to the scope subqueries")
	}
}

func TestColdMembershipScopeLoadIsLoggedOncePerOrganization(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	client := &delayClient{inner: &fakeClient{tables: coldScopeTables()}, delays: map[string]time.Duration{}}
	registry, request := coldScopeRegistry(t, client)
	readColdScope(t, registry, client, request, "org-cold-log")
	readColdScope(t, registry, client, request, "org-cold-log")
	for i := range client.inner.tables {
		if client.inner.tables[i].match == scopeRunMatch {
			client.inner.tables[i].rows = [][]any{{"run-2"}}
		}
	}
	readColdScope(t, registry, client, request, "org-cold-log")
	if loads := countStatements(client.inner, "groupUniqArray(100001)(work_unit_id)"); loads != 2 {
		t.Fatalf("scope loads = %d, want 2 (a new run reloads): measurement did not happen", loads)
	}
	if got := strings.Count(buf.String(), "investment membership scope cold load"); got != 1 {
		t.Fatalf("cold-load lines = %d over two reads, want 1\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "units=2") || !strings.Contains(buf.String(), "duration_ms=") {
		t.Fatalf("cold-load line lacks duration_ms/units: %s", buf.String())
	}
}

func TestInvestmentPrerequisiteLoadCoversEverySubjectKindThatReadsTheScope(t *testing.T) {
	for kind, wantLoads := range map[contextfabric.SubjectKind]int{
		contextfabric.SubjectTeam: 1, contextfabric.SubjectProject: 1, contextfabric.SubjectRepository: 1, contextfabric.SubjectOrganization: 0,
	} {
		client := &delayClient{inner: &fakeClient{tables: coldScopeTables()}, delays: map[string]time.Duration{}}
		var loader contextfabric.FactPrerequisiteLoader
		for _, p := range devhealthfacts.NewProviders(client) {
			if p.Capability().Kind == contextfabric.FactInvestment {
				loader, _ = p.(contextfabric.FactPrerequisiteLoader)
			}
		}
		if loader == nil {
			t.Fatal("the investment provider does not implement FactPrerequisiteLoader")
		}
		query := contextfabric.FactQuery{Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{{Kind: kind, CanonicalID: "x", Label: "x"}}}
		if err := loader.LoadPrerequisites(context.Background(), storage.Principal{OrgID: "org-kinds-" + string(kind)}, query); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got := countStatements(client.inner, "groupUniqArray(100001)(work_unit_id)"); got != wantLoads {
			t.Errorf("%s: scope loads = %d, want %d", kind, got, wantLoads)
		}
	}
}

func TestPrerequisiteLoadFailureIsLoggedNotSwallowed(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	client := &delayClient{inner: &fakeClient{tables: coldScopeTables()}, delays: map[string]time.Duration{}}
	var loader contextfabric.FactPrerequisiteLoader
	for _, p := range devhealthfacts.NewProviders(client) {
		if p.Capability().Kind == contextfabric.FactInvestment {
			loader, _ = p.(contextfabric.FactPrerequisiteLoader)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	query := contextfabric.FactQuery{Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "x", Label: "x"}}}
	if err := loader.LoadPrerequisites(ctx, storage.Principal{OrgID: "org-prereq-fail"}, query); err == nil {
		t.Fatal("a cancelled caller must surface its error: measurement did not happen")
	}
	out := logs.String()
	if !strings.Contains(out, "investment membership scope prerequisite load failed") || !strings.Contains(out, "reason=canceled") || !strings.Contains(out, "duration_ms=") {
		t.Fatalf("prerequisite failure not logged with a reason class:\n%s", out)
	}
}
