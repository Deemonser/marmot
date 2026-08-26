# Marmot

Marmot 是一个 macOS 本地磁盘空间分析与安全清理工具，目标体验类似 DaisyDisk，底层扫描策略参考 Mole 和 GrandPerspective。

## 当前阶段

文档基准、技术预研和核心架构决策已完成，目录结构切片、扫描/清理基础 P0 和
DaisyDisk 交互状态模型和 ADR-0018 视觉版式第一版已落地；当前实现已包含
悬停上下文、单击下钻、键盘焦点、
有限浏览历史、聚合/虚拟对象能力边界和 Collector 状态机。真实签名/TCC、Quick Look
原生窗口、跨卷废纸篓和真实全盘样本仍是发布前验证项。扫描存储重做已由 ADR-0028 锁定，
当前 SQLite 实现仅作为过渡代码；追加式二进制快照格式和 `SnapshotStore` 生产适配器已经接入
主程序，Darwin 原生扫描主循环已经接入并通过真实 `/` 纯扫描和二进制快照终态 smoke；R-044 正确性已通过
但性能子目标未达成，当前按 ADR-0045 处理 Darwin 原生回调锁争用；完整终态的 15 秒性能目标仍未完成。

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
- [二进制快照数据帧发布屏障同步预研](docs/research/R-039-二进制快照数据帧发布屏障同步与终态性能预研.md)
- [Darwin 原生节点名称路径共享缓冲预研](docs/research/R-040-Darwin原生节点名称路径共享缓冲预研.md)
- [Darwin 目录状态映射合并预研](docs/research/R-041-Darwin目录状态映射合并预研.md)
- [FinishSnapshot 数据帧校验与问题索引单次遍历预研](docs/research/R-042-FinishSnapshot数据帧校验与问题索引单次遍历预研.md)
- [FinishSnapshot 节点关系联合排序预研](docs/research/R-043-FinishSnapshot节点关系联合排序预研.md)
- [FinishSnapshot 关系归并直接消费预研](docs/research/R-044-FinishSnapshot关系归并直接消费预研.md)
- [Darwin 原生扫描回调锁争用削减预研](docs/research/R-045-Darwin原生扫描回调锁争用削减预研.md)
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

当前本机 Darwin 原生 `/` 纯扫描与完整终态必须分开测量；完整终态包含批次持久化、索引构建、校验和
manifest 发布。R-034 后真实 `/` 完整可查询二进制终态三次为 `19.473s`、`19.489s`、`19.472s`，
中位数 `19.473s`；`FinishSnapshot` 分别为 `3.227s`、`3.241s`、`3.178s`，中位数 `3.227s`。
三次均为 `completed_with_issues`，节点约 `284.6` 万，问题 `441`，首层和根节点查询均已完成；完整终态
`15s` 门槛仍未通过。
旧 SQLite Application 快照落盘 smoke 记录约 `20.06s`，该实现已被 ADR-0028 标记为过渡方案。
追加式快照 POC 和生产 `SnapshotStore` 适配器已完成合成 100 万节点、分页、Map、校验和、尾部恢复、
目录汇总覆盖和应用级回归验证。纯扫描和完整终态受 macOS 文件变化、权限和缓存状态影响，不能用其中
一个指标替代另一个。
依据见 [R-024](docs/research/R-024-SQLite扫描写入并发与端到端性能复测.md)、[R-026](docs/research/R-026-macOS原生扫描主循环与非SQLite快照预研.md) 和 [ADR-0028](docs/adr/0028-macOS原生扫描与追加式二进制快照.md)。

R-027 已完成：由 [R-027](docs/research/R-027-macOS原生扫描资源复用与完整终态性能预研.md) 和
[ADR-0029](docs/adr/0029-macOS原生扫描资源复用与性能切片.md) 固定：复用原生 worker 的 bulk/节点
缓冲，并按有效 bulk 页分配 ID；不改快照格式、完整性校验、设备并发预算或公共接口。目标仍以相同条件
真实 `/` 三次完整可查询终态中位数 `<=15s` 为总门槛。

R-028 已完成：由 [R-028](docs/research/R-028-应用持久化队列上限与终态内存预研.md) 和
[ADR-0030](docs/adr/0030-应用持久化队列上限与终态内存.md) 固定：Application 持久化事件队列容量为
`2`，`scanBatchSize` 仅作为 SnapshotStore writer 的聚合阈值，队列满时形成可取消背压；不丢节点，
不关闭快照校验或改变公共契约。

R-029 仅取消进程私有排序中间文件的 `Sync`，保留数据帧、最终索引、原子 index 和 manifest 的同步/校验；
其 `FinishSnapshot` 子目标已经达到，但完整终态 `<=15s` 仍是独立总门槛。

