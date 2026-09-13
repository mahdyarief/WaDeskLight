//go:build windows

package app

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	processPowerThrottlingVersion1  = 1
	processPowerThrottlingExecSpeed = 0x1
	// PROCESS_INFORMATION_CLASS::ProcessPowerThrottling
	processInformationPowerThrottling = 4

	webView2ProcessName = "msedgewebview2.exe"
)

type processPowerThrottlingState struct {
	Version     uint32
	ControlMask uint32
	StateMask   uint32
}

// setEcoQoS throttles this process and its WebView2 children to reduced clocks
// on efficiency cores, which is the same treatment Edge gives background tabs.
// It is a scheduling hint only: nothing is allocated or freed, so it does not
// move memory usage either way.
func setEcoQoS(enabled bool) {
	state := processPowerThrottlingState{Version: processPowerThrottlingVersion1}
	if enabled {
		state.ControlMask = processPowerThrottlingExecSpeed
		state.StateMask = processPowerThrottlingExecSpeed
	}
	// Leaving ControlMask at zero hands the decision back to the system.

	self := windows.GetCurrentProcessId()
	throttle(self, &state)
	for _, pid := range webView2Descendants(self) {
		throttle(pid, &state)
	}
}

func throttle(pid uint32, state *processPowerThrottlingState) {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	procSetProcessInformation.Call(
		uintptr(handle),
		processInformationPowerThrottling,
		uintptr(unsafe.Pointer(state)),
		unsafe.Sizeof(*state),
	)
}

// webView2Descendants returns the WebView2 processes below root. WebView2 runs
// its browser process as our child and the renderer, GPU and utility processes
// below that, so the whole subtree has to be walked.
//
// Results are filtered by executable name: PIDs are recycled by Windows, and a
// stale parent id must never let us throttle an unrelated process.
func webView2Descendants(root uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	type process struct {
		pid, parent uint32
		isWebView2  bool
	}
	var all []process

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		name := windows.UTF16ToString(entry.ExeFile[:])
		all = append(all, process{
			pid:        entry.ProcessID,
			parent:     entry.ParentProcessID,
			isWebView2: strings.EqualFold(name, webView2ProcessName),
		})
	}

	inTree := map[uint32]bool{root: true}
	var found []uint32
	// The snapshot is in no particular order, so sweep until nothing new shows up.
	for growing := true; growing; {
		growing = false
		for _, p := range all {
			if !inTree[p.parent] || inTree[p.pid] {
				continue
			}
			inTree[p.pid] = true
			growing = true
			if p.isWebView2 {
				found = append(found, p.pid)
			}
		}
	}
	return found
}
