package devhealthschema_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// CLASS CLOSURE for hand-authored schema residue (CHAOS-3781 round 4).
//
// Three consecutive review rounds each found a different hand-authored
// facet of the same physical definition drifted from production:
//
//   - round 3 F3: sorting keys, authored beside probed types, two wrong.
//   - round 3 F4: whole CREATE TABLE blocks in a test that the "complete"
//     extraction had missed.
//   - round 4 R4-1/R4-3: the engine VERSION column dropped by carrying only
//     the engine class, plus another missed fixture in a package nobody was
//     looking at.
//
// Each was spot-fixed and each time the fix was reported complete. The
// pattern is not carelessness about any one facet; it is that
// "I looked and found no more" is unfalsifiable — an absence audit over a
// space nobody has enumerated. Protocol therefore forbids a fourth
// spot-fix, and this file is the mechanical argument that replaces it.
//
// The closure has two halves:
//
//	(a) ONE SOURCE. Every field of a declared table's physical definition
//	    -- columns, positions, types, engine, version column, partition
//	    key, sorting key, settings -- comes from ProductionColumns and
//	    EngineFull, both probed from live. TestEveryPhysicalFacetHasOneSource
//	    below asserts no second source can exist by construction, because
//	    there is no other exported field to author.
//
//	(b) NO BYPASS. TestNoTestAuthorsDeclaredTableDDL greps every *_test.go
//	    in the repository for a CREATE TABLE naming a declared table and
//	    fails unless it came from the shared renderer. The table set is
//	    DERIVED from the declaration, never curated here, so a table added
//	    to the declaration is immediately protected without anyone
//	    remembering to add it to a list.
//
// What (b) would have caught, had it existed:
//
//	round 3 F4 -- chaos3780_findings_integration_test.go hand-wrote six
//	              declared tables. Caught on the first run.
//	round 4 R4-3 -- hosted_clickhouse_fixture_test.go hand-wrote repos and
//	              ci_pipeline_runs in cmd/acr-api, a package outside the
//	              one under review. Caught identically: the sweep does not
//	              care which package a test lives in, which is precisely
//	              the blind spot that let this one survive.
//
// It would NOT have caught round 3 F3 (wrong sorting keys), because that
// was a wrong value in the single source rather than a bypass of it. That
// half is closed by (a) plus the live freshness check, which now compares
// engine_full and column position -- and which did catch a wrong value on
// its first run.

// repoRoot walks up from this package to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate the module root from the test's working directory")
		}
		directory = parent
	}
}

// createTablePattern matches a CREATE TABLE naming any identifier. The
// table name is compared against the declaration afterwards rather than
// baked into the pattern, so the check derives from the declaration.
var createTablePattern = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "[`\"]?" + `(\w+)`)

// exemptionPattern matches an in-file opt-out. The trailing text is
// required: an exemption without a stated reason is exactly the silent
// bypass this test exists to prevent.
//
// Legitimate uses are tests where a declared table's NAME is incidental --
// input to a SQL parser or migration replayer, say -- rather than a
// fixture that production readers run against. Those tests are not
// replicating production and gain nothing from the renderer.
var exemptionPattern = regexp.MustCompile(`devhealthschema:not-a-production-replica\s+\S+`)

