# R-026 macOS 原生扫描主循环与非 SQLite 快照预研

状态：已完成，结论由 ADR-0028 接受；实现尚未开始

日期：2026-08-22

## 1. 问题

当前 Marmot 已经使用 `getattrlistbulk(2)`、`openat` 和 SSD 8 worker，但完整扫描仍明显受
SQLite 写入和 Go/C 边界影响。需要确定首版是否继续使用 SQLite，以及成熟产品如何在百万级节点下
同时满足以下要求：

- 首次冷扫描尽快得到完整可查询快照；
- 扫描期间支持取消、部分快照和进程退出后的中断恢复；
- 结果页只查询当前目录和有界空间图，不把百万级节点发送到 Wails；
- 清理前仍能读取真实路径和 `device/inode`，重新校验文件身份；
- 重扫可以复用未变化目录，而不是每次重新生成整棵事实树。

## 2. 已知事实

### 2.1 Marmot 当前基线

本机 macOS arm64、Go 1.25.13、扫描根 `/` 的当前工作树只读 smoke：

| 指标 | 结果 |
| --- | ---: |
| 节点 | 2,719,896 |
| 文件 | 2,185,145 |
| 目录 | 534,751 |
| 问题 | 441 |
| 纯扫描耗时 | 15.75s |

R-024 的完整 Application + SQLite smoke 记录为约 20.06s；同步 SQLite 约 30.6s，早期目录
汇总写回 `scan_nodes` 的实现约 61s。纯扫描与完整终态不是同一个指标。

当前 CPU profile 的主要观察：

- 约 91.6% 的采样停留在 `runtime.cgocall`；这包含 C 侧文件系统工作，不能全部视为边界开销，
  但说明热路径被拆成了大量 Go/C 调用；
- 当前每个目录都由 Go 调度并进入一次 C 目录读取函数，约 53 万个目录会产生同数量级的目录级
  cgo 往返；目录还会单独执行 `open/openat/close`；
- `emitNodes` 在全局互斥锁内完成节点 ID、硬链接去重、汇总统计和回调，节点处理不能保持 8 个
  worker 的并行度；
- Application 将每个节点转换成包含完整路径和多个字符串字段的 Go 对象，再通过有界队列写入
  SQLite；最终还要单独写入大量目录派生汇总。

相关实现位置：

- [scanner.go](../../internal/infrastructure/scanner/scanner.go)：目录任务、节点转换和全局状态；
- [bulk_darwin.go](../../internal/infrastructure/scanner/bulk_darwin.go)：单目录 C 批量读取；
- [service.go](../../internal/application/service.go)：节点队列和 SQLite writer；
- [store.go](../../internal/infrastructure/snapshot/store.go)：逐节点事实和目录汇总存储。

### 2.2 DaisyDisk 可观察证据

本机 `/Applications/DaisyDisk.app` 为 4.34.2。只读检查其 Mach-O 动态符号和字符串得到：

- 直接使用 `getattrlistbulk`、`getattrlist` 和 `statfs`；
- 使用 GCD queue、group、semaphore 和自有 `CSScan...`、`SnapshotScanConcurrentTask`、
  `directoryQueue`、`scanDispatcher` 等扫描任务结构；
- 存在 `DirectoryItem`、`serialize/deserialize`、`modifiedDirectories`、
  `rescanSourceAsLastTime` 等自有目录对象和重扫相关能力；
- 未看到 SQLite 动态库依赖，用户可见的 DaisyDisk Application Support 目录也没有百万级节点数据库。
  这不能证明其内部绝对没有数据库，但足以证明它没有采用 Marmot 当前的“每个节点写 SQLite 行”路径；
- 本机可观察到扫描期间留在磁盘选择页，完成后进入结果工作区；官方并行扫描说明明确区分 SSD 并行
  和机械盘排队，官方速度说明给出的 APFS SSD 参考范围约为 12--20 秒。

参考：

