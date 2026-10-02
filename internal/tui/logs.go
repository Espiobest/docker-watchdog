package tui

import (
	"context"
	"fmt"
	"strings"

	"docker-watchdog/internal/watchdog"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const logHistoryLimit = 500

type logEntry struct {
	line watchdog.LogLine
	end  bool
	err  error
}

type logSession struct {
	id, name string
	cancel   context.CancelFunc
	queue    chan logEntry
	lines    []watchdog.LogLine
	status   string
	offset   int
	follow   bool
}

type logMessage struct {
	session *logSession
	entries []logEntry
}

func (m *model) openLogs(id, name string) tea.Cmd {
	if m.logs != nil {
		m.logs.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	session := &logSession{id: id, name: name, cancel: cancel, queue: make(chan logEntry, 64), status: "Connecting…", follow: true}
	m.logs = session
	source := m.options.Logs
	m.logWorkers.Add(1)
	go func() {
		defer m.logWorkers.Done()
		defer close(session.queue)
		err := source(ctx, id, func(line watchdog.LogLine) error {
			select {
			case session.queue <- logEntry{line: line}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		select {
		case session.queue <- logEntry{end: true, err: err}:
		case <-ctx.Done():
		}
	}()
	return receiveLogs(session)
}

func receiveLogs(session *logSession) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-session.queue
		if !ok {
			return logMessage{session: session}
		}
		entries := []logEntry{first}
		// Batch hot streams without starving keyboard and monitoring messages.
		for len(entries) < 32 {
			select {
			case entry, ok := <-session.queue:
				if !ok {
					return logMessage{session: session, entries: entries}
				}
				entries = append(entries, entry)
			default:
				return logMessage{session: session, entries: entries}
			}
		}
		return logMessage{session: session, entries: entries}
	}
}

func (m model) updateLogs(message logMessage) (tea.Model, tea.Cmd) {
	if m.logs != message.session {
		return m, nil
	}
	s := m.logs
	ended := len(message.entries) == 0
	for _, entry := range message.entries {
		if entry.end {
			ended = true
			s.status = "Stream ended · r reconnect"
			if entry.err != nil {
				s.status = "Log error: " + clean(ansi.Strip(entry.err.Error())) + " · r retry"
			}
			continue
		}
		s.status = "Live"
		if entry.line.Ready {
			continue
		}
		entry.line.Text = clean(ansi.Strip(entry.line.Text))
		s.lines = append(s.lines, entry.line)
		if len(s.lines) > logHistoryLimit {
			copy(s.lines, s.lines[1:])
			s.lines = s.lines[:logHistoryLimit]
			if !s.follow {
				s.offset = max(0, s.offset-1)
			}
		}
	}
	if s.follow {
		s.offset = max(0, len(s.lines)-m.logPageSize())
	}
	if ended {
		return m, nil
	}
	return m, receiveLogs(s)
}

func (m model) logKey(key string) (tea.Model, tea.Cmd) {
	s := m.logs
	maxOffset := max(0, len(s.lines)-m.logPageSize())
	switch key {
	case "esc", "l", "q":
		s.cancel()
		m.logs = nil
	case "ctrl+c":
		s.cancel()
		return m, tea.Quit
	case "up", "k":
		s.follow = false
		s.offset = max(0, s.offset-1)
	case "pgup":
		s.follow = false
		s.offset = max(0, s.offset-m.logPageSize())
	case "down", "j":
		s.offset = min(maxOffset, s.offset+1)
	case "pgdown":
		s.offset = min(maxOffset, s.offset+m.logPageSize())
	case "home", "g":
		s.follow = false
		s.offset = 0
	case "end", "G", "f":
		s.follow = true
		s.offset = maxOffset
	case "r":
		cmd := m.openLogs(s.id, s.name)
		return m, cmd
	}
	return m, nil
}

func (m model) logPageSize() int { return max(1, m.height-9) }

func (m model) logsView() string {
	s := m.logs
	width := m.width - 4
	mode := "FOLLOW"
	if !s.follow {
		mode = "SCROLL"
	}
	header := bright.Foreground(accent).Render(fit("LOGS · "+clean(s.name)+" · "+mode, width))
	lines := []string{header, dim.Render(fit(s.status, width)), dim.Render(fmt.Sprintf("%d/%d recent lines · stdout/stderr", len(s.lines), logHistoryLimit)), ""}
	start := min(s.offset, max(0, len(s.lines)-m.logPageSize()))
	if s.follow {
		start = max(0, len(s.lines)-m.logPageSize())
	}
	for _, entry := range s.lines[start:min(len(s.lines), start+m.logPageSize())] {
		text := fit(entry.Stream+" │ "+entry.Text, width)
		if entry.Stream == "err" {
			text = lipgloss.NewStyle().Foreground(amber).Render(text)
		}
		lines = append(lines, text)
	}
	for len(lines) < 4+m.logPageSize() {
		lines = append(lines, "")
	}
	lines = append(lines, "", dim.Render("↑/↓ PgUp/PgDn scroll · f follow · r retry"), dim.Render("Esc / l / q back · Ctrl+C quit"))
	return lipgloss.NewStyle().Padding(1, 2).MaxWidth(m.width).MaxHeight(m.height).Render(strings.Join(lines, "\n"))
}
