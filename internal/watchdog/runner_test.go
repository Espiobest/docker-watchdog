package watchdog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]Sample
	inspectErr error
	listErr    error
	restartErr error
	restarts   int
	active     int
	peak       int
	delay      time.Duration
}

func (f *fakeEngine) List(ctx context.Context) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Container{}
	for _, s := range f.containers {
		out = append(out, Container{ID: s.ID, Name: s.Name, State: s.State})
	}
	return out, f.listErr
}
func (f *fakeEngine) Inspect(ctx context.Context, id string) (Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.containers[id]
	if !ok {
		return s, errors.New("not found")
	}
	return s, f.inspectErr
}
func (f *fakeEngine) Stats(ctx context.Context, s *Sample) error {
	f.mu.Lock()
	f.active++
	if f.active > f.peak {
		f.peak = f.active
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	select {
	case <-time.After(f.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeEngine) Restart(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	return f.restartErr
}

func testConfig() Config {
	c := DefaultConfig()
	c.PollInterval = 5 * time.Millisecond
	c.DiscoveryInterval = 5 * time.Millisecond
	c.BaseBackoff = 5 * time.Millisecond
	c.MaxBackoff = 10 * time.Millisecond
	return c
}

func startRunner(t *testing.T, f *fakeEngine, c Config) (context.CancelFunc, <-chan Event, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := make(chan Event, 128)
	done := make(chan error, 1)
	go func() { done <- (Runner{Engine: f, Config: c}).Run(ctx, events) }()
	return cancel, events, done
}

func awaitEvent(t *testing.T, events <-chan Event, predicate func(Event) bool) Event {
	t.Helper()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("events closed unexpectedly")
			}
			if predicate(e) {
				return e
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for event")
		}
	}
}

func stopRunner(t *testing.T, cancel context.CancelFunc, events <-chan Event, done <-chan error) {
	t.Helper()
	cancel()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-timeout.C:
			t.Fatal("worker shutdown leaked or blocked")
		}
	}
}

func TestRunnerFailedRestartsConsumeBudget(t *testing.T) {
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "running", Health: "unhealthy"}}, restartErr: errors.New("request failed")}
	c := testConfig()
	c.AutoRestart = true
	c.MaxRetries = 2
	cancel, events, done := startRunner(t, f, c)
	awaitEvent(t, events, func(e Event) bool { return e.Status == "retry-exhausted" })
	stopRunner(t, cancel, events, done)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restarts != 2 {
		t.Fatalf("restarts=%d, want 2", f.restarts)
	}
}

func TestRunnerUnknownDoesNotRestart(t *testing.T) {
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "running", Health: "unhealthy"}}, inspectErr: errors.New("daemon unavailable")}
	c := testConfig()
	c.AutoRestart = true
	cancel, events, done := startRunner(t, f, c)
	awaitEvent(t, events, func(e Event) bool { return e.Status == "unknown" })
	stopRunner(t, cancel, events, done)
	if f.restarts != 0 {
		t.Fatal("API error triggered restart")
	}
}

func TestRunnerConcurrentBoundedAndCancellable(t *testing.T) {
	f := &fakeEngine{containers: map[string]Sample{}, delay: 20 * time.Millisecond}
	for i := 0; i < 12; i++ {
		id := fmt.Sprint(i)
		f.containers[id] = Sample{ID: id, State: "running"}
	}
	c := testConfig()
	c.MaxConcurrent = 3
	cancel, events, done := startRunner(t, f, c)
	seen := map[string]bool{}
	awaitEvent(t, events, func(e Event) bool { seen[e.ID] = true; return len(seen) == 12 })
	stopRunner(t, cancel, events, done)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.peak < 2 || f.peak > 3 {
		t.Fatalf("concurrency peak=%d, want 2..3", f.peak)
	}
	if f.active != 0 {
		t.Fatal("stats call leaked")
	}
}

