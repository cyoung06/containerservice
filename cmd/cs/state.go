package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// configDir is ~/.config/cs (presets.toml, state.json).
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "cs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "cs")
}

type state struct {
	Last string `json:"last"` // the container connected to last
}

func loadState() state {
	var s state
	if data, err := os.ReadFile(filepath.Join(configDir(), "state.json")); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

func saveLast(name string) {
	s := loadState()
	s.Last = name
	data, _ := json.Marshal(s)
	os.MkdirAll(configDir(), 0o700)
	os.WriteFile(filepath.Join(configDir(), "state.json"), data, 0o600)
}
