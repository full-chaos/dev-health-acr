package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
)

// Real-database cells of the lifecycle outbox publisher's claim. Lead-run.

type publishFixture struct {
	outboxFixture
	publisher *OutboxPublisher
	base      time.Time
}

func newPublishFixture(t *testing.T) publishFixture {
	t.Helper()
	f := newOutboxFixture(t)
	publisher, err := NewOutboxPublisher(f.db)
	require.NoError(t, err)
	return publishFixture{outboxFixture: f, publisher: publisher, base: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}

// add inserts one outbox row; age orders rows (a larger age is older).
func (f publishFixture) add(t *testing.T, aggregate string, revision int, age time.Duration) {
	t.Helper()
	eventType := "acr_credential.issued"
	if revision > 1 {
		eventType = "acr_credential.revoked"
	}
	payload, err := json.Marshal(map[string]any{"aggregate_id": aggregate, "payload": map[string]any{"revision": revision}})
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4, $5::jsonb, $6)`, credentialTestOrgID, aggregate, revision, eventType, string(payload), f.base.Add(-age))
	require.NoError(t, err)
}

func (f publishFixture) publishedAt(t *testing.T, aggregate string, revision int) sql.NullTime {
	t.Helper()
	var at sql.NullTime
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT published_at FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 AND revision = $2`, aggregate, revision).Scan(&at))
	return at
}

func ack(authprojection.Event) bool { return true }

func (f publishFixture) next(t *testing.T, held []string, send func(authprojection.Event) bool) (bool, authprojection.Event) {
	t.Helper()
	found, event, err := f.publisher.PublishNext(f.ctx, held, func(_ context.Context, e authprojection.Event) bool { return send(e) })
	require.NoError(t, err)
	return found, event
}

func TestPublishNext_TakesTheOldestEventAndMarksOnlyThatOneOnAnAck(t *testing.T) {
	f := newPublishFixture(t)
	f.add(t, "cred_pub_old", 1, 3*time.Hour)
	f.add(t, "cred_pub_new", 1, time.Hour)
	found, event := f.next(t, nil, ack)
	require.True(t, found)
	require.Equal(t, "cred_pub_old", event.AggregateID, "the oldest event is claimed first")
	require.Equal(t, 1, event.Revision)
	require.True(t, f.publishedAt(t, "cred_pub_old", 1).Valid)
	require.False(t, f.publishedAt(t, "cred_pub_new", 1).Valid, "only the claimed event is marked")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(event.Body, &decoded), "the body is the stored envelope")
	require.Equal(t, "cred_pub_old", decoded["aggregate_id"])
}

func TestPublishNext_ANonAckLeavesTheRowUnpublishedAndTheLockReleased(t *testing.T) {
	f := newPublishFixture(t)
	f.add(t, "cred_pub_nack", 1, time.Hour)
	found, _ := f.next(t, nil, func(authprojection.Event) bool { return false })
	require.True(t, found)
	require.False(t, f.publishedAt(t, "cred_pub_nack", 1).Valid, "a non-ack leaves the row unpublished")
	again, event := f.next(t, nil, ack)
	require.True(t, again, "the lock went with the rollback")
	require.Equal(t, "cred_pub_nack", event.AggregateID)
}

func TestPublishNext_NothingEligibleIsNotFound(t *testing.T) {
	f := newPublishFixture(t)
	found, _ := f.next(t, nil, func(authprojection.Event) bool { t.Fatal("send called with nothing claimed"); return false })
	require.False(t, found)
	f.add(t, "cred_pub_done", 1, time.Hour)
	f.next(t, nil, ack)
	found, _ = f.next(t, nil, func(authprojection.Event) bool { t.Fatal("a published event was claimed again"); return false })
	require.False(t, found)
}

func TestPublishNext_ARevisionNeverGoesBeforeTheEarlierUnpublishedOne(t *testing.T) {
	f := newPublishFixture(t)
	f.add(t, "cred_pub_order", 2, 2*time.Hour) // older created_at than revision 1: order is by revision, not time alone
	f.add(t, "cred_pub_order", 1, time.Hour)
	_, first := f.next(t, nil, ack)
	require.Equal(t, 1, first.Revision, "the earlier revision goes first")
	_, second := f.next(t, nil, ack)
	require.Equal(t, 2, second.Revision, "each revision is marked on its own ack")
}

