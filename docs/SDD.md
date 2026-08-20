# SDD 系统设计

状态：技术基准和高风险预研 ADR 已冻结，进入 P0 业务垂直切片实现

SDD 是项目实现门禁。代码、接口和模块必须先在这里定义边界、依赖和验收标准。

文档基准和目录职责见 [BASELINE](BASELINE.md) 与 [项目目录规范](PROJECT-STRUCTURE.md)。

## 1. 产品范围

第一阶段只支持 macOS，但产品目标是扫描整个本地文件系统并解释空间使用。Windows 只保留平台端口，不进入当前实现。

## 2. 技术栈

| 层 | 方案 |
| --- | --- |
| 桌面运行时 | Wails `v3.0.0-beta.9` |
| 后端 | Go 1.25+，本机验证 Go 1.25.13 |
| 前端 | React + TypeScript + Vite |
| 可视化 | D3.js |
| 生产通信 | Wails 类型化 Go-JS 绑定和应用事件 |
| 本地快照 | `SnapshotStore` 端口；SQLite + `github.com/mattn/go-sqlite3`，WAL 和批量写入 |
| 测试 | Go test、go vet、前端 Vite build；前端交互切片后增加 Vitest 和 Wails 端到端测试 |

## 3. 总体架构

```text
Wails Window
  React Web UI + D3
        |
类型化 Go-JS 绑定 + Wails 事件
        |
Application 用例层
        |
Domain: Scan / Cleanup / Recommendation
        |
Ports: Scanner / SnapshotStore / Trash / Permission
        |
macOS Platform Adapters
```

### 依赖规则

- Web 只能调用 Wails 暴露的白名单方法和事件。
- Wails 层只做参数转换、事件转发和窗口能力，不实现业务规则。
- Application 编排用例，不能直接依赖 macOS API。
- Domain 不依赖 UI、Wails、数据库或 Mole。
- Infrastructure 实现扫描、快照存储和任务运行；允许承载固定版本的 Mole 扫描代码。
- Platform 实现文件系统、权限、废纸篓、卷和 Finder 能力。
- Windows 后续只增加 Platform Adapter，不改变 Domain/Application 契约。

Mole 代码若复用，只能来自固定的 MIT 提交 `V1.40.0`，并放在 Infrastructure 的独立目录中。其输出必须先映射为 Marmot 自己的扫描节点和快照模型，不能让 Mole 的 JSON 或内部对象成为公共契约。

目录结构切片必须遵守 [项目目录规范](PROJECT-STRUCTURE.md)，业务切片不得绕过端口直接依赖
SQLite、Wails 或 macOS API。

## 4. Wails 运行与安全边界

生产构建嵌入前端资源，通过 Wails 绑定直接调用 Go 服务，不启动本地 HTTP 服务，也不把端口、Token 或 CORS 暴露给浏览器。

第一阶段采用 Developer ID 签名和公证的直装分发；App Store 沙盒、CLI 和 HTTP 服务不在当前范围。
当前机器只有 ad-hoc 签名能力，真实 Team ID/TCC 流程是发布前门禁。

开发模式可以使用 Vite 开发服务器，但开发服务器不是生产架构。未来若需要 CLI 或无界面服务，必须新增独立 Transport ADR，不能复用桌面应用的隐式权限边界。

Wails 暴露的方法必须：

- 只提供明确的领域用例；
- 使用强类型参数和返回值；
- 在 Go 边界校验路径、快照 ID、计划版本和能力；
- 不暴露任意命令执行、任意路径删除或任意文件读取方法；
- 只推送必要的扫描进度和结果事件。

## 5. 扫描架构

```text
ScanCoordinator
  -> 卷/权限探测
  -> 顶层目录快速发布
  -> 受限并发的目录遍历
  -> 元数据、卷身份和大小计算
  -> 硬链接/完整克隆去重
  -> APFS clone metadata 和快照对象识别
  -> 分批写入 SnapshotStore
  -> 更新目录汇总
  -> Wails 事件推送进度
```

必须采用成熟磁盘分析工具已经验证的策略：

