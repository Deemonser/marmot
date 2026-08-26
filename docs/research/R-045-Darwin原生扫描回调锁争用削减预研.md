# R-045 Darwin 原生扫描回调锁争用削减预研

状态：代码切片已实施（完整终态性能子目标达成；纯扫描回退对照仍受基线限制）

日期：2026-08-24

## 1. 目标

R-044 实施后，快照归并正确性已通过，但真实 `/` 完整终态仍主要受 Darwin 原生扫描 wall time 影响。最新 mutex
profile 显示 `nativeScanContext.addNodes` 约占 mutex delay 的 `98.7%`，争用等待约 `1.046s`。当前批次在
同一把 Go mutex 内完成父路径查找、路径拼接、节点字段转换、硬链接去重和目录汇总，多个原生 worker 因而在
回调入口排队。

本轮目标：

- 将批内父路径快照后的路径拼接和节点转换移到全局锁外；
- 每个批次只进行一次全局状态合并，保留现有批内顺序；
- 保持 emitter 在锁外执行，避免 Application/SnapshotStore 背压阻塞扫描状态锁；
- 不改变节点 ID、父子关系、硬链接去重、目录汇总、挂载边界、取消和快照契约；
- 以同一真实 `/` 串行条件重新测量纯扫描、`FinishSnapshot` 和完整终态，目标是相对 R-044 完整终态中位数
  降低至少 `10%`，且纯扫描、节点数、问题数、查询、最大 RSS 不回退超过 `3%`；
- 产品完整终态 `15s` 仍是独立总门槛。

## 2. 问题与证据

当前 `addNodes` 的临界区覆盖：

```text
Lock
  -> 每条记录查 parent path
  -> joinNativePathAndName
  -> C 字段转换和大小可信度计算
  -> 硬链接 seen 查找/写入
  -> 目录 map、目录大小和 Result 汇总
Unlock
  -> emitter
```

R-044 的真实 `/` 结果为 `FinishSnapshot` 中位数 `6.82s`、完整终态中位数 `34.15s`。mutex profile 中该临界区
是明确的争用热点，且路径转换和节点构造不需要观察共享 map 的实时变化，只需要当前批次各 parent 的不可变
字符串值。目录状态的写入、硬链接判定和汇总才需要串行化。

## 3. 方案比较

### 方案 A：两段式批次处理，采用

第一段在短锁内按 raw entry 顺序读取 `directories`，只复制每条记录需要的 parent path 字符串头并验证父节点
存在；释放锁后完成路径拼接、Name 复用、大小和可信度转换，并按 parent 聚合目录大小增量；第二段重新加锁
一次，按原始批内顺序更新 `seen`、`directories`、`dirSizes` 和 `result`。状态合并完成后再在锁外调用 emitter。

Go string 是不可变值，目录状态不会修改已发布 path 的 backing bytes；节点构造产生独立的 Go path backing
buffer，不引用 getattrlistbulk 的 C 批次内存。

### 方案 B：把整个目录状态改为无锁或 sync.Map，拒绝

这会扩大状态所有权和顺序语义变化，增加每次 parent 查询的类型转换或原子协议成本；当前需求只需要缩短临界
区，不需要替换有界目录状态模型。

### 方案 C：每个 worker 独立状态，Finalize 再合并，拒绝

会改变硬链接发现顺序、父目录可见性和 ID/关系发布顺序，并需要新的跨 worker 合并契约；与本轮行为保持目标
不一致。

### 方案 D：取消全局硬链接去重或目录汇总，拒绝

会直接破坏 `owned_allocated` 和目录空间图事实，不能作为性能优化。

## 4. 实施边界

- 只修改 `internal/infrastructure/scanner/native_darwin.go` 的私有回调状态路径；不新增公共端口、DTO、配置或
  worker 数量。
- 保留 `directories`、`seen` 和 `dirSizes` 的全局所有权；只缩短锁的持有范围。
- 每批的文件目录大小增量在锁外按 parent 聚合，锁内每个 parent 只合并一次；硬链接重复项仍在全局 `seen`
  判定后从对应增量中扣除。
- parent path 快照缺失仍拒绝该批次；emitter 错误、context 取消和原生 issue 记录语义不变。
- `Node.Path` 和 `Node.Name` 必须拥有 Go 内存；不得持有 C 批次指针。
- 状态合并必须按 raw batch 顺序执行，硬链接的首个 owner、结果计数和目录增量不能因为并发而改变现有批次内
  语义。
- 不改变 `getattrlistbulk`、`openat`、挂载 boundary、fd 限额、批次上限、SnapshotStore、checksum、manifest、
  恢复、取消或 Wails 契约。

## 5. 验收

1. 配置扫描器回归覆盖批量节点、队列满载、取消、挂载边界、路径和目录汇总；Darwin race 通过。
2. `go test ./internal/infrastructure/scanner`、相关 `go test -race`、`go test ./...`、`go vet ./...`、前端
   构建和 `git diff --check` 通过。
3. 真实 `/` 串行三次记录纯扫描、`FinishSnapshot`、完整终态、节点/问题、查询、checksum、恢复和最大 RSS。
4. 只有相对 R-044 完整终态中位数降低至少 `10%` 且其他指标无超过 `3%` 回退，才关闭 R-045 性能目标；否则
   保留正确性实现并记录下一 profile 热点。
5. 产品完整终态 `15s` 仍独立验收。

## 5.1 实施结果

R-045 已实施于 `internal/infrastructure/scanner/native_darwin.go`。配置扫描批量、队列满载、取消、挂载边界、
路径、硬链接和目录汇总回归通过；相关 race、全仓 Go 测试、`go vet`、前端生产构建和 `git diff --check` 均通过。

同一真实 `/` 串行条件下，纯扫描三次为 `22.553s`、`21.645s`、`20.309s`，中位数 `21.645s`；完整 binary
终态三次为 `26.691s`、`28.961s`、`23.266s`，中位数 `26.691s`；对应 `FinishSnapshot` 为 `4.723s`、
`4.997s`、`4.670s`，中位数 `4.723s`。三轮均完成为 `completed_with_issues`，节点约 `285.7` 万，问题
`442-445`，根节点、首层分页和查询成功。相对 R-044 完整终态中位数 `34.15s`，本轮下降约 `21.8%`，达到
R-045 的 `10%` 完整终态子目标。

新的 mutex profile 总等待约 `479.56ms`，其中 `addNodes` 约 `456.28ms`（`95.15%`）；相对 R-044 记录的
约 `1.046s` 争用等待，绝对等待下降约 `54%`。一次带 `/usr/bin/time -l` 的完整 smoke 最大 RSS 为
`440,795,136` 字节（约 `420MiB`）。R-044 没有保存同口径纯扫描三轮和最大 RSS 基线，因此不能据此宣称
这些次级指标均已完成 `3%` 回退门禁；产品完整终态 `15s` 也仍未达成。

## 6. 对 DDD、SDD 的影响

DDD 不变。SDD 只补充 Darwin Infrastructure 的批次锁边界：共享目录状态的读快照、锁外节点转换、单次批量
状态合并和锁外 emitter；扫描事实、快照格式、发布边界、取消语义和清理授权不变。

建议 ADR：[ADR-0045 Darwin 原生扫描回调锁争用削减](../adr/0045-Darwin原生扫描回调锁争用削减.md)。
