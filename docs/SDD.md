# SDD 系统设计

状态：技术基准和高风险预研 ADR 已冻结，P0 业务垂直切片已落地，追加式快照格式、`SnapshotStore` 生产适配和 Darwin 原生扫描主循环已接入；R-044 正确性已通过，R-045 锁争用切片已接受，代码切片与真实全盘二进制快照终态验证仍有门禁

SDD 是项目实现门禁。代码、接口和模块必须先在这里定义边界、依赖和验收标准。

文档基准和目录职责见 [BASELINE](BASELINE.md) 与 [项目目录规范](PROJECT-STRUCTURE.md)。

## 1. 产品范围

第一阶段只支持 macOS，但产品目标是扫描主 APFS 卷组的逻辑文件系统并解释卷自身、容器和文件树空间使用。
System/Data firmlink 只在逻辑命名空间中观察一次；嵌套挂载卷必须独立表达。Windows 只保留平台端口，不进入当前实现。

## 2. 技术栈

| 层 | 方案 |
| --- | --- |
| 桌面运行时 | Wails `v3.0.0-beta.9` |
| 后端 | Go 1.25+，本机验证 Go 1.25.13 |
| 前端 | React + TypeScript + Vite |
| 可视化 | D3.js |
| 生产通信 | Wails 类型化 Go-JS 绑定和应用事件 |
| 本地快照 | `SnapshotStore` 端口；追加式二进制快照、固定目录索引、提交校验和、`pread/mmap` 查询，不引入 SQLite 运行时依赖 |
| 测试 | Go test、go vet、前端 Vite build；前端交互切片后增加 Vitest 和 Wails 端到端测试 |

## 3. 总体架构

```text
Wails Window
  React Web UI + D3
        |
类型化 Go-JS 绑定 + Wails 事件
        |
Application 用例层
        |
Domain: Scan / Cleanup / Recommendation
        |
Ports: Scanner / SnapshotStore / Trash / Permission / Volume
        |
macOS Platform Adapters
```

### 依赖规则

- Web 只能调用 Wails 暴露的白名单方法和事件。
- Wails 层只做参数转换、事件转发和窗口能力，不实现业务规则。
- Application 编排用例，不能直接依赖 macOS API。
- Domain 不依赖 UI、Wails、数据库或 Mole。
- Infrastructure 实现扫描、快照存储和任务运行；允许承载固定版本的 Mole 扫描代码。
- Platform 实现文件系统、权限、废纸篓、卷和 Finder 能力。
- Platform 的 `Volume` 只返回 APFS 技术卷事实；Application 负责生成面向启动页的 `StorageSource` 产品读模型。
- Windows 后续只增加 Platform Adapter，不改变 Domain/Application 契约。

Mole 代码若复用，只能来自固定的 MIT 提交 `V1.40.0`，并放在 Infrastructure 的独立目录中。其输出必须先映射为 Marmot 自己的扫描节点和快照模型，不能让 Mole 的 JSON 或内部对象成为公共契约。

目录结构切片必须遵守 [项目目录规范](PROJECT-STRUCTURE.md)，业务切片不得绕过端口直接依赖
快照文件、SQLite、Wails 或 macOS API。

## 4. Wails 运行与安全边界

生产构建嵌入前端资源，通过 Wails 绑定直接调用 Go 服务，不启动本地 HTTP 服务，也不把端口、Token 或 CORS 暴露给浏览器。

第一阶段采用 Developer ID 签名和公证的直装分发；App Store 沙盒、CLI 和 HTTP 服务不在当前范围。
开发构建由 `darwin:sign:dev` 用本机稳定的自签名身份签名（默认 `Marmot Dev Signing`，可用
`MARMOT_SIGN_IDENTITY` 覆盖；身份不在钥匙串时退回 ad-hoc 并打印提示）。这不是为了分发，而是为了 TCC：
macOS 按 bundle 的**指定要求**记录权限授予，ad-hoc 签名的要求是 `cdhash H"..."`（二进制自身的哈希），
每次重新构建都是"另一个 app"，已授予的权限全部作废、逐个重弹；证书签名的要求是
`identifier "com.marmot.disk" and certificate root = H"..."`，与二进制内容无关，实测两次构建（中间改动源码）
完全相同，授权因此跨构建保留。真实 Developer ID、公证与 TCC 流程仍是发布前门禁（ADR-0006）。

开发模式可以使用 Vite 开发服务器，但开发服务器不是生产架构。未来若需要 CLI 或无界面服务，必须新增独立 Transport ADR，不能复用桌面应用的隐式权限边界。

Wails 暴露的方法必须：

- 只提供明确的领域用例；
- 使用强类型参数和返回值；
- 在 Go 边界校验路径、快照 ID、计划版本和能力；
- 不暴露任意命令执行、任意路径删除或任意文件读取方法；
- 只推送必要的扫描进度和结果事件。

## 5. 扫描架构

> **性能门槛口径（`ADR-0047` 取代）**：本文档 5.4 与第 11 节中出现的产品总 `15s` 门槛已失效。它没有
> 实测出处（最早只见于 R-020/R-024 的推定表述），且被当作 durable 完整终态的目标，而其对标的原版数字
> 是**可见终态**。同机同卷原版三次实测为 `18s` / `16s` / `15s`，中位数 `16s`。现行门槛为：
> **用户可见终态三次中位数 `<=16s`**（stretch `<=15s`），且首屏五环图零等待、投影完整覆盖 `100%`
> 目录的全部子项（ADR-0049 把原 `>=90%` 收紧为全保留）。durable 发布完成单独测量，不作为可见终态门槛。
> 相对成本判定使用 `user` / `sys` CPU 时间；绝对 wall 门槛必须在无显著 CPU/I-O 竞争的静默环境下测量，
> 否则记为「未验证」，不得用噪声数据宣称达成或未达成（实测本机实时 `/` 的扫描 wall 在 `14`–`25s` 间
> 随机波动）。以下各切片记录中的 `15s` 判定是历史日志，保留原状，不再作为依据。

```text
ScanCoordinator
  -> 卷/权限探测
  -> 顶层目录快速发布
  -> 受限并发的目录遍历
  -> 元数据、卷身份和大小计算
  -> 硬链接/完整克隆去重
  -> APFS clone metadata 和快照对象识别
  -> 原生扫描批次与 SnapshotWriter 重叠扫描/追加
  -> 提交目录索引、稳定子项顺序和摘要
  -> Wails 事件推送进度
```

必须采用成熟磁盘分析工具已经验证的策略：

- 先展示可用的顶层结果，再逐步补齐细节；
- worker 数量、目录遍历、外部命令和事件队列都必须有上限；
- 不能为每个待扫描目录无限创建 goroutine；
- 前端按目录懒加载、分页和排序查询，不能一次绑定百万节点；
- 结果分批提交，事件只推送汇总和进度，不推送每个文件；
- 缓存必须有 TTL、版本号、容量上限和失效策略；
- 扫描取消必须有明确的发布边界，不能在取消后继续发布新快照；
- 硬链接按卷内身份去重；完整克隆通过公开 `getattrlist` metadata 处理，部分共享块标记为未知或估算。
- 不跟随符号链接；挂载卷、系统快照、FileProvider 占位项和权限错误使用独立状态。
- macOS 使用 `getfsstat(MNT_NOWAIT)` 获取挂载表；扫描根范围内的嵌套挂载点不递归，Data 不因
  `/System/Volumes/Data` 被追加而与 firmlink 重复。
