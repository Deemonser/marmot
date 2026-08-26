# R-043 FinishSnapshot 节点关系联合排序预研

状态：代码切片已实施，正确性通过，性能子目标未达成

日期：2026-08-24

## 1. 目标

降低 `FinishSnapshot` 为百万级快照构建索引时的临时排序和对齐成本。当前每个节点同时写入
`nodeMetaRecord` 和 `relationRecord` 两个独立的 40 字节临时流，终态先分别按 node ID 外排，
再通过两个迭代器重新对齐，之后还要将应用过 override 的 relation 按父目录重新外排。

本轮目标：

- 将进程私有的 node metadata 与 relation 合并为一个 80 字节联合记录，按 node ID 只做一次初始外排；
- 保留流式 node ID 对齐、override 合并和最终 `parentID + owned_allocated DESC + nodeID` 顺序；
- 在同一真实 `/`、串行和权限条件下，使 `FinishSnapshot` 三次中位数推进到 `3.60s` 以内，完整可查询终态
  推进到 `19.50s` 以内；
- 节点、问题、分页、Map、取消、尾部恢复、checksum、manifest 和最大 RSS 不回退超过 `3%`；
- 产品完整终态 `15s` 仍是独立总门槛，不能由本轮子目标替代。

## 2. 问题与证据

R-042 after profile 使用真实 `/`、二进制 `SnapshotStore` 和同一 smoke 测试，完整终态为 `21.650s`，
`FinishSnapshot` 为 `4.217s`，节点 `2,854,358`、问题 `443`。CPU profile 中：

- `Writer.buildIndex` 累计约 `1.74s`；
- `externalSortFixed` 累计约 `1.26s`，其中 node metadata 和 relation-by-node 是两个独立初始排序流；
- `normalizeAndSortRelations` 仍需要流式读取已按 node ID 排序的 relation 和 node metadata；
- `writeIndexAtomic` 仍需将五个 section 复制为一个最终 index，属于后续独立边界。

当前 `AppendBatch` 对每个节点写入两个等长临时记录，总事实字节是 80 字节，但初始排序阶段需要分别
读取、分块、排序、写 run 和归并两次。两个排序结果在归一化阶段又通过两个迭代器逐条对齐。

## 3. 方案比较

### 方案 A：联合记录一次 node ID 外排

采用。定义只属于 Infrastructure 临时文件的 `nodeRelationRecord`，前半部保存现有
`nodeMetaRecord`，后半部保存现有 `relationRecord`。追加时仍保留原字段和写入顺序；终态按联合记录前部
的 node ID 排序，再从同一记录解码 metadata 和 relation。override 仍由独立的 node ID 排序流合并，归一化
后的 relation 继续进入现有有界最终 parent 排序流水。

联合记录会将单个排序块从 `64K * 40` 字节增至 `64K * 80` 字节，但仍按固定记录数量有界，约为 5 MiB，
不把全部节点或 relation 加载到内存。初始外排由三路（node、relation、override）降为两路（联合记录、override）。

### 方案 B：只增大现有排序 buffer

拒绝。只能减少部分 I/O 往返，无法消除 node/relation 两次排序和两路迭代器对齐的重复工作，收益不可控。

### 方案 C：把 relation 或 override 全部加载到 map

拒绝。会使内存随节点或目录数量增长，破坏当前百万级快照的有界外排约束，并可能放大真实 `/` RSS。

### 方案 D：把 parent 排序键直接写入公开快照或改变 index format

拒绝。本轮不需要格式迁移，不能为了终态性能改变查询、恢复或第三方兼容边界。

## 4. 实施边界

- 联合记录、临时文件名和排序比较器只属于 `internal/infrastructure/snapshot/binary`；不进入领域模型、
  `SnapshotStore` 端口或 Wails DTO。
- 联合记录的两个子记录必须使用当前宽度、字段编码和非负校验；node ID 不一致、数量不匹配、未知 override、
  非目录 override 和残留 override 继续报错。
- 初始 node ID 外排仍使用固定记录块、已排序快速路径、run/merge 临时文件和错误清理；联合记录块容量按记录数
  保持有界，不扩大为全量内存排序。
- 最终 relation 的父目录排序、目录汇总、分页、Map、问题索引、checksum、数据帧 footer、manifest Sync、
  原子 rename、取消、恢复和清理身份校验全部不变。
- 不修改公开二进制数据帧、index header、section 布局或 format version；旧的已发布快照无需迁移。

## 5. 验收

1. 新增联合记录编码/解码和无序批次回归，证明按 node ID 外排后 metadata/relation 仍一一对应。
2. binary snapshot 的 override、分页、Map、问题索引、checksum、尾部恢复、目录汇总和百万节点 POC 通过。
3. `go test ./...`、相关 race、`go vet ./...`、前端构建和 `git diff --check` 通过。
4. 真实 `/` 串行三次记录纯扫描、完整终态、`FinishSnapshot`、节点/问题、查询、checksum、恢复和最大 RSS。
5. 只有 `FinishSnapshot` 中位数 `<=3.60s`、完整终态中位数 `<=19.50s` 且其他指标无超过 `3%` 回退，才关闭本
   切片；否则保留正确性实现并记录证据，不调整产品 `15s` 门槛。

## 6. 对 DDD、SDD 的影响

DDD 不变。SDD 只明确 SnapshotStore Infrastructure 可以用联合的进程私有临时记录消除重复外排；扫描事实、
扫描快照、清理计划、公开快照格式和发布边界不变。

建议 ADR：[ADR-0043 FinishSnapshot 节点关系联合排序](../adr/0043-FinishSnapshot节点关系联合排序.md)。

## 7. 实施结果

代码已将每个节点的 metadata 和 relation 写入 80 字节联合临时记录，并按 node ID 只执行一次初始外排；
override 仍按 node ID 流式合并，最终 relation 仍按 parent 和 `owned_allocated` 有界排序。临时排序流只做
`Flush/Close`，公开数据帧、最终索引 section、checksum、manifest 和原子发布同步边界保留。

百万节点 override POC 通过：`1,000,000` 节点、`100,001` 个目录，当前写入/索引读取约 `2.58s`，
分页和目录覆盖查询通过。

真实 `/` 串行 smoke 结果如下。节点和文件系统状态随运行变化，不能把单次结果外推为产品保证：

| 轮次 | 完整终态 | `FinishSnapshot` | 节点 | 问题 |
| --- | ---: | ---: | ---: | ---: |
| 1 | `77.250s` | `27.490s` | `2,854,074` | `454` |
| 2 | `55.663s` | `11.392s` | `2,849,511` | `453` |
| 3 | `47.996s` | `8.010s` | `2,855,912` | `452` |
| 中位数 | `55.663s` | `11.392s` | - | - |

正确性、公开查询和恢复边界没有回退，但 R-043 的 `3.60s/19.50s` 性能子目标未达成，产品完整终态
`15s` 也未达成。当前 profile 和代码路径显示，已归一化的 relation 仍需先写入完整临时流，再由最终
index 构建重新读取；这成为 R-044 的直接优化边界。

## 8. 对下一轮的约束

R-044 可以让 parent 排序归并结果直接消费到 child/directory section，消除完整 normalized relation 临时
文件的写入和重读，但必须继续使用固定记录块和有界 merge；不能把百万级 relation 放入内存，也不能把
目录可信度、override 校验或空目录记录丢给前端补齐。
