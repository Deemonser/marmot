package recommendation

import "strings"

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
	// Pattern is matched against the home-relative path. "*" matches one
	// segment; a leading "**/" matches the segment anywhere in the path.
	Pattern      string
	Recovery     Recovery
	Risk         Risk
	WhatBreaks   string
	HowToRestore string
}

// Catalog is ordered: the first match wins, so the specific entries come before
// the general ones. `~/Library/Caches/Homebrew` must be read as the Homebrew
// download cache and not as the generic user cache, because the two have
// different answers to "how do I get it back".
var Catalog = []Rule{
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
		Pattern: "**/target/debug", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次 cargo build 全量重编，第一次会很慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Rust 发布产物", Category: "编译产物",
		Pattern: "**/target/release", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次 cargo build --release 全量重编。已分发的二进制不受影响。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Android 构建中间产物", Category: "编译产物",
		Pattern: "**/build/intermediates", Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "下次 Gradle 构建全量重建该模块。",
		HowToRestore: "自动重建。",
	},
}

// Match returns the first rule matching an absolute path, or nil. The path is
// reduced to home-relative first: the catalog is written against `~`, and both
// spellings of a home folder have to reach the same answer -- the firmlinked
// /System/Volumes/Data/Users/alice is the same directory as /Users/alice, and a
// rule that fires on one but not the other would depend on which spelling the
// scan happened to walk. cleanup.DeleteBlock handles the same pair for the same
// reason.
func Match(absolutePath string) *Rule {
	relative, ok := homeRelative(absolutePath)
	if !ok {
		return nil
	}
	for index := range Catalog {
		if matchPattern(Catalog[index].Pattern, relative) {
			return &Catalog[index]
		}
	}
	return nil
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
	if trimmed, found := strings.CutPrefix(pattern, "**/"); found {
		for _, segment := range strings.Split(relative, "/") {
			if segment == trimmed {
				return true
			}
		}
		// `**/target/debug` has to match the pair, not one segment.
		return containsSequence(strings.Split(relative, "/"), strings.Split(trimmed, "/"))
	}
	patternSegments := strings.Split(strings.TrimSuffix(pattern, "/*"), "/")
	pathSegments := strings.Split(relative, "/")
	if len(pathSegments) < len(patternSegments) {
		return false
	}
	for index, segment := range patternSegments {
		if segment == "*" {
			continue
		}
		if segment != pathSegments[index] {
			return false
		}
	}
	return true
}

func containsSequence(haystack, needle []string) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for offset := range needle {
			if haystack[start+offset] != needle[offset] {
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
