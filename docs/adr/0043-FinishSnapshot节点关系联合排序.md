# ADR-0043 FinishSnapshot 节点关系联合排序

状态：Accepted

日期：2026-08-24

实施状态：已实施；正确性通过，性能子目标尚未验收

## 背景

R-042 after profile 的真实 `/` 二进制 smoke 为完整终态 `21.650s`、`FinishSnapshot` `4.217s`。
当前 writer 对每个节点分别写入 40 字节 `nodeMetaRecord` 和 40 字节 `relationRecord`，终态先分别按 node ID
外排，再通过两个迭代器对齐；这保留了正确性，但重复了固定记录排序和临时文件读写。

依据：[R-043](../research/R-043-FinishSnapshot节点关系联合排序预研.md)、
[R-042](../research/R-042-FinishSnapshot数据帧校验与问题索引单次遍历预研.md)、
[ADR-0035](0035-FinishSnapshot外部排序归并与缓冲复用.md) 和
[ADR-0036](0036-FinishSnapshot关系归一化与二次排序合并.md)。

## 决策

### 1. 使用进程私有联合记录

新增固定宽度 `nodeRelationRecord`，由当前 `nodeMetaRecord` 和 `relationRecord` 顺序拼接组成。`AppendBatch`
继续先验证领域节点，再将相同的 metadata/relation 字段写入一个临时流；不改变数据帧编码或公开快照字段。

### 2. node ID 初始只外排一次

`buildIndex` 对联合流按前部 node ID 外排，并从同一条联合记录读取 metadata 和 relation。override 仍单独按
node ID 外排；应用 override 后的 relation 继续通过现有有界固定记录流水按
`parentID + owned_allocated DESC + nodeID` 生成最终顺序。

联合流的排序块保持 `externalSortChunkRecords` 条记录上限，记录宽度增加只使单块约为 5 MiB，不能将全量节点、
relation 或 override 放入内存。

### 3. 保留所有校验与发布边界

联合记录解码必须校验两个子记录宽度、node ID 对齐、父子关系和负数大小。未知 override、非目录 override、
数量不一致和尾部残留继续阻止发布。数据帧 header/footer/checksum、问题索引、目录汇总、最终 index checksum、
Sync、manifest 和原子 rename 全部保留。

## 不采用的方案

- 只调整排序块大小：不能消除两路 node/relation 外排和对齐读取；
- 全量内存 map 或内存排序：破坏百万级快照的有界内存约束；
- 修改公开数据帧或 index format：本轮收益不需要格式迁移；
- 跳过 relation 归一化、override 校验、checksum 或 Sync：破坏查询事实和崩溃恢复边界。

## 后果

正面后果：

- 初始 node/relation 外排从两路收敛为一路，减少固定记录排序、临时 run/merge 和对齐读取；
- 保留现有最终 relation 排序，读路径和公开查询协议不变；
- 临时联合记录仍是进程私有、固定宽度和有界内存的实现细节。

代价和风险：

- 临时联合记录从 40 字节增加到 80 字节，排序块内存约翻倍；
- writer 初始化、追加、关闭和 buildIndex 的临时文件生命周期需要同步修改；
- 联合记录解码错误可能同时影响 node index 和 relation index，因此必须保留一一对应回归。

## 验收标准

- 联合记录编码/解码、无序批次、override、分页/Map、问题索引、checksum、尾部恢复、目录汇总和百万节点 POC
  回归通过；
- `go test ./...`、相关 race、`go vet ./...`、前端构建和 `git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=3.60s`、完整终态中位数 `<=19.50s`，其他指标相对 R-042 after
  口径不回退超过 `3%`；
- 产品完整终态 `15s` 仍独立验收，不能由本 ADR 子目标替代。

## 与既有决策的关系

补充 ADR-0035/0036 的进程私有外部排序和关系归一化约束，继承 ADR-0028 的追加式快照、校验和原子发布边界，
继承 ADR-0042 的数据帧单次校验/问题索引遍历；不改变 DDD、SDD 公共端口、Wails DTO 或产品交互。

## 实施结果

联合记录、override 合并、分页、Map、问题索引、checksum、尾部恢复、目录汇总和百万节点 override POC
回归通过。百万节点 POC 为 `1,000,000` 节点、`100,001` 个目录，当前写入/索引读取约 `2.58s`。

真实 `/` 三轮完整终态为 `77.250s`、`55.663s`、`47.996s`，`FinishSnapshot` 为 `27.490s`、`11.392s`、
`8.010s`，中位数分别为 `55.663s` 和 `11.392s`；节点约 `285` 万，问题 `452-454`。真实文件系统状态
和缓存波动较大，但本轮没有达到 `3.60s/19.50s` 子目标，产品 `15s` 门槛继续独立追踪。正确性实现保留，
下一轮由 ADR-0044 处理 normalized relation 临时流的直接消费边界。
