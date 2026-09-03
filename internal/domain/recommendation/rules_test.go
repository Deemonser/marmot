package recommendation

import (
	"strings"
	"testing"

	"example.com/marmot/internal/domain/cleanup"
)

func TestMatchResolvesBothHomeSpellings(t *testing.T) {
	// The firmlinked spelling reaches the same directory, so it has to reach the
	// same rule. cleanup.DeleteBlock handles the pair for the same reason.
	for _, path := range []string{
		"/Users/alice/Library/Caches/com.example.app",
		"/System/Volumes/Data/Users/alice/Library/Caches/com.example.app",
	} {
		rule := Match(MatchContext{Path: path})
		if rule == nil {
			t.Fatalf("%s matched no rule", path)
		}
		if rule.Name != "用户缓存" {
			t.Fatalf("%s matched %q", path, rule.Name)
		}
	}
}

func TestMatchPrefersTheSpecificRule(t *testing.T) {
	// Homebrew's cache is under Library/Caches, so ordering is what makes the
	// answer to "how do I get it back" correct.
	rule := Match(MatchContext{Path: "/Users/alice/Library/Caches/Homebrew/downloads/x.bottle.tar.gz"})
	if rule == nil || rule.Name != "Homebrew 下载缓存" {
		t.Fatalf("expected the Homebrew rule, got %v", rule)
	}
	if rule.Recovery != RecoveryRedownloadable {
		t.Fatalf("Homebrew cache is redownloadable, got %q", rule.Recovery)
	}
}

func TestMatchFindsMultiSegmentPatternAtAnyDepth(t *testing.T) {
	rule := Match(MatchContext{Path: "/Users/alice/work/thing/target/debug"})
	if rule == nil || rule.Name != "Rust 构建产物" {
		t.Fatalf("expected the Rust debug rule, got %v", rule)
	}
	// `target` alone is not the rule: a directory called target that is not a
	// cargo output must not be swept up by it.
	if got := Match(MatchContext{Path: "/Users/alice/work/thing/target"}); got != nil {
		t.Fatalf("bare target matched %q", got.Name)
	}
}

// A home-relative pattern must never fire on a path outside a home folder --
// `Library/Caches` must not answer for /System/Library/Caches.
//
// This test used to assert that NOTHING outside a home folder matched, which was
// the reach bug written down as an invariant: /Library/Developer/CoreSimulator/
// Caches/dyld is 3.6 GB of regenerable cache and the tool had nothing to say
// about it. Paths outside a home folder may now match, but only from
// AbsoluteCatalog, which is checked explicitly.
func TestHomeRelativeRulesNeverFireOutsideAHomeFolder(t *testing.T) {
	fromAbsolute := func(rule *Rule) bool {
		for index := range AbsoluteCatalog {
			if &AbsoluteCatalog[index] == rule {
				return true
			}
		}
		return false
	}
	// Covered on purpose, and only by an absolute rule.
	for _, path := range []string{"/Library/Caches/com.example.app", "/Library/Updates"} {
		rule := Match(MatchContext{Path: path})
		if rule == nil {
			t.Fatalf("%s matched nothing", path)
		}
		if !fromAbsolute(rule) {
			t.Fatalf("%s matched %q from the home-relative catalog", path, rule.Name)
		}
	}
	// Must still match nothing at all. /System/Library/Caches is the one that
	// matters: answering for it with a `Library/Caches` rule would offer to delete
	// part of a sealed, read-only OS.
	for _, path := range []string{
		"/System/Library/Caches",
		"/Users",
		"/Users/Shared/Library/Caches",
		"/var/folders/xx/T",
	} {
		if rule := Match(MatchContext{Path: path}); rule != nil {
			t.Fatalf("%s matched %q and should match nothing", path, rule.Name)
		}
	}
}

