package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func SshRunning(ctx context.Context, socksServer string) bool {
	c, err := dialContext(ctx, "tcp", socksServer)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runOneSSH(ctx context.Context, server string) {
	// config parsing canonicalize sshServer config value
	arr := strings.SplitN(server, ":", 3)
	sshServer, localPort, sshPort := arr[0], arr[1], arr[2]
	alreadyRunPrinted := false

	socksServer := "127.0.0.1:" + localPort
	for {
		if ctx.Err() != nil {
			return
		}
		if SshRunning(ctx, socksServer) {
			if !alreadyRunPrinted {
				debug.Println("ssh socks server", socksServer, "maybe already running")
				alreadyRunPrinted = true
			}
			if !waitContext(ctx, 30*time.Second) {
				return
			}
			continue
		}

		// -n redirects stdin from /dev/null
		// -N do not execute remote command
		debug.Println("connecting to ssh server", sshServer+":"+sshPort)
		cmd := exec.CommandContext(ctx, "ssh", "-n", "-N", "-D", localPort, "-p", sshPort, sshServer)
		if err := cmd.Run(); err != nil {
			debug.Println("ssh:", err)
		}
		debug.Println("ssh", sshServer+":"+sshPort, "exited, reconnect")
		if !waitContext(ctx, 5*time.Second) {
			return
		}
		alreadyRunPrinted = false
	}
}

func runSSH(ctx context.Context) {
	for _, server := range config.SshServer {
		go runOneSSH(ctx, server)
	}
}
