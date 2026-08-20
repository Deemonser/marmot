# R-002 Mole 能力与许可证

状态：已完成（本机、标签和源码核对）

## 环境

- 本机：macOS 26.5.2，arm64。
- 安装方式：Homebrew。
- 本机版本：Mole 1.40.0。
- Git 标签：`V1.40.0`。
- 固定提交：`c3ef1f50ea7a2490957dd8cbffadfcc350dd1182`。
- `mo analyze --help`：只有 `-json` 输出开关。

## 本机验证

对当前项目目录执行：

```bash
MO_ANALYZE_PATH=/Users/deemo/CodeProjects/marmot mo analyze --json
```

结果只返回当前目录的直接子项、总大小和文件数，例如 `docs`、`README.md`、`AGENTS.md`，不会返回完整递归目录树。

## 源码观察

`cmd/analyze` 中确实有递归扫描、并发限制、缓存、硬链接去重、取消和实时扫描能力，但 `json.go` 的输出模型仍然是当前目录的 `entries`、`large_files`、`total_size` 和 `total_files`。

Mole 的命令行可以接受目标路径，默认分析模式还会使用预设的系统目录概览。它适合借鉴扫描策略，也可以作为受控的外部适配器，但不能直接作为 Marmot 的完整目录树协议。

## 许可证核对

- GitHub 当前 `main` 的 API 元数据和 `LICENSE` 文件显示 GPL-3.0-or-later。
- Git 标签 `V1.40.0` 对应提交 `c3ef1f50ea7a2490957dd8cbffadfcc350dd1182`，其 `LICENSE` 文件显示 MIT。
- 本机 Homebrew `1.40.0` 安装包内的 `LICENSE` 也显示 MIT。
- Homebrew 当前公式已经指向 GPL-3.0-or-later 的新版本，因此不能只依赖包名 `1.40.0`，必须固定 Git 提交。

## 结论

可以复用 `V1.40.0` MIT 提交中的代码，但必须遵守以下边界。当前 GPL `main` 和后续未固定版本不能混入 Marmot。Mole 代码只作为：

- Infrastructure 层的扫描实现基础；
- 扫描策略和行为对比基线。

复用时必须保留 MIT copyright 和 permission notice，记录来源提交，并审查该版本的依赖、资源和子目录许可证。不得复制当前 `main` 或之后的 GPL 代码。

## 抽取验证

在固定提交的临时副本中，`cmd/analyze` 上游测试通过。将 `scanner.go`、`constants.go` 和
`heap.go` 抽到独立包后，补充 Marmot 自己的节点类型，并以缓存/路径校验函数作为端口，
即可编译并通过硬链接去重扫描测试。由此确认扫描算法可抽取，但 Mole 的 `package main`、
TUI、JSON、`du`/`mdfind` 编排和缓存格式不能直接作为 Marmot 模块。

## 对 SDD 的影响

即使复用代码，Mole 仍留在 Infrastructure 层，Domain 和 Application 不依赖 Mole。扫描主协议、快照模型和清理模型由 Marmot 自己定义。
