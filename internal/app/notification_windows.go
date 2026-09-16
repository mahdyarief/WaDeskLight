//go:build windows

package app

import (
	"sync/atomic"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
)

// gChromium is the backend handle, kept so the notification permission can be
// re-answered when the setting changes without threading the webview down into
// the tray menu handler.
var gChromium atomic.Pointer[edge.Chromium]

// initNotificationPermission caches the backend so the answer can be changed at
// runtime, then applies the current setting.
func initNotificationPermission(w webview2.WebView) {
	if chromium := browserOf(w); chromium != nil {
		gChromium.Store(chromium)
	}
	setNotificationPermission(notificationsEnabled(loadPrefs()))
}

// setNotificationPermission answers WebView2's notification permission request.
// A host that never handles the event leaves Chromium on its default, which is
// to deny, and that silently breaks any client that gates its popups on it. The
// injected window.Notification shim is not enough on its own: Telegram Web
// consults the Permissions API, and its background popups go out through a
// service worker, so both paths bypass the shim.
func setNotificationPermission(on bool) {
	chromium := gChromium.Load()
	if chromium == nil {
		return
	}
	state := edge.CoreWebView2PermissionStateDeny
	if on {
		state = edge.CoreWebView2PermissionStateAllow
	}
	chromium.SetPermission(edge.CoreWebView2PermissionKindNotifications, state)
}

// notificationPolyfillJS is the injected Notification API polyfill: it
// funnels window.Notification, the Permissions API and service-worker
// showNotification into the native tray balloon bridge.
const notificationPolyfillJS = `		// Native Notification Polyfill for Windows Tray Balloon
		(function() {
			var hasBridge = typeof window.sendNativeNotification === 'function';

			// Re-encode the sender avatar as a PNG data URL the native side can
			// turn into an icon. WhatsApp hands us a blob: URL, which is
			// same-origin and therefore does not taint the canvas.
			function avatarDataURL(url) {
				if (!url) { return Promise.resolve(''); }
				return fetch(url)
					.then(function(r) { return r.blob(); })
					.then(function(blob) { return createImageBitmap(blob); })
					.then(function(bmp) {
						var size = 64;
						var canvas = document.createElement('canvas');
						canvas.width = size;
						canvas.height = size;
						canvas.getContext('2d').drawImage(bmp, 0, 0, size, size);
						bmp.close();
						return canvas.toDataURL('image/png');
					})
					.catch(function() { return ''; });
			}

			// Every notification path funnels through here, so a client that
			// uses the constructor, the service worker registration, or the
			// Permissions API all end up at the same native balloon.
			function deliver(title, body, iconURL) {
				if (!hasBridge) { return; }
				// Never let a slow avatar fetch hold up the notification.
				var timeout = new Promise(function(resolve) {
					setTimeout(function() { resolve(''); }, 1500);
				});
				Promise.race([avatarDataURL(iconURL), timeout])
					.then(function(icon) {
						window.sendNativeNotification(String(title), String(body || ''), icon || '');
					});
			}

			window.Notification = function(title, options) {
				options = options || {};
				deliver(title, options.body, options.icon);
				this.title = title;
				this.onclick = null;
				this.onclose = null;
				this.onerror = null;
				this.onshow = null;
				// A balloon cannot be retracted, but clients close notifications
				// routinely, and a missing method would throw in their cleanup.
				this.close = function() {};
			};
			Object.defineProperty(window.Notification, 'permission', {
				get: function() { return hasBridge ? 'granted' : 'default'; }
			});
			window.Notification.requestPermission = function(callback) {
				var perm = hasBridge ? 'granted' : 'default';
				if (typeof callback === 'function') {
					callback(perm);
				}
				return Promise.resolve(perm);
			};

			// Telegram Web gates its popups on the Permissions API rather than
			// on Notification.permission, so the answer has to come from here
			// too or the notification is never even attempted.
			if (navigator.permissions && navigator.permissions.query) {
				var realQuery = navigator.permissions.query.bind(navigator.permissions);
				navigator.permissions.query = function(desc) {
					if (desc && desc.name === 'notifications') {
						return Promise.resolve({
							state: hasBridge ? 'granted' : 'denied',
							onchange: null,
							addEventListener: function() {},
							removeEventListener: function() {},
							dispatchEvent: function() { return false; }
						});
					}
					return realQuery(desc);
				};
			}

			// Background popups go out through the service worker registration,
			// which never touches the constructor above.
			if (typeof ServiceWorkerRegistration !== 'undefined' &&
				ServiceWorkerRegistration.prototype &&
				ServiceWorkerRegistration.prototype.showNotification) {
				ServiceWorkerRegistration.prototype.showNotification = function(title, options) {
					options = options || {};
					deliver(title, options.body, options.icon);
					return Promise.resolve();
				};
			}
		})();
`
