package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultMaxOpenConns = 12
	defaultMaxIdleConns = 4
	defaultPingTimeout  = 5 * time.Second
	defaultConnMaxLife  = 30 * time.Minute
	defaultConnMaxIdle  = 5 * time.Minute
)

// ErrUnavailable is returned (by identity, message unchanged) when the
// PostgreSQL ping fails; callers classify with errors.Is, never by text.
var ErrUnavailable = errors.New("PostgreSQL is unavailable")

// ErrRejected is returned when a PostgreSQL server ANSWERED the connection
// attempt with a permanent error (authentication failed, database missing,
// role not permitted, ...). See classifyPingError for the retryable answers.
// Callers must not retry this.
var ErrRejected = errors.New("PostgreSQL rejected the connection")

// classifyPingError: retryable (ErrUnavailable) means the server is not yet
// usable, decided POSITIVELY:
//   - no server answer: a transport failure -- dial, connection refused or
//     reset, DNS, a read/write/ping deadline, or the connection closing mid
//     startup (EOF);
//   - a server answer of SQLSTATE class 08 (connection exception), class 53
//     (insufficient resources, e.g. 53300 too many connections) or exactly
//     57P03 (cannot_connect_now: starting up).
//
// Everything else is terminal (ErrRejected): every other *pgconn.PgError
// (28xxx auth, 3D000 missing database, 42xxx, other 57xxx, ...) AND any
// non-transport error that is not a server answer either (the server refusing
// TLS negotiation, a TLS alert or certificate failure, a malformed or
// unexpected protocol response) -- retrying a persistent configuration or
// protocol incompatibility cannot fix it. The cause text is dropped on
// purpose: it can carry role and database names.
func classifyPingError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) >= 2 && (pgErr.Code[:2] == "08" || pgErr.Code[:2] == "53" || pgErr.Code == "57P03") {
			return ErrUnavailable
		}
		return ErrRejected
	}
	if isTransportFailure(err) {
		return ErrUnavailable
	}
	return ErrRejected
}

func isTransportFailure(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "remote error" {
		return false // a TLS alert from the peer, wrapped as an OpError by crypto/tls
	}
	var netErr net.Error
	switch {
	case errors.As(err, &netErr),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ETIMEDOUT),
		errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return true
	}
	return false
}

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
		return nil, classifyPingError(err)
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
