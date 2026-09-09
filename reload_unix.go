//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

// applyValidatedReload replaces this process with a fresh one using the same
// arguments, after a short delay that lets the pending /status response reach
// the browser. The configuration has already been validated by the caller, so
// the new process will start cleanly. Listening sockets close on exec and the
// new process rebinds them, so active connections drop for about a second.
func applyValidatedReload() {
	exe, err := os.Executable()
	if err != nil {
		errl.Println("reload: cannot find executable:", err)
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		info.Println("reload: re-executing with a fresh configuration")
		argv := append([]string{exe}, reloadArgs()...)
		if err := syscall.Exec(exe, argv, os.Environ()); err != nil {
			errl.Println("reload: re-exec failed, keeping the old configuration:", err)
		}
	}()
}

// reloadEndsProcess reports whether a successful reload restarts (false) or
// exits (true) the process, so /status can word its message correctly.
func reloadEndsProcess() bool { return false }
