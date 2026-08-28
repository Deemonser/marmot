package recommendation

import (
	"fmt"
	"strings"
)

// Rule is one known-cleanable location. The catalog is deliberately not the
// product's ceiling -- R-062 §3.4 measured a catalog of this shape reaching only
// 36.6% of the bytes on the reference machine, and 32.46 GB of that came from a
// single entry. It exists for three jobs at once:
//
//  1. a floor: the well-known wins are found even if the advisor is unavailable,
//     misconfigured or wrong;
//  2. evidence: a matched node goes into the pack labelled, so the advisor
//     spends its attention on what the catalog does NOT know;
//  3. a vocabulary: the four categories the catalog cannot express -- unknown
//     app caches, in-project build output, git bloat, cold data -- are exactly
//     what the advisor is asked for.
//
// WhatBreaks and HowToRestore are required on every rule. A suggestion a person
// cannot evaluate is not a suggestion, and "what happens if I say yes" is the
// question every rule-based cleaner leaves unanswered.
type Rule struct {
	Name     string
	Category string
	// Pattern is matched against the home-relative path. A leading "**/" matches
	// the following segment sequence at any depth; "*" matches one whole
	// segment; a trailing "*" on a segment matches a prefix.
	Pattern string
	// MinAgeDays makes a rule fire only on objects whose newest content is at
	// least this old. Zero means no age condition.
	//
	// R-062 §3.4 claimed rules structurally cannot express staleness, and that
	// was wrong: what it actually measured was that the hand-written catalog had
	// no age condition, not that a catalog cannot have one.
	MinAgeDays int64
	// MinProjectIdleDays and MaxProjectIdleDays condition on how long the
	// surrounding project's *source* has been untouched, which is a different
	// question from how old the artifact is.
	//
	// Recoverability is not the standard a person actually applies; disruption
	// is. Every file in an active project's build cache is recoverable, and
	// deleting it still costs the rebuild they were in the middle of. The same
	// bytes in a project dormant for two years cost nothing. Zero means no
	// condition; a candidate outside any recognised project satisfies neither.
	MinProjectIdleDays int64
	MaxProjectIdleDays int64
	// ProjectSensitive marks an artifact whose disruption depends on whether the
	// surrounding project is being worked on, rather than on the artifact itself.
	// The risk is then adjusted from the project's source activity instead of the
	// catalog carrying a cold and a warm copy of every such rule.
	ProjectSensitive bool
	Recovery         Recovery
	Risk             Risk
	WhatBreaks       string
	HowToRestore     string
}

