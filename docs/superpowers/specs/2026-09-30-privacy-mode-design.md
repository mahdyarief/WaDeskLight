# Privacy Mode (blur) — Design Spec

Date: 2026-09-30
Branch: `feat/privacy-mode` (cut from `wagram-desk-lite`)
Status: approved for implementation planning

## Problem

WaGram Desk Lite wraps WhatsApp Web and Telegram Web in a WebView2 window. In a
shared or public space, message text, contact names, and media are visible to
anyone walking past. The existing third-party solution is a browser extension —
which does not exist inside a WebView2 wrapper and cannot be installed there.

The goal is a **built-in** privacy mode, shipped with the app, that blurs
sensitive content inside the page and reveals it on hover, with no extension
required. It must work for both services the app already supports.

## Scope

Blurs, while privacy mode is active:

1. Message text — bubbles in the conversation (`#main`).
2. Contact names and avatars — chat list, conversation header, profile pictures.
3. Chat list previews — last-message snippets and sender names in the left
   pane (`#pane-side`).
4. Media — images, video, stickers, thumbnails inside the conversation.

Reveal-on-hover, with the reveal granularity a user setting (per element vs
per conversation area). A master toggle turns privacy mode on or off; the
setting is persisted per account.

Out of scope: blurring the input box, blurring the account-switcher overlay
itself, keyboard shortcuts, and auto-blur on window unfocus. These were
considered and dropped (YAGNI).

## Background: how the app already works

- Go + WebView2 (`github.com/jchv/go-webview2`), module `wagramdesklite`.
- `Run()` in `internal/app/app_windows.go` injects scripts with `w.Init(...)`
  (which maps to `AddScriptToExecuteOnDocumentCreated`, so a script runs on
  every new document, including hard navigations) and then navigates:
  - `app_windows.go:219` `w.Init(initScript)` — User-Agent spoof + notification
    polyfill.
  - `app_windows.go:220` `w.Init(accountOverlayScript)` — the in-page account
    switcher, living in a shadow root on `documentElement`.
  - `app_windows.go:221` `w.Navigate(serviceURL(ensureAccount(profileID).Service))`.
- `internal/app/bindings_windows.go` `registerBindings(w, hwnd)` exposes host
  functions as `window.<name>()` returning a Promise. The existing settings
  bindings (`wagramNotificationsSet`, `wagramLiteSet`) each do
  load-prefs → mutate → save-prefs.
- `internal/app/prefs_windows.go` holds the per-account `prefs` struct, persisted
  to `prefs.json` in the config dir. Boolean settings use `*bool` where `nil`
  means the default, so files written before a setting existed stay valid.
- `internal/app/overlay_windows.go` renders the settings rows as `.viewrow`
  elements (glyph span + label span). The Notifications and Lite toggles at
  `overlay_windows.go:283-317` are the template the new rows follow.
- `internal/app/service_windows.go` defines `Service` (`ServiceWhatsApp`,
  `ServiceTelegram`), `serviceURL`, `serviceLabel`, `serviceBadge`. Each account
  is its own process with its own config dir, so `prefs.json` is per account.
- There are currently **no tests** in the repository.

## Chosen approach: CSS gate on `<html>`

A single injected script adds one `<style>` to the page `<head>` containing all
blur rules, every rule gated under our own class, e.g. `html.wgdl-privacy`.
Turning privacy on adds that class to `<html>`; turning it off removes it. The
reveal granularity is a second pair of classes on the same element — exactly one
of `wgdl-reveal-element` or `wgdl-reveal-area` is present whenever
`wgdl-privacy` is.

Why this is the most robust option:

- The browser re-evaluates CSS selectors against new DOM nodes automatically.
  React re-renders need no observer, no re-tagging, no polling.
- Selectors live in one readable block, as data. When WhatsApp or Telegram
  changes its DOM and a selector goes stale, the fix is one line.
- The initial state is baked into the injected script at `w.Init()` time, so
  there is no flash of unblurred content on startup when privacy is on.
- A selector that matches nothing blurs nothing and crashes nothing — the
  failure mode is degradation, not breakage.

