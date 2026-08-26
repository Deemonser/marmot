# ADR-0033 FinishSnapshot 临时字符串引用复用

状态：Accepted

日期：2026-08-24

实施状态：代码切片已完成；正确性门禁通过，性能子目标未达成

## 背景

R-030 后真实 `/` 的 `FinishSnapshot` 三次为 `3.437s`、`3.678s`、`3.608s`，中位数 `3.608s`。
`buildIndex` 逐个目录处理 override 时，会重复从进程私有 override string arena 读取相同的
confidence 和 size-basis 字符串。该读取位于 `FinishSnapshot` 的索引构建内部，不属于已发布数据帧或
manifest 的公共格式。

依据：[R-033](../research/R-033-FinishSnapshot临时字符串引用复用预研.md)、
[ADR-0028](0028-macOS原生扫描与追加式二进制快照.md)、[ADR-0031](0031-FinishSnapshot临时索引同步与终态性能.md)。

## 决策

### 1. Writer 临时字符串按值复用

`Writer` 为 override string arena 保存有界的 `string -> (offset, length)` 表。相同值直接复用原引用，
新值才追加到临时文件；超过内部表容量后回退为追加，保证容量有上限且语义不变。

### 2. buildIndex 按引用缓存读取结果

索引构建用 `(confidenceOffset, confidenceLength, basisOffset, basisLength)` 作为进程私有缓存键；命中时
复用已经读取的两个字符串，未命中时执行现有 `readOverrideStrings`。缓存容量固定，达到上限后回退到现有
读取路径。缓存不跨进程、不写入 manifest、不改变 index section。

### 3. 保留完整性边界

所有临时文件写入、Flush/Close、解码、排序、最终 section Sync、checksum、index 原子替换和 manifest
原子发布错误仍必须阻止发布。该优化不能关闭数据帧或最终索引的同步，也不能改变 reader 校验。

## 不采用的方案

- 修改公共 formatVersion 或把字符串改成枚举：需要迁移和 reader 兼容 ADR。
- 删除目录 override 或跳过最终索引：会破坏终态目录汇总和查询。
- 关闭同步、checksum 或 manifest 原子更新：违反已有恢复边界。

## 验收标准

- binary snapshot、application、scanner 相关测试和 race 通过；
- `go vet ./...`、前端构建、`git diff --check` 通过；
- 真实 `/` 三次 `FinishSnapshot` 中位数 `<=3.20s`，完整终态中位数 `<=19.80s`；
- 节点/问题数量、根节点和首层分页/Map、恢复和清理前身份校验无回归；
- 总 `15s` 门槛仍需独立满足，不能由本 ADR 子目标替代。

实施后真实 `/` 三次完整终态为 `19.888s`、`20.701s`、`19.737s`，中位数 `19.888s`；
`FinishSnapshot` 为 `3.504s`、`3.811s`、`3.013s`，中位数 `3.504s`。字符串引用复用和查询/恢复
语义通过测试，但两个性能子目标均未达成；后续切片必须继续基于 profile 选择热点。

## 与既有决策的关系

补充 ADR-0028、ADR-0030、ADR-0031；不改变 DDD、Wails 契约、Darwin 扫描边界和设备并发预算。
