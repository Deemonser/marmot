# ADR-0013 DaisyDisk 空间图与渐进查询数据契约

状态：Accepted

日期：2026-08-20

## 背景

DaisyDisk 的核心体验是先看到当前目录的空间分布，再进入子目录继续下钻。Marmot 的扫描结果可能达到百万级节点，不能把完整树一次绑定到 WebView；直接复用裸 `GetChildren` 也无法表达当前快照版本、截断后的剩余空间和聚合项。

## 预研依据

- [R-011 Sunburst 空间图与渐进查询数据契约](../research/R-011-Sunburst空间图与渐进查询数据契约.md)
- [R-009 DaisyDisk 产品体验与交互基线](../research/R-009-DaisyDisk产品体验与交互基线.md)
- [R-004 百万级节点性能与快照存储](../research/R-004-百万级节点性能与快照存储.md)
- [ADR-0007 SQLite 快照存储与性能门槛](0007-SQLite快照存储与性能门槛.md)
- [ADR-0003 全盘扫描与快照架构](0003-全盘扫描与快照架构.md)

## 决策

### 1. 使用渐进的空间图查询

Application 对外增加 `GetMap(MapQuery)`，由 Wails 暴露强类型 DTO。查询契约为：

```text
MapQuery
  snapshotId
  parentId
  limit       默认 256，最大 1000
  offset      默认 0
  measure     owned_allocated（第一阶段固定）
```

返回契约为：

```text
MapResult
  snapshotId
  snapshotVersion
  parent
  entries[]
  total
  limit
  offset
  hasMore
  remaining
  confidence
```

`entries` 按 `owned_allocated DESC, nodeId` 稳定排序。返回值只包含当前父节点的直接项，不包含嵌套 `children`；单次 Wails 绑定返回不得超过 256 KB。

### 2. 区分真实节点和空间聚合项

- `kind=node` 是快照中的真实扫描节点，可以进入目录、预览、定位或加入清理计划，但仍必须经过 Application 校验。
- `kind=aggregate` 是被截断的小对象汇总，只能继续展开查询，不能预览、定位、加入清理计划或交给清理执行。
- `remaining` 和聚合项沿用 `owned_allocated`、逻辑大小、实际占用、去重后占用和可信度口径；聚合项不写入 `scan_nodes`，不伪造 `device/inode`。

### 3. 以前端当前层为状态边界

前端只保留当前层、面包屑、当前选中真实节点和最近最多 32 个目录页的可丢弃 DTO 缓存。D3 只负责 Sunburst/Treemap 的布局、命中区域和过渡，不负责扫描、文件系统访问、缓存失效或清理判断。

扫描事件只通知 `snapshotId`、版本、阶段和受影响的父节点 ID。当前目录收到更新后，前端以 250 ms 防抖重新调用 `GetMap`；响应版本落后时丢弃并重新查询。窗口重开不能依赖事件，必须通过任务和快照查询恢复。

现有 `GetChildren` 可以作为内部兼容适配保留，但新 UI 不以裸节点数组作为空间图协议。

### 4. 约束对象操作入口

预览、Finder 定位和清理计划只接收 `snapshotId + nodeId`。Wails 不接收任意路径，也不允许前端用聚合项或路径字符串授权文件操作。

## 备选方案

- 一次性把完整目录树传给 WebView：拒绝，载荷、内存和布局成本随节点数增长，无法承载百万级快照。
- 只返回前 N 个节点、不返回剩余汇总：拒绝，空间图会错误地丢失小对象占用。
- 让 D3 直接读取数据库或文件系统：拒绝，破坏前端隔离和 Wails 安全边界。

## 后果

- Wails/Application/存储层需要增加 `MapQuery`、`MapResult` 和聚合查询实现。
- 前端状态管理从全量树改为当前层查询和有限缓存；扫描更新后需要按父节点刷新。
- 聚合项不能复用清理项模型，UI 必须清楚表达其“只能展开”的状态。
- 需要在实现切片中验证高扇出目录的聚合查询 p95 和 Wails 实际载荷大小。

## 验收标准

- 100 万节点快照不会被一次性绑定到 WebView。
- 默认空间图查询返回最多 256 项，分页查询最多 1000 项，并保持稳定排序。
- 截断结果包含剩余空间和可信度，且聚合项不能进入预览、定位或清理计划。
- 快照版本变化时，前端不会继续展示旧页作为当前事实。
