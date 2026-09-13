//go:build windows

package app

import "golang.org/x/sys/windows"

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procCreateMutex      = kernel32.NewProc("CreateMutexW")
	procFindWindow       = user32.NewProc("FindWindowW")
	procSetFgWindow      = user32.NewProc("SetForegroundWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procDwmSetAttr       = dwmapi.NewProc("DwmSetWindowAttribute")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW  = user32.NewProc("CallWindowProcW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procShellNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")

	procWaitForSingleObject   = kernel32.NewProc("WaitForSingleObject")
	procReleaseMutex          = kernel32.NewProc("ReleaseMutex")
	procSetProcessInformation = kernel32.NewProc("SetProcessInformation")

	procSetWindowTextW     = user32.NewProc("SetWindowTextW")
	procGetWindowPlacement = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement = user32.NewProc("SetWindowPlacement")

	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
	procDestroyIcon        = user32.NewProc("DestroyIcon")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
)

// Win32 constants used by the window, tray, and dark-frame code.
const (
	// DWM window attributes for dark theme
	DWMWA_USE_IMMERSIVE_DARK_MODE_BEFORE_20H1 = 19
	DWMWA_USE_IMMERSIVE_DARK_MODE             = 20
	DWMWA_CAPTION_COLOR                       = 35
	DWMWA_TEXT_COLOR                          = 36

	// Win32 messages
	wmClose             = 0x0010
	wmLButtonUp         = 0x0202
	wmRButtonUp         = 0x0205
	wmLButtonDblClk     = 0x0203
	wmShowWindow        = 0x0018
	wmApp               = 0x8000
	wmTrayCallback      = wmApp + 1
	ninBalloonUserClick = wmApp + 5

	// ShowWindow commands
	swHide          = 0
	swShowNormal    = 1
	swShowMaximized = 3
	swRestore       = 9

	// SetWindowLongPtr
	gwlpWndProc = ^uintptr(3) // -4

	// Tray icon
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifMessage    = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004
	nifInfo       = 0x00000010
	niifInfo      = 0x00000001
	niifUser      = 0x00000004
	niifLargeIcon = 0x00000020

	// Icons
	imageIcon      = 1
	lrLoadFromFile = 0x0010
	idiApplication = 32512
	dibRGBColors   = 0

	// Tray popup menu
	tpmReturnCmd = 0x0100
	tpmRightBtn  = 0x0002
	tpmBottom    = 0x0020
	mfSeparator  = 0x0800
	mfChecked    = 0x0008

	menuOpen = 1
	menuExit = 2
	menuAdd  = 3
	// Account entries occupy menuAccountBase + index.
	menuAccountBase = 100

	// MessageBox
	mbIconError = 0x00000010
)
