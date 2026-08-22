# R-019 macOS `getattrlistbulk` 批量元数据扫描预研

状态：已完成，结论由 ADR-0021 接受

日期：2026-08-21

## 1. 问题

当前扫描器对每个目录项调用一次 `DirEntry.Info()`。在本机启动盘 `/` 的只读 smoke test 中，约
270 万节点耗时 41.4 秒；这会把 DaisyDisk 的首层渐进体验拖成长时间等待，也会放大系统调用和
worker 调度成本。

## 2. 参考与证据

- OpenDisk 固定提交 `16765d0a13cdb38ab1c2db1391ef3d9e2b9af30e`，许可证 MIT；其 README 和
  `BulkDirectoryReader` 使用 macOS 公共 `getattrlistbulk(2)`，将名称、类型、文件 ID、目录挂载状态、
  硬链接数和实际占用放入批量返回缓冲区。
- OpenDisk 在 4M 条目的 APFS 样本上记录：并行 `getattrlistbulk` 约 12.6 秒；`searchfs` 约 27 秒以上，
  且卷变化可能以 `EBUSY` 中止后重扫。因此本项目不把 `searchfs` 作为 APFS 第一版主路径。
- macOS SDK 公开 `getattrlistbulk`、`attrlist`、`attrreference_t` 和所需属性位图；不需要复制 OpenDisk
  源码或引入其 Swift 实现。

## 3. 预研结论

1. macOS Darwin 扫描器对可用的本地文件系统优先使用 `getattrlistbulk`，一次读取目录项的名称、
   `dev/inode`、类型、修改时间、文件逻辑长度、实际占用、硬链接数和目录挂载状态。
2. 目录读取使用固定 256 KiB 缓冲区和现有受限 worker；不为每个目录创建 goroutine，不把整棵树
   放入内存。
3. 目录挂载状态和现有挂载目录表共同作为边界判断。挂载点可以作为解释性目录节点保留，但不得
   递归进入；符号链接不跟随。
4. 硬链接去重继续以 `dev + inode` 为身份，仅在 `linkcount > 1` 时进入去重集合。
5. 批量 API 失败或文件系统不支持时，保留现有 `ReadDir + Info` 路径作为逐目录回退，并记录权限/IO
   问题；批量路径不能静默把不可读目录当成空目录。
6. `getattrlistbulk` 只改变 Infrastructure 的元数据生产方式，不改变 Domain 节点大小字段、挂载边界、
   SQLite 快照契约、扫描阶段或 Wails 事件契约。

## 4. 风险与限制

- 属性缓冲区是变长记录，解析必须由记录长度和返回属性位图驱动，越界或缺失属性时丢弃该条并保留
  目录问题，不得读取不可信偏移。
- 某些 FileProvider、网络或虚拟文件系统可能不完整支持属性集合；这些路径使用回退实现并单独测量。
- 扫描耗时仍受权限、磁盘状态、SQLite 批量写入和系统文件变化影响；本记录不把 DaisyDisk 宣传数字
  变成硬性保证。
- 全盘目标的 15 秒体验门槛只作为本机代表性样本的测量目标，必须同时记录节点数、冷/热缓存、首层
  发布时间、总耗时、CPU 和 RSS。

## 5. 验收

- Darwin APFS 代表性样本不再对每个目录项执行 `DirEntry.Info()`。
- 小目录、硬链接、符号链接、嵌套挂载、权限错误和取消测试继续通过。
- 批量路径与回退路径产生相同的节点类型、大小口径、文件身份和部分结果语义。
- 2.7M 节点本机 smoke test 的总耗时、首层发布时间和资源占用被记录，不能只以单次总耗时宣称完成。
