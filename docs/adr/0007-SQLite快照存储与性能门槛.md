# ADR-0007 SQLite 快照存储与性能门槛

状态：Accepted（真实全盘样本仍需发布前回归）

日期：2026-08-19

## 背景

全盘扫描结果可能达到百万级节点，不能以完整内存树或一次性 JSON 作为 UI 协议。
本机合成基准需要验证增量快照、分页查询和前端载荷边界。

## 预研依据

- [R-004 百万级节点性能与快照存储](../research/R-004-百万级节点性能与快照存储.md)
- [ADR-0003 全盘扫描与快照架构](0003-全盘扫描与快照架构.md)

## 决策

- `SnapshotStore` 第一实现使用 SQLite；macOS 第一阶段固定 `github.com/mattn/go-sqlite3`；
- 数据库启用 WAL、`synchronous=NORMAL`、受限连接数和批量事务；默认每批 10,000 节点；
- 节点查询索引为 `(snapshot_id, parent_id, owned_allocated DESC, id)`，提供稳定分页；
- 单次分页最多返回 1000 个节点，绑定返回预算为 256 KB；
- 取消在批次边界提交，取消后不再写入或发布新的扫描结果；
- 快照保存部分结果、权限问题、口径版本和 schema 版本；快照不是清理授权；
- 如果存储层无法达到门槛，只替换 `SnapshotStore` 适配器，不改变 Domain/Application 契约。

## 本机门槛

- 合成 100 万节点写入不超过 20 秒；
- 1000 节点分页查询 p95 不超过 50 ms；
- 存储层运行时 RSS 不超过 256 MB；
- 不能一次向 WebView 发送百万节点。

## 后果

需要维护 schema migration、快照清理和隐私策略。SQLite 驱动为 cgo 依赖，Windows 后续端口
需要单独验证，不影响当前 macOS 范围。
