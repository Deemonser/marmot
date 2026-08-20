# ADR-0016 DaisyDisk 原生交互状态模型

状态：Accepted

日期：2026-08-20

实施状态：P0 状态模型和安全边界已落地；依据本机实测新增的原生布局重做进行中。TypeScript、
Go 测试、go vet、Wails macOS 构建和 Chrome DOM 交互验证已通过。真实签名/TCC、原生
Quick Look/Finder、跨卷废纸篓和全盘样本仍属于发布前验证。

## 背景

R-009 的基础体验切片已经让 Marmot 能扫描、查询空间图、预览对象和创建清理计划，但前端仍以
“点击选中后操作”为主。DaisyDisk 的效率来自一套连续的桌面交互：悬停即解释、单击下钻、中心返回、
键盘保持焦点、历史可回退、虚拟空间可展开、Collector 可审查但不立即修改文件。

如果继续在现有页面上零散增加按钮，会产生多套相互冲突的选择、导航和清理状态，也会让百万级
节点的分页契约被前端状态绕过。

## 预研依据

- [R-013 DaisyDisk 原生交互与开源参考复核](../research/R-013-DaisyDisk原生交互与开源参考复核.md)
- [R-014 DaisyDisk 本机实机交互复核](../research/R-014-DaisyDisk本机实机交互复核.md)
- [R-009 DaisyDisk 产品体验与交互基线](../research/R-009-DaisyDisk产品体验与交互基线.md)
- [ADR-0013 DaisyDisk 空间图与渐进查询数据契约](0013-DaisyDisk空间图与渐进查询数据契约.md)
- [ADR-0015 macOS 预览、Finder 定位与收集区平台边界](0015-macOS预览Finder定位与收集区平台边界.md)

## 决策

### 1. 采用单一桌面工作区

扫描结果页固定由四个互相协作的区域组成：

```text
范围/卷入口 -> 当前空间图 -> 当前目录列表/上下文动作
                           |
                       Collector
```

卷概览仍是启动入口，但进入结果后不再用营销式 Hero、卷卡片或常驻 Inspector 卡片承载主要工作流。
结果首屏必须是 Sunburst、当前目录标题/排序列表和底部 Collector 的连续桌面结构；布局、颜色和
资产由 Marmot 自己定义，目标是信息密度和连续操作，不复制 DaisyDisk 的品牌外观。

### 2. 分离悬停、焦点和选中

前端必须维护以下互不替代的状态：

| 状态 | 作用 | 生命周期 |
| --- | --- | --- |
| `hoveredEntry` | 指针当前指向的条目，驱动 Inspector 临时展示 | 指针进入/离开空间图或列表 |
| `focusedEntry` | 键盘操作目标，必须有清晰焦点样式 | 当前层内移动或导航后重置 |
| `selectedEntry` | 用户明确选中的对象，供预览、Finder 和 Collector 操作 | 点击、键盘确认或导航后更新 |
| `staleEntry` | 快照对象已变化，仍保留用于解释和重扫提示 | 查询或平台校验发现变化 |

悬停和焦点只操作当前已加载的 DTO，不调用 Wails。当前目录列表始终显示 `currentParent` 的直接子项；
上下文动作的展示优先级为 `hoveredEntry -> focusedEntry -> selectedEntry -> currentParent`。鼠标离开后
恢复焦点或选中对象，不能把上下文动作区误做成替代当前目录列表的常驻 Inspector。

### 3. 固定空间图命令

空间图和文件列表必须复用同一组命令：

| 输入 | 行为 |
| --- | --- |
| 文件夹单击/Return | 进入该目录并把当前位置写入历史；右侧列表和 Sunburst 共用命令 |
| 文件单击 | 更新焦点和选中对象，不进入目录；右侧列表保持蓝灰高亮 |
| 中心单击/`Command + Up` | 回到父目录 |
| `smaller objects` 单击/Return | 展开虚拟聚合项，使用分页查询，不伪造节点 |
| 上下方向键 | 在当前稳定排序条目中移动焦点 |
| Page Up/Page Down/Home/End | 在当前页或已加载页范围内移动焦点 |
| Space | 对真实节点调用 `PreviewNode(snapshotId, nodeId)` |
| `Command + Delete` | 加入或移出会话 Collector，不执行文件操作 |
| `Command` 点击/上下文命令 | 对真实节点调用 `RevealNode(snapshotId, nodeId)` |
| `Command + R` | 重查当前目录；`Shift + Command + R` 请求重扫范围 |

目录单击下钻是 P0 目标；实现可以保留双击兼容，但双击不能成为唯一进入方式。

### 4. 导航历史是有上限的应用状态

导航记录保存 `snapshotId`、`parentId`、路径标签、查询口径和页偏移，最多保留 32 个条目。
前进新路径时丢弃历史游标之后的分支；返回或前进必须重新调用 `GetMap`，不能只从旧页面猜测当前事实。
快照版本变化后，历史 DTO 可以丢弃并回到仍有效的最近父目录；过期条目显示 stale，而不是静默打开新路径。

