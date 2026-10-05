package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// wideReadRecorder answers the pull request wide reads from stored rows, by
// the key and version each statement's bindings name, and records the
// statements.
type wideReadRecorder struct {
	stored     map[string]time.Time // "repo:number" -> stored final version
	statements []string
	named      []int
}

func (w *wideReadRecorder) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	w.statements = append(w.statements, statement)
	bound := map[string]any{}
	for _, b := range bindings {
		bound[b.Name] = b.Value
	}
	var rows [][]any
	named := 0
	for i := 0; ; i++ {
		repo, ok := bound[fmt.Sprintf("r%d", i)].(string)
		if !ok {
			break
		}
		named++
		number := bound[fmt.Sprintf("n%d", i)].(uint32)
		version := bound[fmt.Sprintf("v%d", i)].(time.Time)
		if stored, ok := w.stored[fmt.Sprintf("%s:%d", repo, number)]; ok && stored.Equal(version) {
			rows = append(rows, []any{repo, number, "title", "open", version, version, uint8(0), time.Time{}, "branch", "body"})
		}
	}
	w.named = append(w.named, named)
	return &rowsScanner{rows: rows}, nil
}

type rowsScanner struct {
	rows [][]any
	at   int
}

func (r *rowsScanner) Next() bool { r.at++; return r.at <= len(r.rows) }
func (r *rowsScanner) Err() error { return nil }
func (r *rowsScanner) Close() error {
	return nil
}
func (r *rowsScanner) Scan(dest ...any) error {
	row := r.rows[r.at-1]
	for i, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = row[i].(string)
		case *uint32:
			*v = row[i].(uint32)
		case *uint8:
			*v = row[i].(uint8)
		case *time.Time:
			*v = row[i].(time.Time)
		default:
			return fmt.Errorf("unsupported destination %T", d)
		}
	}
	return nil
}

// TestThePullRequestWideReadNamesAFewRowsFromTheChosenVersion: the wide
// columns are read a few rows per statement, without FINAL, each row by its
// key AND the version the page chose; a row whose version moved between the
// two reads is left for the next page, and the rows come back in page order
// with the slug the page read.
func TestThePullRequestWideReadNamesAFewRowsFromTheChosenVersion(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const repo = "86830000-0000-4000-8000-000000000701"
	var keys []pullRequestPageKey
	stored := map[string]time.Time{}
	for n := uint32(9); n >= 1; n-- {
		version := base.Add(time.Duration(n) * time.Second)
		keys = append(keys, pullRequestPageKey{repoID: repo, repoSlug: "acme/r01", number: n, version: version})
		stored[fmt.Sprintf("%s:%d", repo, n)] = version
	}
	// Row 5 was synced again after the page read it.
	stored[fmt.Sprintf("%s:5", repo)] = base.Add(time.Hour)
	recorder := &wideReadRecorder{stored: stored}
	candidates, err := readPullRequestRows(context.Background(), recorder, "org-1", keys, scanPullRequestRow)
	if err != nil {
		t.Fatal(err)
	}
	perStatement := pullRequestWideReadRows(context.Background())
	if perStatement != 5 {
		t.Fatalf("rows per wide statement under the 64 MiB client default and 10 MiB granules = %d, want 5", perStatement)
	}
	if len(recorder.statements) != 2 {
		t.Fatalf("%d wide statements for 9 rows, want 2 of at most %d rows", len(recorder.statements), perStatement)
	}
	for i, statement := range recorder.statements {
		if recorder.named[i] > perStatement {
			t.Errorf("statement %d names %d rows, want at most %d", i, recorder.named[i], perStatement)
		}
		if strings.Contains(statement, "FINAL") || !strings.Contains(statement, "p.last_synced = {v0:DateTime64(3, 'UTC')}") || !strings.Contains(statement, "PREWHERE") {
			t.Errorf("statement %d does not read the chosen version without FINAL under PREWHERE:\n%s", i, statement)
		}
	}
	var order []string
	for _, c := range candidates {
		if c.entity == nil {
			continue
		}
		order = append(order, c.sortKey)
		if got := c.entity.Properties["repo"].String; got == nil || *got != "acme/r01" {
			t.Errorf("%s: repo property %v, want the slug the page read", c.sortKey, got)
		}
	}
	want := []string{}
	for _, key := range keys {
		if key.number != 5 {
			want = append(want, fmt.Sprintf("%s:%d", repo, key.number))
		}
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("rows %v, want %v (page order, row 5 left for the next page)", order, want)
	}

	// A recorded 3 MiB limit over 512 KiB granules allows 4 rows a statement.
	defer func(granule uint64) { pullRequestGranuleBytes = granule }(pullRequestGranuleBytes)
	pullRequestGranuleBytes = 512 << 10
	if got := pullRequestWideReadRows(withReadByteLimit(context.Background(), 3<<20)); got != 4 {
		t.Fatalf("rows per wide statement under 3 MiB and 512 KiB granules = %d, want 4", got)
	}
}
