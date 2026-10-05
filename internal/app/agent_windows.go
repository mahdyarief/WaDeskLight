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
	// "23 unread messages Marketplace 23"; strip the count off both ends.
	function stripUnread(s) {
		s = (s || '').trim();
		var m = s.match(/^\d+\s+unread messages?\s+(.*?)\s+\d+$/i);
		if (m) { return m[1].trim(); }
		return s.replace(/^\d+\s+unread messages?\s+/i, '').trim();
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
		return true;
	}

	function clickSend() {
		var btn = first([
			'[data-testid="send"]',
			'button[aria-label="Send"]',
			'button[aria-label="Kirim"]',
			'span[data-icon="send"]',
			'button.send'
		]);
		if (btn) { btn.click(); return true; }
		var box = composer();
		if (box) {
			box.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true }));
			return true;
		}
		return false;
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
		return { opened: false, name: (args && args.name) || '' };
	};

	AGENT.read_messages = function (args) {
		var limit = (args && args.limit) ? args.limit : 50;
		var rows = all([
			'[data-testid="msg-container"]',
			'div.message-in, div.message-out',
			'.MessageList .message',
			'.bubbles .message'
		]);
		var out = [];
		var start = Math.max(0, rows.length - limit);
		for (var i = start; i < rows.length; i++) {
			var r = rows[i];
			var body = r.querySelector('.selectable-text') || r.querySelector('[data-testid="msg-text"]') || r.querySelector('.text-content');
			var meta = r.querySelector('[data-testid="msg-meta"]') || r.querySelector('.meta') || r.querySelector('.time');
			var outgoing = !!r.querySelector('.message-out') || ('' + r.className).indexOf('message-out') !== -1;
			out.push({ text: txt(body), time: txt(meta), outgoing: outgoing });
		}
		return { messages: out };
	};

	AGENT.send_message = function (args) {
		if (!args || !args.text) { throw new Error('text required'); }
		setComposer(args.text);
		return { sent: clickSend() };
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
			try { return JSON.stringify(AGENT.read_messages({ limit: limit })); }
			catch (e) { return '{"messages":[]}'; }
		},
		setComposer: function (text) {
			try { setComposer(text); return true; } catch (e) { return false; }
		},
		call: function (id, fn, argsJson) {
			var result;
			try {
				var f = AGENT[fn];
				if (typeof f !== 'function') { throw new Error('unknown agent fn: ' + fn); }
				var args = argsJson ? JSON.parse(argsJson) : {};
				result = { ok: true, value: f(args) };
			} catch (e) {
				result = { ok: false, error: String((e && e.message) || e) };
			}
			try { window.wagramAgentReply(id, JSON.stringify(result)); } catch (e2) {}
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
