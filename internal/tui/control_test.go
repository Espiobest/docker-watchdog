package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"docker-watchdog/internal/watchdog"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func key(m model, value rune) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{value}})
	return next.(model), cmd
}

func TestControlRequiresConfirmationAndPinsTarget(t *testing.T) {
	m := fixture()
	var calledID string
	m.options.Control = func(_ context.Context, id string, command watchdog.Command) error {
		calledID = id
		if command != watchdog.CommandStop {
			t.Errorf("command = %s", command)
		}
		return errors.New("daemon unavailable")
	}
	target := m.ordered()[m.cursor].ID
	m, _ = key(m, 'x')
	if calledID != "" || m.pending == nil {
		t.Fatal("command ran without confirmation")
	}
	m.cursor = 0
	m, cmd := key(m, 'y')
	if cmd == nil || !m.busy {
		t.Fatal("confirmation did not schedule command")
	}
	result := cmd()
	next, _ := m.Update(result)
	m = next.(model)
	if calledID != target || m.busy || !strings.Contains(m.controlNotice, "daemon unavailable") {
		t.Fatalf("target=%q notice=%q busy=%t", calledID, m.controlNotice, m.busy)
	}
}

func TestCancelAndRemovedTargetDoNotRunControl(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		m := fixture()
		m, _ = key(m, 'r')
		if cancel {
			m, _ = key(m, 'n')
		} else {
			delete(m.rows, m.pending.id)
			var cmd tea.Cmd
			m, cmd = key(m, 'y')
			if cmd != nil {
				t.Fatal("removed target scheduled command")
			}
		}
		if m.pending != nil || m.busy {
			t.Fatal("confirmation did not cancel")
		}
	}
}

func TestRecoveryToggleAndConfirmationLayout(t *testing.T) {
	m := fixture()
	selected := m.ordered()[m.cursor]
	selected.RecoveryPaused = true
	m.rows[selected.ID] = selected
	m, _ = key(m, 'p')
	if m.pending.command != watchdog.CommandResumeRecovery {
		t.Fatal("expected resume")
	}
	m.pending.id = strings.Repeat("a", 64)
	m.pending.name = strings.Repeat("long-name", 20)
	for _, size := range [][2]int{{48, 15}, {80, 24}, {120, 32}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if !strings.Contains(ansi.Strip(view), "y confirm") || lipgloss.Height(view) > size[1] {
			t.Fatalf("confirmation clipped at %v: %s", size, view)
		}
		if strings.Count(ansi.Strip(view), "a") < 64 {
			t.Fatal("full target ID clipped")
		}
	}
}
