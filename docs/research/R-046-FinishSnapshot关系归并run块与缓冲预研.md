# R-046 FinishSnapshot 关系归并 run 块与缓冲预研

状态：代码切片已实施；正确性通过，性能收益未测出

日期：2026-08-24

## 1. 目标

在不改变追加式二进制快照格式、查询布局、checksum、manifest、恢复和原子发布语义的前提下，降低
`FinishSnapshot` 的稳定 I/O 和外排归并开销，继续推进真实 macOS `/` 完整可查询终态中位数 `<=15s`。

本轮只处理 `SnapshotStore` Infrastructure 的进程私有临时文件：

- parent relation 外排使用更大的固定 run 块，减少 run 数、临时文件打开和 merge heap 读取次数；
- 仅为该 relation 外排增加有界内存，不改变全量节点/关系不入内存的约束；
- 不扩大 worker 数量，不改变扫描器、Application 队列或 Wails 契约。

## 2. 当前证据

R-045 代码切片后的真实 `/` 串行三次完整终态为 `26.691s`、`28.961s`、`23.266s`，中位数
`26.691s`；`FinishSnapshot` 为 `4.723s`、`4.997s`、`4.670s`，中位数 `4.723s`。新的固定二进制测试
profile 单次为完整终态 `21.254s`、`FinishSnapshot` `4.227s`，节点 `2,856,437`、问题 `443`。

稳定 profile 显示：

1. `buildIndex` 的 parent relation 路径调用 `externalSortFixedSourceVisit`，当前 `externalSortChunkRecords`
   为 `64K`，约 `285` 万节点会形成约 `44` 个 relation run；
2. relation run 生成和 merge 都涉及临时文件读写，`visitSortedRelations` 累计约 `0.87s` CPU，`writeFixedRuns`
   和 `visitFixedRuns` 还会产生额外读写等待；
3. `buildIndex` 最后仍需把 section 临时文件复制、哈希到最终 index，当前保留该完整性边界；本轮不删除这一步；
4. 全链路累计分配主要仍在扫描回调和批次编码，不能把本轮 relation 外排优化解释成产品总门槛达成。

## 3. 方案比较

### 方案 A：relation 外排使用专用 256K run，采用

仅在 `visitSortedRelations` 的 relation source 外排中使用 `256K` 固定记录块，约将 2.8M 节点的 run 数从
约 `44` 降到约 `11`。排序块仍是连续字节缓冲和固定记录引用，处理完后释放；不会建立完整 relation 内存切片。

### 方案 B：全局把所有外排块改成 256K，拒绝

node relation、override 和 parent relation 可能同时排序；全局放大会增加峰值 RSS，且没有证据表明所有外排
阶段都需要同一容量。

### 方案 C：把全部 relation 加载到内存后排序，拒绝

会破坏百万级节点下的内存边界，并使扫描规模增长时峰值不可控。

### 方案 D：删除 parent 排序或改为无序 child section，拒绝

会破坏分页稳定顺序、`owned_allocated DESC, node_id` 排序和 Map/目录查询契约。

## 4. 实施边界

- 只修改 `internal/infrastructure/snapshot/binary/index_builder.go` 的私有外排调用和容量参数；
- 保留 `externalSortFixed` 的已排序快速路径、固定记录宽度、比较键、临时文件清理和错误传播；
- 保留最终 node/directory/child/issue/string section、index checksum、数据帧 checksum、manifest 同步和原子 rename；
- relation run 的额外工作内存必须由固定容量计算，不能随节点数线性增长；
- 不改变 `formatVersion`、公开 `SnapshotStore` 方法、分页/Map 返回结果、取消、恢复或 Wails DTO。

## 5. 验收

