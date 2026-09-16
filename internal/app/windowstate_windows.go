//go:build windows

package app

import (
	"encoding/json"
	"os"
	"unsafe"
)

type rect struct {
	Left, Top, Right, Bottom int32
}

type point struct {
	X, Y int32
}

type windowState struct {
	X      int32 `json:"x"`
	Y      int32 `json:"y"`
	Width  int32 `json:"width"`
	Height int32 `json:"height"`
	// Maximized is tracked separately because a maximized window's rect is not
	// a size worth restoring; only the show state reproduces it faithfully.
	Maximized bool `json:"maximized"`
	Saved     bool `json:"saved"`
}

type windowPlacement struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    point
	PtMaxPosition    point
	RcNormalPosition rect
}

func windowStatePath() string {
	return windowStatePathFor(gProfileID)
}

func loadWindowState() windowState {
	var st windowState
	data, err := os.ReadFile(windowStatePath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func saveWindowStateTo(path string, st windowState) {
	data, _ := json.Marshal(st)
	_ = os.WriteFile(path, data, 0644)
}

// captureWindowState reads the window's restored geometry and whether it is
// maximized. It reports false while the window is hidden in the tray, where the
// placement says nothing useful and the last saved state should stand.
func captureWindowState(hwnd uintptr) (windowState, bool) {
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return windowState{}, false
	}
	if wp.ShowCmd == swHide {
		return windowState{}, false
	}
	return windowState{
		X:         wp.RcNormalPosition.Left,
		Y:         wp.RcNormalPosition.Top,
		Width:     wp.RcNormalPosition.Right - wp.RcNormalPosition.Left,
		Height:    wp.RcNormalPosition.Bottom - wp.RcNormalPosition.Top,
		Maximized: wp.ShowCmd == swShowMaximized,
		Saved:     true,
	}, true
}

// applyWindowState works across processes, so it can also size another
// account's window.
func applyWindowState(hwnd uintptr, st windowState) {
	if !st.Saved {
		return
	}
	wp := windowPlacement{
		ShowCmd: swShowNormal,
		RcNormalPosition: rect{
			Left: st.X, Top: st.Y,
			Right: st.X + st.Width, Bottom: st.Y + st.Height,
		},
	}
	wp.Length = uint32(unsafe.Sizeof(wp))
	if st.Maximized {
		wp.ShowCmd = swShowMaximized
	}
	procSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
}

func saveWindowBounds(hwnd uintptr) {
	if st, ok := captureWindowState(hwnd); ok {
		saveWindowStateTo(windowStatePath(), st)
	}
}

// restoreWindow brings the window back exactly as it was left. SW_RESTORE alone
// would un-maximize a window that was maximized when it went to the tray.
func restoreWindow(hwnd uintptr) {
	show := uintptr(swRestore)
	if st := loadWindowState(); st.Saved && st.Maximized {
		show = swShowMaximized
	}
	procShowWindow.Call(hwnd, show)
	procSetFgWindow.Call(hwnd)
}
