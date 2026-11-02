package contextpacket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

// CHAOS-12683: ClickHouse raises code 241 for two different limits: the
// statement's own max_memory_usage ("(for query)") and the server's total
// ("(total)"). askdev-1 logged a server-total refusal of a 11 MB statement as
// "statement memory near cap" with peak = the statement's 256 MiB cap, which
// read as "this statement hit its own cap". A server-total refusal must name
// the server limit and never print the statement's cap as its peak.
func TestAServerTotalRefusalIsNotReportedAsTheStatementsCap(t *testing.T) {
	t.Parallel()
	total := &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "(total) memory limit exceeded: would use 1.80 GiB (attempt to allocate chunk of 4194304 bytes), current RSS 1.68 GiB, maximum: 1.80 GiB"}
	query := &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "Memory limit (for query) exceeded: would use 9.54 MiB (attempt to allocate chunk of 4194304 bytes), maximum: 9.54 MiB"}
	for _, tc := range []struct {
		name        string
		err         error
		serverTotal bool
		peak        uint64
		message     string
	}{
		{"server total, on the first block", fmt.Errorf("query: %w", total), true, 0, "clickhouse server memory limit refused a statement"},
		{"the statement's own cap", fmt.Errorf("query: %w", query), false, 1000, "clickhouse statement memory near cap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen []MemoryNearCap
			var buf bytes.Buffer
			slogObserver := NewSlogMemoryNearCapObserver(slog.New(slog.NewJSONHandler(&buf, nil)), nil)
			client := NewMemoryCappedQueryClient(refusingClient{err: tc.err}, 64<<20,
				WithMemoryNearCapObserver(func(ctx context.Context, near MemoryNearCap) { seen = append(seen, near); slogObserver(ctx, near) }))
			if _, err := client.Query(context.Background(), "SELECT 1 SETTINGS max_memory_usage = 1000", nil); !errors.Is(err, tc.err) {
				t.Fatalf("caller got %v, want the unchanged refusal", err)
			}
			if len(seen) != 1 || seen[0].ServerTotal != tc.serverTotal || seen[0].PeakBytes != tc.peak || seen[0].CapBytes != 1000 {
				t.Fatalf("observer saw %+v, want one with server_total=%v peak=%d cap=1000", seen, tc.serverTotal, tc.peak)
			}
			line := buf.String()
			if !strings.Contains(line, `"msg":"`+tc.message+`"`) {
				t.Fatalf("line = %s, want msg %q", line, tc.message)
			}
			if tc.serverTotal && strings.Contains(line, `"peak_bytes":1000`) {
				t.Fatalf("a server-total line prints the statement's cap as its peak: %s", line)
			}
		})
	}
}

// The same rule when the refusal arrives while reading the first block.
func TestAServerTotalRefusalWhileReadingIsNotReportedAsTheStatementsCap(t *testing.T) {
	t.Parallel()
	total := &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "(total) memory limit exceeded: would use 1.80 GiB, maximum: 1.80 GiB"}
	var seen []MemoryNearCap
	client := NewMemoryCappedQueryClient(scanningClient{scanner: refusedOnFirstReadScanner{err: total}}, 64<<20,
		WithMemoryNearCapObserver(func(_ context.Context, near MemoryNearCap) { seen = append(seen, near) }))
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if got := rows.Err(); !errors.Is(got, total) {
		t.Fatalf("caller got %v, want the unchanged refusal", got)
	}
	_ = rows.Close()
	if len(seen) != 1 || !seen[0].ServerTotal || seen[0].PeakBytes == 64<<20 {
		t.Fatalf("observer saw %+v, want one server-total refusal that does not print the cap as its peak", seen)
	}
}