// Every rule must answer "what happens if I say yes" and "how do I get it back".
// A suggestion a person cannot evaluate is not a suggestion, and this is the
// question rule-based cleaners leave unanswered.
func TestEveryRuleExplainsItself(t *testing.T) {
	for _, rule := range Catalog {
		if rule.Name == "" || rule.Category == "" || rule.Pattern == "" {
			t.Fatalf("rule %+v is missing identity", rule)
		}
		if rule.WhatBreaks == "" {
			t.Fatalf("rule %q does not say what breaks", rule.Name)
		}
		if rule.HowToRestore == "" {
			t.Fatalf("rule %q does not say how to restore", rule.Name)
		}
		switch rule.Recovery {
		case RecoveryRegenerable, RecoveryRedownloadable, RecoveryIrreplaceable:
		default:
			t.Fatalf("rule %q has recovery %q", rule.Name, rule.Recovery)
		}
		switch rule.Risk {
		case RiskSafe, RiskReview, RiskRisky:
		default:
			t.Fatalf("rule %q has risk %q", rule.Name, rule.Risk)
		}
		// Nothing irreplaceable may be called safe. This is the one combination
		// that would turn a suggestion into a trap.
		if rule.Recovery == RecoveryIrreplaceable && rule.Risk == RiskSafe {
			t.Fatalf("rule %q is irreplaceable and marked safe", rule.Name)
		}
	}
}

// The catalog and the delete guard must not disagree: a rule that fires on a
// path the guard refuses would produce a suggestion that can never be acted on.
func TestCatalogDoesNotTargetProtectedPaths(t *testing.T) {
	for _, path := range []string{
		"/Users/alice",
		"/System/Volumes/Data/Users/alice",
		"/System",
		"/Library",
		"/private/var/db",
	} {
		if rule := Match(MatchContext{Path: path}); rule != nil && cleanup.DeleteBlock(path) != "" {
			t.Fatalf("%s is protected (%s) but matched rule %q", path, cleanup.DeleteBlock(path), rule.Name)
		}
	}
}

// Recoverability is not the standard; disruption is. Identical bytes of build
// output cost a rebuild the user is in the middle of, or nothing at all, and the
// discriminator is the surrounding project's source activity -- not the
// artifact's own mtime, which for a project being resumed says "cold" about a
// cache that is about to be needed.
func TestProjectActivityDecidesRiskNotTheArtifactAge(t *testing.T) {
	rule := Match(MatchContext{Path: "/Users/alice/work/app/build/intermediates"})
	if rule == nil || !rule.ProjectSensitive {
		t.Fatalf("build output should be project-sensitive: %v", rule)
	}
	project := func(declared Risk, idle int64) Assessment {
		return Assess(Facts{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: declared, Activity: ActivityProject, IdleDays: idle})
	}

	// Live project: a `safe` declaration is raised, and the reason says why.
	got := project(RiskSafe, 0)
	if got.Risk != RiskReview {
		t.Fatalf("an active project's build cache stayed %q", got.Risk)
	}
	if !strings.Contains(got.Note, "正在使用") || got.Reasons[0] != ReasonProjectActive {
		t.Fatalf("the assessment does not explain the disruption: %#v", got)
	}

	// Dormant project: a cautious `review` may relax.
	got = project(RiskReview, 641)
	if got.Risk != RiskSafe {
		t.Fatalf("a project idle 641 days stayed %q", got.Risk)
	}
	if !strings.Contains(got.Note, "641") || got.Reasons[0] != ReasonProjectDormant {
		t.Fatalf("the assessment does not state the idle time: %#v", got)
	}

	// In between: left exactly as declared, and the only reason is the catalog's.
	if got = project(RiskReview, 90); got.Risk != RiskReview || got.Note != "" || len(got.Reasons) != 1 || got.Reasons[0] != ReasonCatalog {
		t.Fatalf("a 90-day project was assessed as %#v", got)
	}

	// Outside any project there is no signal, and guessing is how an active
	// project's cache gets called cold.
	if got = project(RiskSafe, NoProject); got.Risk != RiskSafe || got.Note != "" || len(got.Reasons) != 0 {
		t.Fatalf("an object outside a project was assessed as %#v", got)
	}
}

// A trailing "*" is a prefix, which is how a random-suffixed artifact is named.
func TestPrefixSegmentMatching(t *testing.T) {
	rule := Match(MatchContext{Path: "/Users/alice/work/thing/.git/objects/pack/tmp_pack_Z8vjYY"})
	if rule == nil || rule.Name != "git 残留临时包" {
		t.Fatalf("expected the stale temp pack rule, got %v", rule)
	}
	// A real pack must not be swept up by it.
	if got := Match(MatchContext{Path: "/Users/alice/work/thing/.git/objects/pack/pack-abc.pack"}); got != nil {
		t.Fatalf("a real pack matched %q", got.Name)
	}
}

