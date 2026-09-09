//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const startupErrorName = "MEOW-startup-error.log"

// windowsGUI is set to "true" with -X only for the GUI-subsystem release.
// Keeping this as a link-time value lets the console and GUI builds use the
// same source while preserving normal command-line behavior in the former.
var windowsGUI = "false"

func isWindowsGUI() bool { return strings.EqualFold(windowsGUI, "true") }

func startupErrorPath() string {
	return filepath.Join(os.TempDir(), startupErrorName)
}

func clearStartupError() {
	if isWindowsGUI() {
		_ = os.Remove(startupErrorPath())
	}
}

func reportCriticalError(message string) {
	if !isWindowsGUI() {
		return
	}
	f, err := os.OpenFile(startupErrorPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	message = strings.TrimSpace(message)
	_, _ = fmt.Fprintf(f, "%s %s\r\n", time.Now().Format("2006-01-02 15:04:05"), message)
}

func acquirePlatformInstance() (release func(), acquired bool, err error) {
	noop := func() {}
	if !isWindowsGUI() {
		return noop, true, nil
	}
	name, err := windows.UTF16PtrFromString(`Local\MEOWProxy.GUI`)
	if err != nil {
		return noop, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return noop, false, nil
	}
	if err != nil {
		return noop, false, err
	}
	return func() { _ = windows.CloseHandle(handle) }, true, nil
}
