package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// refreshSpeed names an automatic update cadence, as in the web UI.
type refreshSpeed string

const (
	refreshPaused  refreshSpeed = "pause"
	refreshTurtle  refreshSpeed = "turtle"
	refreshLlama   refreshSpeed = "llama"
	refreshCheetah refreshSpeed = "cheetah"
)

var refreshSpeeds = []struct {
	speed        refreshSpeed
	label        string
	milliseconds int
}{
	{refreshPaused, "Paused", 0},
	{refreshTurtle, "Turtle (60 s)", 60000},
	{refreshLlama, "Llama (10 s)", 10000},
	{refreshCheetah, "Cheetah (2 s)", 2000},
}

func refreshSpeedLabel(speed refreshSpeed) string {
	for _, entry := range refreshSpeeds {
		if entry.speed == speed {
			return entry.label
		}
	}
	return refreshSpeedLabel(refreshLlama)
}

func refreshInterval(speed refreshSpeed) int {
	for _, entry := range refreshSpeeds {
		if entry.speed == speed {
			return entry.milliseconds
		}
	}
	return 10000
}

type colourScheme string

const (
	schemeSystem colourScheme = "system"
	schemeDark   colourScheme = "dark"
	schemeLight  colourScheme = "light"
)

// settings persists user preferences in the user's configuration directory.
type settings struct {
	RefreshSpeed     refreshSpeed `json:"refresh_speed"`
	ColourScheme     colourScheme `json:"colour_scheme"`
	DismissedWarning string       `json:"dismissed_warning"`
	WindowWidth      int          `json:"window_width"`
	WindowHeight     int          `json:"window_height"`
	SidebarWidth     int          `json:"sidebar_width"`
	ConsoleScale     *bool        `json:"console_scale,omitempty"`

	path string
}

func settingsPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "argui", "settings.json")
}

func loadSettings(path string) *settings {
	s := &settings{RefreshSpeed: refreshLlama, ColourScheme: schemeDark, WindowWidth: 1180, WindowHeight: 760, SidebarWidth: 220, path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, s)
	}
	if refreshSpeedLabel(s.RefreshSpeed) != refreshSpeedLabel(refreshLlama) || s.RefreshSpeed == refreshLlama {
		// The speed is known.
	} else {
		s.RefreshSpeed = refreshLlama
	}
	switch s.ColourScheme {
	case schemeSystem, schemeDark, schemeLight:
	default:
		s.ColourScheme = schemeDark
	}
	return s
}

func (s *settings) save() error {
	if s.path == "" {
		return errors.New("settings have no path")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func (s *settings) consoleScaleToWindow() bool {
	return s.ConsoleScale == nil || *s.ConsoleScale
}
