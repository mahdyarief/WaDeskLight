//go:build windows

package app

import "github.com/jchv/go-webview2"

// accountsView is the snapshot the overlay script reads to render the account
// list and the settings rows.
type accountsView struct {
	Current  string    `json:"current"`
	Accounts []account `json:"accounts"`
	Prefs    prefs     `json:"prefs"`
}

// registerBindings exposes the host functions the injected scripts call.
func registerBindings(w webview2.WebView, hwnd uintptr) {
	_ = w.Bind("sendNativeNotification", func(title, body, iconDataURL string) {
		if !notificationsEnabled(loadPrefs()) {
			return
		}
		icon := decodeDataURL(iconDataURL)
		go trayBalloon(title, body, icon)
	})
	_ = w.Bind("wadeskNotificationsSet", func(on bool) {
		p := loadPrefs()
		setNotificationsEnabled(&p, on)
		savePrefs(p)
		setNotificationPermission(on)
	})
	_ = w.Bind("wadeskLiteSet", func(on bool) {
		p := loadPrefs()
		setLiteEnabled(&p, on)
		savePrefs(p)
		// The binding may run off the UI thread, and both the memory target and
		// the eco-QoS handoff need it; hop over before touching them.
		procPostMessageW.Call(hwnd, wmApplyLite, 0, 0)
	})

	_ = w.Bind("wadeskAccountsState", func() accountsView {
		return accountsView{Current: gProfileID, Accounts: loadAccounts(), Prefs: loadPrefs()}
	})
	_ = w.Bind("wadeskAccountSwitch", func(id string) {
		for _, a := range loadAccounts() {
			if a.ID == id && a.ID != gProfileID {
				selectAccount(hwnd, a)
				return
			}
		}
	})
	_ = w.Bind("wadeskTabSelect", func(id string) {
		for _, a := range loadAccounts() {
			if a.ID == id {
				selectAccount(hwnd, a)
				return
			}
		}
	})
	_ = w.Bind("wadeskAccountAdd", func() {
		focusAccount(hwnd, addAccount())
	})
	_ = w.Bind("wadeskAccountAddService", func(svc string) {
		focusAccount(hwnd, addServiceAccount(Service(svc)))
	})
	_ = w.Bind("wadeskPrefsGet", func() prefs {
		return loadPrefs()
	})
	_ = w.Bind("wadeskPrefsSet", func(mode string) {
		p := loadPrefs()
		if mode == string(ViewPages) {
			p.ViewMode = ViewPages
			savePrefs(p)
			showAllAccounts()
		} else {
			p.ViewMode = ViewTabs
			if p.ActiveID == "" {
				p.ActiveID = gProfileID
			}
			savePrefs(p)
			showOnlyAccount(p.ActiveID)
		}
	})
	_ = w.Bind("wadeskAccountRename", func(id, name string) {
		renameAccount(id, name)
		if id == gProfileID {
			w.Dispatch(func() { applyAccountName(hwnd, accountFor(id)) })
		}
	})
	// Only the account you are looking at can be removed: another instance owns
	// its own window and files, and has no channel to be told to shut down.
	_ = w.Bind("wadeskAccountRemove", func(id string) {
		if id != gProfileID || id == defaultProfileID {
			return
		}
		w.Dispatch(func() {
			gPendingRemove = true
			quitInstance(hwnd)
		})
	})
}
