package contextfabric

import (
	"context"
	"testing"
)

// One turn resolves subjects once per answer reuse recheck batch (CHAOS-12463).
// Every resolver call of the turn sees the same prefix memo, so a work item key
// prefix is read once per turn, not once per call. A second turn gets its own.
func TestEveryResolverCallOfATurnSharesOnePrefixMemo(t *testing.T) {
	t.Parallel()
	var first any
	for turn := 0; turn < 2; turn++ {
		graph := &recheckGraph{pathRefs: map[string]string{}}
		runRecheck(t, graph, recheckCandidate(reuseSubjects(120))).requireHit(t)
		if len(graph.prefixMemos) < 3 {
			t.Fatalf("turn %d: resolver calls = %d, want one per batch of 120 subjects", turn, len(graph.prefixMemos))
		}
		for call, memo := range graph.prefixMemos {
			if memo == nil || memo != graph.prefixMemos[0] {
				t.Fatalf("turn %d: call %d saw memo %p, want the turn's one memo %p", turn, call+1, memo, graph.prefixMemos[0])
			}
		}
		if turn == 1 && graph.prefixMemos[0] == first {
			t.Fatal("two turns shared one memo")
		}
		first = graph.prefixMemos[0]
	}
}

// A memo keeps a read for the turn, and a ctx with no memo records nothing.
func TestAPrefixMemoKeepsAReadForTheTurnOnly(t *testing.T) {
	t.Parallel()
	ctx := WithWorkItemKeyPrefixMemo(context.Background())
	if WithWorkItemKeyPrefixMemo(ctx) != ctx {
		t.Fatal("a second attach replaced the turn's memo")
	}
	RecordWorkItemKeyPrefixRead(ctx, "k", WorkItemKeyPrefixRead{Seen: true, Read: true})
	if read, ok := WorkItemKeyPrefixReadFrom(ctx, "k"); !ok || !read.Seen || !read.Read {
		t.Fatalf("read = %+v, %v", read, ok)
	}
	if _, ok := WorkItemKeyPrefixReadFrom(WithWorkItemKeyPrefixMemo(context.Background()), "k"); ok {
		t.Fatal("a fresh memo holds another turn's read")
	}
	RecordWorkItemKeyPrefixRead(context.Background(), "k", WorkItemKeyPrefixRead{Read: true})
	if _, ok := WorkItemKeyPrefixReadFrom(context.Background(), "k"); ok {
		t.Fatal("a ctx with no memo holds a read")
	}
}
