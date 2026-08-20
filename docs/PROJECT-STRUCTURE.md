# 项目目录规范

状态：目标目录已冻结；现有首个切片目录仍是过渡状态

本项目采用轻量 DDD + 分层架构。目录表达职责边界，不要求为了形式创建没有业务内容的
空包；但一个模块不得跨层持有另一层的业务职责。

## 目标目录

```text
.
├── main.go                         # Wails 组合根、资源嵌入、窗口和应用生命周期
├── frontend/                       # React 页面；不能访问文件系统
│   ├── src/
│   └── bindings/                   # Wails 生成物
├── internal/
│   ├── domain/                     # 领域对象、值对象、不变量；不依赖技术框架
│   │   ├── scan/
│   │   ├── cleanup/
│   │   └── recommendation/         # 当前只保留边界，未进入首个切片
│   ├── application/                # 用例编排、任务生命周期、事务边界
│   ├── ports/                      # Scanner、SnapshotStore、Trash、Permission 等契约
│   ├── infrastructure/             # 技术实现和外部开源代码适配
│   │   ├── scanner/
│   │   └── snapshot/
│   ├── platform/                   # macOS 文件系统、权限、卷、废纸篓适配器
│   └── presentation/
│       └── wails/                  # Wails DTO、绑定入口、事件转换
├── docs/                           # DDD、SDD、预研、ADR 和基准
├── build/                          # Wails 构建配置和生成的跨平台资源，不放业务代码
└── third_party/                    # 如需复制第三方源码，按固定版本隔离并保留声明
```

`main.go` 保留在仓库根目录是 Wails `go:embed` 资源路径和组合根的工程约束，不代表它可以
承载领域规则。

## 依赖方向

```text
presentation/wails -> application -> domain
                         |             ^
                         v             |
                       ports <- infrastructure/platform
```

- `domain` 不依赖 Wails、SQLite、macOS API、前端或 Mole。
- `application` 只依赖 domain 和 ports，不直接调用平台 API。
- `presentation/wails` 只做绑定、DTO、事件和窗口生命周期。
- `infrastructure` 实现 ports；Mole 代码只能在这里隔离。
- `platform` 实现 macOS 能力，不做清理策略和推荐决策。
- `frontend` 只能调用生成的 Wails binding 和事件，不能导入 Go 或访问文件系统。

## 当前目录审计

当前目录可以运行首个实验切片，但不符合上述长期标准：

| 当前路径 | 当前问题 | 基准后的归属 |
| --- | --- | --- |
| `main.go` | 同时包含组合根和窗口配置，暂时可接受 | 保留为组合根，业务逻辑移出 |
| `service.go` | Wails DTO、用例编排、扫描收尾和清理规则混在一个文件 | 拆到 `presentation/wails`、`application`、`domain` |
| `internal/scanner` | 扫描技术实现未标明 Infrastructure 归属 | `internal/infrastructure/scanner` |
| `internal/snapshot` | SQLite 实现直接被服务依赖，缺少端口隔离 | `internal/ports` + `internal/infrastructure/snapshot` |
| `internal/platform` | macOS 能力适配基本符合职责 | 保留，按能力继续拆分 |
| `greetservice.go` | Wails 模板残留，不属于 Marmot 业务 | 基准清理阶段移除 |
| `build/*` | 含 Wails 生成的非 macOS 平台资源 | 作为构建资产保留，不视为当前业务实现 |

因此，当前代码目录是“过渡目录”，不是“标准目录”。文档基准提交只冻结目标，不在本次
文档提交中移动业务代码；目录重构必须作为独立、可验证的结构切片完成。

## 生成物规则

- `frontend/dist`、`frontend/node_modules`、`.task`、`bin` 和临时 bindings 文件属于生成物。
- Wails 生成的 `frontend/bindings` 是否提交由构建策略决定，但不能手工修改后当作领域契约。
- `build` 下的跨平台模板不代表 Windows、Linux、Android 或 iOS 已进入当前产品范围。
