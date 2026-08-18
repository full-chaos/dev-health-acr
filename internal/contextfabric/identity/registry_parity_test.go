package identity_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

var orderByPattern = regexp.MustCompile(`ORDER BY \(([^)]*)\)`)

// orderByColumns extracts a table's declared ReplacingMergeTree/MergeTree
// sorting-key column list, verbatim and in schema order, from
// devhealthschema.EngineFull -- the single declared snapshot of the
// production ClickHouse schema (devhealthschema/schema.go's own doc
// comment: "the same type drift bit this codebase twice").
func orderByColumns(t *testing.T, table string) []string {
	t.Helper()
	engine, ok := devhealthschema.EngineFull[table]
	if !ok {
		t.Fatalf("devhealthschema.EngineFull has no entry for table %q", table)
	}
	m := orderByPattern.FindStringSubmatch(engine)
	if m == nil {
		t.Fatalf("table %q engine string has no ORDER BY clause: %q", table, engine)
	}
	parts := strings.Split(m[1], ",")
	cols := make([]string, len(parts))
	for i, p := range parts {
		cols[i] = strings.TrimSpace(p)
	}
	return cols
}

// TestRegistryStructuralParityWithSchema is the S1 structural parity test
// between the registry and the live derivation sites (design brief §5 item
// 2 / §6). S1 makes no behavior change, so this does not call the live
// producers -- instead it proves every registered kind's declared
// natural-key segments are EXACTLY that kind's live ClickHouse table's
// ORDER BY key minus org_id, in the SAME order: two-way, exact set AND
// order. A future schema change (a reordered sorting key, an added
// dedup-relevant column) the registry has not been updated for fails
// loudly here instead of silently producing a non-canonical id whenever S2
// wires this package into a producer.
func TestRegistryStructuralParityWithSchema(t *testing.T) {
	for _, reg := range identity.Registry {
		reg := reg
		t.Run(reg.Kind, func(t *testing.T) {
			schemaCols := orderByColumns(t, reg.Table)
			if len(schemaCols) == 0 || schemaCols[0] != "org_id" {
				t.Fatalf("table %q ORDER BY does not lead with org_id: %v", reg.Table, schemaCols)
			}
			natural := schemaCols[1:]
			if len(natural) != len(reg.Columns) {
				t.Fatalf("kind %q: registry declares %d segments %v, schema natural key (minus org_id) has %d: %v",
					reg.Kind, len(reg.Columns), reg.Columns, len(natural), natural)
			}
			for i := range natural {
				if natural[i] != reg.Columns[i] {
					t.Errorf("kind %q segment %d: registry says %q, schema ORDER BY says %q (order-sensitive)",
						reg.Kind, i, reg.Columns[i], natural[i])
				}
			}
		})
	}
}

// TestRegistryCoversEveryChangedKind is the reverse direction of the
// parity check: every kind design brief §1.2's fixed-kinds table names
// must have exactly one registry entry -- "zero exemptions" (§1.3), and no
// silent extras either.
func TestRegistryCoversEveryChangedKind(t *testing.T) {
	want := []string{
		identity.KindCIPipelineRun,
		identity.KindPullRequestReview,
		identity.KindDeployment,
		identity.KindWorkItem,
		identity.KindProject,
	}
	for _, kind := range want {
		if _, ok := identity.Lookup(kind); !ok {
			t.Errorf("registry is missing kind %q (design brief §1.2)", kind)
		}
	}
	if len(identity.Registry) != len(want) {
		t.Errorf("registry has %d entries, want exactly %d (zero exemptions, no extras): %+v",
			len(identity.Registry), len(want), identity.Registry)
	}
}

// TestRegistryTablesAreDeclaredInEngineFull guards against a registry
// entry pointing at a table name devhealthschema no longer declares (a
// rename on one side without the other).
func TestRegistryTablesAreDeclaredInEngineFull(t *testing.T) {
	for _, reg := range identity.Registry {
		if _, ok := devhealthschema.EngineFull[reg.Table]; !ok {
			t.Errorf("kind %q points at table %q, which devhealthschema.EngineFull does not declare", reg.Kind, reg.Table)
		}
	}
}
