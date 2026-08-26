# ADR-0038 FinishSnapshot 固定记录排序器去反射与缓冲交换

状态：Accepted

日期：2026-08-24

实施状态：代码切片已实施；正确性通过，性能子目标未达成

## 背景

R-037 后的真实 `/` profile 显示 `FinishSnapshot` 的 `writeFixedRuns` 累计约 `1.05s`，连续排序块中
`sort.Slice` 累计约 `0.61s`；其中反射交换器和固定记录切片交换是明确的内部 CPU 成本。`fixedRecordsSorted`
还会为每条记录把当前缓冲复制到前一条缓冲。

目录字符串回读没有在本次 profile 中形成可独立确认的稳定热点，因此不采用扩大 `nodeMetaRecord` 的方案。
依据：[R-038](../research/R-038-FinishSnapshot固定记录排序器去反射与缓冲交换预研.md)、
[ADR-0035](0035-FinishSnapshot外部排序归并与缓冲复用.md) 和
[ADR-0036](0036-FinishSnapshot关系归一化与二次排序合并.md)。

## 决策

### 1. 固定记录块使用标准库泛型排序

`writeFixedRuns` 使用 `slices.SortFunc` 对 `fixedRecord` 切片排序。三态比较结果由现有 `less` 函数推导，
不改变 node、relation 和 override 的排序键，也不引入第三方排序依赖。

### 2. 已排序检查交换两个固定缓冲

`fixedRecordsSorted` 读取每条记录后，在下一轮交换 `previous` 和 `current` 的 backing buffer，保留逐条
比较和 I/O 错误处理，删除等宽 `copy`。空文件和首条记录的现有边界保持不变。

### 3. 保留数据和发布边界

临时排序 run、merge 文件、normalized relation、最终 index section、数据帧、manifest、checksum、Sync、
原子 rename、恢复和清理语义全部不变。该决策不改变快照格式版本、SnapshotStore 端口或 Wails 契约。

## 不采用的方案

- 扩展 `nodeMetaRecord`：缺少稳定热点证据，并增加临时 I/O 和排序记录宽度。
- 按当前扫描器顺序跳过关系排序：破坏 SnapshotStore 对任意批次写入和最终父目录顺序的契约。
- 全量内存排序：破坏百万级节点的有界内存设计。
- 关闭最终校验、Sync 或扩大 worker：改变恢复、完整性或设备并发边界。

## 验收标准

- 固定排序快速路径、无序输入、多 run、相等键、空输入和错误宽度测试通过；
- binary snapshot 的分页、Map、目录 override、checksum、恢复和百万节点 POC 通过；
- `go test -race ./internal/application ./internal/infrastructure/scanner`、`go vet ./...`、前端构建和
  `git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=3.10s`、完整终态中位数 `<=19.30s`，其他指标相对当前基线
  不回退超过 `3%`；
- 完整终态总 `15s` 门槛继续独立追踪，不能由本 ADR 子目标替代。

## 与既有决策的关系

补充 ADR-0035 和 ADR-0036，继承 ADR-0028/0031 对追加式快照、最终校验和原子发布的约束；不改变 DDD、
SDD 的领域对象、公共端口或产品交互。

## 实施结果

已通过 binary snapshot 回归、目录 override、checksum、恢复、分页/Map、百万节点 POC、race、vet 和前端
构建。真实 `/` 三次 `FinishSnapshot` 为 `6.778s`、`6.406s`、`4.138s`，中位数 `6.406s`；完整终态为
`47.780s`、`29.727s`、`24.315s`，中位数 `29.727s`。profile 中排序累计 CPU 约从 `0.68s` 降至 `0.44s`，
但 wall time 子目标未达成，完整终态 `<=15s` 继续未达成。实现保留，下一轮须用新的 profile 选择稳定热点，
不得把本 ADR 的 CPU profile 改善扩大为产品性能达标。
