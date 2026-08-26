# ADR-0036 FinishSnapshot 关系归一化与二次排序合并

状态：Accepted

日期：2026-08-24

实施状态：代码切片已实施；目标未达成，真实三次门禁未关闭

## 背景

R-035 后稳定 profile 显示 `normalizeRelations` 累计约 `160ms`，其后第二次 relation
`externalSortFixed` 累计约 `540ms`。当前实现先完整写出 normalized relation，再完整读入排序器；该临时
文件不进入 manifest，也不属于可恢复快照。

依据：[R-036](../research/R-036-FinishSnapshot关系归一化与二次排序合并预研.md)、
[ADR-0028](0028-macOS原生扫描与追加式二进制快照.md)、
[ADR-0031](0031-FinishSnapshot临时索引同步与终态性能.md)、
[ADR-0035](0035-FinishSnapshot外部排序归并与缓冲复用.md)。

## 决策

### 1. 归一化结果直接进入最终 relation 排序块

保留 raw relation、node metadata 和 override 按 `nodeID` 排序。读取三者并通过现有规则校验、应用最后一条
override 后，将 relation 记录写入固定容量连续块；块满后按
`parentID + owned_allocated DESC + nodeID` 排序并写入外部 run。所有 run 最后使用 ADR-0035 的有界小顶堆
归并。

### 2. 删除 normalized relation 中间文件

不再为归一化结果创建独立的完整临时文件。raw relation、node/override 临时文件仍按当前调用链读取和清理，
最终 relation 排序结果仍是进程私有临时文件，完成后删除。

没有 override 时直接从 raw relation 生成最终 relation 排序结果；这只跳过不必要的 join，不改变最终排序。

### 3. 保留所有事实和完整性校验

保留 relation/node 数量匹配、node ID 对齐、override 节点存在性、目录类型、记录宽度、排序、summary、
footer、checksum、最终 index section/manifest `Sync` 和原子发布。不得把流式优化变成丢记录或跳过校验的路径。

## 不采用的方案

- 全量内存 map：破坏百万级节点外部排序的 RSS 上界。
- 取消 nodeID 排序或对齐校验：破坏 override 合并和损坏快照检测。
- 修改 relation/reader/index 格式：没有本轮迁移需求，也扩大恢复风险。
- 关闭最终数据、index 或 manifest 同步：违反 ADR-0028/0031。

## 验收标准

- relation 流式归一化、重复 override、坏输入、排序、分页/Map、checksum 和恢复测试通过；
- `go test -race ./internal/application ./internal/infrastructure/scanner`、`go vet ./...`、前端构建和
  `git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=2.70s`，完整终态中位数 `<=19.00s`；
- 节点/问题数量、查询、checksum、恢复、纯扫描和最大 RSS 无超过 `3%` 回退；
- 完整终态总 `15s` 门槛继续独立追踪。

## 与既有决策的关系

补充 ADR-0028、ADR-0031、ADR-0033、ADR-0034 和 ADR-0035；不改变 DDD、Wails、Darwin 扫描边界、设备
并发预算或快照公共契约。
