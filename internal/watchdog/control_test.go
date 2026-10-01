package watchdog

import (
	"context"
	"errors"
	"testing"
	"time"
)

type manualEngine struct {
	*fakeEngine
	start func(context.Context, string) error
	stop  func(context.Context, string) error
}

func (e manualEngine) Start(ctx context.Context, id string) error { return e.start(ctx, id) }
func (e manualEngine) Stop(ctx context.Context, id string) error  { return e.stop(ctx, id) }

func TestStopPersistsPauseBeforeDockerMutation(t *testing.T) {
	c := testConfig()
	c.AutoRestart, c.RecoverExited = true, true
	sample := Sample{ID: "a", State: "running", Health: "unhealthy"}
	f := &fakeEngine{containers: map[string]Sample{"a": sample}}
	var saved Checkpoint
	stopped := false
	engine := manualEngine{fakeEngine: f, stop: func(context.Context, string) error {
		if !saved.RecoveryPaused || saved.Attempts != 2 {
			t.Fatalf("stop preceded durable maintenance state: %+v", saved)
		}
		stopped = true
		return nil
	}}
	journal := journalStub{record: func(_ context.Context, _ Event, checkpoint *Checkpoint) error {
		saved = *checkpoint
		return nil
	}}
	p := NewPolicy(c)
	p.Restore(Checkpoint{Version: 1, Attempts: 2, SeenRunning: true})
	r := Runner{Engine: engine, Config: c, Journal: journal}
	if err := r.control(context.Background(), "a", CommandStop, p, make(chan struct{}, 1), make(chan Event, 4)); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("stop command did not reach Docker")
	}
	restored := NewPolicy(c)
	restored.Restore(saved)
	exited := Sample{State: "exited", ExitCode: 137}
	if d := restored.Evaluate(exited, time.Now().Add(time.Hour)); d.Restart || !d.RecoveryPaused || d.Attempts != 2 {
		t.Fatalf("restored maintenance mode permitted recovery: %+v", d)
	}
}

func TestFailedControlPersistenceNeverMutatesDocker(t *testing.T) {
	for _, command := range []Command{CommandStop, CommandStart, CommandRestart, CommandResumeRecovery} {
		t.Run(string(command), func(t *testing.T) {
			c := testConfig()
			sample := Sample{ID: "a", State: "running"}
			if command == CommandStart {
				sample.State = "exited"
			}
			f := &fakeEngine{containers: map[string]Sample{"a": sample}}
			mutation := func(context.Context, string) error { t.Fatal("Docker called after failed persistence"); return nil }
			engine := manualEngine{fakeEngine: f, start: mutation, stop: mutation}
			journal := journalStub{record: func(context.Context, Event, *Checkpoint) error { return errors.New("disk full") }}
			p := NewPolicy(c)
			p.Restore(Checkpoint{Attempts: 2})
			r := Runner{Engine: engine, Config: c, Journal: journal}
			if err := r.control(context.Background(), "a", command, p, make(chan struct{}, 1), make(chan Event, 4)); err == nil {
				t.Fatal("persistence failure was hidden")
			}
			if f.restarts != 0 || !p.Snapshot().RecoveryPaused || p.Snapshot().Attempts != 2 {
				t.Fatalf("failure did not preserve safe policy: %+v restarts=%d", p.Snapshot(), f.restarts)
			}
		})
	}
}

func TestManualStartPreservesBudgetAndAuthorizesExitRecovery(t *testing.T) {
	c := testConfig()
	c.AutoRestart, c.RecoverExited = true, true
	sample := Sample{ID: "a", State: "exited", ExitCode: 1}
	f := &fakeEngine{containers: map[string]Sample{"a": sample}}
	engine := manualEngine{fakeEngine: f, start: func(context.Context, string) error { return nil }}
	p := NewPolicy(c)
	p.Restore(Checkpoint{Version: 1, Attempts: 1, RecoveryPaused: true})
	r := Runner{Engine: engine, Config: c}
	if err := r.control(context.Background(), "a", CommandStart, p, make(chan struct{}, 1), make(chan Event, 4)); err != nil {
		t.Fatal(err)
	}
	checkpoint := p.Snapshot()
	if checkpoint.Attempts != 1 || checkpoint.RecoveryPaused || !checkpoint.SeenRunning {
		t.Fatalf("manual start changed budget or failed to resume: %+v", checkpoint)
	}
	// The process may crash before its first running sample. Successful Start
	// still established the user's intent to run this container.
	if d := p.Evaluate(sample, checkpoint.NextRetry); !d.Restart || d.Attempts != 2 {
		t.Fatalf("explicitly started container was not eligible: %+v", d)
	}
	neverStarted := NewPolicy(c)
	neverStarted.Evaluate(sample, time.Now())
	if d := neverStarted.Evaluate(sample, time.Now().Add(time.Hour)); d.Restart || d.Attempts != 0 {
		t.Fatalf("imported stopped container was automatically started: %+v", d)
	}
}