// A "*" inside a sequence matches one whole segment, which is what a
// cross-compilation target directory is.
func TestSingleSegmentWildcardInsideASequence(t *testing.T) {
	rule := Match(MatchContext{Path: "/Users/alice/CodeProjects/x/target/aarch64-linux-android/debug/deps"})
	if rule == nil || rule.Name != "Rust 交叉编译产物" {
		t.Fatalf("expected the cross-compile rule, got %v", rule)
	}
}

// Every promoted rule has to reach the paths it was promoted for. These are the
// objects an advisor found on a real machine; a rule that does not match them is
// a rule that changed nothing.
func TestPromotedRulesMatchWhatTheyWerePromotedFor(t *testing.T) {
	cases := map[string]string{
		"/Users/a/dev/flutter/bin/cache/artifacts/engine":                               "Flutter 引擎产物",
		"/Users/a/.konan/kotlin-native-prebuilt-macos-aarch64-2.2.21/klib/platform":     "Kotlin/Native 平台库",
		"/Users/a/.rustup/toolchains/stable-aarch64-apple-darwin/share/doc/rust/html":   "Rust 离线文档",
		"/Users/a/.gradle/jdks/jbrsdk_jcef-21-JetBrains-21.0.11-osx-aarch64.tar.gz":     "Gradle JDK 缓存",
		"/Users/a/work/app/.gradle/8.7/executionHistory/executionHistory.bin":           "Gradle 执行历史",
		"/Users/a/work/app/build/outputs/apk/dev/debug/app-dev-debug.apk":               "Android 构建输出",
		"/Users/a/work/x/.codegraph/codegraph.db":                                       "代码索引数据库",
		"/Users/a/Library/pnpm/store/v11/files":                                         "pnpm 内容存储",
		"/Users/a/Library/Application Support/Google/GoogleUpdater/crx_cache/abc":       "Google 更新缓存",
		"/Users/a/Library/Application Support/com.apple.wallpaper/aerials/videos/x.mov": "macOS 动态壁纸",
	}
	for path, want := range cases {
		got := Match(MatchContext{Path: path})
		if got == nil {
			t.Errorf("%s matched no rule", path)
			continue
		}
		if got.Name != want {
			t.Errorf("%s matched %q, expected %q", path, got.Name, want)
		}
	}
}

// The browser split is the whole point: the cache is worth reclaiming and the
// session is not worth losing, and they sit a directory apart. Verified layout on
// macOS: a Chromium browser keeps its HTTP cache under ~/Library/Caches and its
// cookies and saved logins under Application Support.
func TestBrowserCacheIsOfferedAndLoginStateIsNot(t *testing.T) {
	cleanable := map[string]string{
		"/Users/a/Library/Caches/Google/Chrome/Default/Cache":                                    "Chrome 网页缓存",
		"/Users/a/Library/Caches/Google/Chrome/Default/Code Cache":                               "Chrome 网页缓存",
		"/Users/a/Library/Caches/com.apple.Safari/WebKitCache":                                   "Safari 网页缓存",
		"/Users/a/Library/Caches/BraveSoftware/Brave-Browser/Default/Cache":                      "Brave 网页缓存",
		"/Users/a/Library/Application Support/Google/Chrome/Default/Service Worker/CacheStorage": "浏览器离线资源缓存",
		"/Users/a/Library/Application Support/Google/Chrome/Default/GPUCache":                    "浏览器图形缓存",
		"/Users/a/Library/Application Support/Google/Chrome/Default/Shared Dictionary":           "浏览器压缩字典",
	}
	for path, want := range cleanable {
		got := Match(MatchContext{Path: path})
		if got == nil || got.Name != want {
			t.Errorf("%s matched %v, expected %q", path, got, want)
			continue
		}
		if got.Risk != RiskSafe {
			t.Errorf("%s is browser cache and came back %q", path, got.Risk)
		}
	}
}

