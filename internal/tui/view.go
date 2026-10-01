package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"docker-watchdog/internal/watchdog"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/moby/moby/api/types/container"
)

var (
	accent = lipgloss.Color("81")
	green  = lipgloss.Color("78")
	amber  = lipgloss.Color("221")
	red    = lipgloss.Color("203")
	muted  = lipgloss.Color("245")
	line   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	dim    = lipgloss.NewStyle().Foreground(muted)
	bright = lipgloss.NewStyle().Bold(true)
)

func (m model) View() string {
	if m.width < 48 || m.height < 15 {
		return "Watchdog needs a terminal of at least 48 x 15.\nResize the window, or press q to quit."
	}
	width := m.width - 4
	mode := "AUTO RECOVERY OFF"
	if m.options.AutoRestart {
		mode = "AUTO RECOVERY"
	}
	brand := lipgloss.NewStyle().Bold(true).Foreground(accent).Render("◈ WATCHDOG")
	header := brand + "  " + dim.Render(m.options.Version) + "   " +
		lipgloss.NewStyle().Foreground(amber).Render(mode)
	label := m.options.Label
	if label == "" {
		label = "all containers"
	}
	subtitle := dim.Render(fit(clean(label)+"  •  uptime "+m.now.Sub(m.started).Round(time.Second).String(), width))

	var healthy, attention, recovering int
	for _, event := range m.rows {
		switch event.Status {
		case watchdog.StatusHealthy, watchdog.StatusRunning:
			healthy++
		case watchdog.StatusBackoff, watchdog.StatusRestarting, watchdog.StatusStarting:
			recovering++
		default:
			attention++
		}
	}
	summary := bright.Render(fmt.Sprintf("%d tracked", len(m.rows))) + "    " +
		lipgloss.NewStyle().Foreground(green).Render(fmt.Sprintf("● %d up", healthy)) + "    " +
		lipgloss.NewStyle().Foreground(amber).Render(fmt.Sprintf("◐ %d waiting", recovering)) + "    " +
		lipgloss.NewStyle().Foreground(red).Render(fmt.Sprintf("! %d attention", attention))

	sections := []string{header, subtitle, "", summary, "", m.table(width)}
	if m.pending != nil {
		content := header + "\n\n" + m.confirmationView(width)
		return lipgloss.NewStyle().Padding(1, 2).MaxWidth(m.width).MaxHeight(m.height).Render(content)
	}
	rows := m.ordered()
	if len(rows) > 0 && m.height >= 21 {
		sections = append(sections, "", m.details(rows[m.cursor], width))
	} else if len(rows) == 0 {
		sections = append(sections, "", dim.Render("Waiting for matching containers…"))
	}
	if m.height >= 26 {
		sections = append(sections, "", dim.Render("RECENT TRANSITIONS"))
		for _, transition := range m.recent {
			sections = append(sections, dim.Render(fit(transition, width)))
		}
	}
	if m.systemError != "" {
		sections[1] = lipgloss.NewStyle().Foreground(red).Render(fit(m.systemError, width))
	} else if m.controlNotice != "" {
		sections[1] = lipgloss.NewStyle().Foreground(accent).Render(fit(m.controlNotice, width))
	}
	sortName := []string{"name", "cpu ↓", "memory ↓"}[m.sortKey]
	footer := dim.Render("↑/↓ j/k select   s sort: " + sortName + "   q quit")
	if m.options.Control != nil {
		footer = dim.Render("↑/↓ select · s sort · q quit\nx stop · a start · r restart · p recovery")
	}
	sections = append(sections, "", footer)
	content := strings.Join(sections, "\n")
	return lipgloss.NewStyle().Padding(1, 2).MaxWidth(m.width).MaxHeight(m.height).Render(content)
}

