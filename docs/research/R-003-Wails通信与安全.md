# R-003 Wails 通信与安全

状态：已完成（官方文档核对）

## 参考资料

- [Wails 架构](https://v3.wails.io/concepts/architecture)
- [Wails 构建系统](https://v3.wails.io/concepts/build-system)
- [Wails 前端框架开发](https://v3.wails.io/guides/dev/frontend-frameworks)
- [Wails Server Build](https://v3.wails.io/guides/server-build)

## 结论事实

- 开发模式可以从开发服务器加载前端资源并支持热更新。
- 生产模式可以把编译后的前端资源嵌入 Go 二进制。
- 前端可以通过 Wails 的 Go-JS 绑定调用注册的 Go 服务。
- Wails 的安全模型包含导出方法白名单、参数类型校验和上下文隔离。
- Wails 也支持 Server Mode，但这是显式的 HTTP 部署模式，官方要求绑定 localhost 并按 Web 服务方式校验输入。

## Marmot 决策

生产版不使用 Wails Server Mode，不启动本地 HTTP 控制面：

```text
React Web UI
  -> Wails 类型化绑定
  -> Application 用例
  -> Domain / Platform
```

这样可以避免端口、Token、Origin、CORS 和本地网页调用控制面的额外边界。开发服务器只用于开发，不代表生产安全边界。

## 必须保留的控制

- Go 端只暴露明确的用例方法。
- 不暴露任意命令、任意文件读取或任意路径删除。
- 路径、快照 ID、计划版本和平台能力在 Go 端校验。
- 事件只发送进度、摘要和必要结果。
- 未来若增加 CLI 或 HTTP 服务，必须建立独立 Transport ADR 和威胁模型。

## 限制

本机已用 Wails `v3.0.0-beta.9` 完成临时项目的生产构建和 ad-hoc `.app` 打包；当前没有
Developer ID signing identity，因此真实签名构建、公证和 Full Disk Access smoke test
仍需在发布环境完成。

## 对 SDD 的影响

生产通信从 HTTP + SSE 改为 Wails 绑定 + 应用事件；Wails 运行时是 Presentation/Transport 适配器，不进入 Domain。
