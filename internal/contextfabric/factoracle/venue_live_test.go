//go:build o4venue

package factoracle

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// venueConfig reads the venue from the environment. A missing value fails
// the run: a live run that did not happen is never a pass.
func venueConfig(t *testing.T) VenueConfig {
	t.Helper()
	need := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s is not set", name)
		}
		return value
	}
	end := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	if raw := os.Getenv("ACR_O4_WINDOW_END"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			t.Fatalf("ACR_O4_WINDOW_END is not a date")
		}
		end = parsed.UTC()
	}
	return VenueConfig{
		MCPURL: need("ACR_O4_MCP_URL"), TokenFile: need("ACR_O4_TOKEN_FILE"),
		ClickHouseDSNFile: need("ACR_O4_CLICKHOUSE_DSN_FILE"), ClickHouseAddr: os.Getenv("ACR_O4_CLICKHOUSE_ADDR"),
		OrgID:    need("ACR_O4_ORG_ID"),
		Window:   Window{Start: end.Add(-MaxWindowDays * 24 * time.Hour), End: end},
		OpsBuild: os.Getenv("ACR_O4_OPS_BUILD"), AcrBuild: os.Getenv("ACR_O4_ACR_BUILD"),
	}
}

func logRun(t *testing.T, run *LiveRun) {
	t.Helper()
	t.Logf("window %s to %s\n%s\n%s", run.Oracle.Window.startDate(), run.Oracle.Window.endDate(), run.Report.Table(), run.Report.Details())
	if path := os.Getenv("ACR_O4_REPORT_FILE"); path != "" {
		encoded, err := json.MarshalIndent(run.Report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestVenueLive is the live mode: both planes on the venue. Findings are
// reported, not failed: they are differences to file, and the run measured
// them.
func TestVenueLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	run, err := RunLive(ctx, venueConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	logRun(t, run)
}

// TestVenueCapture records the extract and the replies into testdata/venue.
func TestVenueCapture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	run, err := Capture(ctx, venueConfig(t), "testdata/venue")
	if err != nil {
		t.Fatal(err)
	}
	logRun(t, run)
}
