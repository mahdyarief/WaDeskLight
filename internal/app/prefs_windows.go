//go:build windows

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// ViewMode controls how services are presented.
type ViewMode string

const (
	ViewTabs  ViewMode = "tabs"
	ViewPages ViewMode = "pages"
)

type prefs struct {
	ViewMode ViewMode `json:"viewMode"`
	// Lite keeps the low-memory flags and eco-QoS behaviour on.
	// It defaults to true and exists so a future settings UI can toggle it.
	Lite bool `json:"lite"`
}

func prefsPath() string {
	return filepath.Join(getConfigDir(), "prefs.json")
}

func defaultPrefs() prefs {
	return prefs{ViewMode: ViewTabs, Lite: true}
}

func loadPrefs() prefs {
	def := defaultPrefs()
	data, err := os.ReadFile(prefsPath())
	if err != nil {
		return def
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var p prefs
	if json.Unmarshal(data, &p) != nil {
		return def
	}
	if p.ViewMode != ViewTabs && p.ViewMode != ViewPages {
		p.ViewMode = def.ViewMode
	}
	return p
}

func savePrefs(p prefs) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(prefsPath(), data, 0644)
}