// Session state is recoverable -- you can sign in again -- so the guard corrects
// the risk and the wording rather than the recoverability. Calling it
// irreplaceable would be a lie in the cautious direction.
func TestLoginStateIsGuardedAsRiskyRatherThanIrreplaceable(t *testing.T) {
	for _, path := range []string{
		"/Users/a/Library/Application Support/Google/Chrome/Default/Cookies",
		"/Users/a/Library/Application Support/Google/Chrome/Default/Login Data",
		"/Users/a/Library/Application Support/Google/Chrome/Default/Device Bound Sessions",
		"/Users/a/Library/Safari",
	} {
		if LoginStateReason(path) != LoginState {
			t.Errorf("%s holds signed-in sessions and was not flagged", path)
		}
		// And it must not be swept into the irreplaceable bucket instead.
		if IrreplaceableReason(path) != "" && path != "/Users/a/Library/Safari" {
			t.Errorf("%s was called irreplaceable; signing in again restores it", path)
		}
	}
	// Site-local storage genuinely is irreplaceable: drafts and offline
	// documents that exist nowhere else, one segment away from the caches.
	for _, path := range []string{
		"/Users/a/Library/Application Support/Google/Chrome/Default/Local Storage",
		"/Users/a/Library/Application Support/Google/Chrome/Default/IndexedDB",
	} {
		if IrreplaceableReason(path) != IrreplaceableUserData {
			t.Errorf("%s is site-local data and must be irreplaceable", path)
		}
	}
	// The cache next door must not be caught by either guard.
	cache := "/Users/a/Library/Caches/Google/Chrome/Default/Cache"
	if LoginStateReason(cache) != "" || IrreplaceableReason(cache) != "" {
		t.Errorf("%s is pure cache and was guarded", cache)
	}
}

// The two largest remaining vague items, named. One was a downloaded installer
// Squirrel never cleaned up; the other an IDE index. Neither needed judgement --
// they needed a rule.
func TestUpdaterAndIDECachesAreNamedNotGeneric(t *testing.T) {
	cases := map[string]string{
		"/Users/a/Library/Caches/com.microsoft.VSCode.ShipIt":                   "应用更新包残留",
		"/Users/a/Library/Caches/com.some.other.App.ShipIt/update.xyz":          "应用更新包残留",
		"/Users/a/Library/Caches/Google/AndroidStudio2026.1.3/index":            "IDE 索引缓存",
		"/Users/a/Library/Caches/Google/AndroidStudio2026.1.3/caches":           "IDE 编译缓存",
		"/Users/a/Library/Caches/JetBrains/IntelliJIdea2025.2/index":            "JetBrains 索引缓存",
		"/Users/a/Library/Application Support/Google/Chrome/Default/Extensions": "浏览器扩展",
	}
	for path, want := range cases {
		got := Match(MatchContext{Path: path})
		if got == nil || got.Name != want {
			t.Errorf("%s matched %v, expected %q", path, got, want)
		}
	}
	// An abandoned IDE generation is only offered once it is actually abandoned.
	old := "/Users/a/Library/Application Support/Google/AndroidStudio2024.2"
	if got := Match(MatchContext{Path: old, AgeDays: 340}); got == nil || got.Name != "旧版本 IDE 配置" {
		t.Errorf("a 340-day-idle IDE generation matched %v", got)
	}
	if got := Match(MatchContext{Path: old, AgeDays: 18}); got != nil && got.Name == "旧版本 IDE 配置" {
		t.Error("an IDE generation used 18 days ago was offered as abandoned")
	}
}

// LocalHistory lives inside a directory called Caches and is not a cache: it is
// how uncommitted work is recovered. It sits one segment from the index that
// genuinely is disposable, which is exactly the kind of neighbour a generic
// "it's under Caches" rule gets wrong.
func TestIDELocalHistoryIsProtected(t *testing.T) {
	history := "/Users/a/Library/Caches/Google/AndroidStudio2026.1.3/LocalHistory"
	if IrreplaceableReason(history) != IrreplaceableUserData {
		t.Fatalf("%s holds recoverable uncommitted work and was not protected", history)
	}
	index := "/Users/a/Library/Caches/Google/AndroidStudio2026.1.3/index"
	if IrreplaceableReason(index) != "" {
		t.Fatalf("%s is a rebuildable index and must not be protected", index)
	}
}

