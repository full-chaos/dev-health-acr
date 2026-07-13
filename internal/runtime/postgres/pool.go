package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultMaxOpenConns = 12
	defaultMaxIdleConns = 4
	defaultPingTimeout  = 5 * time.Second
	defaultConnMaxLife  = 30 * time.Minute
	defaultConnMaxIdle  = 5 * time.Minute
)

var ErrTransactionPooler = errors.New("PostgreSQL transaction pooler is not supported")

type Config struct {
	DSN             string
	PoolerAdminDSN  string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
}

func Open(ctx context.Context, config Config) (*sql.DB, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	parsed, err := pgx.ParseConfig(config.DSN)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	if config.PoolerAdminDSN != "" {
		if err := verifyPoolerMode(ctx, config.PoolerAdminDSN, config.PingTimeout); err != nil {
			return nil, err
		}
	}
	db := stdlib.OpenDB(*parsed)
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	pingContext, cancel := context.WithTimeout(ctx, config.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingContext); err != nil {
		db.Close()
		return nil, errors.New("PostgreSQL is unavailable")
	}
	return db, nil
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.DSN) == "" {
		return errors.New("PostgreSQL DSN is required")
	}
	if _, err := pgx.ParseConfig(c.DSN); err != nil {
		return errors.New("invalid PostgreSQL configuration")
	}
	if c.PoolerAdminDSN != "" {
		if _, err := pgx.ParseConfig(c.PoolerAdminDSN); err != nil {
			return errors.New("invalid PgBouncer administration configuration")
		}
	}
	if c.MaxOpenConns == 0 {
		c.MaxOpenConns = defaultMaxOpenConns
	}
	if c.MaxIdleConns == 0 {
		c.MaxIdleConns = defaultMaxIdleConns
	}
	if c.MaxOpenConns < 1 || c.MaxIdleConns < 0 || c.MaxIdleConns > c.MaxOpenConns {
		return fmt.Errorf("invalid PostgreSQL pool bounds")
	}
	if c.PingTimeout == 0 {
		c.PingTimeout = defaultPingTimeout
	}
	if c.ConnMaxLifetime == 0 {
		c.ConnMaxLifetime = defaultConnMaxLife
	}
	if c.ConnMaxIdleTime == 0 {
		c.ConnMaxIdleTime = defaultConnMaxIdle
	}
	if c.PingTimeout <= 0 || c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 {
		return errors.New("invalid PostgreSQL pool duration")
	}
	return nil
}

func verifyPoolerMode(ctx context.Context, dsn string, timeout time.Duration) error {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid PgBouncer administration configuration")
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := pgx.ConnectConfig(probeContext, config)
	if err != nil {
		return errors.New("PgBouncer pool mode connection could not be verified")
	}
	defer connection.Close(probeContext)
	rows, err := connection.Query(probeContext, "SHOW CONFIG")
	if err != nil {
		return errors.New("PgBouncer pool mode query could not be verified")
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil || len(values) < 2 {
			return errors.New("PgBouncer pool mode response body could not be verified")
		}
		key := fmt.Sprint(values[0])
		value := fmt.Sprint(values[1])
		if key != "pool_mode" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "session":
			return nil
		case "transaction", "statement":
			return ErrTransactionPooler
		default:
			return errors.New("PgBouncer pool mode is unsupported")
		}
	}
	if err := rows.Err(); err != nil {
		return errors.New("PgBouncer pool mode response completion could not be verified")
	}
	return errors.New("PgBouncer pool mode could not be verified")
}
