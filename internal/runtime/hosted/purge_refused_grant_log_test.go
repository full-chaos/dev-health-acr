package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A purge tick whose DELETE is refused is logged by error class and table, and
// carries nothing else from the error: no SQL, no DSN.
func TestPurgeTickLogsARefusedDeleteByClassAndTable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refused := fmt.Errorf("purge expired workload credentials: %w: %w", storage.ErrUnavailable,
		&storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "client_credentials"})
	leaky := fmt.Errorf("dial postgres://user:s3cr3t@db/acr: %w", &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "x; DROP TABLE y"})
	for name, tc := range map[string]struct {
		err       error
		wantTable string
	}{"refused delete": {refused, "client_credentials"}, "table with SQL": {leaky, ""}} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tick := make(chan time.Time, 1)
			tick <- time.Now()
			close(tick)
			purge := func(context.Context, time.Time, int) (int, error) { return 0, tc.err }
			runPurgeTickLoop(ctx, tick, time.Now, purge, 1, workloadCredentialPurgeFailureMessage, packetPurgeSlogObserver(slog.New(slog.NewJSONHandler(&buf, nil))))
			var record map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &record); err != nil {
				t.Fatalf("not one JSON record: %q: %v", buf.String(), err)
			}
			if record["db_error_class"] != "insufficient_privilege" || record["db_sqlstate"] != "42501" {
				t.Fatalf("class missing: %s", buf.String())
			}
			if got, _ := record["db_table"].(string); got != tc.wantTable {
				t.Fatalf("db_table = %q, want %q: %s", got, tc.wantTable, buf.String())
			}
			for _, leak := range []string{"s3cr3t", "DROP", "postgres://"} {
				if strings.Contains(buf.String(), leak) {
					t.Fatalf("the log carries %q: %s", leak, buf.String())
				}
			}
		})
	}
	if got := (&storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "audit_events"}).Error(); got != "dependency error class insufficient_privilege (42501) on table audit_events" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "a b"}).Error(); strings.Contains(got, "on table") {
		t.Fatalf("a table that is not a plain identifier is not named: %q", got)
	}
}
