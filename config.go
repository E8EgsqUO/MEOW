package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	version           = "1.6.0"
	defaultListenAddr = "127.0.0.1:4411"
)

type LoadBalanceMode byte

const (
	loadBalanceBackup LoadBalanceMode = iota
	loadBalanceHash
	loadBalanceLatency
)

type Config struct {
	RcFile      string // config file
	LogFile     string // path for log file
	JudgeByIP   bool
	LoadBalance LoadBalanceMode // select load balance mode

	SshServer []string

	// authenticate client
	UserPasswd     string
	UserPasswdFile string // file that contains user:passwd:[port] pairs
	AllowedClient  string
	AuthTimeout    time.Duration

	// advanced options
	DialTimeout time.Duration
	ReadTimeout time.Duration
	// ProxyTLSInsecureSkipVerify is a compatibility escape hatch for legacy
	// HTTPS parent proxies with an untrusted certificate.
	ProxyTLSInsecureSkipVerify bool

	Core int

	HttpErrorCode int

	dir        string // directory containing config file
	DirectFile string // direct sites specified by user
	ProxyFile  string // sites using proxy specified by user
	RejectFile string
	CNIPFile   string

	// not configurable in config file
	PrintVer bool

	// not config option
	saveReqLine bool // for http and meow parent, should save request line from client
	Cert        string
	Key         string
}

var config Config
var configNeedUpgrade bool // whether should upgrade config file

func printVersion() {
	fmt.Println("MEOW version", version)
}

func initConfig(rcFile string) {
	config.dir = filepath.Dir(rcFile)
	config.DirectFile = filepath.Join(config.dir, directFname)
	config.ProxyFile = filepath.Join(config.dir, proxyFname)
	config.RejectFile = filepath.Join(config.dir, rejectFname)
	config.CNIPFile = filepath.Join(config.dir, CNIPFname)

	config.JudgeByIP = true

	config.AuthTimeout = 2 * time.Hour
}

// Whether command line options specifies listen addr
var cmdHasListenAddr bool

func parseCmdLineConfig() *Config {
	var c Config
	var listenAddr string

	flag.StringVar(&c.RcFile, "rc", "", "config file, defaults to $HOME/.meow/rc on Unix, ./rc.txt on Windows")
	// Specifying listen default value to StringVar would override config file options
	flag.StringVar(&listenAddr, "listen", "", "listen address, disables listen in config")
	flag.IntVar(&c.Core, "core", 2, "number of cores to use")
	flag.StringVar(&c.LogFile, "logFile", "", "write output to file")
	flag.BoolVar(&c.PrintVer, "version", false, "print version")
	flag.StringVar(&c.Cert, "cert", "", "cert for local https proxy")
	flag.StringVar(&c.Key, "key", "", "key for local https proxy")

	flag.Parse()

	if c.RcFile == "" {
		c.RcFile = getDefaultRcFile()
	} else {
		c.RcFile = expandTilde(c.RcFile)
	}
	if err := isFileExists(c.RcFile); err != nil {
		Fatal("fail to get config file:", err)
	}
	initConfig(c.RcFile)

	if listenAddr != "" {
		configParser{}.ParseListen(listenAddr)
		cmdHasListenAddr = true // must come after parse
	}
	return &c
}

func parseBool(v, msg string) bool {
	switch v {
	case "true":
		return true
	case "false":
		return false
	default:
		Fatalf("%s should be true or false\n", msg)
	}
	return false
}

func parseInt(val, msg string) (i int) {
	var err error
	if i, err = strconv.Atoi(val); err != nil {
		Fatalf("%s should be an integer\n", msg)
	}
	return
}

func parseDuration(val, msg string) (d time.Duration) {
	var err error
	if d, err = time.ParseDuration(val); err != nil {
		Fatalf("%s %v\n", msg, err)
	}
	return
}

func checkServerAddr(addr string) error {
	_, _, err := net.SplitHostPort(addr)
	return err
}

