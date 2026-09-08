package recommendation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// corpus is the bench this refactor is carried out over. Every path here is
// chosen to land on something: a catalog rule, a guard, an exception, or
// deliberately nothing. The rules and the guards are being merged into one
// ranked catalog, and the only way to do that without flying blind is to fix the
// answers first and diff against them at every step.
//
// It is written out rather than harvested from a real disk on purpose: a real
// corpus carries project names, employer names and client names into the
// repository, and it would only be reproducible on the machine it came from.
var corpus = []string{
	// --- catalog, specific ---
	"~/Library/Caches/com.example.ShipIt",
	"~/Library/Caches/Google/AndroidStudio2026.1/index",
	"~/Library/Caches/Homebrew",
	"~/Library/Caches/go-build",
	// --- catalog, generic container ---
	"~/Library/Caches/com.some.app",
	"~/Library/Caches/GeoServices",
	"~/Library/Logs/SomeApp",
	// --- guards on the object itself ---
	"~/Documents",
	"~/Desktop",
	"~/Pictures",
	"~/Library/Keychains",
	"~/Library/Mail",
	"~/Library/Application Support/MobileSync/Backup",
	"~/Pictures/trip.photoslibrary",
	"~/work/vm.sparsebundle",
	"~/work/disk.vmdk",
	"~/work/repo/.git",
	"~/.ssh",
	"~/.gnupg",
	// --- guards reaching down from an ancestor ---
	"~/Documents/proj/node_modules",
	"~/Documents/proj/build",
	"~/Documents/proj/target",
	"~/Desktop/app/dist",
	"~/Library/Keychains/login.keychain-db",
	"~/Pictures/trip.photoslibrary/originals",
	"~/work/repo/.git/objects/ab",
	// Cased differently on purpose: the suffix guards compare a lowercased path
	// today and the catalog's segment matcher does not, so this line is where
	// that difference has to show up rather than be discovered later.
	"~/Pictures/Trip.PhotosLibrary",
	// A repository under a guarded folder: two lists claiming the same object,
	// which is the case the merge has to get right.
	"~/Documents/proj/.git",
	"~/Documents/proj/.git/objects/ab",
	// --- the exception: a git artifact is not history ---
	"~/work/repo/.git/objects/pack/tmp_pack_Z8vjYY",
	"~/work/repo/.git/objects/pack/tmp_idx_aaaa",
	// --- login state ---
	"~/Library/Safari",
	"~/Library/Safari/Cookies",
	"~/Library/Application Support/Google/Chrome/Default/Cookies",
	"~/Library/Application Support/Google/Chrome/Default/Login Data",
	// --- partial install ---
	"~/development/flutter/bin/cache/dart-sdk/lib",
	// --- R-070 §2.2 的工单，新增的一批 ---
	"~/Library/Android/sdk/ndk",
	"~/Library/Android/sdk/ndk/29.0.14206865/toolchains",
	"~/Library/Android/sdk/platforms",
	"~/Library/Android/sdk/build-tools",
	"~/Library/Android/sdk/sources",
	"~/Library/Android/sdk/system-images",
	"~/Library/Android/sdk/emulator",
	// SDK 目录里不该被整块卷走的部分：adb 在这儿，licenses 是几 KB。
	"~/Library/Android/sdk/platform-tools",
	"~/Library/Android/sdk/licenses",
	"~/.gradle/wrapper",
	"~/.gradle/daemon",
	// 版本目录本身故意不被任何规则占住（见 rules.go 里的说明）；里面的具体项各有规则。
	"~/.gradle/caches/8.13",
	"~/.gradle/caches/8.13/transforms",
	"~/.gradle/caches/modules-2/files-2.1",
	"~/.gradle/caches/build-cache-1",
	"~/.lldb/module_cache",
	"~/Library/Application Support/Code/CachedExtensionVSIXs",
	// 聊天记录是用户数据，不是缓存——工单排出来的不一定是可清理项。
	"~/Library/Group Containers/6N38VWS5BX.ru.keepcoder.Telegram/appstore/account-1/postbox/db",
	"~/Library/Group Containers/6N38VWS5BX.ru.keepcoder.Telegram/appstore/account-1/postbox/media",
	// --- absolute catalog ---
	"/Library/Caches/com.apple.something",
	"/Library/Logs/DiagnosticReports",
	// --- deliberately nothing ---
	"~/work/repo",
	"~/work/repo/src",
	"~/AndroidStudioProjects/app/build",
	"~/.gradle/caches/8.13",
	"~/Library/Android/sdk/platforms",
	"~/Music",
	"~/Downloads",
}

func corpusPaths(t *testing.T) []string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	out := make([]string, 0, len(corpus))
	for _, entry := range corpus {
		// An entry that does not start with ~/ is absolute on purpose: the
		// absolute catalog is a separate list and has to be exercised as one.
		if !strings.HasPrefix(entry, "~/") {
			out = append(out, entry)
			continue
		}
		out = append(out, filepath.Join(home, strings.TrimPrefix(entry, "~/")))
	}
	return out
}

// answer is everything the refactor must preserve for one path, in one line, so
// a diff of the whole corpus is readable.
func answer(path string) string {
	name, recovery, risk, generic := "—", "—", "—", ""
	if rule := Match(MatchContext{Path: path, Kind: "directory", AgeDays: 400, ProjectIdleDays: NoProject}); rule != nil {
		name, recovery, risk = rule.Name, string(rule.Recovery), string(rule.DeclaredRisk)
		if rule.Generic {
			generic += " generic"
		}
		// Recorded so that a guard turning back into a proposal shows up as a diff
		// line, not as a suggestion to delete ~/Documents.
		if rule.IdentifyOnly {
			generic += " identify-only"
		}
	}
	guards := append([]string(nil), GuardsFor(path)...)
	sort.Strings(guards)
	return fmt.Sprintf("rule=%s recovery=%s risk=%s%s guards=[%s]", name, recovery, risk, generic, strings.Join(guards, " "))
}

// TestCorpusAnswers is the bench itself. It prints the whole table with -v, and
// fails the moment any answer moves. Update the golden file deliberately, with
// the diff read line by line -- that diff IS the review of a refactor step.
func TestCorpusAnswers(t *testing.T) {
	paths := corpusPaths(t)
	home, _ := os.UserHomeDir()
	var lines []string
	for _, path := range paths {
		label := path
		if strings.HasPrefix(path, home) {
			label = "~/" + strings.TrimPrefix(strings.TrimPrefix(path, home), "/")
		}
		lines = append(lines, label+"\t"+answer(path))
	}
	got := strings.Join(lines, "\n") + "\n"

	golden := filepath.Join("testdata", "corpus.golden")
	if os.Getenv("UPDATE_CORPUS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("corpus golden updated")
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("no golden file yet; run with UPDATE_CORPUS=1 to write it: %v", err)
	}
	if string(want) == got {
		return
	}
	wantLines := strings.Split(strings.TrimRight(string(want), "\n"), "\n")
	gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	for i := range gotLines {
		if i < len(wantLines) && wantLines[i] == gotLines[i] {
			continue
		}
		if i < len(wantLines) {
			t.Errorf("changed:\n  was %s\n  now %s", wantLines[i], gotLines[i])
		} else {
			t.Errorf("added:\n  %s", gotLines[i])
		}
	}
	if len(wantLines) > len(gotLines) {
		t.Errorf("%d lines removed", len(wantLines)-len(gotLines))
	}
}
