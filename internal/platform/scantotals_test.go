package platform

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/marmot/internal/ports"
)

func TestScanTotalsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	adapter := Adapter{}
	if got := adapter.LoadScanTotal("/"); got != (ports.ScanTotal{}) {
		t.Fatalf("no history yet, got %+v", got)
	}
	if err := adapter.StoreScanTotal("/", ports.ScanTotal{Bytes: 187_628_670_976, Nodes: 2_415_814}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.StoreScanTotal("/Volumes/Work", ports.ScanTotal{Bytes: 42, Nodes: 3}); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != (ports.ScanTotal{Bytes: 187_628_670_976, Nodes: 2_415_814}) {
		t.Fatalf("stored total lost: %+v", got)
	}
	if got := adapter.LoadScanTotal("/Volumes/Work"); got != (ports.ScanTotal{Bytes: 42, Nodes: 3}) {
		t.Fatalf("second root clobbered: %+v", got)
	}

	// A corrupt file downgrades to "no history"; it must never error a scan.
	path := filepath.Join(home, "Library", "Application Support", "marmot", "scan-totals.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("totals file not where expected: %v", err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != (ports.ScanTotal{}) {
		t.Fatalf("corrupt history should read as none, got %+v", got)
	}
	if err := adapter.StoreScanTotal("/", ports.ScanTotal{Bytes: 7, Nodes: 1}); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != (ports.ScanTotal{Bytes: 7, Nodes: 1}) {
		t.Fatalf("store over a corrupt file failed: %+v", got)
	}
}

// History written by the first release is a bare byte count per root. It must
// keep working as the byte denominator, with no node history, until the next
// completed walk rewrites it in the new shape.
func TestScanTotalsReadTheLegacyBareByteFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "Library", "Application Support", "marmot", "scan-totals.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"/":198002835456}`), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{}
	if got := adapter.LoadScanTotal("/"); got != (ports.ScanTotal{Bytes: 198002835456}) {
		t.Fatalf("legacy history misread: %+v", got)
	}
	if err := adapter.StoreScanTotal("/", ports.ScanTotal{Bytes: 5, Nodes: 2}); err != nil {
		t.Fatal(err)
	}
	if got := adapter.LoadScanTotal("/"); got != (ports.ScanTotal{Bytes: 5, Nodes: 2}) {
		t.Fatalf("rewrite after legacy read failed: %+v", got)
	}
}