- 卷目录的 `volume_used`、`container_used` 与空间图的 `owned_allocated` 使用不同字段和
  `usage_basis`；任何一个数字都不能作为另外两个数字的替代。
- 扫描是最终一致快照，变化、取消和权限问题必须保留。
- Darwin 目录读取优先使用 `getattrlistbulk(2)` 单目录批量循环；当前根范围的嵌套挂载边界预先规范化，
  遍历使用有界前缀判断。目录任务可携带 `openat` 子目录 fd，fd 保留槽位固定为 2048，槽位耗尽、
  打开失败或非 Darwin 平台回退到路径读取；任务取消和队列投递失败必须回收未转移 fd。目录队列固定为
  4096；队列满时 worker 转入本地溢出处理，不允许所有 worker 阻塞在生产入队路径。

### 5.1 分阶段、设备感知和缓存契约

扫描任务必须按以下阶段发布状态和结果：

```text
Catalog -> VolumeOverview -> TopLevelPublish -> DeepScan -> Finalize
```

- `Catalog`：列出挂载卷、卷身份、容量、类型、权限和可扫描状态，不遍历文件树；容量优先使用
  固定只读 `diskutil info -plist` 的结构化输出，失败时标记 `statfs` 降级来源；
- `VolumeOverview`：读取卷根和直接子项元数据，尽快形成首层快照；
- `TopLevelPublish`：首层落盘后允许后端查询、取消收尾和恢复保留部分结果；扫描任务仍为 `running` 时
  Presentation 保持磁盘选择页，不因首层可查询而切换窗口；终态且最终 `MapResult` 查询成功后才进入
  空间图和目录列表；
- `DeepScan`：按目录任务递归遍历，跳过当前范围内的嵌套挂载点，受设备和全局并发预算控制；
- `Finalize`：完成目录汇总、问题汇总、缓存复核和快照终态。

`VolumeCatalog` 必须为每个 `ScanScope` 返回卷身份和 `DeviceProfile`：`ssd`、`rotational`、
`network_or_virtual` 或 `unknown`。第一版预算固定为：全局目录 worker 8 个；单 SSD 卷 8 个；
同一机械设备 1 个；网络/虚拟卷 2 个；未知设备 2 个；目录任务队列 4096；待提交批次 2。
这里的“待提交批次 2”同时表示 Application 持久化事件队列最多保留两个待消费批次；
`scanBatchSize` 只表示 writer 在 Application 内聚合后提交给 SnapshotStore 的节点批次阈值，
不得用于推导事件 channel 容量。各项预算不作为用户配置，队列满时生产端必须受控背压。单 SSD 从 4 调整为 8 的依据和边界由
[R-022](research/R-022-macOS_SSD并发预算复测.md) 与 [ADR-0024](adr/0024-macOS_SSD并发预算复测.md) 锁定。

### 5.2 技术卷到产品存储源

`VolumeCatalog.ListVolumes()` 返回技术卷事实，至少包含：

```text
id / name / path / kind / role
container_id / volume_group_id
volume_total / volume_used / volume_free
container_total / container_used / container_free / usage_basis / permission / scannable
```

Application 的 `GetStorageSources()` 生成启动页产品读模型：

```text
StorageSourceOverview
  id / name / path / kind
  total_bytes / used_bytes / free_bytes
  usage_basis / permission / message / scannable
  members[]

StorageVolumeMember
  id / name / path / role
  volume_total_bytes / volume_used_bytes / volume_free_bytes
  usage_basis / permission / scannable
```

映射规则固定如下：

1. 只按非空且相同的 `volume_group_id` 分组；没有卷组身份的卷按挂载卷独立生成入口。
2. 包含 `/` 的卷组使用 `/` 作为入口路径和默认扫描根；Data 只出现在 `members[]`，不作为主入口。
3. `system_auxiliary` 不生成主入口；外部卷不因共享 `container_id`、名称或路径相似被合并。
4. 卷组入口容量选择 APFS container 的 `container_total/container_used/free`；成员的
   `volume_used` 只用于明细，禁止相加得到入口占用。容器数据缺失时必须保留明确的降级 `usage_basis`。
5. StorageSource 只是产品投影，不改变 Scanner 的 `ScanScope`、挂载跳过规则、节点 `volume_id`
   或显式 Data 扫描能力。

该契约由 [R-018](research/R-018-APFS卷组与产品存储源映射.md) 和
[ADR-0020](adr/0020-APFS卷组与产品存储源映射.md) 锁定。前端和 Wails 只消费
`GetStorageSources()`，不得直接把 `ListVolumes()` 的技术卷列表渲染为磁盘入口。

缓存不再存在：扫描结果只存在于内存，没有快照目录、manifest、TTL、条数上限或字节预算
（ADR-0055；此前是"24 小时 TTL、最多 3 个快照或 512 MiB"）。启动时也没有需要恢复或标记的遗留状态。
唯一保留的缓存维护动作是删除已被取代的 SQLite 残留（ADR-0054）。

扫描进度每个任务最多 5 Hz，使用容量为 1 的最新值槽位；事件只发送阶段、汇总、问题数量、快照
版本和受影响父节点 ID，不发送单文件事件。扫描期间 Presentation 保持源页，不因进度事件请求或展示
空间图；终态事件再请求首屏深度 3 投影。全盘总量未知时前端只能显示不确定进度，不能用容器容量伪造
百分比，也不设 `aria-valuenow`。取消观察点之后不得提交新的快照批次，已提交批次保留为部分快照。

**扫描中源页的可见元素按 ADR-0050 与原版一致**：卷图标、卷名和副标题（与空闲态完全相同）、条纹不确定
进度条、条下一句 `扫描中…`、行内单按钮 `取消`，底部 `扫描文件夹…` 保持可见但禁用。**不显示**阶段名、
已处理节点/文件数、文件树占用、已用时，也不出现第二个进度条或第二个取消入口——R-051 逐像素复核确认
这四项在原版上不存在。这些数字仍由 `ScanProgress` 事件照旧携带，并通过进度条的 `aria-valuetext` 暴露，
只是不占版面。源窗口高度 `151`（R-014 实测，多卷时按行数增高），扫描前后骨架与高度都不变。计量条轨道
右对齐、宽约窗口 `24%`、高 `4px`；条下文字空闲态（剩余空间）右对齐、扫描态左对齐。

**遗留 SQLite 缓存的一次性清理**（ADR-0054）：缓存维护阶段先执行 `removeLegacyCache`，按**写死的三个文件名**
（`snapshots.db` / `-wal` / `-shm`）删除应用自己缓存目录下已被 ADR-0028 取代的 SQLite 存储。约束：
禁止通配符/递归；目录由 `main.go` 从 `os.UserCacheDir()` 取得后**显式传入**（不从快照目录往上爬）；
只删普通文件（同名的目录或符号链接一律跳过，不跟随）；文件不存在是正常情况；失败只记日志不影响启动与扫描；
删除了什么、释放多少字节必须记入关键日志。它不是通用缓存清理器——新增清理目标必须新增 ADR。
若将来把 SQLite 存储恢复为生产回退路径，**必须在同一次改动里移除该清理逻辑**。

