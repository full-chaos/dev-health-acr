package contextfabric

import (
	"context"
	"sync"
)

type workItemKeyPrefixMemoKey struct{}

// WorkItemKeyPrefixRead is the outcome of one work item key prefix read
// (CHAOS-12463). Read is false for a read that could not run, or whose grant
// did not apply: the key it gates stays unread.
type WorkItemKeyPrefixRead struct {
	Seen bool
	Read bool
}

// workItemKeyPrefixMemo holds one turn's prefix reads, keyed by the caller's
// organization, census grant and prefix. It lives on the turn's context only:
// nothing carries it to another turn.
type workItemKeyPrefixMemo struct {
	mu    sync.Mutex
	reads map[string]WorkItemKeyPrefixRead
}

// WithWorkItemKeyPrefixMemo attaches a fresh memo to ctx. Investigate attaches
// it before any resolution runs. One turn resolves subjects more than once: the
// main pass, the offers-only pass, each answer reuse recheck batch and the group
// read. Each resolver call binds the question's handles, and the memo keeps that
// to one read of each prefix for the turn. A ctx that already carries a memo
// keeps it.
func WithWorkItemKeyPrefixMemo(ctx context.Context) context.Context {
	if _, ok := ctx.Value(workItemKeyPrefixMemoKey{}).(*workItemKeyPrefixMemo); ok {
		return ctx
	}
	return context.WithValue(ctx, workItemKeyPrefixMemoKey{}, &workItemKeyPrefixMemo{reads: map[string]WorkItemKeyPrefixRead{}})
}

// WorkItemKeyPrefixReadFrom returns the turn's read for key, when ctx carries a
// memo that holds one.
func WorkItemKeyPrefixReadFrom(ctx context.Context, key string) (WorkItemKeyPrefixRead, bool) {
	memo, ok := ctx.Value(workItemKeyPrefixMemoKey{}).(*workItemKeyPrefixMemo)
	if !ok {
		return WorkItemKeyPrefixRead{}, false
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	read, found := memo.reads[key]
	return read, found
}

// RecordWorkItemKeyPrefixRead keeps a read for the rest of the turn. A no-op
// when ctx carries no memo.
func RecordWorkItemKeyPrefixRead(ctx context.Context, key string, read WorkItemKeyPrefixRead) {
	if memo, ok := ctx.Value(workItemKeyPrefixMemoKey{}).(*workItemKeyPrefixMemo); ok {
		memo.mu.Lock()
		memo.reads[key] = read
		memo.mu.Unlock()
	}
}
