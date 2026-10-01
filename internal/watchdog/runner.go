package watchdog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
)

var errNoLongerEligible = errors.New("container is no longer eligible for recovery")

type Runner struct {
	Engine   Engine
	Config   Config
	Journal  Journal
	Controls *Controller
}

type worker struct {
	cancel   context.CancelFunc
	done     chan struct{}
	name     string
	commands chan controlRequest
}

// Run owns membership, workers own policy, and the caller owns aggregation.
// The event channel closes only after all workers have stopped.
func (r Runner) Run(ctx context.Context, events chan<- Event) error {
	defer close(events)
	if r.Controls != nil {
		defer r.Controls.stop()
	}
	if err := r.Config.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	workers := make(map[string]worker)
	slots := make(chan struct{}, r.Config.MaxConcurrent)
	var group sync.WaitGroup
	var requests <-chan controlRequest
	if r.Controls != nil {
		requests = r.Controls.requests
	}
	defer func() {
		cancel()
		group.Wait()
	}()

	discover := func() error {
		containers, err := r.list(ctx, slots)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(containers))
		for _, item := range containers {
			present[item.ID] = true
			if _, exists := workers[item.ID]; exists {
				continue
			}
			adopt := item.State == container.StateRunning || item.State == container.StateRestarting
			// Interactive controls also list stopped containers. Policy will
			// not auto-recover an exit without a known or explicit prior start.
			if r.Controls != nil {
				adopt = true
			}
			if !adopt && item.State == container.StateExited && r.Journal != nil && r.Config.RecoverExited {
				checkpoint, err := r.load(ctx, item.ID)
				if err != nil {
					return fmt.Errorf("load recovery state: %w", err)
				}
				adopt = checkpoint.Initialized
			}
			if !adopt {
				continue
			}
			workerCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			commands := make(chan controlRequest, 1)
			workers[item.ID] = worker{cancel: stop, done: done, name: item.Name, commands: commands}
			group.Add(1)
			go func() {
				defer group.Done()
				defer close(done)
				r.monitor(workerCtx, item, slots, events, commands)
			}()
		}
		for id, active := range workers {
			if present[id] {
				continue
			}
			active.cancel()
			// Join before removal, so no late sample can resurrect a row.
			<-active.done
			delete(workers, id)
			event := Event{
				Time:     time.Now(),
				Sample:   Sample{ID: id, Name: active.name},
				Decision: Decision{Status: StatusRemoved},
				Removed:  true,
			}
			r.publish(ctx, events, event, nil)
		}
		return nil
	}

	if err := discover(); err != nil {
		return fmt.Errorf("initial Docker discovery: %w", err)
	}
	ticker := time.NewTicker(r.Config.DiscoveryInterval)
	defer ticker.Stop()
	discoveryFailed := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case request := <-requests:
			if request.ctx.Err() != nil {
				request.reply <- request.ctx.Err()
				continue
			}
			active, exists := workers[request.id]
			if !exists {
				request.reply <- errors.New("container is no longer tracked")
				continue
			}
			select {
			case active.commands <- request:
			default:
				request.reply <- errors.New("another command is already queued for this container")
			}
		case <-ticker.C:
			err := discover()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				discoveryFailed = true
				r.publish(ctx, events, Event{
					Time:     time.Now(),
					Decision: Decision{Status: StatusDiscoveryError},
					Error:    err.Error(),
				}, nil)
			} else if discoveryFailed {
				discoveryFailed = false
				r.publish(ctx, events, Event{
					Time:     time.Now(),
					Decision: Decision{Status: StatusDiscoveryRestored},
				}, nil)
			}
		}
	}
}

func (r Runner) list(ctx context.Context, slots chan struct{}) ([]Container, error) {
	var result []Container
	err := r.call(ctx, slots, r.Config.APITimeout, func(ctx context.Context) error {
		var err error
		result, err = r.Engine.List(ctx)
		return err
	})
	return result, err
}

func (r Runner) inspect(ctx context.Context, slots chan struct{}, id string) (Sample, error) {
	var sample Sample
	err := r.call(ctx, slots, r.Config.APITimeout, func(ctx context.Context) error {
		var err error
		sample, err = r.Engine.Inspect(ctx, id)
		return err
	})
	return sample, err
}

func (r Runner) call(ctx context.Context, slots chan struct{}, timeout time.Duration, operation func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-slots }()
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return operation(ctx)
}