func TestRunnerDiscoveryRemovalAndPreviouslyRunningExit(t *testing.T) {
	f := &fakeEngine{containers: map[string]Sample{"old": {ID: "old", State: "exited", ExitCode: 1}}}
	c := testConfig()
	c.AutoRestart = true
	c.RecoverExited = true
	cancel, events, done := startRunner(t, f, c)
	f.mu.Lock()
	f.containers["new"] = Sample{ID: "new", State: "running"}
	f.mu.Unlock()
	awaitEvent(t, events, func(e Event) bool {
		if e.ID == "old" {
			t.Fatal("pre-existing stopped container was adopted")
		}
		return e.ID == "new"
	})
	f.mu.Lock()
	f.containers["new"] = Sample{ID: "new", State: "exited", ExitCode: 1}
	f.mu.Unlock()
	awaitEvent(t, events, func(e Event) bool { return e.Action == "restarted" })
	f.mu.Lock()
	delete(f.containers, "new")
	f.mu.Unlock()
	awaitEvent(t, events, func(e Event) bool { return e.Removed && e.ID == "new" })
	stopRunner(t, cancel, events, done)
}

func TestRunnerCancelsWithFullEventChannel(t *testing.T) {
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "running"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event)
	done := make(chan error, 1)
	go func() { done <- (Runner{Engine: f, Config: testConfig()}).Run(ctx, events) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("full event channel prevents cancellation")
	}
}

type journalStub struct {
	load   func(context.Context, string) (Checkpoint, error)
	record func(context.Context, Event, *Checkpoint) error
}

func (j journalStub) Load(ctx context.Context, id string) (Checkpoint, error) {
	if j.load != nil {
		return j.load(ctx, id)
	}
	return Checkpoint{}, nil
}

func (j journalStub) Record(ctx context.Context, event Event, checkpoint *Checkpoint) error {
	if j.record != nil {
		return j.record(ctx, event, checkpoint)
	}
	return nil
}

type recoveryEngine struct {
	*fakeEngine
	beforeRestart func()
}

func (e recoveryEngine) Restart(ctx context.Context, id string) error {
	if e.beforeRestart != nil {
		e.beforeRestart()
	}
	return e.fakeEngine.Restart(ctx, id)
}

// Prime a due recovery without wall-clock sleeps. The runner still performs
// inspection, persistence, a fresh eligibility check, and the Docker action.
func duePolicy(c Config, sample Sample) *Policy {
	p := NewPolicy(c)
	p.Evaluate(sample, time.Now().Add(-2*c.BaseBackoff))
	return p
}

func TestRunnerPersistsReservationBeforeRestart(t *testing.T) {
	c := testConfig()
	c.AutoRestart = true
	sample := Sample{ID: "a", State: "running", Health: "unhealthy"}
	f := &fakeEngine{containers: map[string]Sample{"a": sample}}
	var reserved Checkpoint
	var reservedAt time.Time
	journal := journalStub{record: func(_ context.Context, event Event, checkpoint *Checkpoint) error {
		if event.Restart && event.Action == "" {
			if checkpoint == nil {
				t.Fatal("restart reservation lacks checkpoint")
			}
			reserved = *checkpoint
			reservedAt = event.Time
		}
		return nil
	}}
	engine := recoveryEngine{fakeEngine: f, beforeRestart: func() {
		if reserved.Attempts != 1 || reservedAt.IsZero() {
			t.Fatal("Docker mutation occurred before durable reservation")
		}
		if reserved.NextRetry.Before(reservedAt.Add(c.RestartTimeout + 2*c.BaseBackoff)) {
			t.Fatal("reservation does not cover in-flight request and cooldown")
		}
	}}
	runner := Runner{Engine: engine, Config: c, Journal: journal}
	events := make(chan Event, 4)
	runner.poll(context.Background(), Container{ID: "a"}, duePolicy(c, sample), make(chan struct{}, 1), events)
	if f.restarts != 1 {
		t.Fatalf("restarts=%d, want 1", f.restarts)
	}
	if len(events) != 2 {
		t.Fatalf("events=%d, want reservation and completion", len(events))
	}
	reservation, completion := <-events, <-events
	if !reservation.Restart || completion.Action != ActionRestarted {
		t.Fatalf("unexpected recovery events: %+v, %+v", reservation, completion)
	}
}