- 先展示可用的顶层结果，再逐步补齐细节；
- worker 数量、目录遍历、外部命令和事件队列都必须有上限；
- 不能为每个待扫描目录无限创建 goroutine；
- 前端按目录懒加载、分页和排序查询，不能一次绑定百万节点；
- 结果分批提交，事件只推送汇总和进度，不推送每个文件；
- 缓存必须有 TTL、版本号、容量上限和失效策略；
- 扫描取消必须有明确的发布边界，不能在取消后继续发布新快照；
- 硬链接按卷内身份去重；完整克隆通过公开 `getattrlist` metadata 处理，部分共享块标记为未知或估算。
- 不跟随符号链接；挂载卷、系统快照、FileProvider 占位项和权限错误使用独立状态。
- 扫描是最终一致快照，变化、取消和权限问题必须保留。

### 5.1 分阶段、设备感知和缓存契约

扫描任务必须按以下阶段发布状态和结果：

```text
Catalog -> VolumeOverview -> TopLevelPublish -> DeepScan -> Finalize
```

- `Catalog`：列出挂载卷、容量、类型、权限和可扫描状态，不遍历文件树；
- `VolumeOverview`：读取卷根和直接子项元数据，尽快形成首层快照；
- `TopLevelPublish`：首层可查询后立即允许 UI 进入空间图和目录列表，深层扫描继续运行；
- `DeepScan`：按目录任务递归遍历，受设备和全局并发预算控制；
- `Finalize`：完成目录汇总、问题汇总、缓存复核和快照终态。

`VolumeCatalog` 必须为每个 `ScanScope` 返回 `DeviceProfile`：`ssd`、`rotational`、
`network_or_virtual` 或 `unknown`。第一版预算固定为：全局目录 worker 8 个；单 SSD 卷 4 个；
同一机械设备 1 个；网络/虚拟卷 2 个；未知设备 2 个；目录任务队列 4096；待提交批次 2。
这些预算不作为用户配置，队列满时生产端必须受控背压。

缓存仍由 SQLite 快照存储端口统一承载，不引入第二套完整内存树。缓存 TTL 为 24 小时，最多保留
3 个完整快照或 512 MB，先达到的限制生效。缓存命中必须标为旧结果并重新校验卷、文件身份、权限
和扫描器/口径版本；复核前不能作为当前事实或清理授权。

扫描进度每个任务最多 5 Hz，使用容量为 1 的最新值槽位；事件只发送阶段、汇总、问题数量、快照
版本和受影响父节点 ID，不发送单文件事件。取消观察点之后不得提交新的快照批次，已提交批次保留
为部分快照。该技术方案由 [ADR-0014](adr/0014-分阶段扫描与设备感知并发.md) 锁定。

## 6. 空间数据模型

节点不能只有一个 `size` 字段，至少需要：

```text
logical_size       文件逻辑长度
allocated_size     卷上实际占用估计
owned_allocated    去重后的归属占用
size_confidence    精确 / 估算 / 部分 / 未知
size_basis         计算口径和版本
```

Treemap/Sunburst 默认使用 `owned_allocated`；不可得时必须降级并在界面标明口径。

## 7. 快照和查询

`SnapshotStore` 必须支持：

- 按快照查询根节点和子节点；
- 按父节点分页、排序和过滤；
- 保存部分结果和错误；
- 保存扫描口径、版本和权限状态；
- 增量写入和取消后的安全收尾；
- schema 版本和过期策略。

每个新扫描快照必须持久化对应的 `taskId`。应用启动先将遗留的 `running` 快照标记为
`interrupted`；内存中找不到任务时，`GetScanStatus(taskId)` 通过 `SnapshotStore` 按任务 ID
查询已提交的部分结果。该查询不提供续扫能力。没有任务 ID 的旧快照不能伪造任务状态；清理计划
仍不跨进程持久化。该技术承载由 [ADR-0012](adr/0012-扫描任务身份与中断查询.md) 锁定。

首个切片的进程重启语义是：不续扫；上次仍为运行中的任务恢复为 `interrupted`，已提交
快照保留为部分结果。清理计划暂为会话内对象，跨进程持久化不在本阶段。

