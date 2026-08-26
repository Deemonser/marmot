# ADR-0044 FinishSnapshot 关系归并直接消费

状态：Accepted

日期：2026-08-24

实施状态：已实施；正确性通过，性能子目标未达成

## 背景

R-043 已把 node metadata 与 relation 合并为 80 字节联合临时记录，但 parent 排序仍先把应用 override 后的
relation 写入完整中间文件，再由最终 index 构建重新读取。真实 `/` 当前 `FinishSnapshot` 中位数为 `11.392s`，
完整终态中位数为 `55.663s`，R-043 目标未达成。

依据：[R-044](../research/R-044-FinishSnapshot关系归并直接消费预研.md)、
[R-043](../research/R-043-FinishSnapshot节点关系联合排序预研.md)、
[ADR-0035](0035-FinishSnapshot外部排序归并与缓冲复用.md) 和
[ADR-0036](0036-FinishSnapshot关系归一化与二次排序合并.md)。

## 决策

### 1. 使用 parent merge visitor

保留当前固定记录 run、二叉堆 merge 和 `parentID + owned_allocated DESC + nodeID` 排序键；merge 每产生一条
relation 就交给 Infrastructure 私有 visitor。visitor 按 parent 分组写 child section，并在目录组结束时写
directory section，不再生成完整 normalized relation 中间文件。

### 2. node section 与目录 metadata 分开但仍有界

node section 先从 node ID 联合流生成。visitor 通过第二个 node ID 游标按目录 ID 递进，写入没有 child relation
的空目录记录；第二个 override 游标提供目录最后一次 override 的大小、可信度和 size basis。没有 override 的
目录继续从数据帧读取原始目录字符串。所有游标均为固定缓冲顺序读取，不构建全量 map。

### 3. 保留校验和发布边界

继续校验联合记录 node/parent identity、node ID 单调性、根节点、父目录、非负大小、关系计数和目录计数；
未知/重复/非目录 override 继续拒绝发布。数据帧 footer/SHA-256、问题索引、最终 index section checksum、
section/manifest Sync、原子 rename、取消和恢复语义全部不变。

## 不采用的方案

- 将全量 relation 加载到内存：破坏百万级快照的有界内存约束。
- 改变公开 index section 布局：会扩大查询和恢复兼容范围，本轮不需要格式迁移。
- 删除空目录或由前端补齐目录 metadata：前端不能重建扫描事实。
- 跳过 override、parent 或 checksum 校验：会破坏快照可信边界。

## 后果

正面后果：

- 消除完整 normalized relation 临时文件的写入和最终重读；
- 仍保留 node ID 与 parent ID 两种排序顺序，查询 section 和公开格式不变；
- 内存仍受固定记录块、merge heap 和顺序游标限制。

代价和风险：

- visitor 同时负责 relation 分组、空目录补齐、目录字符串和 section 计数，代码路径复杂度增加；
- 必须覆盖空目录、目录 override 和缺失/未知关系等边界，否则可能产生 section 计数或目录范围漂移；
- parent merge 失败时必须清理 run，并阻止 index/manifest 发布。

## 验收标准

- visitor、override、空目录、parent/root 校验、分页/Map、问题索引、checksum、恢复和百万节点 POC 回归通过；
- `go test ./...`、相关 race、`go vet ./...`、前端生产构建和 `git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=5.50s`、完整终态中位数 `<=20.50s`，其他指标相对 R-043 不回退
  超过 `3%`；
- 产品完整终态 `15s` 仍独立验收。

## 实施结果

visitor、空目录、目录 override、分页、Map、问题索引、checksum、尾部恢复和取消回归已通过。真实 `/` 三轮
`FinishSnapshot` 为 `4.72s`、`6.82s`、`8.93s`，中位数 `6.82s`；完整终态为 `22.70s`、`34.15s`、`35.28s`，
中位数 `34.15s`。`5.50s/20.50s` 子目标未达成，产品完整终态 `15s` 门槛仍未完成；下一轮热点由 ADR-0045
锁定。

## 与既有决策的关系

补充 ADR-0035/0036/0043 的进程私有外部排序约束，继承 ADR-0028 的追加式快照、校验和、恢复和原子发布边界；
不改变 DDD、SDD 公共端口、Wails DTO 或 DaisyDisk 交互。
