package factoracle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// The ownership extract file of the owner-share pair (CHAOS-12691).
//
// Lead ruling on CHAOS-12691: no ClickHouse user is made on the venue for
// this pair. The venue operator runs OwnershipExtractSQLFile, SELECT
// statements only, in the venue ClickHouse pod and keeps its output in a file
// only the operator can read. ACR_O4_OWNERSHIP_EXTRACT names that file; the
// live plane then reads the owners from it and never opens the venue
// ClickHouse. Each line is decoded by decodeRow, the reader of every extract,
// into an Extract, so Store.OwnerShares runs on it unchanged.
//
// The SQL file is the rendering of ownershipExtractTables
// (ownershipExtractSQL); TestOwnerShareVenueExtractSQLIsTheRendering holds the
// committed file to it, so the columns the file holds are the columns the
// loader accepts.

// OwnershipExtractSQLFile is the operator's SQL, relative to this package.
const OwnershipExtractSQLFile = "testdata/owner_share_venue_extract.sql"

// DefaultOwnershipExtractMaxAge bounds the age of the extract snapshot,
// before the ops calls and at the last of them
// (ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE overrides it). Ops reads the owners at its
// own now; the snapshot stands for that reading only while no sync can have
// moved them.
const DefaultOwnershipExtractMaxAge = 15 * time.Minute

// ownershipExtractMaxBytes caps the file read.
const ownershipExtractMaxBytes = 64 << 20

const (
	// extractTableColumn names the table of a line of the file.
	extractTableColumn = "extract_table"
	// snapshotTag tags the first line: the organization, the server time
	// before the reads and the row count of each table.
	snapshotTag = "snapshot"
)

// The values of OwnerShareLiveReport.OwnershipSource.
const (
	// OwnershipSourceClickHouse: the owners were read from the venue
	// ClickHouse before and after the ops calls.
	OwnershipSourceClickHouse = "clickhouse"
	// OwnershipSourceExtractFile: the owners were read once, from the
	// operator's extract file.
	OwnershipSourceExtractFile = "extract_file"
)

// ownershipExtractTable is one table of the file and the columns the store
// reads of it (Store.OwnerShares, Store.TeamIDs, Store.RepositoryIDs), with
// org_id for the organization check.
type ownershipExtractTable struct {
	Table   string
	Columns []string
}

// ownershipExtractTables are the rows Store.OwnerShares needs and nothing
// more. team_repo_ownership is reduced as the venue capture reduces it (the
// OrderBy and LimitBy of its extractTables spec: of one row group the earliest
// valid_from, of one valid_from the newest version), but a row that ended
// before the window is kept: the first record of a repository is the earliest
// valid_from of every owning row, ended or not (Store.OwnerShares, ops
// ownershipwindow).
var ownershipExtractTables = []ownershipExtractTable{
	// repos as FINAL reads it: ReplacingMergeTree(last_synced) ORDER BY
	// (org_id, id), and the provider and name a row without repo_id resolves
	// by.
	{Table: tableRepos, Columns: []string{"org_id", "id", "provider", "repo", "last_synced"}},
	// teams: the newest row of an id decides whether the team is active
	// (inactiveTeams, TeamIDs).
	{Table: tableTeams, Columns: []string{"org_id", "id", "is_active", "updated_at", "last_synced"}},
	// team_repo_ownership as FINAL reads it: the sorting key, the version
	// (updated_at), the validity, the main-owner flag and the share.
	{Table: tableTeamRepoOwnership, Columns: []string{"org_id", "provider", "team_id", "repo_id", "repo_full_name", "source", "is_primary",
		"valid_from", "valid_to", "updated_at", "share"}},
}

// extractSpecOf is the extractTables spec of one table.
func extractSpecOf(table string) tableSpec {
	for _, spec := range extractTables {
		if spec.Table == table {
			return spec
		}
	}
	return tableSpec{}
}

