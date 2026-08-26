# ADR-0040 Darwin 原生节点名称路径共享缓冲

状态：Accepted；代码切片已实施，分配子目标未达成

日期：2026-08-24

## 背景

依据 [R-040](../research/R-040-Darwin原生节点名称路径共享缓冲预研.md)，R-037 已消除通用路径清理和
中间 builder，但 `nativeScanContext.addNodes` 仍为每个目录项分别分配 `Name` 和 `Path`。R-037 分配
profile 的扫描器累计分配约 `1150.04MB`，其中 `C.GoStringN` 约 `63.50MB`。真实终态 profile 同时表明
Darwin 外部扫描代码仍是主要 wall-time 来源，因此本 ADR 只处理确定的 Go/C 回调分配，不改变并发或存储
协议。

## 决策

### 1. 一次分配完整路径

Darwin 原生回调使用已校验的 `name_length`，为父路径、必要分隔符和名称一次分配 Go backing buffer。
名称通过完整路径尾部切片生成，使 `scan.Node.Name` 与 `scan.Node.Path` 共享该 backing buffer。

原生目录项不返回 `.`/`..` 伪目录；内部辅助函数仍保留现有测试覆盖的特殊路径语义，异常输入不得绕过已有
路径边界规则。

### 2. 不跨越 C 回调生命周期

实现只能复制名称内容到 Go 拥有的内存，不能把 C 批次 buffer 转成长期 Go 字符串。批次回调返回后，C worker
可以立即复用 `getattrlistbulk` buffer；节点、问题和 ancestry map 必须继续拥有稳定的 Go 字符串。

### 3. 保留系统边界和公共契约

以下内容不变：

- 节点 ID、父子关系、批次顺序、硬链接去重、三种大小口径和可信度；
- `scan.Node.Path`、问题路径、清理前身份校验和 Finder/预览输入；
- 挂载 boundary、fd/openat 生命周期、SSD 8 worker、取消、错误传播和批次所有权；
- `scan.Node`、`BatchEmitter`、`SnapshotStore`、二进制格式、footer、checksum、manifest、Wails DTO 和清理规则。

## 不采用的方案

- 引用 C 内存：回调结束后生命周期不成立。
- 删除 `scan.Node.Path`：破坏扫描器、问题和清理边界。
- 提高 worker 或放宽队列：没有本轮证据支持，且会改变已冻结的设备预算和取消语义。
- 修改快照格式：生产节点记录本来不保存完整路径，格式变化不属于本轮收益来源。

## 验收标准

- Darwin 路径、批量扫描、取消、挂载边界、硬链接和问题回归通过；
- 分配 profile 相对 R-037 口径下降至少 `5%`；
- 真实 `/` 纯扫描三次中位数不超过 `13.50s`，完整终态、查询、checksum、恢复和 RSS 无超过 `3%` 回退；
- 产品完整终态 `15s` 总门槛继续单独验收，不能由本 ADR 的分配结果替代。

## 与既有决策的关系

补充 ADR-0021、ADR-0022、ADR-0028、ADR-0029、ADR-0032、ADR-0037；不改变 DDD、SDD 公共端口、快照格式
或生产通信边界。
