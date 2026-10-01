package watchdog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
)

type Command string

const (
	CommandStop           Command = "stop"
	CommandStart          Command = "start"
	CommandRestart        Command = "restart"
	CommandPauseRecovery  Command = "pause recovery"
	CommandResumeRecovery Command = "resume recovery"
)

// ContainerController is optional: read-only engines can still be monitored.
type ContainerController interface {
	Start(context.Context, string) error
	Stop(context.Context, string) error
}

// Controller routes explicit user commands through the owning worker, so
// manual operations and automatic recovery cannot run concurrently for an ID.
type Controller struct {
	requests chan controlRequest
	done     chan struct{}
	stopOnce sync.Once
}

type controlRequest struct {
	ctx     context.Context
	id      string
	command Command
	reply   chan error
}

func NewController() *Controller {
	return &Controller{requests: make(chan controlRequest), done: make(chan struct{})}
}

func (c *Controller) stop() {
	c.stopOnce.Do(func() { close(c.done) })
}

func (c *Controller) Do(ctx context.Context, id string, command Command) error {
	request := controlRequest{ctx: ctx, id: id, command: command, reply: make(chan error, 1)}
	select {
	case c.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("watchdog has stopped")
	}

	select {
	case err := <-request.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("watchdog has stopped")
	}
}

func rejectPending(requests <-chan controlRequest) {
	for {
		select {
		case request, ok := <-requests:
			if !ok {
				return
			}
			request.reply <- errors.New("container worker stopped before command could run")
		default:
			return
		}
	}
}

func (r Runner) handleControl(ctx context.Context, request controlRequest, policy *Policy, slots chan struct{}, events chan<- Event) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(request.ctx, cancel)
	defer stop()
	defer cancel()
	if err := request.ctx.Err(); err != nil {
		request.reply <- err
		return
	}

	err := r.control(ctx, request.id, request.command, policy, slots, events)
	request.reply <- err
}

func (r Runner) control(ctx context.Context, id string, command Command, policy *Policy, slots chan struct{}, events chan<- Event) error {
	// Reading fresh state is required even for pause/resume so every action
	// targets an existing full ID and leaves an accurate audit record.
	sample, err := r.inspect(ctx, slots, id)
	if err != nil {
		return fmt.Errorf("inspect before %s: %w", command, err)
	}
	if err := validateCommand(command, sample.State); err != nil {
		return err
	}

	if command == CommandPauseRecovery || command == CommandResumeRecovery {
		paused := command == CommandPauseRecovery
		return r.setRecoveryPaused(ctx, events, sample, policy, paused)
	}
	engine, ok := r.Engine.(ContainerController)
	if !ok {
		return errors.New("engine does not support manual controls")
	}

	// Persist maintenance mode before touching Docker. Failed or timed-out
	// commands leave recovery paused; the watchdog must not undo a stop.
	policy.recoveryPaused = true
	policy.stableSince = time.Time{}
	event := controlEvent(sample, policy)
	event.Action = requestedAction(command)
	checkpoint := policy.Snapshot()
	if !r.publish(ctx, events, event, &checkpoint) {
		return errors.New("could not persist command; Docker was not changed")
	}

	err = r.call(ctx, slots, r.Config.RestartTimeout, func(ctx context.Context) error {
		fresh, err := r.Engine.Inspect(ctx, id)
		if err != nil {
			return err
		}
		if err := validateCommand(command, fresh.State); err != nil {
			return err
		}
		sample = fresh
		switch command {
		case CommandStop:
			return engine.Stop(ctx, id)
		case CommandStart:
			return engine.Start(ctx, id)
		default:
			return r.Engine.Restart(ctx, id)
		}
	})
	if err != nil {
		event.Time = time.Now()
		event.Action = ActionControlFailed
		event.Error = fmt.Sprintf("%s: %v", command, err)
		r.publish(ctx, events, event, &checkpoint)
		return fmt.Errorf("%s failed; recovery remains paused: %w", command, err)
	}

	if command == CommandStop {
		policy.stoppedByUser = true
		policy.stoppedStart = sample.StartedAt
	} else {
		policy.stoppedByUser = false
		policy.expectOwnStart = true
		policy.seenRunning = true
		policy.recoveryPaused = false
		// Give a manually started process a full cooldown without resetting
		// its automatic retry budget.
		policy.nextRetry = time.Now().Add(policy.backoff())
	}
	fresh, inspectErr := r.inspect(ctx, slots, id)
	if inspectErr == nil {
		sample = fresh
	} else {
		// The command succeeded, but its current state is not yet known.
		sample.State = ""
	}
	event = controlEvent(sample, policy)
	if inspectErr != nil {
		event.Error = fmt.Sprintf("inspect after %s: %v", command, inspectErr)
	}
	event.Action = completedAction(command)
	checkpoint = policy.Snapshot()
	if !r.publish(ctx, events, event, &checkpoint) {
		policy.recoveryPaused = true
		return errors.New("container command completed but saving its result failed; recovery paused")
	}
	return nil
}

func (r Runner) setRecoveryPaused(ctx context.Context, events chan<- Event, sample Sample, policy *Policy, paused bool) error {
	policy.recoveryPaused = paused
	policy.stableSince = time.Time{}
	if !paused {
		policy.nextRetry = time.Now().Add(policy.backoff())
	}
	event := controlEvent(sample, policy)
	event.Action = ActionRecoveryPaused
	if !paused {
		event.Action = ActionRecoveryResumed
	}
	checkpoint := policy.Snapshot()
	if !r.publish(ctx, events, event, &checkpoint) {
		policy.recoveryPaused = true
		return errors.New("could not persist recovery setting; recovery remains paused")
	}
	return nil
}

// No policy evaluation here: confirming a manual action must not reserve an
// unrelated automatic restart or consume another retry.
func controlEvent(sample Sample, policy *Policy) Event {
	decision := policy.classify(sample)
	decision.Attempts = policy.attempts
	decision.RecoveryPaused = policy.recoveryPaused
	decision.NextRetry = policy.nextRetry
	if policy.recoveryPaused {
		decision.Reason = "automatic recovery paused by user"
		decision.NextRetry = time.Time{}
	}
	return Event{Time: time.Now(), Sample: sample, Decision: decision}
}

func validateCommand(command Command, state container.ContainerState) error {
	switch command {
	case CommandPauseRecovery, CommandResumeRecovery:
		return nil
	case CommandStop:
		if state == container.StateRunning || state == container.StateRestarting {
			return nil
		}
	case CommandStart:
		if state == container.StateCreated || state == container.StateExited {
			return nil
		}
	case CommandRestart:
		if state == container.StateRunning || state == container.StateExited || state == container.StateRestarting {
			return nil
		}
	default:
		return fmt.Errorf("unsupported command %q", command)
	}
	return fmt.Errorf("cannot %s a container in state %s", command, state)
}

func requestedAction(command Command) Action {
	switch command {
	case CommandStop:
		return ActionStopRequested
	case CommandStart:
		return ActionStartRequested
	default:
		return ActionRestartRequested
	}
}

func completedAction(command Command) Action {
	switch command {
	case CommandStop:
		return ActionStopped
	case CommandStart:
		return ActionStarted
	default:
		return ActionManualRestarted
	}
}
