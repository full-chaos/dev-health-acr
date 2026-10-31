package postgres

import (
	"strings"
	"testing"
)

func TestReconcileClientClassApplies_needsVersion63InHistory(t *testing.T) {
	cases := []struct {
		name     string
		versions []int64
		want     bool
	}{
		{"empty history", nil, false},
		{"before 0063", []int64{1, 58, 62}, false},
		{"exactly 0063", []int64{62, 63}, true},
		{"after 0063", []int64{63, 64, 65}, true},
		{"only later versions", []int64{64, 65}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			versions := map[int64]struct{}{}
			for _, version := range tc.versions {
				versions[version] = struct{}{}
			}
			if got := reconcileClientClassApplies(versions); got != tc.want {
				t.Fatalf("reconcileClientClassApplies(%v) = %v, want %v", tc.versions, got, tc.want)
			}
		})
	}
}

func TestReconcileClientClassSQL_isNotAMigrationAndKeepsTheTriggerOrder(t *testing.T) {
	runner, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	for _, migration := range runner.migrations {
		if strings.Contains(migration.Name, "reconcile") {
			t.Fatalf("reconcile step loaded as migration %s", migration.Name)
		}
	}
	body := reconcileClientClassSQL
	disable := strings.Index(body, "DISABLE TRIGGER trg_client_credentials_provenance_immutable")
	update := strings.Index(body, "client_class = 'org_bearer'")
	enable := strings.Index(body, "ENABLE TRIGGER trg_client_credentials_provenance_immutable")
	if disable < 0 || update < disable || enable < update {
		t.Fatalf("reconcile SQL must disable, update, enable in order (got %d, %d, %d)", disable, update, enable)
	}
}
