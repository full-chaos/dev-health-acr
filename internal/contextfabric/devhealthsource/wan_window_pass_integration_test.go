package devhealthsource_test

// One trailing-window pass of the teams source over a TLS ClickHouse behind a
// latency proxy: the statements and the wall time of a pass and of a tick,
// with the window carry off and on. The shapes follow production: one large
// membership table and a few rows in the others, so a pass walks about a dozen
// pages and only one table stays truncated.

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chtlsrig"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

type wanCountingClient struct {
	inner      contextpacket.ClickHouseQueryClient
	statements atomic.Int64
}

func (c *wanCountingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements.Add(1)
	return c.inner.Query(ctx, statement, bindings)
}

type wanSample struct {
	statements int64
	wall       time.Duration
}

type wanRun struct {
	passes         []wanSample // window pass, net of the paged read every tick also does
	ticks          []time.Duration
	tickStatements []int64 // statements of every tick, passes and paged reads alike
}

const (
	wanRTT        = 60 * time.Millisecond
	wanTick       = 15 * time.Second
	wanPassPause  = time.Minute
	wanPasses     = 5
	wanLargeRows  = 2600
	wanOrgID      = "10715000-0000-4000-8000-0000000000b1"
	wanProjectID  = "10715000-0000-4000-8000-0000000000b2"
	wanRepoZeroID = "00000000-0000-0000-0000-000000000000"
)

