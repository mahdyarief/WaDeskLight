//go:build windows

package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// agentRequestTimeout bounds how long a Go->JS call waits for the page.
const agentRequestTimeout = 8 * time.Second

var (
	agentMu      sync.Mutex
	agentPending = map[string]chan string{}
	agentSeq     int64
)

// jsQuote renders s as a JavaScript string literal.
func jsQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// agentReply resolves a pending Go->JS request. The injected script calls the
// bound function of this name with the request id and its JSON result.
func agentReply(id, result string) {
	agentMu.Lock()
	ch := agentPending[id]
	delete(agentPending, id)
	agentMu.Unlock()
	if ch != nil {
		ch <- result
	}
}

// callAgent asks the page's injected agent to run fn with args and waits for
// its JSON result. It must not be called from the UI thread.
func callAgent(w webview2.WebView, fn string, args any) (json.RawMessage, error) {
	id := strconv.FormatInt(atomic.AddInt64(&agentSeq, 1), 10)
	ch := make(chan string, 1)
	agentMu.Lock()
	agentPending[id] = ch
	agentMu.Unlock()

	payload, err := json.Marshal(args)
	if err != nil {
		agentMu.Lock()
		delete(agentPending, id)
		agentMu.Unlock()
		return nil, err
	}
	js := "window.wagramAgent && window.wagramAgent.call(" +
		jsQuote(id) + "," + jsQuote(fn) + "," + jsQuote(string(payload)) + ");"
	w.Dispatch(func() { w.Eval(js) })

	select {
	case res := <-ch:
		return json.RawMessage(res), nil
	case <-time.After(agentRequestTimeout):
		agentMu.Lock()
		delete(agentPending, id)
		agentMu.Unlock()
		return nil, errors.New("agent request timed out")
	}
}

