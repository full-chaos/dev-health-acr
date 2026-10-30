package devhealthfacts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamBlockerStatementMarkers name the two statements of a team blockers read
// that read the edge rows: the per-repository rollup and the team totals with
// the age. Each marker occurs in its own statement only.
var teamBlockerStatementMarkers = []string{
	"SELECT toString(rid), toInt64(uniqExactIf(tgt, is_open))",
	"is_open AND start_unix IS NOT NULL",
}

// teamBlockerTotalsMarker occurs in both team totals statements: the one with
// the age and its totals-only fallback.
const teamBlockerTotalsMarker = "toInt64(uniqExactIf(tgt, is_open AND arm = 'project'))"

// workItemBlockersMarker names the work item blockers statement.
const workItemBlockersMarker = "ifNull(d.relationship_type, ''), toString(t.repo_id)"

// togetherClient holds each statement a marker names until all of them are
// in flight. A statement still alone after wait fails, naming how many were in
// flight, so a read that runs them one after another fails.
type togetherClient struct {
	inner   *fakeClient
	markers []string
	wait    time.Duration
	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func (c *togetherClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for _, marker := range c.markers {
		if !strings.Contains(statement, marker) {
			continue
		}
		c.mu.Lock()
		c.arrived++
		if c.arrived == len(c.markers) {
			close(c.all)
		}
		c.mu.Unlock()
		select {
		case <-c.all:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.wait):
			c.mu.Lock()
			defer c.mu.Unlock()
			return nil, fmt.Errorf("statement %q ran alone: %d of %d statements were in flight", marker, c.arrived, len(c.markers))
		}
		break
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inner.Query(ctx, statement, bindings)
}

// The team rollup's two edge-row statements depend on nothing but the scope
// read before them, so they are in flight together and the read takes the
// slower of them, not their sum. One after another, on a loaded ClickHouse,
// they took the read past its provider budget.
func TestTeamBlockerEdgeRowStatementsAreInFlightTogether(t *testing.T) {
	client := &togetherClient{markers: teamBlockerStatementMarkers, wait: 2 * time.Second, all: make(chan struct{}), inner: &fakeClient{tables: []fakeTable{
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "00000000-0000-4000-8000-000000000001", "acme/repo"}}},
		{match: "is_open AND start_unix IS NOT NULL", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0), int64(5), int64(5), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}}},
		{match: "SELECT toInt64(uniqExact(if(", rows: [][]any{{int64(0)}}},
		{match: "arrayJoin({team_repo_pairs", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0)}}},
		{match: "FROM work_item_dependencies", rows: [][]any{{"00000000-0000-4000-8000-000000000001", int64(4), int64(5), int64(0), int64(3)}}},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	subject := teamSubject("CHAOS")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("the read failed: %v", err)
	}
	fields := factFor(t, result, subject).Fields
	wantInt(t, fields, "blocked_work_items_current", 4)
	wantInt(t, fields, "blocker_dependencies_current", 5)
	if _, has := fields["oldest_blocked_age_days"]; !has {
		t.Errorf("no age served: reason %+v", fields["oldest_blocked_age_unavailable_reason"])
	}
	// The age is over the totals' 5 open edges (not the 4 blocked items),
	// all 5 with the provider's start: exact.
	if basis := fields["oldest_blocked_age_basis"].String; basis == nil || *basis != "exact" {
		t.Errorf("age basis %+v, want exact", fields["oldest_blocked_age_basis"])
	}
}

// The work item blockers statement needs nothing from the team rollup, so it
// is in flight together with the team rollup's edge-row statements, and its
// facts follow the team facts, as before.
func TestWorkItemBlockersStatementIsInFlightWithTheTeamRollup(t *testing.T) {
	markers := append(append([]string{}, teamBlockerStatementMarkers...), workItemBlockersMarker)
	client := &togetherClient{markers: markers, wait: 2 * time.Second, all: make(chan struct{}), inner: &fakeClient{tables: []fakeTable{
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "00000000-0000-4000-8000-000000000001", "acme/repo"}}},
		{match: "is_open AND start_unix IS NOT NULL", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0), int64(5), int64(5), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}}},
		{match: "arrayJoin({team_repo_pairs", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0)}}},
		{match: "SELECT toInt64(uniqExact(if(", rows: [][]any{{int64(0)}}},
		{match: workItemBlockersMarker, rows: [][]any{{"WIDGET-099", "WIDGET-101", "blocks", "repo-1", "repo-9", uint64(1), uint8(1)}}},
		{match: "FROM work_item_dependencies", rows: [][]any{{"00000000-0000-4000-8000-000000000001", int64(4), int64(5), int64(0), int64(3)}}},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	team, item := teamSubject("CHAOS"), workItemSubject("repo-1", "WIDGET-101")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{team, item},
	})
	if err != nil {
		t.Fatalf("the read failed: %v", err)
	}
	if len(result.Facts) != 2 || result.Facts[0].Subject.CanonicalID != team.CanonicalID || result.Facts[1].Subject.CanonicalID != item.CanonicalID {
		t.Fatalf("facts %+v, want the team fact, then the work item fact", result.Facts)
	}
	wantInt(t, result.Facts[0].Fields, "blocked_work_items_current", 4)
	if blocker := result.Facts[1].Fields["blocked_by_work_item_id"].String; blocker == nil || *blocker != "WIDGET-099" {
		t.Fatalf("work item fact %+v, want blocked by WIDGET-099", result.Facts[1].Fields)
	}
}

