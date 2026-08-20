# ADR-0006 Wails 工具链与 macOS 分发边界

状态：Accepted（真实 Developer ID/TCC 流程仍是发布前门禁）

日期：2026-08-19

## 背景

Wails 生产包和 macOS TCC 权限必须在应用身份下验证。当前项目不能只验证开发服务器，
也不能把无签名的临时二进制当作全盘访问结论。

## 预研依据

- [R-003 Wails 通信与安全](../research/R-003-Wails通信与安全.md)
- [R-006 macOS 全盘权限](../research/R-006-macOS全盘权限.md)
- Wails `v3.0.0-beta.9` 本机打包结果

## 决策

- 第一阶段固定 Wails `v3.0.0-beta.9`；升级必须新增 ADR 并重跑打包 smoke test；
- Go 工具链最低为 1.25.0，本机验证版本为 Go 1.25.13；
- 生产模式嵌入前端资源，生成 macOS `.app`，不启用 Wails Server Mode；
- 通信使用类型化绑定和应用事件；事件传摘要，状态通过绑定查询恢复；
- 第一阶段采用 Developer ID 签名和公证的直装分发，App Store 沙盒不在当前范围；
- 不通过隐式 root/管理员提权扫描；权限不足产生明确的 partial/inaccessible 结果；
- 在获得真实签名身份前，只能把 ad-hoc 包作为构建验证，不能宣称已完成 Full Disk Access 验证。

## 后果

需要在发布环境固定 bundle identifier、Team ID、entitlements 和公证流程。未来更换 Wails
版本、分发渠道或增加 CLI/HTTP，都必须重新评估权限边界。
