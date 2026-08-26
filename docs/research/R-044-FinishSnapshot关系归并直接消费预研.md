# R-044 FinishSnapshot 关系归并直接消费预研

状态：代码切片已实施（正确性通过；性能子目标未达成）

日期：2026-08-24

## 1. 目标

R-043 已把 node metadata 与 relation 合并为 80 字节联合记录，消除了 node ID 初始外排的一路重复工作，
但最终 parent 排序仍将应用 override 后的每条 40 字节 relation 写入完整临时文件，再由最终 index 构建重新
打开并读取。R-044 处理这段确定的写入/重读成本。

本轮目标：

- 让 parent 排序的外部 merge 直接消费到 child/directory section；
- 保留 node ID 联合流生成 node section，不把两种排序顺序混在一个索引中；
- 通过第二个有界 nodeID/override 游标补齐空目录、目录可信度和 size basis；
- 在同一真实 `/` 串行条件下，使 `FinishSnapshot` 三次中位数达到 `5.50s` 以内，完整可查询终态达到
  `20.50s` 以内；
- 节点、问题、分页、Map、checksum、恢复和最大 RSS 不回退超过 `3%`；
- 产品完整终态 `15s` 仍是独立门槛。

## 2. 问题与证据

R-043 当前路径为：

```text
联合流按 node ID 外排
  -> node ID 流式对齐 override
  -> 生成完整 relation 临时文件（parent 排序）
  -> 最终 index loop 再读取 relation 临时文件
  -> 写 child/directory section
```

真实 `/` 当前约 `285` 万节点、约 `58.8` 万目录；R-043 实测三轮 `FinishSnapshot` 为 `27.490s`、
`11.392s`、`8.010s`，中位数 `11.392s`，完整终态中位数 `55.663s`，未达到 R-043 的 `3.60s/19.50s`。
真实 wall time 受扫描文件变化和 APFS 写回影响，但 relation 临时流的完整写入和完整重读是稳定存在的控制流。

百万节点 override POC 已证明联合记录、override 合并和最终 parent 顺序正确；当前 POC 写入/索引读取约 `2.58s`。

## 3. 方案比较

### 方案 A：parent merge visitor 直接写最终 section

采用。保留固定记录 run 和二叉堆 merge；把 merge 的每条已排序 relation 交给受控 visitor。visitor 按
`parentID` 分组写 child section，并在目录组结束时写 directory section。

node section 先由 node ID 联合流单独生成。第二个 node ID 游标按目录 ID 递进，负责发现没有 child relation 的
空目录并写入零 child 目录记录；第二个 override 游标提供该目录最后一次 override 的可信度、size basis 和
空目录汇总。非 override 目录继续从数据帧读取原始字符串。

### 方案 B：把所有归一化 relation 放入内存后排序

拒绝。约 `285` 万节点需要超过 `100MB` 的 relation 原始空间，且 Go slice/对象开销会随节点数增长，违反
当前百万级快照的有界外排约束。

### 方案 C：改变公开 index format，让 relation 按 node ID 存储

拒绝。查询依赖按 parent 的连续 child range；改变 section 顺序会扩大读路径和恢复兼容范围，本轮收益不需要
迁移公开格式。

### 方案 D：删除空目录或把目录 metadata 交给前端补齐

拒绝。空目录仍是有效扫描节点，目录可信度和 size basis 是后端事实；前端不能访问文件系统或重建快照事实。

## 4. 实施边界

- 新增的 merge visitor、目录游标和 relation 消费逻辑只属于
  `internal/infrastructure/snapshot/binary`；不进入 Domain、Application、Ports 或 Wails DTO。
- node section、child section、directory section 的公开布局和记录宽度不变。
- 仍先按 node ID 校验联合记录的 node/parent identity、ID 单调性、根节点和目录类型；parent merge 仍校验
  relation 的非负大小、父目录存在和 root relation 唯一。
- override 仍要求 node ID 有序、最后一次 sequence 生效、未知 node/非目录 node 拒绝；不能因为 visitor 省掉
  normalized relation 文件而减少校验。
- 关系归并使用固定记录块、run 文件和有界 heap；merge 完成或失败后清理 run，不保留中间关系文件。
- 数据帧 header/footer/SHA-256、问题索引、最终 index checksum、section Sync、manifest Sync、原子 rename、
  取消、尾部恢复、部分结果和清理身份校验全部不变。

## 5. 验收

1. 新增 visitor 回归，覆盖 root/空目录/多 child、同 size 稳定 node ID 顺序、override、未知 override、非目录
   override、重复 override、缺失 parent 和目录 metadata。
2. binary snapshot 的分页、Map、问题索引、checksum、尾部恢复、目录汇总和百万节点 override POC 通过。
3. `go test ./...`、相关 race、`go vet ./...`、前端构建和 `git diff --check` 通过。
4. 真实 `/` 串行三次记录完整终态、`FinishSnapshot`、节点/问题、查询、checksum、恢复和最大 RSS。
5. 只有 `FinishSnapshot` 中位数 `<=5.50s`、完整终态中位数 `<=20.50s` 且其他指标无超过 `3%` 回退，才关闭
   本切片；否则保留正确性实现并记录下一热点。

## 5.1 实施结果

R-044 已在 `internal/infrastructure/snapshot/binary` 实施。visitor、空目录、目录 override、分页、Map、
问题索引、checksum、尾部恢复和取消回归通过；固定记录块、有界 merge 和最终 section 发布边界保持不变。

同一真实 `/` 串行条件下，`FinishSnapshot` 三次为 `4.72s`、`6.82s`、`8.93s`，中位数 `6.82s`；完整可查询
终态为 `22.70s`、`34.15s`、`35.28s`，中位数 `34.15s`。因此 R-044 的 `5.50s/20.50s` 子目标尚未达成，
产品完整终态 `15s` 门槛仍未完成。

最新 mutex profile 显示 Darwin 原生回调的 `nativeScanContext.addNodes` 约占 mutex delay 的 `98.7%`，
争用等待约 `1.046s`。下一轮转入 R-045，处理批内路径/节点转换仍位于全局锁内的问题。

## 6. 对 DDD、SDD 的影响

DDD 不变。SDD 只补充 SnapshotStore Infrastructure 可以让有界 parent merge 直接生成最终 child/directory
section；扫描事实、快照公开格式、发布边界和清理授权不变。

建议 ADR：[ADR-0044 FinishSnapshot 关系归并直接消费](../adr/0044-FinishSnapshot关系归并直接消费.md)。