Rejected alternatives: JS tagging with a `MutationObserver` (more code, CPU per
mutation, risk of missed nodes, and unnecessary because the scope is whole-element
blur, not text-node blur); and folding the logic into `accountOverlayScript`
(mixes two concerns — the shadow-root UI versus light-DOM page blur — against the
repo's recent "split mixed-concern files" direction).

## Architecture

### New file: `internal/app/privacy_windows.go`

Holds the whole privacy concern.

- `privacyScript` — a JS const, injected via `w.Init(fmt.Sprintf(privacyScript, bootstrap, cssBlock))`.
- `privacyBootstrap(p prefs) string` — returns a JSON literal
  `{"on":bool,"reveal":"element"|"area"}` baked into the script at startup.
- `privacyCSS(svc Service) string` — returns the service's CSS rule block. Called
  with the account's service so only that service's selectors are injected.

Script behavior (runs at document-created on every document):

1. Create `<style id="wgdl-privacy">` and append it to `document.head` (falling
   back to `document.documentElement` if `<head>` is absent), filling it with the
   baked CSS block.
2. Read the baked bootstrap JSON and set `wgdl-privacy` on
   `document.documentElement`, plus `wgdl-reveal-element` or `wgdl-reveal-area`
   according to `reveal`.
3. Expose `window.wgdlPrivacy = { set: function (on, reveal) { ... } }` so the
   overlay can apply a change immediately, before the Go binding persists it.

CSS shape (illustrative, WhatsApp):

```css
html.wgdl-privacy :is(<selectors>) {
  filter: blur(8px);
  transition: filter .15s ease;
}
html.wgdl-privacy.wgdl-reveal-element :is(<selectors>):hover { filter: none; }
html.wgdl-privacy.wgdl-reveal-area #main:hover :is(<message selectors>) { filter: none; }
```

Text blur is 8px; media blur may be slightly higher; both are constants in the
block, easy to tune.

### `internal/app/app_windows.go`

After `w.Init(accountOverlayScript)` (line 220), compute the account's service
once — it is already fetched for the navigate call on line 221 — and inject:

```go
w.Init(fmt.Sprintf(privacyScript, privacyBootstrap(loadPrefs()), privacyCSS(svc)))
```

### `internal/app/prefs_windows.go`

Add to the `prefs` struct:

- `Privacy *bool json:"privacy,omitempty"` — **nil means OFF** (the inverse of
  `Notifications`/`Lite`). Rationale: a first-run install that blurs every message
  would look broken and alarming to a new user.
- `PrivacyReveal string json:"privacyReveal,omitempty"` — `""` or `"element"`
  means per-element reveal (the default); `"area"` means per-conversation-area.
  Any unknown value normalizes to `"element"`.

New helpers, following the existing naming:

- `privacyEnabled(p prefs) bool` — `p.Privacy != nil && *p.Privacy`.
- `setPrivacyEnabled(p *prefs, on bool)`.
- `privacyReveal(p prefs) string` — returns `"area"` or `"element"`, normalizing.
- `setPrivacyReveal(p *prefs, mode string)`.

`loadPrefs` normalizes `PrivacyReveal` the same way it already normalizes
`ViewMode`.

### `internal/app/bindings_windows.go`

Two bindings in `registerBindings`, each load-prefs → mutate → save-prefs, like
`wagramLiteSet`:

- `wagramPrivacySet(on bool)`.
- `wagramPrivacyRevealSet(mode string)`.

`accountsView.Prefs` already carries the whole `prefs` struct, so the overlay
receives the new fields with no change to the view shape.

### `internal/app/overlay_windows.go`

Two `.viewrow` entries appended after the Lite row (line 317), following the
existing glyph + label + click-handler pattern. Both rows always render, each
reflecting the current persisted value — the Reveal row is not hidden when
privacy is off, matching how the View/Notifications/Lite rows behave.

- Privacy: `🙈`/`👁`, label `Privacy: On (blur, reveal on hover)` /
  `Privacy: Off (turn on)`.
- Reveal: `▭`/`▤` (or equivalent), label `Reveal: Per element (switch to Per area)` /
  `Reveal: Per area (switch to Per element)`.

Two helpers mirror `notifOf`/`liteOf` (lines 78-92), reading both camelCase and
PascalCase for compatibility:

- `privOf()` — reads `state.prefs.privacy` / `Privacy`, default false.
- `revealOf()` — reads `state.prefs.privacyReveal` / `PrivacyReveal`, default
  `"element"`.

Click handlers: toggle the local value, call `window.wgdlPrivacy.set(...)` for the
instant effect, call the Go binding to persist, update `state.prefs`
optimistically, re-render.

## Selectors per service

### WhatsApp (verified against current DOM references)

Gated under `html.wgdl-privacy`:

- Message bubbles: `#main .message-in`, `#main .message-out`.
- Conversation header (name + avatar): `#main > header`.
- Chat list rows (name + preview + avatar): `#pane-side div[role="listitem"]`.
- Media: `#main img`, `#main video`.
- Chat list avatars: `#pane-side img`.

Stable anchors: `#main` and `#pane-side` are stable element IDs; `.message-in` /
`.message-out` and `div[role="listitem"]` are long-lived. WhatsApp's hashed class
names are not relied upon.

Per-area reveal uses `#main:hover` as the trigger for the message selectors.

### Telegram `/a/` (best-effort, requires a live-DOM verification step)

Telegram has two incompatible web clients, `/k/` and `/a/`; the app navigates to
`https://web.telegram.org/a/`. The `/a/` client is a React app whose class names
are generated and offers no `role="listitem"` equivalent, so it cannot use the
same anchors as WhatsApp.

Strategy: target the message-history scroller and its bubble wrappers
structurally, and target the chat list container structurally, rather than by
generated class name. Because the exact generated names must be read from the
running page, the implementation plan includes an explicit step: open Telegram
Web in the app, inspect the live DOM, and fill in the actual selectors for the
four blur categories. This is a required implementation step, not a placeholder —
the shipped code must contain concrete selectors, and the no-match-means-no-blur
rule keeps a stale selector harmless.

If no Telegram selector can be found for a category, that category is simply not
blurred on Telegram; WhatsApp is unaffected.

## Data flow

1. App start: `loadPrefs()` → `privacyBootstrap` + `privacyCSS(svc)` → injected
   script → `<style>` and gate class applied before the page paints.
2. User toggles Privacy in the overlay → `window.wgdlPrivacy.set(true, reveal)`
   applies the class immediately → `wagramPrivacySet(true)` persists to
   `prefs.json`.
3. User toggles Reveal → `window.wgdlPrivacy.set(on, "area")` swaps the class →
   `wagramPrivacyRevealSet("area")` persists.
4. Restart or hard navigation: the script re-runs on the new document and
   re-applies `<style>` + gate class from the baked bootstrap.

## Error handling

- Missing `<head>`: style is appended to `documentElement`.
- Selector matches nothing: no blur, no error.
- Bootstrap JSON malformed: script falls back to privacy off.
- Binding call fails (page not ready): the overlay still applies the visual
  change optimistically; persistence retries on the next toggle. The page-side
  state and the file may briefly disagree; the file wins on next load.

## Testing

The repository has no test infrastructure. This change adds one focused test
file, `internal/app/prefs_windows_test.go`, covering the pure functions (no
filesystem, no GUI):

- `privacyEnabled` on an empty prefs returns false (default off).
- `setPrivacyEnabled` then `privacyEnabled` round-trips for both values.
- `privacyReveal` normalizes `""`, `"element"`, `"area"`, and garbage to the
  right value.

Webview behavior is verified by a manual checklist, since it cannot be
automated here:

1. Fresh install: privacy off, nothing blurred.
2. Toggle Privacy on: message text, names, avatars, previews, and media blur.
3. Hover a message: only that element reveals (per-element mode).
4. Switch reveal to per-area; hover the conversation: all messages reveal.
5. Restart the app with privacy on: content is blurred again from first paint,
   no flash.
6. Repeat 2-4 on a Telegram account with the verified selectors.

## Files touched

- `internal/app/privacy_windows.go` — new.
- `internal/app/prefs_windows.go` — two fields, four helpers, load normalization.
- `internal/app/bindings_windows.go` — two bindings.
- `internal/app/overlay_windows.go` — two rows, two helpers, two handlers.
- `internal/app/app_windows.go` — one `w.Init` call.
- `internal/app/prefs_windows_test.go` — new.
