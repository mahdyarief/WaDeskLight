//go:build windows

package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"wagramdesklite/internal/audio"
)

const (
	windowTitle = "WaGram Desk Lite"
	appURL      = "https://web.whatsapp.com"
	mutexName   = "WaGramDeskLiteSingleInstanceMutex"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
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

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

var (
	gWinProc uintptr
	gOldProc uintptr
	gTray    notifyIconData

	// Identity of the account this process serves.
	gProfileID   = defaultProfileID
	gWindowTitle = windowTitle
	// Set when the user asks to remove this account; acted on after the
	// WebView2 instance is torn down and its files are unlocked.
	gPendingRemove bool

	gTrayMu sync.Mutex
	// Large app icon used as the balloon icon when a message carries no avatar.
	gAppBalloonIcon uintptr
	// Avatar icon of the most recent notification, owned by us.
	gBalloonIcon uintptr
)

func setDarkWindowFrame(hwnd uintptr) {
	darkMode := int32(1)
	// Try standard DWMWA_USE_IMMERSIVE_DARK_MODE (Win10 20H1+ & Win11)
	procDwmSetAttr.Call(
		hwnd,
		uintptr(DWMWA_USE_IMMERSIVE_DARK_MODE),
		uintptr(unsafe.Pointer(&darkMode)),
		unsafe.Sizeof(darkMode),
	)
	// Try older Win10 build
	procDwmSetAttr.Call(
		hwnd,
		uintptr(DWMWA_USE_IMMERSIVE_DARK_MODE_BEFORE_20H1),
		uintptr(unsafe.Pointer(&darkMode)),
		unsafe.Sizeof(darkMode),
	)

	// Set dark caption color (COLORREF: 0x00111B21 WhatsApp Dark Header: RGB 17, 27, 33)
	captionColor := uint32(0x00211B11) // 0x00BBGGRR
	procDwmSetAttr.Call(
		hwnd,
		uintptr(DWMWA_CAPTION_COLOR),
		uintptr(unsafe.Pointer(&captionColor)),
		unsafe.Sizeof(captionColor),
	)

	// Set white caption text (RGB 255, 255, 255)
	textColor := uint32(0x00FFFFFF)
	procDwmSetAttr.Call(
		hwnd,
		uintptr(DWMWA_TEXT_COLOR),
		uintptr(unsafe.Pointer(&textColor)),
		unsafe.Sizeof(textColor),
	)
}

func checkSingleInstance() (uintptr, bool) {
	namePtr, _ := syscall.UTF16PtrFromString(mutexName + "_" + gProfileID)
	handle, _, err := procCreateMutex.Call(0, 1, uintptr(unsafe.Pointer(namePtr)))
	if err == windows.ERROR_ALREADY_EXISTS {
		titlePtr, _ := syscall.UTF16PtrFromString(gWindowTitle)
		hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
		if hwnd != 0 {
			procShowWindow.Call(hwnd, swRestore)
			procSetFgWindow.Call(hwnd)
		}
		return handle, false
	}
	return handle, true
}

func getConfigDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("APPDATA")
		if configDir == "" {
			configDir = "."
		}
	}
	dir := filepath.Join(configDir, "WaGramDeskLite")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

