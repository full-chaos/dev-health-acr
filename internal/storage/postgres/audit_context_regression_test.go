package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestAuditStore_RecordRejectsTypedNilContext(t *testing.T) {
	// Given
	db := newCredentialStoreDatabase(t, context.Background())
	store, err := NewAuditStore(db)
	require.NoError(t, err)
	var ctx *typedNilContext

	// When
	require.NotPanics(t, func() {
		err = store.Record(ctx, storage.AuditEvent{CreatedAt: time.Now().UTC()})
	})

	// Then
	require.ErrorIs(t, err, storage.ErrInvalidCredentialLifecycle)
}

type typedNilContext struct{}

func (*typedNilContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*typedNilContext) Done() <-chan struct{}       { return nil }
func (*typedNilContext) Err() error                  { return nil }
func (*typedNilContext) Value(any) any               { return nil }
