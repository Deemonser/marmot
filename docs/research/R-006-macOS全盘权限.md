# R-006 macOS 全盘权限

状态：部分完成（ad-hoc Wails 包验证；真实 Developer ID/TCC 授权仍待发布环境）

## 问题

Wails 打包后的 macOS 应用如何获得全盘扫描所需权限，并清楚区分可访问、部分可访问和不可访问结果？

## 验证场景

- 未授予 Full Disk Access；
- 授予后重启应用；
- `/System`、`/Library`、其他用户目录和受保护目录；
- iCloud/FileProvider 占位文件；
- 本地 APFS 快照；
- 直装签名版本与 App Store 沙盒版本；
- 是否需要管理员/root 模式，以及其签名和分发影响。

## 本机验证

- Wails `v3.0.0-beta.9` 可以生成嵌入前端资源的 macOS `.app`；
- `.app` 的 bundle identifier、版本、图标资源和 `Info.plist` 均可生成；
- `wails3 package` 默认生成可验证的 ad-hoc 签名包；
- 当前机器 `security find-identity -v -p codesigning` 返回 0 个有效身份，无法验证
  Developer ID、Team ID、公证和真实 TCC 授权流程；
- `diskutil info /` 显示当前根路径位于 APFS Volume Snapshot，说明系统快照不能当作普通目录处理。

## 锁定规则

- 第一阶段只支持 Wails 桌面应用，不通过隐式 root 提权扩大扫描范围；
- 每个卷和目录记录 `accessible`、`partial` 或 `inaccessible`，权限错误不能转成空目录；
- 生产通信仍使用 Wails 类型化绑定和事件，不启动本地 HTTP 服务；
- 正式发行前必须固定签名身份、bundle identifier、Team ID、entitlements、直装/沙盒分发方式，
  并在未授权与已授权状态下重复 smoke test；
- 在真实签名和权限验证完成前，不宣称“已验证全盘访问”。

## 剩余限制

权限状态模型和目录覆盖范围已进入 DDD/SDD；Developer ID/TCC smoke test、直装与 App Store
选择仍需在具备签名身份的发布环境完成。第一阶段不支持管理员/root 扫描。
