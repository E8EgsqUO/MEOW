//go:build windows

package main

import (
	"os"
	"time"
)

// applyValidatedReload stops MEOW after a short delay that lets the pending
// /status response reach the browser. Windows has no exec that replaces the
// running image while keeping the single-instance guard sane, so the operator
// relaunches -- the configuration has already been validated, so the relaunch
// will succeed.
func applyValidatedReload() {
	go func() {
		time.Sleep(300 * time.Millisecond)
		info.Println("reload: configuration valid, exiting so MEOW can be restarted")
		os.Exit(0)
	}()
}

func reloadEndsProcess() bool { return true }