// Catalog is ordered: the first match wins, so the specific entries come before
// the general ones. `~/Library/Caches/Homebrew` must be read as the Homebrew
// download cache and not as the generic user cache, because the two have
// different answers to "how do I get it back".
var Catalog = []Rule{
	// --- Promoted from advisor findings (R-063 §5). Each recovery claim below is
	// written from a mechanism that was read out of the tool's own source or
	// verified on disk, not from the model's wording. Anything an advisor finds
	// and a person confirms belongs here: the catalog is the part that never
	// varies between runs, and it grows from evidence rather than from guessing
	// what software people have installed.
	// --- Application updaters and IDE caches, promoted after the two largest
	// remaining "用户缓存 / review" items turned out to be one downloaded
	// installer nobody cleaned up and one IDE index. Both were only vague
	// because no rule named them.
	{
		Name: "应用更新包残留", Category: "更新器残留",
		Pattern: "Library/Caches/*.ShipIt", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "没有影响。这是已下载并安装完的应用更新包，Squirrel 装完之后没有清理。",
		HowToRestore: "无需恢复；下次更新会重新下载。",
	},
	{
		Name: "IDE 索引缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/Google/AndroidStudio*/index", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重建索引，期间代码跳转与搜索暂时不可用，大项目可能要数分钟。",
		HowToRestore: "无需操作，打开项目时自动重建。",
	},
	{
		Name: "IDE 编译缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/Google/AndroidStudio*/caches", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重新分析工程，第一次打开变慢。",
		HowToRestore: "无需操作，自动重建。",
	},
	{
		Name: "JetBrains 索引缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/JetBrains/*/index", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重建索引，大项目可能要数分钟。",
		HowToRestore: "无需操作，自动重建。",
	},
	{
		Name: "JetBrains 编译缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/JetBrains/*/caches", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重新分析工程，第一次打开变慢。",
		HowToRestore: "无需操作，自动重建。",
	},
	{
		Name: "旧版本 IDE 配置", Category: "旧版本残留",
		Pattern: "Library/Application Support/Google/AndroidStudio*", MinAgeDays: 180,
		Recovery: RecoveryIrreplaceable, Risk: RiskReview,
		WhatBreaks:   "该版本 IDE 的设置、快捷键和插件配置会丢失。若之后回退到这个版本，需要重新配置。",
		HowToRestore: "无法恢复配置本身；重新安装该版本后需要重新设置。",
	},
	{
		Name: "浏览器扩展", Category: "浏览器扩展",
		Pattern:  "Library/Application Support/Google/Chrome/*/Extensions",
		Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "已安装的扩展会消失，需要从商店重新安装。扩展的设置数据保存在别处，重装后多数能恢复。",
		HowToRestore: "在 Chrome 网上应用店重新安装各扩展。",
	},

	// --- Browsers. The whole point is precision: the generic user-cache rule
	// already matches these paths and answers "个别应用会丢失登录态或离线内容",
	// which is the vague warning that stops anyone acting on 1.7 GB. On macOS a
	// Chromium browser keeps its HTTP cache under ~/Library/Caches and its
	// profile -- cookies, saved logins, site storage -- under Application
	// Support, so "登录态不在这里" is a statement that can be made confidently.
	// Verified on this machine: Caches/Google/Chrome/Default/Cache is 1058 MB and
	// Code Cache 632 MB, while Cookies and Login Data live elsewhere entirely.
	{
		Name: "Chrome 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Google/Chrome/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源，短时间内浏览稍慢。登录状态、书签、历史、扩展都不在这里，不受影响。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Chromium 系网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Chromium/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录状态与书签不在这里。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Brave 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/BraveSoftware/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录状态与书签不在这里。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Edge 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Microsoft Edge/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录状态与书签不在这里。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Safari 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/com.apple.Safari/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。Safari 的 Cookie 与登录信息保存在别处，不受影响。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Firefox 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Firefox/Profiles/*/cache2", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录信息在 profile 目录中，不受影响。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "浏览器离线资源缓存", Category: "浏览器缓存",
		Pattern: "**/Service Worker/CacheStorage", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "支持离线使用的网页需要联网重新加载一次；Service Worker 的注册信息不在这里，不会被注销。",
		HowToRestore: "下次访问该站点时自动重建。",
	},
	{
		Name: "浏览器脚本缓存", Category: "浏览器缓存",
		Pattern: "**/Service Worker/ScriptCache", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "对应站点的 Service Worker 脚本要重新下载一次。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器压缩字典", Category: "浏览器缓存",
		Pattern: "**/Shared Dictionary", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "使用共享字典压缩的站点，首次访问传输量略增。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器图形缓存", Category: "浏览器缓存",
		Pattern: "**/GPUCache", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "着色器需要重新编译一次，个别页面首次渲染略慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器 WebGPU 缓存", Category: "浏览器缓存",
		Pattern: "**/DawnWebGPUCache", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "使用 WebGPU 的页面首次渲染略慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器优化提示缓存", Category: "浏览器缓存",
		Pattern: "**/optimization_guide_hint_cache_store", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "没有可感知的影响。",
		HowToRestore: "自动重建。",
	},

	{
		Name: "Flutter 引擎产物", Category: "SDK 缓存",
		Pattern: "**/flutter/bin/cache/artifacts/engine", Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "下次 Flutter 构建要重新下载引擎（GB 级），离线环境会直接失败。",
		HowToRestore: "删除整个 engine 目录后，flutter precache 或任意构建会重新下载；只删内部子目录不会触发，因为 Flutter 只检查根目录与 stamp。",
	},
	{
		Name: "Kotlin/Native 平台库", Category: "编译器发行版",
		Pattern: ".konan/*/klib/platform", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次 Kotlin/Native 构建会先花时间重新生成平台库，该次构建明显变慢。",
		HowToRestore: "无需手动操作：构建时逐个检测缺失的 platform lib 并从 .def 文件本地重新生成。",
	},
	{
		Name: "Rust 离线文档", Category: "离线文档",
		Pattern: ".rustup/toolchains/*/share/doc", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "本地 rustup doc 打不开，查文档需要联网。编译不受影响。",
		HowToRestore: "rustup component add rust-docs --toolchain <该工具链名>。",
	},
	{
		Name: "Gradle JDK 缓存", Category: "工具链缓存",
		Pattern: ".gradle/jdks/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "需要该 JDK 的 Gradle 构建会先重新下载它。",
		HowToRestore: "Gradle 在需要时按 toolchain 配置自动重新下载。",
	},
	{
		Name: "Gradle 执行历史", Category: "构建缓存",
		Pattern: "**/executionHistory", ProjectSensitive: true, Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "该项目失去增量构建信息，下次构建会重跑部分任务，一次性变慢。",
		HowToRestore: "无需手动操作，Gradle 下次构建自行重建。",
	},
	{
		Name: "Android 构建输出", Category: "构建产物",
		Pattern: "**/build/outputs/apk", ProjectSensitive: true, Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "已构建的 APK/AAB 消失。若某个是已分发的版本，重新构建不会得到逐字节相同的产物。",
		HowToRestore: "重新执行对应的 assemble/bundle 任务。",
	},
	{
		Name: "Rust 交叉编译产物", Category: "编译产物",
		Pattern: "**/target/*/debug", ProjectSensitive: true, Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "该目标平台下次 cargo 构建全量重编。",
		HowToRestore: "无需手动操作，cargo 自行重建。",
	},
	{
		Name: "git 残留临时包", Category: "残留文件",
		Pattern: "**/.git/objects/pack/tmp_pack_*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "没有影响。这是 repack 中断后遗留的临时文件，git 不会使用它。",
		HowToRestore: "无需恢复：它不是版本库内容，删除不丢任何提交。",
	},
	{
		Name: "代码索引数据库", Category: "工具索引",
		Pattern: "**/.codegraph", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "该项目的代码图谱与符号搜索不可用，重建索引需要时间。源码不受影响。",
		HowToRestore: "重新运行索引命令。",
	},
	{
		Name: "pnpm 内容存储", Category: "包管理器缓存",
		Pattern: "Library/pnpm/store/*", Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "新的安装要重新下载。若某些项目的 node_modules 依赖存储中的链接，那些项目需要重新 install。",
		HowToRestore: "在相关项目执行 pnpm install。",
	},
	{
		Name: "Google 更新缓存", Category: "更新器缓存",
		Pattern: "Library/Application Support/Google/GoogleUpdater/crx_cache/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "不影响已安装的 Google 软件，只是待安装的更新包副本。",
		HowToRestore: "更新时自动重新下载。",
	},
	{
		Name: "macOS 动态壁纸", Category: "系统媒体缓存",
		Pattern: "Library/Application Support/com.apple.wallpaper/aerials/videos/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "该航拍壁纸暂时不可用。",
		HowToRestore: "在壁纸设置中重新选择该素材，系统会重新下载。",
	},

	{
		Name: "Homebrew 下载缓存", Category: "包管理器缓存",
		Pattern: "Library/Caches/Homebrew/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "不影响已安装的软件，只是下载过的安装包副本。",
		HowToRestore: "下次 brew 安装或升级时自动重新下载。",
	},
	{
		Name: "Go 构建缓存", Category: "编译产物",
		Pattern: "Library/Caches/go-build/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下一次 go build 会全量重编，明显变慢一次。",
		HowToRestore: "无需操作，编译时自动重建。",
	},
	{
		Name: "pip 缓存", Category: "包管理器缓存",
		Pattern: "Library/Caches/pip/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "不影响已安装的包，只是 wheel 下载副本。",
		HowToRestore: "下次 pip install 时重新下载。",
	},
	{
		Name: "CocoaPods 缓存", Category: "包管理器缓存",
		Pattern: "Library/Caches/CocoaPods/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "不影响已集成的 Pods，只是缓存副本。",
		HowToRestore: "下次 pod install 时重新下载。",
	},
	{
		Name: "用户缓存", Category: "应用缓存",
		Pattern: "Library/Caches/*", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "对应应用下次启动会慢一些，个别应用会丢失登录态或离线内容。",
		HowToRestore: "应用自行重建；登录态需要重新登录。",
	},
	{
		Name: "沙盒应用缓存", Category: "应用缓存",
		Pattern: "Library/Containers/*/Data/Library/Caches/*", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "对应应用下次启动会慢一些，可能需要重新下载已缓存的内容。",
		HowToRestore: "应用自行重建。",
	},
	{
		Name: "应用组缓存", Category: "应用缓存",
		Pattern: "Library/Group Containers/*/Library/Caches/*", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "同一应用组内的应用下次启动会慢一些。",
		HowToRestore: "应用自行重建。",
	},
	{
		Name: "用户日志", Category: "日志",
		Pattern: "Library/Logs/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "失去历史诊断信息；如果正在排查某个应用的问题，先别删。",
		HowToRestore: "无法恢复，但会继续产生新日志。",
	},
	{
		Name: "废纸篓", Category: "废纸篓",
		Pattern: ".Trash/*", Recovery: RecoveryIrreplaceable, Risk: RiskReview,
		WhatBreaks:   "废纸篓里的东西会真正消失。",
		HowToRestore: "无法恢复。删除前请确认里面没有还想要的东西。",
	},
	{
		Name: "Xcode DerivedData", Category: "编译产物",
		Pattern: "Library/Developer/Xcode/DerivedData/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次打开工程会重新索引并全量编译，第一次会明显变慢。",
		HowToRestore: "Xcode 自动重建。",
	},
	{
		Name: "Xcode 归档", Category: "构建归档",
		Pattern: "Library/Developer/Xcode/Archives/*", Recovery: RecoveryIrreplaceable, Risk: RiskRisky,
		WhatBreaks:   "已上架版本的符号表会丢失，之后无法符号化这些版本的崩溃日志。",
		HowToRestore: "无法恢复，除非重新用完全相同的源码和工具链构建。",
	},
	{
		Name: "Xcode 设备支持文件", Category: "开发工具支持文件",
		Pattern: "Library/Developer/Xcode/iOS DeviceSupport/*", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "对应 iOS 版本的设备下次连接时要重新拉取符号，需要几分钟。",
		HowToRestore: "设备再次连接时自动重建。",
	},
	{
		Name: "iOS 模拟器", Category: "开发工具支持文件",
		Pattern: "Library/Developer/CoreSimulator/*", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "模拟器里已安装的 App 和数据会消失，运行时需要重新下载。",
		HowToRestore: "Xcode 重新创建模拟器并下载运行时。",
	},
	{
		Name: "iOS 设备备份", Category: "设备备份",
		Pattern: "Library/Application Support/MobileSync/Backup/*", Recovery: RecoveryIrreplaceable, Risk: RiskRisky,
		WhatBreaks:   "这是 iPhone/iPad 的本地完整备份。删掉后无法从本机恢复设备。",
		HowToRestore: "无法恢复。只有在确认已有 iCloud 备份或不再需要时才删。",
	},
	{
		Name: "Docker 数据", Category: "虚拟机磁盘",
		Pattern: "Library/Containers/com.docker.docker/Data/*", Recovery: RecoveryIrreplaceable, Risk: RiskRisky,
		WhatBreaks:   "所有本地镜像、容器和卷会消失，包括未推送的镜像和容器内数据。",
		HowToRestore: "镜像可重新拉取；卷里的数据无法恢复。建议改用 docker system prune。",
	},
	{
		Name: "Gradle 缓存", Category: "包管理器缓存",
		Pattern: ".gradle/caches/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "下一次构建要重新下载依赖并重建转换产物，第一次会很慢。",
		HowToRestore: "构建时自动重新下载。",
	},
	{
		Name: "Maven 本地仓库", Category: "包管理器缓存",
		Pattern: ".m2/repository/*", Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "下次构建要重新下载全部依赖。若有本地 install 的私有构件且没有备份，会丢失。",
		HowToRestore: "公共依赖自动重新下载；本地 install 的构件需要重新构建。",
	},
	{
		Name: "Cargo 注册表缓存", Category: "包管理器缓存",
		Pattern: ".cargo/registry/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "下次构建要重新下载 crate 源码。",
		HowToRestore: "cargo 自动重新下载。",
	},
	{
		Name: "Go 模块缓存", Category: "包管理器缓存",
		Pattern: "go/pkg/mod/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "下次构建要重新下载模块。",
		HowToRestore: "go 自动重新下载。",
	},
	{
		Name: "npm 缓存", Category: "包管理器缓存",
		Pattern: ".npm/_cacache/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "下次 npm install 要重新下载。",
		HowToRestore: "自动重新下载。",
	},
	{
		Name: "yarn 缓存", Category: "包管理器缓存",
		Pattern: ".yarn/cache/*", Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "下次 yarn install 要重新下载。",
		HowToRestore: "自动重新下载。",
	},
	{
		Name: "pnpm 存储", Category: "包管理器缓存",
		Pattern: ".pnpm-store/*", Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "pnpm 项目的 node_modules 是指向这里的硬链接，删除后现有项目会失效。",
		HowToRestore: "在每个项目重新执行 pnpm install。",
	},
	{
		Name: "node_modules", Category: "依赖目录",
		Pattern: "**/node_modules", Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "对应项目在重新安装依赖前无法构建或运行。",
		HowToRestore: "在项目目录执行 npm/yarn/pnpm install。",
	},
	{
		Name: "Python 虚拟环境", Category: "依赖目录",
		Pattern: "**/.venv", Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "对应项目的虚拟环境消失，重建前无法运行。",
		HowToRestore: "重新 python -m venv 并按依赖文件安装。若没有依赖清单则难以还原。",
	},
	{
		Name: "Python 字节码缓存", Category: "编译产物",
		Pattern: "**/__pycache__", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次运行时重新编译，几乎无感。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Rust 构建产物", Category: "编译产物",
		Pattern: "**/target/debug", ProjectSensitive: true, Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次 cargo build 全量重编，第一次会很慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Rust 发布产物", Category: "编译产物",
		Pattern: "**/target/release", ProjectSensitive: true, Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次 cargo build --release 全量重编。已分发的二进制不受影响。",
		HowToRestore: "自动重建。",
	},
	// One rule per artifact, with ProjectSensitive doing the work a cold copy and
	// a warm copy of each would otherwise duplicate. The distinction it draws is
	// the one that matters: identical bytes, and deleting them costs a rebuild
	// the user is in the middle of, or nothing at all.
	{
		Name: "Android 构建中间产物", Category: "编译产物", ProjectSensitive: true,
		Pattern: "**/build/intermediates", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "下次 Gradle 构建全量重建该模块。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Android 转换产物", Category: "编译产物", ProjectSensitive: true,
		Pattern: "**/build/.transforms", Recovery: RecoveryRegenerable, Risk: RiskReview,
		WhatBreaks:   "下次 Gradle 构建重新生成该模块的转换产物。",
		HowToRestore: "自动重建。",
	},
}

