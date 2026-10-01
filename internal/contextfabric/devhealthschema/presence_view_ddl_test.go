package devhealthschema_test

import (
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// The mirrored view is ops migration 100's CREATE OR REPLACE VIEW, byte for
// byte; testdata holds that statement as merged (sha256 of the whole record
// 2d48d601f4cd04c3). A reader of the view depends on its last_synced column.
func TestProjectMembershipPresenceViewDDLMirrorsMigration100(t *testing.T) {
	want, err := os.ReadFile("testdata/migration_100_presence_view.sql")
	if err != nil {
		t.Fatal(err)
	}
	statement := strings.TrimSuffix(strings.TrimSpace(string(want)), ";")
	if devhealthschema.ProjectMembershipPresenceViewDDL != statement {
		t.Fatalf("ProjectMembershipPresenceViewDDL differs from migration 100's view:\n got: %s\nwant: %s", devhealthschema.ProjectMembershipPresenceViewDDL, statement)
	}
	for _, needle := range []string{"max(ingested_at) AS max_ingested_at", "max_ingested_at AS last_synced", "w.ingested_at AS last_synced"} {
		if !strings.Contains(statement, needle) {
			t.Fatalf("migration 100's view lost %q: the testdata is not the ingest-column view", needle)
		}
	}
}
