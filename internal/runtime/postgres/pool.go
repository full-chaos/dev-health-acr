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
	MaxIdleConnsSet bool
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
	if config.PoolerAdminDSN != "" {
		probe := poolerProbe{adminDSN: config.PoolerAdminDSN, database: parsed.Database, user: parsed.User, timeout: config.PingTimeout}
		if err := verifyPoolerMode(ctx, probe); err != nil {
			db.Close()
			return nil, err
		}
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
	if c.MaxIdleConns == 0 && !c.MaxIdleConnsSet {
		c.MaxIdleConns = min(defaultMaxIdleConns, c.MaxOpenConns)
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

type poolerProbe struct {
	adminDSN string
	database string
	user     string
	timeout  time.Duration
}

type poolUserProbe struct {
	database string
	user     string
}

func verifyPoolerMode(ctx context.Context, probe poolerProbe) error {
	config, err := pgx.ParseConfig(probe.adminDSN)
	if err != nil {
		return errors.New("invalid PgBouncer administration configuration")
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	probeContext, cancel := context.WithTimeout(ctx, probe.timeout)
	defer cancel()
	connection, err := pgx.ConnectConfig(probeContext, config)
	if err != nil {
		return errors.New("PgBouncer pool mode connection could not be verified")
	}
	defer connection.Close(probeContext)
	effectiveUser, err := effectivePoolUser(probeContext, connection, poolUserProbe{database: probe.database, user: probe.user})
	if err != nil {
		return err
	}
	rows, err := connection.Query(probeContext, "SHOW POOLS")
	if err != nil {
		return errors.New("PgBouncer pool mode query could not be verified")
	}
	defer rows.Close()
	positions := map[string]int{}
	for index, field := range rows.FieldDescriptions() {
		positions[string(field.Name)] = index
	}
	databaseIndex, hasDatabase := positions["database"]
	userIndex, hasUser := positions["user"]
	modeIndex, hasMode := positions["pool_mode"]
	if !hasDatabase || !hasUser || !hasMode {
		return errors.New("PgBouncer effective pool mode could not be verified")
	}
	matches := 0
	mode := ""
	for rows.Next() {
		values, err := rows.Values()
		if err != nil || len(values) <= max(databaseIndex, userIndex, modeIndex) {
			return errors.New("PgBouncer pool mode response body could not be verified")
		}
		if poolerValue(values[databaseIndex]) != probe.database || poolerValue(values[userIndex]) != effectiveUser {
			continue
		}
		matches++
		mode = strings.ToLower(strings.TrimSpace(fmt.Sprint(values[modeIndex])))
	}
	if err := rows.Err(); err != nil {
		return errors.New("PgBouncer pool mode response completion could not be verified")
	}
	if matches != 1 {
		return errors.New("PgBouncer effective pool mode could not be verified")
	}
	switch mode {
	case "session":
		return nil
	case "transaction", "statement":
		return ErrTransactionPooler
	default:
		return errors.New("PgBouncer pool mode is unsupported")
	}
}

func effectivePoolUser(ctx context.Context, connection *pgx.Conn, probe poolUserProbe) (string, error) {
	rows, err := connection.Query(ctx, "SHOW DATABASES")
	if err != nil {
		return "", errors.New("PgBouncer database configuration could not be verified")
	}
	defer rows.Close()
	positions := map[string]int{}
	for index, field := range rows.FieldDescriptions() {
		positions[string(field.Name)] = index
	}
	nameIndex, hasName := positions["name"]
	forceUserIndex, hasForceUser := positions["force_user"]
	if !hasName || !hasForceUser {
		return "", errors.New("PgBouncer database configuration could not be verified")
	}
	matches := 0
	forcedUser := ""
	for rows.Next() {
		values, err := rows.Values()
		if err != nil || len(values) <= max(nameIndex, forceUserIndex) {
			return "", errors.New("PgBouncer database configuration could not be verified")
		}
		if poolerValue(values[nameIndex]) != probe.database {
			continue
		}
		matches++
		forcedUser = poolerValue(values[forceUserIndex])
	}
	if rows.Err() != nil || matches != 1 {
		return "", errors.New("PgBouncer database configuration could not be verified")
	}
	if forcedUser != "" {
		return forcedUser, nil
	}
	return probe.user, nil
}

func poolerValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
