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

func TestMatchIgnoresPathsOutsideAHomeFolder(t *testing.T) {
	for _, path := range []string{
		"/Library/Caches/com.example.app",
		"/System/Library/Caches",
		"/Users",
		"/Users/Shared/Library/Caches",
		"/var/folders/xx/T",
	} {
		if rule := Match(MatchContext{Path: path}); rule != nil {
			t.Fatalf("%s matched %q but the catalog is home-relative", path, rule.Name)
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

	// Live project: a `safe` declaration is raised, and the reason says why.
	risk, note := AdjustForProjectActivity(RiskSafe, 0)
	if risk != RiskReview {
		t.Fatalf("an active project's build cache stayed %q", risk)
	}
	if !strings.Contains(note, "正在使用") {
		t.Fatalf("the note does not explain the disruption: %q", note)
	}

	// Dormant project: a cautious `review` may relax.
	risk, note = AdjustForProjectActivity(RiskReview, 641)
	if risk != RiskSafe {
		t.Fatalf("a project idle 641 days stayed %q", risk)
	}
	if !strings.Contains(note, "641") {
		t.Fatalf("the note does not state the idle time: %q", note)
	}

	// In between: left exactly as declared, with nothing to say.
	if risk, note = AdjustForProjectActivity(RiskReview, 90); risk != RiskReview || note != "" {
		t.Fatalf("a 90-day project was adjusted to %q / %q", risk, note)
	}

	// Outside any project there is no signal, and guessing is how an active
	// project's cache gets called cold.
	if risk, note = AdjustForProjectActivity(RiskSafe, NoProject); risk != RiskSafe || note != "" {
		t.Fatalf("an object outside a project was adjusted to %q / %q", risk, note)
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
