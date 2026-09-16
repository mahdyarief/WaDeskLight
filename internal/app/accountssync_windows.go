//go:build windows

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// defaultProfileID keeps using the original, un-nested config paths so sessions
// paired before multi-account support survive the upgrade.
const defaultProfileID = "default"

const accountsMutexName = "WaGramDeskLiteAccountsMutex"

type accountsFile struct {
	Accounts []account `json:"accounts"`
}

// profileDir returns the config root for one account.
func profileDir(id string) string {
	if id == defaultProfileID {
		return getConfigDir()
	}
	dir := filepath.Join(getConfigDir(), "profiles", id)
	_ = os.MkdirAll(dir, 0755)
	return dir
}

func userDataDirFor(id string) string {
	dir := filepath.Join(profileDir(id), "UserData")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

func windowStatePathFor(id string) string {
	return filepath.Join(profileDir(id), "window.json")
}

func accountsPath() string {
	return filepath.Join(getConfigDir(), "accounts.json")
}

// withAccountsLock serialises read-modify-write across every running instance,
// since each account is its own process writing the same file.
func withAccountsLock(fn func()) {
	namePtr, err := windows.UTF16PtrFromString(accountsMutexName)
	if err != nil {
		fn()
		return
	}
	h, _, _ := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		fn()
		return
	}
	defer windows.CloseHandle(windows.Handle(h))
	procWaitForSingleObject.Call(h, 5000)
	defer procReleaseMutex.Call(h)
	fn()
}

func readAccounts() []account {
	var f accountsFile
	if data, err := os.ReadFile(accountsPath()); err == nil {
		// Notepad and PowerShell both save a BOM, which encoding/json rejects.
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		if json.Unmarshal(data, &f) != nil && len(bytes.TrimSpace(data)) > 0 {
			// Keep the unreadable file rather than overwriting it with defaults:
			// the profile folders it names still hold real sessions.
			_ = os.Rename(accountsPath(), accountsPath()+".bad")
		}
	}
	// The default account always exists and is always first.
	if len(f.Accounts) == 0 || f.Accounts[0].ID != defaultProfileID {
		rest := make([]account, 0, len(f.Accounts))
		var def *account
		for i := range f.Accounts {
			if f.Accounts[i].ID == defaultProfileID {
				def = &f.Accounts[i]
				continue
			}
			rest = append(rest, f.Accounts[i])
		}
		if def == nil {
			def = &account{ID: defaultProfileID, Name: "Account 1", Autostart: true, Service: ServiceWhatsApp}
		}
		f.Accounts = append([]account{*def}, rest...)
	}
	for i := range f.Accounts {
		f.Accounts[i].Service = f.Accounts[i].Service.normalize()
	}
	return f.Accounts
}

func writeAccounts(accounts []account) {
	data, err := json.MarshalIndent(accountsFile{Accounts: accounts}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(accountsPath(), data, 0644)
}

// mutateAccounts applies fn under the cross-process lock and returns the result.
func mutateAccounts(fn func([]account) []account) []account {
	var out []account
	withAccountsLock(func() {
		out = fn(readAccounts())
		writeAccounts(out)
	})
	return out
}

func loadAccounts() []account {
	var out []account
	withAccountsLock(func() { out = readAccounts() })
	return out
}
