## 更新说明
- 未发布

       * 完整支持 `Expect: 100-continue`：转发 Expect 头给源站，收到 100 后再发请求体；源站直接返回最终响应时转发该响应并关闭连接
       * CONNECT 隧道改用自适应缓冲：空闲隧道用 4 KB，持续满载后升到 32 KB，降低大量长连接的常驻内存
       * `copyN` 在定长响应体正好读完时不再把随最后一段数据一起到达的 EOF 当作截断错误
       * `/status` 新增每条分流的「忘记」链接和顶部「重新加载规则」链接，并显示父代理健康、连接池与 goroutine 数
       * 中国 IP 表改为 gzip 压缩后 `//go:embed` 内嵌（约 30 KB，之前约 170 KB Go 源码），CI 定期更新只产生一行 diff
       * 补充 100-continue、自适应缓冲、`/status` 操作、基础认证和配置解析的测试

- 2026-09-09 Version 1.7.1

       * 自动学习的分流结果只匹配完整主机名，避免影响同一主域名下的其他主机
       * 修正连接池清理阻塞及直连回退过早写入代理缓存的问题
       * 修正固定长度正文截断、HTTP 1xx 临时响应和无斜杠查询 URL 的处理
       * 统一域名大小写和尾点，避免绕过手工分流规则
       * 上游 SOCKS5 与 Shadowsocks 握手支持取消，并设有默认超时
       * 隧道转发缓冲区增至 32 KB，并默认关闭逐请求、逐响应日志
       * 合并同一主机的并发 DNS 查询，修正 latency 负载均衡初始化与失败降级
       * 新增仅本机可访问的 `/status` 运行状态页，集中显示最近分流和聚合错误
       * 精简发布目标，并为 Windows amd64 同时提供控制台版和无窗口 GUI 版
       * Windows GUI 版增加单实例保护，并将关键启动错误写入 `%TEMP%`
       * 加固 HTTP 版本、状态码及 Header 语法校验，拒绝有歧义的报文

- 2026-09-09 Version 1.7.0

       * 中国 IP 数据改用 APNIC 官方来源，同时包含 IPv4 与 IPv6，并可由 CI 定期重新生成
       * IPv6 不再无条件直连，新增 `ipv6Policy` 选项（judge / direct / proxy，默认 judge）
       * 修正中国 IP 网段判断的越界与 off-by-one 错误
       * 域名解析结果按全部地址判断，不再只看第一条记录
       * 自定义 china_ip_list 文件中的错误行不再导致启动崩溃

- 2026-09-09 Version 1.6.1

       * 修正 meow 协议连接写入返回值不符合 io.Writer 约定导致的崩溃与响应体丢失
       * writeFull 遇到异常的 io.Writer 返回错误而非 panic

- 2026-09-08 Version 1.6.0

       * 更新 Go 工具链、依赖、CI 和跨平台构建
       * 加固连接生命周期、HTTP CONNECT 和 HTTP 上级代理处理
       * 改进 IPv6、TLS、SOCKS5、路由规则及调试日志
       * 补充关键协议、连接池、并发和回归测试

- 2016-09-29 Version 1.5

       * 更新中国IP列表

- 2016-02-18 Version 1.3.4

       * 使用 Go 1.6 编译，请重新下载
       
- 2015-12-03 Version 1.3.4

       * 修正客户端连接未正确关闭 bug
       * 修正对文件描述符过多错误的判断（too many open files）

- 2015-11-22 Version 1.3.3

       * 增加 `reject` 拒绝连接列表
       * 支持作为 HTTPS 代理服务器监听
       * 支持 HTTPS 代理服务器作为父代理
	
	
- 2015-10-09 Version 1.3.2

       * 完全托管在 github，不再使用 meowproxy.me 域名，[新的下载地址](https://github.com/renzhn/MEOW/tree/gh-pages/dist/)

- 2015-08-23 Version 1.3.1

       * 去除了端口限制
       * 使用最新的 Go 1.5 编译

- 2015-07-16 Version 1.3

       更新了默认的直连列表、加入了强制使用代理列表，强烈推荐旧版本用户更新 [direct](https://raw.githubusercontent.com/renzhn/MEOW/master/doc/sample-config/direct) 文件和下载 [proxy](https://raw.githubusercontent.com/renzhn/MEOW/master/doc/sample-config/proxy) 文件（或者重新安装）