func TestManualStartDoesNotHideNextExternalStart(t *testing.T) {
	c := testConfig()
	c.CrashThreshold = 1
	p := NewPolicy(c)
	now := time.Now()
	p.Evaluate(Sample{State: "created"}, now)
	p.expectOwnStart = true
	sample := Sample{State: "running", StartedAt: now.Add(time.Second)}
	if d := p.Evaluate(sample, now.Add(time.Second)); d.Status == StatusCrashLoop {
		t.Fatal("intentional first start counted as a crash")
	}
	sample.StartedAt = now.Add(2 * time.Second)
	if d := p.Evaluate(sample, now.Add(2*time.Second)); d.Status != StatusCrashLoop {
		t.Fatalf("manual start marker hid subsequent external start: %+v", d)
	}
}

func TestControlCancellationStopsDockerRequest(t *testing.T) {
	c := testConfig()
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "running"}}}
	entered := make(chan struct{})
	engine := manualEngine{fakeEngine: f, stop: func(ctx context.Context, _ string) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	r := Runner{Engine: engine, Config: c}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := controlRequest{ctx: ctx, id: "a", command: CommandStop, reply: make(chan error, 1)}
	p := NewPolicy(c)
	go r.handleControl(context.Background(), request, p, make(chan struct{}, 1), make(chan Event, 4))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("command never reached Docker")
	}
	cancel()
	select {
	case err := <-request.reply:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled command blocked waiting for a UI consumer")
	}
}

func TestControllerReturnsAfterRunnerShutdown(t *testing.T) {
	controls := NewController()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 4)
	done := make(chan error, 1)
	r := Runner{Engine: &fakeEngine{containers: map[string]Sample{}}, Config: testConfig(), Controls: controls}
	go func() { done <- r.Run(ctx, events) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	commandDone := make(chan error, 1)
	go func() { commandDone <- controls.Do(context.Background(), "a", CommandStop) }()
	select {
	case err := <-commandDone:
		if err == nil {
			t.Fatal("stopped runner accepted command")
		}
	case <-time.After(time.Second):
		t.Fatal("command against stopped runner blocked indefinitely")
	}
}

func TestPendingCommandsRejectedWithoutBlocking(t *testing.T) {
	requests := make(chan controlRequest, 1)
	reply := make(chan error, 1)
	requests <- controlRequest{reply: reply}
	rejectPending(requests)
	select {
	case err := <-reply:
		if err == nil {
			t.Fatal("pending command was not rejected")
		}
	default:
		t.Fatal("pending command caller was left waiting")
	}
}

func TestFailedStartResultPersistenceLeavesRecoveryPaused(t *testing.T) {
	c := testConfig()
	f := &fakeEngine{containers: map[string]Sample{"a": {ID: "a", State: "exited"}}}
	started := false
	engine := manualEngine{fakeEngine: f, start: func(context.Context, string) error {
		started = true
		return nil
	}}
	var durable Checkpoint
	journal := journalStub{record: func(_ context.Context, event Event, checkpoint *Checkpoint) error {
		if event.Action == ActionStarted {
			return errors.New("failed to save completion")
		}
		durable = *checkpoint
		return nil
	}}
	p := NewPolicy(c)
	p.Restore(Checkpoint{Version: 1, Attempts: 2})
	r := Runner{Engine: engine, Config: c, Journal: journal}
	if err := r.control(context.Background(), "a", CommandStart, p, make(chan struct{}, 1), make(chan Event, 4)); err == nil {
		t.Fatal("lost completion was reported as success")
	}
	if !started || !durable.RecoveryPaused || !p.Snapshot().RecoveryPaused || p.Snapshot().Attempts != 2 {
		t.Fatalf("ambiguous completion did not fail closed: durable=%+v live=%+v", durable, p.Snapshot())
	}
}

func TestCanceledCallDoesNotInvokeOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Runner{Config: testConfig()}
	err := r.call(ctx, make(chan struct{}, 1), time.Second, func(context.Context) error {
		t.Fatal("operation invoked after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context cancellation", err)
	}
}
