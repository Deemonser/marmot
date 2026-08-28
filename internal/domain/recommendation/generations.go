package recommendation

import "strings"

// Generational cleanup: the precise half of "there is definitely junk in that
// cache". A tool that keeps one directory per version accumulates the versions
// you have stopped using, and those are dead weight that no path pattern can
// name -- which one is dead depends on which one you still build with.
//
// The obvious rule, keep the newest and offer the rest, is wrong on real data.
// Measured here: ~/.nvm/versions/node holds v24.19.0 touched 17 days ago and
// v22.15.1 touched 25 days ago. Both are live -- keeping several Node versions in
// rotation is the entire purpose of nvm -- and recency alone would have offered
// the one that happened to be touched second. So a generation is only offered
// when it is meaningfully older than the newest one kept, not merely older.
//
// Recency here is the newest mtime anywhere in the generation's subtree, which
// approximates "when it was last used" for anything a tool writes into. It is a
// weaker signal for a read-only distribution, where mtime says when it was
// installed rather than when it was last read; the gap requirement is what keeps
// that imprecision from costing anything.
type GenerationRule struct {
	Name     string
	Category string
	// Parent is the home-relative pattern of the directory whose children are
	// generations of one thing.
	Parent string
	// KeepNewest generations are never offered, however old they are.
	KeepNewest int
	// MinGapDays is how much older than the newest kept generation a candidate
	// must be before it is offered.
	MinGapDays int64
	// NotGenerations names children that live in the same directory without being
	// versions of it. Segment patterns, so `modules-*` covers the numbering.
	NotGenerations []string
	Recovery       Recovery
	Risk           Risk
	WhatBreaks     string
	HowToRestore   string
}

// Generations is the catalog of version-partitioned directories.
var Generations = []GenerationRule{
	{
		Name: "旧版 Rust 工具链", Category: "旧代际",
		Parent: ".rustup/toolchains", KeepNewest: 1, MinGapDays: 30,
		Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "如果某个项目用 rust-toolchain 文件锁定了这个版本，下次构建会重新下载它。",
		HowToRestore: "rustup toolchain install <版本>。",
	},
	{
		Name: "旧版 Gradle 发行版", Category: "旧代际",
		Parent: ".gradle/wrapper/dists", KeepNewest: 1, MinGapDays: 30,
		NotGenerations: []string{"CACHEDIR.TAG"},
		Recovery:       RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "使用该 Gradle 版本的项目，下次构建会由 wrapper 重新下载它。",
		HowToRestore: "无需操作，Gradle wrapper 自动重新下载。",
	},
	{
		Name: "旧版 Gradle 缓存", Category: "旧代际",
		Parent: ".gradle/caches", KeepNewest: 1, MinGapDays: 30,
		// These share the directory with the version folders without being
		// versions of it, and deleting them is a different decision entirely.
		NotGenerations: []string{
			"CACHEDIR.TAG", "build-cache-*", "modules-*", "journal-*", "jars-*",
			"transforms-*", "kotlin-dsl", "*.lock",
		},
		Recovery: RecoveryRedownloadable, Risk: RiskSafe,
		WhatBreaks:   "该 Gradle 版本的编译缓存消失。若某个项目仍用这个版本，第一次构建会明显变慢。",
		HowToRestore: "无需操作，构建时重新生成。",
	},
	{
		Name: "旧版 Node", Category: "旧代际",
		// A deliberately large gap: nvm exists to keep versions in rotation, and
		// two of them a fortnight apart are both in use.
		Parent: ".nvm/versions/node", KeepNewest: 1, MinGapDays: 90,
		Recovery: RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "该 Node 版本及其全局安装的包消失。锁定这个版本的项目需要重新安装它。",
		HowToRestore: "nvm install <版本>，全局包需要重新安装。",
	},
	{
		Name: "旧版 iOS 设备支持", Category: "旧代际",
		// Two, because connecting a second device is ordinary.
		Parent: "Library/Developer/Xcode/iOS DeviceSupport", KeepNewest: 2, MinGapDays: 60,
		Recovery: RecoveryRegenerable, Risk: RiskSafe,
		WhatBreaks:   "对应 iOS 版本的设备下次连接时要重新拉取符号，需要几分钟。",
		HowToRestore: "设备再次连接时自动重建。",
	},
	{
		Name: "旧版 Kotlin/Native 发行版", Category: "旧代际",
		Parent: ".konan", KeepNewest: 1, MinGapDays: 30,
		NotGenerations: []string{"dependencies", "cache", "*.lock"},
		Recovery:       RecoveryRedownloadable, Risk: RiskReview,
		WhatBreaks:   "锁定该 Kotlin 版本的项目下次构建会重新下载整个发行版（数百 MB）。",
		HowToRestore: "构建时自动重新下载。",
	},
}

// IsGenerationParent returns the rule for a directory whose children are
// generations, or nil.
func IsGenerationParent(absolutePath string) *GenerationRule {
	relative, ok := homeRelative(absolutePath)
	if !ok {
		return nil
	}
	for index := range Generations {
		if matchPattern(Generations[index].Parent, relative) &&
			countSegments(relative) == countSegments(Generations[index].Parent) {
			return &Generations[index]
		}
	}
	return nil
}

// IsGeneration reports whether a child of the parent is a version of it rather
// than something else living in the same directory.
func (g GenerationRule) IsGeneration(name string) bool {
	for _, excluded := range g.NotGenerations {
		if segmentMatches(excluded, name) {
			return false
		}
	}
	return true
}

// Offerable decides which generations may be suggested. Sorted newest-first by
// the caller; ages are days since each generation was last written.
//
// Returns the indices of the generations that may be offered.
func (g GenerationRule) Offerable(ageDaysNewestFirst []int64) []int {
	if len(ageDaysNewestFirst) <= g.KeepNewest {
		return nil
	}
	newestKept := ageDaysNewestFirst[g.KeepNewest-1]
	if g.KeepNewest <= 0 {
		newestKept = ageDaysNewestFirst[0]
	}
	offerable := make([]int, 0, len(ageDaysNewestFirst))
	for index := g.KeepNewest; index < len(ageDaysNewestFirst); index++ {
		if ageDaysNewestFirst[index]-newestKept >= g.MinGapDays {
			offerable = append(offerable, index)
		}
	}
	return offerable
}

func countSegments(value string) int {
	return len(strings.Split(strings.Trim(value, "/"), "/"))
}
