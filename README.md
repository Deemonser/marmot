# Marmot

Marmot 是一个 macOS 本地磁盘空间分析与安全清理工具，目标体验类似 DaisyDisk，底层扫描策略参考 Mole 和 GrandPerspective。

## 当前阶段

文档基准、技术预研和核心架构决策已完成，目录结构切片和扫描/清理基础 P0 已落地。
当前已确认下一条 UI 切片必须先完成 DaisyDisk 原生交互状态模型：悬停 Inspector、单击下钻、
键盘焦点、浏览历史、虚拟空间对象和 Collector 状态机。真实签名/TCC、Quick Look 原生窗口、
跨卷废纸篓和真实全盘样本仍是发布前验证项。

## 已确定方案

- 运行形态：Wails `v3.0.0-beta.9` 桌面应用，第一阶段只做 macOS。
- 后端：Go 1.25+，本机验证 Go 1.25.13。
- 前端：React + TypeScript + Vite。
- 可视化：D3.js，负责 Treemap、Sunburst 和目录导航布局。
- 生产通信：Wails 的类型化 Go-JS 绑定和应用事件，不启动本地 HTTP 服务。
- 分发方式：Developer ID 签名和公证的直装版；不支持隐式 root 扫描。
- 扫描方式：后台分层扫描、受限并发、渐进发布、缓存和按需加载；不跟随符号链接，按卷隔离。
- 扫描阶段：`Catalog -> VolumeOverview -> TopLevelPublish -> DeepScan -> Finalize`；全局 worker 8 个，按设备画像限制并发。
- 快照存储：SQLite WAL，10,000 节点批量写入，子节点分页最多 1,000 项。
- 空间图：`MapQuery`/`MapResult` 只传当前层，默认 256 项，截断结果使用不可操作的空间聚合项。
- macOS 体验：Quick Look 预览、NSWorkspace Finder 定位；Collector 只生成可审查清理计划候选。
- 交互基线：DaisyDisk 原生交互状态由 [ADR-0016](docs/adr/0016-DaisyDisk原生交互状态模型.md) 锁定，
  实现顺序由 [R-013](docs/research/R-013-DaisyDisk原生交互与开源参考复核.md) 的差距矩阵约束。
- 数据模型：逻辑大小、实际占用、去重后占用和结果可信度分开记录。
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
- [分阶段扫描预研](docs/research/R-010-分阶段扫描与设备感知并发.md)
- [空间图数据契约预研](docs/research/R-011-Sunburst空间图与渐进查询数据契约.md)
- [macOS 预览与收集区预研](docs/research/R-012-macOS预览Finder定位与收集区边界.md)
- [ADR 决策记录](docs/adr/README.md)
- [第三方代码声明](THIRD_PARTY_NOTICES.md)
- [Agent 规则](AGENTS.md)

当前门禁、已接受 ADR 和验证结果详见 [SDD](docs/SDD.md)、[DDD](docs/DDD.md)、
[文档基准](docs/BASELINE.md) 以及 [技术预研队列](docs/research/README.md)。

文档基线固定后，基础 P0 已按“扫描阶段与卷目录 -> 空间图查询与 Sunburst -> Quick Look/Finder ->
Collector 到 CleanupPlan”完成实现。DaisyDisk 原生交互重做是下一条已冻结切片，后续仍必须先满足
对应 SDD 契约和 ADR 验收标准。

## 当前不做

- Windows 实现。
- AI 自动删除。
- 永久删除和安全擦除。
- 当前 GPL `main` 代码和未固定版本的 Mole 代码集成。
- 一次性向前端传输百万级节点。
- 让空间聚合项绕过快照节点校验执行预览、定位或清理。
