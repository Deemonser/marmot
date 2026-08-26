# ADR-0046：FinishSnapshot 关系归并 run 块与缓冲

- 状态：Accepted
- 日期：2026-08-24
- 实施状态：代码切片已完成；正确性门禁全部通过，性能收益在可用的确定性环境中未测出
  （合成百万节点 POC 的 `user` CPU 中位数 before/after 均为 `2.03s`），详见
  [R-046 第 7 节](../research/R-046-FinishSnapshot关系归并run块与缓冲预研.md)。真实 `/` 三次 wall
  中位数门禁已由 [ADR-0047](0047-用户可见终态解耦与内存目录树查询权威.md) 第 4 条判定为本机不可测。
- 相关预研：[R-046](../research/R-046-FinishSnapshot关系归并run块与缓冲预研.md)

## 背景

真实 macOS `/` 快照约有 285 万节点。`FinishSnapshot` 的 parent relation 归并当前使用 `64K` 固定记录块，
会形成约 44 个临时 run，再执行多路读取和归并。profile 表明这条路径存在可测的临时文件打开、读取和写入等待。

## 决策

仅对 parent relation 的 source 外排使用专用 `256K` 固定记录块。run 仍写入进程私有临时文件，merge 仍使用有界
小顶堆和每个 run 的单条记录缓冲；node relation 和 override 的通用外排容量保持原值。

该决策不改变：

- relation 比较键 `parentID + owned_allocated DESC + nodeID`；
- nodeID 对齐、override 校验、空目录和目录计数校验；
- 数据帧/footer/SHA-256、最终 index checksum、manifest、Sync 和原子发布；
- `SnapshotStore` 查询、分页、Map、取消、恢复和 Wails DTO。

## 取舍

- 优点：减少 relation run 数和 merge 文件数量，增加的内存是固定上限，改动集中在 Infrastructure；
- 代价：relation 排序阶段额外保留约十几 MiB 的排序缓冲，峰值 RSS 必须通过真实 smoke 复测；
- 不确定性：APFS cache、权限和文件变化会影响 wall time，必须用同一 `/` 串行三次中位数判断；
- 未解决项：若本切片不足以达到目标，下一步应重新评估最终 index 的直接流式写入，而不是继续放大 run 块。

## 验收门禁

- `FinishSnapshot` 三次中位数 `<=4.00s`；
- 完整可查询终态三次中位数 `<=20.50s`；
- 节点/问题数量、首层分页、Map、checksum、恢复和取消无回归；
- 最大 RSS、纯扫描和查询相对 R-045 不回退超过 `3%`；
- 未达到以上目标时只能记录为“正确性通过、性能目标未达成”。
