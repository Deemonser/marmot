# R-041 Darwin 目录状态映射合并预研

状态：代码切片已实施；正确性和分配子目标通过，产品完整终态门槛仍未完成

日期：2026-08-24

## 1. 目标

减少 Darwin 原生扫描上下文中重复保存的目录 ancestry 状态。当前每个目录同时写入 `paths` 和
`dirParents` 两张相同 key 的 map：前者用于子目录路径解析，后者只在最终目录汇总时读取。合并为一张
内部状态 map，保留路径和父 ID，不改变扫描结果或领域契约。

本轮目标是：

- 用一张 `directories` map 保存 `path` 和 `parentID`；
- 在与 R-040 相同的分配 profile 口径下，使扫描器累计分配至少下降 `1.5%`，即从约 `1138.52MB`
  推进到 `1121.44MB` 以下；
- 保持节点、问题、纯扫描、完整终态、查询、checksum、恢复和取消语义不回退超过 `3%`；
- 产品完整可查询终态 `15s` 仍是独立总门槛。

## 2. 问题与证据

R-040 的分配 profile 中，`nativeScanContext.addNodes` 的主要内部 map 分配约为：

| 分配点 | 累计分配 |
| --- | ---: |
| `nodes` 批次 slice | `524.22MB` |
| 路径 backing buffer | `356.05MB` |
| `dirSizes` | `149.00MB` |
| `paths` | `62.60MB` |
| `dirParents` | `35.11MB` |

`paths` 和 `dirParents` 的生命周期、key 集合和写入位置完全一致；`paths` 在每个节点回调中读取，
`dirParents` 只在 `finishDirectorySizes` 中读取。两张 map 同时扩大了 bucket、key 查找和锁内写入成本。

## 3. 方案比较

### 方案 A：合并为目录状态 map

采用。定义仅属于 Darwin Infrastructure 的 `nativeDirectoryState{path, parentID}`，用 `directories`
替代两张 map。回调通过一次查找取得父路径，终态汇总通过同一 map 取得父 ID。

### 方案 B：把 parent ID 编码到路径字符串

拒绝。会污染路径事实，增加解析成本和错误风险，也不能表达路径字符串的正常生命周期。

### 方案 C：删除 parent 状态并从节点批次重建

拒绝。会增加额外节点缓存或重复遍历，扩大内存和终态成本，没有证据证明收益。

### 方案 D：修改领域目录大小模型或快照格式

拒绝。本轮只合并进程内扫描状态，不修改 `scan.DirectorySize`、快照格式或查询契约。

## 4. 实施边界

- `directories` 是 Darwin Infrastructure 私有 map，值只包含父路径和 parent ID。
- 父路径查找、目录注册、最终逆 ID 汇总必须继续在同一状态锁下完成。
- 保留 ID 分配、节点顺序、硬链接去重、目录大小汇总、挂载 boundary、fd/openat、取消和错误传播。
- 不修改 `scan.Node`、`BatchEmitter`、`SnapshotStore`、Wails DTO、二进制格式、checksum 或 manifest。

## 5. 验收

1. scanner 全套测试、相关 race、application/snapshot 回归、`go vet ./...` 和前端构建通过。
2. 分配 profile 相对 R-040 下降至少 `1.5%`；若 profile 口径变化，必须保留旧口径对照。
3. 真实 `/` 至少完成一次完整 smoke，节点/问题、查询和 checksum 正常；三次终态门槛继续单独追踪。
4. 未达到分配目标时保留正确性改动并记录证据，不得把 map 合并解释为产品 `15s` 达标。

## 6. 对 DDD、SDD 的影响

DDD 不变。SDD 只补充 Darwin Infrastructure 可以合并重复的内部目录 ancestry map；扫描事实、状态机、大小
口径、清理校验和发布边界不变。

建议 ADR：[ADR-0041 Darwin 目录状态映射合并](../adr/0041-Darwin目录状态映射合并.md)。

## 7. 实施结果

代码切片已完成：Darwin `nativeScanContext` 使用一张 `directories` map 保存目录路径和 `parentID`，
`addNodes` 和 `finishDirectorySizes` 继续在现有 `native.mu` 保护下读取同一份目录状态。节点、问题、目录大小
汇总、挂载边界、fd/openat、取消、快照、checksum、manifest 和 Wails 契约未改变。

同一 `MARMOT_SCAN_ROOT=/` 只读 profile 口径下，R-040 基线累计分配为 `1138.52MB`，R-041 两次为：

| 轮次 | 扫描器累计分配 | 相对 R-040 | `addNodes` 累计分配 |
| --- | ---: | ---: | ---: |
| 1 | `1120.83MB` | `-1.55%` | `1112.85MB` |
| 2 | `1102.12MB` | `-3.20%` | `1095.64MB` |

R-041 的验收线为 `1121.44MB` 以下，两次 profile 均通过；profile 受真实 `/` 文件数量和路径长度变化影响，
因此将两次结果作为机制证据，不将其解释为固定 wall-time 收益。

真实 binary `SnapshotStore` 应用 smoke 通过：扫描耗时 `20.455s`，`FinishSnapshot` `3.836s`，状态为
`completed_with_issues`，节点 `2,845,962`，文件 `2,259,380`，目录 `586,582`，问题 `443`；根节点查询、
首层最多 1,000 项分页和 store close 均成功。该次完整终态超过产品 `15s` 门槛，且本轮未完成三次中位数复测，
不能宣称产品性能达标。

验证通过：

- `go test ./internal/infrastructure/scanner`
- `go test -race ./internal/infrastructure/scanner`
- `go test -race ./internal/application`
- `go test ./...`
- `go vet ./...`
- `npm run build`
- `git diff --check`

DDD 不变；SDD、README 和文档基线已同步 R-041 的实施结果。后续若继续优化，必须基于新的 profile 预研并新增
目标和 ADR，不能把当前分配收益直接外推为产品总性能结论。
