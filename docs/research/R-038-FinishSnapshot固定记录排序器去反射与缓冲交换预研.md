# R-038 FinishSnapshot 固定记录排序器去反射与缓冲交换预研

状态：代码切片已实施；正确性通过，性能目标未达成，结论由 ADR-0038 接受

日期：2026-08-24

## 1. 目标

降低追加式二进制快照 `FinishSnapshot` 的固定记录排序 CPU 开销，优先处理已由真实 `/` profile 证明的
连续排序块反射交换和已排序快速路径记录复制。范围只覆盖 Infrastructure 内部临时排序器，不改变节点、
关系、目录汇总、索引 section、checksum 或原子发布语义。

本切片目标为：在同一真实 `/` 样本和串行条件下，`FinishSnapshot` 三次中位数推进到 `3.10s` 以内，完整
可查询二进制终态三次中位数推进到 `19.30s` 以内；纯扫描、查询、checksum、恢复和最大 RSS 相对当前
基线不回退超过 `3%`。完整终态 `<=15s` 仍是独立产品总门槛。

## 2. 证据

R-037 后使用 Go `1.25.0`、Darwin `arm64`、macOS `26.5.2`、真实 `/`、单 SSD 8 worker 做一次只读
profile。该次运行节点 `2,846,617`、问题 `447`，应用总墙钟 `32.369s`，其中 `FinishSnapshot`
为 `3.429s`。CPU profile 因采样和全盘原生调用不适合作为完整终态墙钟基线，但可用于定位排序内部
累计成本：

- `Writer.buildIndex` 累计约 `1.55s`；
- `externalSortFixed` 累计约 `1.18s`；
- `writeFixedRuns` 累计约 `1.05s`；
- 连续排序块的 `sort.Slice` 累计约 `0.61s`，其中排序回调使用反射交换器；
- `normalizeAndSortRelations` 累计约 `0.81s`，其中归一化源读取本身约 `0.13s`，不能把该阶段全部归因于
  比较器；
- `readAtFull` 累计约 `0.32s`，而 `readDirectoryStrings` 没有独立 profile 符号，目录字符串回读在本次
  profile 中没有形成可确认的稳定热点。

因此本轮不扩展 `nodeMetaRecord`，也不把一次 profile 中的累计 CPU 时间直接当作墙钟收益。外部排序器
是现有 `FinishSnapshot` 最明确、内存有界且不触及公开格式的优化边界。

## 3. 方案比较

### 方案 A：使用标准库泛型排序，并交换已排序检查的两个记录缓冲

采用。使用 Go 标准库 `slices.SortFunc` 对 `fixedRecord` 切片排序，比较器将已有严格弱序的 `less([]byte,
[]byte) bool` 转换为三态结果。该排序不保证稳定，但现有三个排序键已经决定了公开顺序；相等记录的相对
顺序不是契约。

`fixedRecordsSorted` 保留当前逐条比较和坏记录传播，只在读取下一条记录后交换 `previous`/`current` 两个
固定缓冲，不再把 `current` 复制到 `previous`。记录宽度、文件边界和已排序输入直接复用语义不变。

### 方案 B：扩展 `nodeMetaRecord`，携带 confidence/basis 字符串引用

拒绝。本次 profile 没有证明 `readDirectoryStrings` 是稳定墙钟热点；扩展临时记录还会增加排序、I/O 和
恢复校验的记录宽度，收益与代价不匹配。

### 方案 C：跳过 relation 归一化或按输入顺序假定已排序

拒绝。SnapshotStore 允许批次分段写入，必须继续校验 nodeID 对齐、override 合并、未知节点和最终
`parentID + owned_allocated DESC + nodeID` 顺序。没有新的关系布局预研，不能用扫描器当前顺序替代快照
写入契约。

### 方案 D：关闭校验、Sync 或扩大扫描并发

拒绝。这些边界不是本轮热点，且会改变崩溃恢复、完整性或设备画像，不能作为排序器切片的交换条件。

## 4. 决策与边界

- 只改进程私有的固定记录排序器；不修改 `nodeMetaRecordSize`、`relationRecordSize`、`overrideRecordSize`
  或公开二进制格式版本。
- `slices.SortFunc` 的比较结果必须与现有 `less` 一致；所有调用点继续使用原有排序键。
- `fixedRecordsSorted` 仍必须检测固定记录宽度、空文件、降序记录和读错误；只能改变两个临时缓冲的复用方式。
- 不将全量节点、关系或 override 加载到内存；排序块容量继续受 `externalSortChunkRecords` 限制。
- 保留临时 run/merge 文件的错误传播和清理，保留最终 index section、数据帧、manifest 的 `Sync`、checksum
  和原子发布。
- 不改变节点顺序、目录汇总、分页、Map、部分结果、取消、恢复、Wails DTO、挂载边界、清理校验或 DDD 规则。

## 5. 验收

1. 固定记录排序快速路径、无序输入、多 run、相等键、空输入和坏记录宽度测试通过。
2. binary snapshot 的目录 override、分页/Map、checksum、恢复和合成百万节点 POC 通过。
3. `go test -race ./internal/application ./internal/infrastructure/scanner`、`go vet ./...`、前端生产构建和
   `git diff --check` 通过。
4. 真实 `/` 串行三次记录纯扫描、完整终态、`FinishSnapshot`、节点/问题数量、查询、checksum、恢复和最大 RSS。
5. 只有 `FinishSnapshot` 中位数 `<=3.10s`、完整终态中位数 `<=19.30s` 且其他指标无超过 `3%` 回退，才关闭
   本切片；未达成时保留正确性实现并记录证据，不调整阈值。

## 6. 对 DDD、SDD 和后续实现的影响

DDD 不变：节点、目录汇总、扫描快照和清理计划的领域语义不变。SDD 只补充 Infrastructure 固定记录排序的
比较键、内存边界和发布边界；不新增公共端口、领域对象或前端 DTO。

建议 ADR：[ADR-0038 FinishSnapshot 固定记录排序器去反射与缓冲交换](../adr/0038-FinishSnapshot固定记录排序器去反射与缓冲交换.md)。

## 7. 实施结果

已实施 `slices.SortFunc` 固定记录排序和 `fixedRecordsSorted` 的双缓冲交换。binary snapshot 回归、目录
override、checksum、恢复、分页/Map 和百万节点 POC 均通过。百万节点 POC 为 `1,000,000` 节点，写入/读取
`1.679s`，Map `5.57ms`，索引大小 `88,000,179` 字节。

真实 `/` 串行三次结果如下：

| 轮次 | 完整终态 | FinishSnapshot | 节点 | 问题 |
| --- | ---: | ---: | ---: | ---: |
| 1 | `47.780s` | `6.778s` | `2,845,572` | `446` |
| 2 | `29.727s` | `6.406s` | `2,845,560` | `444` |
| 3 | `24.315s` | `4.138s` | `2,845,560` | `443` |

本轮中位数为完整终态 `29.727s`、`FinishSnapshot` `6.406s`，未达到 `19.30s/3.10s` 切片目标；不能宣称
完整终态 `<=15s`。同一 profile 的排序累计 CPU 从旧版约 `0.68s` 降至约 `0.44s`，但真实 wall time 受
最终临时文件写入和 Sync 波动影响，CPU 改善没有转化为稳定的终态收益。R-038 保留正确性和进程私有排序
实现，下一轮必须重新 profile 完整终态 I/O/Sync 或其他稳定热点，不能继续扩大本切片目标。
