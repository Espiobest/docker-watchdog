package watchdog

import (
	"time"

	"github.com/moby/moby/api/types/container"
)

// Policy is owned by one worker. Waiting for a deadline never occupies an API
// slot or blocks monitoring of other containers.
type Policy struct {
	config         Config
	attempts       int
	nextRetry      time.Time
	stableSince    time.Time
	initialized    bool
	lastCount      int
	lastStart      time.Time
	crashes        []time.Time
	expectOwnStart bool
}

func NewPolicy(config Config) *Policy {
	return &Policy{config: config}
}

// Unknown breaks the continuous-health period without authorizing a restart.
func (p *Policy) Unknown() {
	p.stableSince = time.Time{}
}

// CompleteAttempt starts cooldown after the request finishes, even on failure.
func (p *Policy) CompleteAttempt(now time.Time, succeeded bool) {
	p.nextRetry = now.Add(p.backoff())
	if succeeded {
		p.expectOwnStart = true
	}
}

func (p *Policy) SkipAttempt() {
	p.attempts--
	p.nextRetry = time.Time{}
}

func (p *Policy) Evaluate(sample Sample, now time.Time) Decision {
	looping := p.observeStarts(sample, now)
	decision := classify(sample)
	if looping && (sample.State == container.StateRunning ||
		sample.State == container.StateRestarting || sample.State == container.StateExited) {
		decision.Status = StatusCrashLoop
		decision.Reason = "repeated starts detected within crash window"
	}

	p.observeStability(decision.Status, now)
	decision.Attempts = p.attempts
	decision.NextRetry = p.nextRetry

	// A loop alone is an alert: do not kill a currently healthy recovery.
	if !recoveryEligible(sample, p.config.RecoverExited) {
		return decision
	}
	if dockerManagesRestarts(sample) {
		decision.Reason += "; recovery delegated to Docker restart policy"
		return decision
	}
	if !p.config.AutoRestart {
		decision.Reason += "; observe-only mode"
		return decision
	}
	if p.attempts >= p.config.MaxRetries {
		decision.Status = StatusRetryExhausted
		decision.NextRetry = time.Time{}
		return decision
	}
	if p.nextRetry.IsZero() {
		p.nextRetry = now.Add(p.config.BaseBackoff)
	}
	if now.Before(p.nextRetry) {
		decision.Status = StatusBackoff
		decision.NextRetry = p.nextRetry
		return decision
	}

	// Reserve before the API call. A timeout does not prove that Docker did
	// not restart the container, so failed requests consume budget too.
	p.attempts++
	p.nextRetry = now.Add(p.backoff())
	decision.Status = StatusRestarting
	decision.Restart = true
	decision.Attempts = p.attempts
	decision.NextRetry = p.nextRetry
	return decision
}

func classify(sample Sample) Decision {
	switch sample.State {
	case container.StatePaused:
		return Decision{Status: StatusPaused}
	case container.StateRestarting:
		return Decision{Status: StatusRestarting, Reason: "Docker is restarting the container"}
	case container.StateCreated:
		return Decision{Status: StatusCreated}
	case container.StateRemoving:
		return Decision{Status: StatusRemoving}
	case container.StateDead:
		return Decision{Status: StatusDead, Reason: "container requires manual intervention"}
	case container.StateExited:
		if sample.ExitCode != 0 {
			return Decision{Status: StatusCrashed, Reason: "container exited with a nonzero status"}
		}
		return Decision{Status: StatusStopped}
	case container.StateRunning:
		// Health is meaningful only while the container is running.
	default:
		return Decision{Status: StatusUnknown, Reason: "unrecognized Docker lifecycle state"}
	}

	switch sample.Health {
	case container.Unhealthy:
		return Decision{Status: StatusUnhealthy, Reason: "Docker health check failed"}
	case container.Starting:
		return Decision{Status: StatusStarting}
	case container.Healthy:
		return Decision{Status: StatusHealthy}
	case container.NoHealthcheck, "":
		return Decision{Status: StatusRunning, Reason: "no Docker health check configured"}
	default:
		return Decision{Status: StatusUnknown, Reason: "unrecognized Docker health status"}
	}
}

func recoveryEligible(sample Sample, recoverExited bool) bool {
	if sample.State == container.StateRunning && sample.Health == container.Unhealthy {
		return true
	}
	return recoverExited && sample.State == container.StateExited && sample.ExitCode != 0
}

func dockerManagesRestarts(sample Sample) bool {
	return sample.RestartPolicy != "" && sample.RestartPolicy != container.RestartPolicyDisabled
}

func (p *Policy) observeStability(status Status, now time.Time) {
	if status != StatusHealthy && status != StatusRunning {
		p.stableSince = time.Time{}
		return
	}
	if p.attempts == 0 {
		p.nextRetry = time.Time{}
	}
	if p.stableSince.IsZero() {
		p.stableSince = now
	}
	if now.Sub(p.stableSince) >= p.config.StableReset {
		p.attempts = 0
		p.nextRetry = time.Time{}
	}
}

func (p *Policy) observeStarts(sample Sample, now time.Time) bool {
	if p.initialized {
		delta := sample.RestartCount - p.lastCount
		startChanged := !sample.StartedAt.IsZero() && !p.lastStart.IsZero() &&
			!sample.StartedAt.Equal(p.lastStart)
		if startChanged || delta > 0 {
			// Continuous health must be observed within one process lifetime.
			p.stableSince = time.Time{}
		}
		if startChanged {
			if delta <= 0 && !p.expectOwnStart {
				delta = 1
			}
			p.expectOwnStart = false
		}
		// Bound memory even if the counter jumps after a long outage.
		delta = min(delta, p.config.CrashThreshold)
		for range max(delta, 0) {
			p.crashes = append(p.crashes, now)
		}
	}
	p.initialized = true
	p.lastCount = sample.RestartCount
	p.lastStart = sample.StartedAt

	recent := p.crashes[:0]
	for _, started := range p.crashes {
		if now.Sub(started) < p.config.CrashWindow {
			recent = append(recent, started)
		}
	}
	p.crashes = recent
	if len(p.crashes) > p.config.CrashThreshold {
		p.crashes = p.crashes[len(p.crashes)-p.config.CrashThreshold:]
	}
	return len(p.crashes) >= p.config.CrashThreshold
}

func (p *Policy) backoff() time.Duration {
	delay := p.config.BaseBackoff
	for attempt := 0; attempt < p.attempts && delay < p.config.MaxBackoff; attempt++ {
		if delay > p.config.MaxBackoff/2 {
			return p.config.MaxBackoff
		}
		delay *= 2
	}
	return delay
}
