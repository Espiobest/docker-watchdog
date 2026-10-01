package watchdog

import (
	"testing"
	"time"
)

func TestBackoffBudgetAndStableReset(t *testing.T) {
	c := DefaultConfig()
	c.AutoRestart = true
	c.BaseBackoff = time.Second
	c.MaxBackoff = 4 * time.Second
	c.MaxRetries = 2
	c.StableReset = 10 * time.Second
	p := NewPolicy(c)
	now := time.Unix(1000, 0)
	s := Sample{State: "running", Health: "unhealthy"}
	if d := p.Evaluate(s, now); d.Restart || d.Status != "backoff" || !d.NextRetry.Equal(now.Add(time.Second)) {
		t.Fatalf("initial: %+v", d)
	}
	if d := p.Evaluate(s, now.Add(999*time.Millisecond)); d.Restart {
		t.Fatal("restarted before backoff elapsed")
	}
	if d := p.Evaluate(s, now.Add(time.Second)); !d.Restart || d.Attempts != 1 || !d.NextRetry.Equal(now.Add(3*time.Second)) {
		t.Fatalf("first attempt: %+v", d)
	}
	if d := p.Evaluate(s, now.Add(3*time.Second)); !d.Restart || d.Attempts != 2 {
		t.Fatalf("second attempt: %+v", d)
	}
	if d := p.Evaluate(s, now.Add(time.Hour)); d.Restart || d.Status != "retry-exhausted" {
		t.Fatalf("cap must not expire with time alone: %+v", d)
	}
	s.Health = "healthy"
	p.Evaluate(s, now.Add(2*time.Hour))
	if d := p.Evaluate(s, now.Add(2*time.Hour+9*time.Second)); d.Attempts != 2 {
		t.Fatal("budget reset too early")
	}
	p.Unknown()
	if d := p.Evaluate(s, now.Add(2*time.Hour+10*time.Second)); d.Attempts != 2 {
		t.Fatal("unknown period counted as healthy")
	}
	if d := p.Evaluate(s, now.Add(2*time.Hour+20*time.Second)); d.Attempts != 0 {
		t.Fatalf("stable reset: %+v", d)
	}
}

func TestNoUnsafeRestarts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sample        Sample
		auto, recover bool
	}{
		{"observe", Sample{State: "running", Health: "unhealthy"}, false, true},
		{"healthy", Sample{State: "running", Health: "healthy"}, true, true},
		{"starting", Sample{State: "running", Health: "starting"}, true, true},
		{"paused", Sample{State: "paused", Health: "unhealthy"}, true, true},
		{"docker-restarting", Sample{State: "restarting", Health: "unhealthy"}, true, true},
		{"docker-policy", Sample{State: "running", Health: "unhealthy", RestartPolicy: "always"}, true, true},
		{"clean-exit", Sample{State: "exited", ExitCode: 0}, true, true},
		{"exit-recovery-disabled", Sample{State: "exited", ExitCode: 1}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			c.AutoRestart = tc.auto
			c.RecoverExited = tc.recover
			p := NewPolicy(c)
			now := time.Now()
			p.Evaluate(tc.sample, now)
			if d := p.Evaluate(tc.sample, now.Add(time.Hour)); d.Restart || d.Attempts != 0 {
				t.Fatalf("unsafe restart: %+v", d)
			}
		})
	}
}

func TestCrashLoopAndWindowExpiry(t *testing.T) {
	c := DefaultConfig()
	c.AutoRestart = true
	p := NewPolicy(c)
	now := time.Now()
	s := Sample{State: "restarting", RestartCount: 100, RestartPolicy: "always"}
	if d := p.Evaluate(s, now); d.Status == "crash-loop" {
		t.Fatal("historical restarts are not a new loop")
	}
	s.RestartCount += 3
	if d := p.Evaluate(s, now.Add(time.Second)); d.Status != "crash-loop" || d.Restart {
		t.Fatalf("loop: %+v", d)
	}
	s.State = "running"
	s.Health = "healthy"
	if d := p.Evaluate(s, now.Add(c.CrashWindow+time.Second)); d.Status != "healthy" {
		t.Fatalf("window did not expire: %+v", d)
	}
}

func TestWatchdogRestartDoesNotCountAsCrash(t *testing.T) {
	c := DefaultConfig()
	c.CrashThreshold = 1
	p := NewPolicy(c)
	now := time.Now()
	s := Sample{State: "running", Health: "healthy", StartedAt: now}
	p.Evaluate(s, now)
	p.CompleteAttempt(now, true)
	s.StartedAt = now.Add(time.Second)
	if d := p.Evaluate(s, now.Add(time.Second)); d.Status == "crash-loop" {
		t.Fatal("own restart counted as crash")
	}
	s.StartedAt = now.Add(2 * time.Second)
	if d := p.Evaluate(s, now.Add(2*time.Second)); d.Status != "crash-loop" {
		t.Fatal("external restart not counted")
	}
}