// TestNoTestAuthorsDeclaredTableDDL is closure half (b).
func TestNoTestAuthorsDeclaredTableDDL(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	declared := map[string]struct{}{}
	for table := range devhealthschema.ProductionColumns {
		declared[table] = struct{}{}
	}

	var offences []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip build output and vendored trees; neither contains
			// first-party fixtures.
			switch info.Name() {
			case ".git", ".tmp", ".omo", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// This file quotes table names while explaining the rule.
		if strings.HasSuffix(path, "closure_test.go") {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// A file may opt out, but only in place and only with a reason.
		// This is deliberately NOT a curated list held here: a central
		// list is another thing to forget to update, which is the failure
		// this whole closure exists to end. The marker sits at the site,
		// so the person writing the DDL is the one who has to justify it,
		// and a table added to the declaration still trips every file
		// that has not justified itself.
		if exemptionPattern.Match(contents) {
			return nil
		}
		for _, match := range createTablePattern.FindAllStringSubmatch(string(contents), -1) {
			table := match[1]
			if _, isDeclared := declared[table]; !isDeclared {
				continue
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				relative = path
			}
			offences = append(offences, relative+" hand-writes CREATE TABLE "+table)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}

	if len(offences) > 0 {
		t.Fatalf("these tests author DDL for tables the declaration owns; render it with devhealthschema.DDL(...) instead, "+
			"or the fixture silently drifts from production the way rounds 3 and 4 both found:\n  %s",
			strings.Join(offences, "\n  "))
	}
}

// TestEveryPhysicalFacetHasOneSource is closure half (a): every declared
// table carries a complete physical definition, and there is nowhere else
// to author one.
//
// The assertion is deliberately about COMPLETENESS rather than content --
// content freshness belongs to the live check, which compares against
// system.columns and system.tables. What this guarantees is that no table
// can be half-declared, which is the state that invites someone to fill
// the gap locally and start a fourth round of drift.
func TestEveryPhysicalFacetHasOneSource(t *testing.T) {
	t.Parallel()
	if len(devhealthschema.ProductionColumns) == 0 {
		t.Fatal("the declaration is empty")
	}
	for table, columns := range devhealthschema.ProductionColumns {
		if len(columns) == 0 {
			t.Errorf("%s declares no columns", table)
		}
		for _, column := range columns {
			if strings.TrimSpace(column.Name) == "" || strings.TrimSpace(column.Type) == "" {
				t.Errorf("%s has a column with an empty name or type: %+v", table, column)
			}
		}
		engine, ok := devhealthschema.EngineFull[table]
		if !ok {
			t.Errorf("%s declares columns but no engine definition -- a half-declared table is what invites a local hand-written fixture", table)
			continue
		}
		// engine_full always names the engine and its ORDER BY; a value
		// missing either is a truncated probe rather than a real
		// definition.
		if !strings.Contains(engine, "MergeTree") || !strings.Contains(engine, "ORDER BY") {
			t.Errorf("%s engine definition looks truncated, not probed: %q", table, engine)
		}
	}
	for table := range devhealthschema.EngineFull {
		if _, ok := devhealthschema.ProductionColumns[table]; !ok {
			t.Errorf("%s declares an engine but no columns", table)
		}
	}
}

// TestDeclarationExposesNoSecondSource ENFORCES closure half (a) rather
// than asserting it in prose.
//
// This exists because the prose claim was false when first written. The
// EngineFull consolidation left the superseded Engines and OrderBy maps in
// the file -- dead, but exported and therefore authorable, which is
// exactly the second source the closure forbids. Nothing caught it:
// TestEveryPhysicalFacetHasOneSource only checked that every table was
// COMPLETELY declared, which stayed true with a stale duplicate sitting
// beside the real one.
//
// "There is no other source" is a claim about what does NOT exist, and an
// absence is precisely what a completeness check cannot see -- the same
// unfalsifiable shape as the "sweep is complete" reports of rounds 3 and
// 4. So it is checked mechanically: this package may declare exactly the
// two schema maps below and no others.
func TestDeclarationExposesNoSecondSource(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "contextfabric", "devhealthschema", "schema.go"))
	if err != nil {
		t.Fatalf("read the declaration: %v", err)
	}
	sanctioned := map[string]struct{}{"ProductionColumns": {}, "EngineFull": {}}
	declarations := regexp.MustCompile(`(?m)^var (\w+) = map\[string\]`).FindAllStringSubmatch(string(contents), -1)
	if len(declarations) == 0 {
		t.Fatal("found no schema maps at all; the pattern no longer matches the file it guards")
	}
	for _, match := range declarations {
		if _, ok := sanctioned[match[1]]; !ok {
			t.Errorf("devhealthschema declares a second schema map %q -- every facet of a table's physical definition must come from ProductionColumns and EngineFull, or a fixture can be built from a stale duplicate", match[1])
		}
	}
	for name := range sanctioned {
		var found bool
		for _, match := range declarations {
			if match[1] == name {
				found = true
			}
		}
		if !found {
			t.Errorf("the sanctioned map %q is gone; this guard is now checking the wrong thing", name)
		}
	}
}

// TestReplacingMergeTreeDeclarationsCarryTheirVersionColumn is the R4-1
// invariant stated directly: a ReplacingMergeTree's version column decides
// which row FINAL keeps, so a declaration that names the engine without it
// produces a fixture with different dedup semantics than production.
func TestReplacingMergeTreeDeclarationsCarryTheirVersionColumn(t *testing.T) {
	t.Parallel()
	versioned := regexp.MustCompile(`^ReplacingMergeTree\((\w+)\)`)
	for table, engine := range devhealthschema.EngineFull {
		if !strings.HasPrefix(engine, "ReplacingMergeTree") {
			continue
		}
		match := versioned.FindStringSubmatch(engine)
		if match == nil {
			t.Errorf("%s is a ReplacingMergeTree with no version column in its declaration (%q) -- FINAL would keep an arbitrary row among those sharing a sort key, not the newest", table, engine)
			continue
		}
		// The version column must itself be declared, or the rendered
		// CREATE TABLE cannot reference it.
		var found bool
		for _, column := range devhealthschema.ProductionColumns[table] {
			if column.Name == match[1] {
				found = true
			}
		}
		if !found {
			t.Errorf("%s versions on %q but does not declare that column; the rendered DDL would not compile", table, match[1])
		}
	}
}
