package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
)

func TestTheClaimStatementHasTheDesignedShape(t *testing.T) {
	for _, want := range []string{
		"published_at IS NULL",
		"o.aggregate_id <> ALL($1::text[])",
		"NOT EXISTS",
		"p.aggregate_id = o.aggregate_id AND p.revision < o.revision AND p.published_at IS NULL",
		"ORDER BY o.created_at, o.aggregate_id, o.revision",
		"LIMIT 1",
		"FOR UPDATE OF o SKIP LOCKED",
	} {
		if !strings.Contains(LifecycleOutboxClaimSQL, want) {
			t.Errorf("the claim lacks %q:\n%s", want, LifecycleOutboxClaimSQL)
		}
	}
	if strings.Count(LifecycleOutboxClaimSQL, "LIMIT") != 1 || strings.Contains(LifecycleOutboxClaimSQL, "LIMIT $") {
		t.Errorf("the claim takes ONE event: %s", LifecycleOutboxClaimSQL)
	}
}

func TestTheMarkStatementSetsOnlyPublishedAtOfTheClaimedRowOnce(t *testing.T) {
	for _, want := range []string{"SET published_at = now()", "aggregate_id = $1", "revision = $2", "published_at IS NULL"} {
		if !strings.Contains(LifecycleOutboxMarkSQL, want) {
			t.Errorf("the mark lacks %q: %s", want, LifecycleOutboxMarkSQL)
		}
	}
	if strings.Count(LifecycleOutboxMarkSQL, "SET ") != 1 || strings.Contains(LifecycleOutboxMarkSQL, ",") {
		t.Errorf("the mark sets one column: %s", LifecycleOutboxMarkSQL)
	}
}

func TestTheClaimTimeoutsSitAboveTheRequestTimeout(t *testing.T) {
	request := authprojection.RequestTimeout.Milliseconds()
	if outboxPublishIdleInTxTimeoutMS <= request || outboxPublishStatementTimeoutMS <= 0 {
		t.Fatalf("idle-in-transaction %d ms must exceed the %d ms request: the transaction is idle while the POST runs", outboxPublishIdleInTxTimeoutMS, request)
	}
	if time.Duration(outboxPublishIdleInTxTimeoutMS)*time.Millisecond > time.Minute {
		t.Fatal("the idle limit bounds the hold on the row lock and the connection")
	}
}

func TestPublishNextRefusesAnUnusableStore(t *testing.T) {
	if _, err := NewOutboxPublisher(nil); err == nil {
		t.Fatal("a nil database was accepted")
	}
	var p *OutboxPublisher
	if _, _, err := p.PublishNext(nil, nil, func(_ context.Context, _ authprojection.Event) bool { return true }); err == nil {
		t.Fatal("a nil publisher published")
	}
}