// MatchContext is what a rule is evaluated against. A struct rather than a
// growing parameter list, because the interesting conditions are no longer only
// about the path.
type MatchContext struct {
	Path string
	// AgeDays is the age of the newest content in the object's own subtree.
	AgeDays int64
	// ProjectIdleDays is how long the surrounding project's source has been
	// untouched. Negative means the object is not inside a recognised project,
	// which satisfies no project condition either way.
	ProjectIdleDays int64
}

// NoProject is the ProjectIdleDays value for an object outside any recognised
// project.
const NoProject = int64(-1)

// Match returns the first rule whose pattern and conditions the context
// satisfies, or nil. The path is
// reduced to home-relative first: the catalog is written against `~`, and both
// spellings of a home folder have to reach the same answer -- the firmlinked
// /System/Volumes/Data/Users/alice is the same directory as /Users/alice, and a
// rule that fires on one but not the other would depend on which spelling the
// scan happened to walk. cleanup.DeleteBlock handles the same pair for the same
// reason.
func Match(context MatchContext) *Rule {
	relative, ok := homeRelative(context.Path)
	if !ok {
		return nil
	}
	for index := range Catalog {
		rule := &Catalog[index]
		if !rule.conditionsMet(context) {
			continue
		}
		if matchPattern(rule.Pattern, relative) {
			return rule
		}
	}
	return nil
}