**轮盘配色与角度基准按 R-055 的实测取样**：HSL 饱和度基本恒定 `91%`–`96%`、亮度随深度 `67%`→`79%`
（原版 HSB 亮度恒定 `97%`+，此前我们低了约 20 个饱和度点、9 个亮度点，观感就是"不够亮"）。
**圆盘在根层代表卷的总容量**：扫过 `已用/(已用+可用) × 2π`，**终点固定在 3 点钟**，可用空间就是那道
空缺口，不画出来；非根层满圈，同样在 3 点钟收尾。**小项两级降级**：按环半径把 `2.5px` 最小可读宽度换算成
角度，低于它的兄弟折叠成一个中性灰块（`#888787`，实测值），连灰块都不够宽则整组不画、背景透出——
无条件绘制每个投影后代会在外圈堆出 1px 发丝。被折叠或不画的项不再递归其子树。

一处已知差异（R-055 第 5 节）：原版的色相**按子项序号**从色相环取色、整棵子树保持该色相；我们的色相
**由角度位置决定**，因此最大楔形会吃掉色相环的一大段、相邻小楔形颜色相近。改动需要一并重做跨层级色相
继承机制，本轮未做。

**层级切换用 JS 插值形变**（不是 CSS 动画：CSS `transform: scale()` 会覆盖 `transform="translate(...)"`
属性把轮盘挪到原点，此前试过一次已废弃）。每条弧存的是可插值的几何量 `{a0,a1,r0,r1}` 而不是 `d` 字符串；
匹配用**节点身份**（`"node:"+id`，当前层 `entryKey` 与投影层 `projectedArc` 的 key 天然一致），所以一条弧
在下钻前后即使换了环也能连续移动，观感是缩放而不是切换。三条实现约束：

- **直写 DOM**：一层可有 `~1600` 条弧，每帧让 React 重新协调这么多元素撑不住 60fps，因此帧由
  `requestAnimationFrame` 直接 `setAttribute("d", …)`，React 只渲染目标态；
- **必须用 `useLayoutEffect`**：React 提交的是目标几何，源几何要在浏览器绘制前写回去，否则第一帧闪目标；
  另有一个**无依赖**的 layout effect，因为形变途中的任何重渲染（点击后指针仍在轮盘上，新弧挂载立刻触发
  `pointerEnter` → hover 状态变化）都会把 `d` 重置回目标，必须重新施加当前帧；
- **离场弧要扫出去**：只动留存的弧会让不属于目标子树的楔形第一帧消失、轮盘出现空洞。离场弧作为 ghost
  层继续绘制，按**全局角度变换**送走——下钻时把被点楔形的角跨映射到整圆，同一映射施加到其余部分就会把它们
  推过 `0`/`2π` 扫出圆外；回退是其逆变换，整层折回它来自的楔形。ghost 层不可交互、同步淡出。

弧数超过 `2600` 时跳过形变直接切换：每帧代价与弧数成正比，宁可不动也不要卡顿。

**扫描中的进度条是确定进度**（ADR-0053）：填充宽度 = `(卷组辅助卷 statfs 已用之和 + 已遍历字节) / 卷已用`，
分母取快照在 `CreateSnapshot` 时记录的值。辅助卷必须**前置计入**（扫描前已从 `statfs` 得知，且必然进入
最终树），否则末尾会有 `12` 个百分点的跳变；前置计数不得在枚举结束挂上辅助卷时重复计入。填充是**行进中的蓝白斜条纹**
（条纹行进＝正在扫描，宽度＝真实进度，与原版一致）。斜向 `repeating-linear-gradient` **必须**配固定的
`background-size`（等于纹样的水平周期，`105deg` 下为 `6.21px`）：否则渐变按元素盒铺开，相位随盒宽变化，
宽度每变一次（5 Hz）纹样就横向平移，条纹会明显蠕动。行进由 `background-position` 动画提供，不是靠倾斜或
盒宽变化。无分母退回不确定条时改为扫掠动画。封顶为 `已统计/已用`（本机 `95.9%`），缺口是 `hidden_space`，
**只有终态才补满**。分母不可用时退回不确定条，且不设 `aria-valuenow`；`aria-valuetext` 必须写明口径是
"已统计占卷已用"而不是"完成度"。`catalog` 阶段没有遍历字节，条停在前置计数对应的位置，不得自行爬升。

**显示单位是十进制**（除数 `1000`，一位小数，`.0` 结尾省略），与 Apple 和原版一致：`245.1 GB` 就是
`228.3 GiB`。以 `1024` 为除数却标 `GB` 曾让所有数字统一读低 `7.4%`（ADR-0052 §1）。文档与预研记录中的
测量值继续用 `GiB` 并显式标注，两套单位不混用。

**空间图根层必须加得出磁盘。** 快照在 `CreateSnapshot` 之后立即记录卷的容量/已用/可用（manifest 的
`volume_*_bytes`，与源页取自同一时刻），根层由 `buildMap` 追加虚拟项 `hidden_space = 卷已用 - 树总量`
（仅当为正），使 `Σ子项 = 卷已用`。旧快照没有该字段时省略平衡项与两行摘要，不得猜测。同一 APFS 卷组的
辅助卷由 Application 在枚举结束后挂上：每个卷一个 `kind = "volume"` 的不可下钻节点，大小取其 `statfs`
已用字节，并折进各级祖先的 roll-up；`/System/Volumes/Data` 不挂（firmlink 已覆盖）。不按 `allocated`
遍历它们——Preboot 的 cryptexes 是 APFS 克隆，遍历得 `27.60 GiB` 而物理只有 `8.36 GiB`。具体见
[ADR-0052](adr/0052-空间数值口径与卷组覆盖对齐.md)。

**卷行的操作菜单是原生上下文菜单**（ADR-0051）。DOM 菜单画不出 `151px` 高的窗口，而原版的菜单浮在窗口
之外，因此菜单在 Go 侧构建（`app.ContextMenu`），前端在 chevron 上派发合成 `contextmenu` 事件触发，坐标取
分段按钮的左下角。菜单只包含已具备能力的项：`重扫描`、`放弃扫描结果`（仅当该卷有可查看结果）、
`在 Finder 中显示`；原版的"以管理员身份快速重扫描"与"显示简介"当前无能力，不做。菜单项只发
`volume-menu` 事件（卷身份 + 动作），动作仍由前端调用既有服务方法执行，Go 侧不复制业务判断。
`RevealStorageSource(sourceID)` 按存储源身份解析路径后调用 `PreviewPort`，**不接受前端裸路径**
（DDD 不变量 17）；它与 `RevealNode(snapshotID, nodeID)` 并存，一个是存储源、一个是快照节点。

该窗口状态边界由 [ADR-0027](adr/0027-DaisyDisk扫描中窗口状态边界.md) 补充锁定（其第 2 条已由
[ADR-0050](adr/0050-扫描中源页反馈与原版一致.md) 取代）；缓存维护、事件限频和取消语义
仍由 [ADR-0014](adr/0014-分阶段扫描与设备感知并发.md) 和 [ADR-0023](adr/0023-快照缓存生命周期与扫描中进度反馈.md)
承载。

### 5.3 macOS 批量元数据读取

