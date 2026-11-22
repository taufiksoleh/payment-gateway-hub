// Package postgres implements the repositories on PostgreSQL through
// database/sql and the pgx driver.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// PoolConfig sizes the connection pool. Zero values use the defaults.
type PoolConfig struct {
	MaxOpenConns    int // default 20
	MaxIdleConns    int // default 5
	ConnMaxLifetime time.Duration
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, databaseURL string, pool PoolConfig) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(orDefault(pool.MaxOpenConns, 20))
	db.SetMaxIdleConns(orDefault(pool.MaxIdleConns, 5))
	db.SetConnMaxLifetime(orDefault(pool.ConnMaxLifetime, 30*time.Minute))

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// migrationLockID is an arbitrary key for pg_advisory_lock.
const migrationLockID = 7_340_918_211

// Migrate applies unapplied *.sql files in name order, each in its own
// transaction. An advisory lock lets several instances start at once: the
// others wait and then find nothing left to apply.
func Migrate(ctx context.Context, db *sql.DB, migrations fs.FS) error {
	conn, err := db.Conn(ctx) // advisory locks belong to one session
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, name := range files {
		var applied bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		script, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, conn, name, string(script)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, name, script string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, script); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// collect scans every row with scan.
func collect[T any](rows *sql.Rows, scan func(scanner) (*T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// query builds a statement with numbered placeholders.
type query struct {
	sql  strings.Builder
	args []any
}

func newQuery(base string, args ...any) *query {
	q := &query{args: args}
	q.sql.WriteString(base)
	return q
}

// arg registers a value and returns its placeholder ($n).
func (q *query) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func (q *query) where(condition string) {
	q.sql.WriteString(" AND " + condition)
}

// newestFirst appends keyset paging on (created_at, id) for table, the
// newest-first order and the limit.
func (q *query) newestFirst(table, merchantPlaceholder, cursor string, limit int) {
	if cursor != "" {
		q.where("(created_at, id) < (SELECT created_at, id FROM " + table +
			" WHERE id = " + q.arg(cursor) + " AND merchant_id = " + merchantPlaceholder + ")")
	}
	q.sql.WriteString(" ORDER BY created_at DESC, id DESC LIMIT " + q.arg(limit))
}

func (q *query) String() string { return q.sql.String() }

// mapError converts driver errors into domain errors.
func mapError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return fmt.Errorf("%w: %s", domain.ErrConflict, pgErr.ConstraintName)
	}
	return err
}

func nullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: !t.IsZero()}
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
