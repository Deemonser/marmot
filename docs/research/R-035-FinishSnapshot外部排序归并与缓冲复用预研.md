# R-035 FinishSnapshot 外部排序归并与缓冲复用预研

状态：代码切片已实施；目标未达成，正确性实现保留，结论由 ADR-0035 接受

日期：2026-08-24

## 1. 目标

降低追加式二进制快照 `FinishSnapshot` 的外部固定记录排序成本，优先处理排序 run 归并阶段的重复比较
和排序块分配。范围只覆盖 Infrastructure 内部临时排序器，不改变关系归一化的两阶段流程、快照格式、
索引 section、校验和或原子发布边界。

本切片目标为：在同一真实 `/` 样本和串行条件下，`FinishSnapshot` 三次中位数推进到 `2.90s` 以内，
完整可查询二进制终态三次中位数推进到 `19.20s` 以内；纯扫描、查询和校验不得因本切片回退超过 `3%`。
完整终态 `<=15s` 仍是独立产品总门槛。

## 2. 证据

R-034 代码后的真实 `/` 三次完整终态为 `19.473s`、`19.489s`、`19.472s`，中位数 `19.473s`；
`FinishSnapshot` 为 `3.227s`、`3.241s`、`3.178s`，中位数 `3.227s`。节点约 `284.6` 万，问题
`441`。

本轮新鲜 profile 的单次样本为完整终态 `19.492s`、`FinishSnapshot` `3.016s`，节点 `2,846,286`，
问题 `441`。CPU profile 中：

- `externalSortFixed` 累计约 `1.60s`；
- `buildIndex` 累计约 `1.55s`，排序和临时文件读取仍是其中可见成本；
- 当前排序块大小为 `64K` 条记录；
- 多 run 归并每输出一条记录都线性扫描所有活动 run，活动 run 数量随输入规模增长；
- 排序块为每条记录单独分配 `[]byte`，形成额外的分配和 GC 压力。

profile 的 CPU 样本包含并发 worker 和索引 goroutine，不能把累计时间直接当作墙钟收益；因此本轮只选
可独立验证、内存有界且不改变数据布局的排序器切片。

## 3. 方案比较

### 方案 A：有界二叉小顶堆归并，并复用排序块缓冲

采用。每个 run 只保留一个当前记录，归并时从小顶堆取出最小项，写入后用同一记录缓冲读取下一项并
重新入堆。排序块使用连续 backing buffer 和记录切片，避免每条记录独立分配。排序键和已有快速路径
保持不变。

### 方案 B：把全部 relation 或 override 加载到内存并一次排序

拒绝。真实全盘节点数已达百万级，relation/override 数量与输入线性增长；无界内存会扩大 RSS，破坏
当前外部排序的容量边界，也不能由本轮证据证明在不同磁盘样本上安全。

### 方案 C：删除 relation 按 node ID 的归一化排序或第二次 parent 排序

拒绝。目录汇总 override 必须先与同 ID 的 relation 合并，查询索引又必须按
`parentID + owned_allocated DESC + nodeID` 稳定排序。删除任一阶段会破坏目录汇总、分页和 checksum 前的
结构校验；如要改变需要单独的关系布局预研和 ADR。

### 方案 D：修改快照格式或关闭临时/最终校验

拒绝。格式、footer、最终 index、manifest、checksum 和原子发布是已有恢复与完整性边界，不能为单次
性能切片放宽。

## 4. 结论与边界

- `externalSortFixed` 继续保留固定记录宽度校验和已排序输入快速路径。
- 归并阶段改为有界二叉小顶堆；比较器仍使用现有 `less`，比较相等时以 run 序号作为内部稳定 tie-break，
  不改变公开排序键。
- 单个排序块使用连续记录存储；块容量仍由 `externalSortChunkRecords` 固定，不按全盘节点数扩张。
- 每个 run 的当前记录复用同一缓冲；读取、写入、关闭、临时文件清理和错误传播保持原有边界。
- relation 仍先按 `nodeID` 排序，应用 override 后再按 `parentID + owned_allocated DESC + nodeID` 排序；
  不改变 normalized relation 文件的存在和清理语义。
- 进程私有 run、merge 和 normalized relation 仍只要求 `Flush/Close`；最终 index section、最终 index
  临时文件、数据帧和 manifest 的 `Sync`、checksum 与原子发布继续保留。
- 不改变节点顺序、目录汇总、分页、Map、部分结果、恢复、取消、Wails DTO、设备并发或领域规则。

## 5. 验收

1. binary snapshot 排序快速路径、无序输入、重复比较键、空输入、坏记录宽度和临时文件错误测试通过。
2. 合成百万级节点 POC、目录 override、分页/Map、checksum、恢复和应用级 smoke 无回归。
3. `go test -race ./internal/application ./internal/infrastructure/scanner`、`go vet ./...`、前端生产
   构建和 `git diff --check` 通过。
4. 真实 `/` 串行三次记录完整终态、`FinishSnapshot`、节点/问题数量、查询、校验和最大 RSS。
5. 只有 `FinishSnapshot` 中位数 `<=2.90s`、完整终态中位数 `<=19.20s` 且纯扫描/查询/校验无超过 `3%`
   回退，才关闭本切片；未达成时保留正确性实现并记录证据，不调整阈值。

## 6. 对 DDD、SDD 和后续实现的影响

DDD 不变：节点、目录汇总、快照、扫描状态和清理计划领域语义不变。SDD 只补充 Infrastructure 外部排序
的内存有界、归并复杂度和临时文件边界；不新增公共端口或快照字段。

建议 ADR：[ADR-0035 FinishSnapshot 外部排序归并与缓冲复用](../adr/0035-FinishSnapshot外部排序归并与缓冲复用.md)。

## 7. 实施结果

已实施二叉小顶堆归并、连续排序块和 run 记录缓冲复用，并通过排序回归、binary snapshot、checksum 和
恢复测试。真实 `/` 串行三次结果为：

| 轮次 | 完整终态 | FinishSnapshot | 节点 | 问题 |
| --- | --- | --- | ---: | ---: |
| 1 | `19.493s` | `3.229s` | 约 `2,846,000` | 约 `441` |
| 2 | `23.115s` | `5.145s` | 约 `2,846,000` | 约 `441` |
| 3 | `22.946s` | `4.884s` | 约 `2,846,000` | 约 `441` |

完整终态和 `FinishSnapshot` 中位数均未达到本切片目标。结果受 APFS/cache 波动影响，不能据此关闭 R-035；
实现继续作为后续索引优化的基础，不调整目标阈值。