// ownershipExtractSQLHeader opens the SQL file. It names no statement kind
// but SELECT: TestOwnerShareVenueExtractSQLIsSelectOnly scans the whole file.
const ownershipExtractSQLHeader = `-- The ownership extract of the owner-share pair (CHAOS-12691) for one
-- organization: SELECT statements only, no other kind of statement.
--
-- This file is the rendering of ownershipExtractTables in
-- owner_share_extract_file.go; TestOwnerShareVenueExtractSQLIsTheRendering
-- holds it to that rendering. Do not edit it by hand.
--
-- Run it in the venue ClickHouse pod with the organization bound as a
-- parameter, and keep the output in a file only you can read:
--
--   umask 077
--   kubectl exec -i -n <namespace> <clickhouse pod> -- clickhouse-client \
--     --database <ops analytics database> --multiquery \
--     --param_org_id=<organization id> \
--     < owner_share_venue_extract.sql > owner_share_extract.jsonl
--
-- Then, as the user that owns that file and within 15 minutes, run
-- make o4-owner-share-live with the ACR_O4_* variables of the target and
-- ACR_O4_OWNERSHIP_EXTRACT=<path of owner_share_extract.jsonl>
-- (ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE changes the bound).
--
-- The output is JSONEachRow: one JSON object per line, its table named in
-- extract_table. The first line (extract_table 'snapshot') holds the
-- organization, the server time before the reads and the row count of each
-- table; a file whose rows do not match those counts is refused.
`

// ownershipExtractSQL renders the operator's SQL: the snapshot line first,
// then one statement per table.
func ownershipExtractSQL() string {
	var b strings.Builder
	b.WriteString(ownershipExtractSQLHeader)
	fmt.Fprintf(&b, "\nSELECT\n    '%s' AS %s,\n    {org_id:String} AS org,\n    now64(3, 'UTC') AS taken_at", snapshotTag, extractTableColumn)
	for _, t := range ownershipExtractTables {
		inner := "SELECT " + strings.Join(t.Columns, ", ") + " FROM " + t.Table + " WHERE org_id = {org_id:String}"
		if spec := extractSpecOf(t.Table); spec.LimitBy != "" {
			inner += " LIMIT 1 BY " + spec.LimitBy
		}
		fmt.Fprintf(&b, ",\n    (SELECT count() FROM (%s)) AS rows_%s", inner, t.Table)
	}
	b.WriteString("\nFORMAT JSONEachRow;\n")
	for _, t := range ownershipExtractTables {
		spec := extractSpecOf(t.Table)
		fmt.Fprintf(&b, "\nSELECT '%s' AS %s, %s\nFROM %s\nWHERE org_id = {org_id:String}\n", t.Table, extractTableColumn, strings.Join(t.Columns, ", "), t.Table)
		if spec.OrderBy != "" {
			b.WriteString("ORDER BY " + spec.OrderBy + "\n")
		}
		if spec.LimitBy != "" {
			b.WriteString("LIMIT 1 BY " + spec.LimitBy + "\n")
		}
		b.WriteString("FORMAT JSONEachRow;\n")
	}
	return b.String()
}

