package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
)

func main() {
	// A -check-config run is a short-lived child of a running instance; it must
	// not contend for the single-instance guard or touch the startup-error log.
	if !isConfigCheckRun() {
		releaseInstance, acquired, err := acquirePlatformInstance()
		if err != nil {
			Fatal("failed to initialize platform runtime:", err)
		}
		if !acquired {
			return
		}
		defer releaseInstance()
		clearStartupError()
	}

	// Parse flags after load config to allow override options in config
	cmdLineConfig := parseCmdLineConfig()
	if cmdLineConfig.PrintVer {
		printVersion()
		os.Exit(0)
	}

	if !cmdLineConfig.CheckConfig {
		fmt.Printf(`
       /\
   )  ( ')     MEOW Proxy %s
  (  /  )      http://renzhn.github.io/MEOW/
   \(__)|
	`, version)
		fmt.Println()
	}

	parseConfig(cmdLineConfig.RcFile, cmdLineConfig)

	if cmdLineConfig.CheckConfig {
		validateConfigForReload()
		fmt.Println("config OK")
		os.Exit(0)
	}

	initLog()
	initDomainLists(domainList, config)
	initSelfListenAddr()
	initAuth()
	initStat()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connPool.Start(ctx)

	initParentPool(ctx)

	if config.JudgeByIP {
		initCNIPData()
		initDNS()
	}

	if config.Core > 0 {
		runtime.GOMAXPROCS(config.Core)
	}

	go runSSH(ctx)

	var wg sync.WaitGroup
	wg.Add(len(listenProxy))
	for _, proxy := range listenProxy {
		go proxy.Serve(ctx, &wg)
	}
	wg.Wait()
	connPool.CloseAll()
}