SQLite 使用 WAL、`synchronous=NORMAL` 和默认 10,000 节点批次；子节点查询使用
`snapshot_id, parent_id, owned_allocated DESC, id` 索引，单次最多返回 1000 节点。
本机合成 100 万节点基线约 159 MB 数据库、101 MB RSS、3.1 秒树形写入；门槛详见 R-004。

### 7.1 空间图查询契约

空间图和目录列表使用 `GetMap(MapQuery)`，不向前端传输完整树：

```text
MapQuery
  snapshotId
  parentId
  limit       默认 256，最大 1000
  offset      默认 0
  measure     owned_allocated（第一阶段固定）
```

```text
MapResult
  snapshotId
  snapshotVersion
  parent
  entries[]
  total / limit / offset / hasMore
  remaining
  confidence
```

`entries[]` 按 `owned_allocated DESC, nodeId` 稳定排序，单项为真实节点 `kind=node` 或空间聚合项
`kind=aggregate`。真实节点可以进入、预览、Finder 定位或进入清理计划，但每个操作仍由 Application
按快照和节点 ID 重新校验。聚合项只允许展开，不允许预览、定位、清理，也不写入 `scan_nodes`。
`remaining` 和聚合项必须保留三种大小及可信度口径。单次 Wails 返回不得超过 256 KB。

前端只保存当前层、面包屑和最近最多 32 个目录页的可丢弃 DTO 缓存。D3 只负责布局和交互；当前层
收到受影响父节点事件后以 250 ms 防抖重新查询，响应版本过期则丢弃旧页。该数据契约由
[ADR-0013](adr/0013-DaisyDisk空间图与渐进查询数据契约.md) 锁定。

## 8. 用例接口

Wails 对外暴露的接口先按行为定义：

| 用例 | 输入 | 输出 |
| --- | --- | --- |
| 查询卷概览 | 无或权限范围 | 卷身份、容量、类型、权限和可扫描状态 |
| 开始全盘扫描 | 扫描选项 | 扫描任务 ID |
| 查询扫描状态 | 任务 ID | 状态、进度、卷、权限和问题 |
| 查询子节点 | 快照 ID、父节点 ID、分页条件 | 节点、汇总和限制 |
| 查询空间图层 | `MapQuery` | `MapResult`，包含聚合项和快照版本 |
| 取消扫描 | 任务 ID | 最终状态 |
| 预览节点 | 快照 ID、节点 ID | Quick Look 调用结果 |
| Finder 定位节点 | 快照 ID、节点 ID | Finder 调用结果 |
| 创建清理计划 | 快照 ID、候选项、策略 | 计划 ID、原因和估算 |
| 校验清理计划 | 计划 ID、版本 | 总体和逐项结果 |
| 确认清理计划 | 精确版本 | 确认结果 |
| 执行清理计划 | 已确认计划 ID | 逐项执行结果 |

Wails 事件至少包括扫描进度、扫描问题、快照更新、清理进度和清理结果。扫描事件只携带摘要和受
影响父节点，不承担节点传输。客户端断线或窗口重开后，必须通过查询恢复状态。

Preview/Reveal 的 Wails 输入只能是 `snapshotId + nodeId`，不能接收任意路径、URL、命令或 Shell
参数。Application 通过快照取得并校验路径后，调用 macOS Platform 的 Quick Look 或 Finder 端口。

## 9. macOS 权限

- 全盘扫描必须在 Wails 应用身份下验证 Full Disk Access 流程。
- 每个卷和目录必须记录可访问、部分可访问或不可访问状态。
- 权限不足不能被当作空目录。
- 第一阶段不支持 root/管理员扫描，不通过隐式 shell 提权。
- Developer ID 直装分发是当前方案；App Store 沙盒不进入第一阶段。
- 真实签名、Full Disk Access 和公证仍需在发布环境完成 smoke test。

### 9.1 预览、Finder 定位和收集区

Platform 提供：

```text
PreviewPort.Preview(path, ownerWindow)
PreviewPort.Reveal(path)
```

