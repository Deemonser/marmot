# ADR-0002 Wails 优先与通信边界

状态：Accepted

日期：2026-08-19

## 背景

Marmot 必须扫描 macOS 全盘。全盘访问、Full Disk Access、废纸篓和应用签名都属于桌面应用权限边界。浏览器本地 HTTP 服务会增加端口、Token、Origin、CORS 和本地网页调用的安全问题。

## 预研依据

- [R-003 Wails 通信与安全](../research/R-003-Wails通信与安全.md)
- Wails 官方架构、构建系统和 Server Build 文档

## 决策

第一阶段采用 Wails 桌面应用优先，生产版使用：

```text
嵌入的 React 前端
  -> Wails 类型化 Go-JS 绑定
  -> Application 用例
  -> Domain / Platform
```

- 生产构建嵌入前端资源。
- 生产版不启动 Wails Server Mode，不开放本地 HTTP 控制面。
- 长任务通过 Wails 应用事件推送，状态通过绑定方法查询。
- 开发模式可使用 Vite 开发服务器，但它不是生产架构。
- Wails 版本在本机安装和签名 smoke test 后固定。
- 未来若需要 CLI、HTTP 或无界面服务，使用独立 Transport 适配器并新增 ADR。

## 安全边界

- 只暴露明确的领域用例方法。
- Go 端校验路径、快照 ID、计划版本、权限和平台能力。
- 不暴露任意命令执行、任意路径删除或任意文件读取。
- 不把开发服务器、Token 或端口设计带入生产版。

## 后果

### 正面影响

- 应用权限主体明确，适合 Full Disk Access 和系统能力集成。
- 生产版没有本地 HTTP 控制面的网络边界。
- 前端和 Go 服务仍然可以通过稳定的用例契约解耦。

### 代价

- 需要处理 Wails 版本、macOS 签名、公证和 WebView 差异。
- 不能直接把浏览器当作独立客户端；未来要做 CLI 或远程控制需要另行设计。

## 文档同步

- [SDD](../SDD.md) 已改为 Wails 绑定和应用事件。
- [README](../../README.md) 已改为 Wails/macOS 优先。
- [DDD](../DDD.md) 保持技术无关，只保留权限和全盘扫描领域规则。
