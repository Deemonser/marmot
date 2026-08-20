# R-011 Sunburst 空间图与渐进查询数据契约

状态：已完成，结论由 ADR-0013 接受

日期：2026-08-20

## 1. 问题

DaisyDisk 的空间图要求用户可以在当前目录看到按占用排序的对象、进入子目录并回到父目录，但百万级扫描不能把完整树一次发送给 WebView。当前 `GetChildren` 只有节点数组、limit 和 offset，缺少父节点上下文、截断聚合和当前快照版本，无法稳定承载 Sunburst。

## 2. 方法与证据

- 核对 DaisyDisk Sunburst、`smaller objects`、侧栏和快捷键交互说明。
- 核对 D3 hierarchy/layout 的使用方式：D3 只需要当前布局所需的层级数据，不需要接管数据存储或文件系统。
- 核对 R-004 的 256 KB 绑定载荷预算、1000 节点分页和稳定排序索引。
- 核对 Marmot 当前 Wails DTO、SQLite `Children` 查询和前端状态管理。

## 3. 发现

- 空间图只需要当前父节点的直接子项；进入目录后再查询下一层。
- 直接截断前 N 项会让用户看不见剩余空间，必须返回剩余项的汇总，才能绘制 `smaller objects`。
- 聚合项不是观察事实，不能被预览、选入清理计划或传给清理执行。
- 当前层更新时，前端应按父节点重新查询，而不是维护一棵前端全量树。
- 稳定排序必须使用 `owned_allocated DESC, node_id`；否则扫描批次变化会导致界面跳动。

## 4. 决策结论

### 4.1 查询模型

新增一个面向空间图和目录列表的强类型查询，保留 `SnapshotStore` 的分页边界：

```text
MapQuery
  snapshotId
  parentId
  limit       默认 256，最大 1000
  offset      默认 0
  measure     owned_allocated（首版固定）
```

返回：

```text
MapResult
  snapshotId
  snapshotVersion
  parent          当前父节点摘要
  entries[]       真实节点或空间聚合项
  total
  limit
  offset
  hasMore
  remaining       被截断条目的汇总
  confidence
```

`entries` 的单项为 `MapEntry`：

- `kind = node`：携带 `NodeView`，可以进入、预览或进入清理计划；
- `kind = aggregate`：携带名称、条目数、三种大小、可信度和原因，只能展开，不能执行文件操作。

`remaining` 和 `aggregate` 使用与节点相同的大小口径，且必须带可信度。空间聚合项不写入 `scan_nodes`，不产生伪造的 `device/inode`。

### 4.2 前端状态

前端只维护：

- 当前父节点和面包屑；
- 当前父节点的 `MapResult`；
- 最近最多 32 个目录页的可丢弃缓存；
- 当前选中的真实节点和会话内 `CleanupPlan`。

D3 只负责 Sunburst/Treemap 的布局、命中区域和过渡，不负责扫描、缓存、权限或清理判断。

### 4.3 更新和事件

- 事件只发送 `snapshotId`、版本、阶段和受影响父节点 ID；
- 前端对当前父节点的刷新做 250 ms 防抖；
- 事件到达后，如果当前目录受影响，重新调用 `GetMap`；否则只更新状态徽标；
- 查询响应中的 `snapshotVersion` 过期时，前端丢弃旧页并重新查询；
- 页面销毁或窗口重开不依赖事件，按 task/snapshot 查询恢复。

### 4.4 交互规则

- 点击 `node` 目录进入下一层；
- 点击中心返回父节点；
- 点击 `aggregate` 进入该聚合项的展开查询，不将它视为清理候选；
- 排序、大小口径和可信度始终在界面可见；
- 权限受限、部分结果、文件变化和缓存复核使用独立视觉状态；
- 预览、Finder 定位和清理计划只接受真实 `nodeId`，不接受前端任意路径。

## 5. 载荷和性能预算

- Sunburst 默认返回 256 个直接项；目录列表可以请求最多 1000 个；
- 单次 Wails 返回仍不超过 256 KB；
- 不返回嵌套 children，不向 WebView 发送完整树；
- 当前层 p95 查询目标不超过 50 ms（本机 SQLite 合成基线），Wails 绑定另测；
- 页面缓存只缓存 DTO，不缓存清理授权或文件内容。

## 6. 限制

D3 的具体视觉参数、颜色和过渡时间属于产品实现细节，不在本 ADR 中锁定；P0 只锁定数据和状态契约。高扇出目录的 `remaining` 聚合查询需要在 SQLite migration/索引实现后跑真实 p95。

## 7. 对 DDD/SDD 的影响

- 增加“空间图条目”和“空间聚合项”统一语言；聚合项不是 `ScanNode`。
- `ScanSnapshot` 的版本成为前端查询一致性依据。
- Wails 增加 `GetMap` 行为契约，`GetChildren` 可以作为内部兼容适配，但新 UI 不直接依赖裸节点数组。
- SDD 明确前端只持有当前层和有限页缓存，D3 不越过 Presentation 访问文件系统。

## 8. 建议 ADR

[ADR-0013 DaisyDisk 风格空间图和渐进查询数据契约](../adr/0013-DaisyDisk空间图与渐进查询数据契约.md) 已接受本记录结论。
