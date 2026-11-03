//go:build o4venue

package factoracle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ownerShareReportPath is the sibling of the oracle report file: the
// owner-share verdict has its own form (OwnerShareLiveReport), so it never
// overwrites the report of TestVenueLive. report.json gives
// report.owner_share.json.
func ownerShareReportPath(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".owner_share" + ext
}

// TestVenueOwnerShareLive is the owner-share pair (CHAOS-12691) on the venue
// (`make o4-owner-share-live`): ops ORG, TEAM and REPO investment by theme
// through the ops public GraphQL API, the owners read as of the start of the
// calls from the operator's ownership extract file or from the venue
// ClickHouse. Configuration, by environment:
//
//   - ACR_O4_OPS_API_URL: the ops API base (http://api.localhost:3000);
//   - ACR_O4_OPS_EMAIL and ACR_O4_OPS_PASSWORD_FILE: the login; the file is
//     0600 and is read in process only;
//   - ACR_O4_OPS_DIAL_ADDR (optional): the host:port to dial in place of the
//     host of the API URL, which the request keeps (127.0.0.1:3000);
//   - ACR_O4_ORG_ID and ACR_O4_WINDOW_END as for TestVenueLive;
//   - ACR_O4_OWNERSHIP_EXTRACT: the operator's output of
//     testdata/owner_share_venue_extract.sql (OwnershipExtractSQLFile), a
//     0600 file the running user owns; the venue ClickHouse is then not
//     opened. ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE (optional, a Go duration,
//     default 15m) bounds the age of its snapshot at the last ops call;
//   - else ACR_O4_CLICKHOUSE_DSN_FILE and ACR_O4_CLICKHOUSE_ADDR as for
//     TestVenueLive; naming both sources fails the run;
//   - ACR_O4_REPORT_FILE (optional): the verdict goes to its .owner_share
//     sibling (ownerShareReportPath).
//
// Unlike TestVenueLive, a finding fails the run: the pair is the proof the
// run_operation policy needs before it may serve the TEAM and REPO scopes.
func TestVenueOwnerShareLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	orgID := venueEnv(t, "ACR_O4_ORG_ID")
	plane := &OpsGraphQLPlane{
		BaseURL: venueEnv(t, "ACR_O4_OPS_API_URL"), Email: venueEnv(t, "ACR_O4_OPS_EMAIL"),
		PasswordFile: venueEnv(t, "ACR_O4_OPS_PASSWORD_FILE"), OrgID: orgID,
		DialAddr: os.Getenv("ACR_O4_OPS_DIAL_ADDR"), MinInterval: venuePaceInterval, Logf: t.Logf,
	}
	cfg := OwnerShareLiveConfig{
		Ops: plane, OwnershipExtract: os.Getenv("ACR_O4_OWNERSHIP_EXTRACT"),
		ClickHouseDSNFile: os.Getenv("ACR_O4_CLICKHOUSE_DSN_FILE"), ClickHouseAddr: os.Getenv("ACR_O4_CLICKHOUSE_ADDR"),
		OrgID: orgID, Window: venueWindow(t),
	}
	if cfg.OwnershipExtract == "" {
		cfg.ClickHouseDSNFile = venueEnv(t, "ACR_O4_CLICKHOUSE_DSN_FILE")
	}
	if raw := os.Getenv("ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE"); raw != "" {
		maxAge, err := time.ParseDuration(raw)
		if err != nil || maxAge <= 0 {
			t.Fatal("ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE is not a positive Go duration (15m)")
		}
		cfg.OwnershipExtractMaxAge = maxAge
	}
	report, err := RunOwnerShareLive(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s\n%s", report.Table(), report.Details())
	if path := os.Getenv("ACR_O4_REPORT_FILE"); path != "" {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ownerShareReportPath(path), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if findings := report.Findings(); len(findings) > 0 {
		t.Fatalf("the owner-share pair has %d findings (listed above)", len(findings))
	}
}
