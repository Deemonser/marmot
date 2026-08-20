# SDD 系统设计

状态：技术基准已冻结，正式实现等待文档基准提交后进入垂直切片和发布前 smoke test

SDD 是项目实现门禁。代码、接口和模块必须先在这里定义边界、依赖和验收标准。

文档基准和当前目录的过渡状态见 [BASELINE](BASELINE.md) 与
[项目目录规范](PROJECT-STRUCTURE.md)。本轮只确认文档，不把过渡目录误认为最终架构。

## 1. 产品范围

第一阶段只支持 macOS，但产品目标是扫描整个本地文件系统并解释空间使用。Windows 只保留平台端口，不进入当前实现。

## 2. 技术栈

| 层 | 方案 |
| --- | --- |
| 桌面运行时 | Wails `v3.0.0-beta.9` |
| 后端 | Go 1.25+，本机验证 Go 1.25.13 |
| 前端 | React + TypeScript + Vite |
| 可视化 | D3.js |
| 生产通信 | Wails 类型化 Go-JS 绑定和应用事件 |
| 本地快照 | `SnapshotStore` 端口；SQLite + `github.com/mattn/go-sqlite3`，WAL 和批量写入 |
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
Ports: Scanner / SnapshotStore / Trash / Permission
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
- Windows 后续只增加 Platform Adapter，不改变 Domain/Application 契约。

Mole 代码若复用，只能来自固定的 MIT 提交 `V1.40.0`，并放在 Infrastructure 的独立目录中。其输出必须先映射为 Marmot 自己的扫描节点和快照模型，不能让 Mole 的 JSON 或内部对象成为公共契约。

当前首个切片允许以过渡目录运行，但不得新增跨层依赖。目录重构必须遵守
[项目目录规范](PROJECT-STRUCTURE.md)，并作为独立结构切片验证。

## 4. Wails 运行与安全边界

生产构建嵌入前端资源，通过 Wails 绑定直接调用 Go 服务，不启动本地 HTTP 服务，也不把端口、Token 或 CORS 暴露给浏览器。

第一阶段采用 Developer ID 签名和公证的直装分发；App Store 沙盒、CLI 和 HTTP 服务不在当前范围。
当前机器只有 ad-hoc 签名能力，真实 Team ID/TCC 流程是发布前门禁。

开发模式可以使用 Vite 开发服务器，但开发服务器不是生产架构。未来若需要 CLI 或无界面服务，必须新增独立 Transport ADR，不能复用桌面应用的隐式权限边界。

Wails 暴露的方法必须：

- 只提供明确的领域用例；
- 使用强类型参数和返回值；
- 在 Go 边界校验路径、快照 ID、计划版本和能力；
- 不暴露任意命令执行、任意路径删除或任意文件读取方法；
- 只推送必要的扫描进度和结果事件。

## 5. 扫描架构

```text
ScanCoordinator
  -> 卷/权限探测
  -> 顶层目录快速发布
  -> 受限并发的目录遍历
  -> 元数据、卷身份和大小计算
  -> 硬链接/完整克隆去重
  -> APFS clone metadata 和快照对象识别
  -> 分批写入 SnapshotStore
  -> 更新目录汇总
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
- 扫描是最终一致快照，变化、取消和权限问题必须保留。

## 6. 空间数据模型

节点不能只有一个 `size` 字段，至少需要：

```text
logical_size       文件逻辑长度
allocated_size     卷上实际占用估计
owned_allocated    去重后的归属占用
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
- 增量写入和取消后的安全收尾；
- schema 版本和过期策略。

首个切片的进程重启语义是：不续扫；上次仍为运行中的任务恢复为 `interrupted`，已提交
快照保留为部分结果。清理计划暂为会话内对象，跨进程持久化不在本阶段。

SQLite 使用 WAL、`synchronous=NORMAL` 和默认 10,000 节点批次；子节点查询使用
`snapshot_id, parent_id, owned_allocated DESC, id` 索引，单次最多返回 1000 节点。
本机合成 100 万节点基线约 159 MB 数据库、101 MB RSS、3.1 秒树形写入；门槛详见 R-004。

## 8. 用例接口

Wails 对外暴露的接口先按行为定义：

| 用例 | 输入 | 输出 |
| --- | --- | --- |
| 开始全盘扫描 | 扫描选项 | 扫描任务 ID |
| 查询扫描状态 | 任务 ID | 状态、进度、卷、权限和问题 |
| 查询子节点 | 快照 ID、父节点 ID、分页条件 | 节点、汇总和限制 |
| 取消扫描 | 任务 ID | 最终状态 |
| 创建清理计划 | 快照 ID、候选项、策略 | 计划 ID、原因和估算 |
| 校验清理计划 | 计划 ID、版本 | 总体和逐项结果 |
| 确认清理计划 | 精确版本 | 确认结果 |
| 执行清理计划 | 已确认计划 ID | 逐项执行结果 |

Wails 事件至少包括扫描进度、扫描问题、快照更新、清理进度和清理结果。客户端断线或窗口重开后，必须通过查询恢复状态。

## 9. macOS 权限

- 全盘扫描必须在 Wails 应用身份下验证 Full Disk Access 流程。
- 每个卷和目录必须记录可访问、部分可访问或不可访问状态。
- 权限不足不能被当作空目录。
- 第一阶段不支持 root/管理员扫描，不通过隐式 shell 提权。
- Developer ID 直装分发是当前方案；App Store 沙盒不进入第一阶段。
- 真实签名、Full Disk Access 和公证仍需在发布环境完成 smoke test。

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
  -> 顶层结果即时出现
  -> 按需展开子目录
  -> 创建并校验清理计划
  -> 用户确认
  -> 移入废纸篓
  -> 展示逐项结果
```

R-004、R-005、R-007 和 R-008 已完成本机验证，R-006 已完成 ad-hoc 打包验证；在真实签名/TCC
smoke test、跨卷废纸篓验证和真实只读全盘样本完成前，不宣称达到发布级全盘目标。

正式实现仍必须先完成：

1. 固定 Go/Wails 构建环境和 bundle identity；
2. 实现 SnapshotStore schema migration 和基准回归；
3. 实现 macOS 权限、APFS metadata 和 Trash adapters；
4. 用垂直切片验证取消、部分结果、重启恢复和计划版本。

在本基准提交完成前，不继续增加业务代码或扩大首个切片范围。
