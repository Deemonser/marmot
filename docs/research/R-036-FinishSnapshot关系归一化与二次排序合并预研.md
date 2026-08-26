# R-036 FinishSnapshot 关系归一化与二次排序合并预研

状态：代码切片已实施；目标未达成，正确性实现保留，结论由 ADR-0036 接受

日期：2026-08-24

## 1. 目标

减少 `FinishSnapshot` 在目录汇总 override 存在时对 relation 临时文件的整文件写读。当前流程先按
`nodeID` 合并 relation、node metadata 和 override，写出 normalized relation；随后再次读取该文件并按
`parentID + owned_allocated DESC + nodeID` 排序。R-036 将这两步合并为有界的“流式归一化 -> 固定块排序 ->
外部 run 归并”。

本切片目标为：在同一真实 `/` 样本和串行条件下，`FinishSnapshot` 三次中位数推进到 `2.70s` 以内，
完整可查询二进制终态三次中位数推进到 `19.00s` 以内；纯扫描、查询、checksum、恢复和最大 RSS 不因
本切片回退超过 `3%`。完整终态 `<=15s` 仍是独立产品总门槛。

## 2. 证据

R-034 基线真实 `/` 三次完整终态中位数为 `19.473s`，`FinishSnapshot` 中位数为 `3.227s`。
R-035 引入归并堆和排序块连续缓冲后，稳定符号化 profile 仍观测到：

- `normalizeRelations` 累计约 `160ms`；
- normalized relation 后的第二次 `externalSortFixed` 累计约 `540ms`；
- 三路首轮排序累计约 `490ms`、`370ms`、`150ms`；
- R-035 的单次稳定样本 `FinishSnapshot` 为 `3.084s`；三次实测受 APFS/cache 波动影响为 `3.229s`、
  `5.145s`、`4.884s`，中位数未达到 R-035 的 `2.90s` 目标，不能宣称该目标已完成。

第二次 relation 排序的输入已经按 `nodeID` 排序，且归一化只修改固定记录中的三个大小字段；将归一化结果
直接放入 `64K` 固定块并按最终 relation 键排序，可以删除 normalized relation 的完整写入和再次读取，
同时保留外部排序的内存上限。

## 3. 方案比较

### 方案 A：流式归一化并直接生成最终 relation runs

采用。按现有 `nodeID` 顺序读取 relation/node/override，逐条应用同一覆盖和错误校验；归一化后的固定
记录写入有界连续块，块满后按最终查询键排序并生成 run，最后复用 R-035 小顶堆归并。没有 override 时
直接对 raw relation 按最终键排序。

### 方案 B：把 override 全部加载为内存 map，再对 relation 单次排序

拒绝。目录数和 override 数量随全盘节点线性增长，map 会扩大 RSS，且丢失当前以排序文件完成的容量边界。

### 方案 C：取消 nodeID 排序，按 parent 顺序直接应用 override

拒绝。override 以 node ID 排序，node metadata 与 relation 的 node ID 对齐是当前结构校验的一部分；取消
会放宽未知节点、数量不匹配和目录类型校验。

### 方案 D：改变 relation 记录或最终 index 格式

拒绝。查询索引、reader、checksum、恢复和已发布快照没有迁移需求，不能将格式变化混入临时 I/O 优化。

## 4. 结论与边界

- 保留 raw relation、node metadata 和 override 的 `nodeID` 外部排序及 `nodeID` 对齐校验。
- 将当前 `normalizeRelations` 和第二次 `externalSortFixed` 合并为单个有界源：归一化记录进入固定容量
  连续块，按现有 `lessRelation` 排序后写 run，再使用 R-035 小顶堆归并。
- override 的重复 node ID、未知 node、非目录 override、relation/node 数量不匹配和尾部残留校验保持不变；
  每个 node 的最后一条 override 仍按现有排序结果生效。
- 无 override 时允许跳过归一化源，直接对 raw relation 生成最终排序结果；不改变空输入和临时文件错误语义。
- 不再创建 normalized relation 临时文件；raw relation、排序 run 和最终临时 relation 仍由当前调用链拥有并
  在成功/失败路径清理。
- 最终 relation、node/directory/child/issue/string section、footer、checksum、index/manifest `Sync`
  和原子发布保持不变；不改变快照格式、SnapshotStore、Wails 或领域模型。

## 5. 验收

1. 新增 relation override 流式归一化测试：覆盖无 override、单/重复 override、未知节点、非目录 override、
   数量不匹配、尾部残留和多 run 最终排序。
2. binary snapshot 的分页/Map、checksum、恢复、合成百万级 POC 和应用 smoke 通过。
3. `go test -race ./internal/application ./internal/infrastructure/scanner`、`go vet ./...`、前端生产
   构建和 `git diff --check` 通过。
4. 真实 `/` 串行三次记录完整终态、`FinishSnapshot`、节点/问题数量、查询、checksum、恢复和最大 RSS。
5. 只有 `FinishSnapshot` 中位数 `<=2.70s`、完整终态中位数 `<=19.00s` 且其他指标无超过 `3%` 回退，
   才关闭本切片；未达成时保留正确性实现并记录证据，不调整阈值。

## 6. 对 DDD、SDD 和后续实现的影响

DDD 不变：relation 只是快照 Infrastructure 的内部索引构建资料，不改变节点、目录汇总或清理领域语义。
SDD 只明确 normalized relation 不再作为独立临时文件存在，归一化与最终排序必须在有界固定块流水中完成。

建议 ADR：[ADR-0036 FinishSnapshot 关系归一化与二次排序合并](../adr/0036-FinishSnapshot关系归一化与二次排序合并.md)。

## 7. 实施结果

已实施按 `nodeID` 流式对齐、override 归一化直接进入最终 relation 排序 run，并删除 normalized relation
完整中间文件；新增的 override、排序、分页/Map、checksum 和恢复回归通过。真实 `/` 串行三次结果为：

| 轮次 | 完整终态 | FinishSnapshot | 节点 | 问题 |
| --- | --- | --- | ---: | ---: |
| 1 | `19.651s` | `3.064s` | 约 `2,846,000` | 约 `441` |
| 2 | `20.059s` | `3.250s` | 约 `2,846,000` | 约 `441` |
| 3 | `19.651s` | `2.985s` | 约 `2,846,000` | 约 `441` |

完整终态中位数为 `19.651s`，`FinishSnapshot` 中位数为 `3.064s`，均未达到本切片目标。该切片保留正确性
和 I/O 减少的实现，下一步转向 R-037 的 Darwin 原生节点路径转换分配。