macOS 本地文件系统的目录项读取优先使用公开 `getattrlistbulk(2)`，批量返回名称、文件身份、类型、
修改时间、逻辑长度、实际占用、硬链接计数和目录挂载状态；不得在 Darwin 主路径对每个目录项单独
调用 `DirEntry.Info()`。变长属性记录必须按长度和返回属性位图校验。

批量 API 不支持或读取失败时，可以逐目录回退到 `ReadDir + Info`，但必须保留同样的权限错误、部分结果、
取消和大小可信度语义。`searchfs` 不作为 APFS 首版主路径。批量读取只属于 Infrastructure，不改变
Domain 节点或 SnapshotStore 契约。具体实现和测量门禁由 [R-019](research/R-019-macOS_getattrlistbulk批量元数据扫描预研.md)
与 [ADR-0021](adr/0021-macOS_getattrlistbulk批量元数据扫描.md) 锁定。

### 5.4 Darwin 原生扫描主循环

Darwin 扫描主循环由 Infrastructure 原生适配器负责目录队列、`getattrlistbulk`、`openat`、挂载边界、
设备画像并发、硬链接/clone 身份和局部汇总。原生侧以紧凑批次跨 Go/C 边界调用 `BatchEmitter`，
Application 以 `[]scan.Node` 传递持久化事件；Go 不对每个目录或每个节点单独进入持久化 channel。
批次上限由原生适配器和 SnapshotStore 共同约束，不能超过 32,768 条记录或 4 MiB。取消、错误和阶段
发布可以提前结束当前批次，但必须提交完整 footer 后才允许查询。

**`BatchEmitter` 的所有权语义（ADR-0057 §1）**：批次切片及其中的字符串**只在回调返回前有效**。
扫描器跨批次复用底层数组，消费方要留下任何东西必须在返回前复制。这与更早的"转移所有权"相反，
改动原因是批次极小——`421,701` 个批次平均 `6.6` 个节点、`45.7%` 只有 1 个节点（R-058 §4.1），
为每个批次分配一个消费方可以持有的数组，本身就是扫描期最大的一笔分配。两条必须一起遵守：

- 回调**并发调用**（实测并发 2），所以扫描器的复用缓冲按 worker 分配，消费方的记账也必须加锁；
- Application 的 `enqueueBatch` 把批次复制进一组**预先分配、循环回收**的缓冲后才送进持久化 channel；
  自由缓冲耗尽时**等待**而不是新建——新建等于每批次分配，正是这条契约要消除的成本。

**文件节点不携带路径（ADR-0057 §2）**：`scan.Node.Path` 只对目录有值。遍历要用目录路径去
`opendir`，而文件路径建完即被存储丢弃——存储的内存布局不存路径，它按需从父链重建。
这同时让 DDD 不变量 17 变成结构性的。

Application 的持久化事件 channel 使用固定 `scanPersistQueueCapacity = 2`，只计待消费事件批次数；
队列满时发送端在 context 可取消的路径上等待 writer 消费，不能丢弃节点、问题或绕过 SnapshotStore。
writer 出错或退出时必须把错误传回扫描流程。该内存上界和批次职责由 [R-028](research/R-028-应用持久化队列上限与终态内存预研.md)
与 [ADR-0030](adr/0030-应用持久化队列上限与终态内存.md) 锁定。

原生目录队列使用固定 4096 槽位。入队非阻塞失败时，当前 worker 处理本地溢出目录；只有真正入队的
任务计入全局 `pending`，本地任务完成不得重复扣减计数。该约束避免高扇出目录下所有 worker 同时
等待 `not_full` 而无法消费队列，取消时仍必须释放队列内和本地溢出任务持有的 fd 与路径。

每条记录只携带父 ID、名称切片、类型/标志、三种大小、volume、device/inode、修改时间和可信度；
不保存每节点完整路径。目录索引保存直接子项范围、汇总和按 `owned_allocated DESC, node_id` 的
稳定顺序。旧的 Go 目录任务和 SQLite writer 只作为过渡实现，不能继续扩展。格式、恢复、POC 和
性能门槛由 [R-026](research/R-026-macOS原生扫描主循环与非SQLite快照预研.md) 和
[ADR-0028](adr/0028-macOS原生扫描与追加式二进制快照.md) 锁定。

当前性能切片由 [R-027](research/R-027-macOS原生扫描资源复用与完整终态性能预研.md) 和
[ADR-0029](adr/0029-macOS原生扫描资源复用与性能切片.md) 锁定：Darwin worker 在生命周期内复用
`getattrlistbulk` 读取缓冲和节点批缓冲；有效 bulk 页的节点 ID 一次性分配，不能每节点获取 ID 锁。
children 缓冲由 worker 复用；队列满载时的本地递归任务或超过复用容量的 bulk 页必须临时使用独立数组，
以保证本地递归不覆盖父任务待调度内容。该优化不得
改变节点唯一性、父子 ID 顺序、挂载边界、取消/错误收尾、快照格式、footer/校验、设备并发预算或 Wails 契约。

Application 事件队列的容量和 writer 聚合批次由 [R-028](research/R-028-应用持久化队列上限与终态内存预研.md)
和 [ADR-0030](adr/0030-应用持久化队列上限与终态内存.md) 补充锁定：队列容量固定为 `2`，
不得由 `scanBatchSize` 推导；满载时必须受控背压。

> **以下整段关于 durable 管线的约束已随 [ADR-0055](adr/0055-取消快照持久化与内存唯一事实源.md) 作废。**
> `FinishSnapshot`、索引构建、外部排序、relation 归一化、manifest 的 `Sync`/checksum/原子发布、临时文件
> 清理、缓存剪除与字节预算——这些代码路径不再存在，扫描结果只存在于内存。对应的 ADR-0028、0031、0033、
> 0035、0036、0038、0039、0042、0043、0044、0046 与 ADR-0023 的分析与测量仍是有效的历史记录，
> 但不再是任何实现的依据；不要按它们改代码，也不要以为它们描述的东西还在。
>
> 内存事实源的实际约束见本节前面的"扫描结果只存在于内存"与"内存布局"。

## 6. 空间数据模型

节点不能只有一个 `size` 字段，至少需要：

```text
logical_size       文件逻辑长度
allocated_size     卷上实际占用估计
owned_allocated    去重后的归属占用
volume_id          节点所在挂载卷身份
size_confidence    精确 / 估算 / 部分 / 未知
size_basis         计算口径和版本
```

Treemap/Sunburst 默认使用 `owned_allocated`；不可得时必须降级并在界面标明口径。

## 7. 快照和查询

`SnapshotStore` 必须支持：

- 按快照查询根节点和子节点；
- 按父节点分页、排序和过滤；
- 保存部分结果和错误；
- 保存扫描口径、版本和权限状态；
- 保存节点 `volume_id`、卷自身占用和容器占用的来源口径；
- 增量写入和取消后的安全收尾；
- schema 版本和过期策略。

每个新扫描快照必须持久化对应的 `taskId`。应用启动先将遗留的 `running` 快照标记为
`interrupted`；内存中找不到任务时，`GetScanStatus(taskId)` 通过 `SnapshotStore` 按任务 ID
查询已提交的部分结果。该查询不提供续扫能力。没有任务 ID 的旧快照不能伪造任务状态；清理计划
仍不跨进程持久化。节点批次提交时必须同步累计节点、文件、目录和 `owned_allocated` 摘要；标记中断时
直接保留已提交摘要，不得为恢复重新扫描整棵节点表，也不得阻塞窗口显示和新扫描请求。该技术承载由
[ADR-0012](adr/0012-扫描任务身份与中断查询.md) 与
[ADR-0019](adr/0019-macOS_APFS卷组与全盘容量语义.md) 锁定。