// Specificity is how many literal segments a pattern pins down. It decides which
// suggestion survives when a specific rule and a generic one both cover the same
// bytes: "Chrome 网页缓存，登录态不在这里" is worth more than "用户缓存，个别应用会
// 丢失登录态" over a larger number, because only one of them can be acted on.
//
// Measured: the generic Library/Caches rule matched the whole 8.1 GB folder, the
// outermost-wins overlap pass then discarded the 1.7 GB Chrome finding inside
// it, and the user was left with the vague warning that stops anyone acting.
func (r Rule) Specificity() int {
	count := 0
	for _, segment := range strings.Split(strings.TrimPrefix(r.Pattern, "**/"), "/") {
		if segment != "" && segment != "*" && !strings.HasSuffix(segment, "*") {
			count++
		}
	}
	return count
}

func (r Rule) conditionsMet(context MatchContext) bool {
	if r.MinAgeDays > 0 && context.AgeDays < r.MinAgeDays {
		return false
	}
	if r.MinProjectIdleDays > 0 || r.MaxProjectIdleDays > 0 {
		// An object outside any recognised project has no activity signal, so a
		// project condition cannot be satisfied — in either direction. Guessing
		// would be how an active project's cache gets called cold.
		if context.ProjectIdleDays < 0 {
			return false
		}
		if r.MinProjectIdleDays > 0 && context.ProjectIdleDays < r.MinProjectIdleDays {
			return false
		}
		if r.MaxProjectIdleDays > 0 && context.ProjectIdleDays > r.MaxProjectIdleDays {
			return false
		}
	}
	return true
}

