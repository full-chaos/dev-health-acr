package hosted

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/chtlsrig"
)

const warmPoolStatement = "SELECT toString(1)"

// clickhouse-go closes a statement's row stream a moment before it returns the
// connection to the idle queue, so a statement started in that gap dials a
// second connection (seen in CI as 2 connections for 50 sequential statements,
// 5 for 4 workers). The pause stands in for the work acr does between
// statements and sits far above the gap; warmPoolReleaseRaceSlack allows the
// residue. A pool that reconnects per statement opens one per statement, far
// above both.
const (
	warmPoolPause            = 5 * time.Millisecond
	warmPoolReleaseRaceSlack = 1
	warmPoolSettle           = 150 * time.Millisecond
)

func openThroughRig(t *testing.T, rig *chtlsrig.Rig) clickHouseComponents {
	t.Helper()
	components, err := openClickHouse(context.Background(), clickHouseOpenRequest{config: config.Config{
		ClickHouseDSN: rig.DSN, ClickHouseCACertPath: rig.CAPath, ClickHouseMaxBytesToRead: 256 << 20,
		EvidenceIDActiveKID: "current", EvidenceIDKeys: map[string][]byte{"current": []byte(strings.Repeat("k", 32))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = components.close() })
	return components
}

func timedStatement(t *testing.T, components clickHouseComponents) time.Duration {
	t.Helper()
	started := time.Now()
	if err := runWarmPoolStatement(components); err != nil {
		t.Error(err)
	}
	return time.Since(started)
}

func runWarmPoolStatement(components clickHouseComponents) error {
	rows, err := components.queryClient.Query(context.Background(), warmPoolStatement, nil)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var value string
	if !rows.Next() {
		return fmt.Errorf("statement returned no row: %v", rows.Err())
	}
	if err := rows.Scan(&value); err != nil {
		return err
	}
	if value != "1" {
		return fmt.Errorf("statement returned %q, want 1", value)
	}
	return nil
}

// Through the production open path, with a TLS server behind a latency proxy,
// sequential statements ride one warm connection: one TCP connection and one
// TLS handshake in total. A pool that reconnects per statement opens N.
func TestOpenClickHouseKeepsOneWarmTLSConnectionAcrossSequentialStatements(t *testing.T) {
	const statements = 50
	rig := chtlsrig.Start(t, context.Background(), 0)
	for _, rtt := range []time.Duration{0, 20 * time.Millisecond, 60 * time.Millisecond} {
		t.Run(rtt.String(), func(t *testing.T) {
			rig.Proxy.SetRTT(rtt)
			rig.Proxy.ResetStats()
			components := openThroughRig(t, rig)
			latencies := make([]time.Duration, 0, statements)
			for range statements {
				latencies = append(latencies, timedStatement(t, components))
				time.Sleep(warmPoolPause)
			}
			stats := rig.Proxy.Stats()
			p50, p95 := chtlsrig.Percentile(latencies, 0.5), chtlsrig.Percentile(latencies, 0.95)
			t.Logf("ch_rig rtt_ms=%d statements=%d tcp_connections=%d tls_handshakes=%d p50_ms=%.1f p95_ms=%.1f",
				rtt.Milliseconds(), statements, stats.Accepted, stats.ClientHellos, ms(p50), ms(p95))
			if limit := int64(1 + warmPoolReleaseRaceSlack); stats.Accepted > limit || stats.ClientHellos > limit {
				t.Fatalf("%d sequential statements opened %d TCP connections and %d TLS handshakes, want at most %d (1 warm connection plus the release-race slack)", statements, stats.Accepted, stats.ClientHellos, limit)
			}
			if limit := 4*rtt + 100*time.Millisecond; p95 > limit {
				t.Fatalf("p95 per statement %s over a warm connection at rtt %s, want under %s", p95, rtt, limit)
			}
		})
	}
}

// Four statements at once use at most four connections however many statements
// run; the pool never opens one per statement.
func TestOpenClickHouseConcurrentStatementsShareTheWarmPool(t *testing.T) {
	const workers, perWorker = acrMaxConcurrentStatements, 25
	rig := chtlsrig.Start(t, context.Background(), 60*time.Millisecond)
	components := openThroughRig(t, rig)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for range perWorker {
				timedStatement(t, components)
				time.Sleep(warmPoolPause)
			}
		}()
	}
	group.Wait()
	stats := rig.Proxy.Stats()
	t.Logf("ch_rig rtt_ms=60 workers=%d statements=%d tcp_connections=%d tls_handshakes=%d", workers, workers*perWorker, stats.Accepted, stats.ClientHellos)
	if limit := int64(workers + warmPoolReleaseRaceSlack); stats.Accepted > limit || stats.ClientHellos > limit {
		t.Fatalf("%d statements on %d workers opened %d TCP connections and %d TLS handshakes, want at most %d", workers*perWorker, workers, stats.Accepted, stats.ClientHellos, limit)
	}
}

// A burst as wide as the open cap fills the pool; the next burst of the same
// width finds every connection warm. An idle cap below the open cap closes the
// surplus after the first burst and pays new handshakes in the second.
func TestOpenClickHouseSecondBurstReusesEveryWarmConnection(t *testing.T) {
	const width = ClickHousePoolMaxOpenConns
	rig := chtlsrig.Start(t, context.Background(), 60*time.Millisecond)
	components := openThroughRig(t, rig)
	burst := func() {
		start := make(chan struct{})
		var group sync.WaitGroup
		for range width {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				timedStatement(t, components)
			}()
		}
		close(start)
		group.Wait()
	}
	burst()
	time.Sleep(warmPoolSettle)
	first := rig.Proxy.Stats()
	if first.Accepted != width {
		t.Fatalf("first burst of %d concurrent statements opened %d connections, want %d (the burst did not overlap)", width, first.Accepted, width)
	}
	burst()
	second := rig.Proxy.Stats()
	t.Logf("ch_rig rtt_ms=60 burst_width=%d tcp_connections_after_first=%d after_second=%d tls_handshakes=%d", width, first.Accepted, second.Accepted, second.ClientHellos)
	if second.Accepted != first.Accepted || second.ClientHellos != first.ClientHellos {
		t.Fatalf("second burst opened %d new TCP connections and %d new TLS handshakes, want 0", second.Accepted-first.Accepted, second.ClientHellos-first.ClientHellos)
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
