# skysb-panel

代理面板的控制端：用户、节点、订阅、计费。

节点端是独立的 [`skysb-node`](https://github.com/kosje/skysb-node)，两者通过一条
WebSocket 通信。

> **状态：开发中。** 见 [`docs/DESIGN.md`](docs/DESIGN.md)。目前完成到 M1。

## 这个项目为什么存在

前身是 Remnawave 的 fork 加一个派生自 `rwnode-gosingbox` 的节点。那条路上有两个
问题：

1. **`rwnode-gosingbox` 没有 LICENSE**，也就没有分发授权。节点因此被清白重写。
2. **面板用 Xray 的格式描述 sing-box 的世界**，中间夹一层转译器。协议字段名对不上、
   配置校验器不认识 sing-box 的协议、用户列表字段名不一致 —— 一整类 bug 都源于此。

两端都自己写，这两个问题一起消失：面板直接存 sing-box 原生配置，节点不做翻译。

## 许可

本仓库 **AGPL-3.0**（见 [`LICENSE`](LICENSE)）。

节点仓库是 **GPL-3.0** —— 它内嵌 sing-box，属于衍生作品，没有选择余地。面板不链接
sing-box 的任何代码，所以许可可以自选。

**这是一条架构约束，不是文档措辞：** `internal/singbox/` 里只放 JSON 结构体定义，
**绝不 import sing-box 本体**。一旦 import，两个仓库的许可边界就没了。

## 运维形态

```
一个二进制 + 一个 SQLite 文件
```

TLS 由面板自己用 ACME 处理，不需要前置反向代理。备份就是拷一个文件。

## 开发

```bash
go test ./...
go build ./cmd/panel
```

需要 Go 1.27+。（节点那边锁 Go 1.26.x —— sing-box 的 `go:linkname` 在 1.27 下链接
失败。面板不受这个限制，这也是许可边界带来的一点额外好处。）

## 目录

```
cmd/panel/          入口
internal/
  store/            SQLite：schema、迁移、全部查询
  service/          业务逻辑，不知道 HTTP 的存在   ← 换 UI 时的切换点
  web/              htmx handler + 模板
docs/DESIGN.md      数据模型、协议、订阅生成、里程碑
```

`service/` 刻意不感知 HTTP：以后若要把 htmx 换成 React SPA，只需在同一套 service
上加一层 JSON API，数据模型、协议、订阅、计费都不用动。
