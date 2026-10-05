package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	webAssertionReplayStatementTimeout = 2 * time.Second
	// A used id is needed until its assertion can no longer verify: exp plus
	// the 5 s clock skew the verifier accepts. The sweep keeps a minute past
	// exp, measured on the database clock, so pod clock skew up to ~55 s
	// cannot drop a row early.
	webAssertionReplayRetention = time.Minute
	webAssertionReplaySweepEach = 16
	webAssertionReplaySweepRows = 100
)

const insertWebAssertionReplaySQL = `
INSERT INTO acr.web_assertion_replays (issuer, jti, expires_at)
VALUES ($1, $2, $3)
ON CONFLICT (issuer, jti) DO NOTHING`

// The sweep is one statement and holds no lock after it ends. It does not use
// FOR UPDATE (that needs UPDATE on the table, which the runtime role does not
// hold). Two pods that pick the same rows lock them in ctid order, so one waits
// briefly and then deletes nothing; a failed sweep is logged and ignored.
const sweepWebAssertionReplaysSQL = `
DELETE FROM acr.web_assertion_replays
WHERE ctid = ANY (ARRAY(
    SELECT ctid FROM acr.web_assertion_replays
    WHERE expires_at < now() - make_interval(secs => $1::double precision)
    ORDER BY expires_at
    LIMIT $2
))`

// WebAssertionReplayStore records used web-assertion ids in PostgreSQL so every
// acr-api pod shares one record.
type WebAssertionReplayStore struct {
	db      *sql.DB
	logger  *slog.Logger
	counter atomic.Uint64
}

func NewWebAssertionReplayStore(db *sql.DB, logger *slog.Logger) (*WebAssertionReplayStore, error) {
	if db == nil {
		return nil, errors.New("web assertion replay store: database is required")
	}
	return &WebAssertionReplayStore{db: db, logger: logger}, nil
}

// Observe records (issuer, jti). It reports replay=true when the pair was
// already recorded. Any database error is returned and the caller must refuse
// the assertion. A failed expiry sweep never changes the answer.
func (s *WebAssertionReplayStore) Observe(ctx context.Context, issuer, jti string, expiresAt, _ time.Time) (bool, error) {
	statementCtx, cancel := context.WithTimeout(ctx, webAssertionReplayStatementTimeout)
	result, err := s.db.ExecContext(statementCtx, insertWebAssertionReplaySQL, issuer, jti, expiresAt.UTC())
	cancel()
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if s.counter.Add(1)%webAssertionReplaySweepEach == 0 {
		s.sweep(ctx)
	}
	return inserted == 0, nil
}

func (s *WebAssertionReplayStore) sweep(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webAssertionReplayStatementTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(sweepCtx, sweepWebAssertionReplaysSQL, webAssertionReplayRetention.Seconds(), webAssertionReplaySweepRows); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "web assertion replay sweep failed; ignored", "class", "web_assertion_replay_sweep")
	}
}