func TestWindowPassOverA60msRoundTrip(t *testing.T) {
	ctx := context.Background()
	rig := chtlsrig.Start(t, ctx, 0)
	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(rig.CAPath)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(rig.DSN)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := dsn.User.Password()
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{rig.Proxy.Addr()}, Auth: clickhousedriver.Auth{Database: "default", Username: "default", Password: password},
		TLS: &tls.Config{RootCAs: tlsConfig.RootCAs, ServerName: chtlsrig.ServerName, MinVersion: tls.VersionTLS12}, DialTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: rig.DSN, TLS: tlsConfig})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	first := now.Add(-14 * time.Minute)
	mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', 'K1', 'project', 1, 'started', '', ?, ?)`, wanProjectID, wanOrgID, first, first)
	for _, team := range []string{"T-1", "T-2", "T-3"} {
		mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			team, "team "+team, "", first, first, wanOrgID, "linear", team, []string{}, uint8(1))
	}
	mustExec(t, ctx, direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, ?, 'work_item', concat('linear:WAN-', leftPad(toString(number), 5, '0')), 'linear', '', ?, '', 'K1', '', fromUnixTimestamp64Milli(toInt64(?)) + toIntervalMillisecond(number * 300), fromUnixTimestamp64Milli(toInt64(?)) + toIntervalMillisecond(number * 300), concat('linear:wan-', toString(number)), fromUnixTimestamp64Milli(toInt64(?)) + toIntervalMillisecond(number * 300) FROM numbers(?)`,
		wanOrgID, wanRepoZeroID, wanProjectID, first.UnixMilli(), first.UnixMilli(), first.UnixMilli(), uint64(wanLargeRows))

	run := func(carry bool) wanRun {
		counter := &wanCountingClient{inner: client}
		src, err := devhealthsource.NewTeamsProjectsSource(counter, true)
		if err != nil {
			t.Fatal(err)
		}
		src.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
		if _, err := src.WithWindowPassInterval(wanPassPause); err != nil {
			t.Fatal(err)
		}
		src.WithWindowCarry(carry)
		clock := now
		src.SetClockForTest(func() time.Time { return clock })
		checkpoint := contextfabric.ProjectionCheckpoint{OrgID: wanOrgID, Source: devhealthsource.TeamsProjectsSourceName}
		// Drain the rows through the paged read at no latency; the rows it
		// emits count as seen, so the passes below publish nothing.
		rig.Proxy.SetRTT(0)
		for i := 0; i < 80; i++ {
			batch, ok, err := src.NextProjectionBatch(ctx, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			checkpoint.Cursor = batch.NextCursor
		}
		// A statement costs client and server work beside the network, so the
		// proof that the proxy delay applied is the same trivial statement
		// timed with and without it: the fastest of five warm ones each.
		probe := func() time.Duration {
			fastest := time.Duration(1<<63 - 1)
			for i := 0; i < 6; i++ {
				started := time.Now()
				rows, err := client.Query(ctx, "SELECT 1", nil)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				_ = rows.Close()
				if i > 0 {
					fastest = min(fastest, time.Since(started))
				}
			}
			return fastest
		}
		withoutDelay := probe()
		rig.Proxy.SetRTT(wanRTT)
		withDelay := probe()
		t.Logf("carry=%v: SELECT 1 fastest of five: t0=%s t60=%s gap=%s", carry, withoutDelay, withDelay, withDelay-withoutDelay)
		if withDelay-withoutDelay < wanRTT/2 {
			t.Errorf("carry=%v: a one-statement round trip took %s over the proxy against %s without its delay, want at least %s more: the latency did not apply", carry, withDelay, withoutDelay, wanRTT/2)
		}
		tick := func() (int64, time.Duration) {
			before := counter.statements.Load()
			started := time.Now()
			batch, ok, err := src.NextProjectionBatch(ctx, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				checkpoint.Cursor = batch.NextCursor
			}
			return counter.statements.Load() - before, time.Since(started)
		}
		// A tick with no pass open reads every table once: the paged read the
		// pause between passes shows. A pass is counted net of it.
		type rawPass struct {
			statements int64
			wall       time.Duration
			ticks      int64
		}
		var raw []rawPass
		var pagedStatements int64
		var pagedWall time.Duration
		var out wanRun
		var current *rawPass
		for len(raw) < wanPasses {
			clock = clock.Add(wanTick)
			statements, wall := tick()
			out.ticks = append(out.ticks, wall)
			out.tickStatements = append(out.tickStatements, statements)
			open := src.ProjectionWindowPass(checkpoint).Open
			if open || current != nil {
				if current == nil {
					current = &rawPass{}
				}
				current.statements += statements
				current.wall += wall
				current.ticks++
				if !open {
					raw = append(raw, *current)
					current = nil
				}
			} else if pagedStatements == 0 {
				pagedStatements, pagedWall = statements, wall
			}
			if len(out.ticks) > 400 {
				t.Fatalf("carry=%v: %d passes after %d ticks", carry, len(raw), len(out.ticks))
			}
		}
		if pagedStatements == 0 {
			t.Fatalf("carry=%v: no tick without an open pass to take the paged read from", carry)
		}
		for _, p := range raw {
			out.passes = append(out.passes, wanSample{statements: p.statements - p.ticks*pagedStatements, wall: p.wall - time.Duration(p.ticks)*pagedWall})
		}
		return out
	}
	off, on := run(false), run(true)
	summary := func(label string, r wanRun) (statements, widestTick int64, passP50 time.Duration) {
		var walls []time.Duration
		for _, p := range r.passes {
			statements += p.statements
			walls = append(walls, p.wall)
		}
		for _, n := range r.tickStatements {
			widestTick = max(widestTick, n)
		}
		t.Logf("WAN60 pass carry=%s stmts_per_pass=%d widest_tick_stmts=%d p50=%s p95=%s | tick p50=%s p95=%s", label, statements/int64(len(r.passes)), widestTick, chtlsrig.Percentile(walls, 0.5), chtlsrig.Percentile(walls, 0.95), chtlsrig.Percentile(r.ticks, 0.5), chtlsrig.Percentile(r.ticks, 0.95))
		return statements / int64(len(r.passes)), widestTick, chtlsrig.Percentile(walls, 0.5)
	}
	offStatements, offWidest, _ := summary("off", off)
	onStatements, onWidest, _ := summary("on", on)
	stats := rig.Proxy.Stats()
	t.Logf("WAN60 connections accepted=%d tls_handshakes=%d", stats.Accepted, stats.ClientHellos)
	if offStatements <= 0 || float64(onStatements) > 0.40*float64(offStatements) {
		t.Errorf("a pass ran %d statements with the carry and %d without, want at most 0.40 of the latter", onStatements, offStatements)
	}
	if onWidest >= offWidest {
		t.Errorf("the widest tick ran %d statements with the carry and %d without, want fewer", onWidest, offWidest)
	}
	if stats.ClientHellos > 4 {
		t.Errorf("%d TLS handshakes over the run, want a warm pool of at most 4", stats.ClientHellos)
	}
}
