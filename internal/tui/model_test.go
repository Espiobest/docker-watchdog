package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"docker-watchdog/internal/watchdog"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func fixture() model {
	m := newModel(nil, Options{AutoRestart: true, Label: "watchdog.demo=true", MaxRetries: 3, Version: "v0.1.0"})
	m.options.Control = func(context.Context, string, watchdog.Command) error { return nil }
	m.now = time.Date(2026, 10, 1, 14, 30, 45, 0, time.UTC)
	m.started = m.now.Add(-2*time.Minute - 18*time.Second)
	m.width, m.height = 120, 32
	statuses := []watchdog.Status{watchdog.StatusHealthy, watchdog.StatusRunning, watchdog.StatusBackoff, watchdog.StatusCrashLoop, watchdog.StatusRetryExhausted}
	names := []string{"api-gateway", "postgres", "payments-worker", "event-consumer", "legacy-service"}
	for index, status := range statuses {
		event := watchdog.Event{
			Time: m.now.Add(-time.Second),
			Sample: watchdog.Sample{
				ID: fmt.Sprintf("a1b2c3d4e5f%d", index), Name: names[index],
				State: "running", StartedAt: m.now.Add(-12*time.Hour - 30*time.Minute - 15*time.Second),
				Health: "healthy", CPUPercent: float64(index*7) + 1.4,
				MemoryBytes: uint64(64+index*42) * 1024 * 1024, MemoryLimit: 1024 * 1024 * 1024,
				NetworkRX: 720000, NetworkTX: 128000, StatsOK: true,
			},
			Decision: watchdog.Decision{Status: status},
		}
		if status == watchdog.StatusBackoff {
			event.Health = "unhealthy"
			event.Attempts = 1
			event.NextRetry = m.now.Add(7 * time.Second)
			event.Reason = "Docker health check failed"
		}
		if status == watchdog.StatusRetryExhausted {
			event.Attempts = 3
			event.Health = "unhealthy"
		}
		if status == watchdog.StatusCrashLoop {
			event.RestartCount = 5
			event.Reason = "repeated starts detected within crash window"
		}
		m.accept(event)
	}
	for index, event := range m.ordered() {
		if event.Name == "payments-worker" {
			m.cursor = index
			m.lastActions[event.ID] = "restarted at 14:30:31"
		}
	}
	return m
}

func TestDashboardFitsTerminalAndKeepsControlsVisible(t *testing.T) {
	for _, size := range [][2]int{{48, 15}, {80, 20}, {80, 21}, {80, 24}, {80, 25}, {80, 26}, {100, 30}, {120, 32}} {
		m := fixture()
		m.width, m.height = size[0], size[1]
		for index := range 30 {
			id := fmt.Sprint(index)
			m.rows[id] = watchdog.Event{
				Sample:   watchdog.Sample{ID: id, Name: "more-" + id, State: "running", StartedAt: m.now.Add(-12*time.Hour - 30*time.Minute - 15*time.Second)},
				Decision: watchdog.Decision{Status: watchdog.StatusHealthy},
			}
		}
		view := m.View()
		if size[0] >= 80 && size[1] >= 21 && (!strings.Contains(ansi.Strip(view), "12h30m15s") || !strings.Contains(ansi.Strip(view), "Last started:")) {
			t.Errorf("container timing missing at %v", size)
		}
		if !strings.Contains(ansi.Strip(view), "q quit") {
			t.Errorf("footer clipped at %v", size)
		}
		if !strings.Contains(ansi.Strip(view), "p recovery") {
			t.Errorf("action footer clipped at %v", size)
		}
		if lipgloss.Height(view) > size[1] {
			t.Errorf("height overflow at %v: %d", size, lipgloss.Height(view))
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("width overflow at %v: %q", size, line)
			}
		}
	}
}

func TestKeyboardSortingAndSelection(t *testing.T) {
	m := fixture()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(model)
	if m.sortKey != 1 || m.ordered()[0].Name != "legacy-service" {
		t.Fatal("CPU sort did not put busiest container first")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	selected := m.ordered()[m.cursor].ID
	event := m.rows[selected]
	event.CPUPercent = 999
	m.accept(event)
	if m.ordered()[m.cursor].ID != selected {
		t.Fatal("updating metrics moved selection to another container")
	}
}

func TestPreview(t *testing.T) {
	path := os.Getenv("WATCHDOG_PREVIEW")
	if path == "" {
		t.Skip("set WATCHDOG_PREVIEW to export the deterministic dashboard fixture")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	if err := os.WriteFile(path, []byte(fixture().View()), 0600); err != nil {
		t.Fatal(err)
	}
}
