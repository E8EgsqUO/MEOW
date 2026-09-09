package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestInitConfigLocatesRuleFilesBesideRC(t *testing.T) {
	originalConfig := config
	defer func() { config = originalConfig }()
	rcFile := filepath.Join(t.TempDir(), rcFname)
	initConfig(rcFile)
	if config.dir != filepath.Dir(rcFile) {
		t.Fatalf("config directory = %q, want %q", config.dir, filepath.Dir(rcFile))
	}
	if config.ProxyFile != filepath.Join(filepath.Dir(rcFile), proxyFname) {
		t.Fatalf("proxy file = %q", config.ProxyFile)
	}
}

func TestFindConfigParserPreservesKeyMatching(t *testing.T) {
	for _, key := range []string{"proxy", "Proxy", "loadBalance", "LoadBalance", "proxyTLSInsecureSkipVerify"} {
		if _, ok := findConfigParser(key); !ok {
			t.Errorf("documented config key %q was not found", key)
		}
	}
	for _, key := range []string{"", "PROXY", "loadbalance", "unknown"} {
		if _, ok := findConfigParser(key); ok {
			t.Errorf("invalid config key %q was accepted", key)
		}
	}
}

func TestOverrideConfigPreservesLegacySemantics(t *testing.T) {
	oldConfig := Config{LogFile: "configured.log", Core: 1, HttpErrorCode: 418, JudgeByIP: true}
	override := Config{RcFile: "rc.txt", Core: 2, Cert: "cert.pem", JudgeByIP: false}
	overrideConfig(&oldConfig, &override)

	if oldConfig.RcFile != "rc.txt" || oldConfig.LogFile != "configured.log" || oldConfig.Core != 2 || oldConfig.Cert != "cert.pem" {
		t.Fatalf("unexpected overridden config: %+v", oldConfig)
	}
	if !oldConfig.JudgeByIP || oldConfig.HttpErrorCode != 418 {
		t.Fatalf("zero or non-overridable fields changed: %+v", oldConfig)
	}
}

func TestParseListen(t *testing.T) {
	parser := configParser{}
	parser.ParseListen("http://127.0.0.1:8888")

	hp, ok := listenProxy[0].(*httpProxy)
	if !ok {
		t.Error("listen http proxy type wrong")
	}
	if hp.addr != "127.0.0.1:8888" {
		t.Error("listen http server address parse error")
	}

	parser.ParseListen("http://127.0.0.1:8888 1.2.3.4:5678")
	hp, ok = listenProxy[1].(*httpProxy)
	if hp.addrInPAC != "1.2.3.4:5678" {
		t.Error("listen http addrInPAC parse error")
	}
}

func TestParseProxy(t *testing.T) {
	pool, ok := parentProxy.(*backupParentPool)
	if !ok {
		t.Fatal("parentPool by default should be backup pool")
	}
	cnt := -1

	var parser configParser
	parser.ParseProxy("http://127.0.0.1:8080")
	cnt++

	hp, ok := pool.parent[cnt].ParentProxy.(*httpParent)
	if !ok {
		t.Fatal("1st http proxy parsed not as httpParent")
	}
	if hp.server != "127.0.0.1:8080" {
		t.Error("1st http proxy server address wrong, got:", hp.server)
	}

	parser.ParseProxy("http://user:passwd@127.0.0.2:9090")
	cnt++
	hp, ok = pool.parent[cnt].ParentProxy.(*httpParent)
	if !ok {
		t.Fatal("2nd http proxy parsed not as httpParent")
	}
	if hp.server != "127.0.0.2:9090" {
		t.Error("2nd http proxy server address wrong, got:", hp.server)
	}
	if hp.authHeader == nil {
		t.Error("2nd http proxy server user password not parsed")
	}

	parser.ParseProxy("socks5://127.0.0.1:1080")
	cnt++
	sp, ok := pool.parent[cnt].ParentProxy.(*socksParent)
	if !ok {
		t.Fatal("socks proxy parsed not as socksParent")
	}
	if sp.server != "127.0.0.1:1080" {
		t.Error("socks server address wrong, got:", sp.server)
	}

	parser.ParseProxy("ss://aes-256-cfb:foobar!@127.0.0.1:1080")
	cnt++
	_, ok = pool.parent[cnt].ParentProxy.(*shadowsocksParent)
	if !ok {
		t.Fatal("shadowsocks proxy parsed not as shadowsocksParent")
	}
}

func TestParseIPv6Policy(t *testing.T) {
	saved := config.IPv6Policy
	defer func() { config.IPv6Policy = saved }()

	parser := configParser{}
	for _, tc := range []struct {
		val  string
		want IPv6Policy
	}{
		{"judge", ipv6PolicyJudge},
		{"direct", ipv6PolicyDirect},
		{"proxy", ipv6PolicyProxy},
		{"Judge", ipv6PolicyJudge},
		{"DIRECT", ipv6PolicyDirect},
	} {
		config.IPv6Policy = ipv6PolicyProxy
		parser.ParseIPv6Policy(tc.val)
		if config.IPv6Policy != tc.want {
			t.Errorf("ParseIPv6Policy(%q) = %v, want %v", tc.val, config.IPv6Policy, tc.want)
		}
	}

	if _, ok := findConfigParser("ipv6Policy"); !ok {
		t.Error("ipv6Policy is not reachable as a config file option")
	}
}

func TestIPv6PolicyDefaultsToJudge(t *testing.T) {
	saved := config
	defer func() { config = saved }()

	config = Config{}
	initConfig("/tmp/meow-test/rc")
	if config.IPv6Policy != ipv6PolicyJudge {
		t.Errorf("default IPv6Policy = %v, want judge", config.IPv6Policy)
	}
}

// The -core flag used to default to 2 rather than 0. Because overrideConfig
// treats any non-zero command line value as an explicit override, that default
// beat the rc file and pinned every run to two cores.
func TestRcCoreSurvivesCmdlineDefault(t *testing.T) {
	fromRc := Config{Core: 4}
	overrideConfig(&fromRc, &Config{})
	if fromRc.Core != 4 {
		t.Errorf("rc core = %d, want 4: the command line default discarded it", fromRc.Core)
	}

	fromRc = Config{Core: 4}
	overrideConfig(&fromRc, &Config{Core: 1})
	if fromRc.Core != 1 {
		t.Errorf("core = %d, want 1: an explicit -core must still win", fromRc.Core)
	}
}

// -version used to die on "fail to get config file" before it ever printed
// anything, which is the wrong answer on a machine where MEOW is not set up.
func TestPrintVersionNeedsNoConfigFile(t *testing.T) {
	savedArgs, savedFlags := os.Args, flag.CommandLine
	savedConfig := config
	t.Cleanup(func() {
		os.Args, flag.CommandLine = savedArgs, savedFlags
		config = savedConfig
	})

	os.Args = []string{"meow", "-version", "-rc", filepath.Join(t.TempDir(), "does-not-exist")}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)

	c := parseCmdLineConfig()
	if !c.PrintVer {
		t.Fatal("PrintVer not set")
	}
}