R-029 已完成：真实 `/` 三次完整终态为 `20.079s`、`19.886s`、`20.491s`，中位数 `20.079s`；
`FinishSnapshot` 为 `3.576s`、`3.373s`、`3.623s`，中位数 `3.576s`。索引临时同步切片达到目标，
但剩余主要成本仍在 macOS 原生扫描路径，15 秒总门槛继续作为下一目标。

R-030 目标由 [R-030](docs/research/R-030-Darwin原生路径边界检查与长度复用预研.md) 和
[ADR-0032](docs/adr/0032-Darwin原生路径边界检查与长度复用.md) 固定：Darwin 原生扫描对挂载 boundary
使用预计算长度和无分配组件前缀比较，实际子目录 path 复用已知长度；不改变挂载边界、fd/openat、并发
预算、快照格式、完整性校验或公共契约。切片目标为真实 `/` 纯扫描三次中位数 `<=12.50s`、完整可查询
二进制终态三次中位数 `<=19.50s`；实测完整终态中位数为 `20.286s`，性能子目标未达成，完整终态
`<=15s` 仍是独立总门槛。R-033 目标由 [R-033](docs/research/R-033-FinishSnapshot临时字符串引用复用预研.md)
和 [ADR-0033](docs/adr/0033-FinishSnapshot临时字符串引用复用.md) 固定：对进程私有 override 字符串
按值复用并在 `buildIndex` 按引用缓存，目标是 `FinishSnapshot` 中位数 `<=3.20s`、完整终态中位数
`<=19.80s`，不改变快照格式、校验或发布边界。

R-034 已完成：由 [R-034](docs/research/R-034-应用扫描状态记账有界化预研.md) 和
[ADR-0034](docs/adr/0034-应用扫描状态记账有界化.md) 固定：Application 只为目录保留顶层 ancestry
映射，并将扫描中 affected-parent 集合限制为 `256` 项；目标是完整终态三次中位数 `<=19.70s`，纯扫描
和 `FinishSnapshot` 不回退超过 `3%`，实测完整终态中位数 `19.473s`，目标达成，不改变扫描事实和公共契约。

R-035/R-036 的代码切片已经实施但目标未达成：R-035 三次真实 `/` 完整终态为 `19.493s`、`23.115s`、
`22.946s`，`FinishSnapshot` 为 `3.229s`、`5.145s`、`4.884s`；R-036 三次完整终态为 `19.651s`、
`20.059s`、`19.651s`，`FinishSnapshot` 为 `3.064s`、`3.250s`、`2.985s`。两轮均保留正确性实现，
不能宣称性能目标达成。

当前下一性能目标由 [R-037](docs/research/R-037-Darwin原生节点路径转换与分配削减预研.md) 和
[ADR-0037](docs/adr/0037-Darwin原生节点路径转换与分配削减.md) 固定：Darwin 原生节点转换使用已知
名称长度和规范化父路径直接拼接，保留完整 `scan.Node.Path` 和所有清理/快照契约。目标为纯扫描三次
中位数 `<=13.50s`、完整终态中位数 `<=19.30s`，其他指标不回退超过 `3%`，完整终态 `<=15s` 仍是独立
总门槛。

R-037 已实施：纯扫描三次为 `13.069s`、`12.823s`、`14.526s`，中位数 `13.069s`，达到纯扫描子目标；
分配 profile 从约 `1184.46MB` 降至 `1150.04MB`。完整二进制应用三次为 `25.493s`、`31.320s`、
`21.086s`，中位数 `25.493s`，未达到完整终态子目标；应用 profile 仍显示主要成本在 Darwin 原生外部代码，
本轮不能宣称产品总 `15s` 门槛达成。

R-038 已实施：Infrastructure 固定记录排序使用标准库泛型排序，已排序检查交换两个临时缓冲，保留原有
排序键、记录宽度、外部排序内存边界和所有发布/校验契约。真实 `/` 三次 `FinishSnapshot` 为 `6.778s`、
`6.406s`、`4.138s`，中位数 `6.406s`；完整终态为 `47.780s`、`29.727s`、`24.315s`，中位数 `29.727s`。
本轮排序 profile 的累计 CPU 约从 `0.68s` 降至 `0.44s`，但 wall time 子目标未达成，完整终态 `<=15s`
仍未达成。下一轮必须重新 profile 稳定的终态 I/O/Sync 或其他热点，不能把本轮 CPU 改善扩大为产品达标。

R-039 已实施：数据帧保留 footer/checksum，`AppendBatch` 只标记 dirty；`TopLevelPublish`、终态 `Finish`
和未完成 writer 的 `Close` 承担数据文件同步屏障，新增回归和完整性测试通过。真实 `/` 三次完整应用
终态为 `39.163s`、`38.355s`、`32.105s`，中位数 `38.355s`；`FinishSnapshot` 中位数 `5.961s`，纯扫描
中位数 `25.938s`。代码正确性已通过，但 `18.50s` 子目标未达成，产品总 `15s` 门槛仍未通过；下一轮
必须基于稳定 profile 新增预研和 ADR，不能把当前结果解释为性能达标。

