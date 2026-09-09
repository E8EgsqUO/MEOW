package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	routeHistorySize = 256
	errorHistorySize = 64
)

type routeStatus struct {
	At    time.Time
	Host  string
	Route string
	Count uint64
}

type errorStatus struct {
	At     time.Time
	Target string
	Detail string
	Count  uint64
}

var runtimeStatus = struct {
	sync.Mutex
	started     time.Time
	routes      [routeHistorySize]routeStatus
	routeNext   int
	routeLen    int
	routeTotals [4]uint64
	errors      [errorHistorySize]errorStatus
	errorNext   int
	errorLen    int
}{started: time.Now()}

func domainTypeName(route DomainType) string {
	switch route {
	case domainTypeDirect:
		return "DIRECT"
	case domainTypeProxy:
		return "PROXY"
	case domainTypeReject:
		return "REJECT"
	default:
		return "UNKNOWN"
	}
}

func recordRouteStatus(host string, route DomainType) {
	recordRouteStatusNamed(host, domainTypeName(route), route)
}

func recordRouteStatusNamed(host, name string, route DomainType) {
	if host == "" {
		return
	}
	now := time.Now()
	runtimeStatus.Lock()
	if int(route) < len(runtimeStatus.routeTotals) {
		runtimeStatus.routeTotals[route]++
	}
	if runtimeStatus.routeLen > 0 {
		last := (runtimeStatus.routeNext - 1 + routeHistorySize) % routeHistorySize
		entry := &runtimeStatus.routes[last]
		if entry.Host == host && entry.Route == name {
			entry.At = now
			entry.Count++
			runtimeStatus.Unlock()
			return
		}
	}
	runtimeStatus.routes[runtimeStatus.routeNext] = routeStatus{At: now, Host: host, Route: name, Count: 1}
	runtimeStatus.routeNext = (runtimeStatus.routeNext + 1) % routeHistorySize
	if runtimeStatus.routeLen < routeHistorySize {
		runtimeStatus.routeLen++
	}
	runtimeStatus.Unlock()
}

func recordRuntimeError(target string, err error) {
	if err == nil {
		return
	}
	now := time.Now()
	detail := err.Error()
	runtimeStatus.Lock()
	for i := 0; i < runtimeStatus.errorLen; i++ {
		entry := &runtimeStatus.errors[i]
		if entry.Target == target && entry.Detail == detail {
			entry.At = now
			entry.Count++
			runtimeStatus.Unlock()
			return
		}
	}
	runtimeStatus.errors[runtimeStatus.errorNext] = errorStatus{At: now, Target: target, Detail: detail, Count: 1}
	runtimeStatus.errorNext = (runtimeStatus.errorNext + 1) % errorHistorySize
	if runtimeStatus.errorLen < errorHistorySize {
		runtimeStatus.errorLen++
	}
	runtimeStatus.Unlock()
}

type runtimeStatusView struct {
	Version      string
	Uptime       string
	RequestLog   bool
	ReplyLog     bool
	DirectTotal  uint64
	ProxyTotal   uint64
	RejectTotal  uint64
	Goroutines   int
	LearnedRules int
	PoolSites    int
	PoolConns    int
	PoolMuxConns int
	Parents      []parentProxyStatus
	Notice       string
	Routes       []routeStatus
	Errors       []errorStatus
}

func snapshotRuntimeStatus(notice string) runtimeStatusView {
	runtimeStatus.Lock()
	view := runtimeStatusView{
		Version:     version,
		Uptime:      time.Since(runtimeStatus.started).Round(time.Second).String(),
		RequestLog:  bool(dbgRq),
		ReplyLog:    bool(dbgRep),
		DirectTotal: runtimeStatus.routeTotals[domainTypeDirect],
		ProxyTotal:  runtimeStatus.routeTotals[domainTypeProxy],
		RejectTotal: runtimeStatus.routeTotals[domainTypeReject],
		Notice:      notice,
		Routes:      make([]routeStatus, 0, runtimeStatus.routeLen),
		Errors:      make([]errorStatus, 0, runtimeStatus.errorLen),
	}
	for i := 0; i < runtimeStatus.routeLen; i++ {
		idx := (runtimeStatus.routeNext - 1 - i + routeHistorySize) % routeHistorySize
		view.Routes = append(view.Routes, runtimeStatus.routes[idx])
	}
	for i := 0; i < runtimeStatus.errorLen; i++ {
		if runtimeStatus.errors[i].Count != 0 {
			view.Errors = append(view.Errors, runtimeStatus.errors[i])
		}
	}
	runtimeStatus.Unlock()

	sort.Slice(view.Errors, func(i, j int) bool { return view.Errors[i].At.After(view.Errors[j].At) })
	view.Goroutines = runtime.NumGoroutine()
	view.LearnedRules = domainList.learnedCount()
	view.PoolSites, view.PoolConns, view.PoolMuxConns = connPool.stats()
	view.Parents = parentProxyStatuses()
	return view
}

