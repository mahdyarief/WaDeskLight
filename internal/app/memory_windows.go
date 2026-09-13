//go:build windows

package app

import (
	"sync/atomic"
	"syscall"
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
