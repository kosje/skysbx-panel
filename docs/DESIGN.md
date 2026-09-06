# skysbx 设计文档

面板 **skysbx-panel** 和节点 **skysbx-node** 是两个独立的程序，通过一条 WebSocket
通信。下文用 **panel** 和 **node** 指代它们。

内嵌的 sing-box 来自 [`skysbx-core`](https://github.com/kosje/skysbx-core)，一个带
少量补丁的分支，补丁内容见那边的 `MODIFICATIONS.md`。

---

## 1. 总体架构

```
                    ┌─────────────────────────────┐
   浏览器 ──https──▶│  panel（单二进制）           │
   订阅客户端 ─────▶│    ├─ 管理 UI（htmx + 模板） │
                    │    ├─ 订阅生成               │
                    │    ├─ 计费与用量记录         │
                    │    └─ SQLite                 │
                    └──────────────┬──────────────┘
                                   │ ▲
                    node 主动外连   │ │ 单条 WebSocket
                                   ▼ │
                    ┌─────────────────────────────┐
   客户端流量 ─────▶│  node（单二进制）            │
                    │    └─ 内嵌 sing-box          │
                    └─────────────────────────────┘
```

运维形态是**两个二进制 + 一个 SQLite 文件**。备份等于拷一个文件。TLS 由 panel 自己
用 `certmagic` 走 ACME 处理，不需要前置反向代理。

支持三个协议，每个入站一种：

| 协议 | 认证 | 需要证书 |
|---|---|---|
| VLESS + Reality + XTLS-Vision | Reality 密钥对 + UUID | 否 |
| AnyTLS | 口令 | **是** |
| Shadowsocks 2022 | `2022-blake3-aes-256-gcm`，双段 PSK | 否 |

所以一台没有证书的节点照样能跑其中两个。

---

## 2. 面板↔节点协议

### 2.1 连接方向是反的

**node 主动连 panel**，不是反过来。代价很小，收益很具体：

| | node 监听（常见做法） | node 外连（本项目） |
|---|---|---|
| node 控制端口 | 必须开放 | **不需要** |
| 控制面证书 | mTLS 双向 | **不需要**，panel 侧 TLS + Bearer token |
| NAT / 防火墙后的 node | 不行 | 可以 |
| 断线重连 | panel 轮询重试 | node 自己重连 |
| 端点数量 | 每个操作一个 | **1 个** WebSocket |

node 仍可能需要自己域名的 TLS 证书 —— 但那是给 AnyTLS 入站用的，与控制面无关。

### 2.2 接入

```
GET /api/v1/node/connect
Upgrade: websocket
Authorization: Bearer <node-token>
```

`node-token` 在 panel 建节点时生成，**一次性显示，只存哈希**。没有证书交换，没有密钥
派生。token 可以随时在节点页「换 token」。存的是 SHA-256 而不是 bcrypt，理由见 §11.1
—— 那不是省事，是一个未认证端点上的拒绝服务。

### 2.3 消息封装

```json
{ "t": "<type>", "id": 42, "d": { ... } }
```

`id` 用于关联请求与回应；单向上报（`stats`、`online`、`state`）不带 `id`。

### 2.4 消息类型

**node → panel**

| `t` | 时机 | 载荷 |
|---|---|---|
| `hello` | 连上立刻 | `{version, os, arch, hostname, singbox_version}` |
| `ok` / `error` | 回应指令 | `{id, msg?}` |
| `stats` | 每 30s | 流量增量 + 系统指标，见 §7 |
| `online` | 每 30s | 在线用户、每人的来源地址数、活动形状，见 §8 |
| `state` | 每次 apply 之后 | `{inbounds: [...], error?}` |
| `pong` | 回应 ping | — |

**panel → node**

| `t` | 时机 | 载荷 |
|---|---|---|
| `config` | 连上之后、配置变更时 | 完整 sing-box 配置（各入站 `users` 留空） |
| `users` | 用户增删改 | 按入站 tag 分组的用户表 + 统计白名单 + IP 上限 |
| `ping` | 每 30s | — |

只有三条下行指令。

`error` 说的是「这条指令我没执行」，`state` 说的是「那我现在跑的是什么」。**两者都
需要**：节点拒绝一份配置之后仍在跑上一份，面板这边入站照样显示「启用」（操作者确实
是这么要求的），唯一的症状是客户端连不上那一个端口。`state.inbounds` 由节点直接给
出，不从错误消息里反解 tag —— 那是猜的，sing-box 改一次措辞就错。

### 2.5 配置与用户分离

**配置很少变，用户经常变。** 混在一起意味着每次加用户都要重建监听器、断掉所有连接。

```
改配置（加入站、改端口、改策略）→ panel 发 config → node 重建监听器（会断连）
改用户（增删改、到期、超限）    → panel 发 users  → node 热插拔（不断连）
```

`config` 里每个入站的 `users` 是空数组；权威用户表走 `users` 消息，按 tag 分组。
panel 侧有一个 100ms 的合并窗口，批量用户变更只发一次。

```json
// panel → node : config（节选）
{ "t": "config", "id": 1, "d": { "config": {
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [
    { "type": "vless", "tag": "vless-tokyo", "listen": "::", "listen_port": 443,
      "tls": { "enabled": true, "server_name": "www.microsoft.com",
               "reality": { "enabled": true,
                 "handshake": { "server": "www.microsoft.com", "server_port": 443 },
                 "private_key": "<base64url，无填充>", "short_id": ["a1b2c3d4"] } } }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "experimental": { "clash_api": {...}, "v2ray_api": {...} }
} } }
```

```json
// panel → node : users
{ "t": "users", "id": 2, "d": {
  "by_tag": {
    "vless-tokyo": [ { "name": "alice", "uuid": "…", "flow": "xtls-rprx-vision" } ],
    "ss-tokyo":    [ { "name": "alice", "password": "<base64(32字节)>" } ]
  },
  "stats_users": ["alice"],
  "ip_limits":   { "alice": 3 }
} }
```

`name` 是**计费主键**，全局唯一，同一个用户在所有入站上用同一个 `name`。

---

## 3. 数据模型

SQLite，迁移在 `internal/store/migrations/`，按文件名顺序各自一个事务。当前 schema：

```sql
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,       -- 计费主键，也是 sing-box 里的用户名
  vless_uuid    TEXT NOT NULL,
  password      TEXT NOT NULL,              -- AnyTLS 明文口令
  ss_password   TEXT NOT NULL,              -- SS2022 用户 PSK，base64(32 字节)
  sub_token     TEXT NOT NULL UNIQUE,       -- 订阅 URL 里那一段
  enabled       INTEGER NOT NULL DEFAULT 1,
  expires_at    INTEGER,                    -- unix 秒，NULL = 永不过期
  traffic_limit INTEGER NOT NULL DEFAULT 0, -- 字节，0 = 不限
  traffic_used  INTEGER NOT NULL DEFAULT 0,
  ip_limit      INTEGER NOT NULL DEFAULT 0, -- 同时在线来源地址数，0 = 不限
  reset_day     INTEGER NOT NULL DEFAULT 0, -- 每月流量重置日，0 = 不重置，见 §8.4
  last_reset_at INTEGER,                    -- 上次归零时间（含手动清零）
  note          TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL
);

CREATE TABLE nodes (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  token_hash    TEXT NOT NULL,              -- 接入 token 的 bcrypt，历史遗留，见 §11.1
  token_sha     TEXT UNIQUE,                -- 同一个 token 的 SHA-256，认证实际查这个
  address       TEXT NOT NULL,              -- 客户端连的地址（域名或 IP）
  country       TEXT NOT NULL DEFAULT 'XX',
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_seen_at  INTEGER,
  version       TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL
);

CREATE TABLE inbounds (
  id            INTEGER PRIMARY KEY,
  node_id       INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  tag           TEXT NOT NULL UNIQUE,       -- sing-box inbound tag，全局唯一
  protocol      TEXT NOT NULL,              -- vless | anytls | shadowsocks
  port          INTEGER NOT NULL,
  config        TEXT NOT NULL,              -- sing-box 入站 JSON（users 为空）
  client        TEXT NOT NULL,              -- 客户端侧参数，JSON，见 §3.1
  enabled       INTEGER NOT NULL DEFAULT 1,
  address       TEXT NOT NULL DEFAULT '',   -- 外部中转地址，见 §6
  relay_node_id INTEGER REFERENCES nodes(id), -- 站内中转，见 §6
  relay_port    INTEGER NOT NULL DEFAULT 0
);

-- 用户能用哪些入站。没有行 = 全部（自用场景的默认）
CREATE TABLE user_inbounds (
  user_id    INTEGER NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
  inbound_id INTEGER NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, inbound_id)
);

-- 计费明细，按天聚合
CREATE TABLE traffic (
  user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  day      INTEGER NOT NULL,                -- unix 天
  up       INTEGER NOT NULL DEFAULT 0,
  down     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, node_id, day)
);

-- 用量形状，按小时聚合，见 §8
CREATE TABLE user_activity (
  user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  hour     INTEGER NOT NULL,                -- unix 小时
  conns    INTEGER NOT NULL DEFAULT 0,      -- 单次采样里最多的连接数
  peers    INTEGER NOT NULL DEFAULT 0,      -- 最多的不同目的地址数
  ports    INTEGER NOT NULL DEFAULT 0,      -- 最多的不同目的端口数
  ips      INTEGER NOT NULL DEFAULT 0,      -- 最多的不同来源地址数
  samples  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, node_id, hour)
);

CREATE TABLE settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);
```

`settings` 里目前放三样东西：会话签名密钥、管理员凭据、路由策略（§8.2）。

### 3.1 为什么 `inbounds` 有 `config` 和 `client` 两列

- `config` = 服务端配置，原样发给 node
- `client` = 客户端要用、服务端用不上的参数

```json
// client 列，以 Reality 为例
{ "sni": "www.microsoft.com", "pbk": "<公钥>", "sid": "a1b2c3d4",
  "fp": "chrome", "flow": "xtls-rprx-vision" }
```

Reality 公钥在**建入站时**从私钥算出来存进 `client`，不在每次生成订阅时重算 —— 那是
一次标量乘法，否则每个用户每次请求都要做一遍。

`client` 是一组固定字段（`sni/pbk/sid/fp/flow/method/server_psk`），不是自由 JSON。
这限制了能加什么协议：一个需要新客户端参数的传输方式，光改服务端配置是不够的，分享
链接、sing-box 配置、Clash 配置三处都得同时认识它。

### 3.2 一个入站属于且只属于一个节点

`inbounds.node_id` 是外键。订阅条目按 (用户 × 入站) 生成，如果一个入站能跨节点共享，
订阅里就只能出现其中一个地址 —— 结构上直接排除掉这种可能。

### 3.3 tag 由节点名推导

tag 是 `<协议>-<节点名>`，冲突时加数字后缀。**改节点名会重新推导该节点上每一个
入站的 tag**，不只是碰巧带旧名字的那些 —— 一个叫 `01` 的 tag 在日志、面板和客户端
里都答不出它是谁。代价是手打的 tag 活不过一次改名，改名表单里写明了这点。

tag 全局唯一，而改名会让 tag 在行之间挪位，所以重命名在一个事务里分两阶段做（先全
部改成占位名，再改成目标名），否则中途会撞上唯一索引。

---

## 4. 配置下发与用户热插拔

### 4.1 节点侧必须处理的三件事

1. **SS 入站永远至少要有一个用户。** sing-box 在构造时按 `len(users)` 决定建单用户
   还是多用户监听器，零用户会建成单用户型 —— 那个类型没有更新用户的方法，之后所有
   热添加静默失效。单用户模式下配置里的 `password` 本身就是完整凭据，不属于任何
   用户，那部分流量不计费。**对策：** 节点在应用前补一个随机密钥的占位用户。

2. **用户名快照必须原子发布并做边界检查。** VLESS 和 SS 在连接路径上用下标查用户名，
   下标由 service 发放、与用户切片分别更新，删用户时长度对不上会索引越界打崩节点。
   **对策：** `atomic.Pointer` 快照 + 越界返回空串（`skysbx-core` 的补丁之一）。

3. **热插拔后必须同步统计白名单。** v2ray_api 的 stats service 在配置加载时建立用户
   白名单，热加的用户不在里面 → 能连通、能跑流量、**永远不计费**。**对策：** 每次
   用户变更后刷新白名单，所以 `users` 消息里带 `stats_users`。

### 4.2 配置回滚

`ApplyConfig` 先构造新实例，成功了才停旧实例、启新实例。如果**启动**失败，旧实例已经
没了，一个打错的端口会让整台节点上所有入站下线，直到某次无关的编辑碰巧推了新配置。
所以启动失败时节点会自动回滚到上一份配置，并把失败原因通过 `state.error` 报上去。

### 4.3 生效确认

新建或修改入站是异步到达节点的，所以渲染出来的那一刻节点不可能已经应用完 —— 每个新
入站都会显示「未生效」，直到手动刷新。入站页会自己轮询，直到节点的报告追上面板持有的
状态，最多 8 次；节点报了错、或者节点离线，都是「答案」，不再轮询。

五种状态：**已生效**（节点确认在跑）/ **跑的是旧配置**（见下）/ **确认中…**（还在等）/
**未生效**（节点报了错，错误原文直接显示在页面顶部）/ **节点离线**（没有报告，沉默不
等于失败）。

**为什么需要「跑的是旧配置」：** 节点报的是它在服务哪些 **tag**，不是哪些参数。一份被
拒绝的配置通常 tag 没变，于是每一行都会显示「已生效」，而旁边那个端口根本没在监听 ——
实测把一个入站改到被占用的端口时就是这样。「已生效」是操作者唯一会看的那个词，所以它
不能被说给一台正在跑别的东西的节点听。tag 确实不在服务里的行，仍然显示「未生效」。

---

## 5. 证书

sing-box 只在启动时读证书文件。**证书轮换后必须重启监听器，换文件不够。**

安装脚本给了 `--domain` 就用 certbot 签，写到 `/opt/skysbx/cert.pem` 和 `key.pem`，
AnyTLS 入站默认就指这两个路径。

panel 自己的证书走 `certmagic`：先在 :80 起监听（那是 CA 校验的端口），再申请。申请
失败不退出，转为后台重试 —— 否则遇到 CA 限流就会变成一个重启循环。

---

## 6. 中转

订阅里一个入站的地址不一定是它所在节点的地址。两种形式，互斥：

### 6.1 外部中转

`inbounds.address` 填一个面板管不到的中转机，例如 `relay.example.net:443`。**带端口**
是关键 —— 中转的意义常常就是把入站放到一个节点自己拿不到的端口上（443 被面板占了、
或者被运营商挡了）。不带端口表示两边端口一样。

这个字段节点根本看不到，只改它不会重建监听器。

### 6.2 站内中转

选一台面板已经管着的节点，面板在它上面自动开一个 sing-box `direct` 监听：

```json
{ "type": "direct", "tag": "relay-vless-tokyo", "listen": "::", "listen_port": 443,
  "override_address": "jp.example.com", "override_port": 8443 }
```

**中转机只搬字节** —— 不解密、不认证、不知道对面是哪个用户。这是它相对链式代理的全部
理由：协议仍然在落地节点终结，所以按用户计流量、IP 限制、活动记录**全部照旧工作**。
链式代理必须让入口节点以某个内部账号向落地节点认证，落地那侧所有用户就会塌缩成那一
个名字。

设计上的几个决定：

- **中转监听的 tag 是 `relay-<落地入站tag>`**，推导而非存储，所以不会漂移。手打的入站
  tag 不允许用 `relay-` 前缀，否则会遮蔽一个中转口。
- **不需要环路检测。** 中转永远指向一个真实入站，不会指向另一个中转，所以链不成环。
  唯一的例外是两条节点记录指向同一台主机且端口相同，这一种情况被显式拒绝。
- **两端任意一端停用，中转口就撤掉。** 否则那是一个开着的、只会回 connection refused
  的端口。
- **删除一个被别人当中转的节点会被拒绝**，并点名是哪些入站。级联或置空都会让那些入站
  悄悄退回去广播自己节点的地址 —— 那正是设置中转要避免的事。
- **中转节点停用时，订阅里直接不出这一条**，同样不回落。
- **中转口豁免路由策略**（§9）。从它经过的是已经加密的代理流，嗅探只会看到隧道自己的
  握手，永远看不到客户端在里面干什么 —— 纯粹的开销。落地节点解密之后会对同一份流量
  应用同一套策略，那里才咬得住。sing-box 没有「排除某入站」的匹配器，所以豁免写成一
  条放在嗅探规则**之上**的终结型 `route` 规则。
- **中转节点的入站页会列出它替别人占着的端口。** 那些监听属于另一个节点的行，不列出来
  的话就是一批开着却不在任何表里出现的端口。

节点端为这个特性改了 **0 行代码**：`direct` 入站本来就已注册，中转连接的
`metadata.user` 为空，IP 限制器和活动采样器在既有的判空分支上自动跳过。

**两个固有代价：**

1. 落地节点看到的来源 IP 是中转机的。IP 限制对走中转的用户会**变宽**（三个地方连过来
   算一个地址），但不会误杀。sing-box 1.13 移除了入站的 `proxy_protocol`，没有干净解法
   —— realm 之类的外部中转也一样。
2. 中转节点自己的带宽不计入任何用户，所以概览页上它的流量只反映它自己的入站。账是对
   的（计费发生在落地节点），只是中转量看不见。

---

## 7. 流量计费

node 每 30s 从内嵌的 v2ray_api 读一次计数器，算出**增量**上报：

```json
{ "t": "stats", "d": {
  "users": { "alice": { "up": 1048576, "down": 20971520 } },
  "system": { "cpu": 3.1, "mem_used": 214958080, "uptime": 86400 }
} }
```

- **上报增量而不是累计值。** 节点重启时计数器归零，累计值会产生倒退，而面板无法把它
  和一次故意的清零区分开。增量模型天然免疫。
- panel 收到后 `traffic_used += ?`，并写入 `traffic` 的当天行（按 user × node × day）。
- 超限或过期的用户，panel 在下一次 `users` 里直接不包含它 —— 没有额外的「禁用」指令。
- 编辑用户**不写 `traffic_used`**：表单里带着的是读取时的旧值，写回去会静默地把已用
  流量重置成一个陈旧的数。清零是行尾单独的操作。

概览页按节点分列流量，不只给一个总数 —— 总数只能回答「用了多少」，答不出是哪台节点
在扛、哪台不再扛了、哪台快要换套餐了。

**已知误差：** AnyTLS 复用会话，删用户后已建立的连接要等空闲窗口（< 90 秒）才断。另
外两个协议是即时的。

---

## 8. 用量控制

### 8.1 每用户 IP 限制

订阅是一个文件，没有什么能阻止拿到它的人把它贴出去。`users.ip_limit` 限制一个账号
**同时连着的不同来源地址数**（不是连接数 —— 一台机器就会开几十条连接）。

在**节点上**执行，因为连接在那里。面板最晚要 30 秒才知道，而且它唯一的手段是吊销整个
账号 —— 那会把付费的那个人一起踢下线。

- 节点每 **5 秒**从自己的 clash API 拉一次连接列表，超出的地址直接断开。
- 保住位置的是**最早出现**的那些地址，跨轮稳定，不会两台设备互相把对方踢掉。
- 一个地址在最后一条连接关闭后还能保留 **5 分钟**的位置，否则短暂空闲就会把位置让给
  下一个连上来的人。

**两个已知边界：** 5 秒一轮，完全发生在两轮之间的突发看不见（但一个出现过一次的地址
会占住 5 分钟）；限制**按节点各自执行**，而面板显示的地址数是跨节点求和的，所以多节点
下这个数可以合法地大于上限 —— 而那本身正是值得看到的信息。

以上全部在真实流量下实测过（三个来源地址，两个节点，长连接）：超出的地址在一次轮询内
被断开、先到的两个不受影响；两个节点各 2 个地址时零次切断而面板显示红色的 `4 / 2`；
一个地址下线后其位置保留 **299 秒**（宽限 300s，误差在一次轮询内）才让位给等待者。

**一个测量上的坑，值得写下来：** 会重连的客户端每隔两秒回来一次，而判定每 5 秒一轮，
所以从连接列表的快照看它是「在线」的 —— 实际上它每一轮都在被切断。用快照判断「有没有
被放行」会得出完全错误的结论（第一次测出来是 49 秒）。真正的信号是**切断有没有停止**。

### 8.2 路由策略

面板级的一份策略，推给所有节点（一个用户在 A 节点被禁 BT、在 B 节点没被禁，等于没
禁）。三项：

- **禁 BitTorrent** —— 按嗅探结果拒绝。**部分有效，说清楚比说满好**：TCP 嗅探匹配的是
  明文握手，而多数客户端默认加密；仍然能抓到的是 uTP（即使载荷加密，帧头仍是明文）和
  UDP tracker 通告。实践中那是大部分流量和全部的 peer 发现，但它不是一堵墙。
- **禁测速站** —— 12 个内置后缀（speedtest.net、fast.com、speed.cloudflare.com …）。
  持续跑测速的一个人比其他所有人加起来还贵，而没有客户端需要它们。
- **自定义域名黑名单** —— 后缀匹配，粘贴进来的内容会被规范化（去协议头、去 `*.`、去
  路径和端口、转小写、去重），因为 sing-box 是字面匹配后缀，`https://x.example/a` 谁
  也匹配不上。

**顺序不可调**：嗅探规则必须最先，它下面每一条都匹配嗅探的结果。一条 protocol 规则
放在嗅探之上会静默地什么都匹配不到 —— 这是最坏的失效方式。嗅探器只开下面真正用得上
的那几个，因为每一个都是每条连接首包上的读取和比较。

一份存坏了的策略读作「没有策略」，不会把所有节点的路由一起带走。

### 8.3 活动记录

每个用户一个页面，按小时聚合，30 天后清理：连接数、不同目的地址数、不同目的端口数、
不同来源地址数。

**记形状，不记去处。** 区分 BT、大流量下载、长时间测速和普通浏览的是形状：同时开多少
连接、连多少个不同的对端、跨多少个端口。记录一个人去了哪里会回答一个没人问过的问题，
并凭空造出一份原本不存在的浏览历史。

**存峰值不存平均。** 一个账号每小时开两百条连接跑十分钟，平均下来什么都不是，而那十
分钟正是要看的东西。

**没有「这是 BT」计数器**，不是没试过：clash API 给的 `metadata.type` 是入站类型不是
嗅探出的协议，而被 BT 规则拒掉的连接在任何一次采样看到它之前就已经关了。形状是能拿到
的信号，而且它更好 —— 它不在乎载荷加没加密。

---

### 8.4 每月流量重置

`users.reset_day` 是每月已用流量归零的那一天，0 表示不重置。月付套餐用的：否则运营者
每个月要手动清一遍，而漏掉的那个账号会在跨过上限的那一刻静默停掉 —— 症状和到期一模
一样，页面上分不出是哪一种。

- **短月自动落到月底。** 31 就是「每月最后一天」，二月不会被跳过；29、30 同理。
- **比对的是上次归零时间，不是存一个「下次到期」。** 面板在某人的重置日当天没开机，
  下次启动时会补上，而不是丢掉这个月。
- **手动清零也会打上时间戳**，否则运营者刚手动清完，一小时后的定时任务又清一遍。
- **新建用户的时间戳从创建时刻开始**，所以 20 号注册、重置日是 21 号的人，不会第二天
  早上就被清掉一个他根本没有过的月份。
- **给一个已有账号打开重置，也从打开的那一刻开始计周期。** 否则一个跑了半年的账号会被
  拿去和几个月前的周期边界比，判定为「早该重置了」，在下一次扫描时丢掉全部计数 ——
  那不是「从现在起按月重置」的意思。同理，`last_reset_at` 为空表示这个周期还没有起点，
  扫描时补一个起点而不是当成逾期；升级之后所有存量账号都处在这个状态，判断反了会把
  整个面板清空一次。
- **过期账号照样重置。** 月度额度是月度的，与订阅有没有断无关；这样续费只需要改到期
  日一件事，不用再记得清一次流量。归零的只是计数器 —— 概览页的曲线和按节点的流量账
  都来自 `traffic` 表，不受影响。
- 归零可能让一个超限的账号重新可用，所以要推给节点 —— 吊销是靠从推送里省略实现的，
  恢复也一样。

面板每小时跑一次，启动时立刻跑一次。

## 9. 订阅生成

```
GET /sub/<sub_token>
```

按请求头分发格式：

| 客户端 | 判定 | 返回 |
|---|---|---|
| 浏览器 | `Accept` 含 `text/html` | 订阅页（用量 / 到期 / 一键导入） |
| sing-box / SFA / SFI | `User-Agent` | sing-box JSON |
| mihomo / Clash | `User-Agent` | Clash YAML |
| 其它 | 兜底 | base64 分享链接列表 |

也可以用 `?format=` 强制：`singbox` / `sing-box`、`clash` / `mihomo`、`base64` /
`v2ray`、`html` / `page`。

> 用 curl 自测时注意：浏览器规则匹配的是 **`Accept` 头**，只带
> `User-Agent: Mozilla/…` 命中不了。

三种分享链接：

```
vless://<uuid>@<addr>:<port>?encryption=none&flow=xtls-rprx-vision&type=tcp
        &security=reality&sni=<sni>&fp=chrome&pbk=<公钥>&sid=<shortid>#<别名>

anytls://<password>@<addr>:<port>?security=tls&sni=<sni>&fp=chrome#<别名>

ss://base64url("2022-blake3-aes-256-gcm:<服务端PSK>:<用户PSK>")@<addr>:<port>#<别名>
```

**AnyTLS 不要输出 `multiplex` / `smux`** —— 它本身就是多路复用协议，加了反而出错。

**Shadowsocks 2022 要两段 PSK**（入站的共享 PSK + 用户自己的，冒号连接）。只给服务端
那一段，认证成的是「没有人」，计费也计到没有人头上。

订阅内容 = 该用户 × 所有启用且节点启用的入站，并受 `user_inbounds` 限制。

**别名**（客户端服务器列表里显示的那一行）是 `tag | 用户名 | 已用/总量 | 到期`。对某些
客户端来说那一列是它唯一会显示的东西，所以要在一行里同时说清是哪个服务器、是谁的账号、
还剩多少。

不活跃的用户拿到的是**空订阅**而不是错误 —— 客户端应当停止连接，而不是无限重试一个失败
的 HTTP 请求。

生成的 sing-box 配置里，节点域名被钉到 local resolver（`dns.rules`），否则解析一个域名
要走它自己是终点的那条隧道。

---

## 10. 目录结构

**panel**

```
cmd/panel/          入口
internal/
  store/            SQLite：schema、迁移、全部查询   ← 上面没有任何地方写 SQL
  service/          业务逻辑，不知道 HTTP 的存在     ← 换 UI 时的切换点
  hub/              WebSocket 集线器：节点连接、消息路由、心跳
  sub/              订阅生成：分享链接 / sing-box / clash
  singbox/          sing-box 配置的 Go 结构体（只有类型定义和序列化）
  web/              htmx handler + 模板 + 静态资源，go:embed
deploy/             安装脚本
docs/DESIGN.md      本文
```

`service/` 刻意不感知 HTTP：以后若要把 htmx 换成 SPA，只需在同一套 service 上加一层
JSON API，数据模型、协议、订阅、计费都不用动。

**node**

```
cmd/node/           入口
internal/
  link/             连 panel 的 WebSocket 客户端，含重连与上报节拍
  engine/           内嵌 sing-box：应用配置、热插拔、统计、IP 限制
  proto/            控制协议的线格式类型
```

`internal/proto/` 的结构体是**重新定义**的，不是从 panel import 的：两者之间的契约是
线格式，不是共享的 Go 包。

同理，panel 的 `internal/singbox/` 只放 JSON 结构体，**不 import sing-box 本体**。

这条边界有代价，而且付过：panel 没法拿 sing-box 的 schema 校验自己生成的配置。曾经
订阅里的 DNS 块用的还是旧写法、缺 `route.default_domain_resolver`、还把 local DNS 的
detour 指到空的 direct 出站 —— 三条都让客户端起不来，而当时所有订阅测试全绿。单元测试
只能把字段形状钉死，钉不住上游下一次改名。**唯一真正的验证是拿真的 sing-box 跑一遍
生成的配置**，这一步留在部署验证里。

---

## 11. 安全模型

面板对外开三种入口，威胁模型各不相同。

| 入口 | 认证 | 谁能到达 |
|---|---|---|
| 管理 UI | 会话 cookie（HMAC 签名，12 小时，HttpOnly / Secure / SameSite=Lax） | 任何人可到登录页 |
| `/sub/<token>` | 路径里的 token 就是凭据（18 字节随机 = 144 位） | 任何人 |
| `/api/v1/node/connect` | Bearer token（32 字节随机 = 256 位） | 任何人 |

### 11.1 凭据用哈希还是用 bcrypt

**bcrypt 只用于管理员密码**，因为那是这个系统里唯一由人选的凭据，也是唯一值得去猜的。

其余凭据 —— 节点 token、订阅 token、用户密码 —— 都是 `crypto/rand` 出来的高熵随机串，
没有什么可猜。对它们用 bcrypt 不增加任何安全性，成本却全落在面板身上，而且**哈希不能
建索引**，所以只能线性扫。

节点认证一度就是这样：把presented token 拿去和每个启用节点的 bcrypt 哈希比一遍。那个
端点按定义还没认证任何人，于是一个内容为 "wrong" 的 token 就能让面板做 N 次 bcrypt ——
实测二十节点的面板上，**单个未认证请求消耗 1.04 秒 CPU**。谁能连上谁就能打停它。

现在 token 存一份 SHA-256 并建唯一索引，认证是一次索引查找，同样场景 1.04s → **70µs**。

`token_hash` 留着只为一种情况：迁移之前建的节点，明文早已丢弃，无法回填索引。它们继续
走扫描，但那条路径**按来源地址限流**，且每个这样的节点在下一次握手时会把自己的索引补上。
一旦 `NodesMissingTokenSHA()` 归零，扫描对任何请求都不可达。

限流用回调而不是布尔值，因为要在**将要付出代价的那一刻**才问：提前问会为根本不会走到
扫描的请求扣配额，事后问则什么都没限住。

### 11.2 限流

只加在验证凭据的处理器上 —— 它们同时是最贵的和不需要认证的。

| 位置 | 额度 |
|---|---|
| 登录 | 突发 10 次，之后每 5 秒 1 次，按来源地址 |
| 节点认证的旧扫描路径 | 突发 3 次，之后每分钟 1 次，按来源地址 |

桶按**来源地址**分，不是全局 —— 全局计数器会让一个攻击者把所有人一起锁在外面，那样
限流本身就成了拒绝服务。地址取自 `RemoteAddr`，**不看 `X-Forwarded-*`**：那些头由客户端
自己写，认它等于让攻击者每个请求换一个桶，比没有限流更糟，因为看起来像有。

### 11.3 响应头

| 头 | 为什么 |
|---|---|
| `Content-Security-Policy` 含 `frame-ancestors 'none'` + `X-Frame-Options: DENY` | 界面里每个破坏性操作都是一个按钮，会话 cookie 是 Lax（顶层框架加载会带上），不拒绝被嵌套就可点击劫持 |
| `Referrer-Policy: no-referrer` | 订阅 token 在 URL 路径里，否则会出现在订阅页上任何外链的 Referer 中 |
| `Cache-Control: no-store` on `/sub/` | 订阅响应是一份写成文本的 bearer 凭据 |
| `X-Content-Type-Options: nosniff` | — |
| `Strict-Transport-Security`（仅 TLS 上，30 天） | 30 天而非一年：这是别人跑在自己域名上的软件，一个撤不掉的 max-age 是在面板早已下线之后仍然锁着那个域名 |

### 11.4 节点是半可信的

节点跑在租来的机器上，其中一台迟早不是你的 —— 这是值得照着设计的假设。

- **节点只能给它自己服务的用户记账。** 否则任一节点可以给面板上任意账号记流量，而超限
  即吊销，于是一台被攻陷的节点能把其他所有节点的用户逐个刷停。活动记录同样限定范围 ——
  那是拿来判断要不要封人的依据。
- **单次上报的流量有上限（1 TiB）。** 一个上报间隔是 30 秒，跑满的 100 Gbit/s 链路也
  搬不到 400 GiB，超过就不是测量结果。而且和一个偏小的错数不同，一个巨大的错数会永久
  吊销一个账号，没有东西能把它恢复回来。
- **负增量直接丢弃**，它只可能来自节点算错。

节点无法读取别的节点的配置，也拿不到用户凭据以外的东西 —— 面板只推送该节点入站上的
用户列表。

### 11.5 已知残留

- **登出不使已签发的 cookie 失效。** 会话是无状态签名 cookie，没有服务端表可作废；
  被截获的 cookie 在 12 小时内一直有效，改密码也不影响它。单管理员场景下代价可接受，
  真要修需要在 `settings` 里放一个会话世代号并在签名载荷里带上。
- **手动运行二进制时，首次 setup 是开放的。** 一键安装脚本会在启动服务**之前**就问好
  管理员用户名和密码并写进数据库（`-set-admin`，密码走 stdin，不进 argv 也不进 shell
  历史），所以按脚本装的面板没有这个窗口。自己编译、自己起进程的话窗口仍然存在 ——
  `/setup` 归先到者所有。`/setup` 本身的写入是原子的（一个事务里检查加写入），所以
  两个请求同时到达不会有后来者顶掉先到者。
- **没有 CSRF token。** 靠 `SameSite=Lax` 加「没有任何会改状态的 GET」来防 —— 现有的
  GET 路由全是只读的。加了 CSRF token 会更稳妥。
- **管理员可以让节点读任意文件当证书。** 证书路径由管理员填、原样下发给节点。这是这个
  功能本身的含义，不是越权。

## 12. 工程约束

| 约束 | 说明 |
|---|---|
| SS2022 只能用 `2022-blake3-aes-256-gcm` | 服务端 PSK 和用户 PSK 都是 base64(32 字节)；用户 PSK 是 32 字节，配 128 位方法会在节点上直接失败 |
| Reality 私钥 base64url **无填充** | sing-box 用 `base64.RawURLEncoding` 解码，带 `=` 直接失败 |
| Reality 公钥由私钥推导 | 建入站时算一次存进 `client` |
| VLESS 的 flow 两边必须一致 | 入站的 client 参数和推给该入站的每个用户都得是 `xtls-rprx-vision`，不一致会在握手时报 flow mismatch，读起来像客户端的问题 |
| 节点构建 tag 不可省 | `with_clash_api,with_v2ray_api,with_utls,with_acme,with_quic`；缺了能编译，启动即死。`go test` / `go vet` 同样要带 |
| `-race` 需要 `-gcflags=all=-d=checkptr=0` | sing-box 自己的 unsafe 运算会触发 checkptr |
| 面板机的 443 归 panel | 该机上的 Reality 要退到别的端口 |
| 节点域名必须灰云 | 三个协议都不是 HTTP，套 CDN 全挂 |
| 入站 tag 不能以 `relay-` 开头 | 保留给站内中转的监听 |

---

## 13. 已知限制

按能不能修分类，不按重要性：

**固有的**

- 走中转的连接，落地节点看到的是中转机的地址，IP 限制因此变宽（§6）。
- 中转节点自己的带宽不计入任何用户（§6）。
- BT 拦截抓不到 MSE 加密的 TCP BitTorrent（§8.2）。
- panel 无法用 sing-box 的 schema 校验自己生成的配置（§10）。

**可以改进的**

- IP 限制是 5 秒轮询而不是连接建立时判定。sing-box 没有可挂的接受钩子，要做得在
  `skysbx-core` 里加补丁。
- IP 限制按节点各自执行，计数跨节点求和（§8.1）。
- `client` 是固定字段集，加一种新传输方式要同时改四处（§3.1）。
- AnyTLS 删用户后已建立的会话最多 90 秒才断（§7）。
- 登出不使已签发的会话 cookie 失效；首次 setup 窗口开放；没有 CSRF token。
  三条都在 §11.5，那里说明了各自的现有缓解和真正修法。

---

## 14. 许可

- panel：AGPL-3.0
- node：GPL-3.0
- skysbx-core：GPL-3.0（sing-box 的分支）

node 与 sing-box 官方无关联、未获其背书，请勿向他们报告本项目的问题。
