package tui

import (
	"context"
	"fmt"
	"time"

	"docker-watchdog/internal/watchdog"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type confirmation struct {
	id      string
	name    string
	command watchdog.Command
}

type controlResult struct {
	name    string
	command watchdog.Command
	err     error
}

func (m *model) askControl(key string) {
	if m.options.Control == nil || m.busy || len(m.rows) == 0 {
		return
	}
	selected := m.ordered()[m.cursor]
	command := watchdog.CommandStop
	switch key {
	case "a":
		command = watchdog.CommandStart
	case "r":
		command = watchdog.CommandRestart
	case "p":
		command = watchdog.CommandPauseRecovery
		if selected.RecoveryPaused {
			command = watchdog.CommandResumeRecovery
		}
	}
	// Pin the full ID: sorting or new samples cannot change the target while
	// the user is reading the confirmation.
	m.pending = &confirmation{id: selected.ID, name: selected.Name, command: command}
}

func (m model) confirm(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "n", "esc", "q":
		m.pending = nil
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "y":
		target := *m.pending
		m.pending = nil
		if _, exists := m.rows[target.id]; !exists {
			m.controlNotice = "Container was removed; command cancelled"
			return m, nil
		}
		m.busy = true
		m.controlNotice = string(target.command) + " in progress for " + clean(target.name)
		control := m.options.Control
		ctx := m.ctx
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			err := control(ctx, target.id, target.command)
			return controlResult{name: target.name, command: target.command, err: err}
		}
	}
	return m, nil
}

func (m model) confirmationView(width int) string {
	target := m.pending
	name := target.name
	if name == "" {
		name = target.id
	}
	message := fmt.Sprintf("%s: %s", target.command, clean(name))
	note := "Automatic retry counts are preserved."
	switch target.command {
	case watchdog.CommandStop:
		note = "Stops the container and saves recovery-paused mode."
	case watchdog.CommandStart, watchdog.CommandRestart:
		note = "On success, resumes recovery if globally enabled."
	case watchdog.CommandPauseRecovery:
		note = "Keeps monitoring; disables automatic restarts for this container."
	case watchdog.CommandResumeRecovery:
		note = "Allows automatic recovery again if globally enabled."
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(amber).
		Width(width-2).Padding(0, 1).Render(
		"y confirm   n / Esc cancel\n\n" +
			lipgloss.NewStyle().Bold(true).Foreground(amber).Render(fit(message, width-4)) + "\n" +
			"ID: " + target.id + "\n\n" + note)
}
