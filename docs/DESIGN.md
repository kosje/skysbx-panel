# 设计文档

> 工作目录名暂用 `skysbx2`，正式名字还没定 —— 见文末「待定」。
> 下文用 **panel** 和 **node** 指代两个产物。

## 1. 目标与非目标

**目标**

1. **许可来源干净。** 每一行代码要么是自己写的，要么来自有明确许可的上游。这是这个项目存在的首要理由：现有的 `skysbx` 有 96% 的代码继承自没有 LICENSE 的 `rwnode-gosingbox`，没有分发权。
2. **扔掉 Xray 方言。** 面板直接存 sing-box 原生配置，节点不再做格式翻译。
3. 支持 **VLESS+Reality+vision**、**AnyTLS**、**Shadowsocks 2022**，每个节点全协议。
4. 多节点，用户增删不重启监听器。
5. 运维形态尽量简单。

**非目标**

- 不做 Remnawave 的功能对等。Telegram 通知、webhook、HWID 限制、infra-billing、node-ssh、ASN 共享列表、五种客户端模板系统 —— 都不做。
- 不做多租户。单管理员，自用/小圈子。
- 不追求兼容 Remnawave 的数据或协议。这是全新的东西。

## 2. 许可边界

这是**架构约束**，不是文档脚注。

```
node   内嵌 sing-box（GPL-3.0 + 命名条款）→ 衍生作品 → 必须 GPL-3.0
panel  只通过 HTTP/WebSocket 与 node 通信，不链接它的任何代码 → 许可自选
```

所以：

- **两个独立仓库，两个 LICENSE 文件。** 不是一个仓库两个模块 —— 边界要一眼可见，不留解释空间。
- panel **绝不 import** node 的任何包。协议是线格式，两边各自定义自己的 struct。重复几十行结构体定义，换来一条无争议的边界，很划算。
- node 的 `README` 必须写明：派生自 sing-box，非官方、未获背书（GPL-3.0 §7 的命名条款要求）。

沿用现有的 `skysbx-core`（8 文件补丁，已有 `MODIFICATIONS.md`），它本来就是干净的 GPL-3.0 衍生。

**重写节点时的纪律：** 不看 `rwnode-gosingbox` 的代码写新代码。实现依据是两份规格 —— 本文档的协议章节，和 sing-box 自己的公开 API。行为相同不构成侵权，照抄表达才构成。

## 3. 总体架构

```
                    ┌─────────────────────────────┐
   浏览器 ──https──▶│  panel（单二进制）           │
   订阅客户端 ─────▶│    ├─ 管理 UI（htmx 模板）   │
                    │    ├─ 订阅生成               │
                    │    └─ SQLite                 │
                    └──────────────┬──────────────┘
                                   │ ▲
                    node 主动外连   │ │ 单条 WebSocket
                    （见 §4）      ▼ │
                    ┌─────────────────────────────┐
   客户端流量 ─────▶│  node（单二进制）            │
                    │    └─ 内嵌 sing-box          │
                    └─────────────────────────────┘
```

运维形态：**两个二进制 + 一个 SQLite 文件**。备份 = 拷一个文件。

对比现状（Caddy + panel + subscription-page + PostgreSQL + Valkey，五个容器），这是这次重写最直接的收益之一。

TLS 由 panel 自己用 `certmagic` 处理（ACME），不再需要前置 Caddy。

## 4. 面板↔节点协议

### 4.1 最重要的一个决定：反转连接方向

现状是 **panel 主动连 node**（node 监听 2222，mTLS + JWT）。这带来一串代价：

- node 必须有公网可达的入站端口
- node 必须有证书，panel 要下发 `SECRET_KEY`，还要有 keygen 那一套
- node 在 NAT 后面就用不了
- panel 必须知道每个 node 的可达地址

新协议**反过来**：**node 主动连 panel，一条长连接**。

| | 旧（panel → node） | 新（node → panel） |
|---|---|---|
| node 入站端口 | 2222，必须开放 | **不需要** |
| 控制面证书 | mTLS，双向证书 | **不需要**，panel 侧 TLS + Bearer token |
| NAT 后面的 node | 不行 | 可以 |
| 断线重连 | panel 轮询重试 | node 自己重连，天然有韧性 |
| 端点数量 | 27 个 HTTP 端点 | **1 个** WebSocket |

node 仍然需要**自己域名的 TLS 证书** —— 但那是给 AnyTLS 入站用的，跟控制面无关。Reality 和 SS2022 不需要证书。

### 4.2 接入

```
GET /api/v1/node/connect
Upgrade: websocket
Authorization: Bearer <node-token>
```

`node-token` 在 panel 里建节点时生成，一次性显示，只存哈希。没有证书交换、没有密钥派生。

### 4.3 消息封装

