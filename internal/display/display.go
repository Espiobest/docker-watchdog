package display

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"docker-watchdog/internal/watchdog"
)

// Logger is used by one consumer; no locks or shared mutable state are needed.
type Logger struct {
	mode     string
	out      io.Writer
	previous map[string]transition
}

type transition struct {
	status       watchdog.Status
	reason       string
	failure      string
	statsFailure string
	attempts     int
}

func New(mode string, out io.Writer) *Logger {
	return &Logger{
		mode:     mode,
		out:      out,
		previous: make(map[string]transition),
	}
}

// Accept emits every sample as JSON, or only transitions/actions as text.
func (l *Logger) Accept(event watchdog.Event) error {
	if l.mode == "json" {
		return json.NewEncoder(l.out).Encode(event)
	}

	current := transition{
		status:       event.Status,
		reason:       event.Reason,
		failure:      event.Error,
		statsFailure: event.StatsError,
		attempts:     event.Attempts,
	}
	previous, exists := l.previous[event.ID]
	l.previous[event.ID] = current
	if event.Removed {
		delete(l.previous, event.ID)
	}
	if exists && previous == current && event.Action == "" {
		return nil
	}

	name := event.Name
	if name == "" {
		name = event.ID
	}
	if name == "" {
		name = "watchdog"
	}
	from := "discovered"
	if exists {
		from = string(previous.status)
	}

	var line strings.Builder
	fmt.Fprintf(&line, "%s container=%q %s -> %s attempts=%d",
		event.Time.Format(time.RFC3339), name, from, event.Status, event.Attempts)
	if event.Action != "" {
		fmt.Fprintf(&line, " action=%s", event.Action)
	}
	if event.Status == watchdog.StatusBackoff {
		fmt.Fprintf(&line, " next=%s", event.NextRetry.Format(time.RFC3339))
	}
	if event.Reason != "" {
		fmt.Fprintf(&line, " reason=%q", event.Reason)
	}
	if event.Error != "" {
		fmt.Fprintf(&line, " error=%q", event.Error)
	}
	if event.StatsError != "" {
		fmt.Fprintf(&line, " stats_error=%q", event.StatsError)
	}

	_, err := fmt.Fprintln(l.out, line.String())
	return err
}