func (r Runner) load(ctx context.Context, id string) (Checkpoint, error) {
	if r.Journal == nil {
		return Checkpoint{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.Config.APITimeout)
	defer cancel()
	return r.Journal.Load(ctx, id)
}

func (r Runner) monitor(ctx context.Context, item Container, slots chan struct{}, events chan<- Event, commands <-chan controlRequest) {
	defer rejectPending(commands)
	policy := NewPolicy(r.Config)
	ticker := time.NewTicker(r.Config.PollInterval)
	defer ticker.Stop()

	// Fail closed while durable state cannot be loaded.
	for {
		checkpoint, err := r.load(ctx, item.ID)
		if err == nil {
			policy.Restore(checkpoint)
			break
		}
		emit(ctx, events, Event{
			Time: time.Now(), Sample: Sample{ID: item.ID, Name: item.Name},
			Decision: Decision{Status: StatusStorageError}, Error: err.Error(),
		})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	for {
		// Prefer queued user intent over beginning another automatic poll.
		select {
		case request := <-commands:
			r.handleControl(ctx, request, policy, slots, events)
			continue
		default:
		}
		r.poll(ctx, item, policy, slots, events)
		select {
		case <-ctx.Done():
			return
		case request := <-commands:
			r.handleControl(ctx, request, policy, slots, events)
		case <-ticker.C:
		}
	}
}

func (r Runner) poll(ctx context.Context, item Container, policy *Policy, slots chan struct{}, events chan<- Event) {
	sample, err := r.inspect(ctx, slots, item.ID)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		policy.Unknown()
		r.publish(ctx, events, Event{
			Time:     time.Now(),
			Sample:   Sample{ID: item.ID, Name: item.Name},
			Decision: Decision{Status: StatusUnknown, Attempts: policy.attempts, RecoveryPaused: policy.recoveryPaused},
			Error:    err.Error(),
		}, nil)
		return
	}
	if sample.State == container.StateRunning {
		err = r.call(ctx, slots, r.Config.APITimeout, func(ctx context.Context) error {
			return r.Engine.Stats(ctx, &sample)
		})
		if err != nil {
			sample.StatsError = err.Error()
		} else {
			sample.StatsOK = true
		}
	}
	if ctx.Err() != nil {
		return
	}

	now := time.Now()
	event := Event{Time: now, Sample: sample, Decision: policy.Evaluate(sample, now)}
	checkpoint := policy.Snapshot()
	if !event.Restart {
		r.publish(ctx, events, event, &checkpoint)
		return
	}

	// Commit the reservation BEFORE Docker can mutate anything. If the
	// process dies mid-request, its retry cap and cooldown still survive.
	checkpoint.NextRetry = now.Add(r.Config.RestartTimeout + policy.backoff())
	if !r.publish(ctx, events, event, &checkpoint) {
		return
	}
	err = r.restart(ctx, slots, item.ID)
	event.Time = time.Now()
	switch {
	case errors.Is(err, errNoLongerEligible):
		policy.SkipAttempt()
		event.Action = ActionRestartSkipped
	case err != nil:
		policy.CompleteAttempt(event.Time, false)
		event.Action = ActionRestartFailed
		event.Error = err.Error()
	default:
		policy.CompleteAttempt(event.Time, true)
		event.Action = ActionRestarted
	}
	event.Attempts = policy.attempts
	event.NextRetry = policy.nextRetry
	checkpoint = policy.Snapshot()
	r.publish(ctx, events, event, &checkpoint)
}

func (r Runner) restart(ctx context.Context, slots chan struct{}, id string) error {
	return r.call(ctx, slots, r.Config.RestartTimeout, func(ctx context.Context) error {
		// Recheck after queueing: recovery, pause, or policy changes can make
		// the original sample stale.
		fresh, err := r.Engine.Inspect(ctx, id)
		if err != nil {
			return err
		}
		if !recoveryEligible(fresh, r.Config.RecoverExited) || dockerManagesRestarts(fresh) {
			return errNoLongerEligible
		}
		return r.Engine.Restart(ctx, id)
	})
}

func (r Runner) publish(ctx context.Context, events chan<- Event, event Event, checkpoint *Checkpoint) bool {
	if r.Journal != nil {
		writeCtx, cancel := context.WithTimeout(ctx, r.Config.APITimeout)
		err := r.Journal.Record(writeCtx, event, checkpoint)
		cancel()
		if err != nil {
			event.Status = StatusStorageError
			event.Error = "persist observation: " + err.Error()
			event.Restart = false
			emit(ctx, events, event)
			return false
		}
	}
	emit(ctx, events, event)
	return ctx.Err() == nil
}

func emit(ctx context.Context, events chan<- Event, event Event) {
	if ctx.Err() != nil {
		return
	}
	select {
	case events <- event:
	case <-ctx.Done():
	}
}