func TestRunnerFailedReservationBlocksRestart(t *testing.T) {
	c := testConfig()
	c.AutoRestart = true
	sample := Sample{ID: "a", State: "running", Health: "unhealthy"}
	f := &fakeEngine{containers: map[string]Sample{"a": sample}}
	journal := journalStub{record: func(context.Context, Event, *Checkpoint) error {
		return errors.New("disk full")
	}}
	runner := Runner{Engine: f, Config: c, Journal: journal}
	events := make(chan Event, 4)
	runner.poll(context.Background(), Container{ID: "a"}, duePolicy(c, sample), make(chan struct{}, 1), events)
	if f.restarts != 0 {
		t.Fatal("Docker mutation occurred despite failed persistence")
	}
	if len(events) != 1 {
		t.Fatalf("events=%d, want one storage failure", len(events))
	}
	if event := <-events; event.Status != StatusStorageError || event.Restart {
		t.Fatalf("failed persistence not reported safely: %+v", event)
	}
}

func TestRunnerFailedCheckpointLoadBlocksMonitoringAndRecovery(t *testing.T) {
	c := testConfig()
	c.AutoRestart = true
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "running", Health: "unhealthy"}}}
	journal := journalStub{load: func(context.Context, string) (Checkpoint, error) {
		return Checkpoint{}, errors.New("database unavailable")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- (Runner{Engine: f, Config: c, Journal: journal}).Run(ctx, events) }()
	// Observe multiple failed load cycles, so the test covers retrying the load
	// without silently continuing with an empty recovery budget.
	for range 3 {
		awaitEvent(t, events, func(event Event) bool {
			if event.ID == "a" && event.Status != StatusStorageError {
				t.Fatalf("worker monitored without loading durable state: %+v", event)
			}
			return event.Status == StatusStorageError
		})
	}
	stopRunner(t, cancel, events, done)
	if f.restarts != 0 || f.peak != 0 {
		t.Fatalf("worker continued after load failure: restarts=%d stats concurrency=%d", f.restarts, f.peak)
	}
}

func TestRunnerFreshInspectionSkipsRecoveredContainer(t *testing.T) {
	c := testConfig()
	c.AutoRestart = true
	sample := Sample{ID: "a", State: "running", Health: "unhealthy"}
	f := &fakeEngine{containers: map[string]Sample{"a": sample}}
	journal := journalStub{record: func(_ context.Context, event Event, _ *Checkpoint) error {
		if event.Restart && event.Action == "" {
			f.mu.Lock()
			recovered := f.containers["a"]
			recovered.Health = "healthy"
			f.containers["a"] = recovered
			f.mu.Unlock()
		}
		return nil
	}}
	runner := Runner{Engine: f, Config: c, Journal: journal}
	events := make(chan Event, 4)
	p := duePolicy(c, sample)
	runner.poll(context.Background(), Container{ID: "a"}, p, make(chan struct{}, 1), events)
	if f.restarts != 0 {
		t.Fatal("recovered container was restarted using stale observation")
	}
	if len(events) != 2 {
		t.Fatalf("events=%d, want reservation and skipped action", len(events))
	}
	<-events // Reservation precedes the fresh check.
	if event := <-events; event.Action != ActionRestartSkipped || event.Attempts != 0 {
		t.Fatalf("skipped recovery consumed budget or lacked action: %+v", event)
	}
	if checkpoint := p.Snapshot(); checkpoint.Attempts != 0 || !checkpoint.NextRetry.IsZero() {
		t.Fatalf("skipped recovery left a reservation: %+v", checkpoint)
	}
}
