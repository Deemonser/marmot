# ADR-0042 FinishSnapshot 数据帧校验与问题索引单次遍历

状态：Accepted

日期：2026-08-24

## 背景

`Writer.buildIndex` 的问题索引构建先通过 `scanData` 完整读取并校验所有数据帧，再重新读取每个 batch
header 生成问题索引输入。真实 `/` trace 中 `buildIssueSection` 约 `0.88s`，profile 显示
`hashFrame`/`scanData` 与问题索引阶段重复经过数据文件。

## 决策

在 `internal/infrastructure/snapshot/binary` 内共享已提交数据帧解析器。一次遍历完成：

1. data/batch header 和格式、序号、计数、固定记录宽度、payload/frame 边界校验；
2. commit footer 的 magic、版本、序号、frame length、节点/问题计数校验；
3. payload SHA-256 计算和 footer checksum 比较；
4. 对已校验 batch 调用问题索引构建回调。

`scanData` 继续使用该解析器并在坏帧处停止，维持尾部恢复的 `validEnd` 语义；writer 的
`walkCommittedBatches` 在目标 `dataEnd` 内遇到坏帧则返回错误，阻止 index/manifest 发布。

## 不采用的方案

- 跳过 checksum 或 footer 校验：破坏快照完整性边界；
- 删除问题索引扫描：丢失 issue payload；
- 将全部 batch header 缓存到内存：违反百万级快照的有界内存约束；
- 修改数据帧或 index format：收益不需要格式迁移来获得。

## 约束

- 不改变 format version、数据帧布局、index section、manifest、checksum、原子发布或 SnapshotStore 端口；
- 不改变 `ScanJob`、部分结果、取消、恢复、分页、Map、清理身份校验或 Wails DTO；
- 诊断阶段计时不得进入生产代码。

## 验收

- 数据帧、footer、计数、checksum 损坏回归和尾部恢复通过；
- binary snapshot 的问题索引、分页、Map、目录汇总和百万节点 POC 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=3.80s`、完整终态中位数 `<=19.80s`，且其他指标不回退超过 `3%`；
- 产品完整终态 `15s` 仍独立验收。

依据：[R-042](../research/R-042-FinishSnapshot数据帧校验与问题索引单次遍历预研.md)、
[ADR-0028](0028-macOS原生扫描与追加式二进制快照.md)、[ADR-0039](0039-二进制快照数据帧发布屏障同步.md)。

## 实施结果

已接入共享 `committedDataBatch` 解析和单次问题索引遍历；数据帧 header/footer/SHA-256、坏尾部恢复、终态
拒绝发布、问题索引、分页、Map 和目录汇总回归通过。真实 `/` after smoke 完整终态为 `21.650s`、
`FinishSnapshot` 为 `4.217s`，节点 `2,854,358`、问题 `443`；`buildIssueSection` CPU 约从 `0.88s` 降至
`0.35s`。`3.80s/19.80s` 性能子目标未达成，产品 `15s` 门槛继续独立追踪；下一轮新增 ADR-0043 处理
node/relation 初始外排的重复成本。
