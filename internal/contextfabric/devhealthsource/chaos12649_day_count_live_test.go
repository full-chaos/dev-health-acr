package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// dayCountFalkorImage is the digest-pinned FalkorDB the other live tests use.
const dayCountFalkorImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

// dayCountFalkorAddrEnv names a FalkorDB to use instead of starting one (a
// Mac-local proof beside a container started by name).
const dayCountFalkorAddrEnv = "ACR_TEST_FALKORDB_ADDR"

func dayCountFalkorAddr(t *testing.T, ctx context.Context) string {
	t.Helper()
	if addr := strings.TrimSpace(os.Getenv(dayCountFalkorAddrEnv)); addr != "" {
		return addr
	}
	chfixture.RequireContainers(t)
	container := falkorfixture.Start(ctx, t, dayCountFalkorImage)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	return host + ":" + port.Port()
}

type dayCountCheckpoints struct {
	mu   sync.Mutex
	data map[string]contextfabric.ProjectionCheckpoint
}

func (s *dayCountCheckpoints) LoadProjectionCheckpoint(_ context.Context, org, source string) (contextfabric.ProjectionCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if checkpoint, ok := s.data[org+"\x00"+source]; ok {
		return checkpoint, nil
	}
	return contextfabric.ProjectionCheckpoint{OrgID: org, Source: source}, nil
}

func (s *dayCountCheckpoints) CompareAndSwapProjectionCheckpoint(_ context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := expected.OrgID + "\x00" + expected.Source
	if s.data[key].Cursor != expected.Cursor {
		return contextfabric.ErrProjectionConflict
	}
	s.data[key] = updated
	return nil
}

type dayCountRebuildMarker struct{}

func (dayCountRebuildMarker) BeginRebuild(context.Context, string) error { return nil }
func (dayCountRebuildMarker) IsRebuildInProgress(context.Context, string) (bool, error) {
	return false, nil
}
func (dayCountRebuildMarker) CompleteRebuild(context.Context, string) error { return nil }

// finishedChecks reads the count check's Info lines from the log.
func finishedChecks(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var checks []map[string]any
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "projection graph count check finished") {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &fields))
		checks = append(checks, fields)
	}
	return checks
}

