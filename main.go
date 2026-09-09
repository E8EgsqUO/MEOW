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
	// Parse flags after load config to allow override options in config
	cmdLineConfig := parseCmdLineConfig()
	if cmdLineConfig.PrintVer {
		printVersion()
		os.Exit(0)
	}

	fmt.Printf(`
       /\
   )  ( ')     MEOW Proxy %s
  (  /  )      http://renzhn.github.io/MEOW/
   \(__)|      
	`, version)
	fmt.Println()

	parseConfig(cmdLineConfig.RcFile, cmdLineConfig)

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
