# ADR-0026 SQLite 扫描写入并发与端到端性能

状态：Accepted

日期：2026-08-21

## 背景

ADR-0007 固定了 SQLite 快照的安全耐久性和分页边界，ADR-0025 解决了目录汇总回写索引的主要瓶颈。真实 `/` smoke 仍显示 Application 在扫描器回调同步等待 SQLite，用户会看到长时间没有可解释反馈。

## 依据

- [R-024 SQLite 扫描写入并发与端到端性能复测](../research/R-024-SQLite扫描写入并发与端到端性能复测.md)
- [ADR-0007 SQLite 快照存储与性能门槛](0007-SQLite快照存储与性能门槛.md)
- [ADR-0023 快照缓存生命周期与扫描中进度反馈](0023-快照缓存生命周期与扫描中进度反馈.md)
- [ADR-0025 SQLite 目录汇总旁路存储](0025-SQLite目录汇总旁路存储.md)

## 决策

1. Application 和 Scanner 之间使用有界事件队列，容量固定为两个 50,000 节点提交批次；队列满时阻塞生产端，不创建无限 goroutine 或无限内存树。
2. 持久化 worker 每 50,000 个节点提交一次事务；事务内的 `scan_nodes` 插入和 `directory_sizes` upsert 使用 2,000 行 prepared multi-row statement，尾批单独准备。
3. `TopLevelPublish` 通过 FIFO barrier 等待首层节点落盘后再发布；深层扫描和持久化可以并行，进度事件继续限频。
4. 取消时停止接收新的扫描输出；队列中已经接收的有限节点允许完成收尾并作为部分快照保留，未接收的节点不得伪装成结果。
5. 新快照的 `scan_nodes` 只建立 `(snapshot_id, parent_id)` 定位索引；目录和文件的最终排序使用 SQL 稳定表达式叠加 `directory_sizes`，不再依赖无法覆盖 `LEFT JOIN` 汇总口径的占用复合索引。旧数据库不要求启动时重建旧索引，后续缓存维护再处理。
6. SQLite 继续使用 WAL、`synchronous=NORMAL`、单连接、64 MB 页缓存和内存临时排序。禁止为了本次性能目标改为 `synchronous=OFF`。
7. 纯扫描器 `<15s` 与完整 Application + SQLite smoke 分开记录；当前完整 smoke 约 20.06s，未达 `<15s` 时不得由 UI 或 README 宣称达标。

## 与既有 ADR 的关系

- 补充 ADR-0007 的批量事务实现和索引实现；安全耐久性、分页和内存边界仍以 ADR-0007 为准。
- 补充 ADR-0023 的扫描中反馈和取消收尾语义。
- 补充 ADR-0025 的目录汇总旁路存储；不改变 Domain、Application 公共用例、Wails 绑定或清理校验契约。

## 未采用方案

- 无限内存队列或 Application 完整内存树：拒绝；
- `synchronous=OFF`：拒绝，缓存可重建不等于可以接受数据库损坏风险；
- 在没有新预研的情况下引入 append-only spool 或专用数据库：拒绝，留给下一项独立预研；
- 用 APFS 容量或节点计数伪造确定百分比：拒绝。

## 后果

- 首层结果更早可查询，扫描和写入不再在每个回调上串行等待；
- 部分快照仍可能在完整终态前存在，UI 必须继续显示阶段、计数、问题和不确定进度；
- 50,000 节点是受控内存和取消粒度之间的折中，不开放为用户配置；
- 完整 Application + SQLite 仍可能超过原版 `<15s`，后续必须通过独立预研解决，不能继续漂移门槛。

## 验收标准

- 快照、Application、Scanner 单元测试以及取消和首层发布测试通过；
- `go test -race` 不报告写入 worker、任务状态和进度事件数据竞争；
- `/` 应用层 smoke 记录节点数、问题数和耗时，当前目标为相对同步实现明显改善，不能伪称 `<15s`；
- `go vet`、前端构建、Wails 构建和真实 Wails 窗口进度/首层展示 smoke 通过。
