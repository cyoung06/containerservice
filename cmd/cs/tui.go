package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type mode int

const (
	modeList mode = iota
	modeFilter
	modeNewName
	modeNewPreset
	modeDelete
)

// picked is what the picker leaves for main to do once the screen is gone.
type picked struct {
	connect, create string // a container's name
	preset          string
}

type model struct {
	user       string
	containers []container
	cursor     int
	last       string
	mode       mode
	input      textinput.Model
	filter     string
	presets    presetFile
	presetAt   int
	newName    string
	status     string
	width      int
	result     picked
	changes    chan struct{}
}

type listMsg struct {
	list []container
	err  error
}
type changedMsg struct{}
type statusMsg string

var (
	dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold     = lipgloss.NewStyle().Bold(true)
	selected = lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	warn     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	bad      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	good     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

func stateStyle(state string) lipgloss.Style {
	switch {
	case state == "running":
		return good
	case state == "stopped":
		return dim
	case strings.Contains(state, "Err") || strings.Contains(state, "BackOff"):
		return bad
	default:
		return warn
	}
}

func fetchList() tea.Msg {
	var got struct{ Containers []container }
	err := call("GET", "/api/containers", nil, &got)
	return listMsg{got.Containers, err}
}

// watch follows the server's change events (no polling), one message per change.
func (m model) watch() tea.Cmd {
	return func() tea.Msg {
		<-m.changes
		return changedMsg{}
	}
}

func streamChanges(ch chan<- struct{}) {
	for {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", origin()+"/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+keychainGet())
		if resp, err := http.DefaultClient.Do(req); err == nil {
			lines := bufio.NewScanner(resp.Body)
			for lines.Scan() {
				if strings.HasPrefix(lines.Text(), "event:") {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
			resp.Body.Close()
		}
		time.Sleep(5 * time.Second) // reconnect after the stream ends (session expiry, restarts)
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(fetchList, m.watch()) }

func (m model) visible() []container {
	if m.filter == "" {
		return m.containers
	}
	var out []container
	for _, c := range m.containers {
		if strings.Contains(c.Name, m.filter) || strings.Contains(shortImage(c.Image), m.filter) {
			out = append(out, c)
		}
	}
	return out
}

func (m model) current() (container, bool) {
	v := m.visible()
	if m.cursor < 0 || m.cursor >= len(v) {
		return container{}, false
	}
	return v[m.cursor], true
}

func act(f func() error, done string) tea.Cmd {
	return func() tea.Msg {
		if err := f(); err != nil {
			return statusMsg(bad.Render(err.Error()))
		}
		return statusMsg(done)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case listMsg:
		if msg.err != nil {
			m.status = bad.Render(msg.err.Error())
			return m, nil
		}
		first := m.containers == nil
		m.containers = msg.list
		if first {
			for i, c := range m.containers {
				if c.Name == m.last {
					m.cursor = i
				}
			}
		}
		if n := len(m.visible()); m.cursor >= n {
			m.cursor = max(n-1, 0)
		}
		return m, nil
	case changedMsg:
		return m, tea.Batch(fetchList, m.watch())
	case statusMsg:
		m.status = string(msg)
		return m, fetchList
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeFilter, modeNewName, modeDelete:
		switch k.String() {
		case "esc":
			if m.mode == modeFilter {
				m.filter = ""
			}
			m.mode = modeList
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.input.Value())
			switch m.mode {
			case modeFilter:
				m.mode = modeList
			case modeNewName:
				if !nameRE.MatchString(value) {
					m.status = bad.Render("A name is lowercase letters, digits and dashes, starting with a letter.")
					return m, nil
				}
				m.newName, m.mode, m.presetAt = value, modeNewPreset, 0
			case modeDelete:
				m.mode = modeList
				c, ok := m.current()
				if !ok || value != c.Name {
					m.status = "Not deleted."
					return m, nil
				}
				name := c.Name
				return m, act(func() error { return call("DELETE", "/api/containers/"+pathName(name), nil, nil) }, name+" deleted (volumes kept).")
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		if m.mode == modeFilter {
			m.filter = strings.TrimSpace(m.input.Value())
			m.cursor = 0
		}
		return m, cmd
	case modeNewPreset:
		names := m.presets.names()
		switch k.String() {
		case "esc":
			m.mode = modeList
		case "up", "k":
			m.presetAt = max(m.presetAt-1, 0)
		case "down", "j":
			m.presetAt = min(m.presetAt+1, len(names)-1)
		case "enter":
			m.result = picked{create: m.newName, preset: names[m.presetAt]}
			return m, tea.Quit
		}
		return m, nil
	}
	c, ok := m.current()
	switch k.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.visible())-1, 0))
	case "enter":
		if ok {
			m.result = picked{connect: c.Name}
			return m, tea.Quit
		}
	case "n":
		m.mode, m.input = modeNewName, newInput("name")
		return m, textinput.Blink
	case "/":
		m.mode, m.input = modeFilter, newInput("filter")
		m.input.SetValue(m.filter)
		return m, textinput.Blink
	case "s":
		if ok {
			name, on := c.Name, !c.Running
			verb := map[bool]string{true: "start", false: "stop"}[on]
			m.status = map[bool]string{true: "Starting ", false: "Stopping "}[on] + name + "…"
			return m, act(func() error { return call("POST", "/api/containers/"+pathName(name)+"/"+verb, nil, nil) }, "")
		}
	case "d":
		if ok {
			m.mode, m.input = modeDelete, newInput("type "+c.Name+" to delete it")
			return m, textinput.Blink
		}
	case "r":
		return m, fetchList
	}
	return m, nil
}