R-040 已锁定下一切片：Darwin 原生回调为每个节点只分配一次完整路径 backing buffer，`Name` 从路径尾部复用；
不引用 C 批次内存，不改变节点、取消、挂载边界、快照格式或清理契约。目标是相对 R-037 分配 profile 下降至少
`5%`，真实 `/` 纯扫描不超过 `13.50s`，产品完整终态 `15s` 门槛仍单独追踪。依据见
[R-040](docs/research/R-040-Darwin原生节点名称路径共享缓冲预研.md) 和
[ADR-0040](docs/adr/0040-Darwin原生节点名称路径共享缓冲.md)。

R-040 实施后已消除独立 `C.GoStringN` 名称复制，分配 profile 从约 `1150.04MB` 降至 `1138.52MB`，约下降
`1.0%`；路径、取消、快照和查询回归通过，但 `5%` 分配目标及真实 `/` wall-time 子目标未达成。当前三次
纯扫描中位数 `30.899s`、完整终态中位数 `35.117s`，与历史样本不可比；产品完整终态 `15s` 仍未达成，不能
把本轮分配改善解释为产品性能达标。

R-041 已实施：将 Darwin 扫描上下文重复的 `paths`/`dirParents` 合并为一张私有 `directories` 状态 map，
两次同口径分配 profile 为 `1120.83MB` 和 `1102.12MB`，相对 R-040 的 `1138.52MB` 下降 `1.55%` 和 `3.20%`，
达到本切片的 `1.5%` 目标；扫描事实、取消、快照、查询和清理契约不变。真实 binary smoke 一次终态为
`20.455s`，仍未达到产品 `15s` 总门槛，不能用分配收益替代完整三次中位数验收。依据见
[R-041](docs/research/R-041-Darwin目录状态映射合并预研.md) 和
[ADR-0041](docs/adr/0041-Darwin目录状态映射合并.md)。

R-042 已锁定下一切片：在保留每个已提交数据帧完整 header/footer/checksum 校验的前提下，将
`buildIssueSection` 的校验和 batch header 读取合并为一次遍历，目标为 `FinishSnapshot` 三次中位数
`<=3.80s`、完整终态三次中位数 `<=19.80s`。依据见
[R-042](docs/research/R-042-FinishSnapshot数据帧校验与问题索引单次遍历预研.md) 和
[ADR-0042](docs/adr/0042-FinishSnapshot数据帧校验与问题索引单次遍历.md)。

R-042 已实施：共享已提交数据帧解析器，保留 header/footer/SHA-256 校验、坏尾部恢复和终态拒绝发布语义；
窄回归、问题索引、分页、Map、目录汇总和取消链路通过。真实 `/` after smoke 为完整终态 `21.650s`、
`FinishSnapshot` `4.217s`，节点 `2,854,358`、问题 `443`；`buildIssueSection` CPU 约从 `0.88s` 降至
`0.35s`，但 R-042 的 `3.80s/19.80s` 子目标未达成，产品 `15s` 仍未达标。

R-043 已实施联合记录方案：百万节点 override POC 约 `2.58s`，联合记录、override、查询和恢复正确性通过；
真实 `/` 三次完整终态中位数 `55.663s`、`FinishSnapshot` 中位数 `11.392s`，`3.60s/19.50s` 子目标未达成。

R-044 已实施：visitor、空目录、override、查询、checksum 和恢复回归通过；真实 `/` 三轮 `FinishSnapshot`
中位数为 `6.82s`，完整终态中位数为 `34.15s`，`5.50s/20.50s` 子目标未达成。最新 mutex profile 显示
`nativeScanContext.addNodes` 约占 mutex delay 的 `98.7%`，争用等待约 `1.046s`。

当前下一性能目标由 [R-045](docs/research/R-045-Darwin原生扫描回调锁争用削减预研.md) 和
[ADR-0045](docs/adr/0045-Darwin原生扫描回调锁争用削减.md) 固定：将 parent path 快照后的路径/节点转换移到
全局锁外，每批只做一次状态合并并在锁外 emitter；目标是相对 R-044 完整终态中位数降低至少 `10%`，其他指标
不回退超过 `3%`，产品 `15s` 仍独立追踪。

## 当前不做

- Windows 实现。
- AI 自动删除。
- 永久删除和安全擦除。
- 当前 GPL `main` 代码和未固定版本的 Mole 代码集成。
- 一次性向前端传输百万级节点。
- 让空间聚合项绕过快照节点校验执行预览、定位或清理。
