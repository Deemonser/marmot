package recommendation

import (
	"fmt"
	"path/filepath"
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
	// Manual marks a finding this tool must not act on itself. The paths are
	// root-owned, so os.RemoveAll would fail, and an app that asks for admin
	// rights to delete files has a blast radius in a different league from one
	// that does not (ADR-0065). The finding is still worth making -- the user
	// cannot act on what they were never told about -- so it is shown with the
	// exact command and cannot be staged.
	Manual bool
	// Command is what to run in a terminal. Required when Manual is set.
	Command string

	Name     string
	Category string
	// Pattern is matched against the home-relative path. A leading "**/" matches
	// the following segment sequence at any depth; "*" matches one whole
	// segment; a trailing "*" on a segment matches a prefix.
	Pattern string
	// Origin is where this rule came from. See the Origin type; the zero value is
	// the binary, so a rule written in this file needs no annotation.
	Origin Origin
	// Anchor is what Pattern is matched against. See the Anchor type.
	Anchor Anchor
	// Covers is how far this rule's claim reaches. See the Covers type: the zero
	// value is the old behaviour, so a rule that has not been reviewed for it
	// keeps speaking for what is inside it.
	Covers Covers
	// IdentifyOnly marks a rule that names an object and never proposes it.
	//
	// It exists because merging the location guards into this catalog (ADR-0068)
	// made them rules, and a rule is a suggestion: without this flag ~/Documents
	// reached RuleFindings as an 8 GB "irreplaceable, risky" candidate at the top
	// of the list, and every candidate beneath it was silently dropped from the
	// advisor's view because a ruled path covers its children. Both were invisible
	// only because the advice surface is currently off the screen.
	//
	// The zero value proposes, so every pre-existing rule is unchanged. Set on the
	// migrated guards; NOT on the two rules that carried a guard before the merge
	// (iOS 设备备份, Docker 数据), which were findings then and stay findings. A
	// rule's Guard says what it reports; this says whether it is offered at all.
	IdentifyOnly bool
	// Guard is the reason code this rule reports to Assess when it wins, or empty
	// when it reports none. It is what merged the guards into this catalog: they
	// used to be four separate tables behind three separate functions answering
	// "can this come back" a second time, in parallel, with no way to reconcile
	// the two answers. A guard is now a rule like any other, ranked like any
	// other, and GuardsFor is a query over this list.
	Guard string
	// FileOnly restricts a rule to file nodes. Patterns are segment matches, so
	// `Downloads/*.dmg` would otherwise name a directory that happens to end in
	// .dmg -- and everything the user put inside it.
	FileOnly bool
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
	// Generic marks a rule that matches a container of many different things and
	// cannot name the one it matched: `Library/Caches/*` fires on every app's
	// cache and can only say "对应应用". Declared rather than inferred from the
	// pattern, because segment counting gets it backwards -- `**/node_modules`
	// is one segment and names its object exactly. A generic match reports a
	// lower confidence and carries the reason, so the vague wording is explained
	// rather than presented as certainty.
	Generic bool
	// AgeSensitive marks an object whose own newest mtime is its usage signal: an
	// application writes its cache whenever it runs, so a cache unwritten for
	// months belongs to an app nobody has run for months. Assess relaxes review
	// to safe on that signal and never tightens on it (R-069 §4.2). Not to be
	// confused with a build artifact's age, which says nothing about whether the
	// artifact is about to be rebuilt.
	AgeSensitive bool
	Recovery     Recovery
	// DeclaredRisk is this rule author's tier, and it is an INPUT to Assess, not
	// an answer. Assess reads it as Facts.Declared and may raise it -- a guard or
	// a live project overrules a comfortable declaration -- but it does not
	// recompute it, because Recovery cannot: two regenerable objects differ by
	// whether the rebuild costs three seconds or an hour, and only the author of
	// the rule knows which.
	//
	// Named for what it is. It was Risk, the same word Assessment.Risk uses for
	// the conclusion, which read as two answers to one question.
	DeclaredRisk Risk
	WhatBreaks   string
	HowToRestore string
}