func TestUnexpectedExitRecovery(t *testing.T) {
	c := DefaultConfig()
	c.AutoRestart = true
	c.RecoverExited = true
	p := NewPolicy(c)
	now := time.Now()
	s := Sample{State: "exited", ExitCode: 1}
	p.Evaluate(s, now)
	if d := p.Evaluate(s, now.Add(c.BaseBackoff)); !d.Restart {
		t.Fatalf("nonzero exit not recovered: %+v", d)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	c := DefaultConfig()
	c.AutoRestart = true
	c.MaxRetries = 20
	c.BaseBackoff = time.Second
	c.MaxBackoff = 3 * time.Second
	p := NewPolicy(c)
	now := time.Now()
	s := Sample{State: "running", Health: "unhealthy"}
	d := p.Evaluate(s, now)
	for i := 0; i < 10; i++ {
		now = d.NextRetry
		d = p.Evaluate(s, now)
		if !d.Restart || d.NextRetry.Sub(now) > c.MaxBackoff {
			t.Fatalf("invalid backoff: %+v", d)
		}
	}
}

func TestObservedStartsInterruptStableReset(t *testing.T) {
	for _, change := range []string{"started-at", "restart-count", "own-start"} {
		t.Run(change, func(t *testing.T) {
			c := DefaultConfig()
			c.StableReset = 10 * time.Second
			c.CrashThreshold = 3
			p := NewPolicy(c)
			p.Restore(Checkpoint{Attempts: c.MaxRetries})
			now := time.Unix(1000, 0)
			s := Sample{State: "running", Health: "healthy", StartedAt: now}
			p.Evaluate(s, now)
			switch change {
			case "started-at":
				s.StartedAt = now.Add(9 * time.Second)
			case "restart-count":
				s.RestartCount++
			case "own-start":
				p.CompleteAttempt(now.Add(8*time.Second), true)
				s.StartedAt = now.Add(9 * time.Second)
			}
			if d := p.Evaluate(s, now.Add(9*time.Second)); d.Status != StatusHealthy {
				t.Fatalf("single restart should be below crash threshold: %+v", d)
			}
			if d := p.Evaluate(s, now.Add(10*time.Second)); d.Attempts != c.MaxRetries {
				t.Fatalf("interrupted uptime reset retry budget: %+v", d)
			}
			if d := p.Evaluate(s, now.Add(19*time.Second)); d.Attempts != 0 {
				t.Fatalf("continuous health did not reset budget: %+v", d)
			}
		})
	}
}

func TestCooldownBeginsWhenRestartRequestCompletes(t *testing.T) {
	for _, succeeded := range []bool{false, true} {
		c := DefaultConfig()
		c.AutoRestart = true
		p := NewPolicy(c)
		now := time.Unix(1000, 0)
		s := Sample{State: "running", Health: "unhealthy"}
		p.Evaluate(s, now)
		started := now.Add(c.BaseBackoff)
		if d := p.Evaluate(s, started); !d.Restart {
			t.Fatalf("first restart was not reserved: %+v", d)
		}
		finished := started.Add(time.Minute)
		p.CompleteAttempt(finished, succeeded)
		deadline := finished.Add(2 * c.BaseBackoff)
		if d := p.Evaluate(s, deadline.Add(-time.Nanosecond)); d.Restart || !d.NextRetry.Equal(deadline) {
			t.Fatalf("succeeded=%t: cooldown did not start after completion: %+v", succeeded, d)
		}
		if d := p.Evaluate(s, deadline); !d.Restart || d.Attempts != 2 {
			t.Fatalf("succeeded=%t: retry not allowed after cooldown: %+v", succeeded, d)
		}
	}
}

func TestRestorePreservesBudgetButDoesNotCountDowntimeAsHealthy(t *testing.T) {
	c := DefaultConfig()
	c.AutoRestart = true
	original := NewPolicy(c)
	original.Restore(Checkpoint{Attempts: c.MaxRetries})
	now := time.Unix(1000, 0)
	healthy := Sample{State: "running", Health: "healthy", StartedAt: now}
	original.Evaluate(healthy, now)
	checkpoint := original.Snapshot()
	restored := NewPolicy(c)
	restored.Restore(checkpoint)
	resumed := now.Add(24 * time.Hour)
	unhealthy := healthy
	unhealthy.Health = "unhealthy"
	if d := restored.Evaluate(unhealthy, resumed); d.Status != StatusRetryExhausted || d.Restart {
		t.Fatalf("process restart restored recovery budget: %+v", d)
	}
	restored.Restore(checkpoint)
	if d := restored.Evaluate(healthy, resumed); d.Attempts != c.MaxRetries {
		t.Fatalf("downtime counted as observed good health: %+v", d)
	}
	if d := restored.Evaluate(healthy, resumed.Add(c.StableReset)); d.Attempts != 0 {
		t.Fatalf("new continuous health period did not reset budget: %+v", d)
	}
}
