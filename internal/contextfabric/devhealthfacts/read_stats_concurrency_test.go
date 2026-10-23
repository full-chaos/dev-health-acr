package devhealthfacts

import (
	"sync"
	"testing"
)

func TestReadStatsSumsStatementsReportedAtTheSameTime(t *testing.T) {
	t.Parallel()
	stats := &ReadStats{}
	var reporters sync.WaitGroup
	for range 8 {
		reporters.Add(1)
		go func() {
			defer reporters.Done()
			for range 1000 {
				stats.add(3, 5)
			}
		}()
	}
	reporters.Wait()
	rows, bytes := stats.Snapshot()
	if rows != 8*1000*3 || bytes != 8*1000*5 {
		t.Fatalf("rows=%d bytes=%d, want %d and %d", rows, bytes, 8*1000*3, 8*1000*5)
	}
}