```json
{ "t": "<type>", "id": 42, "d": { ... } }
```

- `t` 消息类型
- `id` 关联 id。请求带 id，回应用同一个 id 回 `ok` / `error`
- `d` 载荷

单向通知（`stats`、`online`、`state`）不带 `id`。

### 4.4 消息类型

**node → panel**

| `t` | 时机 | 载荷 |
|---|---|---|
| `hello` | 连上立刻 | `{version, os, arch, hostname, singbox_version}` |
| `ok` / `error` | 回应 panel 的指令 | `{id, msg?}` |
| `stats` | 每 30s | 见 §7 |
| `online` | 每 30s | `{users: ["alice", "bob"]}` |
| `state` | 每次 apply 之后 | `{inbounds: ["ss-tokyo"], error?}` |
| `pong` | 回应 ping | — |

`error` 说的是「这条指令我没执行」，`state` 说的是「那我现在跑的是什么」。两者都需要：
节点拒绝一份配置之后仍在跑上一份，面板这边入站照样显示「启用」（操作者确实是这么
要求的），唯一的症状是客户端连不上那一个端口。`state.inbounds` 由节点直接给出，不
从错误消息里反解 tag —— 那是猜的，而且 sing-box 改一次措辞就错。

**panel → node**

| `t` | 时机 | 载荷 |
|---|---|---|
| `config` | 连上之后、配置变更时 | 完整 sing-box 配置（`users` 留空） |
| `users` | 用户增删改 | `{"<inbound-tag>": [用户...]}` |
| `ping` | 每 30s | — |

只有三条下行指令。对比现在的 27 个端点。

### 4.5 配置与用户分离

这是从这次实战里学到的最重要一条：**配置很少变，用户经常变**。混在一起意味着每次加用户都要重建监听器。

- `config` 携带 sing-box 配置，每个入站的 `users` 是空数组
- `users` 携带权威用户列表，按入站 tag 分组
- node 把两者合并后应用；只收到 `users` 时走热插拔，不重启监听器

```json
// panel → node : config
{ "t": "config", "id": 1, "d": {
  "log": { "level": "warn" },
  "inbounds": [
    { "type": "vless", "tag": "vless-tokyo", "listen": "::", "listen_port": 443,
      "users": [],
      "tls": { "enabled": true, "server_name": "www.microsoft.com",
               "reality": { "enabled": true,
                            "handshake": { "server": "www.microsoft.com", "server_port": 443 },
                            "private_key": "<base64url，无填充>", "short_id": ["a1b2c3d4"] } } }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ]
} }
```

```json
// panel → node : users
{ "t": "users", "id": 2, "d": {
  "vless-tokyo":  [ { "name": "alice", "uuid": "…", "flow": "xtls-rprx-vision" } ],
  "anytls-tokyo": [ { "name": "alice", "password": "…" } ],
  "ss-tokyo":     [ { "name": "alice", "password": "<base64(32字节)>" } ]
} }
```

`name` 是**计费主键**，全局唯一，同一个用户在所有入站上用同一个 `name`。

## 5. 数据模型

SQLite。全部 schema：

```sql
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,      -- 计费主键，也是 sing-box 里的用户名
  vless_uuid    TEXT NOT NULL,             -- VLESS
  password      TEXT NOT NULL,             -- AnyTLS/Trojan 明文口令
  ss_password   TEXT NOT NULL,             -- SS2022 用户 PSK，base64(32 字节)
  sub_token     TEXT NOT NULL UNIQUE,      -- 订阅 URL 里那一段
  enabled       INTEGER NOT NULL DEFAULT 1,
  expires_at    INTEGER,                   -- unix 秒，NULL = 永不过期
  traffic_limit INTEGER NOT NULL DEFAULT 0,-- 字节，0 = 不限
  traffic_used  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);

CREATE TABLE nodes (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  token_hash    TEXT NOT NULL,             -- 接入 token 只存哈希
  address       TEXT NOT NULL,             -- 客户端连的地址（域名或 IP）
  country       TEXT NOT NULL DEFAULT 'XX',
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_seen_at  INTEGER,
  version       TEXT,
  created_at    INTEGER NOT NULL
);

CREATE TABLE inbounds (
  id            INTEGER PRIMARY KEY,
  node_id       INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  tag           TEXT NOT NULL,             -- sing-box inbound tag，全局唯一
  protocol      TEXT NOT NULL,             -- vless | anytls | shadowsocks
  port          INTEGER NOT NULL,
  config        TEXT NOT NULL,             -- sing-box 入站 JSON（users 为空）
  client        TEXT NOT NULL,             -- 客户端侧参数（见下），JSON
  enabled       INTEGER NOT NULL DEFAULT 1,
  UNIQUE(tag)
);

-- 用户能用哪些入站。空 = 全部（自用场景的默认）
CREATE TABLE user_inbounds (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  inbound_id INTEGER NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, inbound_id)
);

-- 计费明细，按天聚合，便于出图和清理
CREATE TABLE traffic (
  user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  day      INTEGER NOT NULL,               -- unix 天
  up       INTEGER NOT NULL DEFAULT 0,
  down     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, node_id, day)
);

CREATE TABLE settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);
```

