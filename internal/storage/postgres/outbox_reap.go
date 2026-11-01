package postgres

import (
	"context"
	"database/sql"
	"time"
)

const LifecycleOutboxReapSQL = "SELECT 1"

type ReapResult struct{ Aggregates, Rows int }

type OutboxReaper struct{ db *sql.DB }

func NewOutboxReaper(db *sql.DB) (*OutboxReaper, error) { return &OutboxReaper{db: db}, nil }

func (r *OutboxReaper) ReapPublished(context.Context, time.Time, int) (ReapResult, error) {
	return ReapResult{}, nil
}

const outboxReapStatementTimeoutMS = 0
