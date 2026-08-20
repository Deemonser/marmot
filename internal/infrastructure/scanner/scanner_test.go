package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"example.com/marmot/internal/domain/scan"
)

func TestScanIsDeterministicAndDeduplicatesHardlinks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "one.bin")
	if err := os.WriteFile(file, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(root, "one-link.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "two.bin"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	var nodes []Node
	var phases []scan.Phase
	result, err := Scan(context.Background(), root, func(node Node) error {
		nodes = append(nodes, node)
		return nil
	}, func(phase scan.Phase) error {
		phases = append(phases, phase)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 || result.Files != 3 || result.Directories != 2 {
		t.Fatalf("unexpected scan result: nodes=%d files=%d dirs=%d", len(nodes), result.Files, result.Directories)
	}
	if nodes[1].Name != "nested" || nodes[2].Name != "one-link.bin" || nodes[3].Name != "one.bin" || nodes[4].Name != "two.bin" {
		t.Fatalf("unexpected staged scan order: %#v", nodes)
	}
	if nodes[2].OwnedAllocated == 0 || nodes[3].OwnedAllocated != 0 {
		t.Fatalf("hardlink ownership was not deterministic: link=%d original=%d", nodes[2].OwnedAllocated, nodes[3].OwnedAllocated)
	}
	expectedPhases := []scan.Phase{scan.PhaseCatalog, scan.PhaseVolumeOverview, scan.PhaseTopLevelPublish, scan.PhaseDeepScan, scan.PhaseFinalize}
	if len(phases) != len(expectedPhases) {
		t.Fatalf("unexpected phase sequence: %#v", phases)
	}
	for i, expected := range expectedPhases {
		if phases[i] != expected {
			t.Fatalf("unexpected phase at %d: got %s want %s", i, phases[i], expected)
		}
	}
	rootSize := result.DirectorySizes[1]
	if rootSize.LogicalSize <= rootSize.OwnedAllocated {
		t.Fatalf("logical and owned sizes were collapsed: %#v", rootSize)
	}
}

func TestScanCanBeCancelled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(root, filepath.Base(t.Name())+string(rune('a'+i))), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Scan(ctx, root, func(Node) error { return nil }, nil)
	if err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