// holdingClient answers from inner, except: the statements hold names run
// until their context ends (failing after 5 s if it never does), and the
// statements fail names fail, the first of them once every held statement is
// in flight.
type holdingClient struct {
	inner   *fakeClient
	hold    []string
	fail    string
	mu      sync.Mutex
	waited  bool
	started chan string
}

func (c *holdingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.fail != "" && strings.Contains(statement, c.fail) {
		c.mu.Lock()
		first := !c.waited
		c.waited = true
		c.mu.Unlock()
		for range c.hold {
			if !first {
				break
			}
			select {
			case <-c.started:
			case <-time.After(2 * time.Second):
				return nil, errors.New("the held statements were not in flight")
			}
		}
		return nil, errors.New("statement failed")
	}
	for _, marker := range c.hold {
		if strings.Contains(statement, marker) {
			c.started <- marker
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, errors.New("a held statement was not cancelled")
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inner.Query(ctx, statement, bindings)
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func teamAndItemTables() []fakeTable {
	return []fakeTable{
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "00000000-0000-4000-8000-000000000001", "acme/repo"}}},
		{match: "is_open AND start_unix IS NOT NULL", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0), int64(5), int64(3), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}}},
		{match: "SELECT toInt64(uniqExact(if(", rows: [][]any{{int64(0)}}},
		{match: "arrayJoin({team_repo_pairs", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0)}}},
		{match: workItemBlockersMarker, rows: [][]any{{"WIDGET-099", "WIDGET-101", "blocks", "repo-1", "repo-9", uint64(1), uint8(1)}}},
		{match: "FROM work_item_dependencies", rows: [][]any{{"00000000-0000-4000-8000-000000000001", int64(4), int64(5), int64(0), int64(3)}}},
	}
}

func readTeamAndItem(ctx context.Context, client contextpacket.ClickHouseQueryClient) error {
	provider := devhealthfacts.NewProviders(client)
	for _, p := range provider {
		if p.Capability().Kind == contextfabric.FactBlockers {
			_, err := p.ReadFacts(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers,
				Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS"), workItemSubject("repo-1", "WIDGET-101")},
			})
			return err
		}
	}
	return errors.New("no blockers provider")
}

// A failed team rollup fails the read and cancels the work item read in
// flight. The read logs one failure, the team rollup's: the work item read it
// cancelled is not logged as a failure of its own.
func TestATeamRollupFailureCancelsTheWorkItemReadAndLogsOneFailure(t *testing.T) {
	logs := captureSlog(t)
	client := &holdingClient{inner: &fakeClient{tables: teamAndItemTables()}, hold: []string{workItemBlockersMarker}, fail: teamBlockerTotalsMarker, started: make(chan string, 1)}
	start := time.Now()
	err := readTeamAndItem(context.Background(), client)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("the read took %s: the work item read was not cancelled", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "query team blockers failed") {
		t.Fatalf("err = %v, want the team blockers read failed", err)
	}
	if got := strings.Count(logs.String(), "devhealthfacts.read_failed"); got != 1 || strings.Contains(logs.String(), `action="query work item blockers"`) {
		t.Fatalf("%d read_failed lines, want one, the team rollup's:\n%s", got, logs.String())
	}
}

// When the caller's deadline ends the read, the failure is logged: a
// cancellation the read did not cause itself is a failure of the read.
func TestADeadlineThatEndsTheBlockersReadIsLogged(t *testing.T) {
	logs := captureSlog(t)
	client := &holdingClient{inner: &fakeClient{tables: teamAndItemTables()}, hold: []string{workItemBlockersMarker, teamBlockerStatementMarkers[0]}, started: make(chan string, 2)}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := readTeamAndItem(ctx, client)
	if err == nil {
		t.Fatal("the read succeeded past its deadline")
	}
	if !strings.Contains(logs.String(), "devhealthfacts.read_failed") {
		t.Fatalf("the read's failure was not logged:\n%s", logs.String())
	}
}

