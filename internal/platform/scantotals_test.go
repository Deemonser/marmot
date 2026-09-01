package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanTotalsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	adapter := Adapter{}
	if got := adapter.LoadScanTotal("/"); got != 0 {
		t.Fatalf("no history yet, got %d", got)
	}
	if err := adapter.StoreScanTotal("/", 187_628_670_976); err != nil {
		t.Fatal(err)
	}
	if err := adapter.StoreScanTotal("/Volumes/Work", 42); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != 187_628_670_976 {
		t.Fatalf("stored total lost: %d", got)
	}
	if got := adapter.LoadScanTotal("/Volumes/Work"); got != 42 {
		t.Fatalf("second root clobbered: %d", got)
	}

	// A corrupt file downgrades to "no history"; it must never error a scan.
	path := filepath.Join(home, "Library", "Application Support", "marmot", "scan-totals.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("totals file not where expected: %v", err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != 0 {
		t.Fatalf("corrupt history should read as none, got %d", got)
	}
	if err := adapter.StoreScanTotal("/", 7); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != 7 {
		t.Fatalf("store over a corrupt file failed: %d", got)
	}
}
