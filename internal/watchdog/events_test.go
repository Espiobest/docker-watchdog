package watchdog

import (
	"context"
	"errors"
	"testing"
	"time"
)

type eventEngine struct {
	*fakeEngine
	hints       chan string
	fail        chan struct{}
	connections chan struct{}
}

func (e *eventEngine) WatchEvents(ctx context.Context, notify func(string)) error {
	notify("")
	select {
	case e.connections <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.fail:
			return errors.New("connection lost")
		case id := <-e.hints:
			notify(id)
		}
	}
}

func TestEventHintsRefreshDiscoverRemoveAndReconnect(t *testing.T) {
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "running"}}}
	e := &eventEngine{fakeEngine: f, hints: make(chan string, 4), fail: make(chan struct{}, 1), connections: make(chan struct{}, 4)}
	c := DefaultConfig()
	c.PollInterval = time.Hour
	c.DiscoveryInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan Event, 128)
	done := make(chan error, 1)
	go func() { done <- (Runner{Engine: e, Config: c}).Run(ctx, out) }()
	wait := func(match func(Event) bool) {
		t.Helper()
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event := <-out:
				if match(event) {
					return
				}
			case <-timer.C:
				t.Fatal("expected event did not arrive")
			}
		}
	}
	wait(func(event Event) bool { return event.ID == "a" && event.State == "running" })
	f.mu.Lock()
	f.containers["a"] = Sample{ID: "a", State: "exited", ExitCode: 1}
	f.mu.Unlock()
	e.hints <- "a"
	wait(func(event Event) bool { return event.ID == "a" && event.Status == StatusCrashed })
	f.mu.Lock()
	f.containers["b"] = Sample{ID: "b", State: "running"}
	f.mu.Unlock()
	e.hints <- "b"
	wait(func(event Event) bool { return event.ID == "b" })
	f.mu.Lock()
	delete(f.containers, "b")
	f.mu.Unlock()
	e.hints <- "b"
	wait(func(event Event) bool { return event.ID == "b" && event.Removed })
	e.fail <- struct{}{}
	wait(func(event Event) bool { return event.Status == StatusEventStreamError })
	// A change while disconnected is caught on reconnect without waiting an hour.
	f.mu.Lock()
	f.containers["c"] = Sample{ID: "c", State: "running"}
	f.mu.Unlock()
	wait(func(event Event) bool { return event.Status == StatusEventStreamConnected })
	wait(func(event Event) bool { return event.ID == "c" })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not join streams")
	}
}

func TestEventReconnectBackoffCancelsPromptly(t *testing.T) {
	e := &eventEngine{fakeEngine: &fakeEngine{}, hints: make(chan string), fail: make(chan struct{}, 1), connections: make(chan struct{}, 1)}
	e.fail <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notices := make(chan engineNotice, 2)
	done := make(chan struct{})
	go func() { watchEvents(ctx, e, notices); close(done) }()
	<-notices
	select {
	case notice := <-notices:
		if notice.err == nil {
			t.Fatal("missing failure")
		}
	case <-time.After(time.Second):
		t.Fatal("no failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backoff prevented cancellation")
	}
}
