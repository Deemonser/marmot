# R-018 APFS 卷组与产品存储源映射

状态：已完成，结论由 ADR-0020 接受

日期：2026-08-21

## 1. 问题

Marmot 当前把 macOS APFS 的 System 卷和 Data 卷作为两个启动页入口，用户会误以为电脑有两块
磁盘。DaisyDisk 将它们显示为一个用户可理解的“Macintosh HD”。需要区分技术卷、扫描范围和产品
入口，避免为了 UI 合并而破坏 APFS 扫描边界。

## 2. 本机证据

本机 `diskutil info -plist` 结果：

| 技术卷 | Device | Container | VolumeGroupID | 挂载点 | CapacityInUse |
| --- | --- | --- | --- | --- | ---: |
| System | `disk3s1s1` | `disk3` | `3FFF83A2-AA8A-4C34-B2B2-910B9E300E59` | `/` | 约 12.6 GB |
| Data | `disk3s5` | `disk3` | `3FFF83A2-AA8A-4C34-B2B2-910B9E300E59` | `/System/Volumes/Data` | 约 209.0 GB |

两者属于同一个 APFS 卷组和同一个容器；物理存储是 `disk0s2`。Data 内容通过 firmlink 出现在
主逻辑命名空间，直接把 Data 追加到 `/` 递归扫描仍然会重复观察文件。

DaisyDisk 的一个“磁盘”是产品层的用户入口，不代表底层只有一个 APFS volume。它可以保留 System、
Data 和容器的独立容量事实，只在入口层按卷组聚合。

## 3. 当前实现定位

- Platform `ListVolumes` 正确发现了 `/`、Data 和系统辅助挂载点。
- `ports.Volume` 直接向 Application 暴露技术卷事实。
- Application `GetVolumes` 一对一映射技术卷。
- Wails 和前端继续一对一渲染非辅助卷，因此技术卷泄漏成产品“磁盘”入口。

这不是 Scanner 重复统计 bug，而是缺少技术事实到产品读模型的上下文映射。

## 4. 方案比较

### 方案 A：前端隐藏 Data

拒绝。前端无法可靠识别卷组关系；会丢失容量明细，也会把平台事实和产品决策混在 Web 层。

### 方案 B：Platform 直接返回合并后的磁盘

拒绝。Platform 应保留 System/Data、挂载点、卷自身占用和卷组身份；合并后会削弱独立
`ScanScope`、权限和边界语义。

### 方案 C：Platform 技术事实 + Application 产品投影

接受。Platform 返回带 `container_id`、`volume_group_id` 和 `role` 的技术卷；Application 只按
明确的 `volume_group_id` 生成 `StorageSource`，Wails 只暴露产品读模型。

## 5. 锁定语义

### 5.1 统一语言

- `APFSVolume`：一个真实挂载卷，拥有挂载点、卷 UUID/设备身份、角色和卷自身容量。
- `APFSVolumeGroup`：由同一个 `volume_group_id` 关联的 System/Data 技术卷集合。
- `StorageSource`：用户启动页看到的产品级存储入口，引用一个逻辑扫描根和一组卷成员。
- `StorageVolumeMember`：StorageSource 下的技术卷容量明细，不是独立的主入口。
- `ScanScope`：实际执行扫描的根路径和卷身份，仍然保持技术边界，不因 StorageSource 聚合而改变。

### 5.2 分组规则

1. 只有非空且相同的 `volume_group_id` 才能合并为一个 `StorageSource`。
2. 没有卷组身份的外部卷按挂载卷独立生成 StorageSource，不能按名称、路径前缀或容量猜测合并。
3. 包含 `/` 的主 APFS 卷组使用 `/` 作为默认扫描根；Data 成为成员明细，不作为默认主入口。
4. Data 仍可作为显式高级 `ScanScope`，但不能通过默认全盘扫描与 `/` 拼接为一棵树。
5. 系统辅助卷不作为主 StorageSource；它们仍可作为 Platform/诊断层事实保留。

### 5.3 容量规则

- 卷成员保留 `volume_used`、`volume_total` 和 `usage_basis`。
- APFS 卷组 StorageSource 的主容量使用容器层 `container_total`、`container_used` 和共享可用空间。
- 不能把 System 与 Data 的 `volume_used` 相加当作容器占用。
- StorageSource 的空间图仍引用扫描快照的 `owned_allocated`，不能用入口容量替代。

## 6. 实施边界

- Platform 只增加 APFS 容器和卷组身份解析，不改变扫描器的挂载跳过规则。
- Application 增加 `GetStorageSources` 产品查询，负责分组、排序和容量口径映射。
- Wails 只暴露 StorageSource DTO；前端不再消费技术卷列表作为主入口。
- 现有扫描、快照、清理计划和节点 `volume_id` 契约不改变。

## 7. 验收标准

1. 本机启动页主入口只显示一个 `Macintosh HD`。
2. 该入口的容量进度使用 APFS 容器口径，并可表达 System/Data 成员明细。
3. 外部卷仍各自显示为独立入口。
4. 根扫描不递归 Data 挂载点，且显式 Data 扫描仍保留独立范围能力。
5. 分组只使用结构化卷组身份，不能由前端路径或名称推断。
6. 技术卷单元测试覆盖同组、无组身份和辅助卷过滤；Application 测试覆盖容量不相加。

## 8. 限制

本记录不改变 APFS 部分克隆、系统快照、FileProvider 或跨卷废纸篓语义；也不把所有同一 APFS
container 中的外部卷自动合并，除非系统明确提供同一个 volume group identity。
