package devhealthschema_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

var directOperationalRead = regexp.MustCompile(`\b(FROM|JOIN)\s+(operational_incidents|operational_service_repository_mappings)\b`)

// operationalReaderFiles are the files that read the two tables; each must
// take its rows from the current-row relations.
var operationalReaderFiles = []string{
	"internal/contextfabric/devhealthfacts/fact_scope_entity_tree_policies.go",
	"internal/contextfabric/devhealthfacts/incidents.go",
	"internal/contextfabric/devhealthsource/source_counts.go",
	"internal/contextfabric/devhealthsource/tables.go",
	"internal/contextpacket/source_queries.go",
	"internal/contextpacket/source_rows.go",
}

// versionKeyedReads are the direct reads that name one physical row by its
// version (id and last_synced) rather than the current row, with the number of
// times each may occur.
var versionKeyedReads = map[string]int{
	// readIncidentDescriptions: the description of the row the page chose.
	"internal/contextfabric/devhealthsource/tables.go: FROM operational_incidents": 1,
}

func directOperationalReads(source string) []string {
	return directOperationalRead.FindAllString(source, -1)
}

func TestCurrentOperationalPickIsOpsOrdering(t *testing.T) {
	const opsPick = "ORDER BY org_id, id, source_revision DESC, source_conflict_key DESC, ingest_revision DESC LIMIT 1 BY org_id, id"
	for table, relation := range map[string]string{
		"operational_incidents":                   devhealthschema.CurrentOperationalIncidents,
		"operational_service_repository_mappings": devhealthschema.CurrentServiceRepositoryMappings,
	} {
		want := "(SELECT * FROM " + table + " WHERE org_id = {org_id:String} " + opsPick + ")"
		if relation != want {
			t.Errorf("%s relation is %q, want %q", table, relation, want)
		}
		if strings.Contains(relation, "FINAL") {
			t.Errorf("%s relation reads FINAL: %q", table, relation)
		}
	}
}

// productionSources is every non-test Go file of the module but the relations'
// own file, keyed by its path from the module root.
func productionSources(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	sources := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "devhealthschema/operational_current.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(rel)] = string(source)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return sources
}

// currentRowPickViolations lists every read of the two tables that does not go
// through the current-row relations, in a stable order.
func currentRowPickViolations(sources map[string]string) []string {
	var offenders []string
	for rel, source := range sources {
		for _, read := range directOperationalReads(source) {
			offenders = append(offenders, rel+": "+read)
		}
	}
	sort.Strings(offenders)
	allowed := map[string]int{}
	for key, count := range versionKeyedReads {
		allowed[key] = count
	}
	var violations []string
	for _, offender := range offenders {
		if allowed[offender] > 0 {
			allowed[offender]--
			continue
		}
		violations = append(violations, "direct read (FINAL keeps every revision of an id): "+offender)
	}
	for key, left := range allowed {
		if left != 0 {
			violations = append(violations, "version-keyed read listed but not found, delete the entry: "+key)
		}
	}
	for _, rel := range operationalReaderFiles {
		if !strings.Contains(sources[rel], "devhealthschema.CurrentOperationalIncidents") {
			violations = append(violations, "no longer reads devhealthschema.CurrentOperationalIncidents: "+rel)
		}
	}
	return violations
}

func TestOperationalReadsGoThroughTheCurrentRowPick(t *testing.T) {
	for _, violation := range currentRowPickViolations(productionSources(t)) {
		t.Error(violation)
	}
}

// A reader planted back on plain FINAL, in the real source tree, is a violation.
func TestPlantedFinalReaderIsAViolation(t *testing.T) {
	sources := productionSources(t)
	if violations := currentRowPickViolations(sources); len(violations) != 0 {
		t.Fatalf("the unplanted tree already fails: %v", violations)
	}
	plants := map[string][3]string{
		"internal/contextfabric/devhealthfacts/fact_scope_entity_tree_policies.go": {"SELECT id FROM ` + devhealthschema.CurrentOperationalIncidents + ` WHERE", "SELECT id FROM operational_incidents FINAL WHERE", "FROM operational_incidents"},
		"internal/contextpacket/source_queries.go":                                 {"INNER JOIN ` + devhealthschema.CurrentServiceRepositoryMappings + ` AS m ON", "INNER JOIN operational_service_repository_mappings AS m FINAL ON", "JOIN operational_service_repository_mappings"},
	}
	for rel, plant := range plants {
		planted := map[string]string{}
		for key, source := range sources {
			planted[key] = source
		}
		if strings.Count(planted[rel], plant[0]) != 1 {
			t.Fatalf("%s: the plant site %q is not there once", rel, plant[0])
		}
		planted[rel] = strings.Replace(planted[rel], plant[0], plant[1], 1)
		violations := currentRowPickViolations(planted)
		want := "direct read (FINAL keeps every revision of an id): " + rel + ": " + plant[2]
		found := false
		for _, violation := range violations {
			found = found || violation == want
		}
		if !found {
			t.Errorf("planted %q in %s: want violation %q, got %v", plant[1], rel, want, violations)
		}
	}
}

func TestDirectOperationalReadIsSeen(t *testing.T) {
	planted := []string{
		"SELECT id FROM operational_incidents FINAL WHERE org_id = {org_id:String}",
		"FROM operational_incidents AS i FINAL\nINNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id",
		"AND e.incident_id IN (SELECT id FROM operational_incidents FINAL WHERE is_deleted = 0)",
		"LEFT JOIN operational_service_repository_mappings AS m ON true",
		"FROM\n\toperational_incidents",
	}
	for _, statement := range planted {
		if len(directOperationalReads(statement)) == 0 {
			t.Errorf("a direct read was not seen: %q", statement)
		}
	}
	for _, statement := range []string{
		"FROM ` + devhealthschema.CurrentOperationalIncidents + ` AS i",
		"// operational_incidents carries no repo_id",
	} {
		if reads := directOperationalReads(statement); len(reads) != 0 {
			t.Errorf("a read through the relation was flagged: %q -> %v", statement, reads)
		}
	}
}