const dataVolumePrefix = "/System/Volumes/Data"

// homeRelative strips /Users/<account> or its firmlinked spelling. It returns
// false for anything outside a home folder: the catalog has no entries there,
// and guessing would be worse than not matching.
func homeRelative(absolutePath string) (string, bool) {
	clean := strings.TrimSuffix(absolutePath, "/")
	clean = strings.TrimPrefix(clean, dataVolumePrefix)
	if !strings.HasPrefix(clean, "/Users/") {
		return "", false
	}
	rest := clean[len("/Users/"):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", false
	}
	account := rest[:slash]
	// "Shared" is not an account.
	if account == "" || account == "Shared" {
		return "", false
	}
	return rest[slash+1:], true
}

// matchPattern supports one leading "**/" and "*" as a single-segment wildcard.
// A pattern matches a path that is the pattern itself or anything beneath it, so
// `Library/Caches/*` matches `Library/Caches/foo` and `Library/Caches` alike --
// the whole cache directory is as legitimate a suggestion as one app's slice of
// it, and which one is offered depends on where the size floor cut.
func matchPattern(pattern, relative string) bool {
	pathSegments := strings.Split(relative, "/")
	if trimmed, found := strings.CutPrefix(pattern, "**/"); found {
		return containsSequence(pathSegments, strings.Split(trimmed, "/"))
	}
	patternSegments := strings.Split(strings.TrimSuffix(pattern, "/*"), "/")
	if len(pathSegments) < len(patternSegments) {
		return false
	}
	for index, segment := range patternSegments {
		if !segmentMatches(segment, pathSegments[index]) {
			return false
		}
	}
	return true
}

