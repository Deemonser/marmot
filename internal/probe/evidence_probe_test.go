package probe

import (
	"os"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// The gate ADR-0061 §9.2 sets: the evidence pack must be produceable and
// readable WITHOUT a model, so what is sent can be judged on its own before
// anything is wired to a network.
//
//	PROBE_ROOT=$HOME go test ./internal/probe -run Evidence -v
//	PROBE_EVIDENCE_OUT=/tmp/pack.txt to also write it out.
func TestEvidencePackOnARealTree(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-tree evidence probe")
	}
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
	})

	scanStart := time.Now()
	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var final marmotapp.ScanStatus
	for {
		final, err = service.GetScanStatus(status.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if final.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	scanElapsed := time.Since(scanStart)

	packStart := time.Now()
	pack, err := service.BuildEvidencePack(final.SnapshotID)
	if err != nil {
		t.Fatalf("build evidence pack: %v", err)
	}
	text := pack.Text()
	packElapsed := time.Since(packStart)
	findings := pack.RuleFindings()

	var ruleBytes int64
	for _, finding := range findings {
		ruleBytes += finding.ReclaimableBytes
	}

	t.Logf("扫描 %s：%d 节点 / %d 文件，用时 %.2fs", root, final.Nodes, final.Files, scanElapsed.Seconds())
	t.Logf("证据包：下限 %s，%d 个节点，%d 字节（估算 %d token），装配用时 %.0fms",
		humanBytes(pack.FloorBytes), len(pack.Nodes), len(text), len(text)/4, float64(packElapsed.Microseconds())/1000)
	t.Logf("规则命中：%d 条建议，合计 %s", len(findings), humanBytes(ruleBytes))

	if len(pack.Nodes) == 0 {
		t.Fatal("evidence pack is empty")
	}
	// ADR-0061 §9.5.
	if len(text) > 128_000 {
		t.Errorf("payload %d bytes exceeds the payload ceiling", len(text))
	}
	// The residues partition the total, on real data and not only on a fixture.
	var residue int64
	for _, node := range pack.Nodes {
		residue += node.Residue
	}
	if len(pack.Nodes) > 0 && residue != pack.Nodes[0].OwnedAllocated {
		t.Errorf("residues sum to %d but the root holds %d", residue, pack.Nodes[0].OwnedAllocated)
	}

	for index, finding := range findings {
		if index >= 15 {
			break
		}
		t.Logf("  %-10s %-22s %-14s %s", humanBytes(finding.ReclaimableBytes), finding.RuleName, finding.Risk, finding.WhatBreaks)
	}

	// The floor is not a free parameter: it is whatever the payload ceiling
	// affords. Sweeping it on a real tree is the only way to say whether the
	// default is reasonable rather than merely stated.
	t.Log("下限扫描（节点数 / 估算载荷，按实测 ~71 字节每行）：")
	for _, floor := range []int64{512_000_000, 256_000_000, 128_000_000, 64_000_000, 32_000_000, 16_000_000} {
		swept, sweepErr := store.EvidenceNodes(recommendation.EvidenceQuery{
			SnapshotID: final.SnapshotID, MinBytes: floor, MaxNodes: 200000, ExtensionsPerNode: 3,
		})
		if sweepErr != nil {
			t.Logf("  %-8s  %v", humanBytes(floor), sweepErr)
			continue
		}
		t.Logf("  %-8s  %6d 节点  ~%5.1f KB", humanBytes(floor), len(swept.Nodes), float64(len(swept.Nodes))*71/1024)
	}

	if out := os.Getenv("PROBE_EVIDENCE_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("证据包已写入 %s", out)
	}
}

func humanBytes(value int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(value)
	index := 0
	for size >= 1024 && index < len(units)-1 {
		size /= 1024
		index++
	}
	return trimFloat(size) + units[index]
}

func trimFloat(value float64) string {
	text := make([]byte, 0, 8)
	whole := int64(value)
	frac := int64((value - float64(whole)) * 10)
	text = appendInt(text, whole)
	if frac > 0 {
		text = append(text, '.')
		text = appendInt(text, frac)
	}
	return string(text)
}

func appendInt(dst []byte, value int64) []byte {
	if value == 0 {
		return append(dst, '0')
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return append(dst, digits[index:]...)
}
