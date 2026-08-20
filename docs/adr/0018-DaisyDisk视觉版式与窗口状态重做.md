# ADR-0018 DaisyDisk 视觉版式与窗口状态重做

状态：Accepted

日期：2026-08-20

## 背景

R-014/ADR-0016 已锁定 DaisyDisk 的交互状态，但当前实现仍把启动态和结果态放在同一套 Dashboard
结构中，并通过后置 CSS 覆盖模拟原生效果。结果是功能可用、视觉层级不对：启动态过于松散，结果态有
常驻 Inspector 和横跨底部 Dock，Sunburst 也没有成为第一视觉主体。

## 依据

- [R-014 DaisyDisk 本机实机交互复核](../research/R-014-DaisyDisk本机实机交互复核.md)
- [R-016 DaisyDisk 视觉版式与窗口状态基线](../research/R-016-DaisyDisk视觉版式与窗口状态基线.md)
- [ADR-0016 DaisyDisk 原生交互状态模型](0016-DaisyDisk原生交互状态模型.md)
- [ADR-0017 有界多层空间图投影](0017-有界多层空间图投影.md)

## 决策

### 1. 分离启动态和结果态的视觉骨架

- 启动态使用紧凑卷选择器：卷行、使用进度、查看/扫描下拉和扫描文件夹入口。
- 结果态使用连续桌面工作区：顶部导航、左侧 Sunburst、右侧当前目录列表、左下 Collector。
- 两种状态可以共享 React 用例和 DTO，但不共享 Dashboard 容器视觉；状态类由应用根节点明确表达。

### 2. 结果页取消常驻 Inspector 主栏

当前目录列表是右侧主导航，不再被 Inspector 卡片替代。悬停、焦点和选中对象的 Quick Look、Finder、
Collector 等动作只能呈现为窄上下文条或局部浮层，不能改变列表和 Sunburst 的主比例。

### 3. Collector 改为左下投放区

Collector 默认固定在结果工作区左下角，打开时只从该位置展开局部面板。加入、移除和拖出仍是会话状态，
不会因视觉重做而直接操作文件。

### 4. Sunburst 使用有界真实投影

沿用 ADR-0017 的 `children` 投影和载荷预算。视觉实现使用多层同心环、同分支色相继承、深度亮度区分、
聚合/虚拟对象低饱和度标识；不得用重复当前层条目伪造多层效果。

### 5. 视觉参数和品牌边界

按 R-016 的尺寸、间距和颜色语义实现，保留 Marmot 自有品牌色和安全文案，不复制 DaisyDisk 私有素材、
源码、商标或永久删除行为。试用/购买仅作为窗口层级占位，不实现支付。

## 被拒绝方案

- 继续给旧 Dashboard 叠加 CSS 覆盖：会保留错误的 DOM 层级和布局占位，无法稳定验收。
- 把右侧 Inspector 扩大为结果页主栏：与本机原版的当前目录列表语义冲突。
- 使用全宽底部 Dock 模拟 Collector：与结果图、列表的空间关系不一致。
- 为了视觉效果把未查询的后代画成真实节点：违反 ADR-0017 的快照和性能边界。

## 后果

- `frontend/src/App.tsx` 需要保留领域交互逻辑但调整启动态/结果态 DOM 和 Collector 挂载位置。
- `frontend/src/styles.css` 需要整体重写为单一版式来源，删除旧 Dashboard 与后置覆盖的重复规则。
- Wails、Domain、Application、SQLite 和扫描契约不变；本 ADR 只改变前端表现层和窗口状态表达。
- 真实 Wails 窗口的尺寸、圆角和标题栏仍须在 macOS 打包 smoke test 中验证。

## 验收标准

- 启动态符合 R-016 的紧凑卷行结构，结果态符合 Sunburst/目录列表/Collector 连续工作区结构。
- 968 x 715 附近的结果窗口没有常驻 Inspector 卡片、全宽底部 Dock 或主区域重叠。
- 多层 Sunburst 使用真实有界 `children` 投影，目录列表与空间图状态同步。
- 现有扫描、预览、Finder、Collector、清理计划和键盘导航测试继续通过。
- TypeScript、Vite build、Wails build/package 和浏览器 DOM 结构检查通过。