func isUserPasswdValid(val string) bool {
	arr := strings.SplitN(val, ":", 2)
	if len(arr) != 2 || arr[0] == "" || arr[1] == "" {
		return false
	}
	return true
}

// proxyParser provides functions to parse different types of parent proxy
type proxyParser struct{}

func (p proxyParser) ProxySocks5(val string) {
	if err := checkServerAddr(val); err != nil {
		Fatal("parent socks server", err)
	}
	parentProxy.add(newSocksParent(val))
}

func (pp proxyParser) ProxyHttp(val string) {
	var userPasswd, server string

	idx := strings.LastIndex(val, "@")
	if idx == -1 {
		server = val
	} else {
		userPasswd = val[:idx]
		server = val[idx+1:]
	}

	if err := checkServerAddr(server); err != nil {
		Fatal("parent http server", err)
	}

	config.saveReqLine = true

	parent := newHttpParent(server)
	parent.initAuth(userPasswd)
	parentProxy.add(parent)
}

func (pp proxyParser) ProxyHttps(val string) {
	var userPasswd, server string

	idx := strings.LastIndex(val, "@")
	if idx == -1 {
		server = val
	} else {
		userPasswd = val[:idx]
		server = val[idx+1:]
	}

	if err := checkServerAddr(server); err != nil {
		Fatal("parent http server", err)
	}

	config.saveReqLine = true

	parent := newHttpsParent(server)
	parent.initAuth(userPasswd)
	parentProxy.add(parent)
}

// Parse method:passwd@server:port
func parseMethodPasswdServer(val string) (method, passwd, server string, err error) {
	// Use the right-most @ symbol to seperate method:passwd and server:port.
	idx := strings.LastIndex(val, "@")
	if idx == -1 {
		err = errors.New("requires both encrypt method and password")
		return
	}

	methodPasswd := val[:idx]
	server = val[idx+1:]
	if err = checkServerAddr(server); err != nil {
		return
	}

	// Password can have : inside, but I don't recommend this.
	arr := strings.SplitN(methodPasswd, ":", 2)
	if len(arr) != 2 {
		err = errors.New("method and password should be separated by :")
		return
	}
	method = arr[0]
	passwd = arr[1]
	return
}

// parse shadowsocks proxy
func (pp proxyParser) ProxySs(val string) {
	method, passwd, server, err := parseMethodPasswdServer(val)
	if err != nil {
		Fatal("shadowsocks parent", err)
	}
	parent := newShadowsocksParent(server)
	parent.initCipher(method, passwd)
	parentProxy.add(parent)
}

func (pp proxyParser) ProxyMeow(val string) {
	method, passwd, server, err := parseMethodPasswdServer(val)
	if err != nil {
		Fatal("meow parent", err)
	}

	if err := checkServerAddr(server); err != nil {
		Fatal("parent meow server", err)
	}

	config.saveReqLine = true
	parent := newMeowParent(server, method, passwd)
	parentProxy.add(parent)
}

// -------------------------------
func (pp proxyParser) ProxyRelay(val string) {
	addr, _ := splitAddrParams(val)
	if err := checkServerAddr(addr); err != nil {
		Fatal("parent relay server", err)
	}
	parentProxy.add(newRelayParent(val))
}

// listenParser provides functions to parse different types of listen addresses
type listenParser struct{}

func (lp listenParser) ListenRelay(val string, proto string) {
	if cmdHasListenAddr {
		return
	}
	addr, _ := splitAddrParams(val)
	if err := checkServerAddr(addr); err != nil {
		Fatal("listen", proto, "server", err)
	}
	addListenProxy(newRelayProxy(val))
}

