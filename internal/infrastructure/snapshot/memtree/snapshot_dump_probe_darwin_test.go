package memtree

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/platform"
)

// R-078: what would a persisted copy of the finished tree cost to write, and --
// the number that decides whether a warm start is worth having -- to load?
//
// The tree is already a flat record table plus a name arena (ADR-0056), so its
// on-disk form is those pages written as they are. This probe scans a real
// volume through the application, dumps the pages, reads them back into a
// second tree, regroups it, and checks the two agree. The load time is measured
// with the file still in the page cache, which is the warm-start case a second
// launch on the same day actually meets; a cold read is bounded below by the
// disk and noted as a limitation.
//
//	PROBE_ROOT=/ go test ./internal/infrastructure/snapshot/memtree -run SnapshotDump -v -timeout 10m
func TestSnapshotDump(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to scan a real volume")
	}
	dir := os.Getenv("PROBE_DUMP_DIR")
	if dir == "" {
		dir = os.TempDir()
	}

	store := OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter, ScanTotals: adapter,
		Emit: func(string, any) {},
	})
	started := time.Now()
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
	scanWall := time.Since(started)
	// Nothing may touch the tree while it is being copied out.
	service.StopLiveUpdate()
	service.StopVolumeWatch()

	store.mu.RLock()
	original, err := store.treeFor(final.SnapshotID)
	store.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	original.ensureGrouped()

	const mib = float64(1 << 20)
	recordSize := int(unsafe.Sizeof(record{}))
	recordBytes := original.records.len() * int64(recordSize)
	arenaBytes := int64(original.names.next)
	childBytes := int64(len(original.childIDs)+len(original.childStart)+len(original.childCount)) * 4
	t.Logf("PROBE root=%s state=%s scan_wall=%.3fs nodes=%d", root, final.State, scanWall.Seconds(), final.Nodes)
	t.Logf("PROBE in-memory: records=%.1f MiB (%d x %d B, %d pages) names=%.1f MiB (%d pages) child_arrays=%.1f MiB",
		float64(recordBytes)/mib, original.records.len(), recordSize, len(original.records.pages),
		float64(arenaBytes)/mib, len(original.names.pages), float64(childBytes)/mib)
	// For R-077: the native scanner's batch cap is 8192 entries, and only a
	// directory wider than that can flush in the middle of a bulk page. How many
	// such directories does this volume have?
	var wide, widest int
	for id := range original.childCount {
		if int(original.childCount[id]) > 8192 {
			wide++
		}
		if int(original.childCount[id]) > widest {
			widest = int(original.childCount[id])
		}
	}
	t.Logf("PROBE directories wider than 8192 entries: %d (widest %d)", wide, widest)

	// Write: header, then every record page and every arena page verbatim.
	path := filepath.Join(dir, "marmot-probe.snapshot")
	defer os.Remove(path)
	writeStarted := time.Now()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 4<<20)
	header := make([]byte, 40)
	binary.LittleEndian.PutUint64(header[0:], uint64(original.records.len()))
	binary.LittleEndian.PutUint64(header[8:], uint64(len(original.records.pages)))
	binary.LittleEndian.PutUint64(header[16:], uint64(original.names.next))
	binary.LittleEndian.PutUint64(header[24:], uint64(len(original.names.pages)))
	binary.LittleEndian.PutUint64(header[32:], uint64(original.rootNodeID))
	if _, err := writer.Write(header); err != nil {
		t.Fatal(err)
	}
	for _, page := range original.records.pages {
		if _, err := writer.Write(unsafe.Slice((*byte)(unsafe.Pointer(&page[0])), len(page)*recordSize)); err != nil {
			t.Fatal(err)
		}
	}
	for _, page := range original.names.pages {
		if _, err := writer.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	writeWall := time.Since(writeStarted)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PROBE write: %.1f MiB in %.3fs (%.0f MiB/s, fsync included)", float64(info.Size())/mib, writeWall.Seconds(), float64(info.Size())/mib/writeWall.Seconds())

	// Read it back twice: the first is the realistic warm case (the file was
	// just written), the second says how much of that was still allocation.
	var loaded *tree
	for pass := range 2 {
		readStarted := time.Now()
		loaded, err = loadDump(path, original)
		if err != nil {
			t.Fatal(err)
		}
		readWall := time.Since(readStarted)
		groupStarted := time.Now()
		loaded.group()
		groupWall := time.Since(groupStarted)
		t.Logf("PROBE load pass=%d: read=%.3fs (%.0f MiB/s) group=%.3fs total=%.3fs",
			pass, readWall.Seconds(), float64(info.Size())/mib/readWall.Seconds(), groupWall.Seconds(), (readWall + groupWall).Seconds())
	}

	// Agreement: same children under the root, same paths and records on a
	// stride through the table.
	if got, want := len(loaded.children(loaded.rootNodeID)), len(original.children(original.rootNodeID)); got != want {
		t.Fatalf("root children: loaded %d, original %d", got, want)
	}
	mismatches := 0
	checked := 0
	for id := int64(1); id < original.records.len(); id += 997 {
		checked++
		if *original.records.at(id) != *loaded.records.at(id) || original.path(id) != loaded.path(id) {
			mismatches++
		}
	}
	t.Logf("PROBE agreement: %d sampled nodes, %d mismatches", checked, mismatches)
	if mismatches > 0 {
		t.Fatalf("loaded tree disagrees with the original on %d of %d sampled nodes", mismatches, checked)
	}
}

func loadDump(path string, original *tree) (*tree, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 4<<20)
	header := make([]byte, 40)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	length := int64(binary.LittleEndian.Uint64(header[0:]))
	recordPages := int(binary.LittleEndian.Uint64(header[8:]))
	arenaNext := uint32(binary.LittleEndian.Uint64(header[16:]))
	arenaPages := int(binary.LittleEndian.Uint64(header[24:]))
	rootNodeID := int64(binary.LittleEndian.Uint64(header[32:]))

	loaded := newTree(original.taskID, original.root)
	loaded.records = recordTable{}
	loaded.records.grow(int64(recordPages) * recordsPerPage)
	loaded.records.length = length
	recordSize := int(unsafe.Sizeof(record{}))
	for _, page := range loaded.records.pages {
		if _, err := io.ReadFull(reader, unsafe.Slice((*byte)(unsafe.Pointer(&page[0])), len(page)*recordSize)); err != nil {
			return nil, err
		}
	}
	loaded.names = arena{next: arenaNext}
	for range arenaPages {
		page := make([]byte, arenaPageSize)
		if _, err := io.ReadFull(reader, page); err != nil {
			return nil, err
		}
		loaded.names.pages = append(loaded.names.pages, page)
	}
	// The interned code tables are a few strings; a real format would carry
	// them in the header. The probe shares the originals.
	loaded.kinds, loaded.volumes, loaded.bases, loaded.confidences = original.kinds, original.volumes, original.bases, original.confidences
	loaded.rootNodeID = rootNodeID
	return loaded, nil
}
