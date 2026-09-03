# Marmot

Marmot 是一个 macOS 磁盘空间分析与清理工具。它把一块磁盘或一个文件夹扫描成可交互的多层空间图，让你看清空间被什么占据，把要删除的对象收集起来审查，再一次性删除。交互和视觉以 DaisyDisk 为基线。

## 功能

- **磁盘与文件夹扫描**：启动页列出本机可扫描的卷，也可以选择任意文件夹。扫描在后台分阶段进行，可随时取消；权限读不到的区域会作为部分结果明确标出。
- **多层空间图**：DaisyDisk 风格的 Sunburst，单击下钻、面包屑返回、悬停预览子层。空间图与右侧目录列表联动，隐藏空间和过小对象单独表示，不冒充文件。
- **APFS 语义**：正确处理系统卷与数据卷组成的卷组、firmlink 和 APFS 克隆，逻辑大小与实际占用分开记录，总量与磁盘已用对齐。
- **收集区**：把扇区或列表项拖到左下角收集，累计释放量随时可见。删除前重新校验，倒计时确认后**直接删除**。这个操作不经过废纸篓、不可撤销；唯一的拒绝是根目录、系统目录树、家目录根和卷根这类会弄坏机器的对象。
- **AI 清理建议（可选）**：接入任意 OpenAI 兼容接口（如 DeepSeek），基于扫描事实和内置规则给出带证据、风险等级和恢复方式的建议。AI 只能建议，不能执行任何文件操作。
- **macOS 集成**：Quick Look 预览、在 Finder 中显示。

## 系统要求

- macOS 12 或更高版本，Apple Silicon 或 Intel。
- 扫描系统目录需要在「系统设置 › 隐私与安全性 › 完整磁盘访问权限」中授权本应用。

## 从源码构建

依赖：

- Go 1.25+
- Node.js 与 npm
- Wails v3 CLI，版本必须与 `go.mod` 一致：

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.9
```

常用命令：

```sh
wails3 task dev                  # 开发模式，前端热更新
wails3 task build                # 编译到 bin/marmot
wails3 task darwin:package       # 生成并签名 bin/marmot.app
wails3 task darwin:package:dmg   # 额外生成 bin/marmot.dmg
```

`darwin:package` 会用钥匙串中名为 "Marmot Dev Signing" 的本地证书签名，这样重新构建后 macOS 不会反复索要磁盘访问权限；创建该证书的步骤见 `build/darwin/Taskfile.yml`。正式分发需要 Developer ID 签名与公证，见 [ADR-0006](docs/adr/0006-Wails工具链与macOS分发边界.md)。

## 开发

测试与检查：

```sh
go test ./...
cd frontend && npm test
cd frontend && npx tsc --noEmit -p tsconfig.json
```

修改了 Go 侧对外方法后重新生成前端绑定：

```sh
wails3 generate bindings -ts -i
```

目录结构：

```text
main.go                     应用入口，嵌入前端产物
internal/
  domain/                   扫描、清理、建议规则的领域模型
  application/              用例编排：扫描任务、空间图查询、清理计划、AI 建议
  ports/                    应用层依赖的接口
  infrastructure/           扫描器（Darwin 原生）、追加式二进制快照、AI 客户端
  platform/                 macOS 事实：卷表、权限、Quick Look、Finder、删除
  presentation/wails/       暴露给前端的服务与事件
  probe/                    针对真实机器的探针测试
frontend/                   React + TypeScript + Vite，Sunburst 用 d3-shape 绘制
build/                      应用图标、Info.plist、打包任务
docs/                       设计文档、ADR 与技术预研
```

依赖方向与目录规则见 [项目目录规范](docs/PROJECT-STRUCTURE.md)。

## 设计约束

- **SDD 是实现门禁**。任何功能必须先在 [SDD](docs/SDD.md) 中有边界、契约和验收标准；预研结论先写 ADR，再改 SDD，最后写代码。
- 扫描事实、清理计划和执行动作分离；清理计划在执行前重新校验。
- 逻辑大小、实际占用和去重后占用是三个字段，不能合并。
- 前端不直接访问文件系统；生产版不启动本地 HTTP 服务，只用 Wails 类型化绑定和事件。
- AI 不能授权或执行文件操作。
- 外部开源代码固定版本、记录许可证和来源。目前只复用了 Mole `V1.40.0`（MIT）中的扫描相关代码，见 [第三方代码声明](THIRD_PARTY_NOTICES.md)。

完整规则见 [AGENTS.md](AGENTS.md)。

## 文档

- [SDD 系统设计](docs/SDD.md)：模块边界、接口契约与验收标准
- [DDD 领域设计](docs/DDD.md)：领域概念与不变量
- [文档基准](docs/BASELINE.md)：当前门禁与已接受的决策汇总
- [ADR 决策记录](docs/adr/README.md)：所有架构决策及其取舍
- [技术预研](docs/research/README.md)：高风险假设的验证记录与性能测量

## 当前状态

核心链路已经完整：扫描、空间图、目录导航、收集区、直接删除、AI 建议和 macOS 集成都已落地并在真实全盘扫描上验证。仍在推进的是全盘扫描的性能目标（完整终态 15 秒，本机约 280 万节点目前在 20 秒上下），历次测量与切片记录在技术预研中。

明确不做的事：Windows 实现、AI 自动删除、安全擦除、集成 Mole 当前 GPL 版本、一次性向前端传输百万级节点。

## 许可

本仓库尚未选定许可证。第三方代码的许可证与来源见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
