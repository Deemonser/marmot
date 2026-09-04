# 第三方代码声明

## Mole

- 项目：[tw93/Mole](https://github.com/tw93/Mole)
- 复用版本：`V1.40.0`
- 固定提交：`c3ef1f50ea7a2490957dd8cbffadfcc350dd1182`
- 许可证：MIT
- 许可证文件：[V1.40.0/LICENSE](https://raw.githubusercontent.com/tw93/Mole/V1.40.0/LICENSE)

Marmot 只允许复用上述固定提交中的代码。当前 `main` 及之后的 GPL-3.0-or-later 版本不属于本项目的复用范围。

复用代码随发行包分发时，必须同时保留原版权声明和 MIT permission notice。新增第三方依赖或资源时，必须在这里补充来源和许可证。

## 直接依赖

### Wails

- 项目：[wailsapp/wails](https://github.com/wailsapp/wails)
- 固定版本：`v3.0.0-beta.15`
- 许可证：MIT
- 本机许可证文件：Go module cache 中的 `github.com/wailsapp/wails/v3@v3.0.0-beta.15/LICENSE`

前端的 `@wailsio/runtime` 必须固定在同一个 beta 号上，Go 端和 JS 端错版会导致绑定和事件不匹配。

### golang.org/x/sys

- 项目：[golang/sys](https://github.com/golang/sys)
- 固定版本：`v0.47.0`
- 许可证：BSD-3-Clause
- 本机许可证文件：Go module cache 中的 `golang.org/x/sys@v0.47.0/LICENSE`

正式发行前必须根据最终 `go.mod` 和前端 lockfile 生成完整依赖清单，不以本节的直接依赖列表替代传递依赖审查。
