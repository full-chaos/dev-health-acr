package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTheReapStatementHasTheDesignedShape(t *testing.T) {
	for _, want := range []string{
		"GROUP BY aggregate_id",
		"bool_and(published_at IS NOT NULL)",
		"max(published_at) < $1",
		"NOT EXISTS (SELECT 1 FROM acr.client_credentials c WHERE c.credential_id = a.aggregate_id)",
		"ORDER BY a.last_published, a.aggregate_id",
		"LIMIT $2",
		"DELETE FROM acr.credential_lifecycle_outbox d",
		"d.aggregate_id = doomed.aggregate_id",
	} {
		if !strings.Contains(LifecycleOutboxReapSQL, want) {
			t.Errorf("the reap statement lacks %q:\n%s", want, LifecycleOutboxReapSQL)
		}
	}
}

// A terminal row is not what decides a reap: an unrevoked credential deleted by a purge gets no terminal event, so its
// aggregate must be reaped on its published rows, its age and the absence of its credential row alone.
func TestTheReapStatementNeverAsksForATerminalEvent(t *testing.T) {
	for _, banned := range []string{"event_type", "acr_credential.", "expired", "revoked"} {
		if strings.Contains(LifecycleOutboxReapSQL, banned) {
			t.Errorf("the reap statement names %q: no row may be required to end terminal", banned)
		}
	}
}

func TestTheReapStatementBindsBothParametersOnce(t *testing.T) {
	for _, parameter := range []string{"$1", "$2"} {
		if got := strings.Count(LifecycleOutboxReapSQL, parameter); got != 1 {
			t.Errorf("%s is used %d times, want 1", parameter, got)
		}
	}
	if strings.Contains(LifecycleOutboxReapSQL, "$3") {
		t.Error("the reap statement takes two parameters")
	}
}

func TestTheReapStatementDeletesWholeAggregatesOnly(t *testing.T) {
	if strings.Contains(LifecycleOutboxReapSQL, "WHERE published_at <") || strings.Contains(LifecycleOutboxReapSQL, "WHERE d.published_at") {
		t.Error("a per-row delete would break revision continuity (MaxRevision and the terminal rule read the outbox)")
	}
	if !strings.Contains(LifecycleOutboxReapSQL, "RETURNING d.aggregate_id") {
		t.Error("the statement reports the aggregates and rows it deleted")
	}
}

func TestReapPublishedRefusesAnUnusableStoreAndBounds(t *testing.T) {
	if _, err := NewOutboxReaper(nil); err == nil {
		t.Fatal("a nil database was accepted")
	}
	var r *OutboxReaper
	if _, err := r.ReapPublished(context.Background(), time.Now(), 10); err == nil {
		t.Fatal("a nil reaper reaped")
	}
	reaper := &OutboxReaper{}
	if _, err := reaper.ReapPublished(context.Background(), time.Now(), 10); err == nil {
		t.Fatal("a reaper with no database reaped")
	}
	for _, limit := range []int{0, -1} {
		if _, err := (&OutboxReaper{db: nil}).ReapPublished(context.Background(), time.Now(), limit); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
	}
	if _, err := (&OutboxReaper{}).ReapPublished(context.Background(), time.Time{}, 10); err == nil {
		t.Fatal("a zero cutoff was accepted: it would reap nothing silently or everything by mistake")
	}
}

func TestTheReapTimeoutBoundsOneStatement(t *testing.T) {
	if outboxReapStatementTimeoutMS <= 0 || time.Duration(outboxReapStatementTimeoutMS)*time.Millisecond > 5*time.Minute {
		t.Fatalf("statement timeout %d ms must be set and bounded", outboxReapStatementTimeoutMS)
	}
}
