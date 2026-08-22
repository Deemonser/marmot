# ADR-0019 macOS APFS 卷组与全盘容量语义

状态：Accepted

日期：2026-08-21

## 背景

macOS APFS 启动盘通常由 System、Data、Preboot、VM 和系统快照等多个挂载对象组成。
`/Users`、`/Applications` 等 firmlink 又把 Data 内容映射到 `/` 的逻辑命名空间。当前
实现只把 `/` 当作卷入口，导致扫描早期看起来只有系统快照大小；如果继续递归所有路径，
又会把 Data 和其他嵌套卷重复纳入根树。

## 依据

- [R-017 macOS APFS 卷组与全盘容量语义](../research/R-017-macOS_APFS卷组与全盘容量语义.md)
- [R-007 文件系统边界与一致性](../research/R-007-文件系统边界与一致性.md)
- [ADR-0003 全盘扫描与快照架构](0003-全盘扫描与快照架构.md)
- [ADR-0008 APFS 空间语义与扫描边界](0008-APFS空间语义与扫描边界.md)

## 决策

### 1. 挂载目录和扫描边界

- macOS Platform 通过 `getfsstat(MNT_NOWAIT)` 枚举当前挂载表，而不是只读取 `/Volumes`。
- Scanner 接收挂载边界解析器；扫描某个根时，根范围内的嵌套挂载点不递归、不把内容复制到
  当前树。扫描根自身所在的挂载点允许读取。
- 主根 `/` 作为逻辑命名空间扫描；Data 通过 firmlink 只观察一次。
- Data、外部卷或其他明确挂载点只能作为独立扫描范围进入；不能通过字符串拼接合并成一棵
  无卷身份的目录树。

### 2. 容量字段和来源

卷目录使用以下分离字段：

- `used_bytes`：卷自身占用，APFS 优先取 `diskutil info -plist` 的 `CapacityInUse`；
- `total_bytes`：卷/容器容量上限；
- `free_bytes`：可用空间，APFS 标记为卷组共享可用空间；
- `container_total_bytes` 和 `container_used_bytes`：APFS 容器层摘要；
- `usage_basis`：`diskutil_apfs_volume_v1`、`statfs_fallback_v1` 等来源版本。

`diskutil` 只能以固定绝对路径、固定 `info -plist` 参数执行，只读解析卷目录，不接受
Shell、URL 或用户命令；失败时使用 `statfs` 降级并标记来源。

### 3. 节点卷身份

`ScanNode` 增加 `volume_id`。第一阶段使用挂载源设备身份，无法获取时使用规范化挂载路径。
这项身份用于快照解释和后续去重边界；前端不自行推断卷身份。

### 4. 部分快照

`interrupted` 和 `cancelled` 快照继续保留可查询的已提交节点，但必须按部分结果展示。
应用启动把遗留 `running` 快照标记为 `interrupted` 时，同时从 `scan_nodes` 回填节点数、文件数、
目录数和文件节点 `owned_allocated` 汇总，避免状态栏显示零而空间图显示非零。

## 被拒绝方案

- 直接把 `/System/Volumes/Data` 追加到 `/` 的递归路径：会与 firmlink 造成重复统计。
- 把 `statfs("/")`、APFS 容器占用和文件树 `owned_allocated` 统一命名为“已用空间”：违反
  DDD 的大小口径不变量。
- 通过 `diskutil` 或 `du` 为每个文件启动子进程：破坏百万级扫描的并发和取消边界。
- 让前端根据路径判断卷边界：绕过 Platform 事实和 Application 契约。

## 后果

- Platform 增加挂载表和卷目录适配；Scanner 增加可注入的挂载边界解析器。
- SnapshotStore schema 增加 `volume_id` 和恢复摘要所需的查询路径。
- 卷入口数量会从单个 `/` 增加为主根、Data、外部卷和有限系统辅助摘要；UI 必须展示容量来源。
- 本 ADR 不改变 APFS 部分克隆、系统快照清理、FileProvider 和真实签名/TCC 的既有限制。

## 验收标准

- 当前 macOS 机器能从挂载表发现 `/`、Data 以及实际外部挂载；根扫描跳过嵌套挂载内容。
- 快照节点拥有非空 `volume_id`，旧 schema 可迁移，旧测试节点允许空值兼容。
- APFS 卷自身占用和容器占用在 DTO 中分开；空间图继续明确使用 `owned_allocated`。
- 中断快照恢复后的状态栏指标与已提交文件节点汇总一致。
- 现有 Go 测试、race、vet、前端类型检查和 macOS 小目录真实扫描通过。
