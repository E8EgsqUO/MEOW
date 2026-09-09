package main

import (
	"crypto/tls"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// validateConfigForReload runs the checks parseConfig and checkConfig skip:
// that the TLS material and auth files a restarted process would need are
// actually usable. By the time this runs the config syntax is already valid.
// Any problem exits non-zero, which is how the /status reload learns to leave
// the running proxy untouched.
func validateConfigForReload() {
	for _, p := range listenProxy {
		hp, ok := p.(*httpProxy)
		if !ok || hp.proto != "https" {
			continue
		}
		if _, err := tls.LoadX509KeyPair(config.Cert, config.Key); err != nil {
			fmt.Println("https listener certificate error:", err)
			os.Exit(1)
		}
	}
	// initAuth reads UserPasswdFile and AllowedClient and calls Fatal on a
	// malformed entry, which is exactly the validation wanted here.
	initAuth()
}

// rcStartupSig is the size+mtime of the rc file when this process parsed it,
// used by the /status reload to tell "rules only" from "rc changed, restart".
var rcStartupSig string

func fileSignature(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

// isConfigCheckRun reports whether -check-config is on the command line, before
// flags are parsed.
func isConfigCheckRun() bool {
	for _, a := range os.Args[1:] {
		if a == "-check-config" || a == "--check-config" {
			return true
		}
	}
	return false
}

// reloadArgs returns this process's arguments with any -check-config removed.
func reloadArgs() []string {
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		if a == "-check-config" || a == "--check-config" {
			continue
		}
		args = append(args, a)
	}
	return args
}

// validateOnDiskConfig parses the current configuration files in a child
// process. It returns "" when the configuration is sound, otherwise the
// child's diagnostic (which names the offending line or option).
func validateOnDiskConfig() string {
	exe, err := os.Executable()
	if err != nil {
		return "无法定位可执行文件：" + err.Error()
	}
	out, err := exec.Command(exe, append(reloadArgs(), "-check-config")...).CombinedOutput()
	if err == nil {
		return ""
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return msg
	}
	return err.Error()
}