func (m model) table(width int) string {
	wide := width >= 100
	nameWidth := max(8, width-66)
	if wide {
		nameWidth = width - 91
	}
	compact := width < 74
	if compact {
		nameWidth = width - 22
	}
	nameWidth = min(nameWidth, 36)
	heading := "  " + cell("CONTAINER", nameWidth) + "  " + cell("STATUS", 17)
	if !compact {
		heading += "  " + cell("CPU", 7) + "  " + cell("MEMORY", 12) + "  " + cell("RETRIES", 7) + " " + cell("UPTIME", 12)
	}
	if wide {
		heading += "  RX / TX"
	}
	lines := []string{dim.Render(heading), line.Render(strings.Repeat("─", width))}
	rows := m.ordered()
	start := max(0, m.cursor-m.pageSize()+1)
	end := min(len(rows), start+m.pageSize())
	for index := start; index < end; index++ {
		event := rows[index]
		pointer := "  "
		if index == m.cursor {
			pointer = lipgloss.NewStyle().Foreground(accent).Render("› ")
		}
		name := clean(event.Name)
		if name == "" {
			name = event.ID
		}
		nameColumn := cell(name, nameWidth)
		if index == m.cursor {
			nameColumn = lipgloss.NewStyle().Bold(true).Foreground(accent).Render(nameColumn)
		}
		row := pointer + nameColumn + "  " +
			lipgloss.NewStyle().Foreground(statusColor(event.Status)).Render(cell(string(event.Status), 17))
		if !compact {
			cpu, memory := "—", "—"
			if event.StatsOK {
				cpu = fmt.Sprintf("%.1f%%", event.CPUPercent)
				memory = bytes(event.MemoryBytes)
			}

			uptime := "—"
			if event.State == container.StateRunning && !event.StartedAt.IsZero() {
				uptime = max(time.Duration(0), m.now.Sub(event.StartedAt)).Truncate(time.Second).String()
			}
			row += "  " + cell(cpu, 7) + "  " + cell(memory, 12) + "  " +
				cell(fmt.Sprintf("%d/%d", event.Attempts, m.options.MaxRetries), 7) + " " + cell(uptime, 12)
		}
		if wide {
			network := "—"
			if event.StatsOK {
				network = bytes(event.NetworkRX) + " / " + bytes(event.NetworkTX)
			}
			row += "  " + network
		}
		lines = append(lines, ansi.Truncate(row, width, ""))
	}
	if len(rows) > end || start > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  %d–%d of %d  ·  PgUp/PgDn to scroll", start+1, end, len(rows))))
	}
	return strings.Join(lines, "\n")
}

func (m model) details(event watchdog.Event, width int) string {
	name := clean(event.Name)
	id := event.ID[:min(12, len(event.ID))]
	heading := bright.Foreground(accent).Render(fit(name+"  "+id, width))
	info := fmt.Sprintf("Docker: %s  ·  health: %s  ·  engine restarts: %d", event.State, event.Health, event.RestartCount)
	startedAt := "Last started: unknown"
	if !event.StartedAt.IsZero() {
		startedAt = "Last started: " + event.StartedAt.Local().Format("2006-01-02 15:04:05 MST")
	} else if event.State == container.StateCreated {
		startedAt = "Last started: never"
	}
	note := event.Reason
	if event.Error != "" {
		note = event.Error
	} else if event.StatsError != "" {
		note = "Stats unavailable: " + event.StatsError
	}
	if event.Status == watchdog.StatusBackoff {
		remaining := max(time.Duration(0), event.NextRetry.Sub(m.now)).Round(time.Second)
		note = "Next recovery in " + remaining.String() + "  ·  " + note
	}
	action := m.lastActions[event.ID]
	if event.RecoveryPaused {
		note = "RECOVERY PAUSED  ·  " + note
	}
	if action == "" {
		action = "No recovery action taken"
	}
	return strings.Join([]string{
		line.Render(strings.Repeat("─", width)), heading,
		dim.Render(fit(clean(info), width)), fit(clean(note), width),
		dim.Render(fit(startedAt, width)),
		dim.Render(fit(action, width)),
	}, "\n")
}

func statusColor(status watchdog.Status) lipgloss.Color {
	switch status {
	case watchdog.StatusHealthy, watchdog.StatusRunning:
		return green
	case watchdog.StatusStarting, watchdog.StatusRestarting, watchdog.StatusBackoff:
		return amber
	case watchdog.StatusUnhealthy, watchdog.StatusCrashed, watchdog.StatusCrashLoop,
		watchdog.StatusRetryExhausted, watchdog.StatusUnknown, watchdog.StatusStorageError:
		return red
	default:
		return muted
	}
}

func bytes(value uint64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	if value < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(value)/1024)
	}
	if value < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MiB", float64(value)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GiB", float64(value)/(1024*1024*1024))
}

func clean(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func fit(value string, width int) string {
	return ansi.Truncate(value, max(0, width), "…")
}

func cell(value string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(fit(value, width))
}
