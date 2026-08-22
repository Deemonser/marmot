# ADR-0021 macOS `getattrlistbulk` 批量元数据扫描

状态：Accepted

日期：2026-08-21

## 背景

R-019 证明当前逐节点 `DirEntry.Info()` 在本机约 270 万节点的 `/` 扫描中耗时 41.4 秒。成熟的
macOS 磁盘分析器使用 `getattrlistbulk(2)` 批量取得目录项属性，减少每个文件一次元数据系统调用。

## 决策

1. 在 `internal/infrastructure/scanner` 增加 Darwin 专用批量目录读取适配器，使用 macOS SDK 公共
   `getattrlistbulk`；Go 的通用扫描编排、Domain 和 Application 不依赖 Darwin API。
2. 第一版请求并解析：`NAME`、`DEVID`、`OBJTYPE`、`MODTIME`、`FILEID`、目录 `MOUNTSTATUS`、文件
   `LINKCOUNT`、`ALLOCSIZE` 和 `DATALENGTH`。变长记录按长度和返回属性位图校验。
3. 每个 worker 重用自己的固定缓冲区；目录任务继续受 ADR-0014 的有界队列和设备画像预算约束。
   SSD 仍为 4 个 worker，机械盘 1 个，网络/虚拟卷 2 个，未知卷 2 个。
4. 批量 API 不支持或读取失败时回退到现有 `ReadDir + Info`；回退只作为兼容路径，不改变扫描问题、取消和部分
   结果语义。`searchfs` 不进入当前 APFS 主路径。
5. 批量读取得到的元数据直接映射为 Marmot `ScanNode`，不把 OpenDisk 的 Swift 类型、JSON 或缓存格式
   引入公共契约。

## 不变量

- 不跟随符号链接；嵌套挂载点不能递归进入。
- `logical_size`、`allocated_size`、`owned_allocated` 继续分开；无法取得的字段必须降级并标记可信度。
- 硬链接仍以卷内 `dev + inode` 去重；批量读取不得改变去重规则。
- 权限或 IO 错误不能伪装为空目录；取消观察点后不能提交新批次。
- 前端仍只接收摘要进度和快照查询，不接收逐文件事件。

## 被拒绝方案

- 只把 worker 数从 2 调大：无法解决每节点 `Info()` 的系统调用成本，也会放大 APFS I/O 争用。
- 复制 OpenDisk 的 Swift 扫描器：语言、缓存和快照模型不匹配，且会把外部实现细节混入公共契约。
- 用 `searchfs` 作为 APFS 默认路径：R-019 记录其 APFS 性能和 `EBUSY` 重试风险不适合作为首版主路径。
- 为了速度跳过修改时间、设备身份或硬链接计数：会破坏清理校验和空间口径不变量。

## 验收标准

- Darwin 编译和非 Darwin 编译均通过，通用回退路径可用。
- 高扇出队列不会死锁；批量路径覆盖小目录、硬链接、符号链接、嵌套挂载、权限错误和取消。
- 代表性本地 APFS 样本记录首层发布时间、总节点数、总耗时、CPU、RSS 和问题数量；不以单次结果替代
  设备分层性能结论。
