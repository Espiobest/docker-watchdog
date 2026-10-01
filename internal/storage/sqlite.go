package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"docker-watchdog/internal/watchdog"
	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const historyLimit = 10000

type Store struct {
	db   *sql.DB
	lock *flock.Flock
}

// Open holds an OS file lock independently of SQLite's connection lifecycle.
// Replacing an interrupted SQL connection must never release ownership.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path cannot be empty")
	}
	var lock *flock.Flock
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		// Canonicalize existing parent directories, including symlinks.
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return nil, err
		}
		path = filepath.Join(parent, filepath.Base(absolute))
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		lock = flock.New(path+".lock", flock.SetPermissions(0600))
		locked, err := lock.TryLock()
		if err != nil {
			return nil, fmt.Errorf("lock database: %w", err)
		}
		if !locked {
			return nil, errors.New("database is already owned by another watchdog")
		}
	}
	db, err := sql.Open("sqlite", dataSource(path))
	if err != nil {
		if lock != nil {
			lock.Close()
		}
		return nil, err
	}
	// One connection serializes short writes and keeps :memory: tests consistent.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, lock: lock}
	if err := store.initialize(ctx); err != nil {
		store.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Current observations must be fresh for this process. Durable policy and
	// incident history remain intact across restarts.
	if _, err := tx.ExecContext(ctx, "DELETE FROM current_state"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error {
	err := s.db.Close()
	if s.lock != nil {
		err = errors.Join(err, s.lock.Close())
	}
	return err
}

func dataSource(path string) string {
	parameters := url.Values{}
	// Driver applies these to every physical connection, including replacements.
	parameters.Add("_pragma", "busy_timeout(2000)")
	parameters.Add("_pragma", "journal_mode(WAL)")
	parameters.Add("_pragma", "synchronous(FULL)")
	if path == ":memory:" {
		return path + "?" + parameters.Encode()
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: parameters.Encode()}
	return uri.String()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Load(ctx context.Context, id string) (watchdog.Checkpoint, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM checkpoints WHERE container_id = ?", id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return watchdog.Checkpoint{}, nil
	}
	if err != nil {
		return watchdog.Checkpoint{}, err
	}
	var checkpoint watchdog.Checkpoint
	if err := json.Unmarshal(payload, &checkpoint); err != nil {
		return checkpoint, err
	}
	if checkpoint.Attempts < 0 {
		return checkpoint, errors.New("invalid stored retry count")
	}
	return checkpoint, nil
}

// Record atomically commits the latest sample, optional recovery reservation,
// and a deduplicated incident. A restart is never issued before this commits.
func (s *Store) Record(ctx context.Context, event watchdog.Event, checkpoint *watchdog.Checkpoint) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var previousPayload []byte
	err = tx.QueryRowContext(ctx, "SELECT payload FROM current_state WHERE container_id = ?", event.ID).Scan(&previousPayload)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var previous watchdog.Event
	if len(previousPayload) > 0 {
		if err := json.Unmarshal(previousPayload, &previous); err != nil {
			return err
		}
	}
	changed := len(previousPayload) == 0 || event.Status != previous.Status ||
		event.Error != previous.Error || event.StatsError != previous.StatsError ||
		event.Attempts != previous.Attempts || event.Reason != previous.Reason || event.Action != ""
	if changed {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO events (container_id, observed_at, payload) VALUES (?, ?, ?)",
			event.ID, event.Time.UTC().Format(time.RFC3339Nano), payload)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM events WHERE sequence <= (SELECT MAX(sequence) FROM events) - ?", historyLimit); err != nil {
			return err
		}
	}
	if event.Removed {
		for _, table := range []string{"current_state", "checkpoints"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE container_id = ?", event.ID); err != nil {
				return err
			}
		}
	} else {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO current_state (container_id, payload) VALUES (?, ?) ON CONFLICT(container_id) DO UPDATE SET payload = excluded.payload",
			event.ID, payload)
		if err != nil {
			return err
		}
	}
	if checkpoint != nil {
		state, err := json.Marshal(checkpoint)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"INSERT INTO checkpoints (container_id, payload) VALUES (?, ?) ON CONFLICT(container_id) DO UPDATE SET payload = excluded.payload",
			event.ID, state)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Current(ctx context.Context) ([]watchdog.Event, error) {
	return s.query(ctx, "SELECT payload FROM current_state WHERE container_id != '' ORDER BY container_id")
}

func (s *Store) History(ctx context.Context, id string, before int64, limit int) ([]Incident, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	query := "SELECT sequence, payload FROM events WHERE 1=1"
	args := []any{}
	if id != "" {
		query += " AND container_id = ?"
		args = append(args, id)
	}
	if before > 0 {
		query += " AND sequence < ?"
		args = append(args, before)
	}
	query += " ORDER BY sequence DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	incidents := []Incident{}
	for rows.Next() {
		var incident Incident
		var payload []byte
		if err := rows.Scan(&incident.Sequence, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &incident.Event); err != nil {
			return nil, err
		}
		incidents = append(incidents, incident)
	}
	return incidents, rows.Err()
}

type Incident struct {
	Sequence int64 `json:"sequence"`
	watchdog.Event
}

func (s *Store) query(ctx context.Context, query string) ([]watchdog.Event, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []watchdog.Event{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var event watchdog.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