### 5.1 为什么 `inbounds` 有 `config` 和 `client` 两列

**这是这次实战里最贵的一课。** Host（订阅指向的东西）绑定的是**入站**而不是节点，所以多个节点共用一个 tag 时，订阅里只会出现一个地址。新模型直接把 `inbounds.node_id` 设成外键 —— **一个入站属于且只属于一个节点**，结构上就不可能犯这个错。

- `config` = 服务端配置，原样发给 node
- `client` = 客户端连接参数，服务端不用但订阅要用

```json
// client 列，以 Reality 为例
{ "sni": "www.microsoft.com", "pbk": "<公钥>", "sid": "a1b2c3d4",
  "fp": "chrome", "flow": "xtls-rprx-vision" }
```

Reality 公钥由 panel 在**建入站时**从私钥算出来存进 `client`，不在每次生成订阅时重算。

## 6. 配置下发与用户热插拔

```
改配置（加入站、改端口）  →  panel 发 config  →  node 重建监听器（会断连）
改用户（增删改、到期）    →  panel 发 users   →  node 热插拔（不断连）
```

panel 侧用一个 100ms 的合并窗口，批量用户变更只发一次 `users`。

**node 侧必须处理的三件事**（全部来自这次实战，不是假想）：

1. **SS 入站永远至少要有一个用户。** sing-box 在启动时按 `len(users)` 决定建单用户还是多用户监听器，零用户会建成单用户型 —— 那个类型没有 `UpdateUsersByOptions`，之后所有热添加静默失效。而且单用户模式下配置里的 `password` 本身就是完整凭据，不属于任何用户，那部分流量不计费。**对策：** 用户为空时塞一个随机密钥的占位用户。

2. **用户名快照必须原子发布并做边界检查。** VLESS 和 SS 用**下标**在连接路径上查用户名，下标由 service 发放、与用户切片分别更新，删用户时长度对不上会索引越界打崩节点。**对策：** `atomic.Pointer` 快照 + 越界返回空串。（这条已在 `skysbx-core` 里修好，重写节点时照抄自己的补丁即可。）

3. **热插拔后必须同步统计白名单。** v2ray_api 的 stats service 在配置加载时建立用户白名单，热加的用户不在里面 → 能连通、能跑流量、**永远不计费**。**对策：** 每次用户变更后调 `UpdateUsers` 刷新白名单。

**证书：** sing-box 只在启动时读证书。证书轮换后必须重启监听器，不能只换文件。

## 7. 流量计费

node 每 30s 从内嵌的 v2ray_api 读一次计数器，算出**增量**上报：

```json
{ "t": "stats", "d": {
  "users": { "alice": { "up": 1048576, "down": 20971520 } },
  "system": { "cpu": 3.1, "mem_used": 214958080, "uptime": 86400 }
} }
```

- 上报**增量**而不是累计值。节点重启时计数器归零，累计值会产生倒退；增量模型天然免疫。
- panel 收到后 `UPDATE users SET traffic_used = traffic_used + ?` 并写入 `traffic` 当天行。
- 超限或过期的用户，panel 在下一次 `users` 里直接不包含它 —— 不需要额外的"禁用"指令。

**已知误差：** AnyTLS 复用会话，删用户后已建立的连接要等空闲窗口（< 90 秒）才断。另外两个协议是即时的。

## 8. 订阅生成

```
GET /sub/<sub_token>
```

按请求头分发格式（依据 `User-Agent`，浏览器额外看 `Accept`）：

| 客户端 | 判定 | 返回 |
|---|---|---|
| 浏览器 | `Accept` 含 `text/html` | 订阅页（用量/到期/一键导入） |
| sing-box / SFA / SFI | `User-Agent` | sing-box JSON |
| mihomo / Clash | `User-Agent` | Clash YAML |
| 其它 | 兜底 | base64 分享链接 |

> 用 curl 自测时注意：浏览器规则匹配的是 **`Accept` 头**，只带 `User-Agent: Mozilla/…` 命中不了。

三个协议的分享链接格式（已在实战中验证可用）：

```
vless://<uuid>@<addr>:<port>?encryption=none&flow=xtls-rprx-vision&type=tcp
        &security=reality&sni=<sni>&fp=chrome&pbk=<公钥>&sid=<shortid>#<备注>

anytls://<password>@<addr>:<port>?security=tls&sni=<addr>&fp=chrome#<备注>

ss://base64("2022-blake3-aes-256-gcm:<服务端PSK>:<用户PSK>")@<addr>:<port>#<备注>
```

