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
	// ActiveID is the account shown in tabs mode. Empty means current.
	ActiveID string `json:"activeAccount,omitempty"`
	// Notifications toggles message popup balloons. Nil means on (default),
	// so prefs.json files written before this setting existed stay enabled.
	Notifications *bool `json:"notifications,omitempty"`
	// Lite keeps the low-memory and eco-QoS behaviour on: WebView2 drops to
	// its low memory target while the window is hidden and once it has been
	// left untouched. Nil means on (default), same as Notifications, so files
	// written before the setting existed keep the original behaviour.
	Lite *bool `json:"lite,omitempty"`
}

func prefsPath() string {
	return filepath.Join(getConfigDir(), "prefs.json")
}

func defaultPrefs() prefs {
	return prefs{ViewMode: ViewTabs}
}

func notificationsEnabled(p prefs) bool {
	return p.Notifications == nil || *p.Notifications
}

func setNotificationsEnabled(p *prefs, on bool) {
	v := on
	p.Notifications = &v
}

func liteEnabled(p prefs) bool {
	return p.Lite == nil || *p.Lite
}

func setLiteEnabled(p *prefs, on bool) {
	v := on
	p.Lite = &v
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