// -------------------------------
func (lp listenParser) ListenHttp(val string, proto string) {
	if cmdHasListenAddr {
		return
	}

	arr := strings.Fields(val)
	if len(arr) > 2 {
		Fatal("too many fields in listen =", proto, val)
	}

	var addr, addrInPAC string
	addr = arr[0]
	if len(arr) == 2 {
		addrInPAC = arr[1]
	}

	if err := checkServerAddr(addr); err != nil {
		Fatal("listen", proto, "server", err)
	}
	addListenProxy(newHttpProxy(addr, addrInPAC, proto))
}

func (lp listenParser) ListenMeow(val string) {
	if cmdHasListenAddr {
		return
	}
	method, passwd, addr, err := parseMethodPasswdServer(val)
	if err != nil {
		Fatal("listen meow", err)
	}
	addListenProxy(newMeowProxy(method, passwd, addr))
}

// configParser provides functions to parse options in config file.
type configParser struct{}

func upperFirst(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (p configParser) ParseProxy(val string) {
	arr := strings.Split(val, "://")
	if len(arr) != 2 {
		Fatal("proxy has no protocol specified:", val)
	}
	protocol := arr[0]
	parser := proxyParser{}
	switch upperFirst(protocol) {
	case "Socks5":
		parser.ProxySocks5(arr[1])
	case "Http":
		parser.ProxyHttp(arr[1])
	case "Https":
		parser.ProxyHttps(arr[1])
	case "Ss":
		parser.ProxySs(arr[1])
	case "Meow":
		parser.ProxyMeow(arr[1])
	case "Relay":
		parser.ProxyRelay(arr[1])
	default:
		Fatalf("no such protocol \"%s\"\n", arr[0])
	}
}

func (p configParser) ParseListen(val string) {
	if cmdHasListenAddr {
		return
	}

	var protocol, server string
	arr := strings.Split(val, "://")
	if len(arr) == 1 {
		protocol = "http"
		server = val
		configNeedUpgrade = true
	} else {
		protocol = arr[0]
		server = arr[1]
	}

	parser := listenParser{}
	switch upperFirst(protocol) {
	case "Http", "Https":
		parser.ListenHttp(server, protocol)
	case "Meow":
		parser.ListenMeow(server)
	case "Relay":
		parser.ListenRelay(server, protocol)
	default:
		Fatalf("no such listen protocol \"%s\"\n", arr[0])
	}
}

func (p configParser) ParseLogFile(val string) {
	config.LogFile = expandTilde(val)
}

func (p configParser) ParseAddrInPAC(val string) {
	configNeedUpgrade = true
	arr := strings.Split(val, ",")
	for i, s := range arr {
		if s == "" {
			continue
		}
		s = strings.TrimSpace(s)
		host, _, err := net.SplitHostPort(s)
		if err != nil {
			Fatal("proxy address in PAC", err)
		}
		if host == "0.0.0.0" {
			Fatal("can't use 0.0.0.0 as proxy address in PAC")
		}
		if hp, ok := listenProxy[i].(*httpProxy); ok {
			hp.addrInPAC = s
		} else {
			Fatal("can't specify address in PAC for non http proxy")
		}
	}
}

func (p configParser) ParseSocksParent(val string) {
	var pp proxyParser
	pp.ProxySocks5(val)
	configNeedUpgrade = true
}

func (p configParser) ParseSshServer(val string) {
	arr := strings.Split(val, ":")
	if len(arr) == 2 {
		val += ":22"
	} else if len(arr) == 3 {
		if arr[2] == "" {
			val += "22"
		}
	} else {
		Fatal("sshServer should be in the form of: user@server:local_socks_port[:server_ssh_port]")
	}
	// add created socks server
	p.ParseSocksParent("127.0.0.1:" + arr[1])
	config.SshServer = append(config.SshServer, val)
}

var http struct {
	parent    *httpParent
	serverCnt int
	passwdCnt int
}

func (p configParser) ParseHttpParent(val string) {
	if err := checkServerAddr(val); err != nil {
		Fatal("parent http server", err)
	}
	config.saveReqLine = true
	http.parent = newHttpParent(val)
	parentProxy.add(http.parent)
	http.serverCnt++
	configNeedUpgrade = true
}

func (p configParser) ParseHttpUserPasswd(val string) {
	if !isUserPasswdValid(val) {
		Fatal("httpUserPassword syntax wrong, should be in the form of user:passwd")
	}
	if http.passwdCnt >= http.serverCnt {
		Fatal("must specify httpParent before corresponding httpUserPasswd")
	}
	http.parent.initAuth(val)
	http.passwdCnt++
}

func (p configParser) ParseLoadBalance(val string) {
	switch val {
	case "backup":
		config.LoadBalance = loadBalanceBackup
	case "hash":
		config.LoadBalance = loadBalanceHash
	case "latency":
		config.LoadBalance = loadBalanceLatency
	default:
		Fatalf("invalid loadBalance mode: %s\n", val)
	}
}

func (p configParser) ParseDirectFile(val string) {
	config.DirectFile = expandTilde(val)
	if err := isFileExists(config.DirectFile); err != nil {
		Fatal("direct file:", err)
	}
}

func (p configParser) ParseProxyFile(val string) {
	config.ProxyFile = expandTilde(val)
	if err := isFileExists(config.ProxyFile); err != nil {
		Fatal("proxy file:", err)
	}
}

var shadow struct {
	parent *shadowsocksParent
	passwd string
	method string

	serverCnt int
	passwdCnt int
	methodCnt int
}

func (p configParser) ParseShadowSocks(val string) {
	if shadow.serverCnt-shadow.passwdCnt > 1 {
		Fatal("must specify shadowPasswd for every shadowSocks server")
	}
	// create new shadowsocks parent if both server and password are given
	// previously
	if shadow.parent != nil && shadow.serverCnt == shadow.passwdCnt {
		if shadow.methodCnt < shadow.serverCnt {
			shadow.method = ""
			shadow.methodCnt = shadow.serverCnt
		}
		shadow.parent.initCipher(shadow.method, shadow.passwd)
	}
	if val == "" { // the final call
		shadow.parent = nil
		return
	}
	if err := checkServerAddr(val); err != nil {
		Fatal("shadowsocks server", err)
	}
	shadow.parent = newShadowsocksParent(val)
	parentProxy.add(shadow.parent)
	shadow.serverCnt++
	configNeedUpgrade = true
}

func (p configParser) ParseShadowPasswd(val string) {
	if shadow.passwdCnt >= shadow.serverCnt {
		Fatal("must specify shadowSocks before corresponding shadowPasswd")
	}
	if shadow.passwdCnt+1 != shadow.serverCnt {
		Fatal("must specify shadowPasswd for every shadowSocks")
	}
	shadow.passwd = val
	shadow.passwdCnt++
}

func (p configParser) ParseShadowMethod(val string) {
	if shadow.methodCnt >= shadow.serverCnt {
		Fatal("must specify shadowSocks before corresponding shadowMethod")
	}
	// shadowMethod is optional
	shadow.method = val
	shadow.methodCnt++
}

func checkShadowsocks() {
	if shadow.serverCnt != shadow.passwdCnt {
		Fatal("number of shadowsocks server and password does not match")
	}
	// parse the last shadowSocks option again to initialize the last
	// shadowsocks server
	parser := configParser{}
	parser.ParseShadowSocks("")
}

// Put actual authentication related config parsing in auth.go, so config.go
// doesn't need to know the details of authentication implementation.

func (p configParser) ParseUserPasswd(val string) {
	config.UserPasswd = val
	if !isUserPasswdValid(config.UserPasswd) {
		Fatal("userPassword syntax wrong, should be in the form of user:passwd")
	}
}

func (p configParser) ParseUserPasswdFile(val string) {
	err := isFileExists(val)
	if err != nil {
		Fatal("userPasswdFile:", err)
	}
	config.UserPasswdFile = val
}

func (p configParser) ParseAllowedClient(val string) {
	config.AllowedClient = val
}

func (p configParser) ParseAuthTimeout(val string) {
	config.AuthTimeout = parseDuration(val, "authTimeout")
}

func (p configParser) ParseCore(val string) {
	config.Core = parseInt(val, "core")
}

func (p configParser) ParseHttpErrorCode(val string) {
	config.HttpErrorCode = parseInt(val, "httpErrorCode")
}

func (p configParser) ParseReadTimeout(val string) {
	config.ReadTimeout = parseDuration(val, "readTimeout")
}

func (p configParser) ParseDialTimeout(val string) {
	config.DialTimeout = parseDuration(val, "dialTimeout")
}

func (p configParser) ParseProxyTLSInsecureSkipVerify(val string) {
	config.ProxyTLSInsecureSkipVerify = parseBool(val, "proxyTLSInsecureSkipVerify")
}

func (p configParser) ParseJudgeByIP(val string) {
	config.JudgeByIP = parseBool(val, "judgeByIP")
}

func (p configParser) ParseCert(val string) {
	config.Cert = val
}

func (p configParser) ParseKey(val string) {
	config.Key = val
}

type configParseFunc func(configParser, string)

var configParsers = map[string]configParseFunc{
	"Proxy":                      configParser.ParseProxy,
	"Listen":                     configParser.ParseListen,
	"LogFile":                    configParser.ParseLogFile,
	"AddrInPAC":                  configParser.ParseAddrInPAC,
	"SocksParent":                configParser.ParseSocksParent,
	"SshServer":                  configParser.ParseSshServer,
	"HttpParent":                 configParser.ParseHttpParent,
	"HttpUserPasswd":             configParser.ParseHttpUserPasswd,
	"LoadBalance":                configParser.ParseLoadBalance,
	"DirectFile":                 configParser.ParseDirectFile,
	"ProxyFile":                  configParser.ParseProxyFile,
	"ShadowSocks":                configParser.ParseShadowSocks,
	"ShadowPasswd":               configParser.ParseShadowPasswd,
	"ShadowMethod":               configParser.ParseShadowMethod,
	"UserPasswd":                 configParser.ParseUserPasswd,
	"UserPasswdFile":             configParser.ParseUserPasswdFile,
	"AllowedClient":              configParser.ParseAllowedClient,
	"AuthTimeout":                configParser.ParseAuthTimeout,
	"Core":                       configParser.ParseCore,
	"HttpErrorCode":              configParser.ParseHttpErrorCode,
	"ReadTimeout":                configParser.ParseReadTimeout,
	"DialTimeout":                configParser.ParseDialTimeout,
	"ProxyTLSInsecureSkipVerify": configParser.ParseProxyTLSInsecureSkipVerify,
	"JudgeByIP":                  configParser.ParseJudgeByIP,
	"Cert":                       configParser.ParseCert,
	"Key":                        configParser.ParseKey,
}

func findConfigParser(key string) (configParseFunc, bool) {
	parser, ok := configParsers[upperFirst(key)]
	return parser, ok
}

// overrideConfig should contain options from command line to override options
// in config file.
func parseConfig(rc string, override *Config) {
	// fmt.Println("rcFile:", path)
	f, err := os.Open(expandTilde(rc))
	if err != nil {
		Fatal("Error opening config file:", err)
	}

	IgnoreUTF8BOM(f)

	scanner := bufio.NewScanner(f)

	var lines []string // store lines for upgrade

	var n int
	for scanner.Scan() {
		lines = append(lines, scanner.Text())

		n++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}

		v := strings.SplitN(line, "=", 2)
		if len(v) != 2 {
			Fatal("config syntax error on line", n)
		}
		key, val := strings.TrimSpace(v[0]), strings.TrimSpace(v[1])

		parse, ok := findConfigParser(key)
		if !ok {
			Fatalf("no such option \"%s\"\n", key)
		}
		// for backward compatibility, allow empty string in shadowMethod and logFile
		if val == "" && key != "shadowMethod" && key != "logFile" {
			Fatalf("empty %s, please comment or remove unused option\n", key)
		}
		parse(configParser{}, val)
	}
	if scanner.Err() != nil {
		Fatalf("Error reading rc file: %v\n", scanner.Err())
	}
	f.Close()

	overrideConfig(&config, override)
	checkConfig()

	if configNeedUpgrade {
		upgradeConfig(rc, lines)
	}
}

