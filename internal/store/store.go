// Package store owns the SQLite database: schema migration and every query the
// panel runs. Nothing above this package writes SQL.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, so the panel stays a CGO-free static binary
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	db *sql.DB
}

// Open connects to the database at path and brings the schema up to date.
func Open(path string) (*Store, error) {
	// WAL keeps readers from blocking the writer, which matters because the
	// node hub writes traffic rows while the UI is being served. busy_timeout
	// covers the brief writer-vs-writer overlap that remains.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// SQLite takes one writer at a time. Serialising here turns what would be
	// SQLITE_BUSY errors into ordinary waiting.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for the few places that need a transaction of their own.
func (s *Store) DB() *sql.DB { return s.db }

// migrate applies every embedded migration that has not run yet, in filename
// order, each in its own transaction.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	applied := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		applied[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, unixepoch())`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}

// ── settings ────────────────────────────────────────────────────────────────

func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		key, value)
	return err
}

// SetSettingsIfAbsent writes several settings together, but only if the key
// named by guard has no value yet. It reports whether it wrote.
//
// This is how first-run setup claims the administrator. Reading "does an admin
// exist" and then writing one are two statements, and between them a second
// request can pass the same check — so the check has to be part of the write.
// One transaction, so the username and the password hash cannot land separately
// either.
func (s *Store) SetSettingsIfAbsent(guard string, kv map[string]string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRow(`SELECT v FROM settings WHERE k = ?`, guard).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if existing != "" {
		return false, nil
	}
	for k, v := range kv {
		if _, err := tx.Exec(`INSERT INTO settings (k, v) VALUES (?, ?)
			ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
