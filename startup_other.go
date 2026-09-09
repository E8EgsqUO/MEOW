//go:build !windows

package main

func clearStartupError() {}

func reportCriticalError(string) {}

func acquirePlatformInstance() (release func(), acquired bool, err error) {
	return func() {}, true, nil
}