func upgradeConfig(rc string, lines []string) {
	newrc := rc + ".upgrade"
	f, err := os.Create(newrc)
	if err != nil {
		fmt.Println("can't create upgraded config file")
		return
	}

	// Upgrade config.
	proxyId := 0
	listenId := 0
	w := bufio.NewWriter(f)
	for _, line := range lines {
		line := strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			w.WriteString(line + newLine)
			continue
		}

		v := strings.Split(line, "=")
		key := strings.TrimSpace(v[0])

		switch key {
		case "listen":
			listen := listenProxy[listenId]
			listenId++
			w.WriteString(listen.genConfig() + newLine)
			// comment out original
			w.WriteString("#" + line + newLine)
		case "httpParent", "shadowSocks", "socksParent":
			backPool, ok := parentProxy.(*backupParentPool)
			if !ok {
				panic("initial parent pool should be backup pool")
			}
			parent := backPool.parent[proxyId]
			proxyId++
			w.WriteString(parent.genConfig() + newLine)
			// comment out original
			w.WriteString("#" + line + newLine)
		case "httpUserPasswd", "shadowPasswd", "shadowMethod", "addrInPAC":
			// just comment out
			w.WriteString("#" + line + newLine)
		case "proxy":
			proxyId++
			w.WriteString(line + newLine)
		default:
			w.WriteString(line + newLine)
		}
	}
	w.Flush()
	f.Close() // Must close file before renaming, otherwise will fail on windows.

	// Rename new and old config file.
	if err := os.Rename(rc, rc+"0.8"); err != nil {
		fmt.Println("can't backup config file for upgrade:", err)
		return
	}
	if err := os.Rename(newrc, rc); err != nil {
		fmt.Println("can't rename upgraded rc to original name:", err)
		return
	}
}

