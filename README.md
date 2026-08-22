# Marmot

Marmot 是一个 macOS 本地磁盘空间分析与安全清理工具，目标体验类似 DaisyDisk，底层扫描策略参考 Mole 和 GrandPerspective。

## 当前阶段

文档基准、技术预研和核心架构决策已完成，目录结构切片、扫描/清理基础 P0 和
DaisyDisk 交互状态模型和 ADR-0018 视觉版式第一版已落地；当前实现已包含
悬停上下文、单击下钻、键盘焦点、
有限浏览历史、聚合/虚拟对象能力边界和 Collector 状态机。真实签名/TCC、Quick Look
原生窗口、跨卷废纸篓和真实全盘样本仍是发布前验证项。扫描存储重做已由 ADR-0028 锁定，
当前 SQLite 实现仅作为过渡代码，新的扫描快照实现尚未开始。

## 已确定方案

- 运行形态：Wails `v3.0.0-beta.9` 桌面应用，第一阶段只做 macOS。
- 后端：Go 1.25+，本机验证 Go 1.25.13。
- 前端：React + TypeScript + Vite。
- 可视化：D3.js，负责 Treemap、Sunburst 和目录导航布局。
- 生产通信：Wails 的类型化 Go-JS 绑定和应用事件，不启动本地 HTTP 服务。
- 分发方式：Developer ID 签名和公证的直装版；不支持隐式 root 扫描。
- 扫描方式：后台分层扫描、受限并发、渐进发布、缓存和按需加载；Darwin 使用 `getattrlistbulk(2)` 批量元数据、预计算挂载边界和有界 `openat` fd；不跟随符号链接，按挂载边界隔离。
- 扫描反馈：扫描中始终停留在磁盘选择页，持续显示阶段、节点/文件计数、耗时、不确定进度条和取消入口；后端可先保存首层部分结果，但只有终态且最终空间图查询成功后才进入结果页。
- 扫描阶段：`Catalog -> VolumeOverview -> TopLevelPublish -> DeepScan -> Finalize`；全局 worker 8 个，SSD 使用 8 个并发，其他设备按画像限制并发。
- 快照存储：`SnapshotStore` 端口使用追加式二进制快照、目录索引和 `pread/mmap` 查询；不引入 SQLite 运行时依赖。
  批次、提交校验和、部分结果、取消恢复、子节点分页最多 1,000 项由 [ADR-0028](docs/adr/0028-macOS原生扫描与追加式二进制快照.md) 锁定。
- 空间图：`MapQuery`/`MapResult` 默认传当前层 256 项和有界多层投影，深度/投影预算由
  [ADR-0017](docs/adr/0017-有界多层空间图投影.md) 锁定，截断结果使用不可操作的空间聚合项。
- macOS 体验：Quick Look 预览、NSWorkspace Finder 定位；Collector 只生成可审查清理计划候选。
- 交互基线：DaisyDisk 原生交互状态由 [ADR-0016](docs/adr/0016-DaisyDisk原生交互状态模型.md) 锁定，
  本机实测由 [R-014](docs/research/R-014-DaisyDisk本机实机交互复核.md) 固化；多层空间图由
  [ADR-0017](docs/adr/0017-有界多层空间图投影.md) 锁定。
- 数据模型：逻辑大小、实际占用、去重后占用和结果可信度分开记录。
- 容量语义：卷自身占用、APFS 容器占用和空间图 `owned_allocated` 分开；macOS 挂载表决定扫描边界，
  Data 不通过路径追加到 `/` 的逻辑树。
- 存储入口：Platform 保留 APFS 技术卷事实，Application 按明确的卷组身份投影为 `StorageSource`；
  System/Data 在启动页合并为一个入口，成员容量仍独立保留，入口容量使用共享容器口径。
- APFS：完整克隆使用公开 `getattrlist` metadata；部分共享块保留未知，不伪造精确值。
- Mole：只复用固定的 MIT 版本 `V1.40.0` 中与扫描相关的代码，并放在 Infrastructure 层；当前 `main` 不使用。
- Windows：后续阶段，当前只保留平台端口和能力抽象。

## 第一条业务链路