- [DaisyDisk 并行扫描说明](https://daisydiskapp.com/guide/4/en/ParallelScan/)；
- [R-001 成熟产品与扫描策略](R-001-成熟产品与扫描策略.md)；
- [R-013 DaisyDisk 原生交互与开源参考复核](R-013-DaisyDisk原生交互与开源参考复核.md)；
- [R-024 SQLite 端到端复测](R-024-SQLite扫描写入并发与端到端性能复测.md)。

## 3. 方案对比

### 方案 A：继续以 SQLite 作为扫描主存储

拒绝。SQLite 的查询能力没有问题，R-004 也证明它可以承载百万级节点；问题在于当前写入模型
把扫描元数据、完整路径、目录派生汇总和索引维护全部放进首轮扫描关键路径。异步队列只能重叠工作，
不能消除写入和索引成本。继续调批次、WAL 或 `synchronous` 不能解决架构差距；`synchronous=OFF`
也不满足快照耐久性边界。

### 方案 B：扫描先写 append-only spool，完成后导入 SQLite

部分采用，但不作为最终方案。它可以缩短用户可见扫描时间，却仍需要一次完整 SQLite 导入才能
形成首版查询结果，不能满足“完整可查询终态不依赖 SQLite”的目标，并且保留两套存储格式和维护成本。

### 方案 C：追加式二进制快照文件加固定索引

采用。扫描器只追加紧凑记录，查询通过固定索引、`pread` 和受控 `mmap` 读取当前层；不引入通用
数据库、不建立每节点 SQL 索引。快照文件既可作为扫描中的部分结果，也可作为终态结果和缓存的
唯一事实源。

### 方案 D：只保留内存树

拒绝。不能满足崩溃恢复、部分快照、重启后查看、缓存 TTL 和清理前稳定身份校验；高峰内存也不可控。

### 方案 E：引入其他嵌入式 KV/文档数据库

拒绝。它们仍然把通用事务、索引和依赖带入扫描热路径，且当前没有比固定二进制格式更强的查询需求或
本机证据。未来若增加任意条件查询、跨快照分析或搜索索引，必须单独预研和 ADR。

## 4. 目标架构

```text
Darwin NativeScanner
  -> 原生目录队列、getattrlistbulk、openat、设备并发和挂载边界
  -> 原生局部聚合、硬链接/clone 身份和紧凑批记录
  -> 每 32,768 条记录或 4 MiB 批量跨 Go/C 边界一次
        |
        v
SnapshotWriter
  -> append-only 二进制事实段
  -> 目录摘要和当前层子项索引
  -> 提交标记、校验和、manifest 原子更新
        |
        v
SnapshotStore
  -> pread/mmap 当前层分页、Map 投影和节点身份读取
  -> Wails 只返回有界 DTO
```

### 4.1 快照逻辑结构

物理文件可以在实现阶段选择单文件分段或多个同目录文件，但逻辑上必须包含：

- `manifest`：schema、scanner/size-basis 版本、task ID、scope、卷身份、状态、phase、摘要和限制；
- `node records`：固定宽度元数据，包含 `node_id`、`parent_id`、名称切片、类型/标志、三种大小、
  volume、device/inode、修改时间和可信度；不保存每个节点的完整路径；
- `string arena`：名称和必要错误文本的长度前缀字节区；
- `directory index`：目录 ID 到子项范围、直接汇总和可信度；
- `child order index`：按 `owned_allocated DESC, node_id` 稳定排序的子项 ID 段；
- `issue log`：权限、变化、取消和读取问题；
- `commit footer`：批次序号、有效长度、计数和校验和。

路径由根路径、父子关系和名称切片按需重建。预览、Finder 和清理动作必须先从快照读取真实节点，
再用平台 API 重新捕获并比较 `device/inode`、类型、大小和修改时间；快照文件本身不是授权。

### 4.2 写入、取消和恢复

- 一个 SnapshotWriter 顺序追加批次，不在扫描线程上执行每节点系统调用或 SQL；
- 每个批次先写记录和校验信息，再写 commit footer；manifest 只通过临时文件加原子 rename 更新；
- 进程崩溃时只承认最后一个完整 footer，未提交尾部直接丢弃；
- `TopLevelPublish` 只等待首层批次和索引段提交，`ScanJob` 仍为 running，Presentation 仍保持源页；
- 取消后不接收新的扫描批次，已提交段标记为 partial；
- 启动时把 manifest 中的 running 快照标记为 interrupted，不续扫；
- 文件权限固定为用户私有，读取时校验 magic、schema、段长度、偏移、计数和校验和，拒绝越界映射。

### 4.3 目录级缓存复用

首轮完成后，目录缓存键至少包含：

```text
volume_id / device / inode / parent identity / name / mtime / ctime-or-change marker
permission state / mount boundary version / scanner version / size-basis version
```

命中时复用目录子树记录和汇总，不把旧结果直接当作当前事实；命中目录仍需要重新确认入口元数据、
权限、挂载边界和扫描版本。硬链接、APFS clone、父目录改名、权限变化、挂载变化和 scanner/schema
版本变化必须使相关缓存失效。增量复用是首版第二个性能切片，不能先于快照格式和身份校验实现。

## 5. 验证计划与门槛

实现前必须先完成最小 POC，验证格式和恢复语义，不以设计推测性能：

1. 用 100 万和真实 `/` 样本写入固定记录、名称区、目录索引和排序子项索引；
2. 对比节点数、问题数、三种大小、分页、Map 投影、Preview/Reveal 身份读取和取消结果；
3. 模拟写入中断、尾部截断、坏长度、坏校验和、manifest 中断更新；
4. 记录 cgo 调用次数、扫描耗时、可查询终态耗时、峰值 RSS、快照大小和重启恢复时间；
5. 以相同根路径、权限、节点规模和冷/热缓存对比旧 SQLite 基线。

实现门槛：

- 真实 `/` 样本的节点、文件、目录和问题数量与扫描器基线一致，差异必须可解释；
- 纯扫描和完整可查询终态分别计时，完整终态目标为本机三次同条件运行中位数不超过 15 秒；
- 单次跨 Go/C 批量不得按目录回调，记录数与 cgo 调用数必须保持批量比例；
- 查询只读取有界当前层和投影，单次 Wails 返回不超过 256 KB；
- 取消、崩溃尾部恢复、部分快照和清理前身份校验不能回归；
- 生产构建不再引入 `go-sqlite3` 和 SQLite 数据库文件。

## 6. 结论

首版不再使用 SQLite 作为扫描快照的运行时存储或缓存事实源。`SnapshotStore` 端口保留，底层改为
追加式二进制快照和固定索引；扫描热路径改为 Darwin 原生目录队列，并以有界紧凑批次跨 Go/C 边界。

SQLite 相关 R-004、R-023、R-024 和 ADR-0007、ADR-0025、ADR-0026 保留为历史基准和反例，不再作为
新实现依据。旧开发数据库不进入发布迁移承诺；项目尚未发布，首版可以直接切换快照目录格式。

## 7. 限制

- DaisyDisk 是闭源软件，原生符号和行为只能证明可观察的技术方向，不能证明其私有文件格式；
- 当前 append-only 格式尚未完成 POC，15 秒目标是验收门槛而不是已达成结果；
- 增量复用对 APFS clone、硬链接、Firmlink、FileProvider 和权限变化的完整失效策略仍需实现测试；
- 搜索、跨快照比较和任意条件过滤不属于首版快照查询能力，未来需要独立索引预研。

## 8. 对 DDD、SDD 和后续实现的影响

- DDD 的 `ScanSnapshot`、`ScanNode`、部分结果、失效对象和清理校验语义不变；物理数据库不属于领域语言；
- SDD 将 `SnapshotStore` 定义为二进制快照端口，补充原生扫描批次、文件格式、恢复、查询和缓存契约；
- 下一步只实施 POC 和 Infrastructure 适配，不修改 Wails 公共接口、清理领域不变量或前端 DTO；
- 任何恢复 SQLite、引入通用数据库、跨快照搜索或可续扫能力都必须新增 ADR。