// CHAOS-12649 end to end, on a real ClickHouse and a real FalkorDB: the real
// producer walks 200 CI runs synced on one day and 3 synced on the next, the real
// adapter projects them, and the real count check finds both stores in step per
// day. Then the 3 nodes of the second day become stubs (no owned marker, no
// stamp), the state the askdev-1 hole left. The kind's total (203 against 200) is
// inside its tolerance, so the total check stays silent, as it did on askdev-1;
// the day check warns, naming the day, the kind and both counts. The CI runs'
// own time (started_at) is days earlier than their sync, so the check reads the
// sync stamp, not the domain time.
func TestLiveALostDayOfSyncStampsIsSeenPerDayWhileTheTotalPasses(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		require.NoError(t, direct.Exec(ctx, statement), "create table")
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	org := fmt.Sprintf("org-12649-%d", time.Now().UnixNano())
	repoID := "12649000-0000-0000-0000-000000000001"
	dayOne := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	dayTwo := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	started := dayOne.AddDate(0, 0, -3)
	require.NoError(t, direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/days', 'github', ?)`, repoID, org, dayOne))
	for i := 0; i < 203; i++ {
		synced := dayOne
		if i >= 200 {
			synced = dayTwo
		}
		require.NoError(t, direct.Exec(ctx, `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES (toUUID(?), ?, 'success', ?, ?, ?)`,
			repoID, fmt.Sprintf("R-%03d", i), started, synced, org))
	}

	addr := dayCountFalkorAddr(t, ctx)
	prefix := fmt.Sprintf("acr-cf-12649-%d", time.Now().UnixNano())
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: addr, GraphPrefix: prefix, RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 4, AllowInsecure: true, TLS: false,
	})
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = adapter.PurgeOrganization(context.Background(), org)
		_ = client.Close()
	})

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	require.NoError(t, err)
	logs := &bytes.Buffer{}
	var logMu sync.Mutex
	clock := dayTwo.Add(24 * time.Hour)
	logger := slog.New(slog.NewJSONHandler(dayCountLogWriter{mu: &logMu, w: logs}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{org},
		Sources: []projectionrun.SourcePair{{Name: devhealthsource.SourceName, Source: source}},
		Backend: adapter, Checkpoints: &dayCountCheckpoints{data: map[string]contextfabric.ProjectionCheckpoint{}}, RebuildMarkers: dayCountRebuildMarker{},
		Logger:                  logger,
		Observer:                projectionrun.SlogObserver{Logger: logger},
		GraphCountCheckInterval: time.Minute,
		Now:                     func() time.Time { return clock },
	})
	require.NoError(t, err)
	tick := func() {
		clock = clock.Add(2 * time.Minute)
		coordinator.Tick(ctx)
	}
	read := func() string {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.String()
	}

	// Tick until the check has run twice: it runs only after a tick that drained
	// every pair, and the walk's window can take more than one tick to close.
	ticksUntilChecks := func(n int) {
		t.Helper()
		target := len(finishedChecks(t, read())) + n
		for i := 0; i < 60 && len(finishedChecks(t, read())) < target; i++ {
			tick()
		}
	}
	ticksUntilChecks(2)
	inStep := finishedChecks(t, read())
	require.GreaterOrEqual(t, len(inStep), 2, "the count check did not run twice:\n%s", read())
	last := inStep[len(inStep)-1]
	require.Equal(t, "completed", last["outcome"], "a walk from zero did not leave the check able to compare:\n%s", read())
	require.Greater(t, last["days_compared"], float64(0), "no day was compared")
	require.NotContains(t, read(), "graph_below_source", "stores in step reported a gap")

	// Plant the hole: the second day's CI run nodes become stubs.
	graphs, err := client.Do(ctx, "GRAPH.LIST").StringSlice()
	require.NoError(t, err)
	key := ""
	for _, name := range graphs {
		if strings.HasPrefix(name, prefix) {
			key = name
		}
	}
	require.NotEmpty(t, key, "the organization's graph key")
	planted, err := client.Do(ctx, "GRAPH.QUERY", key, fmt.Sprintf(
		"MATCH (n:Subject {org_id:'%s', subject_kind:'ci_pipeline_run'}) WHERE n.property_%s >= %d SET n.projection_owned = NULL, n.property_%s = NULL RETURN count(n)",
		org, contextfabric.ProjectionSyncedAtProperty, dayTwo.Truncate(24*time.Hour).UnixNano(), contextfabric.ProjectionSyncedAtProperty)).Slice()
	require.NoError(t, err)
	require.Contains(t, fmt.Sprint(planted), "3", "precondition: the second day's 3 nodes were made stubs: %v", planted)

	before := len(finishedChecks(t, read()))
	ticksUntilChecks(2)
	logged := read()
	require.GreaterOrEqual(t, len(finishedChecks(t, logged)), before+2, "the count check did not run twice after the plant")
	require.NotContains(t, logged, `"check":"graph_below_source",`, "the kind's total (203 against 200) is inside its tolerance; it must stay silent, as on askdev-1")
	var dayLine string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, `"check":"graph_below_source_on_day"`) {
			dayLine = line
		}
	}
	require.NotEmpty(t, dayLine, "the lost day was not reported:\n%s", logged)
	for _, want := range []string{`"day":"2026-10-27"`, `"kind":"ci_pipeline_run"`, `"source_count":3`, `"graph_count":0`} {
		require.Contains(t, dayLine, want)
	}
	t.Logf("in step: %v", last)
	t.Logf("after the plant: %s", dayLine)
}

type dayCountLogWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l dayCountLogWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