func TestPublishNext_ALockedHeadKeepsItsLaterRevisionsIneligible(t *testing.T) {
	f := newPublishFixture(t)
	f.add(t, "cred_pub_head", 1, 3*time.Hour)
	f.add(t, "cred_pub_head", 2, 2*time.Hour)
	other := f.publisher
	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var locked int
	require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT revision FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pub_head' AND revision = 1 FOR UPDATE`).Scan(&locked))
	found, _, err := other.PublishNext(f.ctx, nil, func(context.Context, authprojection.Event) bool {
		t.Fatal("revision 2 was claimed while revision 1 was held by another pod")
		return true
	})
	require.NoError(t, err, "claim must skip a locked head, not wait on it")
	require.False(t, found)
	f.add(t, "cred_pub_other", 1, time.Hour)
	foundOther, event := f.next(t, nil, ack)
	require.True(t, foundOther)
	require.Equal(t, "cred_pub_other", event.AggregateID, "an unrelated aggregate still flows past the locked head")
}

func TestPublishNext_AHeldAggregateLeavesTheClaim(t *testing.T) {
	f := newPublishFixture(t)
	for i := 0; i < 3; i++ {
		f.add(t, fmt.Sprintf("cred_pub_poison%d", i), 1, time.Duration(10-i)*time.Hour)
	}
	f.add(t, "cred_pub_healthy", 1, time.Hour)
	held := []string{"cred_pub_poison0", "cred_pub_poison1", "cred_pub_poison2"}
	found, event := f.next(t, held, ack)
	require.True(t, found)
	require.Equal(t, "cred_pub_healthy", event.AggregateID, "a held aggregate is never claimed")
	found, _ = f.next(t, held, ack)
	require.False(t, found, "held aggregates are never claimed")
	found, event = f.next(t, held[:2], ack)
	require.True(t, found)
	require.Equal(t, "cred_pub_poison2", event.AggregateID)
}

func TestPublishNext_EachEventIsItsOwnTransaction(t *testing.T) {
	f := newPublishFixture(t)
	f.add(t, "cred_pub_one", 1, 3*time.Hour)
	f.add(t, "cred_pub_one", 2, 2*time.Hour)
	f.add(t, "cred_pub_two", 1, time.Hour)
	for i := 0; i < 3; i++ {
		found, _ := f.next(t, nil, func(authprojection.Event) bool {
			var committed int
			require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE published_at IS NOT NULL`).Scan(&committed))
			require.Equal(t, i, committed, "the events before this one are already committed while it is being sent")
			return true
		})
		require.True(t, found)
	}
}

func TestPublishNext_ALostCommitLeavesTheEventForAResend(t *testing.T) {
	f := newPublishFixture(t)
	f.add(t, "cred_pub_crash", 1, time.Hour)
	ctx, cancel := context.WithCancel(f.ctx)
	_, _, err := f.publisher.PublishNext(ctx, nil, func(context.Context, authprojection.Event) bool { cancel(); return true })
	require.Error(t, err)
	require.False(t, f.publishedAt(t, "cred_pub_crash", 1).Valid, "an ack that did not commit is re-sent")
	found, _ := f.next(t, nil, ack)
	require.True(t, found)
}

func TestPublishNext_ASlowOrDeadPublisherNeverBlocksACredentialMutation(t *testing.T) {
	f := newPublishFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("publish-nonblocking"))
	require.NoError(t, err)
	id := issued.Credential.CredentialID
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, _, err := f.publisher.PublishNext(f.ctx, nil, func(context.Context, authprojection.Event) bool {
			close(entered)
			<-release
			return false
		})
		done <- err
	}()
	<-entered
	mutated := make(chan error, 1)
	go func() {
		_, err := f.service.Revoke(f.ctx, credentialTestOrgID, id, credentialTestActorID)
		mutated <- err
	}()
	select {
	case err := <-mutated:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("a revoke waited on the publisher's claim")
	}
	_, err = f.service.Create(f.ctx, credentialCreateRequest("publish-nonblocking-2"))
	require.NoError(t, err)
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, 2, f.count(t, fmt.Sprintf(`SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = '%s'`, id)))
}
