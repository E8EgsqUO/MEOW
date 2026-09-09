# MEOW Proxy

当前版本：1.7.0 [CHANGELOG](CHANGELOG.md)

<pre>
       /\
   )  ( ')     MEOW 是 [COW](https://github.com/cyfdecyf/cow) 的一个派生版本
  (  /  )      MEOW 与 COW 最大的不同之处在于，COW 采用黑名单模式， 而 MEOW 采用白名单模式
   \(__)|      国内网站直接连接，其他的网站使用代理连接
</pre>

## 与原版MEOW的差别

* 本代码仓库删除了编译好的二进制文件，大大减少了git clone时的传输大小
* IPv6 与 IPv4 一样按中国地址段判断分流（可用 `ipv6Policy = direct` 恢复旧的一律直连行为，教育网用户可能需要）

## MEOW 可以用来
- 作为全局 HTTP 代理（支持 PAC），可以智能分流（直连国内网站、使用代理连接其他网站）
- 将 SOCKS5 等代理转换为 HTTP 代理，HTTP 代理能最大程度兼容各种软件（可以设置为程序代理）和设备（设置为系统全局代理）
- 架设在内网（或者公网），为其他设备提供智能分流代理
- 编译成一个无需任何依赖的可执行文件运行，支持各种平台（Win / Linux / OS X），甚至是树莓派（Linux ARM）

## 获取

- **从源码构建：** 安装 Go 1.26 或更高版本，然后执行：

      git clone https://github.com/E8EgsqUO/MEOW.git
      cd MEOW
      go build -trimpath -o meow .

- **运行测试：**

      go test ./...
      go vet ./...
      go test -race ./...

- **交叉编译所有平台：** 一次产出 Linux（amd64 / arm64 / armv7）、macOS（arm64 /
  amd64）和 Windows（amd64 / arm64）的可执行文件：

      ./script/build-release.sh

  单独构建某个平台，例如 Windows x64：

      CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
        -trimpath -buildvcs=false -ldflags="-s -w" \
        -o MEOW-windows-amd64.exe .

  `-s -w` 会移除符号表和 DWARF 调试信息；Go 编译器的正常优化保持开启。

- **Docker：**

      docker build -t meow .
      docker run --rm -v "$HOME/.meow:/config:ro" meow -rc /config/rc

## 开机自启

- **Linux (systemd)：** 参考 [doc/meow.service](doc/meow.service)，文件开头有安装步骤
- **macOS (launchd)：** 参考 [doc/osx/net.ohrz.meow.plist](doc/osx/net.ohrz.meow.plist)，
  把其中的 `MEOWBINARY` 换成可执行文件的绝对路径，放进 `~/Library/LaunchAgents/`
- **Windows：** `script/meow-taskbar.exe` 是一个托盘启动器，与 `MEOW.exe` 放在同一
  目录即可，详见 [script/README.md](script/README.md)

## 配置

编辑 `~/.meow/rc` (OS X, Linux) 或 `rc.txt` (Windows)，例子：

    # 监听地址，设为0.0.0.0可以监听所有端口，共享给局域网使用
    listen = http://127.0.0.1:4411
    # 至少指定一个上级代理
    # SOCKS5 上级代理
    # proxy = socks5://127.0.0.1:1080
    # HTTP 上级代理
    # proxy = http://127.0.0.1:8087
    # shadowsocks 上级代理
    # proxy = ss://aes-128-cfb:password@example.server.com:25
    # HTTPS 上级代理
    # proxy = https://user:password@example.server.com:port

    # HTTPS 上游代理默认校验证书；仅为兼容旧的自签名部署时才开启
    # proxyTLSInsecureSkipVerify = true

## 工作方式

当 MEOW 启动时会从配置文件加载直连列表和强制使用代理列表，详见下面两节。

当通过 MEOW 访问一个网站时，MEOW 会：

- 检查域名是否在直连列表中，如果在则直连
- 检查域名是否在强制使用代理列表中，如果在则通过代理连接
- **检查域名的 IP 是否为国内 IP**
    - 通过本地 DNS 解析域名，得到域名的全部 IP
    - 全部都是国内 IP 才直连，否则通过代理连接
    - 将域名加入临时的直连或者强制使用代理列表，下次可以不用 DNS 解析直接判断域名是否直连

国内 IP 地址段来自 [APNIC 的地址分配数据](https://ftp.apnic.net/stats/apnic/delegated-apnic-latest)，
编译进可执行文件，**运行时不需要联网更新，也不需要订阅任何规则**。想换用自己维护的列表，
把 CIDR 写进 `~/.meow/china_ip_list`（Windows 为 `china_ip_list.txt`）即可，IPv4 与 IPv6 都支持。

开发时用 `./script/update-chinaip.sh` 重新生成内置数据，仓库的 CI 每月也会自动提一个更新 PR。

## 直连列表

直接连接的域名列表保存在 `~/.meow/direct` (OS X, Linux) 或 `direct.txt` (Windows)

匹配域名**按 . 分隔的后两部分**或者**整个域名**，例子：

-  `baidu.com` => `*.baidu.com`
-  `com.cn` => `*.com.cn`
-  `edu.cn` => `*.edu.cn`
-  `music.163.com` => `music.163.com`

一般是**确定**要直接连接的网站

## 强制使用代理列表

强制使用代理连接的域名列表保存在 `~/.meow/proxy` (OS X, Linux) 或 `proxy.txt` (Windows)，语法格式与直连列表相同
（注意：匹配的是域名**按 . 分隔的后两部分**或者**整个域名**）

## 致谢

- @cyfdecyf - COW author
- @renzhn - Original MEOW author
- Github - Github Student Pack
- https://www.pandafan.org/pac/index.html - Domain White List
- https://github.com/Leask/Flora_Pac - CN IP Data
- https://github.com/17mon/china_ip_list - CN IP Data