// agentScript is injected into every page. It exposes window.wagramAgent, a
// small RPC shim: Go calls wagramAgent.call(id, fn, argsJson); the page runs the
// matching AGENT function and answers via the bound wagramAgentReply(id, json).
const agentScript = `
(function () {
	if (window.wagramAgent) { return; }

	function q(sel) { try { return document.querySelector(sel); } catch (e) { return null; } }
	function qa(sel) { try { return Array.prototype.slice.call(document.querySelectorAll(sel)); } catch (e) { return []; } }
	function first(sels) {
		for (var i = 0; i < sels.length; i++) { var el = q(sels[i]); if (el) { return el; } }
		return null;
	}
	function all(sels) {
		for (var i = 0; i < sels.length; i++) { var els = qa(sels[i]); if (els.length) { return els; } }
		return [];
	}
	function txt(el) { return el ? (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim() : ''; }
	// WhatsApp's chat rows expose an accessibility label like
	// "23 unread messages Marketplace 23". The current build runs the count
	// and the mute/bell icon ligature text straight into the name
	// ("101 unread messagesGeneralic-notifications-off101"), so strip the
	// leading "N unread message(s)", any trailing icon + repeated count, and
	// any stray variation selectors left behind by emoji in the name.
	function stripUnread(s) {
		s = (s || '').trim();
		var m = s.match(/^\d+\s+unread\s+messages?\s*(.*)$/i);
		if (!m) { return s; }
		return m[1]
			.replace(/(?:\s*ic-[a-z0-9-]*)+\s*\d*$/i, '')
			.replace(/\s+\d+$/, '')
			.replace(/^[\s\uFE0E\uFE0F]+/, '')
			.replace(/[\s\uFE0E\uFE0F]+$/, '');
	}
	// WhatsApp selects a chat on pointer/mouse-down, not on a bare click, so a
	// synthetic click() alone is ignored. Dispatch the whole sequence and let
	// it bubble to the row's handler.
	function fireClick(el) {
		if (!el) { return; }
		try { el.scrollIntoView({ block: 'center' }); } catch (e) {}
		var opts = { bubbles: true, cancelable: true, view: window, detail: 1 };
		try { el.dispatchEvent(new PointerEvent('pointerdown', opts)); } catch (e) {}
		try { el.dispatchEvent(new MouseEvent('mousedown', opts)); } catch (e) {}
		try { el.dispatchEvent(new PointerEvent('pointerup', opts)); } catch (e) {}
		try { el.dispatchEvent(new MouseEvent('mouseup', opts)); } catch (e) {}
		try { el.dispatchEvent(new MouseEvent('click', opts)); } catch (e) {}
	}
	function host() { return location.host || ''; }
	function isWA() { return host().indexOf('whatsapp') !== -1; }
	function isTG() { return host().indexOf('telegram') !== -1; }

	function composer() {
		return first([
			'[data-testid="conversation-compose-box-input"]',
			'footer div[contenteditable="true"]',
			'#message-input div[contenteditable="true"]',
			'.input-field-input[contenteditable="true"]',
			'div[contenteditable="true"][role="textbox"]'
		]);
	}

	function setComposer(text) {
		var box = composer();
		if (!box) { throw new Error('composer not found'); }
		box.focus();
		var sel = window.getSelection();
		var range = document.createRange();
		range.selectNodeContents(box);
		sel.removeAllRanges();
		sel.addRange(range);
		document.execCommand('insertText', false, text);
		// execCommand normally fires input for us, but re-dispatch it so React's
		// controlled state definitely sees the text and the send button lights up.
		try { box.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text })); } catch (e) {}
		return true;
	}

	function sendButton() {
		return first([
			'[data-testid="send"]',
			'button[aria-label="Send"]',
			'button[aria-label="Kirim"]',
			'button[aria-label="Send message"]',
			'button[aria-label="Kirim pesan"]',
			'span[data-icon="send"]',
			'button.send'
		]);
	}

	// WhatsApp's send button reacts to the full pointer sequence rather than a
	// bare click(), the same way its chat rows do. When no button is in the DOM
	// yet, fall back to a complete Enter press on the composer.
	function clickSend() {
		var btn = sendButton();
		if (btn) {
			fireClick(btn.closest('button') || btn);
			return true;
		}
		var box = composer();
		if (box) {
			var opts = { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true };
			try { box.dispatchEvent(new KeyboardEvent('keydown', opts)); } catch (e) {}
			try { box.dispatchEvent(new KeyboardEvent('keypress', opts)); } catch (e) {}
			try { box.dispatchEvent(new KeyboardEvent('keyup', opts)); } catch (e) {}
			return true;
		}
		return false;
	}

	// The left-pane search filters the chat list and also surfaces contacts
	// you have never chatted with, which is how a chat below the fold (or a
	// brand-new one) becomes reachable. WhatsApp has changed this input
	// repeatedly, so try the known hooks first and then fall back to "the
	// contenteditable (or text input) that is not the composer".
	function searchBox() {
		var named = first([
			'[data-testid="chat-list-search"]',
			'#side div[contenteditable="true"][data-tab="3"]',
			'div[aria-label="Search input textbox"]',
			'input[aria-label="Search input textbox"]',
			'#side input[type="text"]',
			'#side input[type="search"]'
		]);
		if (named) { return named; }
		var comp = composer();
		var boxes = qa('div[contenteditable="true"]');
		for (var i = 0; i < boxes.length; i++) {
			if (boxes[i] !== comp) { return boxes[i]; }
		}
		var inputs = qa('input');
		for (var j = 0; j < inputs.length; j++) {
			var it = (inputs[j].type || '').toLowerCase();
			if (it === 'text' || it === 'search') { return inputs[j]; }
		}
		return null;
	}

	// Type into a contenteditable editor or a native text input. WhatsApp's
	// search is a plain <input> whose value must be written through the native
	// setter so React notices the change; the composer is a contenteditable
	// that takes execCommand. Returns what actually landed, for diagnostics.
	function setBoxText(box, text) {
		box.focus();
		if (box.tagName === 'INPUT' || box.tagName === 'TEXTAREA') {
			var proto = box.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
			var desc = Object.getOwnPropertyDescriptor(proto, 'value');
			if (desc && desc.set) { desc.set.call(box, text); } else { box.value = text; }
			box.dispatchEvent(new Event('input', { bubbles: true }));
			return boxText(box);
		}
		var sel = window.getSelection();
		var range = document.createRange();
		range.selectNodeContents(box);
		sel.removeAllRanges();
		sel.addRange(range);
		var ok = false;
		try { ok = document.execCommand('insertText', false, text); } catch (e) {}
		if (!ok || boxText(box) === '') {
			try {
				range.deleteContents();
				range.insertNode(document.createTextNode(text));
			} catch (e2) {
				box.textContent = text;
			}
			try { box.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text })); } catch (e3) {}
		}
		return boxText(box);
	}

	function boxText(box) {
		var v = box.value !== undefined ? box.value : (box.innerText || box.textContent || '');
		return (v || '').trim();
	}

	var AGENT = {};

	AGENT.status = function () {
		return {
			service: isTG() ? 'telegram' : (isWA() ? 'whatsapp' : 'unknown'),
			ready: !!composer(),
			title: document.title
		};
	};

	AGENT.list_chats = function () {
		var rows = all([
			'[data-testid="cell-frame-container"]',
			'#pane-side [role="listitem"]',
			'.chat-list .ListItem',
			'a.chat-item'
		]);
		var out = [];
		for (var i = 0; i < rows.length && i < 200; i++) {
			var r = rows[i];
			var nameEl = r.querySelector('[data-testid="cell-frame-title"]') || r.querySelector('span[title]') || r.querySelector('.title') || r.querySelector('.user-title');
			var prevEl = r.querySelector('[data-testid="cell-frame-secondary"]') || r.querySelector('.preview') || r.querySelector('.last-message');
			var unreadEl = r.querySelector('[data-testid="icon-unread-count"]') || r.querySelector('.unread') || r.querySelector('.badge');
			out.push({
				name: stripUnread(txt(nameEl)),
				preview: txt(prevEl),
				unread: txt(unreadEl),
				active: r.getAttribute('aria-selected') === 'true' || ('' + r.className).indexOf('active') !== -1
			});
		}
		return { chats: out };
	};

	// Open a conversation by name so the tools that act on the open
	// conversation (read_messages, send_message) can target it. Matching is
	// case-insensitive and falls back to a substring hit.
	AGENT.open_chat = function (args) {
		var want = (args && args.name) ? String(args.name).trim().toLowerCase() : '';
		if (!want) { throw new Error('name required'); }
		var rows = all([
			'[data-testid="cell-frame-container"]',
			'#pane-side [role="listitem"]',
			'.chat-list .ListItem',
			'a.chat-item'
		]);
		for (var i = 0; i < rows.length && i < 200; i++) {
			var r = rows[i];
			var nameEl = r.querySelector('[data-testid="cell-frame-title"]') || r.querySelector('span[title]') || r.querySelector('.title') || r.querySelector('.user-title');
			var name = stripUnread(txt(nameEl));
			if (name.toLowerCase() === want || name.toLowerCase().indexOf(want) !== -1) {
				fireClick(r.closest('[role="button"]') || r);
				return { opened: true, name: name };
			}
		}
		// Not visible: type it into the search box so the row renders, then
		// the caller retries open_chat once the list has filtered.
		var box = searchBox();
		if (box) {
			setBoxText(box, args.name);
			return { opened: false, searched: true, name: args.name };
		}
		return { opened: false, name: (args && args.name) || '' };
	};

	// WhatsApp's current build drops the message-in/message-out classes and
	// marks each text bubble with data-pre-plain-text instead, so try that
	// first and fall back to the older hooks. Take whichever selector yields
	// the most rows rather than the first that matches anything.
	function messageRows() {
		var sels = [
			'[data-pre-plain-text]',
			'div.message-in, div.message-out',
			'[data-testid="msg-container"]',
			'.MessageList .message',
			'.bubbles .message'
		];
		var best = [];
		for (var i = 0; i < sels.length; i++) {
			var els = qa(sels[i]);
			if (els.length > best.length) { best = els; }
		}
		return best;
	}

	function isScrollable(el) {
		if (!el || el.scrollHeight <= el.clientHeight + 20) { return false; }
		var oy = '';
		try { oy = window.getComputedStyle(el).overflowY; } catch (e) {}
		return oy === 'auto' || oy === 'scroll';
	}

	function scrollableWithin(root) {
		if (!root) { return null; }
		if (isScrollable(root)) { return root; }
		var nodes = root.querySelectorAll('div');
		for (var i = 0; i < nodes.length; i++) {
			if (isScrollable(nodes[i])) { return nodes[i]; }
		}
		return null;
	}

	function messageScroller() {
		var found = scrollableWithin(q('[data-testid="conversation-panel-messages"]'));
		if (found) { return found; }
		return scrollableWithin(q('#main'));
	}

	// Direction is marked by the bubble tail icon: tail-out for a message we
	// sent, tail-in for one we received. Walk up from the text node until an
	// ancestor owns a tail, which is the message row.
	function rowOutgoing(el) {
		var node = el;
		for (var i = 0; i < 12 && node; i++) {
			var out = node.querySelectorAll ? node.querySelectorAll('[data-icon="tail-out"]').length : 0;
			var inn = node.querySelectorAll ? node.querySelectorAll('[data-icon="tail-in"]').length : 0;
			if (out + inn > 0) { return out >= inn; }
			node = node.parentElement;
		}
		return false;
	}

	// The bubble's own timestamp is embedded in data-pre-plain-text as
	// "[14:37, 10/5/2026] sender: "; fall back to the meta node.
	function rowTime(el) {
		var ptt = el.getAttribute ? (el.getAttribute('data-pre-plain-text') || '') : '';
		var m = ptt.match(/^\[(\d{1,2}:\d{2})/);
		if (m) { return m[1]; }
		var meta = el.querySelector('[data-testid="msg-meta"]') || el.querySelector('.meta') || el.querySelector('.time');
		return txt(meta);
	}

	function msgSig(r) {
		var body = r.querySelector('.selectable-text') || r.querySelector('[data-testid="msg-text"]') || r.querySelector('.text-content');
		return txt(body) + '\u0001' + rowTime(r);
	}

	function collectMessages(rows, limit) {
		var out = [];
		var start = Math.max(0, rows.length - limit);
		for (var i = start; i < rows.length; i++) {
			var r = rows[i];
			var body = r.querySelector('.selectable-text') || r.querySelector('[data-testid="msg-text"]') || r.querySelector('.text-content');
			out.push({ text: txt(body || r), time: rowTime(r), outgoing: rowOutgoing(r) });
		}
		return out;
	}

	// Read the open conversation. WhatsApp keeps only a window of message rows
	// in the DOM, so when the caller wants more than is rendered we scroll the
	// pane toward the top, accumulating rows until we have enough or run out.
	AGENT.read_messages = function (args) {
		var limit = (args && args.limit) ? args.limit : 50;
		var scroller = messageScroller();
		if (!scroller) {
			return { messages: collectMessages(messageRows(), limit) };
		}
		return new Promise(function (resolve) {
			var seen = {};
			var acc = [];
			function snapshot() {
				var rows = messageRows();
				for (var i = rows.length - 1; i >= 0; i--) {
					var r = rows[i];
					var sig = msgSig(r);
					if (!seen[sig]) { seen[sig] = 1; acc.push(r); }
				}
			}
			function finish() {
				try { scroller.scrollTop = scroller.scrollHeight; } catch (e) {}
				acc.reverse();
				resolve({ messages: collectMessages(acc, limit) });
			}
			// Bound the loop by wall-clock time, not step count, so a busy page
			// (each setTimeout delayed) can never push us past the RPC timeout.
			// Any throw also resolves instead of hanging the request.
			var started = Date.now();
			function step() {
				try {
					snapshot();
				} catch (e) {
					finish();
					return;
				}
				if (acc.length >= limit || Date.now() - started > 3000) { finish(); return; }
				try { scroller.scrollTop = Math.max(0, scroller.scrollTop - scroller.clientHeight); } catch (e2) {}
				setTimeout(step, 250);
			}
			step();
		});
	};

	// Give React a tick to register the typed text (and enable the send
	// button) before clicking, then confirm the composer emptied, which is
	// the real signal that the message left.
	AGENT.send_message = function (args) {
		if (!args || !args.text) { throw new Error('text required'); }
		setComposer(args.text);
		return new Promise(function (resolve) {
			setTimeout(function () {
				var clicked = clickSend();
				setTimeout(function () {
					var box = composer();
					var left = box ? boxText(box) : '';
					resolve({ sent: clicked && left === '', remaining: left });
				}, 150);
			}, 150);
		});
	};

	AGENT.composer_set = function (args) {
		return { ok: setComposer((args && args.text) || '') };
	};

	AGENT.export_chat = function (args) {
		return AGENT.read_messages(args);
	};

	// Quick replies: a /token that exactly matches a saved shortcut is swapped
	// for its text as the user types. The map is pushed from Go.
	var quickReplies = {};

	document.addEventListener('input', function (ev) {
		var box = composer();
		if (!box || ev.target !== box) { return; }
		var t = (box.innerText || box.textContent || '').trim();
		if (!t || t.charAt(0) !== '/') { return; }
		var repl = quickReplies[t];
		if (typeof repl === 'string' && repl) { setComposer(repl); }
	}, true);

	window.wagramAgent = {
		setQuickReplies: function (json) {
			try { quickReplies = JSON.parse(json) || {}; } catch (e) { quickReplies = {}; }
		},
		readMessages: function (limit) {
			try {
				var r = AGENT.read_messages({ limit: limit });
				if (r && typeof r.then === 'function') {
					return r.then(function (v) { return JSON.stringify(v); },
						function () { return '{"messages":[]}'; });
				}
				return Promise.resolve(JSON.stringify(r));
			} catch (e) { return Promise.resolve('{"messages":[]}'); }
		},
		setComposer: function (text) {
			try { setComposer(text); return true; } catch (e) { return false; }
		},
		call: function (id, fn, argsJson) {
			function reply(result) {
				try { window.wagramAgentReply(id, JSON.stringify(result)); } catch (e2) {}
			}
			try {
				var f = AGENT[fn];
				if (typeof f !== 'function') { throw new Error('unknown agent fn: ' + fn); }
				var args = argsJson ? JSON.parse(argsJson) : {};
				var r = f(args);
				if (r && typeof r.then === 'function') {
					r.then(function (v) { reply({ ok: true, value: v }); },
						function (e) { reply({ ok: false, error: String((e && e.message) || e) }); });
				} else {
					reply({ ok: true, value: r });
				}
			} catch (e) {
				reply({ ok: false, error: String((e && e.message) || e) });
			}
		}
	};

	// Pull the saved shortcuts once the page is alive. Doing it here rather
	// than from Go avoids racing the document load.
	try {
		if (typeof window.wagramQuickRepliesState === 'function') {
			window.wagramQuickRepliesState().then(function (list) {
				var m = {};
				for (var i = 0; i < (list || []).length; i++) { m[list[i].token] = list[i].text; }
				window.wagramAgent.setQuickReplies(JSON.stringify(m));
			});
		}
	} catch (e3) {}
})();
`
