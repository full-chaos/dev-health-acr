package postgres

import (
	"context"
	"database/sql"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
)

const (
	LifecycleOutboxClaimSQL = "SELECT 1"
	LifecycleOutboxMarkSQL  = "SELECT 1"

	outboxPublishStatementTimeoutMS = 0
	outboxPublishIdleInTxTimeoutMS  = 0
)

type OutboxPublisher struct{ db *sql.DB }

func NewOutboxPublisher(db *sql.DB) (*OutboxPublisher, error) { return &OutboxPublisher{db: db}, nil }

func (p *OutboxPublisher) PublishNext(context.Context, []string, func(context.Context, authprojection.Event) bool) (bool, authprojection.Event, error) {
	return false, authprojection.Event{}, nil
}
