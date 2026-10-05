// Package db owns the SQLite file: pragmas, the single write connection, the
// read pool and the embedded goose migrations. Everything else reaches the
// database through Store.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the only handle services get. Reads go through Queries (a read-only
// pool); every write goes through WithTx on the single write connection.
type Store struct {
	write *sql.DB
	read  *sql.DB
}

// Open creates the state directory (0700) and database file (0600) if needed,
// opens both pools and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("db: create state dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("db: create file: %w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("db: chmod file: %w", err)
	}

	write, err := openPool(path, "_txlock=immediate")
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	read, err := openPool(path, "_pragma=query_only(1)")
	if err != nil {
		write.Close()
		return nil, err
	}
	s := &Store{write: write, read: read}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func openPool(path, extra string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode() + "&" + extra}).String()
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	return d, nil
}

func (s *Store) migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.write, sub)
	if err != nil {
		return fmt.Errorf("db: migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

// Queries returns sqlc queries bound to the read pool.
func (s *Store) Queries() *sqlcgen.Queries { return sqlcgen.New(s.read) }

// WithTx runs fn in one transaction on the write connection, committing when fn
// returns nil and rolling back otherwise.
func (s *Store) WithTx(ctx context.Context, fn func(*sqlcgen.Queries) error) (err error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			tx.Rollback()
		}
	}()
	if err = fn(sqlcgen.New(tx)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// VacuumInto writes a consistent copy of the database to dest (VACUUM INTO);
// SQLite fails if dest already exists. It cannot run inside a transaction, so it
// uses the write connection directly.
func (s *Store) VacuumInto(ctx context.Context, dest string) error {
	if _, err := s.write.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("db: vacuum into: %w", err)
	}
	return nil
}

// Ping checks that the database answers a query (healthz).
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.read.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

func (s *Store) Close() error {
	werr := s.write.Close()
	if rerr := s.read.Close(); rerr != nil {
		return rerr
	}
	return werr
}
