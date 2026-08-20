# R-013 DaisyDisk 原生交互与开源参考复核

状态：已完成，结论由 ADR-0016 接受；前端状态模型待按新基线实现

日期：2026-08-20

## 1. 目的和问题

R-009 已经确定了 Marmot 要复刻的产品闭环，但 P0 实现完成后的复核表明，当前前端更接近一个
“扫描结果展示 Demo”，还没有达到 DaisyDisk 的原生桌面交互密度。问题不在于再调几组颜色，
而在于悬停、焦点、选中、下钻、历史、虚拟对象和 Collector 没有形成一套连续的状态模型。

本记录补齐两件事：

1. 用官方 Version 4 用户指南逐项核对可观察的交互和空间语义；
2. 复核 GitHub 上的同类实现、许可证和可借鉴边界，避免在实现阶段才发现产品或许可问题。

本记录不复制 DaisyDisk 的源码、商标、图片、文案或界面资产，也不把任何社区仓库自动变成
Marmot 的运行时依赖。

## 2. 方法和证据

### 2.1 官方产品资料

通过 Chrome DOM 读取以下公开资料，未把宣传页面的截图或性能数字当作实现事实：

- [Disk overview, scanning](https://daisydiskapp.com/guide/4/en/DisksOverview/)
- [Understanding the disk map](https://daisydiskapp.com/guide/4/en/UnderstandingSunburst/)
- [Locating space wasters](https://daisydiskapp.com/guide/4/en/LocatingSpaceWasters/)
- [Previewing file content and details](https://daisydiskapp.com/guide/4/en/Previewing/)
- [Deleting files](https://daisydiskapp.com/guide/4/en/DeletingFiles/)
- [Keyboard shortcuts and multi-touch gestures](https://daisydiskapp.com/guide/4/en/Hotkeys/)
- [Hidden space](https://daisydiskapp.com/guide/4/en/HiddenSpace/)
- [Purgeable space](https://daisydiskapp.com/guide/4/en/PurgeableSpace/)
- [Other volumes](https://daisydiskapp.com/guide/4/en/ReservedSpace/)
- [Local APFS snapshots](https://daisydiskapp.com/guide/4/en/Snapshots/)
- [Full Disk Access](https://daisydiskapp.com/guide/4/en/FullDiskAccess/)
- [Parallel scanning](https://daisydiskapp.com/guide/4/en/ParallelScan/)
- [Mismatches with Finder](https://daisydiskapp.com/guide/4/en/FinderMismatch/)
- [Tips & tricks](https://daisydiskapp.com/guide/4/en/TipsAndTricks/)

### 2.2 Marmot 当前实现

核对了 `frontend/src/App.tsx`、`frontend/src/styles.css`、Wails DTO 和本地 Vite DOM。当前事实是：

- 已有卷概览、容量 gauge、分阶段扫描、取消、部分结果、分页 Map、Sunburst、Inspector、
  Quick Look/Finder 入口、Collector 和 CleanupPlan 链路；
- Sunburst 的 `pointerenter` 只用于设置拖放属性，Inspector 只绑定 `selected`，没有独立的
  hover 对象；
- 目录通过双击进入，单击只选中对象；当前实现没有 DaisyDisk 的单击下钻语义；
- 面包屑可以回到父级，但没有独立的前进/后退历史游标；
- 全局快捷键覆盖返回父级、预览、加入 Collector、进入目录和当前目录刷新，但没有上下选择、
  Page Up/Page Down、Home/End、历史前进后退或全盘重扫；
- `MapEntry` 只有 `node` 和 `aggregate` 两类，没有 `smaller objects`、hidden space、purgeable
  space、other volumes、snapshot、restricted 或 stale 的显式能力和显示状态；
- Collector 保存的是 `NodeView[]`，可以展开、清空和创建计划，但还没有“像侧栏一样预览并拖出”
  的状态机；
- 页面结构是大标题区、卷卡片、扫描工具条、地图面板、Inspector 和底部 Dock，信息层级能工作，
  但与原生桌面工具的连续工作区仍有明显差距；
- 当前前端没有收藏目录、全树搜索、扫描历史、快照比较或速度/吞吐反馈。

## 3. DaisyDisk 可观察行为

### 3.1 启动和扫描入口

- 启动即列出所有已挂载卷，启动卷置顶，并用 gauge 让用户先判断剩余空间；
- gauge 默认把 purgeable space 计入可用空间，按住 `Option` 可以切换展示口径；
- 可扫描整卷或单个文件夹，也支持从 Finder 或 Dock 拖入；
- 常用文件夹可以固定，重启后仍出现在入口列表；
- SSD 可以并行扫描，同一机械盘的请求排队，避免磁头争用；
- 扫描过程是工作流的一部分，不应只有不可操作的等待页。

### 3.2 Sunburst 和侧栏

- 彩色扇区代表文件夹，灰色扇区代表文件，按空间贡献排序；
- 指向对象时，侧栏立即显示对象内容和基本信息；
- 点击文件夹下钻，点击中心回到父目录；
- `smaller objects` 是由大量小对象组成的虚拟聚合项，点击后展开完整列表；
- 已经被移动、改名或删除的对象以划线/失效状态保留，提示用户重新扫描；
- restricted 对象、hidden space、purgeable space、other volumes、snapshots 都是有语义的
  虚拟对象，不能当作普通文件夹或空目录处理。

### 3.3 预览和高频操作

- `Space` 在空间图、侧栏和 Collector 中都可以调用 Quick Look；
- `Return` 进入当前指向的文件夹或展开 `smaller objects`；
- 上下方向键、Page Up/Page Down、Home/End 选择当前目录对象；
- `Command + Up` 回到父目录，`Command + [ / ]` 进行历史前进后退；
- `Command + Delete` 把当前对象加入或移出 Collector，`Command` 点击在 Finder 中显示；
- `Command + R` 重扫当前目录，`Shift + Command + R` 重扫整卷或顶层目录。

### 3.4 Collector

- 文件或文件夹可以拖入底部 Collector，也可以通过上下文菜单或快捷键加入；
- 加入 Collector 不会立刻改变文件；
- 展开后 Collector 像侧栏一样支持预览、逐项移除和拖出；
- `smaller objects`、扫描根、卷根、`/System`、`/Library` 和当前用户 Home 等高风险对象不能
  直接进入 Collector；
- 点击删除后仍有短暂取消窗口；DaisyDisk 最终采用永久删除，Marmot 必须保留“计划、复核、
  版本校验、移入废纸篓”的安全差异。

### 3.5 空间解释

- hidden space 表示可见扫描结果与卷使用量之间的未解释差额；
- purgeable space 主要包含可回收快照、缓存、swap、sleep image 和临时系统文件，macOS 可能
  异步更新其数值；
- APFS 的 other volumes 与 local snapshots 需要独立展示，不得重复计入普通文件树；
- hard link 和 APFS clone 共享块时，逻辑大小、实际占用和去重归属必须分开；
- 权限不足时不能把目录伪装成空目录，结果必须保留受限状态和可信度。

## 4. 差距矩阵

| 能力 | DaisyDisk 行为 | Marmot 当前 | 结论 |
| --- | --- | --- | --- |
| 工作区结构 | 概览、空间图、侧栏和 Collector 连续工作 | 通过 Hero、卷卡片和面板拼接，能工作但偏 Web Dashboard | P0 重做工作区层级 |
| 悬停解释 | 悬停即更新侧栏 | 只有点击后 Inspector 才更新 | P0 增加 `hovered` 状态 |
| 下钻语义 | 单击文件夹进入，中心返回 | 单击选择、双击进入 | P0 统一点击/键盘命令 |
| 小对象 | `smaller objects` 虚拟项，展开后分页 | 只有通用 `aggregate`，点击后直接跳下一页 | P0 显式建模和显示能力 |
| 历史导航 | `Command + [ / ]`、父级和面包屑一致 | 只有面包屑，没有历史游标 | P0 增加有限历史栈 |
| 键盘选择 | 方向键、分页、Home/End、Return、Space | 缺少连续选择和大部分快捷键 | P0 使用 roving focus |
| Collector | 可拖入、展开、预览、拖出、移除 | 可加入、清空、建计划，不能拖出 | P0 完成会话状态机 |
| 失效对象 | 移动/删除后保留失效样式 | 没有 stale 显示状态 | P0 增加刷新和失效表达 |
| 虚拟空间 | hidden/purgeable/other volumes/snapshot/restricted | DTO 和 UI 没有类型能力 | P0 先锁契约，按范围实现 |
| 收藏入口 | 常用目录重启后保留 | 没有持久化收藏 | P1 |
| 搜索 | 可从扫描结果快速定位大对象 | 没有搜索 | P1，参考 OpenDisk/Radix |
| 扫描历史 | 重扫、实时容量更新、忘记结果 | 没有历史和缓存命中反馈 | P1，先完成当前交互后再加 |
| 口径切换 | gauge 可查看 purgeable 口径 | 后端保留多种大小，UI 只有固定口径 | P1，不能改变默认 `owned_allocated` |
| 多窗口、触控板手势、管理员扫描 | 产品支持或有明确入口 | 当前不支持 | P2，需单独 ADR/TCC 预研 |

## 5. 开源社区复核

### 5.1 可作为合法参考的 MIT 项目

以下版本只作为研究快照，若未来复制代码，必须固定提交、保留原许可证和版权声明，并先放入
`third_party/` 或独立适配层。当前不直接复制任何一个项目的 UI 代码。

| 项目 | 固定提交（2026-08-20） | 主要补充 | Marmot 用法 |
| --- | --- | --- | --- |
| [Radix](https://github.com/colinvkim/Radix) | `8dff58f32cdcfd1cef4a6faa74656fa6bc891279`，MIT | 727 stars；Sunburst/Treemap 切换、悬停、可排序文件表、搜索、历史、收藏位置、扫描比较、快照导出、Inspector、Trash | 首要产品体验和状态模型参考 |
| [OpenDisk](https://github.com/137137137/OpenDisk) | `16765d0a13cdb38ab1c2db1391ef3d9e2b9af30e`，MIT | `getattrlistbulk`、`searchfs`、固定 worker、实时流式结果、增量缓存、APFS volume group/firmlink、全树搜索、Collector | 首要扫描与 Sunburst 参考 |
| [Disk Bloom](https://github.com/jgalea/disk-bloom) | `626186938bff61c5fa7c2ea1650acf4fc0719a0c`，MIT | DaisyDisk 风格动画、hover、中心返回、Quick Look、Collector、Trash、受限和管理员扫描 | 交互细节和原生动作参考 |
| [StorageScope](https://github.com/RasputinKaiser/StorageScope) | `3174a6cdb50cb502a80302c5e2a4a85f15a683b7`，MIT | Verified/Review 风险分层、Cleanup Review、嵌套选择折叠、Trash 事务、缺失项检测、键盘测试 | 清理计划和验证 UX 参考 |
| [Flare Scan](https://github.com/flare-collection/flare-scan) | `44e58f240b5520f1435bb36821be817456b2b04d`，MIT | 本地 baseline、What Changed、Sunburst/Treemap、重复项、诊断、导出、Trash | 后续差异分析和审计参考 |
| [MacDirStat](https://github.com/phalladar/MacDirStat) | `225f65ab1efda7eee50d76e3a29d123ba191b960`，MIT | BSD `fts` 扫描、AsyncStream 节流、Treemap、目录树、Inspector、allocated size 切换 | 大规模扫描和 UI 流式更新参考 |
| [DiskAtlas](https://github.com/AloysAugustin/diskatlas) | `188d53165810e6057986f568a8bd06dc0bb02346`，MIT | Treemap 渲染、结构化节点存储和大规模布局思路 | 只参考性能数据结构和布局 |
| [glisk](https://github.com/d0ugal/glisk) | `58f337b971194cdbce49c1ffb26a3f4d6ecb1fba`，MIT | Sunburst 动画、颜色继承、hover、focus path、Top-K 聚合 | 只参考视觉算法；其 HTTP 服务不进入 Marmot 生产架构 |

这些项目的 README、stars 和活跃度只能说明参考价值，不能证明其实现已经通过 Marmot 的
APFS、TCC、百万级节点或清理安全门槛；关键行为仍需在 Marmot 自己的 macOS 预研中验证。

### 5.2 只能阅读，不能复制或集成

| 项目 | 原因 | 可保留的研究价值 |
| --- | --- | --- |
| [Spacie](https://github.com/AlexGladkov/Spacie) | Non-Commercial License，不能作为当前项目的代码来源 | arena 文件树、FSEvents 增量缓存、渐进哈希、Drop Zone 和键盘优先交互 |
| [MangoDisk](https://github.com/harry0703/MangoDisk) | GPL-3.0；不能混入当前闭源/独立许可证边界 | 清理规则来源审计、风险分类、操作历史、跨平台产品信息架构 |
| [QDirStat for macOS](https://github.com/jesusha123/qdirstat-macos) | GPL-2.0 | 目录树 + Treemap 的经典信息结构 |
| [MichaelStromberg/macdirstat](https://github.com/MichaelStromberg/macdirstat) | GPL-3.0 | `getattrlistbulk` + rayon 的性能思路 |
| 当前 Mole `main` | GPL-3.0 | 只能阅读；Marmot 已锁定旧版 MIT 扫描代码边界 |

### 5.3 许可证不充分的项目

`kbrady1/DiskBloom`、`avantigroupai/DiskX`、`Chartres/mac-dir-stat` 和
`ashwinn-si/diskManager` 的仓库页没有发现可确认的 LICENSE 文件，即使 README 使用了
“open-source”或“free”描述，也不能据此复制代码。它们可以作为产品文案、键盘流程和信息架构
的黑盒参考，不能进入 `third_party/`。

`DaisyDisk-Mac-Q/DaisyDisk-Mac`、`Daisy-Disk-Mac/DaisyDisk-Mac-App` 等搜索结果主要是指南、
SEO 页面或没有可信实现的仓库，不作为代码或设计资产来源。

## 6. 补充后的实现基线

### P0：必须先完成的原生交互状态

- 工作区改为“卷/范围入口 + 当前空间图 + Inspector + Collector”的连续桌面结构；
- 空间图至少区分 `hovered`、`focused`、`selected`、`stale`，悬停只更新展示，不触发 Wails；
- 文件夹单击下钻，文件单击选中，中心返回父目录，Return 使用相同命令；
- 键盘采用当前层稳定排序的 roving focus，覆盖方向键、Page Up/Page Down、Home/End、Return、
  Space、`Command + Up`、`Command + [ / ]`、`Command + Delete`、`Command + R` 和
  `Shift + Command + R`；
- `smaller objects`、hidden space、purgeable space、other volumes、snapshots、restricted
  和 stale 必须有明确类型和能力矩阵；
- Collector 支持拖入、展开、预览、移除、拖出和计划审查；加入 Collector 不能产生文件操作；
- 对象变化后显示失效状态，重新扫描或重新查询后才能恢复当前事实；
- 前端只持有当前层 DTO、有限历史页和会话收集区，不恢复为百万级内存树。

### P1：完成 P0 后再做

- 常用目录收藏和最近扫描入口；
- 当前扫描范围内搜索，以及按大小/名称/类型排序的列表；
- 扫描差异、缓存命中、速度/吞吐和快照导出；
- gauge 的 purgeable 展示口径切换，但默认仍使用 Marmot 的明确大小口径。

### P2：不进入当前实现

- 多窗口、触控板手势、云存储、管理员/root 扫描和 APFS snapshot 清理；
- 任何 AI 自动选择、授权或执行文件操作。

## 7. 对 DDD、SDD 和 ADR 的影响

### DDD

- 增加悬停对象、焦点对象、选中对象、浏览历史、虚拟空间对象和收集区状态的统一语言；
- 明确虚拟对象不持有文件身份，聚合项和受限项不能绕过 Application 进入预览、定位或清理；
- 明确导航和 Collector 是展示/应用状态，不能改变扫描快照事实。

### SDD

- `MapEntry` 从 `node | aggregate` 扩展为 `node | aggregate | virtual` 的目标契约，并附带
  `virtualType`、显示状态和可执行能力；
- 增加前端状态预算、历史栈上限、键盘焦点规则和 250 ms 受影响父节点刷新规则；
- P0 体验验收不再以“按钮可点击”作为完成标准，而以交互状态和错误边界作为标准。

## 8. 结论和限制

社区已经存在比当前 Marmot 更接近目标的产品实现，尤其是 Radix、OpenDisk 和 Disk Bloom；
因此后续不需要再探索“有没有答案”，需要把这些已经验证过的交互模式转换成 Marmot 自己的
Wails/React 状态模型。扫描算法和清理安全仍不能只凭 README 采信。

本记录只记录截至 2026-08-20 的公开页面和固定提交；项目主分支会继续变化。任何真正复制代码的
动作都必须重新确认提交、许可证、版权声明、依赖许可证和第三方声明，并新增或更新对应 ADR。

## 9. 建议 ADR

[ADR-0016 DaisyDisk 原生交互状态模型](../adr/0016-DaisyDisk原生交互状态模型.md) 已接受本记录结论。
