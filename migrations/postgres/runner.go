package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

const advisoryLockID int64 = 2944003

var ErrInvalidMigration = errors.New("invalid PostgreSQL migration")

type Migration struct {
	Version int64
	Name    string
}

type migration struct {
	Migration
	SQL      string
	Checksum string
}

type Runner struct {
	migrations []migration
}

func Embedded() (*Runner, error) {
	return NewRunner(Files)
}

func NewRunner(files fs.FS) (*Runner, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	versions := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		migration, err := loadMigration(files, entry.Name())
		if err != nil {
			return nil, err
		}
		if _, found := versions[migration.Version]; found {
			return nil, fmt.Errorf("%w: duplicate version %d", ErrInvalidMigration, migration.Version)
		}
		versions[migration.Version] = struct{}{}
		migrations = append(migrations, migration)
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("%w: no SQL files", ErrInvalidMigration)
	}
	sort.Slice(migrations, func(left, right int) bool {
		return migrations[left].Version < migrations[right].Version
	})
	return &Runner{migrations: migrations}, nil
}

func (r *Runner) Up(ctx context.Context, db *sql.DB) error {
	_, err := r.Apply(ctx, db)
	return err
}

func (r *Runner) Apply(ctx context.Context, db *sql.DB) (int, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return 0, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockID) }()
	if err := ensureHistory(ctx, conn); err != nil {
		return 0, err
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return 0, err
	}
	if err := r.validateAppliedHistory(ctx, conn, applied); err != nil {
		return 0, err
	}
	appliedVersions := make(map[int64]struct{}, len(applied))
	for _, entry := range applied {
		appliedVersions[entry.Version] = struct{}{}
	}
	count := 0
	for _, migration := range r.migrations {
		if _, exists := appliedVersions[migration.Version]; exists {
			continue
		}
		if err := applyMigration(ctx, conn, migration); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

func (r *Runner) Status(ctx context.Context, db *sql.DB) ([]Migration, error) {
	var history sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('acr.schema_migrations')").Scan(&history); err != nil {
		return nil, fmt.Errorf("locate migration history: %w", err)
	}
	if !history.Valid {
		return []Migration{}, nil
	}
	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := r.validateStatusHistory(applied); err != nil {
		return nil, err
	}
	status := make([]Migration, len(applied))
	for index, entry := range applied {
		status[index] = entry.Migration
	}
	return status, nil
}

func loadMigration(files fs.FS, name string) (migration, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return migration{}, fmt.Errorf("%w: %s must start with a numeric version", ErrInvalidMigration, name)
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || version < 1 {
		return migration{}, fmt.Errorf("%w: %s has invalid version", ErrInvalidMigration, name)
	}
	contents, err := fs.ReadFile(files, name)
	if err != nil {
		return migration{}, fmt.Errorf("read migration %s: %w", name, err)
	}
	checksum := sha256.Sum256(contents)
	return migration{Migration: Migration{Version: version, Name: name}, SQL: string(contents), Checksum: fmt.Sprintf("%x", checksum)}, nil
}

func ensureHistory(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := executor.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS acr; CREATE TABLE IF NOT EXISTS acr.schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); ALTER TABLE acr.schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT")
	if err != nil {
		return fmt.Errorf("ensure migration history: %w", err)
	}
	return nil
}

type appliedMigration struct {
	Migration
	Checksum sql.NullString
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func appliedMigrations(ctx context.Context, executor queryer) ([]appliedMigration, error) {
	rows, err := executor.QueryContext(ctx, "SELECT version, name, checksum FROM acr.schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("query migration history: %w", err)
	}
	defer rows.Close()
	entries := []appliedMigration{}
	for rows.Next() {
		var entry appliedMigration
		if err := rows.Scan(&entry.Version, &entry.Name, &entry.Checksum); err != nil {
			return nil, fmt.Errorf("scan migration history: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration history: %w", err)
	}
	return entries, nil
}

func (r *Runner) validateAppliedHistory(ctx context.Context, conn *sql.Conn, applied []appliedMigration) error {
	if err := r.validateStatusHistory(applied); err != nil {
		return err
	}
	for index, entry := range applied {
		if entry.Checksum.Valid {
			continue
		}
		if _, err := conn.ExecContext(ctx, "UPDATE acr.schema_migrations SET checksum = $1 WHERE version = $2 AND checksum IS NULL", r.migrations[index].Checksum, entry.Version); err != nil {
			return fmt.Errorf("backfill migration checksum: %w", err)
		}
	}
	return nil
}

func (r *Runner) validateStatusHistory(applied []appliedMigration) error {
	if len(applied) > len(r.migrations) {
		return ErrInvalidMigration
	}
	for index, entry := range applied {
		expected := r.migrations[index]
		if entry.Version != expected.Version || entry.Name != expected.Name {
			return ErrInvalidMigration
		}
		if entry.Checksum.Valid && entry.Checksum.String != expected.Checksum {
			return ErrInvalidMigration
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, migration migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.Version, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("apply migration %d: %w", migration.Version, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO acr.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)", migration.Version, migration.Name, migration.Checksum); err != nil {
		return fmt.Errorf("record migration %d: %w", migration.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.Version, err)
	}
	return nil
}