// A failed work item read fails the read and cancels the team rollup in
// flight; the read logs one failure, the work item read's.
func TestAWorkItemReadFailureCancelsTheTeamRollupAndLogsOneFailure(t *testing.T) {
	logs := captureSlog(t)
	client := &holdingClient{inner: &fakeClient{tables: teamAndItemTables()}, hold: []string{teamBlockerStatementMarkers[0]}, fail: workItemBlockersMarker, started: make(chan string, 1)}
	start := time.Now()
	err := readTeamAndItem(context.Background(), client)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("the read took %s: the team rollup was not cancelled", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "query work item blockers failed") {
		t.Fatalf("err = %v, want the work item blockers read failed", err)
	}
	if got := strings.Count(logs.String(), "devhealthfacts.read_failed"); got != 1 || strings.Contains(logs.String(), `action="query team blockers"`) {
		t.Fatalf("%d read_failed lines, want one, the work item read's:\n%s", got, logs.String())
	}
}

// A failed rollup or totals read fails the read, as it did one after another,
// and cancels the statement still in flight (a statement not cancelled fails
// after 5 s, and the read takes that long). A totals-with-age statement
// cancelled by the rollup's failure is the read's failure, not an age
// failure: no age warning is logged for it. A failed totals-with-age statement
// is an age failure, logged once, and its totals-only fallback failing fails
// the read.
func TestATeamBlockerStatementFailureCancelsItsSiblingAndFailsTheRead(t *testing.T) {
	for _, cell := range []struct {
		name, fail, hold string
		ageWarnings      int
	}{
		{"rollup", teamBlockerStatementMarkers[0], teamBlockerStatementMarkers[1], 0},
		{"totals", teamBlockerTotalsMarker, teamBlockerStatementMarkers[0], 1},
	} {
		t.Run(cell.name, func(t *testing.T) {
			logs := captureSlog(t)
			client := &holdingClient{inner: &fakeClient{tables: teamAndItemTables()}, hold: []string{cell.hold}, fail: cell.fail, started: make(chan string, 1)}
			provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
			start := time.Now()
			_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
			})
			if elapsed := time.Since(start); elapsed > 4*time.Second {
				t.Fatalf("the read took %s: the statement in flight was not cancelled", elapsed)
			}
			if err == nil || !strings.Contains(err.Error(), "query team blockers failed") {
				t.Fatalf("err = %v, want the team blockers read failed", err)
			}
			if got := strings.Count(logs.String(), "devhealthfacts.blocker_age_read_failed"); got != cell.ageWarnings {
				t.Fatalf("%d age warnings, want %d:\n%s", got, cell.ageWarnings, logs.String())
			}
			if got := strings.Count(logs.String(), "devhealthfacts.read_failed"); got != 1 {
				t.Fatalf("%d read_failed lines, want one:\n%s", got, logs.String())
			}
		})
	}
}

// The team totals and the team age read the same edge rows, so one statement
// reads them: a team read evaluates the totals' edge rows once, not twice.
func TestOneStatementReadsTheTeamTotalsAndTheAge(t *testing.T) {
	client := &fakeClient{tables: teamAndItemTables()}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	subject := teamSubject("CHAOS")
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("the read failed: %v", err)
	}
	var totals []string
	for _, query := range client.queries {
		if strings.Contains(query.statement, "arrayJoin({team_repo_pairs") {
			totals = append(totals, query.statement)
		}
	}
	if len(totals) != 1 || !strings.Contains(totals[0], teamBlockerStatementMarkers[1]) || !strings.Contains(totals[0], teamBlockerTotalsMarker) {
		t.Fatalf("%d team totals statements, want one that reads the totals and the age", len(totals))
	}
	fields := factFor(t, result, subject).Fields
	wantInt(t, fields, "blocked_work_items_current", 4)
	wantInt(t, fields, "blocker_dependencies_current", 5)
	// 5 open edges, all 5 with a start, 3 of them exact: a lower bound.
	if basis := fields["oldest_blocked_age_basis"].String; basis == nil || *basis != "lower_bound" {
		t.Fatalf("age basis %+v, want lower_bound: 2 of the 5 open edges have no exact start", fields["oldest_blocked_age_basis"])
	}
	if _, has := fields["oldest_blocked_age_days"]; !has {
		t.Fatalf("no age served: reason %+v", fields["oldest_blocked_age_unavailable_reason"])
	}
}
