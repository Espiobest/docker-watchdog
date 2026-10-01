package watchdog

import (
	"context"
	"fmt"
	"time"

	"github.com/moby/moby/api/types/container"
)

type Container struct {
	ID    string
	Name  string
	State container.ContainerState
}

type Sample struct {
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	State         container.ContainerState    `json:"state"`
	Health        container.HealthStatus      `json:"health"`
	ExitCode      int                         `json:"exit_code"`
	RestartCount  int                         `json:"docker_restarts"`
	RestartPolicy container.RestartPolicyMode `json:"restart_policy"`
	StartedAt     time.Time                   `json:"started_at"`
	CPUPercent    float64                     `json:"cpu_percent"`
	MemoryBytes   uint64                      `json:"memory_bytes"`
	MemoryLimit   uint64                      `json:"memory_limit"`
	NetworkRX     uint64                      `json:"network_rx_bytes"`
	NetworkTX     uint64                      `json:"network_tx_bytes"`
	StatsOK       bool                        `json:"stats_ok"`
	StatsError    string                      `json:"stats_error,omitempty"`
}

// Engine isolates Docker's transport from the scheduler and recovery policy.
type Engine interface {
	List(context.Context) ([]Container, error)
	Inspect(context.Context, string) (Sample, error)
	Stats(context.Context, *Sample) error
	Restart(context.Context, string) error
}

type Config struct {
	PollInterval      time.Duration
	DiscoveryInterval time.Duration
	APITimeout        time.Duration
	RestartTimeout    time.Duration
	BaseBackoff       time.Duration
	MaxBackoff        time.Duration
	StableReset       time.Duration
	CrashWindow       time.Duration
	CrashThreshold    int
	MaxRetries        int
	MaxConcurrent     int
	AutoRestart       bool
	RecoverExited     bool
}

func DefaultConfig() Config {
	return Config{
		PollInterval: 3 * time.Second, DiscoveryInterval: 5 * time.Second,
		APITimeout: 5 * time.Second, RestartTimeout: 30 * time.Second,
		BaseBackoff: 5 * time.Second, MaxBackoff: time.Minute,
		StableReset: 5 * time.Minute, CrashWindow: time.Minute,
		CrashThreshold: 3, MaxRetries: 3, MaxConcurrent: 8,
	}
}

func (c Config) Validate() error {
	for _, d := range []time.Duration{c.PollInterval, c.DiscoveryInterval, c.APITimeout, c.RestartTimeout, c.BaseBackoff, c.MaxBackoff, c.StableReset, c.CrashWindow} {
		if d <= 0 {
			return fmt.Errorf("all durations must be positive")
		}
	}
	if c.MaxBackoff < c.BaseBackoff {
		return fmt.Errorf("max-backoff must be >= backoff")
	}
	if c.MaxRetries < 1 || c.CrashThreshold < 1 || c.MaxConcurrent < 1 {
		return fmt.Errorf("retries, crash-threshold and concurrency must be >= 1")
	}
	if c.RestartTimeout <= 10*time.Second {
		return fmt.Errorf("restart-timeout must exceed Docker's 10s stop grace period")
	}
	return nil
}

type Decision struct {
	Status         Status    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	Attempts       int       `json:"attempts"`
	NextRetry      time.Time `json:"next_retry"`
	Restart        bool      `json:"-"`
	RecoveryPaused bool      `json:"recovery_paused"`
}

type Event struct {
	Time time.Time `json:"time"`
	Sample
	Decision
	Action  Action `json:"action,omitempty"`
	Error   string `json:"error,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}
