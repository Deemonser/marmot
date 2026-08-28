package recommendation

import (
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
		rule := Match(path)
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
	rule := Match("/Users/alice/Library/Caches/Homebrew/downloads/x.bottle.tar.gz")
	if rule == nil || rule.Name != "Homebrew 下载缓存" {
		t.Fatalf("expected the Homebrew rule, got %v", rule)
	}
	if rule.Recovery != RecoveryRedownloadable {
		t.Fatalf("Homebrew cache is redownloadable, got %q", rule.Recovery)
	}
}

func TestMatchFindsMultiSegmentPatternAtAnyDepth(t *testing.T) {
	rule := Match("/Users/alice/work/thing/target/debug")
	if rule == nil || rule.Name != "Rust 构建产物" {
		t.Fatalf("expected the Rust debug rule, got %v", rule)
	}
	// `target` alone is not the rule: a directory called target that is not a
	// cargo output must not be swept up by it.
	if got := Match("/Users/alice/work/thing/target"); got != nil {
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
		if rule := Match(path); rule != nil {
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
		if rule := Match(path); rule != nil && cleanup.DeleteBlock(path) != "" {
			t.Fatalf("%s is protected (%s) but matched rule %q", path, cleanup.DeleteBlock(path), rule.Name)
		}
	}
}
