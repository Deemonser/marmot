package recommendation

import "testing"

func TestGenerationParentsAreRecognisedAtTheirOwnDepth(t *testing.T) {
	for _, path := range []string{
		"/Users/alice/.rustup/toolchains",
		"/Users/alice/.gradle/wrapper/dists",
		"/Users/alice/Library/Developer/Xcode/iOS DeviceSupport",
	} {
		if IsGenerationParent(path) == nil {
			t.Errorf("%s is not recognised as holding generations", path)
		}
	}
	// A generation itself is not a parent of generations, or every version
	// directory would be searched for versions of itself.
	for _, path := range []string{
		"/Users/alice/.rustup/toolchains/stable-aarch64-apple-darwin",
		"/Users/alice/.gradle/wrapper/dists/gradle-8.13-bin",
	} {
		if rule := IsGenerationParent(path); rule != nil {
			t.Errorf("%s was treated as a generation parent by %q", path, rule.Name)
		}
	}
}

// The measured counter-example. ~/.nvm/versions/node held v24.19.0 at 17 days
// and v22.15.1 at 25 days, and keeping several Node versions in rotation is what
// nvm is for. "Keep the newest, offer the rest" would have offered a live 1 GB
// toolchain because it happened to be touched second.
func TestGenerationsInRotationAreNotOffered(t *testing.T) {
	node := generationNamed(t, "旧版 Node")
	if offerable := node.Offerable([]int64{17, 25}); len(offerable) != 0 {
		t.Fatalf("offered a Node version 8 days behind the newest: %v", offerable)
	}
	// Genuinely abandoned, and the same rule offers it.
	if offerable := node.Offerable([]int64{17, 400}); len(offerable) != 1 || offerable[0] != 1 {
		t.Fatalf("a Node version idle 400 days was not offered: %v", offerable)
	}
}

// The other half of the measurement: rustup held stable at 4 days with 1.91.0
// and 1.89.0 at 57 and 59. Both older ones are dead and worth 2.4 GB.
func TestSupersededGenerationsAreOffered(t *testing.T) {
	rust := generationNamed(t, "旧版 Rust 工具链")
	offerable := rust.Offerable([]int64{4, 57, 59})
	if len(offerable) != 2 || offerable[0] != 1 || offerable[1] != 2 {
		t.Fatalf("expected the two superseded toolchains, got %v", offerable)
	}
}

// A lone generation is the one in use, whatever its age. This is the difference
// between generational cleanup and an age rule, and getting it wrong deletes the
// only Rust toolchain on a machine nobody has built on for a year.
func TestTheLastGenerationIsNeverOffered(t *testing.T) {
	for _, rule := range Generations {
		ages := make([]int64, rule.KeepNewest)
		for index := range ages {
			ages[index] = 900
		}
		if offerable := rule.Offerable(ages); len(offerable) != 0 {
			t.Errorf("%s offered its last %d generation(s): %v", rule.Name, rule.KeepNewest, offerable)
		}
	}
}

// Shared caches live in ~/.gradle/caches beside the version directories. They
// are a different decision, and one this rule does not get to make.
func TestSharedCachesAreNotGenerations(t *testing.T) {
	gradle := generationNamed(t, "旧版 Gradle 缓存")
	for _, name := range []string{"build-cache-1", "modules-2", "journal-1", "jars-9", "transforms-3", "CACHEDIR.TAG"} {
		if gradle.IsGeneration(name) {
			t.Errorf("%q was treated as a Gradle version", name)
		}
	}
	for _, name := range []string{"8.13", "9.6.1"} {
		if !gradle.IsGeneration(name) {
			t.Errorf("%q was not treated as a Gradle version", name)
		}
	}
}

// Every generational rule must state a real gap. A zero gap is the naive rule
// this file exists to rule out.
func TestEveryGenerationRuleRequiresAGap(t *testing.T) {
	for _, rule := range Generations {
		if rule.MinGapDays < 30 {
			t.Errorf("%s requires only a %d-day gap", rule.Name, rule.MinGapDays)
		}
		if rule.KeepNewest < 1 {
			t.Errorf("%s keeps nothing", rule.Name)
		}
		if rule.WhatBreaks == "" || rule.HowToRestore == "" {
			t.Errorf("%s does not say what it costs", rule.Name)
		}
		if rule.Recovery == RecoveryIrreplaceable {
			t.Errorf("%s offers something irreplaceable", rule.Name)
		}
	}
}

func generationNamed(t *testing.T, name string) GenerationRule {
	t.Helper()
	for _, rule := range Generations {
		if rule.Name == name {
			return rule
		}
	}
	t.Fatalf("no generational rule named %q", name)
	return GenerationRule{}
}