var runtimeStatusTemplate = template.Must(template.New("status").Funcs(template.FuncMap{
	"clock": func(t time.Time) string { return t.Format("15:04:05") },
	"class": func(s string) string {
		switch {
		case strings.HasPrefix(s, "DIRECT"):
			return "direct"
		case strings.HasPrefix(s, "PROXY"):
			return "proxy"
		case strings.HasPrefix(s, "REJECT"):
			return "reject"
		default:
			return ""
		}
	},
}).Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta http-equiv="refresh" content="5">
<title>MEOW status</title><style>
body{font:14px system-ui,sans-serif;margin:24px;color:#222} table{border-collapse:collapse;width:100%;max-width:1100px}
th,td{padding:6px 9px;border-bottom:1px solid #ddd;text-align:left} code{font-family:ui-monospace,monospace}
.direct{color:#16803a}.proxy{color:#2257c7}.reject{color:#b42318}.muted{color:#666} h2{margin-top:24px}
a{color:#2257c7} .notice{background:#eef6ff;border:1px solid #cfe2ff;padding:8px 12px;border-radius:6px;max-width:1076px}
</style></head><body><h1>MEOW {{.Version}}</h1>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
<p>运行 {{.Uptime}} · DIRECT {{.DirectTotal}} · PROXY {{.ProxyTotal}} · REJECT {{.RejectTotal}} · 请求/响应日志 {{.RequestLog}}/{{.ReplyLog}}</p>
<p class="muted">goroutine {{.Goroutines}} · 已学习分流 {{.LearnedRules}} 条 · 连接池 {{.PoolSites}} 站点/{{.PoolConns}} 直连空闲/{{.PoolMuxConns}} 复用空闲</p>
<p><a href="/status/reload">重新加载 direct/proxy/reject 及中国 IP 列表（并清空已学习分流）</a></p>
<p class="muted">每 5 秒自动刷新；只保留最近 256 条分流和 64 类上游错误。</p>
{{if .Parents}}<h2>父代理</h2><table><tr><th>服务器</th><th>状态</th></tr>
{{range .Parents}}<tr><td><code>{{.Server}}</code></td><td>{{.Detail}}</td></tr>{{end}}</table>{{end}}
<h2>最近分流</h2><table><tr><th>时间</th><th>主机</th><th>结果</th><th>连续次数</th><th></th></tr>
{{range .Routes}}<tr><td>{{clock .At}}</td><td><code>{{.Host}}</code></td><td class="{{class .Route}}">{{.Route}}</td><td>{{.Count}}</td><td><a href="/status/forget?host={{.Host | urlquery}}">忘记</a></td></tr>{{else}}<tr><td colspan="5">暂无记录</td></tr>{{end}}</table>
<h2>上游错误（相同错误聚合）</h2><table><tr><th>最后发生</th><th>目标</th><th>错误</th><th>次数</th></tr>
{{range .Errors}}<tr><td>{{clock .At}}</td><td><code>{{.Target}}</code></td><td>{{.Detail}}</td><td>{{.Count}}</td></tr>{{else}}<tr><td colspan="4">暂无错误</td></tr>{{end}}</table>
</body></html>`))

func isStatusPath(path string) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return path == "/status" || strings.HasPrefix(path, "/status/")
}

func clientIsLoopback(c *clientConn) bool {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	return err == nil && net.ParseIP(host).IsLoopback()
}

func sendRuntimeStatus(c *clientConn, r *Request) error {
	if !clientIsLoopback(c) {
		sendErrorPage(c, statusForbidden, "Status page is local only", "Open this page from the computer running MEOW.")
		return errPageSent
	}
	path, rawQuery, _ := strings.Cut(r.URL.Path, "?")
	notice := ""
	switch path {
	case "/status", "/status/":
	case "/status/forget":
		notice = statusForget(rawQuery)
	case "/status/reload":
		notice = statusReload()
	default:
		sendErrorPage(c, "404 not found", "Unknown status action", path)
		return errPageSent
	}
	return renderRuntimeStatus(c, notice)
}

func statusForget(rawQuery string) string {
	values, _ := url.ParseQuery(rawQuery)
	host := canonicalHost(strings.TrimSpace(values.Get("host")))
	if host == "" {
		return "忘记失败：未指定 host"
	}
	if domainList.forget(host) {
		info.Printf("status: forgot learned route for %s\n", host)
		return "已忘记 " + host + " 的自动学习结果，下次请求将重新判断"
	}
	return host + " 没有自动学习的分流结果"
}

func statusReload() string {
	domainList.reload(config)
	if config.JudgeByIP {
		initCNIPData()
	}
	info.Println("status: reloaded routing lists and cleared learned routes")
	return "已重新加载 direct/proxy/reject 及中国 IP 列表，并清空全部自动学习结果"
}

func renderRuntimeStatus(c *clientConn, notice string) error {
	var body bytes.Buffer
	if err := runtimeStatusTemplate.Execute(&body, snapshotRuntimeStatus(notice)); err != nil {
		return err
	}
	header := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nCache-Control: no-store\r\nConnection: close\r\nContent-Length: %d\r\n\r\n", body.Len())
	if err := writeFull(c, []byte(header)); err != nil {
		return err
	}
	if err := writeFull(c, body.Bytes()); err != nil {
		return err
	}
	return errPageSent
}
