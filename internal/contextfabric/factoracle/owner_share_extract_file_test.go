package factoracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The ownership extract file of the owner-share pair (CHAOS-12691): the
// operator's SQL, the loader and the live plane in file mode, on hand-written
// files. No container and no network but the httptest fake ops of
// owner_share_live_test.go.

// osExtractFileText is a hand-written output of OwnershipExtractSQLFile as
// clickhouse-client prints it in JSONEachRow: the counts (UInt64) quoted, the
// flags and the shares numbers, a NULL valid_to null. It holds the rows of
// osReference: A owned by X (main, 0.7) and Y (0.3), B by X alone, C by
// nobody, Z an active team that owns nothing. ORG and TAKEN are replaced.
var osExtractFileText = strings.NewReplacer("@R@", tableRepos, "@T@", tableTeams, "@O@", tableTeamRepoOwnership).Replace(`{"extract_table":"snapshot","org":"ORG","taken_at":"TAKEN","rows_repos":"3","rows_teams":"3","rows_team_repo_ownership":"3"}
{"extract_table":"@R@","org_id":"ORG","id":"aaaaaaaa-0000-4000-8000-00000000000a","provider":"github","repo":"o/a","last_synced":"2026-09-01 00:00:00.000"}
{"extract_table":"@R@","org_id":"ORG","id":"bbbbbbbb-0000-4000-8000-00000000000b","provider":"github","repo":"o/b","last_synced":"2026-09-01 00:00:00.000"}
{"extract_table":"@R@","org_id":"ORG","id":"cccccccc-0000-4000-8000-00000000000c","provider":"github","repo":"o/c","last_synced":"2026-09-01 00:00:00.000"}
{"extract_table":"@T@","org_id":"ORG","id":"team-x","is_active":1,"updated_at":"2026-08-01 00:00:00.000000","last_synced":"2026-08-01 00:00:00.000000"}
{"extract_table":"@T@","org_id":"ORG","id":"team-y","is_active":1,"updated_at":"2026-08-01 00:00:00.000000","last_synced":"2026-08-01 00:00:00.000000"}
{"extract_table":"@T@","org_id":"ORG","id":"team-z","is_active":1,"updated_at":"2026-08-01 00:00:00.000000","last_synced":"2026-08-01 00:00:00.000000"}
{"extract_table":"@O@","org_id":"ORG","provider":"github","team_id":"team-x","repo_id":"aaaaaaaa-0000-4000-8000-00000000000a","repo_full_name":"o/a","source":"inferred","is_primary":1,"valid_from":"2026-09-01 00:00:00.000","valid_to":null,"updated_at":"2026-09-01 00:00:00.000","share":0.7}
{"extract_table":"@O@","org_id":"ORG","provider":"github","team_id":"team-y","repo_id":"aaaaaaaa-0000-4000-8000-00000000000a","repo_full_name":"o/a","source":"inferred","is_primary":0,"valid_from":"2026-09-01 00:00:00.000","valid_to":null,"updated_at":"2026-09-01 00:00:00.000","share":0.3}
{"extract_table":"@O@","org_id":"ORG","provider":"github","team_id":"team-x","repo_id":"bbbbbbbb-0000-4000-8000-00000000000b","repo_full_name":"o/b","source":"inferred","is_primary":1,"valid_from":"2026-09-01 00:00:00.000","valid_to":null,"updated_at":"2026-09-01 00:00:00.000","share":1}
`)

// osOtherOrgID is an organization that is not the run's.
const osOtherOrgID = "1f1f1f1f-0000-4000-8000-00000000f4c7"

