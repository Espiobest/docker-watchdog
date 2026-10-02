package watchdog

// LogLine is one bounded chunk from stdout/stderr (combined for TTY containers).
type LogLine struct {
	// Ready marks successful subscription before the first output line.
	Ready  bool
	Stream string
	Text   string
}
