package main

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

// TestOverlapSettingReachesBothClickHouseBackedSources pins the composition
// root's half of ACR_CONTEXT_FABRIC_PROJECTOR_OVERLAP (CHAOS-7263): the parsed
// config.ProjectorConfig.Overlap must reach BOTH sources the projector builds
// from ClickHouse. config.Load's half (unset, valid, invalid values) is pinned
// in internal/config. The values are not the 15m default, so a source that
// kept its default fails here.
func TestOverlapSettingReachesBothClickHouseBackedSources(t *testing.T) {
	t.Parallel()
	for _, overlap := range []time.Duration{time.Minute, 42 * time.Minute} {
		cfg := config.ProjectorConfig{Overlap: overlap, TeamsProjectsEnabled: true}
		clickhouse, teamsProjects, err := clickhouseBackedSources(unreachableClient{t: t}, cfg, discardLogger())
		if err != nil {
			t.Fatalf("overlap %s: %v", overlap, err)
		}
		if got := clickhouse.Overlap(); got != overlap {
			t.Errorf("the ClickHouse source walks a %s window, want the configured %s", got, overlap)
		}
		if got := teamsProjects.Overlap(); got != overlap {
			t.Errorf("the teams/projects source walks a %s window, want the configured %s", got, overlap)
		}
	}
	// A non-positive window would silently turn the late-arrival re-read off:
	// the projector must refuse to build its sources.
	for _, overlap := range []time.Duration{0, -time.Minute} {
		if _, _, err := clickhouseBackedSources(unreachableClient{t: t}, config.ProjectorConfig{Overlap: overlap}, discardLogger()); err == nil {
			t.Errorf("overlap %s: the sources were built; want an error", overlap)
		}
	}
}

// TestReadByteLimitReachesTheClickHouseSource pins that the configured
// ACR_CLICKHOUSE_MAX_BYTES_TO_READ reaches the ClickHouse source, which sizes
// its key-named pull request reads from it (CHAOS-8683).
func TestReadByteLimitReachesTheClickHouseSource(t *testing.T) {
	t.Parallel()
	const limit = 96 << 20
	clickhouse, _, err := clickhouseBackedSources(unreachableClient{t: t}, config.ProjectorConfig{Overlap: time.Minute, ClickHouseMaxBytesToRead: limit}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if got := clickhouse.ReadByteLimit(); got != limit {
		t.Fatalf("the ClickHouse source sizes its reads from %d bytes, want the configured %d", got, limit)
	}
}

// TestWindowPassIntervalReachesBothClickHouseBackedSources pins the composition
// root's half of ACR_CONTEXT_FABRIC_PROJECTOR_WINDOW_PASS_INTERVAL. Zero is a
// valid setting (the rollback: the next pass starts at once), so it must reach
// the sources as zero and not fall back to their default.
func TestWindowPassIntervalReachesBothClickHouseBackedSources(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{0, 90 * time.Second} {
		cfg := config.ProjectorConfig{Overlap: time.Minute, WindowPassInterval: interval, TeamsProjectsEnabled: true}
		clickhouse, teamsProjects, err := clickhouseBackedSources(unreachableClient{t: t}, cfg, discardLogger())
		if err != nil {
			t.Fatalf("interval %s: %v", interval, err)
		}
		if got := clickhouse.WindowPassInterval(); got != interval {
			t.Errorf("the ClickHouse source pauses %s between window passes, want the configured %s", got, interval)
		}
		if got := teamsProjects.WindowPassInterval(); got != interval {
			t.Errorf("the teams/projects source pauses %s between window passes, want the configured %s", got, interval)
		}
	}
	if _, _, err := clickhouseBackedSources(unreachableClient{t: t}, config.ProjectorConfig{Overlap: time.Minute, WindowPassInterval: -time.Second}, discardLogger()); err == nil {
		t.Error("a negative interval built the sources; want an error")
	}
}
