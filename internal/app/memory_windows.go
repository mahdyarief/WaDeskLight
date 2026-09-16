//go:build windows

package app

import (
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// IID_ICoreWebView2_19, from WebView2.h in the Microsoft.Web.WebView2 SDK.
var iidCoreWebView219 = windows.GUID{
	Data1: 0x6921f954,
	Data2: 0x79b0,
	Data3: 0x437f,
	Data4: [8]byte{0xa9, 0x97, 0xc8, 0x58, 0x11, 0x89, 0x7c, 0x68},
}

const (
	memoryUsageNormal = 0
	memoryUsageLow    = 1
)

type unknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type comUnknown struct {
	vtbl *unknownVtbl
}

// coreWebView219Vtbl covers the 121 flattened slots of ICoreWebView2_19, of
// which only the last two are of interest here.
type coreWebView219Vtbl struct {
	unused                    [119]uintptr
	GetMemoryUsageTargetLevel uintptr
	PutMemoryUsageTargetLevel uintptr
}

type coreWebView219 struct {
	vtbl *coreWebView219Vtbl
}

var gMemoryIface atomic.Pointer[coreWebView219]

// initMemoryControl caches ICoreWebView2_19 when the installed WebView2 Runtime
// is new enough (1.0.1823.32+). On older runtimes QueryInterface fails and
// memory levelling is skipped; everything else keeps working.
func initMemoryControl(w webview2.WebView) {
	base := coreWebView2Ptr(w)
	if base == nil {
		return
	}
	var iface unsafe.Pointer
	hr, _, _ := syscall.SyscallN(
		(*comUnknown)(base).vtbl.QueryInterface,
		uintptr(base),
		uintptr(unsafe.Pointer(&iidCoreWebView219)),
		uintptr(unsafe.Pointer(&iface)),
	)
	if hr != 0 || iface == nil {
		return
	}
	gMemoryIface.Store((*coreWebView219)(iface))
}

// setMemoryUsageTargetLevel asks WebView2 to release what it can (low) or to
// return to full speed (normal). Scripts keep running at either level, so the
// WhatsApp socket and its notifications survive the low setting.
//
// Must be called on the UI thread.
func setMemoryUsageTargetLevel(level uintptr) {
	obj := gMemoryIface.Load()
	if obj == nil {
		return
	}
	syscall.SyscallN(obj.vtbl.PutMemoryUsageTargetLevel, uintptr(unsafe.Pointer(obj)), level)
}

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
