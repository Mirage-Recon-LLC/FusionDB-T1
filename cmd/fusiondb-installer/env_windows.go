//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// updatePath adds the given directory to the User PATH environment variable
// if it is not already present. It returns true if the path was added,
// and any error encountered.
func updatePath(installDir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, fmt.Errorf("failed to open registry key HKEY_CURRENT_USER\\Environment: %w", err)
	}
	defer k.Close()

	val, valType, err := k.GetStringValue("Path")
	if err != nil {
		// If Path doesn't exist, we can initialize it
		val = ""
		valType = registry.EXPAND_SZ
	}

	// Clean and normalize paths for comparison
	normalizedInstallDir := filepathClean(installDir)
	paths := strings.Split(val, ";")
	for _, p := range paths {
		if filepathClean(p) == normalizedInstallDir {
			// Already present
			return false, nil
		}
	}

	// Append the new directory
	newPath := val
	if len(newPath) > 0 && !strings.HasSuffix(newPath, ";") {
		newPath += ";"
	}
	newPath += installDir

	if valType == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", newPath)
	} else {
		err = k.SetStringValue("Path", newPath)
	}
	if err != nil {
		return false, fmt.Errorf("failed to write registry key: %w", err)
	}

	// Broadcast WM_SETTINGCHANGE so newly launched shells pick up the change
	broadcastSettingChange()

	return true, nil
}

// broadcastSettingChange broadcasts a WM_SETTINGCHANGE message to the system.
func broadcastSettingChange() {
	var (
		user32                  = syscall.NewLazyDLL("user32.dll")
		procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
	)

	const (
		hwndBroadcast     = 0xffff
		wmSettingChange   = 0x001a
		smtoAbortIfHung   = 0x0002
		timeout           = 5000 // 5 seconds
	)

	envStr, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}

	var result uintptr
	// Call SendMessageTimeout
	procSendMessageTimeoutW.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(envStr)),
		smtoAbortIfHung,
		timeout,
		uintptr(unsafe.Pointer(&result)),
	)
}

// filepathClean cleans a filepath string for comparison
func filepathClean(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ToLower(p)
	p = strings.ReplaceAll(p, "/", "\\")
	p = strings.TrimSuffix(p, "\\")
	return p
}
