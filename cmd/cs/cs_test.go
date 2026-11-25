package main

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultPresets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := loadPresets() // writes the defaults, then reads them
	if f.Default != "claude" || !f.Presets["claude"].ClaudeLogin || !f.Presets["pwn"].Debug || f.Presets["claude"].Home != 20 {
		t.Fatalf("%+v", f)
	}
	if names := f.names(); names[0] != "claude" || len(names) != 3 {
		t.Errorf("names %v", names)
	}
	if _, err := os.Stat(presetsPath()); err != nil {
		t.Error(err)
	}
}

func TestResolveImage(t *testing.T) {
	sugg := []string{"oci.syeyoung.dev/containers/claude:2026.11.22-3", "oci.syeyoung.dev/containers/pwn:2026.11.22-1"}
	if got, err := resolveImage("claude", sugg); err != nil || got != sugg[0] {
		t.Errorf("claude: %q %v", got, err)
	}
	if got, _ := resolveImage("docker.io/library/ubuntu:24.04", sugg); got != "docker.io/library/ubuntu:24.04" {
		t.Errorf("full ref: %q", got)
	}
	if _, err := resolveImage("nope", sugg); err == nil {
		t.Error("unknown short name accepted")
	}
}

func TestSpecFor(t *testing.T) {
	s := session{Images: []string{"oci.syeyoung.dev/containers/claude:x"}}
	sp := specFor("agent", Preset{Image: "claude", Home: 20, Disk: 20, ClaudeLogin: true}, s)
	if len(sp.Mounts) != 1 || sp.Mounts[0].Volume != "agent-home" || sp.Mounts[0].New.SizeGiB != 20 || !sp.ClaudeLogin || sp.Debug {
		t.Fatalf("%+v", sp)
	}
	if sp.Identity != "agent" || !sp.NewIdentity {
		t.Errorf("default identity: %q new=%v", sp.Identity, sp.NewIdentity)
	}
	if sp := specFor("x", Preset{Image: "claude", Identity: "shared"}, s); sp.Identity != "shared" || sp.NewIdentity {
		t.Errorf("existing: %+v", sp)
	}
}

func TestPickerKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := model{user: "me", last: "b", presets: loadPresets()}
	step := func(msg tea.Msg) {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	step(listMsg{list: []container{
		{spec: spec{Name: "a", Image: "x/claude:1"}, Running: true, State: "running"},
		{spec: spec{Name: "b", Image: "x/pwn:1", Debug: true}, State: "stopped"},
		{spec: spec{Name: "c", Image: "x/base:1"}, State: "stopped"},
	}})
	if m.cursor != 1 {
		t.Errorf("the last used isn't preselected: cursor %d", m.cursor)
	}
	view := m.View()
	for _, want := range []string{"a", "running", "claude", "debugging", "last", "⏎ connect"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	step(tea.KeyMsg{Type: tea.KeyDown})
	step(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.connect != "c" || m.result.session != "console" {
		t.Errorf("enter on c: %+v", m.result)
	}
	m.result = picked{}
	step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.result.connect != "c" || m.result.session != "" {
		t.Errorf("p on c: %+v", m.result)
	}
	m.result = picked{}
	step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	for _, r := range "work" {
		step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	step(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.connect != "c" || m.result.session != "work" {
		t.Errorf("t work on c: %+v", m.result)
	}
	m.mode = modeList
	// Filter, then new from a preset.
	m.result = picked{}
	step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "pw" {
		step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	step(tea.KeyMsg{Type: tea.KeyEnter})
	if v := m.visible(); len(v) != 1 || v[0].Name != "b" {
		t.Errorf("filter pw: %v", v)
	}
	step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	for _, r := range "lab" {
		step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	step(tea.KeyMsg{Type: tea.KeyEnter})
	step(tea.KeyMsg{Type: tea.KeyDown})
	step(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.create != "lab" || m.result.preset != m.presets.names()[1] {
		t.Errorf("new: %+v", m.result)
	}
}
