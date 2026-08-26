# R-029 FinishSnapshot 临时索引同步与终态性能预研

状态：已完成，结论由 ADR-0031 接受；代码切片已实施，切片目标已达成，总性能门槛仍未完成

日期：2026-08-24

## 1. 目标

在不削弱快照已提交数据帧、最终索引、校验和和 manifest 原子发布边界的前提下，降低
`FinishSnapshot` 的临时索引 I/O 成本。

本切片目标是将当前真实 `/` 约 `4.11s` 的 `FinishSnapshot` 三次中位数推进到 `3.70s` 以内；
完整可查询终态仍以三次中位数 `<=15s` 为总门槛，不能用本切片目标替代总门槛。

## 2. 基线与证据

当前 macOS arm64、Go 1.25.13，真实 `/` 同一工作区三次完整终态记录为：

| 轮次 | 完整终态 | FinishSnapshot | 节点 | 目录 | 最大 RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | `20.504s` | `4.054s` | 2,846,083 | 585,744 | `611,942,400` |
| 2 | `25.731s` | `4.127s` | 2,846,083 | 585,744 | `619,282,432` |
| 3 | `20.895s` | `4.112s` | 2,846,083 | 585,744 | `611,860,480` |

当前队列收紧后的符号化 CPU profile：

- `Writer.buildIndex` 累计约 `4.42%`，wall time 约 `4.05s`；
- `externalSortFixed` 累计约 `3.53%`；第二次 relation sort 在 `buildIndex` 内累计约 `650ms`；
- `normalizeRelations` 累计约 `160ms`；最终索引段的 `readAtFull` 约 `940ms`，
  `readOverrideStrings` 约 `630ms`；
- 扫描主成本仍是 macOS 原生外部代码约 `79.43%`，因此本切片不能承诺总终态一定下降到 15 秒。

代码审查确认 `externalSortFixed` 会为每个临时 run 执行 `Flush -> Sync -> Close`，合并文件也会
执行 `Sync`；`normalizeRelations` 生成的中间文件同样执行 `Sync`。这些文件只在当前
`buildIndex` 调用链中读取，最终会被删除，不会写入 manifest，也不能被 `OpenCommitted` 查询。
按本轮约 `284.6` 万节点、`58.6` 万目录、64K records/run 估算，节点、relation、override 和
二次 relation sort 合计约 140 个临时 run，形成大量不必要的同步边界。

## 3. 方案比较

### 方案 A：只取消进程私有临时文件的 `Sync`

采用。临时 run、合并结果和 relation normalized 文件继续 `Flush` 并 `Close`，保证当前进程后续
读取到完整内容；不为它们提供崩溃后可恢复语义。保留最终索引段、最终 index 临时文件、数据帧和
manifest 的 `Sync`。

### 方案 B：关闭所有 `Sync`

拒绝。`AppendBatch` 的数据帧、最终索引段、原子 index 临时文件和 manifest 是已提交快照的完整性/发布
边界，不能为了终态秒数移除。

### 方案 C：本轮改写 relation 二次排序或快照格式

暂不采用。二次排序可能由最终目录大小覆盖改变排序键，当前 profile 只能支持先移除临时同步成本；
改变格式或覆盖合并算法需要独立的排序正确性、恢复和迁移预研。

## 4. 决策与边界

- `externalSortFixed` 的 run 文件和 merged 文件不执行 `Sync`；保留写入、`Flush`、`Close` 和错误传播；
- `normalizeRelations` 的进程私有中间文件不执行 `Sync`；保留 `Flush`、`Close`、清理和关系计数校验；
- `buildIndex` 在最终索引 section 写完后仍逐段 `Sync`；
- `writeIndexAtomic` 生成的最终 index 临时文件仍执行 `Sync` 后再 rename；
- `AppendBatch` 写入的数据帧仍执行 `Sync`，footer、SHA-256、索引计数和 manifest 原子发布不变；
- 临时文件异常、关闭失败或排序失败仍必须结束 `FinishSnapshot`，不得静默发布不完整索引；
- 不改变 `SnapshotStore`、查询分页/Map、Wails、取消、部分快照和清理前身份校验契约。

## 5. 验收与测量

1. 只有进程私有排序中间文件取消 `Sync`，最终数据/索引/manifest 的同步和校验路径仍存在；
2. binary snapshot、application、scanner 窄测试、相关 race、`go vet ./...` 和前端构建通过；
3. 合成百万节点快照的排序顺序、目录汇总、问题索引、分页、Map、尾部恢复和坏校验测试通过；
4. 真实 `/` 串行三次记录纯扫描、完整终态、`FinishSnapshot`、节点/问题数量、RSS 和快照文件大小；
5. 三次 `FinishSnapshot` 中位数 `<=3.70s` 且完整终态、快照校验和查询无回归，才关闭本切片；否则保留
   当前实现并记录波动，不以单次快跑宣称收益。

本轮代码切片后的真实 `/` 串行复测如下：

| 轮次 | 完整终态 | FinishSnapshot | 节点 | 问题 | 最大 RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | `20.079s` | `3.576s` | 2,846,120 | 442 | `600,309,760` |
| 2 | `19.886s` | `3.373s` | 2,846,120 | 441 | `650,199,040` |
| 3 | `20.491s` | `3.623s` | 2,846,120 | 441 | `629,817,344` |

三次完整终态中位数为 `20.079s`，`FinishSnapshot` 中位数为 `3.576s`，相对 R-028 的约 `4.11s`
下降约 `13%`；最大 RSS 仍受 Go allocator 和 macOS 文件系统缓存影响，不能据此宣称内存降低。
节点和查询、校验结果保持可用，15 秒总门槛仍未完成。

## 6. 对 DDD、SDD 和后续实现的影响

DDD 不变。SDD 只补充已发布快照的同步边界与进程私有中间文件的生命周期；实现已修改
`internal/infrastructure/snapshot/binary/index_builder.go` 的临时文件同步调用，不改变文件格式。
若下一步要消除 relation 二次排序、改用 mmap 或调整索引格式，必须新增预研与 ADR。
