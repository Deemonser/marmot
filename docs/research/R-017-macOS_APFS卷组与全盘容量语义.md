# R-017 macOS APFS 卷组与全盘容量语义

状态：已完成（本机 APFS 挂载表、diskutil plist 和 Marmot 快照交叉验证）

日期：2026-08-21

## 1. 问题

当前空间图显示约 12GB，但本机数据卷已使用约 206GB，APFS 容器已使用约 235GB。
需要确定“全盘扫描”的范围、APFS System/Data 卷组的关系、嵌套挂载卷的边界，以及
卷容量和文件树占用是否可以使用同一个数字。

## 2. 本机证据

本机为 macOS 26 arm64，主 APFS 容器为 `disk3`：

| 对象 | 证据 | 结果 |
| --- | --- | ---: |
| 系统快照 `/` | `diskutil info -plist /` 的 `CapacityInUse` | 约 12.6 GB |
| Data 卷 | `diskutil info -plist /System/Volumes/Data` 的 `CapacityInUse` | 约 206.2 GB |
| 主 APFS 容器 | `APFSContainerSize - APFSContainerFree` | 约 234.7 GB |
| VM/Preboot | `diskutil apfs list` | 分别约 5.4 GB / 9.0 GB |

`/Users`、`/Applications` 等路径通过 macOS firmlink 映射到 Data 卷；直接遍历
`/System/Volumes/Data` 会再次观察到同一批用户文件。

当前 Marmot 最新落盘快照为 `interrupted`，实际只有 10,022 个节点，根节点的
`owned_allocated` 约 13.06GB，属于已提交的部分结果，不是全盘完成结果。历史快照还曾
把 `/System/Volumes/Data`、Preboot、VM 和 iOS Simulator 挂载内容递归纳入根树，产生约
479.7GB 的重复/跨卷汇总。

## 3. 方案比较

### 方案 A：把 Data 路径直接加入根扫描

拒绝。根路径已经可以通过 firmlink 访问 Data 内容；再加入 Data 会重复统计用户文件，且
Preboot、VM 和模拟器等嵌套卷仍会继续污染结果。

### 方案 B：只使用 `/` 的 `statfs` 数字

拒绝。APFS 卷组共享容量，`statfs` 的可用空间和卷自身占用不能解释为文件树大小；系统
快照、Data 卷和容器占用必须分别表达。

### 方案 C：挂载表做扫描边界，diskutil plist 做卷容量目录

接受。使用 `getfsstat(MNT_NOWAIT)` 获取当前挂载点；根扫描跳过根范围内的嵌套挂载点，
让 firmlink 只在逻辑命名空间中出现一次。卷目录通过固定的只读
`/usr/sbin/diskutil info -plist <mount>` 获取 `CapacityInUse`、容器大小和容器空闲，
解析 XML plist；命令失败时降级到 `statfs`，并降低 `UsageBasis` 可信度。

## 4. 锁定语义

1. `ScanScope` 仍是一个根路径和卷身份；`/` 是主逻辑命名空间，Data 通过 firmlink 被观察
   一次，`/System/Volumes/Data` 不作为其子树再次递归。
2. 根扫描不跨越任何嵌套挂载点。嵌套卷保留在卷目录或系统辅助空间摘要中，不伪装成普通
   目录节点；用户显式选择某个卷时，该卷可以作为独立 `ScanScope` 扫描。
3. `volume_used` 表示卷自身的 `CapacityInUse`；`container_used` 表示 APFS 容器层面的
   `container_size - container_free`；两者不能混入 `owned_allocated`。
4. 空间图继续默认使用 `owned_allocated`。它表示已观察文件节点的去重后占用，不表示容器
   物理已用，也不能在扫描未完成时被称为全盘总量。
5. 每个节点持久化 `volume_id`。第一阶段使用挂载源设备身份，无法得到时退化为挂载路径；
   该身份用于解释卷边界和后续硬链接/克隆归属，不依赖前端路径猜测。
6. `interrupted`、`cancelled` 和权限受限快照可以查询，但必须显示为部分结果；恢复状态时
   从已提交节点重新计算节点数、文件数、目录数和文件占用摘要。

## 5. 验收标准

- 本机卷目录至少能分别识别 `/` 和 `/System/Volumes/Data`，并显示约 12.6GB 与 206.2GB
  的卷自身占用；不能把二者渲染为同一个文件树数字。
- 根扫描不会产生 `/System/Volumes/Data`、Preboot、VM 或模拟器挂载点下的重复子树。
- 根扫描完整前，空间图中心和状态栏使用 `partial`/`interrupted` 语义；重启后部分快照的
  进度摘要不再显示为零。
- 文件夹扫描、显式 Data 卷扫描和非 macOS 测试仍保持原有端口契约。
- 真实本机验证用 `diskutil apfs list`、`df`、快照节点计数和挂载边界交叉检查；不以一次
  合成 SQLite 基准替代卷边界验证。

## 6. 限制

本预研不解决 APFS 部分共享块的精确归属、系统快照删除和 FileProvider 私有行为；这些仍
受 ADR-0008 的未知/部分语义约束。`diskutil` 只用于启动时卷目录，不参与逐文件扫描，
也不执行任何文件操作。

## 7. 对 DDD/SDD 的影响

- DDD 新增“卷自身占用、容器占用、文件树去重占用”不能混淆，以及挂载边界的领域规则。
- SDD 新增挂载表探测、plist 解析、`volume_id` 持久化、部分快照摘要恢复和验收门槛。
- 实现依据为 [ADR-0019](../adr/0019-macOS_APFS卷组与全盘容量语义.md)。
