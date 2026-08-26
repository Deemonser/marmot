# ADR-0035 FinishSnapshot 外部排序归并与缓冲复用

状态：Accepted

日期：2026-08-24

实施状态：代码切片已实施；目标未达成，真实三次门禁未关闭

## 背景

R-034 后真实 `/` 完整终态三次中位数为 `19.473s`，`FinishSnapshot` 三次中位数为 `3.227s`。
新鲜 profile 观测 `externalSortFixed` 累计约 `1.60s`。当前多 run 归并每输出一条固定记录都线性扫描
所有活动 run；排序块还为每条记录单独分配字节切片。

依据：[R-035](../research/R-035-FinishSnapshot外部排序归并与缓冲复用预研.md)、
[ADR-0028](0028-macOS原生扫描与追加式二进制快照.md)、
[ADR-0031](0031-FinishSnapshot临时索引同步与终态性能.md)。

## 决策

### 1. 归并使用有界二叉小顶堆

`externalSortFixed` 为每个活动 run 保留一个当前记录，使用按现有 `less` 比较器排序的小顶堆选择下一条
记录。输出后复用该 run 的记录缓冲读取下一条，再执行下沉或上浮。比较键保持现有语义；比较器认为相等
时使用 run 序号作为内部 tie-break，不写入记录、不改变公开索引排序键。

该实现把归并选择从每条记录的 `O(runCount)` 比较降为 `O(log runCount)`，同时维持外部排序的总内存有界。

### 2. 排序块使用连续缓冲

每个内存排序块使用固定容量的连续 `[]byte` 保存记录，并由记录切片引用对应区间。块满后排序并写入
run，下一块重新使用容量边界；不保存跨块节点或全量 relation。

### 3. 保留发布和完整性边界

固定记录宽度校验、已排序输入快速路径、relation 按 node ID 归一化、relation 第二次按 parent/size/node
排序、临时文件错误传播、最终 section `Sync`、index checksum、index 原子替换和 manifest 原子发布全部
保留。该 ADR 不改变 formatVersion、reader、SnapshotStore、Wails 或领域模型。

## 不采用的方案

- 把全部节点/关系放入内存排序：破坏百万级输入的 RSS 边界。
- 合并或删除 relation 两阶段：改变目录 override 和查询排序校验，需单独 ADR。
- 修改快照格式或关闭 checksum/Sync：破坏恢复、完整性和崩溃发布语义。
- 增加扫描 worker 或放宽挂载边界：与本切片无关，且会混淆扫描与终态收益。

## 验收标准

- 排序器、binary snapshot、application、scanner 相关测试和 race 通过；
- `go vet ./...`、前端构建和 `git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=2.90s`，完整终态中位数 `<=19.20s`；
- 纯扫描、查询、checksum、恢复、节点/问题数量和最大 RSS 不比 R-034 基线回退超过 `3%`；
- 完整终态总 `15s` 门槛继续独立追踪，不能由本 ADR 子目标替代。

## 与既有决策的关系

补充 ADR-0028、ADR-0031、ADR-0033 和 ADR-0034；不改变 DDD、Wails、Darwin 扫描边界、设备并发预算或
快照公共契约。
