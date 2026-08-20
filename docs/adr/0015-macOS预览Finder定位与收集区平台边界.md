# ADR-0015 macOS 预览、Finder 定位与收集区平台边界

状态：Accepted

日期：2026-08-20

## 背景

DaisyDisk 用 Quick Look、Finder 定位和 Collector 支撑用户在清理前确认对象。Marmot 不能把任意路径直接交给前端，也不能通过 `qlmanage`、`open`、`osascript` 或 Shell 拼接命令绕过既定的 Wails 和文件操作安全边界。

## 预研依据

- [R-012 macOS 预览、Finder 定位与收集区边界](../research/R-012-macOS预览Finder定位与收集区边界.md)
- [R-009 DaisyDisk 产品体验与交互基线](../research/R-009-DaisyDisk产品体验与交互基线.md)
- [ADR-0009 清理计划与 macOS 废纸篓执行](0009-清理计划与macOS废纸篓执行.md)
- [ADR-0013 DaisyDisk 空间图与渐进查询数据契约](0013-DaisyDisk空间图与渐进查询数据契约.md)

## 决策

### 1. Application 只暴露快照节点行为

对外提供：

```text
PreviewNode(snapshotId, nodeId)
RevealNode(snapshotId, nodeId)
```

Application 必须先确认节点属于指定快照、节点类型允许、快照仍有效，并从快照取得已校验路径，再调用平台端口。Wails DTO 不接受任意路径、URL 或 Shell 参数。

平台端口为：

```text
PreviewPort.Preview(path, ownerWindow)
PreviewPort.Reveal(path)
```

### 2. macOS 使用原生 API

- `Preview` 使用 `QuickLookUI` 的 `QLPreviewPanel` 和 `QLPreviewItem` data source，在 AppKit 主线程绑定 Wails 窗口的 responder 生命周期；
- `Reveal` 使用 `NSWorkspace.activateFileViewerSelectingURLs`；
- 不调用 `qlmanage`、`open`、`osascript` 或任意 Shell；
- 原生 bridge 放在 Platform 层的 macOS 文件中，不进入 Domain/Application；
- Windows 只保留平台端口，不进入当前实现。

### 3. Collector 只是会话选择区

前端 Collector 是当前会话的选择视图，Application 的 `CleanupPlan` 才是唯一可审查、可校验和可执行的计划对象。加入或移出 Collector 不触发文件操作；创建计划、确认计划和执行计划仍遵守 ADR-0009 的版本与文件身份校验，默认移入废纸篓，不复制 DaisyDisk 的永久删除策略。

聚合项、卷根、扫描根、特殊文件和权限不明对象不能直接加入清理计划。聚合项不能预览或定位。

### 4. 拖放只扩展扫描 scope

Finder 拖放只用于提交扫描范围。路径必须回到 Go 端做规范化、存在性、目录/卷边界和权限校验；拖放路径不能直接成为预览、Finder 定位或清理授权。只有扫描快照中存在的真实节点才能执行 Preview/Reveal。

## 错误和生命周期

- 文件已经移动或删除：返回 `stale_node`，提示重新扫描，不打开新路径；
- 权限不足：返回 `permission_denied`，保留扫描可信度，不隐式提权；
- Quick Look 创建或窗口绑定失败：返回可展示错误，不能让 React 根组件崩溃；
- 窗口关闭或切换快照时，预览 panel 取消当前 item；
- Platform 只报告预览/定位结果，不修改扫描快照或清理计划。

## 备选方案

- 前端接收任意路径并调用预览：拒绝，路径是高风险输入，绕过快照和 Application 校验。
- 通过 Shell 调用系统命令：拒绝，参数注入、生命周期和错误语义不可控。
- 让 Platform 持有 Collector 或待删除状态：拒绝，平台能力不能拥有清理领域状态。

## 后果

- 需要新增 Preview/Reveal Application 用例和 macOS Platform adapter，并在 Wails 窗口生命周期中处理 Quick Look。
- 前端需要把 Collector 映射为 CleanupPlan 候选，而不是维护删除队列。
- 真实 Wails 窗口的 Quick Look 显示、关闭、切换、Finder 定位和窗口重开仍需 smoke test。

## 验收标准

- Preview/Reveal 的 Wails 输入只有快照 ID 和节点 ID，任意路径输入被拒绝。
- macOS 预览使用 Quick Look，Finder 定位使用 NSWorkspace，代码路径不执行 Shell。
- Collector 加入/移除不产生文件操作，只有确认后的 CleanupPlan 才能进入执行流程。
- 过期节点、聚合项、权限错误和窗口生命周期异常都有可展示的失败结果。
