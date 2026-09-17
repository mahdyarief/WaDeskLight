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

			// Two services share one balloon layout, so the icon is the only
			// cue that says which one a popup came from. A ring in the brand
			// colour plus a corner chip carrying the initial reads at the size
			// Windows scales a balloon icon down to, and needs no extra text.
			var SERVICE = /telegram/i.test(location.hostname)
				? { color: '#229ed9', letter: 'T' }
				: { color: '#25d366', letter: 'W' };

			// Draws the balloon icon. A missing avatar still gets the full
			// treatment, so the badge is always the thing you see.
			function paintIcon(ctx, size, bmp) {
				var cx = size / 2, cy = size / 2, r = size / 2;
				ctx.clearRect(0, 0, size, size);

				ctx.save();
				ctx.beginPath();
				ctx.arc(cx, cy, r - 3, 0, Math.PI * 2);
				ctx.closePath();
				ctx.clip();
				if (bmp) {
					// WhatsApp hands us a blob: URL, which is same-origin and
					// therefore does not taint the canvas.
					ctx.drawImage(bmp, 0, 0, size, size);
				} else {
					ctx.fillStyle = SERVICE.color;
					ctx.fillRect(0, 0, size, size);
				}
				ctx.restore();

				ctx.beginPath();
				ctx.arc(cx, cy, r - 3, 0, Math.PI * 2);
				ctx.lineWidth = 4;
				ctx.strokeStyle = SERVICE.color;
				ctx.stroke();

				// The chip sits over the avatar, so it needs a dark outline to
				// stay separate from a light or busy picture.
				var br = size * 0.26;
				var bx = size - br - 1, by = size - br - 1;
				ctx.beginPath();
				ctx.arc(bx, by, br, 0, Math.PI * 2);
				ctx.fillStyle = SERVICE.color;
				ctx.fill();
				ctx.lineWidth = 3;
				ctx.strokeStyle = 'rgba(11, 20, 26, 0.85)';
				ctx.stroke();
				ctx.fillStyle = '#ffffff';
				ctx.font = '700 ' + Math.round(br * 1.25) + 'px system-ui, sans-serif';
				ctx.textAlign = 'center';
				ctx.textBaseline = 'middle';
				ctx.fillText(SERVICE.letter, bx, by + 1);
			}

			function newCanvas(size) {
				var canvas = document.createElement('canvas');
				canvas.width = size;
				canvas.height = size;
				return canvas;
			}

			// Always resolves to a data URL the native side can turn into an
			// icon. The badge is the point, so an unreachable avatar degrades
			// to a badged disc rather than to no icon at all.
			function iconDataURL(url) {
				var size = 64;
				var canvas = newCanvas(size);
				var ctx = canvas.getContext('2d');
				var badged = function() {
					paintIcon(ctx, size, null);
					return canvas.toDataURL('image/png');
				};
				if (!url) { return Promise.resolve(badged()); }
				return fetch(url)
					.then(function(r) { return r.blob(); })
					.then(function(blob) { return createImageBitmap(blob); })
					.then(function(bmp) {
						paintIcon(ctx, size, bmp);
						bmp.close();
						return canvas.toDataURL('image/png');
					})
					.catch(badged);
			}

			// Every notification path funnels through here, so a client that
			// uses the constructor, the service worker registration, or the
			// Permissions API all end up at the same native balloon.
			function deliver(title, body, iconURL) {
				if (!hasBridge) { return; }
				var size = 64;
				var canvas = newCanvas(size);
				paintIcon(canvas.getContext('2d'), size, null);
				var fallback = canvas.toDataURL('image/png');
				// Never let a slow avatar fetch hold up the notification.
				var timeout = new Promise(function(resolve) {
					setTimeout(function() { resolve(fallback); }, 1500);
				});
				Promise.race([iconDataURL(iconURL), timeout])
					.then(function(icon) {
						window.sendNativeNotification(String(title), String(body || ''), icon || fallback);
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

			// Telegram Web hands its popups to the service worker over
			// postMessage, and the worker calls self.registration.
			// showNotification from inside its own global scope. None of the
			// patches above exist there: a document-created script never runs
			// in a worker. Catching the outgoing message is the only place on
			// this side of the boundary where the popup is still visible.
			if (typeof ServiceWorker !== 'undefined' &&
				ServiceWorker.prototype &&
				ServiceWorker.prototype.postMessage) {
				var realPostMessage = ServiceWorker.prototype.postMessage;
				ServiceWorker.prototype.postMessage = function(message, transfer) {
					try {
						if (message && message.type === 'showMessageNotification') {
							var payload = message.payload || {};
							// Telegram marks silent notifications so a muted
							// chat still updates the badge without a popup.
							if (!payload.isSilent) {
								deliver(payload.title, payload.body, payload.icon);
							}
						}
					} catch (e) {}
					// Still deliver to the worker: it keeps the notification
					// tag bookkeeping that later closes them by chat.
					return realPostMessage.call(this, message, transfer);
				};
			}
		})();
`