// segmentMatches handles the wildcards a segment may carry: "*" alone is any
// whole segment, a trailing "*" is a prefix -- which names an artifact like
// `tmp_pack_Z8vjYY` or a versioned directory like `AndroidStudio2026.1.3` -- and
// a leading "*" is a suffix, which is how a convention-named directory like
// `com.microsoft.VSCode.ShipIt` is identified across every app that uses it.
func segmentMatches(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	if suffix, found := strings.CutPrefix(pattern, "*"); found {
		return strings.HasSuffix(segment, suffix)
	}
	if prefix, found := strings.CutSuffix(pattern, "*"); found {
		return strings.HasPrefix(segment, prefix)
	}
	return pattern == segment
}

func containsSequence(haystack, needle []string) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for offset := range needle {
			if !segmentMatches(needle[offset], haystack[start+offset]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// Project activity thresholds. Not tuned against anything -- they are the
// obvious round numbers, and R-063 records them as unvalidated.
const (
	// ProjectActiveDays: source touched this recently means the work is live and
	// its build cache is about to be needed again.
	ProjectActiveDays = 30
	// ProjectDormantDays: nothing touched for this long means deleting the build
	// cache costs nothing anyone will feel.
	ProjectDormantDays = 180
)

// AdjustForProjectActivity rewrites the risk of a project-sensitive suggestion
// from how live the surrounding work is. Recoverability is unchanged: the bytes
// come back either way. What changes is whether their absence is felt.
//
// Returns the risk to use, plus a sentence to put in front of what_breaks, or ""
// when nothing needed saying.
func AdjustForProjectActivity(declared Risk, idleDays int64) (Risk, string) {
	if idleDays < 0 {
		return declared, ""
	}
	switch {
	case idleDays <= ProjectActiveDays:
		if declared == RiskSafe {
			declared = RiskReview
		}
		return declared, fmt.Sprintf("这个项目 %d 天前还改过源码，正在使用中：删掉之后下一次构建要重新下载或重新编译。", idleDays)
	case idleDays >= ProjectDormantDays:
		if declared == RiskReview {
			declared = RiskSafe
		}
		return declared, fmt.Sprintf("这个项目的源码已经 %d 天没有改动。", idleDays)
	}
	return declared, ""
}