func overrideConfig(oldconfig, override *Config) {
	if override.RcFile != "" {
		oldconfig.RcFile = override.RcFile
	}
	if override.LogFile != "" {
		oldconfig.LogFile = override.LogFile
	}
	if override.UserPasswd != "" {
		oldconfig.UserPasswd = override.UserPasswd
	}
	if override.UserPasswdFile != "" {
		oldconfig.UserPasswdFile = override.UserPasswdFile
	}
	if override.AllowedClient != "" {
		oldconfig.AllowedClient = override.AllowedClient
	}
	if override.Core != 0 {
		oldconfig.Core = override.Core
	}
	if override.HttpErrorCode != 0 {
		oldconfig.HttpErrorCode = override.HttpErrorCode
	}
	if override.DirectFile != "" {
		oldconfig.DirectFile = override.DirectFile
	}
	if override.ProxyFile != "" {
		oldconfig.ProxyFile = override.ProxyFile
	}
	if override.RejectFile != "" {
		oldconfig.RejectFile = override.RejectFile
	}
	if override.CNIPFile != "" {
		oldconfig.CNIPFile = override.CNIPFile
	}
	if override.Cert != "" {
		oldconfig.Cert = override.Cert
	}
	if override.Key != "" {
		oldconfig.Key = override.Key
	}
}

// Must call checkConfig before using config.
func checkConfig() {
	checkShadowsocks()
	// listenAddr must be handled first, as addrInPAC dependends on this.
	if listenProxy == nil {
		listenProxy = []Proxy{newHttpProxy(defaultListenAddr, "", "http")}
	}
}
