# ADR-0037 Darwin 原生节点路径转换与分配削减

状态：Accepted

日期：2026-08-24

实施状态：代码切片已实施；纯扫描目标达成，完整终态目标未达成

## 背景

R-036 后 Darwin 原生扫描的分配 profile 显示 `nativeScanContext.addNodes` 约占累计分配的 `99.11%`；
其中 `filepath.Join`/`strings.Builder` 约 `333.55MB`，`C.GoString` 约 `73.50MB`。CPU profile 的
`94.65%` 累计样本仍属于原生系统调用，因此本 ADR 只处理已确认的 Go 侧逐节点分配，不改变原生 I/O 并发。

依据：[R-037](../research/R-037-Darwin原生节点路径转换与分配削减预研.md)、
[ADR-0021](0021-macOS_getattrlistbulk批量元数据扫描.md)、
[ADR-0022](0022-macOS目录fd与openat扫描路径.md)、
[ADR-0028](0028-macOS原生扫描与追加式二进制快照.md)。

## 决策

### 1. 按原生记录长度复制名称

`addNodes` 使用 C 记录已经校验过的 `name_length` 调用 `C.GoStringN`，不再让 `C.GoString` 对名称重新
执行 NUL 扫描。名称仍复制为 Go 字符串，C 批次返回后不保留 C 内存引用。

### 2. 使用受约束的直接路径拼接

新增 Darwin 内部 `joinNativePath(parent, name)`：父路径来自已 `filepath.Clean` 的扫描根或该函数自身，
名称来自 `getattrlistbulk` 的单个目录项；函数只追加一个必要的 `/`，不执行通用 `filepath.Join` 的
清理和中间 builder 分配。

该函数只用于已经通过原生目录项解析的名称。路径语义保持为当前 macOS 绝对路径语义；目录路径继续保存
在 `native.paths`，文件路径继续写入 `scan.Node.Path`，因此清理和过渡 SQLite 不受影响。

### 3. 保留系统边界和公共契约

不修改节点 ID、父子顺序、硬链接去重、问题路径、取消/错误回收、批次所有权、挂载 boundary、fd/openat
生命周期、设备并发、`scan.Node`/`BatchEmitter`、SnapshotStore、二进制格式、checksum、恢复和 Wails 接口。

## 不采用的方案

- 清空文件节点 `Path`：破坏通用 Scanner、SQLite 过渡实现和清理校验。
- 扩大 worker 或修改 `getattrlistbulk` 请求：没有本轮证据支持，且改变 I/O 基线。
- 让 C 内存直接作为 Go 字符串：违反回调生命周期和 C 内存所有权边界。
- 修改二进制快照格式：路径本来就不写入生产节点记录，格式变化不能带来本切片收益。

## 验收标准

- Darwin 路径拼接、批量扫描、取消、挂载边界、硬链接和问题回归通过；
- `go test -race ./internal/application ./internal/infrastructure/scanner`、`go vet ./...`、前端构建和
  `git diff --check` 通过；
- 真实 `/` 三次纯扫描中位数 `<=13.50s`、完整终态中位数 `<=19.30s`；
- 节点/问题数量、查询、checksum、恢复、`FinishSnapshot` 和最大 RSS 相对 R-036 无超过 `3%` 回退；
- 完整终态总 `15s` 门槛继续独立追踪。

## 与既有决策的关系

补充 ADR-0021、ADR-0022、ADR-0028、ADR-0029、ADR-0032 和 ADR-0036；不改变 DDD、SDD、快照公共契约或
生产存储方案。