### 5. 显式建模虚拟空间对象和能力

`MapEntry.kind` 的目标取值为：

```text
node       真实扫描节点，拥有 snapshotId + nodeId
aggregate  分页剩余项的统计聚合，不拥有文件身份
virtual    hidden/purgeable/other volumes/snapshot/restricted 等解释对象
```

`aggregate` 和 `virtual` 必须有 `virtualType`、大小口径、可信度和能力集合。能力集合至少区分：

```text
enter | preview | reveal | collect | rescan
```

默认能力：

- `node` 根据节点类型和权限决定能力；
- `aggregate` 只有 `enter`；
- `smaller_objects` 只有 `enter`，展开后产生真实节点；
- `hidden_space`、`restricted` 只有解释和重新扫描/权限引导能力；
- `purgeable_space`、`other_volumes`、`snapshot` 第一阶段只读展示，不进入 CleanupPlan；
- stale 条目只允许查看原因和重扫，不允许预览、定位或清理。

虚拟类型、文件身份、权限和可信度由 Domain/Platform 提供；Application 根据当前快照和平台能力
计算 `displayState` 与 `capabilities`，Wails 只负责类型化转换。前端可以隐藏不适用的操作，但不能
通过本地状态授予能力，所有 Preview、Reveal 和 Cleanup 仍回到 Application 校验。

### 6. Collector 是会话状态机，不是删除队列

Collector 状态为：

```text
Closed -> Open -> Staged -> Reviewing -> PlanValidated -> PlanConfirmed -> Applying
   ^       |        |            |              |               |             |
   +-------+--------+------------+--------------+---------------+-------------+
```

加入、移除、展开、预览和拖出只改变 Presentation/Application 会话状态。创建计划后必须展示来源
快照、口径、风险、逐项对象和计划版本；执行前沿用 ADR-0009 的再次校验，默认移入 macOS 废纸篓。
根目录、聚合项、虚拟项、权限不明项和父子重叠项必须在加入或建计划时拒绝并说明原因。

### 7. 性能和通信边界不变

- 悬停、焦点和键盘移动不能触发文件系统访问或 Wails 往返；
- 前端只保留当前层、最多 32 个历史页和 Collector DTO；
- 当前层受扫描事件影响时按 250 ms 防抖重新查询，旧 `snapshotVersion` 响应丢弃；
- 空间图仍按 `owned_allocated` 稳定排序，单次响应最多 256 KB；
- 真实节点的预览、定位和清理仍只接受快照 ID + 节点 ID；
- Wails 不新增 HTTP 服务，不把任意路径或虚拟对象映射成文件操作。

## 被拒绝的方案

- **继续在现有 Demo 上叠加按钮**：会把 hover、focus、selection、history 和 Collector 状态混在
  React 回调中，无法验证交互一致性。
- **前端接收完整文件树实现全局导航**：违反百万级节点分页和内存预算。
- **把聚合项或 hidden space 当成普通 NodeView**：会让虚拟对象绕过快照身份校验进入文件操作。
- **直接复制 DaisyDisk 或未审查社区项目的界面/代码**：违反品牌、许可证或第三方声明边界。
- **让 hover 每次调用 Go 获取详情**：会放大事件频率并破坏 Wails 绑定边界；详情必须来自当前 DTO。

## 后果

- 结果工作区需要以统一状态模型实现，并按 R-014 实测重做布局；状态模型和安全边界已完成，
  原生布局重做仍是当前 P0 工作；
- Wails DTO 需要补充虚拟类型、显示状态和能力集合，但不改变文件操作只接受真实节点的安全契约；
- Application 需要提供有界历史所需的查询和失效语义；
- 需要增加前端交互测试，至少覆盖目录列表与 Sunburst 同步、文件单击选中、hover 回退、键盘移动、
  历史分支、聚合展开、Collector 拖出和虚拟对象拒绝；
- 收藏、全树搜索、扫描比较、快照导出等能力暂列 P1，不得插入 P0 重做范围。

## 验收标准

- 悬停真实条目能在不调用 Wails 的情况下更新 Inspector，离开后恢复焦点/选中对象；
- 当前目录列表和 Sunburst 同时呈现同一批稳定排序条目；目录单击进入，文件单击只高亮，不改变目录；
- 中心和 `Command + Up` 返回，`Command + [ / ]` 可在 32 条以内历史中前进后退；
- 键盘方向键、Page Up/Page Down、Home/End、Return、Space 和 `Command + Delete` 对同一当前条目生效；
- `smaller_objects`、hidden、purgeable、other volumes、snapshot、restricted 和 stale 在 UI 中
  有独立显示和能力限制；
- Collector 能展开、逐项预览、移除和拖出，加入/移除阶段不发生文件操作；
- 启动入口为紧凑卷行；扫描进行中在卷行内显示进度、扫描中和取消；已有结果可通过查看下拉重扫、
  放弃或在 Finder 中显示；
- 计划必须经过校验和精确版本确认，执行仍只能移入废纸篓；
- 前端交互测试证明不会一次加载百万级节点，也不会对每次 hover 产生 Wails 调用。
