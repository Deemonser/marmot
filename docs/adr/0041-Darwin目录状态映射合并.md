# ADR-0041 Darwin 目录状态映射合并

状态：Accepted

日期：2026-08-24

## 背景

R-040 分配 profile 显示 Darwin 扫描上下文同时维护 `paths` 和 `dirParents` 两张相同 key 的 map，累计约
`62.60MB` 和 `35.11MB`。`paths` 服务回调期间的父路径解析，`dirParents` 只服务最终目录大小归并，二者
可以在同一内部状态值中表达。

## 决策

### 1. 使用一张目录状态 map

新增私有 `nativeDirectoryState`，包含 `path string` 和 `parentID int64`，由 `directories map[int64]...`
统一保存。节点回调通过目录状态取得父路径；`finishDirectorySizes` 通过同一状态取得父 ID。

这只改变扫描调用期间的内存布局和 map 查找次数，不改变返回的 `DirectorySizes` map。

### 2. 保留状态和安全边界

目录状态仍在现有 `native.mu` 保护下访问。节点 ID、父子关系、顺序、硬链接去重、大小汇总、挂载 boundary、
fd/openat、取消、错误回收、快照和清理校验全部不变。

## 不采用的方案

- 把 parent ID 编码进路径：污染事实并引入解析风险。
- 从节点批次重建父关系：增加缓存或重复遍历。
- 修改领域模型、快照格式或公共 DTO：不属于内部内存优化。

## 验收标准

- scanner、application、snapshot 回归和 race 通过；
- 分配 profile 相对 R-040 下降至少 `1.5%`；
- 真实 `/` smoke 的节点/问题、查询、checksum 和恢复无回归；
- 产品完整终态 `15s` 仍独立验收。

## 实施结果

已实施 `nativeDirectoryState` 和 `directories` map。R-040 基线累计分配为 `1138.52MB`，同一
`MARMOT_SCAN_ROOT=/` profile 口径下两次 R-041 分别为 `1120.83MB` 和 `1102.12MB`，相对下降
`1.55%` 和 `3.20%`，均低于验收线 `1121.44MB`。scanner、application、snapshot 回归和 race 均通过。

真实 binary `SnapshotStore` smoke 为 `20.455s`，`FinishSnapshot` 为 `3.836s`，节点 `2,845,962`、
问题 `443`，终态为 `completed_with_issues`；根节点和首层分页查询成功。该次终态仍超过产品 `15s` 门槛，
本 ADR 不将分配目标达成解释为产品总性能达标。

## 与既有决策的关系

补充 ADR-0028、ADR-0029、ADR-0032、ADR-0037 和 ADR-0040；不改变 DDD、SDD 公共契约或快照发布边界。
