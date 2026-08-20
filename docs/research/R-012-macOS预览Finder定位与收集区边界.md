# R-012 macOS 预览、Finder 定位与收集区边界

状态：已完成，结论由 ADR-0015 接受；原生窗口 smoke test 待实现切片

日期：2026-08-20

## 1. 问题

DaisyDisk 使用 Quick Look、Finder 定位和 Collector 让用户在清理前确认对象。Marmot 当前只有扫描和清理计划接口，没有预览/定位平台端口；如果直接把路径交给前端或通过 Shell 调用 `qlmanage`/`open`，会破坏既定的 Wails 和安全边界。

## 2. 方法与证据

- 核对 DaisyDisk 的文件预览、删除/Collector、快捷键和产品交互说明。
- 检查 Marmot 当前 Wails DTO、Application/Platform/Ports 分层和废纸篓 Objective-C bridge。
- 使用本机 Xcode macOS 26.5 SDK 对 `QuickLookUI/QLPreviewPanel`、`QLPreviewItem` 和 `NSWorkspace.activateFileViewerSelectingURLs` 做 Objective-C 语法编译验证，均通过（只有属性声明警告）。
- 核对 Wails v3 的 `EnvironmentManager.OpenFileManager` 能力，确认其存在，但不让 Platform 反向依赖 Wails。

## 3. 发现

- Quick Look 是 AppKit/QuickLookUI 的原生 UI 能力，需要 data source/delegate 和主线程窗口生命周期；它不是一个适合暴露给 WebView 的 HTTP 服务。
- Finder 定位可以由 `NSWorkspace` 原生调用，并支持选择目标文件；不需要 Shell 拼接参数。
- Collector 是用户界面和清理领域之间的暂存交互，不应让平台适配器持有“待删除”状态。
- 文件路径来自扫描快照或拖放输入，都是不可信边界；预览和 Finder 动作必须由 Application 根据 `snapshotId + nodeId` 重新取得并校验。

## 4. 决策结论

### 4.1 平台端口

新增平台能力：

```text
PreviewPort.Preview(path, ownerWindow)
PreviewPort.Reveal(path)
```

Application 对外只暴露：

```text
PreviewNode(snapshotId, nodeId)
RevealNode(snapshotId, nodeId)
```

Wails DTO 不接受任意路径作为预览或 Finder 定位参数。Application 先确认节点属于快照、节点类型允许、快照未被废弃；再把已校验路径交给 Platform。

### 4.2 macOS 实现

- `Preview` 使用 `QuickLookUI` 的 `QLPreviewPanel`，通过 Objective-C bridge 创建单个 `QLPreviewItem` data source，并在 AppKit 主线程绑定当前 Wails 窗口的 responder 生命周期；
- `Reveal` 使用 `NSWorkspace.sharedWorkspace activateFileViewerSelectingURLs`；
- 不调用 `qlmanage`、`open`、`osascript` 或任意 Shell；
- Windows 只增加空的平台端口，不进入当前实现；
- 原生 bridge 放在 `internal/platform/*_darwin.go` 或配套 `.m` 文件，不能进入 Domain/Application。

### 4.3 收集区和清理计划

- 前端的 Collector 是当前会话的选择视图；
- Application 的 `CleanupPlan` 是唯一可审查和可校验的计划对象；
- 加入 Collector 不触发文件操作；
- 聚合项、卷根、扫描根、特殊文件和权限不明对象不能加入计划；
- 创建计划、确认计划、执行计划都沿用 ADR-0009 的版本和身份校验；
- 执行仍默认移入废纸篓，不能复制 DaisyDisk 的永久删除行为。

### 4.4 拖放

- Finder 拖放仅用于提交扫描 scope；
- Wails/前端取得的路径必须回到 Go 端做规范化、存在性、目录/卷边界和权限校验；
- 拖放路径不能直接成为预览、清理或 Finder 定位授权；
- 预览和定位必须在扫描快照中有对应节点后执行。

## 5. 错误和生命周期

- 文件已移动/删除：返回 `stale_node`，UI 提示重新扫描，不打开新路径；
- 权限不足：返回 `permission_denied`，保留扫描可信度，不弹出隐式提权；
- Quick Look panel 创建失败：返回可展示错误，不能让 React 根组件崩溃；
- 窗口关闭或切换快照时，预览 panel 取消当前 item；
- Platform 只报告预览/定位结果，不修改扫描快照或清理计划。

## 6. 验收和限制

- 语法编译验证通过；
- 需要在真实 Wails macOS 窗口中验证 Quick Look panel 的显示、关闭、切换文件和窗口重开；
- 需要验证 Finder 定位目录、包目录、符号链接和跨卷路径；
- 需要验证权限拒绝和节点过期不会绕过 Application 校验；
- 真实签名/TCC 仍是发布 smoke test，不因本 ADR 提前宣称完成。

## 7. 对 DDD/SDD 的影响

- 增加 `Preview` 和 `Reveal` 作为 Platform 能力，不创建新的领域聚合。
- `CleanupPlan` 明确承载 Collector 的审查语义，Collector 本身不是授权。
- Wails 只暴露 `PreviewNode`/`RevealNode`，不暴露任意文件读写或任意路径命令。
- SDD 增加 macOS 原生预览、Finder 定位、主线程和错误边界。

## 8. 建议 ADR

[ADR-0015 macOS 预览、Finder 定位与收集区平台边界](../adr/0015-macOS预览Finder定位与收集区平台边界.md) 已接受本记录结论。