首个切片的进程重启语义是：不续扫；上次仍为运行中的任务恢复为 `interrupted`，已提交
快照保留为部分结果。清理计划暂为会话内对象，跨进程持久化不在本阶段。

快照使用追加式二进制事实段、名称字节区、目录索引、稳定子项索引、问题日志和提交 footer。查询通过
`pread/mmap` 从目录索引定位子项范围，再按 `owned_allocated DESC, node_id` 读取当前层；单次最多返回
1000 节点，Map 投影继续遵守 256 KB Wails 载荷和 ADR-0017 预算。`Publish`/`Finish` 在发布 manifest
前校验完整数据帧、索引和计数；数据帧只在首层/终态发布屏障前同步 dirty 内容，不要求每个批次同步；
应用查询已提交快照使用 `OpenCommitted`，不在首次查询时重复扫描整个
数据文件。`TopLevelPublish` 使用首层 footer barrier，首层提交后允许后端查询和恢复保留；前端仍须
等待扫描终态和最终 Map 查询成功后才展示结果。
取消时只收尾已经提交的批次，崩溃时忽略不完整尾部。格式和验收由
[ADR-0028](adr/0028-macOS原生扫描与追加式二进制快照.md) 锁定；R-004/R-024 的 SQLite 数据只作为
历史对照，不是新实现门槛。

**扫描结果只存在于内存（ADR-0055）。** 没有快照文件、没有 manifest、没有索引、没有缓存预算、没有剪除。
枚举结束时结果就地变为可查询，扫描即结束——此前"可见终态"与"durable 发布完成"两个事实塌缩为一个。
启动时内存里没有结果，源页只提供"扫描"；**不得**从任何磁盘残留恢复，也不得暗示结果可恢复。
代价如实呈现：崩溃即全失。

**内存布局（ADR-0056，第 1 条由 ADR-0057 §3 替代）。** `internal/infrastructure/snapshot/memtree`：

- **节点表**：定长 `64` 字节/节点，`records.at(id)` 即节点——**nodeID 就是下标**，不单独存储；
  插入时强制"编号连续无空洞"，留下的零记录被 `valid()` 视为不存在。`TestRecordStaysPacked` 用
  `unsafe.Sizeof` 钉住这 64 字节：它就是内存预算。
- **节点表与名字 arena 都是分页的**（页 `256 KiB` / `1 MiB`，`TestRecordPageIsBudgeted` 钉住）。
  扩容只追加一页，**永不复制已写入的部分**，所以结束时的余量上限是一页，不需要再做一次全量裁剪。
  这替代了 ADR-0056 实施时的"翻倍增长 ＋ `finish()` 裁剪"：那套花 `737.54 MB` 分配量换 `110 MiB`
  留存下降，而 Darwin 上分配高水位事后不可回收（R-057 §4c），方向是反的。
  名字**不跨页**，所以 `uint32` 偏移仍能按固定步长解出（页号，页内偏移）。
- **不存路径**。路径均长 `122` 字节、全盘 `325 MiB`，且绝大部分是重复前缀；改为遍历父链重建，只有当前层
  需要、且一次只有一页。按路径反查（清理计划用）也是从根往下走，不建路径索引。
- **名字 arena**：节点表存 `uint32` 偏移 ＋ `uint16` 长度，两个上界都显式校验
  （实测总量 `55.7 MiB`、最长 `178`，离上限很远，但静默回绕会毁掉之后每一个名字）。
- **1 字节码**：`kind`/`volumeID`/`sizeBasis`/`confidence` 的真实基数是 `3/1/2/1`。码表**有上界**——
  无上界的 intern 表是穿着优化外衣的内存泄漏，基数爆炸时必须报错而不是静默吃堆。
- **子项按计数排序分组**（一遍计数、前缀和、一遍落位、各父区间就地排序），不在扫描期维护按父分组的副本——
  那份副本正是 2× 峰值的来源。分组**按需重建**：插入与改写 roll-up 使其失效，查询时重建一次
  （`O(n)`，实测 `0.04s`），这样 `TopLevelPublish` 之后的后端可查询边界（ADR-0014/0027）仍然成立。
- **roll-up 直接写回记录**，不另建目录尺寸表；`allocatedSize` 与 `ownedAllocated` 合成一个字段
  （二者恒等，R-053），引入 `privatesize`（ADR-0052 §2）需要**新增**字段而不是复用它。

**扫描器侧的对应约束（ADR-0057 §3）**：按节点 ID 索引的结构一律是稠密数组而不是 map。
节点 ID 由单一计数器分配（根为 `1`），所以 `dirOrdinal[nodeID]` 直接给出目录序号，
目录状态与目录尺寸两张表按序号分页存放。目录尺寸滚动到父目录时**按节点 ID 倒序遍历**即可——
子节点的编号一定大于父节点——不需要排序，也不需要为排序建一个 ID 切片。

实测（真实 `/`，`2,76x,xxx` 节点）：

| 指标 | ADR-0056 实施后 | ADR-0057 实施后 |
| :-- | --: | --: |
| 扫描期总分配量 | `2530 MB` | **`650.1 MiB`** |
| 峰值 `phys_footprint` | `1126.4 MiB` | **`487.8 MiB`**（含 ADR-0058） |
| 峰值 `ps rss` | `1058.9 MiB` | `532.4 MiB` |
| 留存堆 | `257.8 MiB` | `254.7 MiB` |
| 可见终态 | `15.2s` | **`14.259s`** |
| 目录覆盖率 | `100%` | `100%` |

**扫描期内存上限（ADR-0058 §1）**：扫描运行期间，`internal/application` 每 `100ms` 读一次
`/gc/heap/live:bytes`，设 `debug.SetMemoryLimit(live + 100 MiB)`，并在**扫描结束、取消、出错**
三条路径上还原为 `math.MaxInt64`。还原走 `defer` 且按持有计数——并发扫描时一个结束不得解除另一个的上限。

- **必须读 `/gc/heap/live:bytes`，不得读 `/memory/classes/heap/objects:bytes`**。后者含未清扫的死对象，
  据它推出的上限永远落在堆的当前位置之上，**等于没设，而且是静默失效**（R-059 §3）。
- **不采用固定 `GOMEMLIMIT`**：活跃集约 `92` 字节/节点，任何在本机调好的常量都会在超过约 `4.3M` 节点
  的卷上落到活跃集之下，退化成纯粹的性能损失（R-059 §4.3）。
- 这条策略把峰值与稳态的关系从**比例**（`GOGC=100` 给的 `2×`）变成**加法常数**，这正是门禁能跨卷规模
  生效的前提。

**报告内存必须写明口径。** `ps rss`、`phys_footprint`、`/gc/heap/live:bytes` 在本场景下分别是
`532.4 / 487.8 / 254.7 MiB`，不写口径的内存数字没有意义（R-057 §4c.5）。

