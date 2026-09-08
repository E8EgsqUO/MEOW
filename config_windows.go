package main

import (
	"os"
	"path/filepath"
)

const (
	rcFname     = "rc.txt"
	directFname = "direct.txt"
	proxyFname  = "proxy.txt"
	rejectFname = "reject.txt"
	CNIPFname   = "china_ip_list.txt"

	newLine = "\r\n"
)

func getDefaultRcFile() string {
	// On Windows, keep the default configuration beside the executable,
	// independently of the process working directory or how MEOW was launched.
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}
	return filepath.Join(filepath.Dir(executable), rcFname)
}
