# R-037 Darwin 原生节点路径转换与分配削减预研

状态：代码切片已实施；纯扫描目标达成，完整终态目标未达成，正确性实现保留，结论由 ADR-0037 接受

日期：2026-08-24

## 1. 目标

降低 Darwin 原生扫描在 Go/C 回调后的每节点路径转换分配，保持完整的 `scan.Node.Path` 契约和现有
快照、清理、挂载边界语义。目标不是删除路径字段，也不是把生产扫描改成另一套节点协议。

本切片目标为：在同一真实 `/` 样本和串行条件下，纯扫描三次中位数推进到 `13.50s` 以内，完整可查询
二进制终态三次中位数推进到 `19.30s` 以内；节点数、问题数、查询、checksum、恢复和最大 RSS 相对
R-036 不回退超过 `3%`。完整终态 `<=15s` 仍是独立产品总门槛。

## 2. 约束和证据

生产入口使用 `scanner.Scanner` 和追加式二进制 `SnapshotStore`。二进制节点记录不保存完整路径，查询时
由父 ID 和名称重建路径；但 `scan.Node.Path` 仍是 Scanner 的公共批次契约，旧 SQLite 适配器、应用清理
校验、问题记录和通用测试仍会读取它。因此不能让生产路径优化通过返回空路径来换取速度。

本轮真实 `/` 只读 profile 环境为 Go `1.25.0`、Darwin `arm64`、macOS `26.5.2`，单 SSD 预算 8 worker：

- 纯扫描墙钟样本为 `13.344s`，CPU profile 墙钟 `13.31s`；profile 的累计样本 `94.65%` 位于
  `runtime._ExternalCode`，原生系统调用仍是最大成本；
- 分配 profile 总量约 `1184.46MB`；`nativeScanContext.addNodes` 的累计分配约占 `99.11%`；
- `filepath.Join`/`strings.Builder` 累计约 `333.55MB`，约占总分配 `28.16%`；
- `C.GoString` 累计约 `73.50MB`，约占总分配 `6.21%`；
- 因此本切片优先削减确定的每节点分配，不调整 worker、挂载边界、`getattrlistbulk` 请求字段或快照格式。

单次 profile 不能证明墙钟收益；目标必须用同一根目录三次中位数复测，分配 profile 只作为机制证据。

## 3. 方案比较

### 方案 A：使用已知名称长度和直接路径拼接

采用。原生记录已经在 C 层完成名称长度和边界校验，Go 侧使用 `C.GoStringN` 按该长度复制名称，并用
只处理规范化绝对父路径和单个目录项名称的直接拼接函数生成路径。目录项名称不能包含 `/` 或 NUL，
`getattrlistbulk` 不返回 `.`/`..` 伪目录；扫描根和每个父路径均来自已规范化根路径或同一拼接函数。

这保留一份可供 `Node`、清理和过渡 SQLite 使用的 Go 路径字符串，但避免 `filepath.Join` 的清理、
`strings.Builder` 中间增长和名称 `strlen` 扫描。

### 方案 B：让文件节点不携带 `Path`

拒绝。虽然生产二进制 reader 可以由父 ID 重建路径，但会破坏 Scanner 的通用契约、旧 SQLite 适配器、
问题展示和清理校验边界；如果未来要引入按需路径，需要另行定义版本化的内部批次端口。

### 方案 C：扩大 worker 或放宽挂载边界

拒绝。当前 profile 的主要累计样本在原生系统调用；增加并发会改变设备画像、I/O 竞争和结果可比性，
放宽边界会造成 Data/嵌套挂载重复扫描，均不属于本切片。

### 方案 D：改变 C/Go 批次协议或快照格式

拒绝。批次已经按有效 bulk 页受限传递，格式变化不能由本轮分配 profile 证明必要，会扩大取消、恢复、
checksum 和 Wails 公共契约的风险。

## 4. 结论与边界

- `nativeScanContext.addNodes` 必须继续为每个节点生成完整 `Path`；只更换名称复制和规范化路径拼接实现。
- 仅使用 C 记录中已验证的 `name_length`，禁止读取固定 `NAME_MAX` 缓冲中的未初始化尾部。
- 目录路径仍写入 `native.paths`，用于后续批次的父路径解析；文件路径不进入该 map。
- 保留节点顺序、ID 分配、硬链接去重、三种大小口径、问题路径、取消、批次所有权和 emitter 错误传播。
- 不修改 `scan.Node`、`BatchEmitter`、`SnapshotStore`、二进制 formatVersion、index、checksum、manifest、
  Wails DTO、清理校验、设备并发或挂载边界。

## 5. 验收

1. 新增 Darwin 路径拼接回归测试，覆盖根路径 `/`、普通绝对父路径和已有尾部 `/`；名称长度使用测试路径
   结果验证，不能出现重复或缺失分隔符。
2. `go test ./internal/infrastructure/scanner`、`go test -race ./internal/application ./internal/infrastructure/scanner`
   和 `go vet ./...` 通过。
3. binary snapshot 的分页/Map、checksum、恢复、目录 override 和应用级 smoke 通过。
4. 真实 `/` 串行三次记录纯扫描、完整终态、`FinishSnapshot`、节点/问题数量、查询、checksum、恢复和最大 RSS。
5. 只有纯扫描中位数 `<=13.50s`、完整终态中位数 `<=19.30s` 且其他指标无超过 `3%` 回退，才关闭本切片；
   未达成时保留正确性优化并记录证据，不调整阈值。

## 6. 对 DDD、SDD 和后续实现的影响

DDD 不变：路径是扫描观察事实和清理校验输入，不是文件操作授权。SDD 只补充 Darwin 扫描器可以使用名称
长度和规范化路径的直接拼接来降低分配；不能把完整路径从公共扫描节点中删除。

建议 ADR：[ADR-0037 Darwin 原生节点路径转换与分配削减](../adr/0037-Darwin原生节点路径转换与分配削减.md)。

## 7. 实施结果

已实施 `C.GoStringN`、单 backing buffer 的路径拼接和 Darwin 路径回归测试。分配 profile 对照为：

| 指标 | 实施前 | 实施后 |
| --- | ---: | ---: |
| 扫描器累计分配 | `1184.46MB` | `1150.04MB` |
| `C.GoString`/`C.GoStringN` | `73.50MB` | `63.50MB` |
| `nativeScanContext.addNodes` 累计分配 | `1173.97MB` | `1139.34MB` |

真实 `/` 纯扫描三次为 `13.069s`、`12.823s`、`14.526s`，中位数 `13.069s`，达到本切片的纯扫描目标；
节点均为 `2,846,466`，问题数为 `442`、`441`、`443`，差异来自扫描期间文件系统变化。

完整二进制应用三次为 `25.493s`、`31.320s`、`21.086s`，中位数 `25.493s`；`FinishSnapshot` 为
`4.958s`、`4.820s`、`3.621s`，中位数 `4.820s`，未达到完整终态 `19.30s` 目标。应用 CPU profile
仍有约 `81.83%` 累计样本位于 `runtime._ExternalCode`，说明下一轮应继续围绕原生扫描/系统调用和完整
终态 I/O 做独立预研，不能把本轮纯扫描达标扩大为完整终态达标。