func newInput(placeholder string) textinput.Model {
	in := textinput.New()
	in.Placeholder = placeholder
	in.CharLimit = 52
	in.Focus()
	return in
}

func (m model) View() string {
	var b strings.Builder
	head := bold.Render(strings.TrimPrefix(origin(), "https://"))
	if m.user != "" {
		head += dim.Render(" · " + m.user)
	}
	b.WriteString(head + "\n\n")
	switch m.mode {
	case modeNewPreset:
		b.WriteString("New container " + bold.Render(m.newName) + " from preset:\n\n")
		for i, n := range m.presets.names() {
			p := m.presets.Presets[n]
			line := fmt.Sprintf("%-10s %s", n, dim.Render(describePreset(p)))
			if i == m.presetAt {
				line = selected.Render("▸ " + line)
			} else {
				line = "  " + line
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n" + dim.Render("⏎ make and connect · esc back · presets: "+presetsPath()) + "\n")
		return b.String()
	}
	v := m.visible()
	switch {
	case m.containers == nil:
		b.WriteString(dim.Render("Loading…") + "\n")
	case len(v) == 0 && m.filter != "":
		b.WriteString(dim.Render("Nothing matches "+m.filter) + "\n")
	case len(v) == 0:
		b.WriteString(dim.Render("No containers yet: n makes one.") + "\n")
	}
	for i, c := range v {
		sec := ""
		if c.Debug {
			sec = warn.Render("debugging")
		}
		lastMark := ""
		if c.Name == m.last {
			lastMark = dim.Render("last")
		}
		line := fmt.Sprintf("%-20s %s %-8s %-28s %s %s", c.Name, stateStyle(c.State).Render(fmt.Sprintf("%-18s", c.State)), shortImage(c.Image), dim.Render(limits(c.spec)), sec, lastMark)
		if i == m.cursor {
			b.WriteString(selected.Render("▸ "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n")
	switch m.mode {
	case modeFilter, modeNewName, modeDelete:
		b.WriteString(m.input.View() + dim.Render("  ⏎ ok · esc cancel") + "\n")
	default:
		if m.filter != "" {
			b.WriteString(dim.Render("filter: "+m.filter+" (/ to change, esc in it to clear)") + "\n")
		}
		b.WriteString(dim.Render("⏎ connect · n new · s start/stop · d delete · / filter · q quit") + "\n")
	}
	if m.status != "" {
		b.WriteString(m.status + "\n")
	}
	return b.String()
}

func limits(s spec) string {
	var parts []string
	if s.CPUMillis > 0 {
		parts = append(parts, fmt.Sprintf("%g CPU", float64(s.CPUMillis)/1000))
	}
	if s.MemoryMiB > 0 {
		parts = append(parts, fmt.Sprintf("%g GiB", float64(s.MemoryMiB)/1024))
	}
	if s.DiskGiB > 0 {
		parts = append(parts, fmt.Sprintf("%d GiB disk", s.DiskGiB))
	}
	return strings.Join(parts, " · ")
}

func describePreset(p Preset) string {
	parts := []string{p.Image}
	if p.Home > 0 {
		parts = append(parts, fmt.Sprintf("home %d GiB", p.Home))
	}
	if p.Debug {
		parts = append(parts, "debugging")
	}
	if p.ClaudeLogin {
		parts = append(parts, "Claude login")
	}
	return strings.Join(parts, " · ")
}

func pick() {
	s := sessionInfo() // signs in first if needed, before the screen takes over
	presets := loadPresets()
	writeSSHConfig(false)
	changes := make(chan struct{}, 1)
	go streamChanges(changes)
	m := model{user: s.Username, last: loadState().Last, presets: presets, changes: changes}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	must(err)
	r := final.(model).result
	switch {
	case r.connect != "":
		connect(r.connect, "console", false)
	case r.create != "":
		create(r.create, r.preset, presets.Presets[r.preset])
	}
}
