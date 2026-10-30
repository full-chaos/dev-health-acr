package api

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-11948 integration: the thread routes over two App instances, each
// with its own pool and *pginvestigation.Store, on one real PostgreSQL.

// newPostgresThreadHarness is two App instances over two pools whose stores
// read the harness clock (Get's closed-thread rule).
func newPostgresThreadHarness(t *testing.T, dsn string) *submissionHarness {
	t.Helper()
	var h *submissionHarness
	clock := func() time.Time { return h.clock.Now() }
	var stores []submissionTestStore
	for range 2 {
		store, err := pginvestigation.NewStore(openSubmissionPool(t, dsn), pginvestigation.WithClock(clock))
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	h = newSubmissionHarness(t, stores...)
	return h
}

func TestThreadRoutesOnPostgresAcrossTwoInstances(t *testing.T) {
	dsn, _ := submissionPostgres(t)
	runThreadRoutesOnPostgres(t, dsn)
}

// runThreadRoutesOnPostgres is the integration body, on a migrated database.
func runThreadRoutesOnPostgres(t *testing.T, dsn string) {
	t.Run("turns on both instances build one thread; reads and delete agree", func(t *testing.T) {
		h := newPostgresThreadHarness(t, dsn)
		thread := "thread-11948-pg-one"
		servedResultID(t, h.turnOn(0, h.tokenA, "submission-11948-pg-0001", thread, "first", ""))
		h.clock.Advance(time.Minute)
		servedResultID(t, h.turnOn(1, h.tokenA, "submission-11948-pg-0002", thread, "second", "30"))
		for app := range 2 {
			got := readThreadBody(t, h.threadCallOn(app, http.MethodGet, h.tokenA, threadPath(thread)))
			if got.TurnCount != 2 || got.RetentionDays != contractsv1.ContextFabricThreadRetentionMonth || got.Turns[0].Question != "first" || got.Turns[1].Question != "second" {
				t.Fatalf("app %d thread = %+v", app, got)
			}
		}
		// Another principal sees nothing of it on either instance.
		for app := range 2 {
			expectErrorCode(t, h.threadCallOn(app, http.MethodGet, h.tokenB, threadPath(thread)), http.StatusNotFound, "not_found")
		}
		if response := h.threadCallOn(1, http.MethodDelete, h.tokenA, threadPath(thread)); response.Code != http.StatusNoContent {
			t.Fatalf("delete = %d", response.Code)
		}
		expectErrorCode(t, h.threadCallOn(0, http.MethodGet, h.tokenA, threadPath(thread)), http.StatusNotFound, "not_found")
		expectErrorCode(t, h.turnOn(0, h.tokenA, "submission-11948-pg-0003", thread, "third", ""), http.StatusConflict, contractsv1.ErrorCodeThreadClosed)
		expectErrorCode(t, h.turnOn(1, h.tokenA, "submission-11948-pg-0001", thread, "first", ""), http.StatusConflict, contractsv1.ErrorCodeThreadClosed)
		expectErrorCode(t, h.status(0, h.tokenA, "submission-11948-pg-0002"), http.StatusNotFound, "not_found")
	})

	t.Run("concurrent turns on both instances count exactly", func(t *testing.T) {
		h := newPostgresThreadHarness(t, dsn)
		thread := "thread-11948-pg-race"
		const turns = 4
		var wg sync.WaitGroup
		codes := make([]int, turns)
		for i := range turns {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = h.turnOn(i%2, h.tokenA, fmt.Sprintf("submission-11948-race-%d", i), thread, fmt.Sprintf("q%d", i), "").Code
			}(i)
		}
		wg.Wait()
		admitted := 0
		for _, code := range codes {
			if code == http.StatusOK {
				admitted++
			}
		}
		got := readThreadBody(t, h.threadCallOn(0, http.MethodGet, h.tokenA, threadPath(thread)))
		if got.TurnCount != admitted || len(got.Turns) != admitted {
			t.Fatalf("thread counts %d turns (%d served), %d admitted (codes %v)", got.TurnCount, len(got.Turns), admitted, codes)
		}
	})
}
