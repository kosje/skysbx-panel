# skysbx-panel

代理面板的控制端：用户、节点、入站、订阅、计费、用量控制。

节点端是独立的 [`skysbx-node`](https://github.com/kosje/skysbx-node)，两者通过一条
WebSocket 通信。设计见 [`docs/DESIGN.md`](docs/DESIGN.md)。

```
一个二进制 + 一个 SQLite 文件
```

TLS 由面板自己用 ACME 处理，不需要前置反向代理。备份就是拷一个文件。

## 功能

**协议** —— VLESS + Reality + XTLS-Vision、AnyTLS、Shadowsocks 2022。面板直接存
sing-box 原生配置，不做格式转译。密钥、short id、SS 服务端 PSK 全部自动生成。三个里
只有 AnyTLS 需要证书，所以没证书的节点照样跑另外两个。

**用户** —— 到期时间、流量上限、同时在线 IP 上限、备注，全部可编辑。可以按用户指定
能用哪些入站（不是把所有协议都发给所有人）。流量清零是单独的操作，编辑表单不会覆盖
已用流量。

**每月流量重置** —— 月付套餐用的：选一个日子（或「按创建日」），到那天已用流量自动
归零，不用每月手动清。短月自动落到月底，二月不会被跳过；面板在重置日当天没开机的话，
下次启动补上而不是丢掉这个月。归零的只是计数器，历史曲线和按节点的流量账不受影响。

**订阅** —— 一个链接，按客户端自动给 sing-box JSON / Clash YAML / base64 分享链接，
浏览器打开则是一个带用量和一键导入的页面。客户端服务器列表里显示的别名是
`节点 | 用户名 | 已用/总量 | 到期`。

**节点** —— 节点主动外连面板，不需要开放控制端口，NAT 后面可用。接入 token 一次性
显示、只存哈希、可随时更换。改节点名会自动同步该节点上所有入站的 tag。

**生效确认** —— 新建或改动入站后，页面自己轮询到节点确认为止，四种状态：已生效 /
确认中 / 未生效（附节点报回的原始错误）/ 节点离线。节点拒绝一份配置时会自动回滚到
上一份，不会因为一个打错的端口让整台机器下线。

**中转** —— 两种。**外部中转**填一个面板管不到的中转机地址（realm、nginx stream 之
类）。**站内中转**选一台面板已经管着的节点，面板自动在它上面开一个 L4 转发口，订阅
地址随之改变；中转机只搬字节，协议仍在落地节点终结，所以按用户计流量、IP 限制、活动
记录全部不受影响。

**用量控制** —— 每用户同时在线 IP 上限（在节点上执行，超出的地址直接断开，先连上的
不受影响）；面板级路由策略：禁 BitTorrent、禁测速站、自定义域名黑名单。

**监控** —— 概览页按节点分列流量和 14 天曲线；每个用户一个活动页，按小时记录连接数、
对端数、端口数、来源地址数的峰值，保留 30 天。只记形状不记去处 —— 分辨滥用靠的是
形状，而不是某个人访问了什么。

## 安装

### 面板

```bash
wget -qO- https://raw.githubusercontent.com/kosje/skysbx-panel/main/install-panel-and-node.sh
```

不带参数就是交互式，会问域名。带参数要加 `-s --`：

```bash
P=https://raw.githubusercontent.com/kosje/skysbx-panel/main/install.sh

wget -qO- $P | sh -s -- --domain panel.example.com --email you@example.com
wget -qO- $P | sh -s -- --version      # 装的是哪个版本（也用来看 CDN 是否还在缓存旧版）
wget -qO- $P | sh -s -- --upgrade      # 重新构建并重启，数据库不动
wget -qO- $P | sh -s -- --uninstall    # 卸载服务，保留数据库和证书
wget -qO- $P | sh -s -- --purge        # 连数据库和证书一起删，不可恢复
```

等价的手动方式：

```bash
git clone https://github.com/kosje/skysbx-panel.git
cd skysbx-panel
sudo ./deploy/install-panel.sh --domain panel.example.com --email you@example.com
```

需要 `80` 和 `443` 空闲 —— 面板自己终止 TLS、自己应答 ACME 挑战，**没有反向代理要装、
要配、要保持同步**。域名必须已解析到本机且未套 CDN（HTTP-01 挑战要直连）。

**管理员在安装时就问，是必填项。** 脚本在启动服务之前把它写进数据库，所以面板从第一
秒起就是有主的 —— 不存在「装完到建管理员之间谁先打开谁就是管理员」的窗口。密码走
stdin 交给二进制，不会出现在进程列表或 shell 历史里。装完直接
`https://panel.example.com/login` 登录。

没有终端可问的话（CI、`wget | sh` 且没有 tty），用环境变量代替，脚本不会静默跳过：

```bash
SKYSBX_ADMIN_USER=admin SKYSBX_ADMIN_PASSWORD='...' \
  ./deploy/install-panel.sh --domain panel.example.com
```

忘了密码：

```bash
/opt/skysbx/skysbx-panel -db /opt/skysbx/skysbx.db -set-admin <用户名>
# 然后输入新密码（从 stdin 读，不回显也不进 argv）
```

升级不需要停机以外的动作，数据库迁移在启动时自动跑，也不会再问管理员。

### 节点

在面板里 **节点 → 新增**，复制那个只显示一次的接入 token，然后在新服务器上：

```bash
wget -qO- https://raw.githubusercontent.com/kosje/skysbx-node/main/install.sh | sh
```

它会问面板地址和 token。带参数同样加 `-s --`：

```bash
N=https://raw.githubusercontent.com/kosje/skysbx-node/main/install.sh

wget -qO- $N | sh -s -- --panel https://panel.example.com --token <token>
wget -qO- $N | sh -s -- --version      # 节点版本 + 内嵌的 sing-box 版本
wget -qO- $N | sh -s -- --upgrade      # 重新构建并重启，含 sing-box 核心升级
wget -qO- $N | sh -s -- --uninstall    # 卸载服务，保留证书和 node.env
wget -qO- $N | sh -s -- --purge        # 连证书、构建缓存、脚本装的 Docker 一起清掉
```

**sing-box 核心怎么升级：** 节点把 sing-box 编进自己二进制里，所以 `--upgrade` 重新
构建一次就是升级 —— 它会重新拉 [`skysbx-core`](https://github.com/kosje/skysbx-core)
再编。没有单独的核心版本要管，也没有第二个进程要重启。

节点**主动连面板**，所以它不需要开放任何控制端口、不需要面板能路由到它，NAT 后面也
能用。

`--domain` 是可选的：只有 AnyTLS 需要证书。给了域名脚本就用 certbot 签
（`--cf-token` 可走 DNS-01）；不给就只跑另外两个协议。

> 节点域名必须是 **DNS-only（灰云）**。三个协议都不是 HTTP，套 CDN 会全部失效。

### 同机安装面板和节点

一台服务器同时跑面板和节点时，使用下面的一键命令。它会先安装面板；面板上线后，在网页的
**节点 → 新增**创建节点并复制一次性接入 token，回到终端粘贴即可继续安装节点：

```bash
wget -qO- https://raw.githubusercontent.com/kosje/skysbx-panel/main/install-panel-and-node.sh | \
  sudo sh -s -- --domain panel.example.com --email you@example.com
```

非交互环境可直接提供 token。`--panel` 默认是 `https://<面板域名>`；只有该节点要使用
AnyTLS 时才需要 `--node-domain`（以及可选的 `--cf-token`）：

```bash
I=https://raw.githubusercontent.com/kosje/skysbx-panel/main/install-panel-and-node.sh
wget -qO- "$I" | sudo sh -s -- \
  --domain panel.example.com --token '<node-token>' \
  --node-domain node.example.com
```

同机运行时，面板占用 `80` 和 `443`；为该节点新建 Reality 入站时请选择其他端口。节点仍然
通过 WebSocket 主动连接面板，不会额外开放控制端口。

### 离线 / 自建镜像

`--src`（面板或节点源码）和 `--fork`（打过补丁的 sing-box）可以指向本机已有的检出，
完全不联网安装。仓库设为私有的话，`export GITHUB_TOKEN=...` 后再跑；token 走
per-command header，不会落到 `.git/config` 里。

单组件一键脚本认这几个环境变量：`SKYSBX_REPO`、`SKYSBX_FORK`、`SKYSBX_REF`。同机脚本
还可用 `SKYSBX_NODE_REPO` 和 `SKYSBX_NODE_REF` 分别指定节点源码和分支。

## 开发

```bash
go test ./...
go build ./cmd/panel
```

需要 Go 1.27+。节点那边锁 Go 1.26.x（sing-box 的 `go:linkname` 在 1.27 下链接失败），
面板不受这个限制。

```
cmd/panel/          入口
internal/
  store/            SQLite：schema、迁移、全部查询   ← 上面没有任何地方写 SQL
  service/          业务逻辑，不知道 HTTP 的存在     ← 换 UI 时的切换点
  hub/              WebSocket 集线器
  sub/              订阅生成
  singbox/          sing-box 配置的 Go 结构体
  web/              htmx handler + 模板
docs/DESIGN.md      协议、数据模型、订阅、计费、中转、已知限制
```

`service/` 刻意不感知 HTTP：以后若要把 htmx 换成 SPA，只需在同一套 service 上加一层
JSON API，数据模型、协议、订阅、计费都不用动。

面板生成 sing-box 配置，但 `internal/singbox/` 里只有 JSON 结构体定义，**不 import
sing-box 本体**。代价是面板没法用 sing-box 的 schema 校验自己的输出 —— 真正的验证得
拿真的 sing-box 跑一遍，那一步在部署验证里做。

## 许可

**AGPL-3.0**，见 [`LICENSE`](LICENSE)。节点仓库是 GPL-3.0。
