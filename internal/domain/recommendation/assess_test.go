package recommendation

import (
	"strings"
	"testing"
)

// The order of the derivation is the contract: a permanent loss is not
// something an activity signal gets to soften, however dormant the project.
func TestAssessGuardsComeBeforeEverythingElse(t *testing.T) {
	got := Assess(Facts{
		Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskSafe,
		Activity: ActivityProject, IdleDays: 900,
		Guards: []string{IrreplaceableUserContent},
	})
	if got.Risk != RiskRisky || got.Reasons[0] != ReasonIrreplaceable {
		t.Fatalf("a user-content guard was softened to %#v", got)
	}
	if got.Note != "" {
		t.Fatalf("a dormant-project note was attached to a permanent loss: %q", got.Note)
	}

	got = Assess(Facts{Source: SourceAdvisor, Recovery: RecoveryRegenerable, Confidence: 1, Guards: []string{LoginState}})
	if got.Risk != RiskRisky || got.Reasons[0] != ReasonLoginState {
		t.Fatalf("login state concluded as %#v", got)
	}

	// A rule that declares its object irreplaceable chose its tier knowing that:
	// emptying the trash is review, deleting a device backup is risky. The
	// declaration stands, except that safe is never allowed.
	for declared, want := range map[Risk]Risk{RiskReview: RiskReview, RiskRisky: RiskRisky, RiskSafe: RiskReview} {
		got = Assess(Facts{Source: SourceRule, Recovery: RecoveryIrreplaceable, Declared: declared})
		if got.Risk != want || got.Reasons[0] != ReasonIrreplaceable {
			t.Fatalf("an irreplaceable rule declared %q concluded as %#v", declared, got)
		}
	}
	// A model's claim on something permanent is the strongest warning there is.
	if got = Assess(Facts{Source: SourceAdvisor, Recovery: RecoveryIrreplaceable, Confidence: 1}); got.Risk != RiskRisky {
		t.Fatalf("an irreplaceable advisor claim concluded as %q", got.Risk)
	}
}

// Nothing irreplaceable is ever safe, by construction: no combination of the
// other facts reaches safe once recovery says the bytes do not come back.
func TestAssessNeverPairsIrreplaceableWithSafe(t *testing.T) {
	for _, source := range []Source{SourceRule, SourceAdvisor} {
		for _, declared := range []Risk{"", RiskSafe, RiskReview, RiskRisky} {
			for _, activity := range []ActivityKind{ActivityNone, ActivityProject, ActivityArtifact, ActivityGeneration} {
				got := Assess(Facts{Source: source, Recovery: RecoveryIrreplaceable, Declared: declared, Confidence: 1, Activity: activity, IdleDays: 1000})
				if got.Risk == RiskSafe {
					t.Fatalf("%s/%q/%s: irreplaceable concluded as safe", source, declared, activity)
				}
				if source == SourceAdvisor && got.Risk != RiskRisky {
					t.Fatalf("%q/%s: an advisor's irreplaceable claim concluded as %q", declared, activity, got.Risk)
				}
			}
		}
	}
}

// The advisor's baseline comes from what it is stable about: recovery and its
// own confidence. It never supplies a tier.
func TestAssessAdvisorBaseline(t *testing.T) {
	cases := []struct {
		recovery   Recovery
		confidence float64
		want       Risk
		reason     string
	}{
		{RecoveryRegenerable, 0.9, RiskSafe, ""},
		{RecoveryRegenerable, 0.5, RiskReview, ReasonAdvisorUncertain},
		{RecoveryRedownloadable, 0.95, RiskReview, ReasonRedownloadCost},
	}
	for _, tc := range cases {
		got := Assess(Facts{Source: SourceAdvisor, Recovery: tc.recovery, Confidence: tc.confidence})
		if got.Risk != tc.want {
			t.Fatalf("%s @ %.2f concluded as %q, want %q", tc.recovery, tc.confidence, got.Risk, tc.want)
		}
		if tc.reason != "" && !hasGuard(got.Reasons, tc.reason) {
			t.Fatalf("%s @ %.2f gave reasons %v, want %s", tc.recovery, tc.confidence, got.Reasons, tc.reason)
		}
		if tc.want == RiskSafe && len(got.Reasons) != 0 {
			t.Fatalf("a safe conclusion carried reasons: %v", got.Reasons)
		}
	}
}

