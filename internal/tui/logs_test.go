package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"docker-watchdog/internal/watchdog"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestLogsBoundedSanitizedAndScrollable(t *testing.T) {
	m := fixture()
	s := &logSession{cancel: func() {}, follow: true, status: "Live"}
	m.logs = s
	var entries []logEntry
	for i := range 600 {
		entries = append(entries, logEntry{line: watchdog.LogLine{Stream: "out", Text: fmt.Sprintf("\x1b[2Jline %d\r", i)}})
	}
	next, _ := m.updateLogs(logMessage{session: s, entries: entries})
	m = next.(model)
	if len(s.lines) != 500 || s.lines[0].Text != "line 100 " || strings.Contains(s.lines[0].Text, "\x1b") {
		t.Fatalf("invalid retained output: %d %q", len(s.lines), s.lines[0].Text)
	}
	for _, size := range [][2]int{{48, 15}, {80, 24}, {120, 32}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if lipgloss.Height(view) > size[1] || !strings.Contains(ansi.Strip(view), "Ctrl+C quit") {
			t.Fatalf("logs overflow: %v", size)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("wide line %q", line)
			}
		}
	}
	m, _ = key(m, 'g')
	if s.follow || s.offset != 0 {
		t.Fatal("home did not stop following")
	}
	m, _ = key(m, 'f')
	if !s.follow {
		t.Fatal("follow not restored")
	}
	other := &logSession{}
	m.updateLogs(logMessage{session: other, entries: []logEntry{{line: watchdog.LogLine{Text: "stale"}}}})
	if len(s.lines) != 500 {
		t.Fatal("stale session modified history")
	}
}

func TestLeavingLogsCancelsBlockedProducer(t *testing.T) {
	m := fixture()
	started := make(chan string, 1)
	m.options.Logs = func(ctx context.Context, id string, emit func(watchdog.LogLine) error) error {
		started <- id
		for {
			if err := emit(watchdog.LogLine{Text: "busy"}); err != nil {
				return err
			}
		}
	}
	target := m.ordered()[m.cursor].ID
	m, cmd := key(m, 'l')
	if cmd == nil {
		t.Fatal("no receive command")
	}
	select {
	case id := <-started:
		if id != target {
			t.Fatal("wrong container")
		}
	case <-time.After(time.Second):
		t.Fatal("logs never started")
	}
	// No consumption: cancellation must release a full queue too.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.logs != nil {
		t.Fatal("log view did not close")
	}
	done := make(chan struct{})
	go func() { m.logWorkers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer leaked on close")
	}
}

func TestLogReconnectReplacesSessionAndShowsErrors(t *testing.T) {
	m := fixture()
	m.options.Logs = func(ctx context.Context, _ string, emit func(watchdog.LogLine) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	m, _ = key(m, 'l')
	old := m.logs
	m, cmd := key(m, 'r')
	if m.logs == old || cmd == nil || m.logs.id != old.id {
		t.Fatal("reconnect did not replace the selected subscription")
	}
	next, _ := m.updateLogs(logMessage{session: old, entries: []logEntry{{end: true, err: fmt.Errorf("old failure")}}})
	m = next.(model)
	if strings.Contains(m.logs.status, "old failure") {
		t.Fatal("old stream overwrote active status")
	}
	next, _ = m.updateLogs(logMessage{session: m.logs, entries: []logEntry{{end: true, err: fmt.Errorf("unsupported logging driver")}}})
	m = next.(model)
	if !strings.Contains(m.View(), "unsupported logging driver") {
		t.Fatal("log failure hidden")
	}
	m, _ = key(m, 'q')
	done := make(chan struct{})
	go func() { m.logWorkers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnected stream leaked")
	}
}
