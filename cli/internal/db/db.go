// Package db opens the server's database: SQLite for a single server, or Postgres when several servers
// share their state. Queries are written once, with ? placeholders, and rewritten for Postgres.
package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DB is a database and its dialect.
type DB struct {
	*sql.DB
	// PG is true for Postgres, false for SQLite.
	PG bool
	// DSN is how it was opened, for connections outside database/sql (Postgres LISTEN).
	DSN string
}

// IsPostgres reports whether a database address names Postgres rather than a SQLite file.
func IsPostgres(addr string) bool {
	return strings.HasPrefix(addr, "postgres://") || strings.HasPrefix(addr, "postgresql://") || strings.Contains(addr, "host=")
}

// Open opens Postgres for a postgres:// URL or key=value DSN, and otherwise the SQLite file at addr.
func Open(addr string) (*DB, error) {
	if IsPostgres(addr) {
		d, err := sql.Open("pgx", addr)
		if err != nil {
			return nil, err
		}
		conns := 8 // a small Cloud SQL instance allows 25 connections across every server
		if n, err := strconv.Atoi(os.Getenv("CONTEXT_DROP_DATABASE_CONNS")); err == nil && n > 0 {
			conns = n
		}
		d.SetMaxOpenConns(conns)
		if err := d.Ping(); err != nil {
			d.Close()
			return nil, err
		}
		return &DB{DB: d, PG: true, DSN: addr}, nil
	}
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite", "file:"+addr+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // SQLite has one writer; one connection also serializes transactions
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	_ = os.Chmod(addr, 0o600)
	return &DB{DB: d, DSN: addr}, nil
}

// Q rewrites ? placeholders as $1, $2, … for Postgres.
func (d *DB) Q(q string) string {
	if !d.PG {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (d *DB) Exec(q string, args ...any) (sql.Result, error) { return d.DB.Exec(d.Q(q), args...) }
func (d *DB) Query(q string, args ...any) (*sql.Rows, error) { return d.DB.Query(d.Q(q), args...) }
func (d *DB) QueryRow(q string, args ...any) *sql.Row        { return d.DB.QueryRow(d.Q(q), args...) }

// ExecContext and the rest take a context.
func (d *DB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, d.Q(q), args...)
}
func (d *DB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.Q(q), args...)
}
func (d *DB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.Q(q), args...)
}

// Tx is a transaction with the same placeholder rewriting.
type Tx struct {
	*sql.Tx
	db *DB
}

func (t *Tx) Exec(q string, args ...any) (sql.Result, error) { return t.Tx.Exec(t.db.Q(q), args...) }
func (t *Tx) Query(q string, args ...any) (*sql.Rows, error) { return t.Tx.Query(t.db.Q(q), args...) }
func (t *Tx) QueryRow(q string, args ...any) *sql.Row        { return t.Tx.QueryRow(t.db.Q(q), args...) }

// ForUpdate is " FOR UPDATE" on Postgres, to lock the rows a transaction reads; SQLite already runs
// one transaction at a time.
func (t *Tx) ForUpdate() string {
	if t.db.PG {
		return " FOR UPDATE"
	}
	return ""
}

// InTx runs fn in a transaction, committing when it returns nil.
func (d *DB) InTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Tx{Tx: tx, db: d}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Duplicate reports whether err is a unique constraint violation.
func Duplicate(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint") || strings.Contains(s, "SQLSTATE 23505") || strings.Contains(s, "duplicate key")
}

// ErrNoRows is sql.ErrNoRows.
var ErrNoRows = sql.ErrNoRows

// NoRows reports whether err is sql.ErrNoRows.
func NoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// Migrate runs each statement of a schema; every statement must be safe to run again.
func (d *DB) Migrate(schema string) error {
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := d.DB.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