**内存门禁（ADR-0058 §2，替代 ADR-0055 门禁 4 与 ADR-0056 门禁 2 的峰值条款）**：
稳态 `<=280 MiB`、峰值 `phys_footprint` `<=700 MiB`、**峰值 `-` 稳态 `<=350 MiB`**。
最后一条是加法而不是比例：比例门禁在更大的卷上会自动放宽（`10M` 节点的卷，`2×` 就是 `1.8 GB`），
加法门禁不会，而且它能验证自适应上限有没有被误删。

### 7.1b 空间图配色与环几何（ADR-0059）

配色**不是公式，是一张取样表**。原版的色相环是手工调制的渐变：同一深度下 HSV 饱和度随色相变化
`43%`–`62%`（跨度 17 点，而相邻深度之间只差 `2.6` 点），V 也随色相变化（多数 `97%`–`100%`，
`290°`–`350°` 只有 `92%`–`93%`）。任何单参数曲线都会在这个跨度上系统性偏离，所以：

- `frontend/src/sunburst.ts` 持有 **36 个 10° 桶的 `(HSV 饱和度, 明度)`**，锚定在**绝对深度 4**，
  外加按深度的收敛偏移 `[18.7, 8.7, 2.2, 0, -1.1, -1.8, -1.9]` 加在饱和度上。V **不加深度偏移**；
- 34 个桶为实测，`300°`/`310°` 为插值（该区间只有被排除取样的隐藏空间楔形），代码里逐格标注；
- **这些是取样结果，不是可调参数。** 修改必须附新的取样证据。取样方法见 R-060 第 2 节：
  `CGWindowListCopyWindowInfo` 读窗口逻辑尺寸 ＋ `screencapture -l<id>` 截原生窗口图。

**取色的深度参数是节点在树里的绝对深度，不是它画在第几环。** 原版里一个文件夹在任何视图下都是同一个
颜色（`private` 视图的第一环等于根视图 depth 2，`用户/deemo` 视图第一环等于根视图 depth 3，
十个色相桶吻合到 `0.2` 点以内）。前端用 `baseDepth = pageIndex` 换算，`sliceColor(hue, baseDepth + 环号 + 1)`。

环几何全部以主环宽 `w` 为基准（六个导航层级的视图上 `hub`/`ring` 完全一致）：

| 量 | 值 |
| :-- | --: |
| 孔半径 | `1.38 w` |
| L1–L5 环宽 | `w`，五层等宽 |
| 主环径向间隙 | `1 pt`（`w` 的 `1/33.5`） |
| L6–L12 环宽 | `0.147 w` |
| 细环间隙 | `0.46 ×` 细环宽 |
| 相邻扇区角向分隔 | `1.5 pt` 的恒定**弧长**（换算成角度随半径反比） |
| 圆心位置 | `(0.308 W, 0.512 H)` |
| 圆盘直径 | 可用高度的约 `75%` |

**孔是一个洞**：原版孔内部与页面背景逐字节相同，边缘无描边。实现用 `fill: transparent`
而不是 `fill: none`——后者不参与命中测试，点圆心返回上层的功能会失效。
孔内标签的字号与基线也是孔半径的比值（`0.484` / `-0.086` / `+0.538`）。

**层级上限 12，剔除下移到服务端。** `scan.MapQuery.MinSweeps` 带来每个投影层的最小可读角宽，
渲染侧用**同一份几何常量**算出它，存储侧在投影时直接剪掉。这是一处有意的前后端耦合：两份比值会漂移，
而漂移表现为缺失的弧。剔除必须在服务端，因为此前的实现把亚像素条目序列化、传输、解析之后才丢掉，
这正是四层深度就触发 ADR-0048 密度上限的原因。

`frontend/src/sunburst.ts` 是纯函数，由 `frontend/src/sunburst.test.ts` 钉住
（`npm test`，Node 内置 test runner，无新增依赖）：双色相锚点、色相依赖必须存在、深度阶梯收敛、
跨导航颜色稳定、环几何比值、剔除阈值随半径递减。

### 7.1c 右侧列表（ADR-0059 附带）

尺寸取自原版实测（R-060 第 3.7 节）：分栏 `62 : 38`；行字号 `17pt`、行间距 `22pt`、
子项色点 `7pt`、列间距 `11pt`；标题字号 `21pt`；面板右内边距 `12pt`。
**每一行的右边缘必须落在同一个 x**——原版所有行都对齐在一个像素列上。

两条实现约束：

- 计数格（"N 项"）**永远渲染**，空时留零宽占位。条件渲染会让尺寸落进倒数第二列，
  使没有计数的行短两个 gap；给空格子加 `display: none` 是同一个 bug 的另一种写法；
- 根**必须写 `-webkit-font-smoothing: antialiased`**。WebKit 在 macOS 默认亚像素抗锯齿，
  比相邻的原生文字明显更粗，实测同一字号的字形高比原版多 `2px`。

标题的色点：**扫描根不画**，下钻时画，直径为子项色点的两倍（`14pt`），颜色取该节点自身的颜色。

### 7.1 空间图查询契约

空间图和目录列表使用 `GetMap(MapQuery)`，不向前端传输完整树：

```text
MapQuery
  snapshotId
  parentId
  limit       默认 256，最大 1000
  offset      默认 0
  measure     owned_allocated（第一阶段固定）
  depth       前端默认 3，最大 4；0 表示兼容浅层模式，只查当前层
  projectionLimit  默认 2000，最大 2000（ADR-0048；旧值 384/512）
```

```text
MapResult
  snapshotId
  snapshotVersion
  parent
  entries[]
  total / limit / offset / hasMore
  remaining
  confidence
```

`entries[]` 按 `owned_allocated DESC, nodeId` 稳定排序，目标契约允许真实节点 `kind=node`、空间聚合项
`kind=aggregate` 和解释性虚拟项 `kind=virtual`。真实节点可以进入、预览、Finder 定位或进入清理
计划，但每个操作仍由 Application 按快照和节点 ID 重新校验。聚合项只允许展开，不允许预览、定位、
清理，也不写入 `scan_nodes`。虚拟项必须带 `virtualType`、可信度和能力集合；hidden/restricted、
purgeable、other volumes、snapshot 等对象不能伪装成真实节点。
`remaining`、聚合项和虚拟项必须保留三种大小及可信度口径。单次 Wails 返回不得超过 256 KB。

当 `depth > 0` 时，目录 `MapEntry` 可以带有有界 `children[]`、`childrenTotal` 和 `childrenHasMore`，
供 Sunburst 渲染真实的多层后代；投影共享同一 `snapshotVersion`、大小口径和能力集合，不创建新的
扫描事实。响应接近 256 KB 或预算耗尽时必须提前截断并标记 `densityTruncated`，不能无限递归或由
前端为每个扇区单独查询。`DensityTruncated`（ADR-0049 前名为 `projectionTruncated`）表示**绘制预算**
耗尽，不是存储知识的边界：被标记的条目仍然可以被直接查询到。Application 负责深度/节点预算钳制，Wails DTO 层在序列化前负责最终字节裁剪。
Wails 调用方必须显式发送 `depth` 才使用多层投影；省略整数零值按 `depth=0` 处理。

**投影后代使用精简条目形态**（`ProjectedEntry`，ADR-0048），与当前层 `entries` 的完整 `MapEntry` 不同。
它只携带绘制一条弧所需的字段——节点 ID、名称、种类（`directory`/`file`/`aggregate`）、
`owned_allocated`、`children`、`total`、`more`；**不携带** `Path`、`Device`、`Inode`、`ModifiedAt`、
`VolumeID`，逐条的 `Confidence`/`SizeBasis` 由所在层继承。JSON 键刻意取短
（`id`/`name`/`kind`/`size`/`children`/`total`/`more`）：目标密度下重复的字段名是载荷主要成本。

