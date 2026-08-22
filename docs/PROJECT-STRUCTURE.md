# 项目目录规范

状态：目标目录已冻结；目录结构切片已完成，业务职责仍按垂直切片继续细化

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

- `domain` 不依赖 Wails、任何数据库实现、macOS API、前端或 Mole。
- `application` 只依赖 domain 和 ports，不直接调用平台 API。
- `presentation/wails` 只做绑定、DTO、事件和窗口生命周期。
- `infrastructure` 实现 ports；Mole 代码只能在这里隔离。
- `platform` 实现 macOS 能力，不做清理策略和推荐决策。
- `frontend` 只能调用生成的 Wails binding 和事件，不能导入 Go 或访问文件系统。

## 当前目录审计

当前目录已按目标边界完成第一轮结构切片：

| 当前路径 | 当前问题 | 基准后的归属 |
| --- | --- | --- |
| `main.go` | 只负责组合根、资源嵌入和窗口生命周期 | 保留在仓库根目录 |
| `internal/application` | 承载扫描/清理用例、任务生命周期和端口编排 | 当前第一阶段应用层 |
| `internal/presentation/wails` | 承载 Wails DTO、绑定入口和事件转换 | 当前第一阶段展示适配层 |
| `internal/domain`、`internal/ports` | 领域模型和技术无关契约已建立，Recommendation 只保留边界 | 后续按领域切片细化 |
| `internal/infrastructure/*` | 扫描器和 `SnapshotStore` 已归入基础设施并实现端口；当前 checkout 仍保留 SQLite 过渡适配器 | 保留，最终快照实现按 ADR-0028 切换为追加式二进制格式，Mole 只能在此隔离 |
| `internal/platform` | macOS 权限、文件身份和废纸篓适配 | 保留，按平台能力继续拆分 |
| `build/*` | 含 Wails 生成的非 macOS 平台资源 | 作为构建资产保留，不视为当前业务实现 |

因此，当前目录已经符合轻量 DDD + 分层架构的第一轮结构标准；这不表示所有领域行为都已实现。
扫描并发、APFS 语义、权限和清理安全仍必须按 SDD、预研和 ADR 逐项完成。

## 生成物规则

- `frontend/dist`、`frontend/node_modules`、`.task`、`bin` 和临时 bindings 文件属于生成物。
- Wails 生成的 `frontend/bindings` 是否提交由构建策略决定，但不能手工修改后当作领域契约。
- `build` 下的跨平台模板不代表 Windows、Linux、Android 或 iOS 已进入当前产品范围。
