package devhealthsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// A caught-up tick on a saved cursor runs the repository reconcile pass once
// per period. Its batches keep the cursor position and carry an ack; a second
// pass writes only the repositories whose row changed since the first.
func TestCaughtUpTicksReconcileRepositoriesOncePerPeriod(t *testing.T) {
	ctx := context.Background()
	const repositories = 250
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var rows [][]any
	for n := 0; n < repositories; n++ {
		rows = append(rows, []any{fmt.Sprintf("00000000-0000-4000-8000-%012d", n), fmt.Sprintf("example-org/repo-%03d", n), "synthetic", at, at, ""})
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM repos", rows: rows, cursorOf: repoCursorOf}}}
	source, err := newUnpacedClickHouseSource(client)
	if err != nil {
		t.Fatal(err)
	}
	now := at.Add(48 * time.Hour)
	source.SetClockForTest(func() time.Time { return now })
	saved := devhealthsource.IngestCursorForTest(at.Add(time.Hour), "")

	// pass drains one pass from cursor and returns the repository entities it
	// emitted, the reconcile reads it issued and the cursor it ended on.
	pass := func(cursor string) (map[string]bool, int, string) {
		t.Helper()
		reads := client.repositoryReconcileReads
		emitted := map[string]bool{}
		for i := 0; i < 10; i++ {
			batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return emitted, client.repositoryReconcileReads - reads, cursor
			}
			if keysetPosition(t, batch.NextCursor) != keysetPosition(t, saved) {
				t.Fatalf("a reconcile batch moved the cursor position (%q -> %q)", saved, batch.NextCursor)
			}
			if cursorAck(t, batch.NextCursor) == "" {
				t.Fatalf("a reconcile batch carries no ack: %q", batch.NextCursor)
			}
			for _, e := range batch.Entities {
				if e.Subject.Kind != contextfabric.SubjectRepository {
					t.Fatalf("a reconcile batch emitted a %s entity", e.Subject.Kind)
				}
				if e.Properties[contextfabric.ProjectionSyncedAtProperty].Integer == nil {
					t.Fatalf("a reconciled repository lacks the sync stamp the paged read writes: %+v", e.Properties)
				}
				emitted[e.Subject.CanonicalID] = true
			}
			cursor = batch.NextCursor
		}
		t.Fatal("the pass did not end")
		return nil, 0, ""
	}

	first, reads, cursor := pass(saved)
	if len(first) != repositories {
		t.Fatalf("the first pass of a process with a saved cursor emitted %d repositories, want %d", len(first), repositories)
	}
	if reads == 0 {
		t.Fatal("the first pass issued no repository reconcile read")
	}

	now = now.Add(repositoryReconcilePeriod - time.Second)
	if inside, reads, _ := pass(cursor); len(inside) != 0 || reads != 0 {
		t.Fatalf("a tick inside the period issued %d reconcile reads and emitted %d repositories", reads, len(inside))
	}

	rows[7][1] = "example-org/renamed"
	rows[7][3] = at.Add(10 * time.Minute)
	now = now.Add(time.Second)
	second, reads, _ := pass(cursor)
	if reads == 0 {
		t.Fatal("the pass after the period issued no repository reconcile read")
	}
	if len(second) != 1 || !second[contextfabric.RepositoryCanonicalID(rows[7][0].(string))] {
		t.Fatalf("the second pass emitted %d repositories, want only the changed one", len(second))
	}
}

// cursorAck is the ack a cursor carries.
func cursorAck(t *testing.T, cursor string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	var state struct {
		Ack string `json:"ack"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	return state.Ack
}
