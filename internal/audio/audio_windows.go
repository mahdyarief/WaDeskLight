//go:build windows

package audio

import (
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	wca "github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

const (
	th32csSnapProcess = 0x00000002
)

// StartLabeler periodically renames the audio sessions of the WebView2
// renderer processes owned by the current process to "WhatsApp", so the
// WhatsApp audio appears under that name in the Windows volume mixer.
func StartLabeler() {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
			return
		}
		defer ole.CoUninitialize()

		for {
			labelAudioSessions()
			time.Sleep(2 * time.Second)
		}
	}()
}

func labelAudioSessions() {
	processIDs := waGramDeskLiteWebViewProcesses(uint32(syscall.Getpid()))
	if len(processIDs) == 0 {
		return
	}

	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator,
		0,
		wca.CLSCTX_INPROC_SERVER,
		wca.IID_IMMDeviceEnumerator,
		&enumerator,
	); err != nil {
		return
	}
	defer enumerator.Release()

	var device *wca.IMMDevice
	if err := enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.DEVICE_STATE_ACTIVE, &device); err != nil {
		return
	}
	defer device.Release()

	var manager *wca.IAudioSessionManager2
	if err := device.Activate(wca.IID_IAudioSessionManager2, wca.CLSCTX_INPROC_SERVER, nil, &manager); err != nil {
		return
	}
	defer manager.Release()

	var sessions *wca.IAudioSessionEnumerator
	if err := manager.GetSessionEnumerator(&sessions); err != nil {
		return
	}
	defer sessions.Release()

	var count int
	if err := sessions.GetCount(&count); err != nil {
		return
	}

	for i := 0; i < count; i++ {
		var control *wca.IAudioSessionControl
		if err := sessions.GetSession(i, &control); err != nil || control == nil {
			continue
		}

		var control2 *wca.IAudioSessionControl2
		if err := control.PutQueryInterface(wca.IID_IAudioSessionControl2, &control2); err != nil || control2 == nil {
			control.Release()
			continue
		}

		var processID uint32
		if err := control2.GetProcessId(&processID); err == nil && processIDs[processID] {
			name := "WhatsApp"
			_ = control2.SetDisplayName(&name, nil)
		}

		control2.Release()
		control.Release()
	}
}

func waGramDeskLiteWebViewProcesses(rootPID uint32) map[uint32]bool {
	parents := map[uint32]uint32{}
	entries := map[uint32]string{}

	snapshot, err := windows.CreateToolhelp32Snapshot(th32csSnapProcess, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil
	}
	for {
		parents[entry.ProcessID] = entry.ParentProcessID
		entries[entry.ProcessID] = syscall.UTF16ToString(entry.ExeFile[:])
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}

	result := make(map[uint32]bool)
	for pid, name := range entries {
		if !strings.EqualFold(name, "msedgewebview2.exe") {
			continue
		}
		for current := pid; current != 0; current = parents[current] {
			if current == rootPID {
				result[pid] = true
				break
			}
		}
	}
	return result
}