```text
请求全盘扫描
  -> 显示顶层结果和进度
  -> 按需展开目录
  -> 创建清理计划
  -> 用户确认
  -> 通过 macOS 废纸篓执行
  -> 返回逐项结果
```

## 产品原则

- 全盘扫描必须明确展示权限限制和部分结果。
- 扫描事实、清理计划和执行动作必须分离。
- 默认移入废纸篓，不做永久删除。
- AI 只能提供带证据和风险的建议，不能执行文件操作。
- 前端不能直接调用文件系统。
- 生产版不开放本地 HTTP 控制面。

## 文档

- [DDD 领域设计](docs/DDD.md)
- [SDD 系统设计](docs/SDD.md)
- [文档基准](docs/BASELINE.md)
- [项目目录规范](docs/PROJECT-STRUCTURE.md)
- [技术预研](docs/research/README.md)
- [产品体验与交互基线](docs/research/R-009-DaisyDisk产品体验与交互基线.md)
- [DaisyDisk 交互与开源参考复核](docs/research/R-013-DaisyDisk原生交互与开源参考复核.md)
- [DaisyDisk 本机实机交互复核](docs/research/R-014-DaisyDisk本机实机交互复核.md)
- [有界多层空间图投影预研](docs/research/R-015-有界多层空间图投影预研.md)
- [DaisyDisk 视觉版式与窗口状态基线](docs/research/R-016-DaisyDisk视觉版式与窗口状态基线.md)
- [快照缓存生命周期与扫描中进度反馈](docs/research/R-021-快照缓存生命周期与扫描中进度反馈.md)
- [DaisyDisk 扫描中窗口状态复核](docs/research/R-025-DaisyDisk扫描中窗口状态复核.md)
- [macOS 原生扫描主循环与非 SQLite 快照预研](docs/research/R-026-macOS原生扫描主循环与非SQLite快照预研.md)
- [分阶段扫描预研](docs/research/R-010-分阶段扫描与设备感知并发.md)
- [APFS 卷组与全盘容量语义预研](docs/research/R-017-macOS_APFS卷组与全盘容量语义.md)
- [APFS 卷组与产品存储源映射预研](docs/research/R-018-APFS卷组与产品存储源映射.md)
- [空间图数据契约预研](docs/research/R-011-Sunburst空间图与渐进查询数据契约.md)
- [macOS 预览与收集区预研](docs/research/R-012-macOS预览Finder定位与收集区边界.md)
- [ADR 决策记录](docs/adr/README.md)
- [第三方代码声明](THIRD_PARTY_NOTICES.md)
- [Agent 规则](AGENTS.md)

当前门禁、已接受 ADR 和验证结果详见 [SDD](docs/SDD.md)、[DDD](docs/DDD.md)、
[文档基准](docs/BASELINE.md) 以及 [技术预研队列](docs/research/README.md)。

文档基线固定后，基础 P0 已按“扫描阶段与卷目录 -> APFS 挂载边界与产品存储入口 -> 空间图查询与 Sunburst -> Quick Look/Finder ->
Collector 到 CleanupPlan -> 交互状态模型 -> DaisyDisk 视觉版式”完成第一版实现；R-014/R-015/R-016
的验收标准和真实原生窗口 smoke test 仍是后续门禁。后续修改仍必须先满足对应 SDD 契约和 ADR 验收标准。

当前本机 `/` 纯扫描器 smoke 约 270 万节点、441 个问题，最近复测为 `15.75s`；
旧 SQLite Application 快照落盘 smoke 记录约 `20.06s`，该实现已被 ADR-0028 标记为过渡方案。
新快照格式尚未实现，完整可查询终态的本机三次中位数 `<15s` 是后续 POC 和实现验收目标，不能提前宣称达标。
依据见 [R-024](docs/research/R-024-SQLite扫描写入并发与端到端性能复测.md)、[R-026](docs/research/R-026-macOS原生扫描主循环与非SQLite快照预研.md) 和 [ADR-0028](docs/adr/0028-macOS原生扫描与追加式二进制快照.md)。

## 当前不做

- Windows 实现。
- AI 自动删除。
- 永久删除和安全擦除。
- 当前 GPL `main` 代码和未固定版本的 Mole 代码集成。
- 一次性向前端传输百万级节点。
- 让空间聚合项绕过快照节点校验执行预览、定位或清理。
