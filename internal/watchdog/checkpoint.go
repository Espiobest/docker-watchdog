package watchdog

import (
	"context"
	"time"
)

// Checkpoint persists recovery decisions across watchdog process restarts.
// StableSince is deliberately excluded: downtime is not observed good health.
type Checkpoint struct {
	Version        int         `json:"version"`
	Attempts       int         `json:"attempts"`
	NextRetry      time.Time   `json:"next_retry"`
	Initialized    bool        `json:"initialized"`
	LastCount      int         `json:"last_count"`
	LastStart      time.Time   `json:"last_start"`
	Crashes        []time.Time `json:"crashes"`
	ExpectOwnStart bool        `json:"expect_own_start"`
	RecoveryPaused bool        `json:"recovery_paused"`
	SeenRunning    bool        `json:"seen_running"`
}

// Journal belongs to the consumer: storage implementations need only load
// policy state and atomically record an observation with its checkpoint.
type Journal interface {
	Load(context.Context, string) (Checkpoint, error)
	Record(context.Context, Event, *Checkpoint) error
}

func (p *Policy) Snapshot() Checkpoint {
	return Checkpoint{
		Version:  1,
		Attempts: p.attempts, NextRetry: p.nextRetry, Initialized: p.initialized,
		LastCount: p.lastCount, LastStart: p.lastStart,
		Crashes: append([]time.Time(nil), p.crashes...), ExpectOwnStart: p.expectOwnStart,
		RecoveryPaused: p.recoveryPaused, SeenRunning: p.seenRunning,
	}
}

func (p *Policy) Restore(checkpoint Checkpoint) {
	p.attempts = checkpoint.Attempts
	p.nextRetry = checkpoint.NextRetry
	p.initialized = checkpoint.Initialized
	p.lastCount = checkpoint.LastCount
	p.lastStart = checkpoint.LastStart
	p.crashes = append([]time.Time(nil), checkpoint.Crashes...)
	p.expectOwnStart = checkpoint.ExpectOwnStart
	p.recoveryPaused = checkpoint.RecoveryPaused
	p.seenRunning = checkpoint.SeenRunning
	// Before manual controls, only running containers could be initialized.
	if checkpoint.Version == 0 && checkpoint.Initialized {
		p.seenRunning = true
	}
	p.stableSince = time.Time{}
}
