package contextfabric

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-11948: the thread row rules every store applies.
func TestThreadRowRules(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	key := ThreadKey{OrgID: "org", Owner: SubmissionOwner{AuthenticationMethod: "web_assertion", Subject: "u"}, ThreadID: "thread-0001"}
	month := contractsv1.ContextFabricThreadRetentionMonth
	day := contractsv1.ContextFabricThreadRetentionDay
	at := func(minutes int, id string) ThreadTurnCursor {
		return ThreadTurnCursor{AdmittedAt: start.Add(time.Duration(minutes) * time.Minute), SubmissionID: id}
	}

	first := AdvanceThread(nil, key, at(0, "s1"), nil)
	if first.TurnCount != 1 || first.RetentionDays != day || first.RetentionSetBy != nil || !first.ExpiresAt.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("first turn = %+v", first)
	}
	second := AdvanceThread(&first, key, at(2, "s2"), &month)
	if second.TurnCount != 2 || second.RetentionDays != month || !second.LastTurnAt.Equal(start.Add(2*time.Minute)) ||
		!second.ExpiresAt.Equal(start.Add(2*time.Minute).AddDate(0, 0, 30)) {
		t.Fatalf("second turn = %+v", second)
	}
	// An earlier turn (another replica's clock) neither moves last_turn_at
	// back nor overrides the later turn's retention; it can move created_at.
	early := AdvanceThread(&second, key, at(-1, "s0"), &day)
	if early.TurnCount != 3 || early.RetentionDays != month || !early.LastTurnAt.Equal(second.LastTurnAt) || !early.CreatedAt.Equal(start.Add(-time.Minute)) {
		t.Fatalf("early turn = %+v", early)
	}
	// The same instant breaks ties by submission id.
	tie := AdvanceThread(&second, key, at(2, "s3"), &day)
	if tie.RetentionDays != day {
		t.Fatalf("a later submission id at the same instant must decide: %+v", tie)
	}
	if first.ClosedReason(start) != "" {
		t.Fatal("an open thread reports a closure")
	}
	if first.ClosedReason(first.ExpiresAt) != ThreadClosedExpired || first.ClosedReason(first.ExpiresAt.Add(-time.Nanosecond)) != "" {
		t.Fatal("expiry is not exactly at expires_at")
	}
	closedAt := start
	deleted := first
	deleted.ClosedAt = &closedAt
	if deleted.ClosedReason(start) != ThreadClosedDeleted || !deleted.Closed(start) {
		t.Fatal("a deleted thread is not closed")
	}
	full := first
	full.TurnCount = contractsv1.ContextFabricThreadMaxTurnCount
	if full.ClosedReason(start) != ThreadClosedFull || full.Closed(start) {
		t.Fatal("a full thread refuses turns (ClosedReason) but stays readable (Closed)")
	}
	full.TurnCount--
	if full.ClosedReason(start) != "" {
		t.Fatal("one turn below the cap is full")
	}
}
