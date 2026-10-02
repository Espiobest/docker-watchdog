package watchdog

// Status describes the watchdog's assessment, not Docker's lifecycle state.
// String constants remain readable in logs and JSON without custom marshaling.
type Status string

const (
	StatusUnknown              Status = "unknown"
	StatusCreated              Status = "created"
	StatusRunning              Status = "running"
	StatusHealthy              Status = "healthy"
	StatusUnhealthy            Status = "unhealthy"
	StatusStarting             Status = "starting"
	StatusPaused               Status = "paused"
	StatusRestarting           Status = "restarting"
	StatusStopped              Status = "stopped"
	StatusCrashed              Status = "crashed"
	StatusDead                 Status = "dead"
	StatusRemoving             Status = "removing"
	StatusRemoved              Status = "removed"
	StatusCrashLoop            Status = "crash-loop"
	StatusBackoff              Status = "backoff"
	StatusRetryExhausted       Status = "retry-exhausted"
	StatusDiscoveryError       Status = "discovery-error"
	StatusDiscoveryRestored    Status = "discovery-restored"
	StatusStorageError         Status = "storage-error"
	StatusEventStreamError     Status = "event-stream-error"
	StatusEventStreamConnected Status = "event-stream-connected"
)

type Action string

const (
	ActionRestarted        Action = "restarted"
	ActionRestartFailed    Action = "restart-failed"
	ActionRestartSkipped   Action = "restart-skipped"
	ActionStopRequested    Action = "stop-requested"
	ActionStartRequested   Action = "start-requested"
	ActionRestartRequested Action = "manual-restart-requested"
	ActionStopped          Action = "stopped"
	ActionStarted          Action = "started"
	ActionManualRestarted  Action = "manually-restarted"
	ActionRecoveryPaused   Action = "recovery-paused"
	ActionRecoveryResumed  Action = "recovery-resumed"
	ActionControlFailed    Action = "control-failed"
)
