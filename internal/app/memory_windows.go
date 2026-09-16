//go:build windows

package app

import (
	"sync/atomic"
	"time"
)

const (
	// idlePollMS is how often the idle check runs. Coarse on purpose: each
	// tick is one visibility syscall plus a timestamp comparison.
	idlePollMS = 5000
	// idleLowAfter is how long the window may sit untouched before WebView2
	// is asked to drop its caches. Long enough that reading a chat does not
	// trigger it, short enough to matter for a window left open all day.
	idleLowAfter = 60 * time.Second
)

var (
	// gLowMemory tracks whether WebView2 is currently at its low target, so
	// the restore path only pays the cost when there is something to undo.
	gLowMemory atomic.Bool
	// gLiteOn mirrors prefs.Lite so the idle timer does not read prefs.json
	// off disk on every tick. Refreshed at startup and on every toggle.
	gLiteOn atomic.Bool
	// gLastActivity is the UnixNano of the last user input seen by the window.
	gLastActivity atomic.Int64
)

// startIdleTimer arms the periodic idle check. Call once the window proc is
// subclassed, so WM_TIMER reaches idleTick.
func startIdleTimer(hwnd uintptr) {
	gLiteOn.Store(liteEnabled(loadPrefs()))
	gLastActivity.Store(time.Now().UnixNano())
	procSetTimer.Call(hwnd, timerIdle, idlePollMS, 0)
}

// enterLowMemory drops WebView2 to its low memory target. It is a no-op when
// Lite is off or the target is already low.
func enterLowMemory() {
	if !gLiteOn.Load() {
		return
	}
	if gLowMemory.Swap(true) {
		return
	}
	setMemoryUsageTargetLevel(memoryUsageLow)
	setEcoQoS(true)
}

// exitLowMemory restores full speed. Safe to call at any time.
func exitLowMemory() {
	if !gLowMemory.Swap(false) {
		return
	}
	setMemoryUsageTargetLevel(memoryUsageNormal)
	setEcoQoS(false)
}

// markActivity records that the user touched the window and, if WebView2 had
// been dropped to its low target, brings it back to full speed. Runs on the UI
// thread from the window procedure.
func markActivity() {
	gLastActivity.Store(time.Now().UnixNano())
	exitLowMemory()
}

// applyLiteSetting re-reads the setting after a toggle: turning it off releases
// any active low state right away.
func applyLiteSetting() {
	gLiteOn.Store(liteEnabled(loadPrefs()))
	if !gLiteOn.Load() {
		exitLowMemory()
	}
}

// idleTick runs on the UI thread from WM_TIMER and drops WebView2 to its low
// memory target once the window has been untouched for idleLowAfter. Only a
// visible window qualifies; a hidden one is already handled by the tray.
func idleTick(hwnd uintptr) {
	if gLowMemory.Load() {
		return
	}
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible == 0 {
		return
	}
	last := gLastActivity.Load()
	if last == 0 || time.Since(time.Unix(0, last)) < idleLowAfter {
		return
	}
	enterLowMemory()
}
