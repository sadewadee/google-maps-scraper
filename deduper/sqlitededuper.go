package deduper

import (
	"context"
	"database/sql"
	"sync"
	"time"

	_ "modernc.org/sqlite" // sqlite driver
)

// sqliteDeduper persists keys in a SQLite table (dedup_keys) to deduplicate across jobs.
// Table schema: dedup_keys(key TEXT PRIMARY KEY, created_at INT)
type sqliteDeduper struct {
	db  *sql.DB
	mux *sync.Mutex
}

var _ Deduper = (*sqliteDeduper)(nil)

// NewPersistentSQLite opens (or creates) a SQLite database at the given path
// and ensures the dedup_keys table exists. It returns a Deduper implementation
// that uses INSERT OR IGNORE to atomically record seen keys.
func NewPersistentSQLite(path string) (Deduper, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// Keep a tiny pool; WAL is enabled by the main repo, but this handle is dedicated
	// to dedup writes so a single connection suffices.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Ensure table exists
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS dedup_keys (
			key TEXT PRIMARY KEY,
			created_at INT NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &sqliteDeduper{
		db:  db,
		mux: &sync.Mutex{},
	}, nil
}

// AddIfNotExists inserts the key if it doesn't exist and returns true when inserted.
// If the key already exists, it returns false. On database errors, it returns true
// to avoid over-filtering results during transient failures.
func (d *sqliteDeduper) AddIfNotExists(ctx context.Context, key string) bool {
	if key == "" {
		return true
	}

	// Serialize writes to reduce contention on the single connection.
	d.mux.Lock()
	defer d.mux.Unlock()

	res, err := d.db.ExecContext(
		ctx,
		"INSERT OR IGNORE INTO dedup_keys(key, created_at) VALUES(?, ?)",
		key,
		time.Now().UTC().Unix(),
	)
	if err != nil {
		// Be permissive on error: let the pipeline proceed as if unseen.
		return true
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return true
	}

	// Inserted → unseen (true). Ignored → seen (false).
	return rows == 1
}

// Close releases the SQLite connection used by the persistent deduper.
func (d *sqliteDeduper) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}
