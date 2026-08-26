# R-042 FinishSnapshot 数据帧校验与问题索引单次遍历预研

状态：代码切片已实施，正确性通过，性能子目标未达成

日期：2026-08-24

## 1. 目标

降低追加式二进制快照 `FinishSnapshot` 在构建问题索引时的重复数据读取。保持每个已提交数据帧的
header、payload 边界、commit footer、计数、序号和 SHA-256 校验，把完整校验和 batch header 回调合并
为一次有界顺序遍历。

本轮目标：

- `buildIssueSection` 不再先完整执行一次 `scanData`，再第二次读取全部 batch header；
- 继续校验所有已提交帧，不关闭 footer、checksum、manifest 或恢复边界；
- 在同一真实 `/`、串行和权限条件下，使 `buildIssueSection` 阶段相对当前 trace 基线约 `0.88s` 至少下降
  `25%`，并使 `FinishSnapshot` 三次中位数推进到 `3.80s` 以内；
- 完整可查询终态以三次中位数 `<=19.80s` 作为本切片目标，产品 `15s` 仍是独立总门槛；
- 节点、问题、分页、Map、取消、尾部恢复和快照完整性不回退。

## 2. 问题与证据

当前 `Writer.buildIndex` 的 `buildIssueSection` 调用 `walkCommittedBatches`。该方法先调用
`scanData(w.data, w.dataEnd, snapshotID)`，逐帧读取 header/footer 并计算 SHA-256；校验完成后又从
`dataHeaderSize` 开始第二次读取每个 batch header，只为把 `nodeCount`、`issueCount` 和 payload 长度传给
问题索引回调。

当前真实 `/` trace 的终态阶段为：

| 阶段 | 结果 |
| --- | ---: |
| `buildIssueSection` | `0.8815s` |
| `sort-inputs` | `1.3914s` |
| `normalize-relations` | `1.6937s` |
| `sync-index-sections` | `0.1274s` |
| `write-index-atomic` | `1.5176s` |
| `FinishSnapshot` | `7.1660s` |

该次运行节点 `2,853,306`、问题 `444`；profile 同时显示 `hashFrame`/`scanData` 成对出现，说明问题
索引阶段存在可定位的重复遍历。阶段计时包含真实磁盘波动，不能单独代表固定收益，但重复控制流是确定的。

## 3. 方案比较

### 方案 A：共享已提交帧解析器，校验并回调一次

采用。抽取 Infrastructure 私有的已提交帧读取函数，单次读取 header、校验字段和边界，读取 footer、计算
并比较 SHA-256，然后直接调用 `buildIssueSection` 的 batch 回调。`scanData` 继续使用同一解析器，但在
发现坏帧或不完整尾部时保持“停止在最后完整帧并返回 `validEnd`”的恢复语义；writer 的终态遍历把坏帧视为
发布失败。

### 方案 B：FinishSnapshot 跳过数据帧 checksum

拒绝。会削弱发布前完整性校验和已提交数据的信任边界，不能作为性能优化。

### 方案 C：问题索引不再扫描数据帧

拒绝。问题记录位于数据帧的 issue payload；不读取它会丢失问题索引或需要新增第二份事实缓存。

### 方案 D：把所有 batch header 保存在内存

拒绝。会把百万级节点快照的内存随扫描批次增长，违反有界快照构建约束。

## 4. 实施边界

- 新增共享的已提交数据帧解析逻辑，只属于 `internal/infrastructure/snapshot/binary`；
- 保留 data header、batch magic/version、递增 sequence、节点/问题计数、固定记录宽度、payload 长度、
  frame length、footer 字段和 SHA-256 校验；
- `scanData` 的部分尾部恢复行为不变；`walkCommittedBatches` 在 `w.dataEnd` 内遇到坏帧仍拒绝发布；
- 不修改二进制 format version、数据帧布局、index section、manifest、checksum、原子 rename、查询、
  取消、恢复、Wails DTO 或领域模型；
- 临时阶段计时只用于本轮研究，生产代码不保留环境变量诊断输出。

## 5. 验收

1. 新增或调整回归，证明 `FinishSnapshot` 在数据帧损坏、footer 损坏、计数异常和不完整尾部下仍拒绝
   发布或保持原有恢复语义。
2. binary snapshot 的分页、Map、问题索引、checksum、尾部恢复、目录汇总和百万节点 POC 通过。
3. `go test ./...`、相关 race、`go vet ./...`、前端构建和 `git diff --check` 通过。
4. 真实 `/` 串行三次记录纯扫描、完整终态、`FinishSnapshot`、节点/问题、查询、checksum、恢复和最大 RSS。
5. 只有 `buildIssueSection` 相对 trace 基线下降至少 `25%`、`FinishSnapshot` 中位数 `<=3.80s`、完整终态
   中位数 `<=19.80s` 且其他指标无超过 `3%` 回退，才关闭本切片；否则保留正确性实现并记录证据。

## 6. 对 DDD、SDD 的影响

DDD 不变。SDD 只补充 SnapshotStore Infrastructure 可以在一次受控帧遍历中同时完成数据完整性校验和问题
索引输入，不改变已提交快照、部分结果、恢复和清理前身份校验语义。

建议 ADR：[ADR-0042 FinishSnapshot 数据帧校验与问题索引单次遍历](../adr/0042-FinishSnapshot数据帧校验与问题索引单次遍历.md)。

## 7. 实施结果

已新增 `committedDataBatch` 和共享 `readCommittedDataBatch`，`walkCommittedBatches` 不再先完整扫描再
第二次读取 batch header；`scanData` 仍在坏帧处停止并返回最后完整帧的恢复边界。新增损坏数据帧终态拒绝
发布回归，binary snapshot 窄回归通过。

真实 `/` after smoke 的完整终态为 `21.650s`，`FinishSnapshot` 为 `4.217s`，节点 `2,854,358`、问题 `443`。
CPU profile 中 `buildIssueSection` 约从 `0.88s` 降至 `0.35s`，但 R-042 的 `FinishSnapshot <=3.80s` 和完整
终态 `<=19.80s` 子目标均未达成；产品 `15s` 总门槛继续未完成。剩余热点主要是 node/relation 初始外排、
关系归一化和最终 index 写入，下一轮由 R-043/ADR-0043 处理联合外排边界。
