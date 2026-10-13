package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

type refusingClient struct{ err error }

func (c refusingClient) Query(context.Context, string, []ClickHouseBinding) (ClickHouseRowScanner, error) {
	return nil, c.err
}

func TestMemoryCappedQueryClientCountsAMemoryLimitRefusalOnTheFirstBlock(t *testing.T) {
	t.Parallel()
	refusal := fmt.Errorf("query: %w", &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "memory limit"})
	var seen []MemoryNearCap
	client := NewMemoryCappedQueryClient(refusingClient{err: refusal}, 64<<20,
		WithMemoryNearCapObserver(func(_ context.Context, near MemoryNearCap) { seen = append(seen, near) }))
	rows, err := client.Query(context.Background(), "SELECT 1 SETTINGS max_memory_usage = 1000", nil)
	if rows != nil || !errors.Is(err, refusal) {
		t.Fatalf("caller got rows=%v err=%v, want the unchanged refusal", rows, err)
	}
	if len(seen) != 1 || !seen[0].PeakIsLowerBound || seen[0].PeakBytes != 1000 || seen[0].CapBytes != 1000 {
		t.Fatalf("observer saw %+v, want one near-cap with the statement's own cap as a lower bound", seen)
	}
}

func TestMemoryCappedQueryClientIgnoresOtherRefusals(t *testing.T) {
	t.Parallel()
	called := false
	for _, err := range []error{&clickhousedriver.Exception{Code: 60}, errors.New("dial failed")} {
		client := NewMemoryCappedQueryClient(refusingClient{err: err}, 64<<20,
			WithMemoryNearCapObserver(func(context.Context, MemoryNearCap) { called = true }))
		if _, got := client.Query(context.Background(), "SELECT 1", nil); !errors.Is(got, err) {
			t.Fatalf("error changed: %v", got)
		}
	}
	if called {
		t.Fatal("an error other than 241 must not count as near-cap")
	}
}

type refusedOnFirstReadScanner struct {
	ClickHouseRowScanner
	err error
}

func (s refusedOnFirstReadScanner) Next() bool   { return false }
func (s refusedOnFirstReadScanner) Err() error   { return s.err }
func (s refusedOnFirstReadScanner) Close() error { return nil }

type scanningClient struct{ scanner ClickHouseRowScanner }

func (c scanningClient) Query(context.Context, string, []ClickHouseBinding) (ClickHouseRowScanner, error) {
	return c.scanner, nil
}

func TestMemoryCappedQueryClientCountsAMemoryLimitRefusalRaisedWhileReadingTheFirstBlock(t *testing.T) {
	t.Parallel()
	refusal := &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "memory limit"}
	var seen []MemoryNearCap
	unobserved := 0
	client := NewMemoryCappedQueryClient(scanningClient{scanner: refusedOnFirstReadScanner{err: refusal}}, 64<<20,
		WithMemoryNearCapObserver(func(_ context.Context, near MemoryNearCap) { seen = append(seen, near) }),
		WithMemoryPeakUnobservedObserver(func(context.Context, string) { unobserved++ }))
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if got := rows.Err(); !errors.Is(got, refusal) {
		t.Fatalf("caller got %v, want the unchanged refusal", got)
	}
	_ = rows.Close()
	_ = rows.Close()
	if len(seen) != 1 || !seen[0].PeakIsLowerBound || seen[0].PeakBytes != 64<<20 || unobserved != 0 {
		t.Fatalf("observer saw %+v (unobserved %d), want one lower-bound near-cap and no unobserved count", seen, unobserved)
	}
}

type refusedOnCloseScanner struct {
	ClickHouseRowScanner
	err error
}

func (s refusedOnCloseScanner) Next() bool   { return false }
func (s refusedOnCloseScanner) Err() error   { return nil }
func (s refusedOnCloseScanner) Close() error { return s.err }

func TestMemoryCappedQueryClientCountsAMemoryLimitRefusalReturnedByClose(t *testing.T) {
	t.Parallel()
	refusal := &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "memory limit"}
	var seen []MemoryNearCap
	client := NewMemoryCappedQueryClient(scanningClient{scanner: refusedOnCloseScanner{err: refusal}}, 64<<20,
		WithMemoryNearCapObserver(func(_ context.Context, near MemoryNearCap) { seen = append(seen, near) }))
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if got := rows.Close(); !errors.Is(got, refusal) {
		t.Fatalf("Close returned %v, want the unchanged refusal", got)
	}
	if len(seen) != 1 || !seen[0].PeakIsLowerBound {
		t.Fatalf("observer saw %+v, want one lower-bound near-cap", seen)
	}
}
