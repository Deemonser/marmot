package binarysnapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
)

func TestRoundTripPaginationMapAndIssues(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewWriter(Config{Directory: directory, SnapshotID: 7, TaskID: "task-7", Root: "/test-root"})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []scan.Node{
		fixtureNode(1, 0, "root", "directory", 0, true),
		fixtureNode(2, 1, "large", "directory", 100, true),
		fixtureNode(3, 1, "medium", "file", 50, false),
		fixtureNode(4, 1, "small", "file", 10, false),
		fixtureNode(5, 2, "inside", "file", 25, false),
	}
	if err := writer.AppendBatch(nodes[:3], nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendBatch(nodes[3:], []scan.Issue{{Path: "/test-root/restricted", Message: "permission denied"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Publish(string(scan.PhaseDeepScan)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := Open(directory, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	manifest := reader.Manifest()
	if manifest.NodeCount != 5 || manifest.FileCount != 3 || manifest.DirectoryCount != 2 || manifest.IssueCount != 1 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	root, err := reader.NodeByPath("/test-root")
	if err != nil {
		t.Fatal(err)
	}
	if root.ID != 1 || root.Path != "/test-root" {
		t.Fatalf("unexpected root: %+v", root)
	}
	inside, err := reader.NodeByID(5)
	if err != nil {
		t.Fatal(err)
	}
	if inside.Path != "/test-root/large/inside" {
		t.Fatalf("unexpected reconstructed path: %+v", inside)
	}

	children, err := reader.Children(1, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int64{children[0].ID, children[1].ID, children[2].ID}; !equalInt64(got, []int64{2, 3, 4}) {
		t.Fatalf("children are not stably sorted: %v", got)
	}
	page, err := reader.Children(1, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != 3 || page[1].ID != 4 {
		t.Fatalf("unexpected page: %+v", page)
	}

	result, err := reader.Map(scan.MapQuery{SnapshotID: 7, ParentID: 1, Limit: 2, Depth: 1, ProjectionLimit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || result.Entries[0].Node.ID != 2 || result.Entries[1].Kind != "aggregate" || result.Entries[1].Count != 2 || result.Entries[1].LogicalSize != 120 || result.Entries[1].AllocatedSize != 60 || result.Entries[1].OwnedAllocated != 60 {
		t.Fatalf("unexpected map result: %+v", result)
	}
	if len(result.Entries[0].Children) != 1 || result.Entries[0].Children[0].Node.ID != 5 {
		t.Fatalf("nested projection missing: %+v", result.Entries[0])
	}

	issues, err := reader.Issues()
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Path != "/test-root/restricted" || issues[0].Message != "permission denied" {
		t.Fatalf("unexpected issues: %+v", issues)
	}

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err = NewWriter(Config{Directory: t.TempDir(), SnapshotID: 8, TaskID: "task-8", Root: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.AppendBatch([]scan.Node{fixtureNode(1, 0, "root", "directory", 0, false)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(scan.JobCompleted, string(scan.PhaseFinalize), ""); err != nil {
		t.Fatal(err)
	}
	if writer.Manifest().State != scan.JobCompleted || writer.Manifest().FinishedAt == 0 {
		t.Fatalf("finish did not update manifest: %+v", writer.Manifest())
	}
}

func TestTailTruncationUsesLastCommittedFooter(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewWriter(Config{Directory: directory, SnapshotID: 9, TaskID: "task-9", Root: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendBatch([]scan.Node{fixtureNode(1, 0, "root", "directory", 0, true), fixtureNode(2, 1, "file", "file", 5, false)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(scan.JobCompleted, string(scan.PhaseFinalize), ""); err != nil {
		t.Fatal(err)
	}
	paths := writer.paths
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(paths.data, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	partial := encodeBatchHeader(2, 1, 0, nodeRecordSize, 0, 0)
	if _, err := file.Write(partial[:19]); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := Open(directory, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Manifest().NodeCount != 2 {
		t.Fatalf("tail changed committed manifest: %+v", reader.Manifest())
	}
	children, err := reader.Children(1, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].ID != 2 {
		t.Fatalf("unexpected children after tail recovery: %+v", children)
	}
	state, err := scanData(reader.data, reader.dataSize, 9)
	if err != nil {
		t.Fatal(err)
	}
	if state.validEnd != reader.Manifest().DataEnd || state.nodeCount != 2 {
		t.Fatalf("unexpected recovery state: %+v manifest=%+v", state, reader.Manifest())
	}
}

func TestIndexChecksumAndManifestTempRecovery(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewWriter(Config{Directory: directory, SnapshotID: 10, TaskID: "task-10", Root: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendBatch([]scan.Node{fixtureNode(1, 0, "root", "directory", 0, false)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(scan.JobCompleted, string(scan.PhaseFinalize), ""); err != nil {
		t.Fatal(err)
	}
	paths := writer.paths
	manifestBefore, err := os.ReadFile(paths.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.manifest+".tmp-interrupted", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	manifestAfter, err := os.ReadFile(paths.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("temporary manifest changed the committed manifest")
	}

	indexPath := filepath.Join(directory, writer.Manifest().IndexFile)
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes[len(indexBytes)-1] ^= 0x01
	if err := os.WriteFile(indexPath, indexBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory, 10); err == nil {
		t.Fatal("corrupted index checksum was accepted")
	}
}

func TestRejectsManifestBeyondCommittedData(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewWriter(Config{Directory: directory, SnapshotID: 11, TaskID: "task-11", Root: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendBatch([]scan.Node{fixtureNode(1, 0, "root", "directory", 0, false)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(scan.JobCompleted, string(scan.PhaseFinalize), ""); err != nil {
		t.Fatal(err)
	}
	paths := writer.paths
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(paths.manifest)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.DataEnd += 100
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.manifest, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory, 11); err == nil {
		t.Fatal("manifest beyond committed data was accepted")
	}
}

func TestCommittedBatchChecksumIsRequired(t *testing.T) {
	directory := t.TempDir()
	writer, err := NewWriter(Config{Directory: directory, SnapshotID: 13, TaskID: "task-13", Root: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendBatch([]scan.Node{fixtureNode(1, 0, "root", "directory", 0, false)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(scan.JobCompleted, string(scan.PhaseFinalize), ""); err != nil {
		t.Fatal(err)
	}
	dataPath := writer.paths.data
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.OpenFile(dataPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	byteAtNode := int64(dataHeaderSize + batchHeaderSize)
	if _, err := data.WriteAt([]byte{0xff}, byteAtNode); err != nil {
		_ = data.Close()
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory, 13); err == nil {
		t.Fatal("corrupted committed data was accepted")
	}
}

func TestMillionNodePOC(t *testing.T) {
	if os.Getenv("MARMOT_RUN_SNAPSHOT_POC") == "" {
		t.Skip("set MARMOT_RUN_SNAPSHOT_POC=1 to run the million-node POC")
	}
	const nodeCount = 1_000_000
	directory := t.TempDir()
	started := time.Now()
	writer, err := NewWriter(Config{Directory: directory, SnapshotID: 12, TaskID: "task-12", Root: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	const batchSize = 8192
	for start := 0; start < nodeCount; start += batchSize {
		end := start + batchSize
		if end > nodeCount {
			end = nodeCount
		}
		nodes := make([]scan.Node, 0, end-start)
		for id := start; id < end; id++ {
			if id == 0 {
				nodes = append(nodes, fixtureNode(1, 0, "root", "directory", 0, true))
				continue
			}
			node := fixtureNode(int64(id+1), 1, fmt.Sprintf("file-%07d", id), "file", int64(id%1000+1), false)
			nodes = append(nodes, node)
		}
		if err := writer.AppendBatch(nodes, nil); err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
	}
	if err := writer.Finish(scan.JobCompleted, string(scan.PhaseFinalize), ""); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	paths := writer.paths
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(directory, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Manifest().NodeCount != nodeCount {
		t.Fatalf("expected %d nodes, got %d", nodeCount, reader.Manifest().NodeCount)
	}
	firstPage, err := reader.Children(1, maxPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	lastPage, err := reader.Children(1, maxPageSize, nodeCount-1-maxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != maxPageSize || len(lastPage) != maxPageSize {
		t.Fatalf("unexpected pages: first=%d last=%d", len(firstPage), len(lastPage))
	}
	mapStarted := time.Now()
	result, err := reader.Map(scan.MapQuery{SnapshotID: 12, ParentID: 1, Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 256 || result.Entries[len(result.Entries)-1].Kind != "aggregate" || result.Entries[len(result.Entries)-1].Count != nodeCount-1-255 {
		t.Fatalf("unexpected million-node map result: entries=%d remaining=%+v", len(result.Entries), result.Remaining)
	}
	t.Logf("million-node POC: nodes=%d data=%d index=%d write-read=%s map=%s", reader.Manifest().NodeCount, fileSize(t, paths.data), fileSize(t, filepath.Join(directory, reader.Manifest().IndexFile)), time.Since(started), time.Since(mapStarted))
}

func fixtureNode(id, parentID int64, name, kind string, owned int64, hasChildren bool) scan.Node {
	return scan.Node{
		ID: id, ParentID: parentID, Name: name, Kind: kind,
		LogicalSize: owned * 2, AllocatedSize: owned, OwnedAllocated: owned,
		VolumeID: "volume-1", Confidence: "exact", SizeBasis: "poc_v1",
		Device: 1, Inode: uint64(id), ModifiedAt: time.Unix(1700000000, id), HasChildren: hasChildren,
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func equalInt64(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