// Catalog is ordered: the first match wins, so the specific entries come before
// the general ones. `~/Library/Caches/Homebrew` must be read as the Homebrew
// download cache and not as the generic user cache, because the two have
// different answers to "how do I get it back".
var Catalog = []Rule{
	// --- Whole-path rules, merged in from AbsoluteCatalog. ---
	//
	// They are first so that a tie still goes to them, which is what checking
	// them in a separate pass used to guarantee. Everything about them is now in
	// the rule: Anchor says what the pattern is matched against, and Manual says
	// whether it is root-owned and can only be reported.
	{
		Anchor: AnchorPath,
		Name:   "模拟器 dyld 缓存", Category: "构建缓存",
		Pattern: "/Library/Developer/CoreSimulator/Caches/dyld",
		Manual:  true, Command: "sudo rm -rf /Library/Developer/CoreSimulator/Caches/dyld",
		Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。模拟器下次启动会重建共享缓存，第一次启动明显变慢。",
		HowToRestore: "模拟器自动重建。",
	},
	{
		Anchor: AnchorPath,
		Name:   "系统更新包残留", Category: "更新器残留",
		Pattern: "/Library/Updates",
		Manual:  true, Command: "sudo rm -rf /Library/Updates/*",
		Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有影响。这是已下载的系统与 Rosetta 更新包，需要时会重新下载。",
		HowToRestore: "系统更新时自动重新下载。",
	},
	{
		Anchor: AnchorPath,
		Name:   "根级用户缓存", Category: "应用缓存", Generic: true,
		Pattern: "/Library/Caches",
		Manual:  true, Command: "sudo rm -rf /Library/Caches/*",
		Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "系统级共享缓存消失，相关服务首次使用时重建。个别项可能包含许可或激活信息。",
		HowToRestore: "多数自动重建。",
	},
	{
		Anchor: AnchorPath,
		Name:   "根级日志", Category: "日志", Generic: true,
		Pattern: "/Library/Logs", MinAgeDays: 30,
		Manual: true, Command: "sudo rm -rf /Library/Logs/*",
		Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskReview,
		WhatBreaks:   "历史诊断日志消失。排查旧问题时会缺少记录，功能不受影响。",
		HowToRestore: "无法恢复，但新日志会继续写入。",
	},

	// --- The location guards, merged in from anywhereIrreplaceable,
	// suffixIrreplaceable, loginStatePaths and PartialInstallReason. ---
	//
	// They live here because they are matched against the whole path: a
	// repository on an external disk is still a repository, and a guard that only
	// fires inside one home folder is not the guard it looks like.
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableRepository,
		Name:         "版本库历史", Category: "版本库",
		Pattern: "**/.git", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是版本库历史。即使远端可能有副本，本工具无法确认，删除后可能永久丢失未推送的提交。",
		HowToRestore: "只能从远端重新克隆，且未推送的提交无法找回。",
	},
	// The exception that used to be a mechanism. gitTransient was a list read by
	// the guard to suppress itself; a more specific rule does that by itself now.
	// Both live here rather than in the home-relative catalog because a
	// repository on an external disk leaves the same artifacts, and an exception
	// that only holds inside one home folder is not the exception it looks like.
	{
		Anchor: AnchorPath,
		Name:   "git 残留临时包", Category: "残留文件",
		Pattern: "**/.git/objects/pack/tmp_pack_*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有影响。这是 repack 中断后遗留的临时文件，git 不会使用它。",
		HowToRestore: "无需恢复：它不是版本库内容，删除不丢任何提交。",
	},
	{
		Anchor: AnchorPath,
		Name:   "git 残留临时索引", Category: "残留文件",
		Pattern: "**/.git/objects/pack/tmp_idx_*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有影响。这是 repack 中断后遗留的临时文件，git 不会使用它。",
		HowToRestore: "无需恢复：它不是版本库内容，删除不丢任何提交。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserData,
		Name:         "站点本地存储", Category: "用户数据",
		Pattern: "**/Local Storage", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserData,
		Name:         "站点数据库", Category: "用户数据",
		Pattern: "**/IndexedDB", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserData,
		Name:         "站点文件系统", Category: "用户数据",
		Pattern: "**/File System", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserData,
		Name:         "扩展本地设置", Category: "用户数据",
		Pattern: "**/Local Extension Settings", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本。",
	},
	// Not a cache despite where it sits: IntelliJ's local file history is how
	// uncommitted work is recovered, one segment from the index that is disposable.
	// R-070 §2.2 把它列在"该写哪条规则"的工单里，实测 667 MB。但它是聊天记录本身，
	// 不是缓存——工单按未解释字节排序，排出来的不一定是可清理项，这一条就是反例。
	// 同目录下的 media 是可重建的缓存，此处不涉及。
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserData,
		Name:         "聊天记录数据库", Category: "用户数据",
		Pattern: "**/postbox/db", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是本机保存的聊天记录。服务端未必保留全部历史，删除后无法重建。",
		HowToRestore: "无法恢复；能同步回来的部分取决于服务端保留策略。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserData,
		Name:         "本地文件历史", Category: "用户数据",
		Pattern: "**/LocalHistory", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是 IDE 保存的本地修改历史，未提交的改动靠它找回，删除后无法重建。",
		HowToRestore: "无法恢复。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableCredentials,
		Name:         "SSH 密钥", Category: "凭据",
		Pattern: "**/.ssh", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是密钥或凭据，删除后无法恢复。",
		HowToRestore: "无法恢复，只能重新生成密钥并在每个服务上重新登记。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableCredentials,
		Name:         "GPG 密钥", Category: "凭据",
		Pattern: "**/.gnupg", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是密钥或凭据，删除后无法恢复。",
		HowToRestore: "无法恢复，只能重新生成密钥。",
	},
	// The bundles, by their own extension. Matched on the segment rather than on
	// a lowercased whole path, so the comparison is now case sensitive -- the
	// applications that create these write the extension themselves, so this is
	// a narrowing on paper more than in practice.
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserContent,
		Name:         "照片图库", Category: "用户内容",
		Pattern: "**/*.photoslibrary", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserContent,
		Name:         "照片图库（旧版）", Category: "用户内容",
		Pattern: "**/*.photolibrary", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserContent,
		Name:         "Final Cut 资源库", Category: "用户内容",
		Pattern: "**/*.fcpbundle", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的剪辑工程与素材，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableUserContent,
		Name:         "Logic 工程", Category: "用户内容",
		Pattern: "**/*.logicx", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的工程文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableVirtualDisk,
		Name:         "稀疏磁盘映像", Category: "虚拟磁盘",
		Pattern: "**/*.sparsebundle", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是虚拟机或容器的磁盘，里面是整个客体文件系统。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableVirtualDisk,
		Name:         "虚拟机磁盘", Category: "虚拟磁盘",
		Pattern: "**/*.vmdk", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是虚拟机或容器的磁盘，里面是整个客体文件系统。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableVirtualDisk,
		Name:         "虚拟机磁盘", Category: "虚拟磁盘",
		Pattern: "**/*.qcow2", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是虚拟机或容器的磁盘，里面是整个客体文件系统。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableVirtualDisk,
		Name:         "UTM 虚拟机", Category: "虚拟磁盘",
		Pattern: "**/*.utm", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是虚拟机或容器的磁盘，里面是整个客体文件系统。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableVirtualDisk,
		Name:         "Parallels 虚拟机", Category: "虚拟磁盘",
		Pattern: "**/*.pvm", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是虚拟机或容器的磁盘，里面是整个客体文件系统。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        IrreplaceableVirtualDisk,
		Name:         "VirtualBox 磁盘", Category: "虚拟磁盘",
		Pattern: "**/*.vdi", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是虚拟机或容器的磁盘，里面是整个客体文件系统。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	// Login state is not irreplaceable -- you can sign in again -- so the
	// correction is on the risk and the wording, not on the recoverability.
	// Recovery is deliberately left unset rather than filled with a comfortable
	// answer.
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "Cookie", Category: "登录状态",
		Pattern: "**/Cookies", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是浏览器的登录状态。删除后所有网站都需要重新登录，部分站点的两步验证需要重新设置。",
		HowToRestore: "只能逐个网站重新登录。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "Cookie 日志", Category: "登录状态",
		Pattern: "**/Cookies-journal", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是浏览器的登录状态。删除后所有网站都需要重新登录。",
		HowToRestore: "只能逐个网站重新登录。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "保存的登录信息", Category: "登录状态",
		Pattern: "**/Login Data", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是浏览器记住的账号密码，删除后需要重新输入。",
		HowToRestore: "只能重新输入或从密码管理器恢复。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "保存的登录信息", Category: "登录状态",
		Pattern: "**/Login Data For Account", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是浏览器记住的账号密码，删除后需要重新输入。",
		HowToRestore: "只能重新输入或从密码管理器恢复。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "扩展 Cookie", Category: "登录状态",
		Pattern: "**/Extension Cookies", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是浏览器扩展的登录状态，删除后扩展需要重新登录。",
		HowToRestore: "只能逐个扩展重新登录。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "设备绑定会话", Category: "登录状态",
		Pattern: "**/Device Bound Sessions", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是与本机绑定的会话，删除后相关站点需要重新登录并重新验证设备。",
		HowToRestore: "只能重新登录。",
	},
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        LoginState,
		Name:         "Safari 数据", Category: "登录状态",
		Pattern: "**/Safari", Recovery: RecoveryReauthenticate, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这里保存的是 Safari 的登录状态与浏览数据，删除后所有网站都需要重新登录。",
		HowToRestore: "只能逐个网站重新登录。",
	},
	// Inside an installed toolchain: the installer checks the root and a stamp,
	// not the files, so nothing self-heals and the toolchain stays broken.
	{
		IdentifyOnly: true,
		Anchor:       AnchorPath,
		Guard:        PartialInstall,
		Name:         "工具链内部目录", Category: "工具链",
		Pattern: "**/flutter/bin/cache/dart-sdk", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是已安装工具链的内部目录。安装器只检查根目录和 stamp，不校验里面的文件，所以删掉之后不会自动重新下载——工具链会一直是坏的。",
		HowToRestore: "真实恢复方式是删除整个缓存目录重新拉取。",
	},
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
		Pattern: "Library/Caches/*.ShipIt", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有影响。这是已下载并安装完的应用更新包，Squirrel 装完之后没有清理。",
		HowToRestore: "无需恢复；下次更新会重新下载。",
	},
	{
		Name: "IDE 索引缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/Google/AndroidStudio*/index", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重建索引，期间代码跳转与搜索暂时不可用，大项目可能要数分钟。",
		HowToRestore: "无需操作，打开项目时自动重建。",
	},
	{
		Name: "IDE 编译缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/Google/AndroidStudio*/caches", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重新分析工程，第一次打开变慢。",
		HowToRestore: "无需操作，自动重建。",
	},
	{
		Name: "JetBrains 索引缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/JetBrains/*/index", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重建索引，大项目可能要数分钟。",
		HowToRestore: "无需操作，自动重建。",
	},
	{
		Name: "JetBrains 编译缓存", Category: "IDE 缓存",
		Pattern: "Library/Caches/JetBrains/*/caches", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次打开该 IDE 会重新分析工程，第一次打开变慢。",
		HowToRestore: "无需操作，自动重建。",
	},
	{
		Name: "旧版本 IDE 配置", Category: "旧版本残留",
		Pattern: "Library/Application Support/Google/AndroidStudio*", MinAgeDays: 180,
		Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskReview,
		WhatBreaks:   "该版本 IDE 的设置、快捷键和插件配置会丢失。若之后回退到这个版本，需要重新配置。",
		HowToRestore: "无法恢复配置本身；重新安装该版本后需要重新设置。",
	},
	{
		Name: "浏览器扩展", Category: "浏览器扩展",
		Pattern:  "Library/Application Support/Google/Chrome/*/Extensions",
		Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
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
		Pattern: "Library/Caches/Google/Chrome/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源，短时间内浏览稍慢。登录状态、书签、历史、扩展都不在这里，不受影响。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Chromium 系网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Chromium/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录状态与书签不在这里。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Brave 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/BraveSoftware/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录状态与书签不在这里。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Edge 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Microsoft Edge/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录状态与书签不在这里。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Safari 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/com.apple.Safari/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。Safari 的 Cookie 与登录信息保存在别处，不受影响。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "Firefox 网页缓存", Category: "浏览器缓存",
		Pattern: "Library/Caches/Firefox/Profiles/*/cache2", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "网页首次重新访问时要重新下载资源。登录信息在 profile 目录中，不受影响。",
		HowToRestore: "无需操作，浏览时自动重建。",
	},
	{
		Name: "浏览器离线资源缓存", Category: "浏览器缓存",
		Pattern: "**/Service Worker/CacheStorage", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "支持离线使用的网页需要联网重新加载一次；Service Worker 的注册信息不在这里，不会被注销。",
		HowToRestore: "下次访问该站点时自动重建。",
	},
	{
		Name: "浏览器脚本缓存", Category: "浏览器缓存",
		Pattern: "**/Service Worker/ScriptCache", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "对应站点的 Service Worker 脚本要重新下载一次。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器压缩字典", Category: "浏览器缓存",
		Pattern: "**/Shared Dictionary", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "使用共享字典压缩的站点，首次访问传输量略增。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器图形缓存", Category: "浏览器缓存",
		Pattern: "**/GPUCache", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "着色器需要重新编译一次，个别页面首次渲染略慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器 WebGPU 缓存", Category: "浏览器缓存",
		Pattern: "**/DawnWebGPUCache", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "使用 WebGPU 的页面首次渲染略慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "浏览器优化提示缓存", Category: "浏览器缓存",
		Pattern: "**/optimization_guide_hint_cache_store", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有可感知的影响。",
		HowToRestore: "自动重建。",
	},

	{
		Name: "Flutter 引擎产物", Category: "SDK 缓存",
		Pattern: "**/flutter/bin/cache/artifacts/engine", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "下次 Flutter 构建要重新下载引擎（GB 级），离线环境会直接失败。",
		HowToRestore: "删除整个 engine 目录后，flutter precache 或任意构建会重新下载；只删内部子目录不会触发，因为 Flutter 只检查根目录与 stamp。",
	},
	{
		Name: "Kotlin/Native 平台库", Category: "编译器发行版",
		Pattern: ".konan/*/klib/platform", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次 Kotlin/Native 构建会先花时间重新生成平台库，该次构建明显变慢。",
		HowToRestore: "无需手动操作：构建时逐个检测缺失的 platform lib 并从 .def 文件本地重新生成。",
	},
	{
		Name: "Rust 离线文档", Category: "离线文档",
		Pattern: ".rustup/toolchains/*/share/doc", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "本地 rustup doc 打不开，查文档需要联网。编译不受影响。",
		HowToRestore: "rustup component add rust-docs --toolchain <该工具链名>。",
	},
	{
		Name: "Gradle JDK 缓存", Category: "工具链缓存",
		Pattern: ".gradle/jdks/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "需要该 JDK 的 Gradle 构建会先重新下载它。",
		HowToRestore: "Gradle 在需要时按 toolchain 配置自动重新下载。",
	},
	{
		Name: "Gradle 执行历史", Category: "构建缓存",
		Pattern: "**/executionHistory", ProjectSensitive: true, Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "该项目失去增量构建信息，下次构建会重跑部分任务，一次性变慢。",
		HowToRestore: "无需手动操作，Gradle 下次构建自行重建。",
	},
	{
		Name: "Android 构建输出", Category: "构建产物",
		Pattern: "**/build/outputs/apk", ProjectSensitive: true, Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "已构建的 APK/AAB 消失。若某个是已分发的版本，重新构建不会得到逐字节相同的产物。",
		HowToRestore: "重新执行对应的 assemble/bundle 任务。",
	},
	{
		Name: "Rust 交叉编译产物", Category: "编译产物",
		Pattern: "**/target/*/debug", ProjectSensitive: true, Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "该目标平台下次 cargo 构建全量重编。",
		HowToRestore: "无需手动操作，cargo 自行重建。",
	},
	{
		Name: "代码索引数据库", Category: "工具索引",
		Pattern: "**/.codegraph", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "该项目的代码图谱与符号搜索不可用，重建索引需要时间。源码不受影响。",
		HowToRestore: "重新运行索引命令。",
	},
	{
		Name: "pnpm 内容存储", Category: "包管理器缓存",
		Pattern: "Library/pnpm/store/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "新的安装要重新下载。若某些项目的 node_modules 依赖存储中的链接，那些项目需要重新 install。",
		HowToRestore: "在相关项目执行 pnpm install。",
	},
	{
		Name: "Google 更新缓存", Category: "更新器缓存",
		Pattern: "Library/Application Support/Google/GoogleUpdater/crx_cache/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不影响已安装的 Google 软件，只是待安装的更新包副本。",
		HowToRestore: "更新时自动重新下载。",
	},
	{
		Name: "macOS 动态壁纸", Category: "系统媒体缓存",
		Pattern: "Library/Application Support/com.apple.wallpaper/aerials/videos/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "该航拍壁纸暂时不可用。",
		HowToRestore: "在壁纸设置中重新选择该素材，系统会重新下载。",
	},

	// Installers left in Downloads after the install. Not user content -- the app
	// they installed is the thing being kept -- and not in any guard list, so
	// until now every one of them was a question for the model. Files only, so
	// the size floor decides which ones appear, and a 30-day age keeps this
	// week's download out of the list. The age is the file's mtime: a browser
	// download carries the time it landed, but `curl -R` and `wget` keep the
	// server's timestamp, so a fresh download can look old. The cost of that
	// is one `review` row too many, never a wrong recovery claim.
	{
		Name: "下载的安装镜像", Category: "安装包残留", FileOnly: true,
		Pattern: "Downloads/*.dmg", MinAgeDays: 30, Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "没有影响。已安装的应用不在这里；这是安装时用过的磁盘镜像。若还没安装，需要重新下载。",
		HowToRestore: "从原下载地址重新下载。",
	},
	{
		Name: "下载的安装包", Category: "安装包残留", FileOnly: true,
		Pattern: "Downloads/*.pkg", MinAgeDays: 30, Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "没有影响。已安装的软件不在这里；这是安装程序本身。若还没安装，需要重新下载。",
		HowToRestore: "从原下载地址重新下载。",
	},
	{
		Name: "Homebrew 下载缓存", Category: "包管理器缓存",
		Pattern: "Library/Caches/Homebrew/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不影响已安装的软件，只是下载过的安装包副本。",
		HowToRestore: "下次 brew 安装或升级时自动重新下载。",
	},
	{
		Name: "Go 构建缓存", Category: "编译产物",
		Pattern: "Library/Caches/go-build/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下一次 go build 会全量重编，明显变慢一次。",
		HowToRestore: "无需操作，编译时自动重建。",
	},
	{
		Name: "pip 缓存", Category: "包管理器缓存",
		Pattern: "Library/Caches/pip/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不影响已安装的包，只是 wheel 下载副本。",
		HowToRestore: "下次 pip install 时重新下载。",
	},
	{
		Name: "CocoaPods 缓存", Category: "包管理器缓存",
		Pattern: "Library/Caches/CocoaPods/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不影响已集成的 Pods，只是缓存副本。",
		HowToRestore: "下次 pod install 时重新下载。",
	},
	{
		Name: "用户缓存", Category: "应用缓存", Generic: true, AgeSensitive: true,
		Pattern: "Library/Caches/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "对应应用下次启动会慢一些，个别应用会丢失登录态或离线内容。",
		HowToRestore: "应用自行重建；登录态需要重新登录。",
	},
	{
		Name: "沙盒应用缓存", Category: "应用缓存", Generic: true, AgeSensitive: true,
		Pattern: "Library/Containers/*/Data/Library/Caches/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "对应应用下次启动会慢一些，可能需要重新下载已缓存的内容。",
		HowToRestore: "应用自行重建。",
	},
	{
		Name: "应用组缓存", Category: "应用缓存", Generic: true, AgeSensitive: true,
		Pattern: "Library/Group Containers/*/Library/Caches/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "同一应用组内的应用下次启动会慢一些。",
		HowToRestore: "应用自行重建。",
	},
	{
		Name: "用户日志", Category: "日志", Generic: true,
		Pattern: "Library/Logs/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "失去历史诊断信息；如果正在排查某个应用的问题，先别删。",
		HowToRestore: "无法恢复，但会继续产生新日志。",
	},
	{
		Name: "废纸篓", Category: "废纸篓",
		Pattern: ".Trash/*", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskReview,
		WhatBreaks:   "废纸篓里的东西会真正消失。",
		HowToRestore: "无法恢复。删除前请确认里面没有还想要的东西。",
	},
	{
		Name: "Xcode DerivedData", Category: "编译产物",
		Pattern: "Library/Developer/Xcode/DerivedData/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次打开工程会重新索引并全量编译，第一次会明显变慢。",
		HowToRestore: "Xcode 自动重建。",
	},
	{
		Name: "Xcode 归档", Category: "构建归档",
		Pattern: "Library/Developer/Xcode/Archives/*", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "已上架版本的符号表会丢失，之后无法符号化这些版本的崩溃日志。",
		HowToRestore: "无法恢复，除非重新用完全相同的源码和工具链构建。",
	},
	{
		Name: "Xcode 设备支持文件", Category: "开发工具支持文件",
		Pattern: "Library/Developer/Xcode/iOS DeviceSupport/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "对应 iOS 版本的设备下次连接时要重新拉取符号，需要几分钟。",
		HowToRestore: "设备再次连接时自动重建。",
	},
	{
		Name: "iOS 模拟器", Category: "开发工具支持文件",
		Pattern: "Library/Developer/CoreSimulator/*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "模拟器里已安装的 App 和数据会消失，运行时需要重新下载。",
		HowToRestore: "Xcode 重新创建模拟器并下载运行时。",
	},
	{
		Guard: IrreplaceableBackup,
		Name:  "iOS 设备备份", Category: "设备备份",
		Pattern: "Library/Application Support/MobileSync/Backup/*", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是 iPhone/iPad 的本地完整备份。删掉后无法从本机恢复设备。",
		HowToRestore: "无法恢复。只有在确认已有 iCloud 备份或不再需要时才删。",
	},
	{
		Guard: IrreplaceableVirtualDisk,
		Name:  "Docker 数据", Category: "虚拟机磁盘",
		Pattern: "Library/Containers/com.docker.docker/Data/*", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "所有本地镜像、容器和卷会消失，包括未推送的镜像和容器内数据。",
		HowToRestore: "镜像可重新拉取；卷里的数据无法恢复。建议改用 docker system prune。",
	},
	// ~/.gradle/caches was one 33.9 GB rule saying "safe, re-downloaded next
	// build". That sentence was false about 31.7 GB of it. The parts do not cost
	// the same thing to get back, and the difference is the whole decision:
	//
	//   transforms      19.7 GB  local CPU, no network
	//   build-cache-1   12.0 GB  local CPU, no network
	//   modules-2        2.8 GB  network -- and re-downloaded on the very next
	//                            build of any project that uses those dependencies
	//
	// So the right advice is close to the opposite of what one blanket rule gave:
	// take the 31.7 GB that costs a slower build, leave the 2.8 GB that costs a
	// download you will immediately pay again. Splitting it is also why the folding
	// in foldUnderSettledRules no longer swallows the inside of this directory --
	// a safe rule absorbs its whole subtree, which is exactly what hid this.
	{
		Name: "Gradle 转换产物", Category: "构建缓存",
		Pattern: ".gradle/caches/*/transforms", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。下次构建重新执行 artifact transform（AAR 解包、dex 等），第一次明显变慢。",
		HowToRestore: "构建时自动重建，输入来自已下载的依赖，不重新下载。",
	},
	{
		Name: "Gradle 构建缓存", Category: "构建缓存",
		Pattern: ".gradle/caches/build-cache-*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。命中过的任务要重新执行，下次构建变慢一次。",
		HowToRestore: "构建时自动重建。Gradle 本身也会按默认 7 天回收其中不再使用的条目。",
	},
	{
		Name: "Gradle 依赖下载", Category: "包管理器缓存",
		// review, not safe, and this is the point of the split. These are the
		// downloaded artifacts themselves: deleting them while you still build
		// those projects buys space that the next build spends again, on the
		// network. It is the one part of this directory where "删了立刻又下回来"
		// is literally true.
		Pattern: ".gradle/caches/modules-*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks: "要重新下载全部依赖。仍在构建的项目下次构建立刻把它们下回来，删了基本没有意义；只有确定不再构建这些项目才值得。",
		HowToRestore: "构建时自动重新下载，需要联网且耗时。想按“多久没用过”精细回收的话，" +
			"用 Gradle 8+ 的缓存保留配置——它有文件访问时间记录，本工具只能看到下载时间（R-063 §4d）。",
	},
	{
		Name: "Gradle 编译分析缓存", Category: "构建缓存",
		Pattern: ".gradle/caches/*/javaCompile", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。下次构建重新做编译回避分析，慢一次。",
		HowToRestore: "构建时自动重建。",
	},
	{
		Name: "Gradle 插桩 jar", Category: "构建缓存",
		Pattern: ".gradle/caches/jars-*", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。下次构建重新插桩，慢一次。",
		HowToRestore: "构建时自动重建。",
	},
	{
		Name: "Gradle 生成的 API jar", Category: "构建缓存",
		Pattern: ".gradle/caches/*/generated-gradle-jars", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。下次构建重新生成 Gradle API jar。",
		HowToRestore: "构建时自动重建。",
	},
	{
		Name: "Gradle Kotlin DSL 缓存", Category: "构建缓存",
		Pattern: ".gradle/caches/*/kotlin-dsl", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。下次构建重新编译 .gradle.kts 构建脚本与访问器。",
		HowToRestore: "构建时自动重建。",
	},
	{
		Name: "Maven 本地仓库", Category: "包管理器缓存",
		Pattern: ".m2/repository/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "下次构建要重新下载全部依赖。若有本地 install 的私有构件且没有备份，会丢失。",
		HowToRestore: "公共依赖自动重新下载；本地 install 的构件需要重新构建。",
	},
	{
		Name: "Cargo 注册表缓存", Category: "包管理器缓存",
		Pattern: ".cargo/registry/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次构建要重新下载 crate 源码。",
		HowToRestore: "cargo 自动重新下载。",
	},
	{
		Name: "Go 模块缓存", Category: "包管理器缓存",
		Pattern: "go/pkg/mod/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次构建要重新下载模块。",
		HowToRestore: "go 自动重新下载。",
	},
	{
		Name: "npm 缓存", Category: "包管理器缓存",
		Pattern: ".npm/_cacache/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次 npm install 要重新下载。",
		HowToRestore: "自动重新下载。",
	},
	{
		Name: "yarn 缓存", Category: "包管理器缓存",
		Pattern: ".yarn/cache/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次 yarn install 要重新下载。",
		HowToRestore: "自动重新下载。",
	},
	{
		Name: "pnpm 存储", Category: "包管理器缓存",
		Pattern: ".pnpm-store/*", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "pnpm 项目的 node_modules 是指向这里的硬链接，删除后现有项目会失效。",
		HowToRestore: "在每个项目重新执行 pnpm install。",
	},
	{
		Name: "node_modules", Category: "依赖目录",
		Pattern: "**/node_modules", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "对应项目在重新安装依赖前无法构建或运行。",
		HowToRestore: "在项目目录执行 npm/yarn/pnpm install。",
	},
	{
		Name: "Python 虚拟环境", Category: "依赖目录",
		Pattern: "**/.venv", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "对应项目的虚拟环境消失，重建前无法运行。",
		HowToRestore: "重新 python -m venv 并按依赖文件安装。若没有依赖清单则难以还原。",
	},
	{
		Name: "Python 字节码缓存", Category: "编译产物",
		Pattern: "**/__pycache__", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次运行时重新编译，几乎无感。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Rust 构建产物", Category: "编译产物",
		Pattern: "**/target/debug", ProjectSensitive: true, Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次 cargo build 全量重编，第一次会很慢。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Rust 发布产物", Category: "编译产物",
		Pattern: "**/target/release", ProjectSensitive: true, Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次 cargo build --release 全量重编。已分发的二进制不受影响。",
		HowToRestore: "自动重建。",
	},
	// One rule per artifact, with ProjectSensitive doing the work a cold copy and
	// a warm copy of each would otherwise duplicate. The distinction it draws is
	// the one that matters: identical bytes, and deleting them costs a rebuild
	// the user is in the middle of, or nothing at all.
	{
		Name: "Android 构建中间产物", Category: "编译产物", ProjectSensitive: true,
		Pattern: "**/build/intermediates", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "下次 Gradle 构建全量重建该模块。",
		HowToRestore: "自动重建。",
	},
	{
		Name: "Android 转换产物", Category: "编译产物", ProjectSensitive: true,
		Pattern: "**/build/.transforms", Recovery: RecoveryRegenerable, DeclaredRisk: RiskReview,
		WhatBreaks:   "下次 Gradle 构建重新生成该模块的转换产物。",
		HowToRestore: "自动重建。",
	},
	// --- Android SDK。R-070 §2.2 的工单里最大的一块，实测约 9.5 GB，此前 0 条规则。 ---
	//
	// 逐个组件而不是一条 `Library/Android/sdk`：SDK 目录里还有 licenses、skins 这类
	// 几 KB 的东西，以及 platform-tools（adb 在里面），整块报会把它们一起卷进去。
	{
		Name: "Android NDK", Category: "SDK 缓存",
		Pattern: "Library/Android/sdk/ndk", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "所有含原生代码的模块无法编译，直到 NDK 重新下载完成。单个版本约 3 GB。",
		HowToRestore: "SDK Manager 里重新勾选对应 NDK 版本。",
	},
	{
		Name: "Android 平台 SDK", Category: "SDK 缓存",
		Pattern: "Library/Android/sdk/platforms", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "编译需要的目标平台缺失，Gradle 同步会报找不到 compileSdk。",
		HowToRestore: "SDK Manager 里重新勾选对应 API 级别。",
	},
	{
		Name: "Android 构建工具", Category: "SDK 缓存",
		Pattern: "Library/Android/sdk/build-tools", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "aapt2、d8 等工具缺失，构建在打包阶段失败。",
		HowToRestore: "SDK Manager 里重新勾选对应 build-tools 版本。",
	},
	{
		Name: "Android 平台源码", Category: "SDK 缓存",
		Pattern: "Library/Android/sdk/sources", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不影响编译。IDE 里跳转到框架源码会变成反编译结果。",
		HowToRestore: "SDK Manager 里重新勾选 Sources。",
	},
	{
		Name: "Android 模拟器系统镜像", Category: "SDK 缓存",
		Pattern: "Library/Android/sdk/system-images", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "已创建的虚拟设备无法启动，直到对应镜像重新下载。单个镜像 1–3 GB。",
		HowToRestore: "SDK Manager 里重新勾选对应镜像；虚拟设备自身的配置不受影响。",
	},
	{
		Name: "Android 模拟器", Category: "SDK 缓存",
		Pattern: "Library/Android/sdk/emulator", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskReview,
		WhatBreaks:   "模拟器无法启动，直到重新下载。",
		HowToRestore: "SDK Manager 里重新勾选 Android Emulator。",
	},

	// --- Gradle 的补漏。目录里已有 8 条版本无关的规则，这三处是它们没盖到的。 ---
	{
		Name: "Gradle 发行版", Category: "SDK 缓存",
		Pattern: ".gradle/wrapper", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "下次用 Gradle Wrapper 构建时重新下载对应版本，离线状态下失败。",
		HowToRestore: "联网后 wrapper 自行下载。",
	},
	{
		Name: "Gradle 守护进程日志", Category: "日志",
		Pattern: ".gradle/daemon", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有影响。不需要联网。这是守护进程的日志与注册信息，下次启动重建。",
		HowToRestore: "无需恢复。",
	},
	// 版本目录（8.13 一类）剩下的部分故意不加规则。写一条 `.gradle/caches/*` 会连
	// `~/.gradle/caches` 本身一起占住——容器模式的语义就是这样——而证据包的折叠是
	// 外层胜（ADR-0061 §2b），于是里面已经分清楚的 transforms / modules 会被重新
	// 收成一句模糊警告。`Specificity()` 的注释记着这个实测：通用的 Library/Caches
	// 规则占住整个 8.1 GB，把里面 1.7 GB 的 Chrome 发现丢掉了。
	// 未解释的残余约 0.56 GB，不值得换这个。TestNoRuleClaimsTheWholeGradleCacheDirectory 看着这条。

	// --- 其余工单项。 ---
	{
		Name: "LLDB 模块缓存", Category: "构建缓存",
		Pattern: ".lldb/module_cache", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "不需要联网。下次调试时重建，首次断点变慢。",
		HowToRestore: "无需操作，调试器自行重建。",
	},
	// 装完之后没清理的安装包副本；扩展本体在 extensions/ 下，删掉不影响已装扩展。
	{
		Name: "VS Code 扩展安装包残留", Category: "更新器残留",
		Pattern: "Library/Application Support/Code/CachedExtensionVSIXs", Recovery: RecoveryRedownloadable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "没有影响。这是已安装扩展的 .vsix 安装包副本，扩展本体不在这里。",
		HowToRestore: "无需恢复；下次安装或更新扩展时重新下载。",
	},

	// --- The location guards, merged in from homeRelativeIrreplaceable. ---
	//
	// They were a parallel table read by IrreplaceableReason, which meant two
	// systems answered "can this come back" and nothing reconciled them: a
	// node_modules three levels under ~/Documents came back redownloadable from
	// here and irreplaceable from there, and the panel printed both. As rules
	// they are ranked with everything else, so the node_modules rule -- which
	// names that object rather than reaching it from above -- simply wins.
	//
	// Every one of them covers what is inside it, which is the point: a photo in
	// ~/Pictures is user content too. That is now declared rather than inferred.
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "用户文档", Category: "用户内容",
		Pattern: "Documents", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "桌面", Category: "用户内容",
		Pattern: "Desktop", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "图片", Category: "用户内容",
		Pattern: "Pictures", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "影片", Category: "用户内容",
		Pattern: "Movies", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "音乐", Category: "用户内容",
		Pattern: "Music", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "公共目录", Category: "用户内容",
		Pattern: "Public", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	// A sandboxed application keeps the person's own documents one segment away
	// from its caches. The two could not be more different.
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "沙盒应用文档", Category: "用户内容",
		Pattern: "Library/Containers/*/Data/Documents", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "应用组文档", Category: "用户内容",
		Pattern: "Library/Group Containers/*/Documents", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableCredentials,
		Name:         "钥匙串", Category: "凭据",
		Pattern: "Library/Keychains", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是密钥或凭据，删除后无法恢复。",
		HowToRestore: "无法恢复。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserData,
		Name:         "邮件数据", Category: "用户数据",
		Pattern: "Library/Mail", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本并重新同步。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserData,
		Name:         "信息记录", Category: "用户数据",
		Pattern: "Library/Messages", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本并重新同步。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserData,
		Name:         "日历数据", Category: "用户数据",
		Pattern: "Library/Calendars", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本并重新同步。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserData,
		Name:         "提醒事项数据", Category: "用户数据",
		Pattern: "Library/Reminders", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是应用为你保存的数据，不是缓存，删除后无法重建。",
		HowToRestore: "无法恢复，除非服务端仍有副本并重新同步。",
	},
	{
		IdentifyOnly: true,
		Guard:        IrreplaceableUserContent,
		Name:         "照片数据", Category: "用户内容",
		Pattern: "Library/Photos", Recovery: RecoveryIrreplaceable, DeclaredRisk: RiskRisky,
		WhatBreaks:   "这是你自己的文件，删除后无法再生成。",
		HowToRestore: "无法恢复，除非你另有备份。",
	},
}

