//go:build windows

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// defaultProfileID keeps using the original, un-nested config paths so sessions
// paired before multi-account support survive the upgrade.
const defaultProfileID = "default"

const accountsMutexName = "WaGramDeskLiteAccountsMutex"

type account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Autostart records whether this account was open when the app last ran.
	Autostart bool `json:"autostart"`
	// Service is whatsapp (default) or telegram. Empty means whatsapp
	// for accounts written before multi-service support.
	Service Service `json:"service,omitempty"`
}

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

func accountName(id string) string {
	return accountFor(id).Name
}

func accountFor(id string) account {
	for _, a := range loadAccounts() {
		if a.ID == id {
			a.Service = a.Service.normalize()
			return a
		}
	}
	return account{ID: id, Name: "Account", Service: ServiceWhatsApp}
}

func setAutostart(id string, on bool) {
	mutateAccounts(func(accounts []account) []account {
		for i := range accounts {
			if accounts[i].ID == id {
				accounts[i].Autostart = on
			}
		}
		return accounts
	})
}

// nextProfileID picks the lowest unused "pN" so profile folders stay readable.
func nextProfileID(accounts []account) string {
	used := map[string]bool{}
	for _, a := range accounts {
		used[a.ID] = true
	}
	for n := 2; ; n++ {
		id := "p" + strconv.Itoa(n)
		if !used[id] {
			return id
		}
	}
}

// nextAccountName picks the lowest unused "Account N".
func nextAccountName(accounts []account) string {
	used := map[int]bool{}
	for _, a := range accounts {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(a.Name), "Account %d", &n); err == nil {
			used[n] = true
		}
	}
	nums := make([]int, 0, len(used))
	for n := range used {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for n := 1; ; n++ {
		if !used[n] {
			return "Account " + strconv.Itoa(n)
		}
	}
}

// ensureAccount returns the named account, registering it first if the list has
// never seen it. This keeps an explicit --profile launch self-describing and
// lets a deleted accounts.json rebuild itself.
func ensureAccount(id string) account {
	var found account
	mutateAccounts(func(accounts []account) []account {
		for _, a := range accounts {
			if a.ID == id {
				found = a
				return accounts
			}
		}
		found = account{ID: id, Name: nextAccountName(accounts), Autostart: true, Service: ServiceWhatsApp}
		return append(accounts, found)
	})
	return found
}

// addAccount registers a new profile and returns it.
func addAccount() account {
	return addServiceAccount(ServiceWhatsApp)
}

// addServiceAccount registers a new profile for the given service.
func addServiceAccount(svc Service) account {
	var created account
	mutateAccounts(func(accounts []account) []account {
		created = account{
			ID:        nextProfileID(accounts),
			Name:      nextAccountName(accounts),
			Autostart: true,
			Service:   svc.normalize(),
		}
		return append(accounts, created)
	})
	return created
}

func renameAccount(id, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	mutateAccounts(func(accounts []account) []account {
		for i := range accounts {
			if accounts[i].ID == id {
				accounts[i].Name = name
			}
		}
		return accounts
	})
}

// removeAccount drops the entry and deletes its stored session. The default
// account is the app itself and cannot be removed.
func removeAccount(id string) bool {
	if id == defaultProfileID {
		return false
	}
	dir := profileDir(id)
	mutateAccounts(func(accounts []account) []account {
		out := accounts[:0]
		for _, a := range accounts {
			if a.ID != id {
				out = append(out, a)
			}
		}
		return out
	})
	// WebView2 may still be releasing handles under the profile directory.
	for attempt := 0; attempt < 3; attempt++ {
		if os.RemoveAll(dir) == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	return true
}
