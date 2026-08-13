package postgres

import "testing"

// TestEmbedded_loadsAndOrdersEveryMigration is a DB-free structural check:
// every embedded .sql file parses into a valid, uniquely-versioned,
// ascending Migration list. It exists so a migration file naming mistake
// (duplicate version, non-numeric prefix) fails fast without needing the
// testcontainers-backed integration suites in this package.
func TestEmbedded_loadsAndOrdersEveryMigration(t *testing.T) {
	runner, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	if len(runner.migrations) == 0 {
		t.Fatal("Embedded() loaded no migrations")
	}
	seenTen := false
	for index, migration := range runner.migrations {
		if index > 0 && migration.Version <= runner.migrations[index-1].Version {
			t.Fatalf("migrations are not strictly ascending at index %d: %d after %d", index, migration.Version, runner.migrations[index-1].Version)
		}
		if migration.Checksum == "" {
			t.Fatalf("migration %d (%s) has no checksum", migration.Version, migration.Name)
		}
		if migration.Version == 10 {
			seenTen = true
			if migration.Name != "0010_context_fabric_answer_reuse.sql" {
				t.Fatalf("migration 10 name = %q, want the CHAOS-3782 reuse migration", migration.Name)
			}
		}
	}
	if !seenTen {
		t.Fatal("expected migration 10 (CHAOS-3782 answer reuse) to be embedded")
	}
}