// osExtractLines is osExtractFileText for the run's organization, its
// snapshot taken at takenAt, one line per item.
func osExtractLines(takenAt time.Time) []string {
	text := strings.NewReplacer("ORG", FixtureOrgID, "TAKEN", takenAt.UTC().Format("2006-01-02 15:04:05.000")).Replace(osExtractFileText)
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func osExtractText(lines []string) string { return strings.Join(lines, "\n") + "\n" }

// osWriteExtractFile writes an extract file with mode and, when modified is
// not zero, that modification time.
func osWriteExtractFile(t *testing.T, text string, mode os.FileMode, modified time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owner_share_extract.jsonl")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	if !modified.IsZero() {
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func osSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// The file reads as the hand-seeded rows of the store: the same owner
// shares, the same teams and repositories asked.
func TestOwnerShareExtractFileReadsAsTheSeededStore(t *testing.T) {
	text := osExtractText(osExtractLines(osAsOf))
	path := osWriteExtractFile(t, text, 0o600, osAsOf.Add(time.Minute))
	extract, record, err := LoadOwnershipExtract(path, FixtureOrgID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.OwnerShares(osAsOf)
	if err != nil {
		t.Fatal(err)
	}
	if owners := osOwners(got, osRepoA); !osSameShares(owners, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}) {
		t.Fatalf("owners of A %v, want X 0.7 and Y 0.3", owners)
	}
	if owners := osOwners(got, osRepoB); !osSameShares(owners, map[string]float64{osTeamX: 1}) {
		t.Fatalf("owners of B %v, want X 1", owners)
	}
	seeded, err := NewStore(osExtract(osReference()...))
	if err != nil {
		t.Fatal(err)
	}
	want, err := seeded.OwnerShares(osAsOf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the file reads %+v, the seeded store %+v", got, want)
	}
	if !reflect.DeepEqual(store.TeamIDs(), seeded.TeamIDs()) || !reflect.DeepEqual(store.RepositoryIDs(), seeded.RepositoryIDs()) {
		t.Fatalf("teams %v and repositories %v, the seeded store %v and %v", store.TeamIDs(), store.RepositoryIDs(), seeded.TeamIDs(), seeded.RepositoryIDs())
	}
	wantRows := map[string]int{tableRepos: 3, tableTeams: 3, tableTeamRepoOwnership: 3}
	if record.SHA256 != osSHA256(text) || record.Bytes != len(text) || !reflect.DeepEqual(record.Rows, wantRows) {
		t.Fatalf("record %+v: want the sha256 and the size of the file and 3 rows of each table", record)
	}
	if !record.SnapshotTakenAt.Equal(osAsOf) || !record.FileModifiedAt.Equal(osAsOf.Add(time.Minute)) || !record.SnapshotAt.Equal(osAsOf) {
		t.Fatalf("record %+v: the snapshot is the earlier of the server time and the file time", record)
	}
}

// Each refusal names why and no value of the file.
func TestOwnerShareExtractFileRefusals(t *testing.T) {
	good := osExtractLines(osAsOf)
	change := func(edit func(lines []string) []string) string {
		return osExtractText(edit(append([]string(nil), good...)))
	}
	cases := []struct {
		name string
		text string
		mode os.FileMode
		want string
	}{
		{"a file group and others can read", osExtractText(good), 0o644, "make it 0600"},
		{"a file the group can write", osExtractText(good), 0o620, "make it 0600"},
		{"a row of another organization", change(func(l []string) []string {
			l[8] = strings.Replace(l[8], FixtureOrgID, osOtherOrgID, 1)
			return l
		}), 0o600, "line 9 (team_repo_ownership): the row is of another organization"},
		{"a snapshot of another organization", change(func(l []string) []string {
			l[0] = strings.Replace(l[0], FixtureOrgID, osOtherOrgID, 1)
			return l
		}), 0o600, "snapshot line names another organization"},
		{"a file cut short", change(func(l []string) []string { return l[:9] }), 0o600,
			"table team_repo_ownership has 2 rows, its snapshot line counts 3"},
		{"a file with no snapshot line", change(func(l []string) []string { return l[1:] }), 0o600, "line 1 is not the snapshot line"},
		{"a second run appended", change(func(l []string) []string { return append(l, l...) }), 0o600, "line 11 is a second snapshot line"},
		{"a column the SQL does not select", change(func(l []string) []string {
			l[5] = strings.Replace(l[5], `"is_active":1,`, `"is_active":1,"name":"Team Y",`, 1)
			return l
		}), 0o600, "line 6 (teams): the columns are not the ones"},
		{"a column the SQL selects is missing", change(func(l []string) []string {
			l[8] = strings.Replace(l[8], `,"share":0.3`, "", 1)
			return l
		}), 0o600, "line 9 (team_repo_ownership): the columns are not the ones"},
		{"a table the extract does not hold", change(func(l []string) []string {
			l[2] = strings.Replace(l[2], `"extract_table":"`+tableRepos+`"`, `"extract_table":"work_unit_investments"`, 1)
			return l
		}), 0o600, "line 3: extract_table names no table"},
		{"a line that is not JSON", change(func(l []string) []string {
			l[3] = "Code: 497. DB::Exception: not enough privileges"
			return l
		}), 0o600, "line 4: a row is not JSON"},
		{"an empty file", "", 0o600, "is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := osWriteExtractFile(t, tc.text, tc.mode, time.Time{})
			_, _, err := LoadOwnershipExtract(path, FixtureOrgID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want one naming %q", err, tc.want)
			}
			for _, value := range []string{osOtherOrgID, FixtureOrgID, osTeamY, "Team Y", "privileges", path} {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("the error %q quotes a value of the file or its path", err)
				}
			}
		})
	}
	t.Run("a link to a good file", func(t *testing.T) {
		target := osWriteExtractFile(t, osExtractText(good), 0o600, time.Time{})
		link := filepath.Join(t.TempDir(), "link.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadOwnershipExtract(link, FixtureOrgID); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("error %v, want a link refused", err)
		}
	})
	t.Run("the control file loads", func(t *testing.T) {
		if _, _, err := LoadOwnershipExtract(osWriteExtractFile(t, osExtractText(good), 0o600, time.Time{}), FixtureOrgID); err != nil {
			t.Fatal(err)
		}
	})
}

