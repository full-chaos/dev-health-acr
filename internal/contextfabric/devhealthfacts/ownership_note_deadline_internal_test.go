package devhealthfacts

import (
	"context"
	"testing"
	"time"
)

// The extra ownership read is bounded by a quarter of the provider's remaining
// time, so it cannot consume the deadline that the original failure raced.
func TestOwnershipNoteReadIsBoundedByTheRemainingDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	child, release := ownershipNoteContext(parent)
	defer release()
	deadline, ok := child.Deadline()
	if !ok || time.Until(deadline) > 150*time.Millisecond {
		t.Fatalf("child deadline = %v ok = %v, want at most a quarter of the parent's remaining time", time.Until(deadline), ok)
	}
	if _, unbounded := context.Background().Deadline(); unbounded {
		t.Fatal("background context carries a deadline")
	}
	free, release2 := ownershipNoteContext(context.Background())
	defer release2()
	if d, ok := free.Deadline(); !ok || time.Until(d) > ownershipNoteReadBudget {
		t.Fatalf("a context without a deadline must still be bounded by %v", ownershipNoteReadBudget)
	}
}