func getUserDataDir() string {
	return userDataDirFor(gProfileID)
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

func webView2RuntimeInstalled() bool {
	clsid := `Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	roots := []string{
		`SOFTWARE\WOW6432Node\` + clsid,
		`SOFTWARE\` + clsid,
	}
	for _, root := range roots {
		pathPtr, _ := windows.UTF16PtrFromString(root)
		var key windows.Handle
		if err := windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, pathPtr, 0, windows.KEY_READ, &key); err == nil {
			windows.RegCloseKey(key)
			return true
		}
	}
	return false
}

func messageBox(parent uintptr, message, title string, flags uintptr) int {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	msgPtr, _ := windows.UTF16PtrFromString(message)
	r, _, _ := procMessageBoxW.Call(parent, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), flags)
	return int(r)
}

func showErrorDialog(message string) {
	messageBox(0, message, windowTitle, mbIconError)
}

func strPtr(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func copyUTF16(dst []uint16, s string) {
	src, _ := windows.UTF16FromString(s)
	copy(dst, src)
}

func setTip(n *notifyIconData, s string)       { copyUTF16(n.szTip[:], s) }
func setInfoTitle(n *notifyIconData, s string) { copyUTF16(n.szInfoTitle[:], s) }
func setInfo(n *notifyIconData, s string)      { copyUTF16(n.szInfo[:], s) }

func loadTrayIcon(iconPath string, size uintptr) uintptr {
	pathPtr, _ := windows.UTF16PtrFromString(iconPath)
	hIcon, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), imageIcon, size, size, lrLoadFromFile)
	if hIcon != 0 {
		return hIcon
	}
	hIcon, _, _ = procLoadIconW.Call(0, idiApplication)
	return hIcon
}

func trayAdd(hwnd uintptr, iconPath string) {
	if gTray.hWnd != 0 {
		return
	}
	var nid notifyIconData
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTrayCallback
	nid.hIcon = loadTrayIcon(iconPath, 16)
	setTip(&nid, gWindowTitle)
	gAppBalloonIcon = loadTrayIcon(iconPath, 32)
	gTray = nid
	procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
}

// trayBalloon shows a notification. iconData, when it decodes to an image, is
// used as the balloon icon so the sender's avatar appears; otherwise the app
// icon is used. Passing nil is fine for app-generated messages.
func trayBalloon(title, message string, iconData []byte) {
	gTrayMu.Lock()
	defer gTrayMu.Unlock()
	if gTray.hWnd == 0 {
		return
	}

	// NIF_INFO alone would drop the icon, tip, and callback flags.
	gTray.uFlags = nifMessage | nifIcon | nifTip | nifInfo
	setInfoTitle(&gTray, title)
	setInfo(&gTray, message)

	avatar := createIconFromImage(decodeIconImage(iconData))
	icon := avatar
	if icon == 0 {
		icon = gAppBalloonIcon
	}
	if icon != 0 {
		gTray.hBalloonIcon = icon
		gTray.dwInfoFlags = niifUser | niifLargeIcon
	} else {
		gTray.dwInfoFlags = niifInfo
	}
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&gTray)))

	// Release the previous avatar, not the one just handed to the shell.
	if gBalloonIcon != 0 {
		procDestroyIcon.Call(gBalloonIcon)
	}
	gBalloonIcon = avatar
}

// decodeDataURL unwraps a base64 "data:image/...;base64,..." URL.
func decodeDataURL(s string) []byte {
	const marker = ";base64,"
	if !strings.HasPrefix(s, "data:image/") {
		return nil
	}
	i := strings.Index(s, marker)
	if i < 0 {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(s[i+len(marker):])
	if err != nil {
		return nil
	}
	return data
}

func trayDelete() {
	if gTray.hWnd == 0 {
		return
	}
	procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&gTray)))
	gTray.hWnd = 0
	if gBalloonIcon != 0 {
		procDestroyIcon.Call(gBalloonIcon)
		gBalloonIcon = 0
	}
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

func windowTitleFor(a account) string {
	return windowTitle + " — " + a.Name + " [" + string(serviceBadge(a.Service.normalize())) + "]"
}

// validProfileID guards the profile name before it reaches the filesystem.
func validProfileID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// profileFromArgs reports which account this process was launched for, and
// whether it was named explicitly. Only the implicit launch restores the rest
// of the previous session.
func profileFromArgs() (string, bool) {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		var id string
		switch {
		case strings.HasPrefix(args[i], "--profile="):
			id = strings.TrimPrefix(args[i], "--profile=")
		case args[i] == "--profile" && i+1 < len(args):
			id = args[i+1]
		default:
			continue
		}
		if validProfileID(id) {
			return id, true
		}
		return defaultProfileID, false
	}
	return defaultProfileID, false
}

func spawnAccount(id string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "--profile", id)
	if cmd.Start() == nil && cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}

// focusAccount brings another account's window forward, starting it if it is
// not running yet.
func focusAccount(from uintptr, a account) {
	// Carry this window's placement over so switching reads as one window
	// changing account, rather than a smaller new window appearing.
	st, _ := captureWindowState(from)
	titlePtr, _ := windows.UTF16PtrFromString(windowTitleFor(a))
	target, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if target != 0 {
		applyWindowState(target, st)
		procSetFgWindow.Call(target)
		return
	}
	// Not running yet: leave the geometry where the new process will read it,
	// so its very first paint is already the right size.
	if st.Saved {
		saveWindowStateTo(windowStatePathFor(a.ID), st)
	}
	spawnAccount(a.ID)
}

func applyAccountName(hwnd uintptr, a account) {
	gWindowTitle = windowTitleFor(a)
	titlePtr, _ := windows.UTF16PtrFromString(gWindowTitle)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(titlePtr)))

	gTrayMu.Lock()
	defer gTrayMu.Unlock()
	if gTray.hWnd == 0 {
		return
	}
	gTray.uFlags = nifMessage | nifIcon | nifTip
	setTip(&gTray, gWindowTitle)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&gTray)))
}

func quitInstance(hwnd uintptr) {
	saveWindowBounds(hwnd)
	trayDelete()
	procPostQuitMessage.Call(0)
}

func showTrayMenu(hwnd uintptr) {
	procSetFgWindow.Call(hwnd)
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	procAppendMenuW.Call(menu, 0, menuOpen, strPtr("Open WaGram Desk Lite"))
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)

	accounts := loadAccounts()
	for i, a := range accounts {
		flags := uintptr(0)
		if a.ID == gProfileID {
			flags = mfChecked
		}
		procAppendMenuW.Call(menu, flags, uintptr(menuAccountBase+i), strPtr(a.Name))
	}

	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	procAppendMenuW.Call(menu, 0, menuAdd, strPtr("Add Account"))
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	procAppendMenuW.Call(menu, 0, menuExit, strPtr("Exit"))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightBtn|tpmBottom, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	procDestroyMenu.Call(menu)

	switch {
	case cmd == menuOpen:
		restoreWindow(hwnd)
	case cmd == menuExit:
		// A deliberate exit opts this account out of the next restore.
		setAutostart(gProfileID, false)
		quitInstance(hwnd)
	case cmd == menuAdd:
		focusAccount(hwnd, addAccount())
	case cmd >= menuAccountBase:
		if i := int(cmd) - menuAccountBase; i < len(accounts) {
			if accounts[i].ID == gProfileID {
				restoreWindow(hwnd)
			} else {
				focusAccount(hwnd, accounts[i])
			}
		}
	}
}

func windowProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmClose:
		// Close-to-tray: hide the window and keep running in the background.
		saveWindowBounds(hwnd)
		procShowWindow.Call(hwnd, swHide)
		setMemoryUsageTargetLevel(memoryUsageLow)
		setEcoQoS(true)
		go trayBalloon("WaGram Desk Lite", "Masih berjalan di system tray. Klik ikon untuk membuka kembali.", nil)
		return 0
	case wmShowWindow:
		if wp != 0 {
			setMemoryUsageTargetLevel(memoryUsageNormal)
			setEcoQoS(false)
		}
		r, _, _ := procCallWindowProcW.Call(gOldProc, hwnd, msg, wp, lp)
		return r
	case wmTrayCallback:
		switch uint32(lp) & 0xFFFF {
		case wmLButtonUp, wmLButtonDblClk, ninBalloonUserClick:
			restoreWindow(hwnd)
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	default:
		r, _, _ := procCallWindowProcW.Call(gOldProc, hwnd, msg, wp, lp)
		return r
	}
}

func installWindowSubclass(hwnd uintptr) {
	gWinProc = windows.NewCallback(windowProc)
	gOldProc, _, _ = procSetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProc), gWinProc)
}

// Run starts the WaGramDeskLite window and blocks until the app exits.
// It returns the process exit code.
func Run() int {
	profileID, explicit := profileFromArgs()
	gProfileID = profileID
	gWindowTitle = windowTitleFor(ensureAccount(profileID))

	_, isSingle := checkSingleInstance()
	if !isSingle {
		return 0
	}
	setAutostart(gProfileID, true)

	if !webView2RuntimeInstalled() {
		showErrorDialog("WebView2 Runtime tidak ditemukan.\n\nSilakan install Microsoft Edge WebView2 Runtime dari:\nhttps://developer.microsoft.com/microsoft-edge/webview2/")
		return 1
	}

	userDataDir := getUserDataDir()
	executablePath, _ := os.Executable()
	iconFullPath := filepath.Join(filepath.Dir(executablePath), "icon.ico")

	opts := webview2.WebViewOptions{
		Window:    nil,
		Debug:     false,
		DataPath:  userDataDir,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  windowTitle,
			Width:  1100,
			Height: 750,
			IconId: 2,
			Center: true,
		},
	}

	// Trim memory footprint: limit renderer count and disable unused Chromium
	// components (SmartScreen, in-app PDF viewer, background networking).
	// Read by WebView2 loader when the environment is created.
	//
	// max-old-space-size caps V8's old space. It does not free memory by
	// itself; it makes GC run sooner. Too low a value crashes the renderer on
	// large chat histories, so 512 MB is deliberately conservative.
	//
	// Do not drop --disable-gpu because it looks like a naive tweak: measured
	// on integrated graphics, hardware compositing put ~590 MB of texture
	// memory in the GPU process and pushed the total from 773 MB to 1440 MB.
	// low-end-device-mode is worth about 70 MB across three runs per config.
	_ = os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--js-flags=--max-old-space-size=512 --renderer-process-limit=1 --process-per-site --disable-site-isolation-trials --disable-gpu --disable-gpu-compositing --enable-low-end-device-mode --disable-features=SitePerProcess,IsolateOrigins,OutOfProcessNetworkService,msWebOOUI,msPdfOOUI,msSmartScreenProtection --disable-background-networking --disable-component-update --no-first-run --disable-sync")

	w := webview2.NewWithOptions(opts)
	if w == nil {
		showErrorDialog("Gagal menginisialisasi WebView2. Pastikan Microsoft Edge WebView2 Runtime terinstall.")
		return 1
	}
	// Registered before w.Destroy so it runs after it, once WebView2 has let go
	// of the profile's files.
	defer func() {
		if gPendingRemove {
			removeAccount(gProfileID)
		}
	}()
	defer w.Destroy()
	initMemoryControl(w)
	enableContextMenu(w)
	audio.StartLabeler()

	// Only an implicit launch reopens the accounts from the previous session;
	// an explicit --profile starts exactly the one account it names.
	if !explicit {
		for _, a := range loadAccounts() {
			if a.ID != gProfileID && a.Autostart {
				spawnAccount(a.ID)
			}
		}
	}

	hwnd := uintptr(w.Window())
	setDarkWindowFrame(hwnd)

	// Restore window size/position from the previous session, if any.
	applyWindowState(hwnd, loadWindowState())

	installWindowSubclass(hwnd)
	trayAdd(hwnd, iconFullPath)
	defer trayDelete()

	w.SetTitle(gWindowTitle)
	_ = w.Bind("sendNativeNotification", func(title, body, iconDataURL string) {
		icon := decodeDataURL(iconDataURL)
		go trayBalloon(title, body, icon)
	})

	type accountsView struct {
		Current  string    `json:"current"`
		Accounts []account `json:"accounts"`
		Prefs    prefs     `json:"prefs"`
	}
	_ = w.Bind("wadeskAccountsState", func() accountsView {
		return accountsView{Current: gProfileID, Accounts: loadAccounts(), Prefs: loadPrefs()}
	})
	_ = w.Bind("wadeskAccountSwitch", func(id string) {
		for _, a := range loadAccounts() {
			if a.ID == id && a.ID != gProfileID {
				focusAccount(hwnd, a)
				return
			}
		}
	})
	_ = w.Bind("wadeskAccountAdd", func() {
		focusAccount(hwnd, addAccount())
	})
	_ = w.Bind("wadeskAccountAddService", func(svc string) {
		focusAccount(hwnd, addServiceAccount(Service(svc)))
	})
	_ = w.Bind("wadeskPrefsGet", func() prefs {
		return loadPrefs()
	})
	_ = w.Bind("wadeskPrefsSet", func(mode string) {
		p := loadPrefs()
		if mode == string(ViewPages) {
			p.ViewMode = ViewPages
		} else {
			p.ViewMode = ViewTabs
		}
		savePrefs(p)
	})
	_ = w.Bind("wadeskAccountRename", func(id, name string) {
		renameAccount(id, name)
		if id == gProfileID {
			w.Dispatch(func() { applyAccountName(hwnd, accountFor(id)) })
		}
	})
	// Only the account you are looking at can be removed: another instance owns
	// its own window and files, and has no channel to be told to shut down.
	_ = w.Bind("wadeskAccountRemove", func(id string) {
		if id != gProfileID || id == defaultProfileID {
			return
		}
		w.Dispatch(func() {
			gPendingRemove = true
			quitInstance(hwnd)
		})
	})

	uaJSON, _ := json.Marshal(userAgent)

	// Inject JS: User-Agent spoofing + Notification API polyfill connecting to the native tray balloon.
	initScript := fmt.Sprintf(`
		// UserAgent override
		Object.defineProperty(navigator, 'userAgent', {
			get: () => %[1]s
		});
		Object.defineProperty(navigator, 'appVersion', {
			get: () => %[1]s
		});

		// Native Notification Polyfill for Windows Tray Balloon
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

			window.Notification = function(title, options) {
				options = options || {};
				var body = options.body || '';
				if (hasBridge) {
					// Never let a slow avatar fetch hold up the notification.
					var timeout = new Promise(function(resolve) {
						setTimeout(function() { resolve(''); }, 1500);
					});
					Promise.race([avatarDataURL(options.icon), timeout])
						.then(function(icon) {
							window.sendNativeNotification(String(title), String(body), icon || '');
						});
				}
				this.title = title;
				this.onclick = null;
				this.onclose = null;
				this.onerror = null;
				this.onshow = null;
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
		})();
	`, string(uaJSON))

	w.Init(initScript)
	w.Init(accountOverlayScript)
	w.Navigate(serviceURL(ensureAccount(profileID).Service))
	w.Run()

	return 0
}