// A partial toolchain deletion is recoverable but not cheaply, so it cannot be
// safe -- and the correction stays visible as a reason. It is a floor: a
// dormant project around the toolchain does not make the toolchain any less
// broken, and the activity note ("下一次构建要重新下载") would contradict the
// partial-install message beside it, so it is dropped.
func TestAssessPartialInstallIsNeverSafe(t *testing.T) {
	got := Assess(Facts{Source: SourceAdvisor, Recovery: RecoveryRedownloadable, Confidence: 1, Guards: []string{PartialInstall}})
	if got.Risk != RiskReview || !hasGuard(got.Reasons, ReasonPartialInstall) {
		t.Fatalf("partial install concluded as %#v", got)
	}
	for _, idle := range []int64{0, 400} {
		got = Assess(Facts{Source: SourceAdvisor, Recovery: RecoveryRedownloadable, Confidence: 1, Guards: []string{PartialInstall}, Activity: ActivityProject, IdleDays: idle})
		if got.Risk != RiskReview {
			t.Fatalf("partial install in a project idle %d days concluded as %q", idle, got.Risk)
		}
		if got.Note != "" {
			t.Fatalf("an activity note stood beside the partial-install message: %q", got.Note)
		}
	}
}

// Dormancy says the bytes will not be missed, not what the bytes are. A model
// that was not sure what it was looking at is not relaxed by it, and its own
// uncertainty stays on the record.
func TestAssessDormancyDoesNotRelaxAnUncertainAdvisor(t *testing.T) {
	got := Assess(Facts{Source: SourceAdvisor, Recovery: RecoveryRegenerable, Confidence: 0.3, Activity: ActivityProject, IdleDays: 400})
	if got.Risk != RiskReview {
		t.Fatalf("an uncertain claim in a dormant project concluded as %q", got.Risk)
	}
	if !hasGuard(got.Reasons, ReasonAdvisorUncertain) || !hasGuard(got.Reasons, ReasonProjectDormant) {
		t.Fatalf("the reasons lost a fact: %v", got.Reasons)
	}
	// A confident redownloadable claim in a dormant project does relax: the
	// download will not be needed.
	if got = Assess(Facts{Source: SourceAdvisor, Recovery: RecoveryRedownloadable, Confidence: 0.95, Activity: ActivityProject, IdleDays: 400}); got.Risk != RiskSafe {
		t.Fatalf("a confident claim in a dormant project stayed %q", got.Risk)
	}
}

// A cache nobody has written in six months belongs to an app nobody runs. The
// relaxation goes one way only: a cache written yesterday is a cache in use, and
// deleting a cache in use costs a rebuild, which is what safe means.
func TestAssessColdCacheRelaxesAndWarmCacheDoesNotTighten(t *testing.T) {
	cold := Assess(Facts{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskReview, Activity: ActivityArtifact, IdleDays: 200, Generic: true})
	if cold.Risk != RiskSafe || cold.Reasons[0] != ReasonCacheCold || !strings.Contains(cold.Note, "200") {
		t.Fatalf("a cold cache concluded as %#v", cold)
	}
	warm := Assess(Facts{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskSafe, Activity: ActivityArtifact, IdleDays: 1})
	if warm.Risk != RiskSafe || warm.Note != "" {
		t.Fatalf("a warm cache was tightened: %#v", warm)
	}
	// A warm generic cache stays exactly as declared, and says why it is vague.
	vague := Assess(Facts{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskReview, Activity: ActivityArtifact, IdleDays: 1, Generic: true})
	if vague.Risk != RiskReview || !hasGuard(vague.Reasons, ReasonGenericRule) {
		t.Fatalf("a warm generic cache concluded as %#v", vague)
	}
}