// ~/.gradle/caches was a single 33.9 GB "safe, re-downloaded next build" item.
// That sentence was false about 31.7 GB of it, and the falsehood pointed the user
// at the wrong bytes: the part that costs a download is the small part.
func TestGradleCacheIsSplitByWhatItCostsToGetBack(t *testing.T) {
	rule := func(path string) *Rule {
		return Match(MatchContext{Path: path, ProjectIdleDays: NoProject})
	}
	// The two big ones cost CPU, not network, and say so.
	for _, path := range []string{
		"/Users/a/.gradle/caches/8.13/transforms",
		"/Users/a/.gradle/caches/build-cache-1",
	} {
		hit := rule(path)
		if hit == nil {
			t.Fatalf("%s matched nothing", path)
		}
		if hit.Recovery != RecoveryRegenerable {
			t.Errorf("%s is %q; it rebuilds from local inputs", hit.Name, hit.Recovery)
		}
		if hit.Risk != RiskSafe {
			t.Errorf("%s is %q; a slower next build is not a decision to agonise over", hit.Name, hit.Risk)
		}
		if !strings.Contains(hit.WhatBreaks, "不需要联网") {
			t.Errorf("%s does not say the recovery is offline: %q", hit.Name, hit.WhatBreaks)
		}
	}
	// The downloaded artifacts are the opposite case, and the only part of this
	// directory where "delete it and it comes straight back" is literally true.
	downloads := rule("/Users/a/.gradle/caches/modules-2/files-2.1")
	if downloads == nil || downloads.Name != "Gradle 依赖下载" {
		t.Fatalf("the downloaded dependencies are not named separately: %#v", downloads)
	}
	if downloads.Recovery != RecoveryRedownloadable || downloads.Risk != RiskReview {
		t.Errorf("downloads are %q/%q; they cost a download the next build pays again",
			downloads.Recovery, downloads.Risk)
	}
	// Version numbering moves. build-cache-2 and modules-3 must not silently stop
	// matching and reappear as an unexplained multi-GB blob.
	for _, path := range []string{
		"/Users/a/.gradle/caches/build-cache-2",
		"/Users/a/.gradle/caches/modules-3/files-3.0",
		"/Users/a/.gradle/caches/jars-11",
		"/Users/a/.gradle/caches/9.6.1/transforms",
	} {
		if rule(path) == nil {
			t.Errorf("%s matched nothing; a version bump must not blind the catalog", path)
		}
	}
}

// The blanket rule was safe, and a safe rule absorbs its whole subtree in
// foldUnderSettledRules. That is what hid the split in the first place, so no
// rule may claim the directory as a whole again.
func TestNoRuleClaimsTheWholeGradleCacheDirectory(t *testing.T) {
	if hit := Match(MatchContext{Path: "/Users/a/.gradle/caches", ProjectIdleDays: NoProject}); hit != nil {
		t.Fatalf("~/.gradle/caches is claimed whole by %q, which folds its parts away again", hit.Name)
	}
}

// Every rule in Catalog goes through homeRelative, which only recognises
// /Users/<account>/..., so on a whole-disk scan the tool knew nothing outside the
// user's home folder. Measured: /Library/Developer/CoreSimulator/Caches/dyld is
// 3.6 GB of cache the simulator rebuilds on demand, and it matched nothing.
func TestAbsoluteRulesReachOutsideAnyHomeFolder(t *testing.T) {
	hit := func(path string) *Rule {
		return Match(MatchContext{Path: path, AgeDays: 400, ProjectIdleDays: NoProject})
	}
	for _, path := range []string{
		"/Library/Developer/CoreSimulator/Caches/dyld",
		"/Library/Updates/140-17812",
		"/Library/Caches",
		"/Library/Logs/DiagnosticReports",
	} {
		rule := hit(path)
		if rule == nil {
			t.Fatalf("%s matched nothing", path)
		}
		// All of these are root-owned, so the tool must not try to act on them.
		if !rule.Manual || rule.Command == "" {
			t.Errorf("%s matched %q, which is not marked manual with a command", path, rule.Name)
		}
	}
	// And the home-relative catalog still works, i.e. the absolute patterns did
	// not shadow it. /Library/Caches must not swallow ~/Library/Caches.
	if rule := hit("/Users/alice/Library/Caches/Google/Chrome"); rule == nil || rule.Manual {
		t.Fatalf("a home-relative cache matched %#v", rule)
	}
}

// A manual rule with no command is a finding the user cannot act on, which is the
// only thing worse than not making it.
func TestEveryManualRuleCarriesItsCommand(t *testing.T) {
	for _, rule := range AbsoluteCatalog {
		if rule.Manual && strings.TrimSpace(rule.Command) == "" {
			t.Errorf("%s is manual with no command", rule.Name)
		}
		if rule.WhatBreaks == "" || rule.HowToRestore == "" {
			t.Errorf("%s does not say what it costs", rule.Name)
		}
	}
}
