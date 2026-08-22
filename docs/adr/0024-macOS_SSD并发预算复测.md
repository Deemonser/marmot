# ADR-0024 macOS SSD 并发预算复测

状态：Accepted

日期：2026-08-21

## 背景

ADR-0014 和 ADR-0022 将单 SSD 目录 worker 固定为 4。R-020 的历史复测约 19.62s 到 20.70s，
没有足够证据放宽预算。当前实现已经完成 getattrlistbulk、挂载边界预计算和有界 fd/openat，
需要用同一版本重新验证原版 `<15s` 的纯扫描目标。

## 依据

- [R-022 macOS SSD 并发预算复测](../research/R-022-macOS_SSD并发预算复测.md)
- [R-020 macOS 目录 fd 与 openat 扫描路径优化预研](../research/R-020-macOS目录fd与openat扫描路径优化预研.md)
- [ADR-0014 分阶段扫描与设备感知并发](0014-分阶段扫描与设备感知并发.md)

## 决策

1. `DeviceProfileSSD` 使用 8 个目录 worker。
2. 全局目录 worker 上限仍为 8；因此第一阶段一个 SSD scope 不会与其他 scope 叠加超过全局预算。
3. `rotational=1`、`network_or_virtual=2`、`unknown=2` 保持不变。
4. 该决策只约束 Infrastructure 扫描调度，不改变 Domain、SnapshotStore、Wails 事件或清理契约。
5. 纯扫描器 `<15s` 是本机代表性样本指标，不构成包含 SQLite 落盘、权限等待或前端渲染的产品保证。

## 未采用方案

- 将所有设备统一调到 8：拒绝，会把 SSD 证据错误外推到机械盘、网络/虚拟卷和未知设备。
- 开放用户自定义 worker：拒绝，会破坏资源上限和可复现的性能门禁。
- 用容器容量伪造扫描百分比：拒绝，容量口径和节点遍历总量不同。

## 后果

- 本机 SSD 的纯扫描器两次复测进入 15 秒目标附近，目录任务仍受全局队列、fd 槽位和取消边界约束；
- 高并发会增加 SSD 上的 CPU/I/O 压力，必须继续记录冷/热缓存、节点数和问题数；
- 应用层 SQLite 快照写入仍是独立瓶颈，不能因为扫描器变快就把任务提前标为完整。

## 验收标准

- `workersForProfile(DeviceProfileSSD)` 返回 8，其他 profile 预算保持 1/2/2；
- 高扇出、取消、硬链接、嵌套挂载和权限问题测试继续通过；
- 本机纯扫描 smoke 记录节点数、问题数和耗时，应用层 smoke 单独记录落盘耗时；
- SDD、DDD、README、基准和索引均引用本 ADR。
