package storage

import (
	"context"
	"database/sql/driver"
	"path/filepath"
	"testing"
	"time"

	"docker-watchdog/internal/watchdog"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRecoveryBudgetSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watchdog.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	checkpoint := watchdog.Checkpoint{Attempts: 3, Initialized: true, NextRetry: time.Now().Add(time.Minute)}
	event := watchdog.Event{
		Time: time.Now(), Sample: watchdog.Sample{ID: "container-a"},
		Decision: watchdog.Decision{Status: watchdog.StatusRetryExhausted, Attempts: 3},
	}
	if err := store.Record(ctx, event, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.Load(ctx, event.ID)
	if err != nil || loaded.Attempts != 3 || !loaded.NextRetry.Equal(checkpoint.NextRetry) {
		t.Fatalf("checkpoint=%+v err=%v", loaded, err)
	}
	config := watchdog.DefaultConfig()
	config.AutoRestart = true
	policy := watchdog.NewPolicy(config)
	policy.Restore(loaded)
	decision := policy.Evaluate(watchdog.Sample{State: "running", Health: "unhealthy"}, time.Now())
	if decision.Restart || decision.Status != watchdog.StatusRetryExhausted {
		t.Fatalf("reopen bypassed retry cap: %+v", decision)
	}

	current, err := store.Current(ctx)
	if err != nil || len(current) != 0 {
		t.Fatalf("old samples must not appear fresh after reopen: %+v %v", current, err)
	}
	history, err := store.History(ctx, "", 0, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("history lost: %+v %v", history, err)
	}
}

func TestRecordIsAtomic(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	_, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_checkpoint BEFORE INSERT ON checkpoints BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	event := watchdog.Event{Time: time.Now(), Sample: watchdog.Sample{ID: "a"}, Decision: watchdog.Decision{Status: watchdog.StatusRestarting}}
	if err := store.Record(ctx, event, &watchdog.Checkpoint{Attempts: 1}); err == nil {
		t.Fatal("expected transaction failure")
	}
	history, err := store.History(ctx, "", 0, 10)
	if err != nil || len(history) != 0 {
		t.Fatal("failed transaction left an incident behind")
	}
	current, err := store.Current(ctx)
	if err != nil || len(current) != 0 {
		t.Fatal("failed transaction left a current sample behind")
	}
}

func TestHistoryDedupPaginationAndRemoval(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	event := watchdog.Event{Time: time.Now(), Sample: watchdog.Sample{ID: "a"}, Decision: watchdog.Decision{Status: watchdog.StatusHealthy}}
	for range 3 {
		if err := store.Record(ctx, event, &watchdog.Checkpoint{Initialized: true}); err != nil {
			t.Fatal(err)
		}
		event.Time = event.Time.Add(time.Second)
		event.CPUPercent++
	}
	event.Status = watchdog.StatusUnhealthy
	if err := store.Record(ctx, event, nil); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "a", 0, 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	page, err := store.History(ctx, "a", history[0].Sequence, 1)
	if err != nil || len(page) != 1 || page[0].Status != watchdog.StatusHealthy {
		t.Fatalf("cursor page=%+v err=%v", page, err)
	}
	page, err = store.History(ctx, "a' OR 1=1 --", 0, 10)
	if err != nil || len(page) != 0 {
		t.Fatal("container filter must be a bound parameter")
	}
	event.Removed = true
	event.Status = watchdog.StatusRemoved
	if err := store.Record(ctx, event, nil); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Load(ctx, "a")
	if err != nil || checkpoint.Initialized {
		t.Fatal("removed container retained checkpoint")
	}
}

func TestSecondProcessCannotShareDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclusive.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Simulate database/sql replacing an interrupted physical connection.
	connection, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Raw(func(any) error { return driver.ErrBadConn })
	connection.Close()
	if err := store.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	var synchronous, busyTimeout int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("replacement lost FULL durability: %d %v", synchronous, err)
	}
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil || busyTimeout != 2000 {
		t.Fatalf("replacement lost busy timeout: %d %v", busyTimeout, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	second, err := Open(ctx, path)
	if err == nil {
		second.Close()
		t.Fatal("second writer acquired the same recovery database")
	}
}
