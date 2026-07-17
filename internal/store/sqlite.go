// Package store is the SQLite-backed persistence layer. Pure Go via
// modernc.org/sqlite so the binary builds with CGO_ENABLED=0.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store wraps the database handle and typed accessors.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and runs
// migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One connection: SQLite serialises writers anyway, and a single conn means
	// the pragmas above are guaranteed to apply to every statement rather than
	// only to whichever conn happened to be opened first.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// DB exposes the raw handle for packages that need direct queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// randID mints a primary key. Keys are random rather than sequential so an id
// that leaks into a URL says nothing about how much else exists.
func randID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewID exposes randID to packages that write rows through the store.
func NewID() string { return randID() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
