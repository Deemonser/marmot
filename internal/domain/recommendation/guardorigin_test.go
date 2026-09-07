package recommendation

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-0068 §5. The overlay does not exist yet, so this is the whole enforcement
// of it: a test that fails the day someone lets a downloaded rule speak over a
// guard. ADR-0062 §1 says the line cannot be added later, which is why it is
// here before the thing it constrains.
func TestOverlayCannotOutrankAGuardHoweverSpecific(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	// Something the baseline guards, with an overlay rule that names it far more
	// precisely and calls it disposable.
	guarded := filepath.Join(home, "Library", "Keychains")
	baseline := Match(MatchContext{Path: guarded, Kind: "directory", ProjectIdleDays: NoProject})
	if baseline == nil || baseline.Guard == "" {
		t.Fatalf("%s is not guarded by the baseline; the test has nothing to check", guarded)
	}

	restore := Catalog
	t.Cleanup(func() { Catalog = restore })
	Catalog = append(append([]Rule(nil), Catalog...), Rule{
		Origin: OriginOverlay,
		Name:   "冒充的可清理项", Category: "覆盖层",
		Pattern: "Library/Keychains", Recovery: RecoveryRegenerable, DeclaredRisk: RiskSafe,
		WhatBreaks:   "（覆盖层声称没有影响）",
		HowToRestore: "（覆盖层声称自动重建）",
	})
	after := Match(MatchContext{Path: guarded, Kind: "directory", ProjectIdleDays: NoProject})
	if after == nil || after.Origin == OriginOverlay {
		t.Fatalf("an overlay rule outranked a baseline guard: %#v", after)
	}
	if after.Guard != baseline.Guard || after.Recovery != baseline.Recovery {
		t.Fatalf("the guard's answer moved: %#v", after)
	}
}

// The other half: an overlay may not introduce a guard at all, so it cannot
// invent one for a path the baseline says nothing about either.
func TestValidateOverlayRefusesAGuardAndAMissingOrigin(t *testing.T) {
	if err := ValidateOverlay([]Rule{{Origin: OriginOverlay, Name: "ok"}}); err != nil {
		t.Fatalf("a plain overlay rule was refused: %v", err)
	}
	if err := ValidateOverlay([]Rule{{Origin: OriginOverlay, Name: "带护栏", Guard: IrreplaceableUserContent}}); err == nil {
		t.Error("an overlay rule carrying a guard was accepted")
	}
	if err := ValidateOverlay([]Rule{{Name: "未声明来源"}}); err == nil {
		t.Error("an overlay rule that did not declare its origin was accepted")
	}
}
