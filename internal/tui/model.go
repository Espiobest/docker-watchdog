package tui

import (
	"context"
	"io"
	"sort"
	"time"

	"docker-watchdog/internal/watchdog"
	tea "github.com/charmbracelet/bubbletea"
)

type Options struct {
	AutoRestart bool
	Label       string
	Endpoint    string
	MaxRetries  int
	Version     string
	Control     func(context.Context, string, watchdog.Command) error
}

type model struct {
	options       Options
	events        <-chan watchdog.Event
	rows          map[string]watchdog.Event
	lastActions   map[string]string
	recent        []string
	width         int
	height        int
	cursor        int
	sortKey       int
	now           time.Time
	started       time.Time
	systemError   string
	ctx           context.Context
	pending       *confirmation
	busy          bool
	controlNotice string
}

type eventMessage struct{ event watchdog.Event }
type closedMessage struct{}
type tickMessage time.Time

func newModel(events <-chan watchdog.Event, options Options) model {
	now := time.Now()
	return model{
		options: options, events: events,
		rows: make(map[string]watchdog.Event), lastActions: make(map[string]string),
		width: 100, height: 30, now: now, started: now,
		ctx: context.Background(),
	}
}

func Run(ctx context.Context, events <-chan watchdog.Event, options Options, input io.Reader, output io.Writer) error {
	model := newModel(events, options)
	model.ctx = ctx
	program := tea.NewProgram(model,
		tea.WithContext(ctx), tea.WithAltScreen(), tea.WithInput(input), tea.WithOutput(output))
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(receive(m.events), tick())
}

func receive(events <-chan watchdog.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return closedMessage{}
		}
		return eventMessage{event: event}
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(now time.Time) tea.Msg { return tickMessage(now) })
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case tea.KeyMsg:
		if m.pending != nil {
			return m.confirm(message.String())
		}
		switch message.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.cursor = min(m.cursor+1, max(0, len(m.rows)-1))
		case "k", "up":
			m.cursor = max(0, m.cursor-1)
		case "pgdown":
			m.cursor = min(m.cursor+m.pageSize(), max(0, len(m.rows)-1))
		case "pgup":
			m.cursor = max(0, m.cursor-m.pageSize())
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = max(0, len(m.rows)-1)
		case "s":
			m.sortKey = (m.sortKey + 1) % 3
			m.cursor = 0
		case "x", "a", "r", "p":
			m.askControl(message.String())
		}
	case controlResult:
		m.busy = false
		if message.err != nil {
			m.controlNotice = "Command failed: " + clean(message.err.Error())
		} else {
			m.controlNotice = string(message.command) + " completed for " + clean(message.name)
		}
	case tickMessage:
		m.now = time.Time(message)
		return m, tick()
	case closedMessage:
		return m, tea.Quit
	case eventMessage:
		m.accept(message.event)
		return m, receive(m.events)
	}
	return m, nil
}

func (m *model) accept(event watchdog.Event) {
	var selected string
	ordered := m.ordered()
	if len(ordered) > m.cursor {
		selected = ordered[m.cursor].ID
	}
	if event.ID == "" {
		m.systemError = event.Error
		return
	}
	previous, exists := m.rows[event.ID]
	if event.Removed {
		delete(m.rows, event.ID)
		delete(m.lastActions, event.ID)
	} else {
		m.rows[event.ID] = event
	}
	if event.Action != "" {
		m.lastActions[event.ID] = string(event.Action) + " at " + event.Time.Format("15:04:05")
	}
	if !exists || previous.Status != event.Status || event.Action != "" {
		description := string(event.Status)
		if event.Action != "" {
			description = string(event.Action)
		}
		m.recent = append(m.recent, event.Time.Format("15:04:05")+"  "+clean(event.Name)+"  →  "+description)
		if len(m.recent) > 3 {
			m.recent = m.recent[len(m.recent)-3:]
		}
	}
	m.cursor = min(m.cursor, max(0, len(m.rows)-1))
	for index, row := range m.ordered() {
		if row.ID == selected {
			m.cursor = index
			break
		}
	}
}

func (m model) ordered() []watchdog.Event {
	rows := make([]watchdog.Event, 0, len(m.rows))
	for _, event := range m.rows {
		rows = append(rows, event)
	}
	sort.Slice(rows, func(i, j int) bool {
		switch m.sortKey {
		case 1:
			if rows[i].CPUPercent != rows[j].CPUPercent {
				return rows[i].CPUPercent > rows[j].CPUPercent
			}
		case 2:
			if rows[i].MemoryBytes != rows[j].MemoryBytes {
				return rows[i].MemoryBytes > rows[j].MemoryBytes
			}
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

func (m model) pageSize() int {
	extra := 0
	if m.options.Control != nil {
		extra = 1
	}
	switch {
	case m.height >= 26:
		return max(1, m.height-24-extra)
	case m.height >= 21:
		return max(1, m.height-19-extra)
	default:
		return max(1, m.height-12-extra)
	}
}
