# ADR-0031 FinishSnapshot 临时索引同步与终态性能

状态：Accepted

日期：2026-08-24

实施状态：代码切片已完成；`FinishSnapshot` 切片目标已达成，总 15 秒门槛仍未达到

## 背景

R-028/ADR-0030 修正 Application 持久化队列后，真实 `/` 三次 `FinishSnapshot` 为 `4.054s`、
`4.127s`、`4.112s`，中位数约 `4.11s`。符号化 profile 显示 `buildIndex` 和 `externalSortFixed`
仍是终态阶段的稳定可见成本。

`externalSortFixed` 对节点、关系和 override 的每个临时 run 以及合并文件调用 `Sync`；
`normalizeRelations` 的临时结果也调用 `Sync`。这些文件不进入 manifest，发布前会被删除，
崩溃后不能作为可查询快照恢复。对约 284.6 万节点样本，临时 run 数量约 140 个。

依据：[R-029](../research/R-029-FinishSnapshot临时索引同步与终态性能预研.md)、
[ADR-0028](0028-macOS原生扫描与追加式二进制快照.md)、
[ADR-0030](0030-应用持久化队列上限与终态内存.md)。

## 决策

### 1. 进程私有排序中间文件不提供持久化保证

排序 run、merge 输出和 normalized relation 文件只需要在当前 `buildIndex` 调用链内可读；它们继续
执行 `Flush` 和 `Close`，但不执行 `Sync`。任何写入、flush、close、读取或排序错误仍然原样返回。

### 2. 已发布快照边界保持同步和完整性

以下同步与校验不变：

- `AppendBatch` 的数据帧和 commit footer 写入后 `Sync`；
- 最终 node/directory/child/issue/string sections 写完后逐段 `Sync`；
- 最终 index 临时文件写完、SHA-256 写入后 `Sync`，再 rename；
- manifest 继续使用临时文件、`Sync` 和原子替换；
- reader 对已发布 index、数据范围、计数和 checksum 的验证保持不变。

### 3. 不改变公共契约和格式

本 ADR 不改变二进制格式、索引字段、排序规则、SnapshotStore 端口、Wails DTO、取消、部分结果、
查询分页/Map 或清理前身份校验。

## 不采用的方案

- 关闭数据帧或最终 index/manifest 的 `Sync`：会破坏已提交快照的崩溃恢复边界；
- 取消 SHA-256、footer、索引计数或 manifest 原子发布：违反 ADR-0028；
- 本轮直接去掉 relation 二次排序或改变快照格式：需要新的排序正确性和恢复证据。

## 后果

正面后果：

- 减少约 140 个进程私有临时文件的同步系统调用；
- 保持已发布快照的 durability、校验和原子可见性；
- 改动集中在索引构建内部，不影响领域层和应用契约。

代价和风险：

- 进程在中间排序期间崩溃时，临时文件可能只写入部分内容；它们本来就不属于可恢复快照，恢复流程必须
  继续依赖 manifest 和已提交 data/index；
- 若临时文件关闭后的读取在特定文件系统上表现异常，排序/校验必须报错且不得发布；
- 真实收益受 APFS 缓存状态和原生扫描波动影响，必须看三次中位数。

实施后真实 `/` 三次完整终态为 `20.079s`、`19.886s`、`20.491s`，中位数 `20.079s`；
`FinishSnapshot` 为 `3.576s`、`3.373s`、`3.623s`，中位数 `3.576s`；最大 RSS 为
`600,309,760`、`650,199,040`、`629,817,344` 字节。相对 R-028 的约 `4.11s` 中位数下降约 `13%`，
但完整终态仍明显高于 15 秒。

## 验收标准

- 只有进程私有中间文件移除 `Sync`；最终快照同步、校验和发布边界不变；（已完成）
- binary snapshot、application、scanner 测试和相关 race 通过；
- `go vet ./...`、前端构建、`git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=3.70s`，完整终态查询与恢复无回归；（已完成）
- 完整终态仍需单独满足三次中位数 `<=15s` 才算总目标完成。

## 与既有决策的关系

补充 ADR-0028 和 ADR-0030；不改变 ADR-0014/0024 的并发预算，也不改变 ADR-0029 的原生 worker
资源复用边界。