// The live plane in file mode: the pair runs on the file with no ClickHouse,
// the report records the file; a stale snapshot is refused, before the login
// or at the last call.
func TestOwnerShareExtractFileModeRunsThePairAndRefusesAStaleSnapshot(t *testing.T) {
	type outcome struct {
		report *OwnerShareLiveReport
		fake   *opsFake
		text   string
		err    error
	}
	run := func(t *testing.T, takenAt, modified time.Time, maxAge time.Duration, now ...time.Time) outcome {
		t.Helper()
		fake := &opsFake{}
		rig := newOSLiveRig(t, fake)
		text := osExtractText(osExtractLines(takenAt))
		path := osWriteExtractFile(t, text, 0o600, modified)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		report, err := runOwnerShareLive(ctx, OwnerShareLiveConfig{Ops: rig.plane, OrgID: FixtureOrgID, Window: osLiveWindow,
			Measures: []string{"COUNT", "ISSUE_SIZE"}, OwnershipExtract: path, OwnershipExtractMaxAge: maxAge}, osFixedNow(now...))
		if len(fake.problems) != 0 {
			t.Fatalf("the fake ops saw: %v", fake.problems)
		}
		return outcome{report: report, fake: fake, text: text, err: err}
	}

	t.Run("a fresh snapshot", func(t *testing.T) {
		o := run(t, osAsOf, osAsOf.Add(time.Minute), 0, osAsOf.Add(2*time.Minute))
		if o.err != nil {
			t.Fatal(o.err)
		}
		if len(o.report.Findings()) != 0 || len(o.fake.logins) != 1 || o.report.OwnedRepositories != 2 || o.report.OwningTeams != 2 || o.report.Teams != 3 {
			t.Fatalf("report %+v with %d logins: want the control pass of owner_share_live_test.go", o.report, len(o.fake.logins))
		}
		e := o.report.OwnershipExtract
		if o.report.OwnershipSource != OwnershipSourceExtractFile || e == nil || e.SHA256 != osSHA256(o.text) ||
			e.Rows[tableTeamRepoOwnership] != 3 || !e.SnapshotAt.Equal(osAsOf) || e.AgeAtLastCall != "2m0s" || e.MaxAge != "15m0s" ||
			!strings.Contains(e.Note, "read once") {
			t.Fatalf("source %q, extract %+v: want the file recorded", o.report.OwnershipSource, e)
		}
		encoded, err := json.Marshal(o.report)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"ownership_source":"extract_file"`) || !strings.Contains(string(encoded), `"sha256":"`+e.SHA256+`"`) ||
			!strings.Contains(o.report.Table(), "owners read once from the extract snapshot") {
			t.Fatalf("the report does not carry the extract: %s\n%s", encoded, o.report.Table())
		}
	})
	t.Run("a server time older than the bound", func(t *testing.T) {
		o := run(t, osAsOf, osAsOf.Add(time.Minute), 0, osAsOf.Add(20*time.Minute))
		if o.err == nil || !strings.Contains(o.err.Error(), "20m0s before the ops calls") || !strings.Contains(o.err.Error(), "older than 15m0s") || len(o.fake.logins) != 0 {
			t.Fatalf("error %v and %d logins: want a refusal before the login", o.err, len(o.fake.logins))
		}
	})
	t.Run("a file time older than the bound", func(t *testing.T) {
		o := run(t, osAsOf.Add(19*time.Minute), osAsOf, 0, osAsOf.Add(20*time.Minute))
		if o.err == nil || !strings.Contains(o.err.Error(), "20m0s before the ops calls") || len(o.fake.logins) != 0 {
			t.Fatalf("error %v and %d logins: the file time is the earlier one and is 20m old", o.err, len(o.fake.logins))
		}
	})
	t.Run("a snapshot that grew stale during the calls", func(t *testing.T) {
		o := run(t, osAsOf, osAsOf, 0, osAsOf.Add(time.Minute), osAsOf.Add(time.Minute), osAsOf.Add(16*time.Minute))
		if o.err == nil || !strings.Contains(o.err.Error(), "16m0s at the last ops call") || len(o.fake.logins) != 1 {
			t.Fatalf("error %v and %d logins: want the run refused at its last call", o.err, len(o.fake.logins))
		}
	})
	t.Run("a wider bound admits an older snapshot", func(t *testing.T) {
		if o := run(t, osAsOf, osAsOf, 30*time.Minute, osAsOf.Add(20*time.Minute)); o.err != nil {
			t.Fatal(o.err)
		}
	})
}

func TestOwnerShareLiveNamesOneSourceOfTheOwners(t *testing.T) {
	rig := newOSLiveRig(t, &opsFake{})
	path := osWriteExtractFile(t, osExtractText(osExtractLines(osAsOf)), 0o600, time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name, extract, dsn, want string
	}{
		{"both", path, filepath.Join(t.TempDir(), "dsn"), "not both"},
		{"neither", "", "", "name the ownership extract file"},
	} {
		_, err := runOwnerShareLive(ctx, OwnerShareLiveConfig{Ops: rig.plane, OrgID: FixtureOrgID, Window: osLiveWindow,
			OwnershipExtract: tc.extract, ClickHouseDSNFile: tc.dsn}, time.Now)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
	if len(rig.fake.logins) != 0 {
		t.Fatalf("%d logins: a run with no single source of the owners must not reach ops", len(rig.fake.logins))
	}
}

// The committed SQL is the rendering of ownershipExtractTables, and every
// column it selects is a declared production column the venue capture also
// reads. ACR_O4_WRITE_EXTRACT_SQL=1 writes the rendering first.
func TestOwnerShareVenueExtractSQLIsTheRendering(t *testing.T) {
	rendered := ownershipExtractSQL()
	if os.Getenv("ACR_O4_WRITE_EXTRACT_SQL") == "1" {
		if err := os.WriteFile(OwnershipExtractSQLFile, []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(OwnershipExtractSQLFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != rendered {
		t.Fatalf("%s is not the rendering of ownershipExtractTables; ACR_O4_WRITE_EXTRACT_SQL=1 writes it. The rendering:\n%s", OwnershipExtractSQLFile, rendered)
	}
	for _, table := range ownershipExtractTables {
		declared, err := declaredColumns(table.Table)
		if err != nil {
			t.Fatal(err)
		}
		rules := extractSpecOf(table.Table).Rules
		for _, column := range table.Columns {
			found := false
			for _, d := range declared {
				found = found || d == column
			}
			if _, ruled := rules[column]; !found || !ruled {
				t.Errorf("%s.%s: declared %v, read by the venue capture %v", table.Table, column, found, ruled)
			}
		}
	}
}