// OwnershipExtractRecord is what the report keeps of the operator's
// ownership extract file. It holds no path and no row.
type OwnershipExtractRecord struct {
	// SHA256 and Bytes name the file read.
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	// Rows counts the rows of each table.
	Rows map[string]int `json:"rows"`
	// SnapshotTakenAt is the server time the SQL stamped before the reads;
	// FileModifiedAt is the file's modification time. SnapshotAt is the
	// earlier of the two: the age of the snapshot is measured from it, so
	// neither a copy that renews the modification time nor a server clock
	// ahead of this one makes an old snapshot young.
	SnapshotTakenAt time.Time `json:"snapshot_taken_at"`
	FileModifiedAt  time.Time `json:"file_modified_at"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	// MaxAge is the bound; AgeAtLastCall the age of the snapshot at the end
	// of the ops calls.
	MaxAge        string `json:"max_age"`
	AgeAtLastCall string `json:"age_at_last_call"`
	// Note says how the owners were read.
	Note string `json:"note"`
}

// ownershipSnapshot is the first line of the file.
type ownershipSnapshot struct {
	takenAt time.Time
	rows    map[string]int
}

// LoadOwnershipExtract reads the operator's ownership extract file for one
// organization. It refuses a path that is not a regular file, a file group or
// others can reach, a file the running user does not own, a row of another
// organization, a line whose columns are not the ones the SQL selects, and a
// file whose rows do not match the counts of its snapshot line. Its errors
// name a line number at most, never a value of the file.
func LoadOwnershipExtract(path, orgID string) (*Extract, *OwnershipExtractRecord, error) {
	if path == "" || orgID == "" {
		return nil, nil, errors.New("the ownership extract needs a file and an organization")
	}
	linked, err := os.Lstat(path)
	if err != nil {
		return nil, nil, errors.New("the ownership extract file is not readable")
	}
	if !linked.Mode().IsRegular() {
		return nil, nil, errors.New("the ownership extract path is not a regular file (a link, a directory or a device is refused)")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, errors.New("the ownership extract file is not readable")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !os.SameFile(linked, info) {
		return nil, nil, errors.New("the ownership extract file changed while it was opened")
	}
	if err := checkOwnershipExtractFile(info); err != nil {
		return nil, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, ownershipExtractMaxBytes+1))
	if err != nil {
		return nil, nil, errors.New("the ownership extract file is not readable")
	}
	if len(raw) > ownershipExtractMaxBytes {
		return nil, nil, fmt.Errorf("the ownership extract file is larger than %d MiB", ownershipExtractMaxBytes>>20)
	}
	extract, snap, err := parseOwnershipExtract(raw, orgID)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	record := &OwnershipExtractRecord{SHA256: hex.EncodeToString(sum[:]), Bytes: len(raw), Rows: map[string]int{},
		SnapshotTakenAt: snap.takenAt, FileModifiedAt: info.ModTime().UTC(), SnapshotAt: snap.takenAt}
	if record.FileModifiedAt.Before(record.SnapshotAt) {
		record.SnapshotAt = record.FileModifiedAt
	}
	for _, t := range ownershipExtractTables {
		record.Rows[t.Table] = len(extract.Tables[t.Table])
	}
	return extract, record, nil
}

// checkOwnershipExtractFile refuses a file group or others can reach, and a
// file the running user does not own.
func checkOwnershipExtractFile(info os.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("the ownership extract file has mode %04o: group or others can reach it; make it 0600", perm)
	}
	owned, known := fileOwnedByRunningUser(info)
	if !known {
		return errors.New("the owner of the ownership extract file cannot be read on this system")
	}
	if !owned {
		return errors.New("the ownership extract file is not owned by the user running the pair")
	}
	return nil
}

// parseOwnershipExtract reads the lines of the file: the snapshot line first,
// then the rows, each tagged with its table.
func parseOwnershipExtract(raw []byte, orgID string) (*Extract, ownershipSnapshot, error) {
	var snap ownershipSnapshot
	text := bytes.TrimSuffix(raw, []byte("\n"))
	if len(text) == 0 {
		return nil, snap, errors.New("the ownership extract file is empty")
	}
	columns := map[string]map[string]bool{}
	out := &Extract{Tables: map[string][]Row{}}
	for _, t := range ownershipExtractTables {
		columns[t.Table] = map[string]bool{}
		for _, c := range t.Columns {
			columns[t.Table][c] = true
		}
		out.Tables[t.Table] = []Row{}
	}
	for i, line := range bytes.Split(text, []byte("\n")) {
		n := i + 1
		row, err := decodeRow(line)
		if err != nil {
			return nil, snap, fmt.Errorf("ownership extract line %d: %w", n, err)
		}
		tag, _ := row[extractTableColumn].(string)
		delete(row, extractTableColumn)
		switch {
		case n == 1:
			if tag != snapshotTag {
				return nil, snap, fmt.Errorf("ownership extract line 1 is not the snapshot line: the file is not the output of %s", OwnershipExtractSQLFile)
			}
			if snap, err = readOwnershipSnapshot(row, orgID); err != nil {
				return nil, snap, err
			}
		case tag == snapshotTag:
			return nil, snap, fmt.Errorf("ownership extract line %d is a second snapshot line: the file holds more than one run", n)
		case columns[tag] != nil:
			if org, _ := rowString(row, "org_id"); org != orgID {
				return nil, snap, fmt.Errorf("ownership extract line %d (%s): the row is of another organization than the run's", n, tag)
			}
			if !sameColumns(row, columns[tag]) {
				return nil, snap, fmt.Errorf("ownership extract line %d (%s): the columns are not the ones %s selects", n, tag, OwnershipExtractSQLFile)
			}
			out.Tables[tag] = append(out.Tables[tag], row)
		default:
			return nil, snap, fmt.Errorf("ownership extract line %d: extract_table names no table of the extract", n)
		}
	}
	for _, t := range ownershipExtractTables {
		if got, want := len(out.Tables[t.Table]), snap.rows[t.Table]; got != want {
			return nil, snap, fmt.Errorf("ownership extract table %s has %d rows, its snapshot line counts %d: the file was cut, or the venue store changed during the extract; take it again", t.Table, got, want)
		}
	}
	return out, snap, nil
}

func readOwnershipSnapshot(row Row, orgID string) (ownershipSnapshot, error) {
	snap := ownershipSnapshot{rows: map[string]int{}}
	want := map[string]bool{"org": true, "taken_at": true}
	for _, t := range ownershipExtractTables {
		want["rows_"+t.Table] = true
	}
	if !sameColumns(row, want) {
		return snap, fmt.Errorf("the ownership extract snapshot line has not the columns %s selects", OwnershipExtractSQLFile)
	}
	if org, _ := rowString(row, "org"); org != orgID {
		return snap, errors.New("the ownership extract snapshot line names another organization than the run's")
	}
	takenAt, err := rowTime(row, "taken_at")
	if err != nil {
		return snap, errors.New("the ownership extract snapshot line has no server time")
	}
	snap.takenAt = takenAt
	for _, t := range ownershipExtractTables {
		count, ok := rowCount(row["rows_"+t.Table])
		if !ok {
			return snap, fmt.Errorf("the ownership extract snapshot line has no row count of %s", t.Table)
		}
		snap.rows[t.Table] = count
	}
	return snap, nil
}

// rowCount reads a count(): a UInt64, which JSONEachRow quotes by default
// (output_format_json_quote_64bit_integers).
func rowCount(v any) (int, bool) {
	var text string
	switch t := v.(type) {
	case json.Number:
		text = t.String()
	case string:
		text = t
	default:
		return 0, false
	}
	n, err := strconv.Atoi(text)
	return n, err == nil && n >= 0
}

func sameColumns(row Row, want map[string]bool) bool {
	if len(row) != len(want) {
		return false
	}
	for column := range row {
		if !want[column] {
			return false
		}
	}
	return true
}

// checkAge refuses a snapshot older than maxAge at the instant at.
func (r *OwnershipExtractRecord) checkAge(at time.Time, maxAge time.Duration, when string) error {
	if age := at.Sub(r.SnapshotAt); age > maxAge {
		return fmt.Errorf("the ownership extract snapshot was taken at %s, %s %s; ops reads the owners at its own now, so a snapshot older than %s (ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE) is refused: take the extract again and run at once",
			r.SnapshotAt.Format(time.RFC3339), age.Round(time.Second), when, maxAge)
	}
	return nil
}

// runOwnerShareFromExtract is the owner-share pair with the owners read once
// from the operator's extract file. The snapshot must be younger than the
// bound before the ops calls and still at the last of them. The owners of
// the snapshot are read as of the start and the end of the calls, as in the
// ClickHouse mode; the venue store itself is not read again.
func runOwnerShareFromExtract(ctx context.Context, cfg OwnerShareLiveConfig, measures []string, now func() time.Time) (*OwnerShareLiveReport, error) {
	maxAge := cfg.OwnershipExtractMaxAge
	if maxAge == 0 {
		maxAge = DefaultOwnershipExtractMaxAge
	}
	if maxAge < 0 {
		return nil, errors.New("the ownership extract age bound is negative")
	}
	extract, record, err := LoadOwnershipExtract(cfg.OwnershipExtract, cfg.OrgID)
	if err != nil {
		return nil, err
	}
	record.MaxAge = maxAge.String()
	if err := record.checkAge(now().UTC(), maxAge, "before the ops calls"); err != nil {
		return nil, err
	}
	store, err := NewStore(extract)
	if err != nil {
		return nil, err
	}
	report, err := runOwnerShare(ctx, cfg.Ops, store, cfg.Window, measures, now)
	if err != nil {
		return nil, err
	}
	if err := record.checkAge(report.OwnershipCheckedAt, maxAge, "at the last ops call"); err != nil {
		return nil, err
	}
	record.AgeAtLastCall = report.OwnershipCheckedAt.Sub(record.SnapshotAt).Round(time.Second).String()
	record.Note = fmt.Sprintf("the owners were read once, from the extract snapshot taken at %s; the venue store was not read again at the end of the calls",
		record.SnapshotAt.Format(time.RFC3339))
	report.OwnershipSource = OwnershipSourceExtractFile
	report.OwnershipExtract = record
	return report, nil
}