**AnyTLS 不要输出 `multiplex` / `smux`** —— 它本身就是多路复用协议，加了反而出错。

订阅内容 = 该用户 × 所有启用且节点在线的入站。每个入站一条。

## 9. 目录结构

**仓库一：panel**（许可自选）

```
cmd/panel/main.go
internal/
  store/          SQLite：schema、迁移、查询
  hub/            WebSocket 集线器：节点连接、消息路由、心跳
  api/            管理 API（给 UI 用）
  sub/            订阅生成：分享链接 / sing-box / clash
  singbox/        sing-box 配置结构体（只是 JSON 结构，不 import sing-box）
  web/            htmx 模板 + 静态资源，go:embed
  acme/           certmagic 封装
migrations/
LICENSE
```

**仓库二：node**（GPL-3.0）

```
cmd/node/main.go
internal/
  link/           连 panel 的 WebSocket 客户端，含重连
  engine/         内嵌 sing-box：应用配置、热插拔、统计
  stats/          v2ray_api 读数、增量计算
LICENSE            GPL-3.0
MODIFICATIONS.md   sing-box 补丁说明（沿用现有的）
NOTICE             命名条款要求的声明
```

`internal/singbox/` 里只放 JSON 结构体定义 —— panel 需要生成 sing-box 配置，但**绝不能 import sing-box 本体**，否则许可边界就破了。这是一条硬规则，值得在 CI 里加一条检查。

这条边界有代价，而且已经付过一次了：panel 没法拿 sing-box 的 schema 去校验自己生成的配置。2026-09-05 那次，订阅里的 DNS 块用的还是 1.12 之前的写法（`"address": "https://1.1.1.1/dns-query"`），1.14 已经直接拒收；同时缺 `route.default_domain_resolver`，还把 local DNS 的 detour 指到了空的 direct 出站 —— 三条都让客户端起不来，而 16 个订阅测试全绿。测试只能把字段形状钉死，钉不住上游下一次改名。**唯一真正的验证是拿真的 sing-box 跑一遍生成的配置**（`sing-box check -c`），这一步得留在部署验证里，进不了 panel 的单元测试。

## 10. 从这次实战里带出来的约束清单

写代码时会反复用到，集中列在这里：

| 约束 | 说明 |
|---|---|
| SS2022 只能用 `2022-blake3-aes-256-gcm` | 服务端 PSK 和用户 PSK 都是 base64(32 字节) |
| Reality 私钥 base64url **无填充** | sing-box 用 `base64.RawURLEncoding` 解码，带 `=` 直接失败 |
| Reality 公钥由私钥推导 | 建入站时算一次存起来 |
| `network` 用 `raw` 不是 `tcp` | 新版 Xray 的叫法；本项目用 sing-box 原生格式，这条只在导出 xray-json 时相关 |
| Go 必须 1.26.x | 1.27 链接失败：`http2.(*Transport).connPool not defined` |
| 构建 tag 不可省 | `with_clash_api,with_v2ray_api,with_utls,with_acme,with_quic`，缺了能编译但启动即死 |
| `-race` 需要 `-gcflags=all=-d=checkptr=0` | sing-box 自己的 unsafe 运算会触发 checkptr |
| 面板机的 443 归 panel | 该机上的 Reality 要退到别的端口 |
| 节点域名必须灰云 | 三个协议都不是 HTTP，套 CDN 全挂 |

## 11. 里程碑

| # | 内容 | 产出 |
|---|---|---|
| M1 | panel 骨架：SQLite、schema、管理 API、htmx 建用户/建节点 | 能建数据，还没有流量 |
| M2 | WebSocket hub + 一个假 node，跑通 `config`/`users`/`stats` | 协议验证 |
| M3 | 订阅生成三种格式 | 能拿到订阅链接 |
| M4 | node 重写：link + engine，接上真 sing-box | **端到端三协议连通** |
| M5 | 计费闭环、到期/超限、在线用户 | 可用 |
| M6 | ACME、一键安装脚本、多节点验证 | 可部署 |

M4 是真正的验证点 —— 到那一步为止，现有的 `skysbx` 部署继续跑，不会有停机。

## 12. 待定

1. **项目名。** `skysbx` 是现有的；新项目沿用还是另起？两个仓库的名字也一并定（如 `xxx-panel` / `xxx-node`）。
2. **panel 的许可。** 你可以自选。MIT / Apache-2.0 / AGPL-3.0 / 不开源，都行。
3. **管理 UI 的形态。** 本文假设 htmx + Go 模板。若你更想要 React SPA，M1 的工作量会显著变大。