// MatchContext is what a rule is evaluated against. A struct rather than a
// growing parameter list, because the interesting conditions are no longer only
// about the path.
type MatchContext struct {
	Path string
	// Kind is "file" or "directory", for rules that only make sense on one.
	Kind string
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
// Origin says whether a rule ships in the binary or arrived from an external
// overlay (ADR-0062 §6, ADR-0068 §5).
//
// It exists before the overlay does, on purpose. ADR-0062 §1 draws its red line
// with the words "这条线现在不划，以后加不上去", and merging the location guards
// into this catalog changed the shape of that line: the guards used to be
// outside the catalog and therefore outside anything remotely updatable, and now
// they are rules like any other. So the line is redrawn here instead -- a guard
// is baseline-only, and no overlay rule may outrank one -- and it is redrawn now
// rather than when the loader arrives.
type Origin string

const (
	// OriginBaseline is the zero value: compiled in, and the only origin a rule
	// carrying a Guard may have.
	OriginBaseline Origin = ""
	// OriginOverlay came from a signed external catalog.
	OriginOverlay Origin = "overlay"
)

// Anchor is what a rule's pattern is matched against: the path below a home
// folder, or the whole path.
//
// It is a field because it was list membership, and list membership is not
// something a pattern can be read for. Both catalogs held `**/` patterns, so
// "does this fire outside a home folder" depended on which of two lists an
// author had dropped the rule into -- and dropping one in the wrong list is
// exactly how a stale repack artifact on an external disk kept the guard the
// exception was written to lift.
type Anchor string

const (
	// AnchorHome is the zero value: matched against the path below a home folder,
	// so it cannot answer for /System or /Library. Most of the catalog is about
	// one person's files and belongs here.
	AnchorHome Anchor = ""
	// AnchorPath is matched against the whole path. For the guards, because a
	// repository on an external disk is still a repository; for the root-owned
	// caches, because that is where they are.
	AnchorPath Anchor = "path"
)

// Covers says how far a rule's claim reaches: to the object its pattern names,
// or to everything inside it as well.
//
// It is declared, not inferred. Inferring it from the pattern shape is what put
// "不可重建：这是你自己的文件" on a node_modules three levels under ~/Documents:
// the Documents rule means every photo below it, the node_modules rule means
// that directory and nothing else, and only the rules themselves know which.
type Covers string

const (
	// CoversSelfAndInside is the zero value, because it is what every rule did
	// before this field existed: `Library/Caches/go-build` also speaks for what
	// is inside it.
	CoversSelfAndInside Covers = ""
	// CoversSelfOnly names the object and stops. A rule about an object that can
	// contain anything -- node_modules, target, build -- has to say so, or it
	// claims the unrelated things people put inside it.
	CoversSelfOnly Covers = "self"
)

// guardedBaseline is a rule that ships in the binary and carries a guard: the
// class ADR-0068 §5 puts out of an overlay's reach.
func guardedBaseline(rule *Rule) bool {
	return rule != nil && rule.Origin == OriginBaseline && rule.Guard != ""
}

// ValidateOverlay is the gate an overlay loader must pass its rules through
// before they join the catalog. There is no loader yet (ADR-0062 §8 puts it
// last); this exists so the constraint is in the code the loader's author will
// read, rather than only in a decision record they may not.
func ValidateOverlay(rules []Rule) error {
	for index := range rules {
		rule := &rules[index]
		if rule.Origin != OriginOverlay {
			return fmt.Errorf("overlay rule %q must declare OriginOverlay", rule.Name)
		}
		// A guard is what an overlay must not be able to introduce, remove or
		// weaken. Refusing the field outright is the cheapest form of that.
		if rule.Guard != "" {
			return fmt.Errorf("overlay rule %q carries guard %q: guards are baseline only (ADR-0068 §5)", rule.Name, rule.Guard)
		}
	}
	return nil
}

// candidate is one rule that matched, with what is needed to rank it.
type candidate struct {
	rule *Rule
	self bool
	segs int
}

// moreSpecific is the whole arbitration, and there is deliberately no other.
//
// Before this there was none: Match returned the first rule in list order and
// the guards were a second, parallel system whose answer could contradict it.
// Merging them into one catalog only works if "which of these two answers is
// about this object" has a definition, so here it is, in order:
//
//  1. a rule that named THIS object beats one that reached it from above;
//  2. a longer pattern beats a shorter one;
//  3. a rule that names the object beats one that names its container.
//
// Ties go to whichever was considered first, which keeps the absolute catalog
// ahead of the home-relative one as it was.
func moreSpecific(a, b candidate) bool {
	// ADR-0068 §5, and it comes first because it is not a question of
	// specificity: an overlay rule may never outrank a baseline rule carrying a
	// guard, however precisely it names the object. Inside the baseline the
	// ranking below decides, which is what lets a specific identification
	// supersede a guard reached from above (ADR-0067).
	if guardedBaseline(b.rule) && a.rule.Origin != OriginBaseline {
		return false
	}
	if guardedBaseline(a.rule) && b.rule.Origin != OriginBaseline {
		return true
	}
	if a.self != b.self {
		return a.self
	}
	if a.segs != b.segs {
		return a.segs > b.segs
	}
	return !a.rule.Generic && b.rule.Generic
}

func Match(context MatchContext) *Rule {
	clean := strings.TrimSuffix(filepath.Clean(context.Path), "/")
	whole := strings.TrimPrefix(clean, "/")
	relative, insideHome := homeRelative(clean)
	var best candidate
	for index := range Catalog {
		rule := &Catalog[index]
		if !rule.conditionsMet(context) {
			continue
		}
		subject, pattern := relative, rule.Pattern
		if rule.Anchor == AnchorPath {
			subject, pattern = whole, strings.TrimPrefix(rule.Pattern, "/")
		} else if !insideHome {
			// A home-relative pattern must never answer for /System or /Library:
			// `Library/Caches` is not /System/Library/Caches.
			continue
		}
		matched, exact := matchPatternScoped(pattern, subject)
		if !matched {
			continue
		}
		if rule.Covers == CoversSelfOnly && !exact {
			continue
		}
		next := candidate{rule: rule, self: exact, segs: rule.Specificity()}
		if best.rule == nil || moreSpecific(next, best) {
			best = next
		}
	}
	return best.rule
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

// GenericConfidence is what a container rule reports: it knows the directory,
// not the object. Everything else in the catalog names its object and reports 1.
const GenericConfidence = 0.7

// Confidence is the certainty a match carries. A flat 1 told the user that
// `Library/Caches/*` knew what it was looking at, which it does not.
func (r Rule) Confidence() float64 {
	if r.Generic {
		return GenericConfidence
	}
	return 1
}

func (r Rule) conditionsMet(context MatchContext) bool {
	if r.FileOnly && context.Kind != "file" {
		return false
	}
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
	matched, _ := matchPatternScoped(pattern, relative)
	return matched
}

// matchPatternScoped is matchPattern plus the one bit a description needs:
// whether the pattern consumed the whole path, so whether it named THIS object
// or an ancestor of it.
//
// Patterns are segment prefixes, which is right for the question the guards
// answer -- losing anything under ~/Documents does cost user content -- and
// wrong for the question a description answers, which is what this object is.
// Measured: ~/Documents/proj/node_modules matched the node_modules rule
// (redownloadable) and the Documents guard (user_content) at the same time, so
// the panel printed 可重新下载 and 不可重建 about one directory.
func matchPatternScoped(pattern, relative string) (matched bool, exact bool) {
	pathSegments := strings.Split(relative, "/")
	if trimmed, found := strings.CutPrefix(pattern, "**/"); found {
		return sequenceScoped(pathSegments, strings.Split(trimmed, "/"))
	}
	// A trailing "/*" names a container, so such a pattern can never be the
	// object itself however the segments line up.
	container := strings.HasSuffix(pattern, "/*")
	patternSegments := strings.Split(strings.TrimSuffix(pattern, "/*"), "/")
	if len(pathSegments) < len(patternSegments) {
		return false, false
	}
	for index, segment := range patternSegments {
		if !segmentMatches(segment, pathSegments[index]) {
			return false, false
		}
	}
	return true, !container && len(pathSegments) == len(patternSegments)
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

// sequenceScoped reports whether the needle appears in the haystack at all, and
// whether any occurrence of it ends on the haystack's last segment -- which is
// what separates "this IS a .git" from "this is inside one".
func sequenceScoped(haystack, needle []string) (matched bool, atEnd bool) {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false, false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		hit := true
		for offset := range needle {
			if !segmentMatches(needle[offset], haystack[start+offset]) {
				hit = false
				break
			}
		}
		if hit {
			matched = true
			if start+len(needle) == len(haystack) {
				return true, true
			}
		}
	}
	return matched, false
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
