package factoracle

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// sqlWriteKeyword is every word of a statement that is not a plain read, and
// the SETTINGS of a SELECT too: the file needs none.
var sqlWriteKeyword = regexp.MustCompile(`(?i)\b(INSERT|ALTER|CREATE|DROP|UNDROP|TRUNCATE|OPTIMIZE|SYSTEM|SET|SETTINGS|DELETE|UPDATE|RENAME|ATTACH|DETACH|EXCHANGE|GRANT|REVOKE|KILL|INTO|OUTFILE|BACKUP|RESTORE)\b`)

func TestOwnerShareVenueExtractSQLIsSelectOnly(t *testing.T) {
	// The scan finds what it exists to find, and no column that holds a
	// keyword inside a longer name.
	for _, planted := range []string{"INSERT INTO repos SELECT 1", "select 1 settings max_threads = 1", "Alter table teams delete where 1",
		"OPTIMIZE TABLE repos FINAL", "system stop merges", "SET readonly = 0", "drop table repos", "TRUNCATE teams",
		"create table x (a Int8) engine = Memory", "SELECT 1 INTO OUTFILE 'x'"} {
		if !sqlWriteKeyword.MatchString(planted) {
			t.Fatalf("the scan misses %q", planted)
		}
	}
	if words := sqlWriteKeyword.FindAllString("SELECT updated_at, is_active, settings_x, created_at FROM teams", -1); len(words) != 0 {
		t.Fatalf("the scan flags %v in a plain read", words)
	}

	raw, err := os.ReadFile(OwnershipExtractSQLFile)
	if err != nil {
		t.Fatal(err)
	}
	// The whole file, comments included: the operator reads all of it.
	if words := sqlWriteKeyword.FindAllString(string(raw), -1); len(words) != 0 {
		t.Fatalf("%s holds %v: it must be SELECT statements only", OwnershipExtractSQLFile, words)
	}
	var code []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			code = append(code, line)
		}
	}
	var statements []string
	for _, statement := range strings.Split(strings.Join(code, "\n"), ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			statements = append(statements, statement)
		}
	}
	if len(statements) != len(ownershipExtractTables)+1 {
		t.Fatalf("%d statements, want the snapshot and one per table (%d)", len(statements), len(ownershipExtractTables)+1)
	}
	for _, statement := range statements {
		if !strings.HasPrefix(statement, "SELECT") || !strings.HasSuffix(statement, "FORMAT JSONEachRow") ||
			!strings.Contains(statement, "org_id = {org_id:String}") {
			t.Fatalf("a statement is not a SELECT of the bound organization in JSONEachRow:\n%s", statement)
		}
	}
	if !strings.Contains(statements[0], "'"+snapshotTag+"' AS "+extractTableColumn) {
		t.Fatalf("the first statement is not the snapshot line:\n%s", statements[0])
	}
}
