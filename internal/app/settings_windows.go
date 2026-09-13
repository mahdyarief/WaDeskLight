//go:build windows

package app

import (
	"reflect"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
)

// browserOf reaches go-webview2's Chromium backend, which the library keeps
// unexported and offers no accessor for. Fields are located by name rather than
// by offset, so an upstream layout change degrades to nil instead of handing
// back a bogus pointer.
func browserOf(w webview2.WebView) (c *edge.Chromium) {
	defer func() {
		if recover() != nil {
			c = nil
		}
	}()
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil
	}
	browser := v.Elem().FieldByName("browser")
	if !browser.IsValid() || browser.Kind() != reflect.Interface || browser.IsNil() {
		return nil
	}
	chromium := browser.Elem()
	if chromium.Kind() != reflect.Pointer || chromium.IsNil() {
		return nil
	}
	return (*edge.Chromium)(chromium.UnsafePointer())
}

// enableContextMenu restores the right-click menu. go-webview2 ties it to its
// Debug option, which also switches on DevTools, so running with Debug off left
// no way to copy or save an image. DevTools stay off; only the menu comes back.
func enableContextMenu(w webview2.WebView) {
	chromium := browserOf(w)
	if chromium == nil {
		return
	}
	settings, err := chromium.GetSettings()
	if err != nil || settings == nil {
		return
	}
	_ = settings.PutAreDefaultContextMenusEnabled(true)
}

// coreWebView2Ptr returns the raw ICoreWebView2 the backend holds.
func coreWebView2Ptr(w webview2.WebView) (ptr unsafe.Pointer) {
	defer func() {
		if recover() != nil {
			ptr = nil
		}
	}()
	chromium := browserOf(w)
	if chromium == nil {
		return nil
	}
	field := reflect.ValueOf(chromium).Elem().FieldByName("webview")
	if !field.IsValid() || field.Kind() != reflect.Pointer {
		return nil
	}
	return field.UnsafePointer()
}