不携带路径有两个理由。一是成本：装配时每条弧原本要沿父链逐级读记录来重建路径，实测这是延迟主因
（`2021` 弧 p95 从 `346.9ms` 降到 `66.2ms`）。二是安全：投影后代因此**在结构上**无法充当文件操作授权，
把 DDD 不变量 17 从约定变成不可违反。对投影后代的任何操作必须先按节点 ID 回查完整节点；因此空间图中
当前层之外的弧不可点击、不可拖拽。

预算与门槛及其实测出处（真实 `/`、`snapshot-9`、depth 4、预热后 15 次样本，ADR-0048）：

| 项 | 值 | 实测 |
| --- | --- | --- |
| 密度目标 | `2000` 弧 | 实得 `2021` 弧 |
| 载荷上限 | `<=256 KB` | `235.2 KB` |
| 装配 p95 | `<=150ms` | `66.2ms` |
| 深度上限 | `4` | — |
| 当前层 `entries` 上限 | `256` | — |

投影内部的子项读取不受分页 `maxPageSize`（`1000`）约束，改由投影预算作为硬界；`Children` 与 `Map` 的
`limit` 仍受 `maxPageSize` 约束。具体契约由 [ADR-0017](adr/0017-有界多层空间图投影.md) 与
[ADR-0048](adr/0048-空间图投影条目精简与密度上限.md) 锁定；后者更新了前者的预算数值、载荷与 p95 表述，
并注明 `256 KB` 此前在默认预算下即被突破（实测 `274 KB`）。

目标 DTO 语义如下：

```text
MapEntry.kind          node | aggregate | virtual
MapEntry.virtualType   smaller_objects | hidden_space | purgeable_space | other_volumes |
                       snapshot | restricted（仅 aggregate/virtual）
MapEntry.displayState  current | stale | partial
MapEntry.capabilities  enter | preview | reveal | collect | rescan 的子集
```

Domain/Platform 负责提供虚拟类型、文件身份、权限和可信度事实；Application 根据快照状态和平台能力
计算 `displayState`/`capabilities`，Wails 只做 DTO 转换。前端可以隐藏不适用的按钮，但不能新增能力；
每次 Preview、Reveal 或 Cleanup 仍必须回到 Application 重新校验。

前端只保存当前层、面包屑和最近最多 32 个目录页的可丢弃 DTO 缓存。D3 只负责布局和交互；当前层
收到受影响父节点事件后以 250 ms 防抖重新查询，响应版本过期则丢弃旧页。该数据契约由
[ADR-0013](adr/0013-DaisyDisk空间图与渐进查询数据契约.md) 锁定。
多层 Sunburst 的有界真实后代投影由 [ADR-0017](adr/0017-有界多层空间图投影.md) 补充锁定。

## 8. 用例接口

Wails 对外暴露的接口先按行为定义：

| 用例 | 输入 | 输出 |
| --- | --- | --- |
| 查询产品存储源 | 无或权限范围 | StorageSource、System/Data 成员明细、容器入口容量、权限和可扫描状态 |
| 开始全盘扫描 | 扫描选项 | 扫描任务 ID |
| 查询扫描状态 | 任务 ID | 状态、进度、卷、权限和问题 |
| 查询子节点 | 快照 ID、父节点 ID、分页条件 | 节点、汇总和限制 |
| 查询空间图层 | `MapQuery` | `MapResult`，包含聚合项和快照版本 |
| 取消扫描 | 任务 ID | 最终状态 |
| 预览节点 | 快照 ID、节点 ID | Quick Look 调用结果 |
| Finder 定位节点 | 快照 ID、节点 ID | Finder 调用结果 |
| 创建清理计划 | 快照 ID、候选项、策略 | 计划 ID、原因和估算 |
| 校验清理计划 | 计划 ID、版本 | 总体和逐项结果 |
| 确认清理计划 | 精确版本 | 确认结果 |
| 执行清理计划 | 已确认计划 ID | 逐项执行结果 |

Wails 事件至少包括扫描进度、扫描问题、快照更新、清理进度和清理结果。扫描事件只携带摘要和受
影响父节点，不承担节点传输。客户端断线或窗口重开后，必须通过查询恢复状态。

Preview/Reveal 的 Wails 输入只能是 `snapshotId + nodeId`，不能接收任意路径、URL、命令或 Shell
参数。Application 通过快照取得并校验路径后，调用 macOS Platform 的 Quick Look 或 Finder 端口。

## 9. macOS 权限

- 全盘扫描必须在 Wails 应用身份下验证 Full Disk Access 流程。
- 卷目录必须明确区分 System、Data、嵌套挂载和 APFS 容器共享容量；不把 `statfs("/")` 作为文件树总量。
- 每个卷和目录必须记录可访问、部分可访问或不可访问状态。
- 权限不足不能被当作空目录。
- 第一阶段不支持 root/管理员扫描，不通过隐式 shell 提权。
- Developer ID 直装分发是当前方案；App Store 沙盒不进入第一阶段。
- 真实签名、Full Disk Access 和公证仍需在发布环境完成 smoke test；当前 SQLite 完整 smoke 约
  20.06 秒只是历史基线。最新二进制终态三次为 `20.47s`、`23.26s`、`20.87s`，中位数约
  `20.87s`，不能把纯扫描器中位数约 `13.99s` 当作新格式完整产品终态门槛已达成。

### 9.1 预览、Finder 定位和收集区

Platform 提供：

```text
PreviewPort.Preview(path, ownerWindow)
PreviewPort.Reveal(path)
```

macOS 预览使用 `QuickLookUI` 的 `QLPreviewPanel`/`QLPreviewItem`，Finder 定位使用
`NSWorkspace.activateFileViewerSelectingURLs`，不调用 `qlmanage`、`open`、`osascript` 或任意
Shell。原生 bridge 位于 Platform 层并遵守 AppKit 主线程和 Wails 窗口生命周期。

Collector 只是前端会话内的选择视图，最终必须映射为可审查的 `CleanupPlan`；加入 Collector 不
触发文件操作。聚合项、卷根、扫描根、特殊文件和权限不明对象不能加入计划。该边界由
[ADR-0015](adr/0015-macOS预览Finder定位与收集区平台边界.md) 锁定。

## 10. 清理安全

- 清理计划与扫描快照分离。
- 执行前重新检查路径、卷身份、节点类型、预期元数据和平台能力。
- 默认调用 macOS Foundation Trash 能力，不做永久删除。
- 清理项基于卷、device、inode、类型、大小和修改时间执行前重新校验。
- 父子清理项默认拒绝重叠计划，不能把扩大删除范围的选择静默替用户完成。
- 每个清理项独立返回成功、跳过或失败。
- 不承诺跨多个文件的原子回滚；恢复边界必须在 UI 和 SDD 中明确。

## 11. 第一条垂直切片

