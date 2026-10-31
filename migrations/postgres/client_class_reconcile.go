package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

const reconcileClientClassVersion int64 = 63

// reconcileClientClassApplies is true once 0063 is in the history: before it
// the class CHECK refuses org_bearer.
func reconcileClientClassApplies(applied map[int64]struct{}) bool {
	_, found := applied[reconcileClientClassVersion]
	return found
}

// reconcileClientClass relabels operator credentials to org_bearer and returns
// the number relabelled. With no operator row it takes no table lock.
func reconcileClientClass(ctx context.Context, conn *sql.Conn) (int64, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin client class reconcile: %w", err)
	}
	defer tx.Rollback()
	var pending bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM acr.client_credentials WHERE client_class = 'operator')").Scan(&pending); err != nil {
		return 0, fmt.Errorf("check operator credentials: %w", err)
	}
	if !pending {
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx, reconcileClientClassSQL); err != nil {
		return 0, fmt.Errorf("relabel operator credentials: %w", err)
	}
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT current_setting('acr.reconcile_client_class_count')").Scan(&raw); err != nil {
		return 0, fmt.Errorf("read relabel count: %w", err)
	}
	relabelled, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse relabel count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit client class reconcile: %w", err)
	}
	return relabelled, nil
}
