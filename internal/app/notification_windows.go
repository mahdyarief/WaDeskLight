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

// openLastNotificationChat hands a tray balloon click back to the page, which
// knows how to open the conversation the popup was for. Telegram's worker
// routes its own clicks by posting a focusMessage payload to the page, and a
// page-built Notification carries the handler the site would have run, so
// replaying either one opens the right chat. When neither exists the window
// simply comes forward, which is all the site itself would have done.
func openLastNotificationChat() {
	w := gWebView
	if w == nil {
		return
	}
	w.Dispatch(func() {
		w.Eval("window.wagramNotificationClick && window.wagramNotificationClick()")
	})
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
			// cue that says which one a popup came from. The brand colour
			// alone carries it, as a ring around the avatar and as the disc
			// behind an avatar-less popup.
			var SERVICE = /telegram/i.test(location.hostname)
				? { color: '#229ed9' }
				: { color: '#25d366' };

			// Draws the balloon icon. A missing avatar still gets the full
			// treatment, so the colour is always the thing you see.
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
			}

			function newCanvas(size) {
				var canvas = document.createElement('canvas');
				canvas.width = size;
				canvas.height = size;
				return canvas;
			}

			// Always resolves to a data URL the native side can turn into an
			// icon. The colour is the point, so an unreachable avatar degrades
			// to a tinted disc rather than to no icon at all.
			function iconDataURL(url) {
				var size = 64;
				var canvas = newCanvas(size);
				var ctx = canvas.getContext('2d');
				var tinted = function() {
					paintIcon(ctx, size, null);
					return canvas.toDataURL('image/png');
				};
				if (!url) { return Promise.resolve(tinted()); }
				return fetch(url)
					.then(function(r) { return r.blob(); })
					.then(function(blob) { return createImageBitmap(blob); })
					.then(function(bmp) {
						paintIcon(ctx, size, bmp);
						bmp.close();
						return canvas.toDataURL('image/png');
					})
					.catch(tinted);
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

			// A balloon is native, so the browser never delivers its click to
			// the page. Remember what the newest popup was for and let the host
			// replay the click into the page it came from. Only the newest
			// matters: Windows replaces the balloon, so an older target would
			// open the wrong conversation.
			var lastNotification = null;
			var lastTarget = null;

			// Called by the host when the balloon is clicked.
			window.wagramNotificationClick = function() {
				if (lastTarget && navigator.serviceWorker && typeof MessageEvent === 'function') {
					// Telegram's worker routes a click by posting this payload
					// back to the page, which opens the chat with its own logic.
					try {
						navigator.serviceWorker.dispatchEvent(new MessageEvent('message', {
							data: { type: 'focusMessage', payload: lastTarget }
						}));
						return true;
					} catch (e) {}
				}
				if (lastNotification && typeof lastNotification.onclick === 'function') {
					try { lastNotification.onclick({}); return true; } catch (e) {}
				}
				return false;
			};

			window.Notification = function(title, options) {
				options = options || {};
				deliver(title, options.body, options.icon);
				// The site assigns onclick after construction, so keep the
				// object rather than the handler.
				lastNotification = this;
				lastTarget = null;
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
					// No page-side handler exists for this path, so the newest
					// popup has no target and must clear any older one.
					lastNotification = null;
					lastTarget = null;
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
							// Mirror the data the worker attaches to its own
							// notification, which is what it posts back to the
							// page when the popup is clicked.
							if (payload.chatId) {
								lastTarget = {
									chatId: payload.chatId,
									messageId: payload.messageId,
									reaction: payload.reaction,
									count: 1,
									shouldReplaceHistory: payload.shouldReplaceHistory
								};
								lastNotification = null;
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