```text
Wails 启动
  -> 获取权限状态
  -> 扫描本机测试卷/目录
  -> Catalog/VolumeOverview/TopLevelPublish：源页显示首层扫描进度，后端开始保留部分结果
  -> DeepScan：后台受限并发补齐结果，窗口仍保持源页
  -> Finalize：终态后查询最终首屏 Map，切换结果工作区
  -> 按需展开子目录
  -> Quick Look/Finder 预览和定位
  -> Collector 形成清理候选
  -> 创建并校验清理计划
  -> 用户确认
  -> 移入废纸篓
  -> 展示逐项结果
```

R-004、R-005、R-007、R-008 和 R-017 已完成本机验证，R-006 已完成 ad-hoc 打包验证；在真实签名/TCC
smoke test、跨卷废纸篓验证和真实只读全盘样本完成前，不宣称达到发布级全盘目标。

本轮 P0 已完成：固定 Go/Wails 构建环境和 bundle identity、旧版 SnapshotStore schema migration、
分阶段扫描与首层发布、Map 查询和聚合、macOS 卷/权限/Trash/Quick Look/Finder 适配、
交互状态模型，以及取消、部分结果、重启恢复和清理计划版本的自动化验证。旧版 SQLite 快照实现仅
作为过渡基线；产品总 `15s` 门槛已由 [ADR-0047](adr/0047-用户可见终态解耦与内存目录树查询权威.md)
取代为「可见终态三次中位数 `<=16s`」（原版实测中位数），下文历史记录中的 `15s` 判定保留原状；
依据本机原版实测的
紧凑卷入口、当前目录列表、底部 Collector 和启动态/结果态窗口切换已按
[ADR-0018](adr/0018-DaisyDisk视觉版式与窗口状态重做.md) 完成第一版实现；真实原生窗口 smoke test
完成前，不得宣称达到发布级原生体验。

发布前仍必须完成真实签名/TCC、真实 Wails 窗口中的 Quick Look/Finder smoke test、跨卷废纸篓
验证和只读全盘样本性能验证；这些验证不能由本机 ad-hoc 构建替代。

在目录结构切片完成前，不扩大首个业务垂直切片范围；新增跨进程恢复能力必须先新增 ADR。

## 12. 产品体验与交互契约

产品和交互以 [R-009 DaisyDisk 产品体验与交互基线](research/R-009-DaisyDisk产品体验与交互基线.md)
为参考基线，公开资料和社区许可证复核以 [R-013](research/R-013-DaisyDisk原生交互与开源参考复核.md)，
本机原版交互以 [R-014](research/R-014-DaisyDisk本机实机交互复核.md)，视觉版式以
[R-016](research/R-016-DaisyDisk视觉版式与窗口状态基线.md) 为准。Marmot 对齐其“先概览、渐进扫描、空间图下钻、预览、删除前复核”的体验链路，但不复制
第三方源码、品牌素材、文案或永久删除策略。

基础 P0 状态模型已满足扫描、清理和以下交互契约；原生布局重做完成前，不能把当前 UI 视为 DaisyDisk
体验完成；
[ADR-0016](adr/0016-DaisyDisk原生交互状态模型.md) 仍是后续修改的依据：

- 启动后先展示紧凑的本机挂载卷行、容量口径、权限状态和扫描入口；已有结果提供查看/重扫/放弃/Finder
  操作，扫描文件夹使用原生 Open 面板；
- 扫描必须先在后端发布可用的顶层/首批结果，后台继续补齐；用户可见窗口在扫描期间保持源页，终态
  后才进入空间图，并提供取消和部分结果；
- 空间图按 `owned_allocated` 导航，单层懒加载、排序、聚合和分页，不能一次传输百万节点；
- 结果首屏必须是 Sunburst、当前目录标题/排序列表和底部 Collector；不能以 Hero、卷卡片和常驻
  Inspector 卡片替代该结构；
- 目录列表和 Sunburst 共享同一批 `MapEntry`：目录单击下钻，文件单击只高亮，中心圆返回父目录；
- 选中对象可以通过 Space 预览，清理必须先进入可审查的计划，再确认、复核和执行；
- 权限不足、隐藏空间、聚合对象、文件变化和大小不确定性必须显式表达；
- 悬停、键盘焦点、选中和失效对象必须分离；悬停详情只能读取当前 DTO，不能每次触发 Wails；
- 文件夹单击下钻、中心/`Command + Up` 返回，`Command + [ / ]` 历史，方向键和 `Return` 必须共享同一
  导航命令；
- `smaller objects`、hidden space、purgeable space、other volumes、snapshot 和 restricted 必须是
  有能力限制的聚合/虚拟项，不能进入文件操作；
- Collector 必须支持展开、预览、移除和拖出；加入/移除只改变会话状态，不执行文件操作；
- 设备感知并发、扫描阶段、缓存、空间图数据载荷和 Quick Look 能力已经分别由
  [ADR-0014](adr/0014-分阶段扫描与设备感知并发.md)、[ADR-0024](adr/0024-macOS_SSD并发预算复测.md)、[ADR-0013](adr/0013-DaisyDisk空间图与渐进查询数据契约.md)
  和 [ADR-0015](adr/0015-macOS预览Finder定位与收集区平台边界.md) 锁定；多层空间图投影由
  [ADR-0017](adr/0017-有界多层空间图投影.md) 和 [ADR-0018](adr/0018-DaisyDisk视觉版式与窗口状态重做.md)
  锁定，后续实现不得绕过这些边界。

参考产品允许永久删除，Marmot 不采用该差异：Marmot 的默认动作仍是移入 macOS 废纸篓，
执行前必须重新校验文件身份和计划版本。R-009 的 P0 清单是首个体验垂直切片的验收入口，
P1/P2 能力不得在没有对应 SDD 条目和 ADR 的情况下直接实现。

## 13. 原生交互状态和开源参考

R-013 对 DaisyDisk 官方指南、当前 Marmot 前端和社区实现做了差距与许可证复核，R-014 对本机原版
进行了实际操作复核，ADR-0016 已接受
以下系统契约：

- 结果工作区由紧凑范围/卷入口、空间图、当前目录列表、上下文动作和 Collector 组成，不能用局部按钮
  堆叠替代状态模型；
- 前端区分 `hoveredEntry`、`focusedEntry`、`selectedEntry` 和 `staleEntry`；当前目录列表不被上下文
  Inspector 替代，展示优先级和导航后的恢复规则固定；
- 导航历史最多 32 条，历史项保存 `snapshotId`、`parentId`、口径和页偏移，前进/后退重新查询；
- `MapEntry` 目标取值为 `node | aggregate | virtual`，虚拟项带 `virtualType`、可信度和能力集合；
- 多层空间图只能使用后端有界 `children` 投影，不能重复绘制当前层或向 WebView 发送完整树；
- 前端只保留当前层、有限历史页和 Collector DTO，不能为了键盘导航恢复百万级完整树；
- 只有 MIT 项目在固定提交、保留版权/许可证声明并经过独立适配后，才可能进入代码复用评估；GPL、
  非商用或无确认许可证的项目只能阅读和提取设计结论；
- 后续实现必须以 [R-013](research/R-013-DaisyDisk原生交互与开源参考复核.md)、
  [R-014](research/R-014-DaisyDisk本机实机交互复核.md) 的 P0/P1/P2 分层和
  [ADR-0016](adr/0016-DaisyDisk原生交互状态模型.md) 和 [ADR-0018](adr/0018-DaisyDisk视觉版式与窗口状态重做.md)
  的验收标准为门禁。