// Every review or risky conclusion explains itself. A tier the user is asked to
// weigh with no stated reason is the failure this whole file exists to remove.
func TestAssessNonSafeAlwaysHasAReason(t *testing.T) {
	for _, facts := range []Facts{
		{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskReview},
		{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskRisky},
		{Source: SourceRule, Recovery: RecoveryRedownloadable, Declared: RiskReview, Activity: ActivityGeneration, IdleDays: 60},
		{Source: SourceAdvisor, Recovery: RecoveryRedownloadable, Confidence: 1},
		{Source: SourceAdvisor, Recovery: RecoveryRegenerable, Confidence: 0.2},
	} {
		got := Assess(facts)
		if got.Risk != RiskSafe && len(got.Reasons) == 0 {
			t.Fatalf("%#v concluded %q with no reason", facts, got.Risk)
		}
	}
	// The catalog's own judgement is a reason when nothing else applies.
	if got := Assess(Facts{Source: SourceRule, Recovery: RecoveryRegenerable, Declared: RiskReview}); got.Reasons[0] != ReasonCatalog {
		t.Fatalf("a declared review carried %v", got.Reasons)
	}
}

// A container rule admits it cannot name the object; a rule that names its
// object is fully confident, however few segments the pattern has.
func TestRuleConfidenceFollowsWhetherTheRuleNamesItsObject(t *testing.T) {
	generic := Match(MatchContext{Path: "/Users/a/Library/Caches/com.unknown.app"})
	precise := Match(MatchContext{Path: "/Users/a/Library/Caches/Google/Chrome/Default/Cache"})
	oneSegment := Match(MatchContext{Path: "/Users/a/work/app/node_modules"})
	if generic == nil || precise == nil || oneSegment == nil {
		t.Fatal("all three paths should match the catalog")
	}
	if !generic.Generic || generic.Confidence() != GenericConfidence {
		t.Fatalf("the container rule reports %.2f (generic=%v)", generic.Confidence(), generic.Generic)
	}
	if precise.Generic || precise.Confidence() != 1 {
		t.Fatalf("the Chrome rule reports %.2f", precise.Confidence())
	}
	if oneSegment.Generic || oneSegment.Confidence() != 1 {
		t.Fatalf("node_modules names its object and reports %.2f", oneSegment.Confidence())
	}
}

// GuardsFor is the one place the three guards are consulted together.
func TestGuardsForConsultsEveryGuard(t *testing.T) {
	if guards := GuardsFor("/Users/a/Pictures/Trip"); !hasGuard(guards, IrreplaceableUserContent) {
		t.Fatalf("user content not guarded: %v", guards)
	}
	if guards := GuardsFor("/Users/a/Library/Application Support/Google/Chrome/Default/Cookies"); !hasGuard(guards, LoginState) {
		t.Fatalf("login state not guarded: %v", guards)
	}
	if guards := GuardsFor("/Users/a/dev/flutter/bin/cache/dart-sdk/bin/snapshots"); !hasGuard(guards, PartialInstall) {
		t.Fatalf("partial install not guarded: %v", guards)
	}
	if guards := GuardsFor("/Users/a/Library/Caches/Google/Chrome/Default/Cache"); len(guards) != 0 {
		t.Fatalf("pure cache was guarded: %v", guards)
	}
}

// The installers in Downloads: named, aged, files only, and never the freshly
// downloaded one.
func TestDownloadedInstallersAreNamedAfterAMonth(t *testing.T) {
	fresh := Match(MatchContext{Path: "/Users/a/Downloads/Xcode.dmg", Kind: "file", AgeDays: 3})
	if fresh != nil {
		t.Fatalf("a three-day-old download was offered: %s", fresh.Name)
	}
	old := Match(MatchContext{Path: "/Users/a/Downloads/Xcode.dmg", Kind: "file", AgeDays: 45})
	if old == nil || old.Category != "安装包残留" || old.Recovery != RecoveryRedownloadable {
		t.Fatalf("an old installer image was not named: %v", old)
	}
	if pkg := Match(MatchContext{Path: "/Users/a/Downloads/Setup.pkg", Kind: "file", AgeDays: 45}); pkg == nil || pkg.Category != "安装包残留" {
		t.Fatalf("an old installer package was not named: %v", pkg)
	}
	// A folder that happens to end in .dmg is the user's, not an installer.
	if folder := Match(MatchContext{Path: "/Users/a/Downloads/Xcode.dmg", Kind: "directory", AgeDays: 45}); folder != nil {
		t.Fatalf("a directory named like an image was offered: %s", folder.Name)
	}
	// Downloads itself, and anything that is not an installer, stays out.
	if other := Match(MatchContext{Path: "/Users/a/Downloads/thesis.pdf", AgeDays: 400}); other != nil {
		t.Fatalf("a document in Downloads matched %s", other.Name)
	}
}