1. scanner、binary snapshot 的窄测试、race、全仓 Go 测试和 `go vet` 通过；
2. 合成百万节点的 relation、override、空目录、分页、Map、checksum 和尾部恢复回归通过；
3. 真实 `/` 串行三次记录纯扫描、`FinishSnapshot`、完整终态、节点/问题、查询、checksum、恢复和最大 RSS；
4. 以 R-045 同口径为基线，`FinishSnapshot` 中位数目标为 `<=4.00s`，完整终态目标为 `<=20.50s`；
5. 若目标未达成，保留正确性切片并基于新的 profile 再决定是否进入最终 index 直接流式写入预研，不能宣称产品
   `15s` 门槛完成。

## 6. 对 DDD、SDD 的影响

DDD 不变。SDD 只增加 Infrastructure 外排 run 容量的有界性要求；扫描事实、领域状态、快照公开格式、查询、
清理授权和 Wails 通信边界均不变。

建议 ADR：[ADR-0046 FinishSnapshot 关系归并 run 块与缓冲](../adr/0046-FinishSnapshot关系归并run块与缓冲.md)。

## 7. 实施结果（2026-08-24）

代码切片已完成：`index_builder.go` 新增 `relationSortChunkRecords = 256 * 1024`，只用于
`visitSortedRelations` 的两个 parent relation 外排调用点；`writeFixedRuns` 增加显式的 `chunkRecords`
参数，`externalSortFixed` / `externalSortFixedSource`（node relation 与 override 路径）继续传入
`externalSortChunkRecords`。比较键、nodeID 对齐、override 校验、checksum、manifest 与原子发布均未改动。

正确性：snapshot / binary / scanner / application 窄测试与 race 全部通过，`go vet ./internal/...` 通过，
全仓 `go test ./internal/...` 通过。

**性能收益未测出。** ADR-0046 的验收门禁是真实 `/` 三次 wall 中位数，而 R-047 第 3.11 节已证明该口径在本机
不可用（扫描 wall 在 `14`–`25s` 间随机波动，配对交替无效）。因此改用唯一可用的确定性环境——合成百万节点
POC（`TestMillionNodePOC` + `TestMillionNodeOverridePOC`），before/after 各三次：

| 配置 | `real` 三次 | `user` 三次 | `sys` 三次 |
| --- | --- | --- | --- |
| before（`64K`） | `3.92` / `3.57` / `3.80` | `2.37` / `2.03` / `1.99` | `0.85` / `0.75` / `0.79` |
| after（`256K`） | `6.01` / `4.28` / `4.11` | `2.04` / `2.03` / `1.95` | `0.84` / `0.84` / `0.80` |

- `user` CPU 中位数两者相同（`2.03s`），即本切片没有带来可测的 CPU 收益；
- `real` 中位数 after 略差（`4.28s` 对 `3.80s`，after 首次 `6.01s` 含构建开销）；`sys` 中位数略升
  （`0.84s` 对 `0.79s`）。

可能的解释（未验证）：run 数减少让 merge 的比较次数下降（`log2(16)` → `log2(4)`），但生成 run 时的块内排序
代价随块变大而上升（`log2(256K)` > `log2(64K)`），两者大致相抵；同时固定工作内存增加约 `16 MiB`
（`256K × 40B` 记录存储加 `256K × 24B` 记录切片）。此外百万节点 POC 的总耗时包含节点构建、数据帧写入和
校验，relation 外排只占其中一部分，小幅收益可能被掩盖。

限制：POC 为 `100` 万节点（`64K` → 约 16 runs，`256K` → 约 4 runs），不是 ADR-0046 论证针对的 `285` 万节点
（约 44 runs → 约 11 runs）；本机没有 `285` 万节点规模的确定性环境。因此这是**反对证据，不是危害证明**。

结论：实现按 ADR-0046 保留（它是已接受决策），但**不得记为性能达成**。是否回退 `256K`、或把该常量与
`externalSortChunkRecords` 合并，需要新的 ADR 决定；R-047 已把 `FinishSnapshot` 内部优化整体排除在门槛
达成路径之外（其方案 E）。