macOS 预览使用 `QuickLookUI` 的 `QLPreviewPanel`/`QLPreviewItem`，Finder 定位使用
`NSWorkspace.activateFileViewerSelectingURLs`，不调用 `qlmanage`、`open`、`osascript` 或任意
Shell。原生 bridge 位于 Platform 层并遵守 AppKit 主线程和 Wails 窗口生命周期。

Collector 只是前端会话内的选择视图，最终必须映射为可审查的 `CleanupPlan`；加入 Collector 不
触发文件操作。聚合项、卷根、扫描根、特殊文件和权限不明对象不能加入计划。该边界由
[ADR-0015](adr/0015-macOS预览Finder定位与收集区平台边界.md) 锁定。

## 10. 清理安全

- 清理计划与扫描快照分离。
- 执行前重新检查路径、卷身份、节点类型、预期元数据和平台能力。
- 默认调用 macOS Foundation Trash 能力，不做永久删除。
- 清理项基于卷、device、inode、类型、大小和修改时间执行前重新校验。
- 父子清理项默认拒绝重叠计划，不能把扩大删除范围的选择静默替用户完成。
- 每个清理项独立返回成功、跳过或失败。
- 不承诺跨多个文件的原子回滚；恢复边界必须在 UI 和 SDD 中明确。

## 11. 第一条垂直切片

```text
Wails 启动
  -> 获取权限状态
  -> 扫描本机测试卷/目录
  -> Catalog/VolumeOverview/TopLevelPublish：顶层结果即时出现
  -> DeepScan：后台受限并发补齐结果
  -> 按需展开子目录
  -> Quick Look/Finder 预览和定位
  -> Collector 形成清理候选
  -> 创建并校验清理计划
  -> 用户确认
  -> 移入废纸篓
  -> 展示逐项结果
```

R-004、R-005、R-007 和 R-008 已完成本机验证，R-006 已完成 ad-hoc 打包验证；在真实签名/TCC
smoke test、跨卷废纸篓验证和真实只读全盘样本完成前，不宣称达到发布级全盘目标。

正式实现仍必须先完成：

1. 固定 Go/Wails 构建环境和 bundle identity；
2. 实现 SnapshotStore schema migration 和基准回归；
3. 实现 macOS 权限、APFS metadata 和 Trash adapters；
4. 用垂直切片验证取消、部分结果、重启恢复和计划版本。

在目录结构切片完成前，不扩大首个业务垂直切片范围；新增跨进程恢复能力必须先新增 ADR。

## 12. 产品体验与交互契约

产品和交互以 [R-009 DaisyDisk 产品体验与交互基线](research/R-009-DaisyDisk产品体验与交互基线.md)
为参考基线。Marmot 对齐其“先概览、渐进扫描、空间图下钻、预览、删除前复核”的体验链路，
但不复制第三方源码、品牌素材、文案或永久删除策略。

进入 UI 实现前必须满足：

- 启动后先展示本机挂载卷、容量口径、权限状态和扫描入口；
- 扫描必须先发布可用的顶层/首批结果，后台继续补齐，并提供取消和部分结果；
- 空间图按 `owned_allocated` 导航，单层懒加载、排序、聚合和分页，不能一次传输百万节点；
- 选中对象可以预览，清理必须先进入可审查的计划，再确认、复核和执行；
- 权限不足、隐藏空间、聚合对象、文件变化和大小不确定性必须显式表达；
- 设备感知并发、扫描阶段、缓存、空间图数据载荷和 Quick Look 能力已经分别由
  [ADR-0014](adr/0014-分阶段扫描与设备感知并发.md)、[ADR-0013](adr/0013-DaisyDisk空间图与渐进查询数据契约.md)
  和 [ADR-0015](adr/0015-macOS预览Finder定位与收集区平台边界.md) 锁定，后续实现不得绕过这些边界。

参考产品允许永久删除，Marmot 不采用该差异：Marmot 的默认动作仍是移入 macOS 废纸篓，
执行前必须重新校验文件身份和计划版本。R-009 的 P0 清单是首个体验垂直切片的验收入口，
P1/P2 能力不得在没有对应 SDD 条目和 ADR 的情况下直接实现。
