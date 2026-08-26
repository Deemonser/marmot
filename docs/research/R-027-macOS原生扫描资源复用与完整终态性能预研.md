# R-027 macOS 原生扫描资源复用与完整终态性能预研

状态：已完成，结论由 ADR-0029 接受；本轮优化已实施，15 秒终态门槛仍以真实三次复测为准

日期：2026-08-24

## 1. 目标

将 macOS 真实 `/` 扫描的完整可查询终态中位数从历史约 `20.87s` 推进到 `15s` 以内。
本轮只处理已经有 profile 证据的原生扫描固定成本，保持约百万级节点、问题记录、分页/Map 查询、取消、
部分结果和快照原子发布语义不变。

## 2. 基线与证据

本机 macOS arm64、Go 1.25.13，执行：

```text
MARMOT_SCAN_ROOT=/ go test -cpuprofile=/tmp/marmot-baseline.cpu \
  -run '^TestScanConfiguredRootWithBinarySnapshotStoreSmoke$' -count=1 -v ./internal/application
```

得到一轮工作负载记录：

| 指标 | 结果 |
| --- | ---: |
| 节点 | 2,845,923 |
| 文件 | 2,260,137 |
| 目录 | 585,786 |
| 问题 | 441 |
| 完整终态 | 24.87s |
| FinishSnapshot wall time | 4.58s |

本轮样本比历史约 273 万节点更大，不能单独用绝对秒数替代三次中位数。CPU profile 的主要观察为：

- `runtime._ExternalCode` 累计约 `80.30%`，说明扫描主成本仍在 macOS 原生目录枚举/文件系统调用；
- `Writer.buildIndex` 累计约 `4.43%`，`externalSortFixed` 累计约 `3.74%`，暂不足以证明应先重写索引；
- `nativeScanContext.addNodes` 累计约 `1.25%`，但原生主循环代码审查发现每个目录都会申请并释放约 256 KB
  读取缓冲和约 9 MB 节点批缓冲；
- 原生每个节点都通过 `id_mutex` 获取 ID，锁粒度与节点数量同阶；每个 bulk 页还会单独申请 children
  数组。

因此，下一步应先消除 worker 内可复用资源的重复分配，并将 ID 锁从每节点一次降到每页一次。不能把
单次 profile 的低于 15 秒结果，或只优化 `FinishSnapshot`，宣称完整终态已经达标。

## 3. 方案对比

### 方案 A：worker 生命周期内复用原生缓冲，并按页分配 ID

采用。每个原生 worker 持有一个 `getattrlistbulk` 读取缓冲和一个节点批缓冲，连续处理目录任务时复用；
解析完一个 bulk 页后一次性预留该页有效节点的 ID 区间，再写入节点和 children。ID 只要求唯一、正数且
子节点大于父节点，不增加连续 ID 的领域契约。

### 方案 B：先重写 `FinishSnapshot` 的索引格式

暂不采用。当前 profile 只能证明索引有约 4 秒级 wall 成本，不能证明某个排序或校验步骤是稳定主因；
先改变索引格式会扩大恢复和完整性风险。

### 方案 C：关闭批次 `Sync` 或完整性校验

拒绝。提交 footer、数据校验、索引校验和 manifest 原子发布是 ADR-0028 的快照边界，不为性能数字削弱。

### 方案 D：增加 worker 数量

暂不采用。SSD 8 worker 已是 ADR-0024 锁定预算；再次加大并发需要新的设备画像和 I/O 证据，不能用调参
替代主循环固定成本优化。

## 4. 决策与实现边界

本轮只修改 `internal/infrastructure/scanner/native_darwin.go` 的原生 worker 资源生命周期和 ID 分配路径：

- 读取缓冲和节点批缓冲由 worker 申请一次、退出时释放；目录任务不再重复申请/释放；
- children 缓冲由 worker 复用；队列满载触发本地递归或 bulk 页超过复用容量时才临时申请独立数组，
  避免覆盖父任务仍在使用的 children 内容；
- ID 分配按有效 bulk 页批量完成，保留父 ID 先于子 ID、节点顺序可查询和错误/取消释放语义；
- 不改变 `ScanNode`、`SnapshotStore`、Wails DTO、快照文件格式、排序口径、挂载边界和清理校验；
- 非 Darwin 回退路径不改变；原生分配失败必须取消本次扫描并释放 worker 资源。

## 5. 验收与测量

必须分别记录纯扫描和完整可查询终态，并在相同根路径、权限和尽可能相同的文件系统状态下运行三次：

1. 节点、文件、目录、问题数量以及三种大小口径与基线一致，变化必须可解释；
2. root/children 分页、Map、`NodeByID`、问题查询、取消和尾部恢复测试通过；
3. `go test ./...`、相关包 race、`go vet ./...`、前端构建和 `git diff --check` 通过；
4. 真实 `/` 完整可查询终态中位数不超过 `15s` 才能关闭本轮目标；否则保留新的中位数和下一热点，
   不伪造达标；
5. 记录峰值 RSS、快照文件大小和 `FinishSnapshot` wall time，确认资源复用没有以异常内存增长换取时间。

## 6. 限制

- profile 运行期间文件系统内容可能变化，单次节点数和耗时不能替代三次中位数；
- `runtime._ExternalCode` 包含文件系统调用和原生等待，必须结合真实 wall benchmark 解释；
- 目录级增量缓存、mmap 查询和更深的索引重写仍是独立性能切片，不能在本轮隐式加入。

## 7. 对 DDD、SDD 和后续实现的影响

领域对象和不变量不变。SDD 增加原生 worker 资源复用和批次 ID 分配的实现门禁；ADR-0029 固定本轮方案。
若本轮未达到 `15s`，下一步仍须先提交新的 profile 证据和 ADR，再调整并发、缓存或索引格式。
