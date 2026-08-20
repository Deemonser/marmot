package wails

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrimMapPayloadDropsProjectionBeforeCompactingEntries(t *testing.T) {
	children := make([]MapEntry, 0, 120)
	for index := 0; index < 120; index++ {
		children = append(children, MapEntry{Kind: "node", Name: strings.Repeat("child", 600), Node: NodeView{ID: int64(index + 2), Name: strings.Repeat("child", 600)}})
	}
	result := MapResult{
		SnapshotID: 1,
		Parent:     NodeView{ID: 1, Name: "root"},
		Entries:    []MapEntry{{Kind: "node", Name: "root-child", Node: NodeView{ID: 2}, Children: children}},
	}

	trimmed := trimMapPayload(result)

	if !trimmed.ProjectionTruncated {
		t.Fatal("projection trim should be visible to the caller")
	}
	if len(trimmed.Entries) != 1 || len(trimmed.Entries[0].Children) != 0 || !trimmed.Entries[0].ChildrenHasMore {
		t.Fatalf("projection children were not compacted: %#v", trimmed.Entries)
	}
	assertPayloadLimit(t, trimmed)
}

func TestTrimMapPayloadAggregatesOmittedNodesOnce(t *testing.T) {
	entries := make([]MapEntry, 0, 900)
	for index := 0; index < 900; index++ {
		entries = append(entries, MapEntry{
			Kind:           "node",
			Name:           strings.Repeat("entry", 100),
			OwnedAllocated: 3,
			Node: NodeView{
				ID:   int64(index + 1),
				Name: strings.Repeat("entry", 100),
				Path: strings.Repeat("/long/path", 35),
			},
		})
	}
	result := MapResult{
		SnapshotID: 1,
		Parent:     NodeView{ID: 1, Name: "root"},
		Entries:    entries,
		Remaining:  MapEntry{Kind: "aggregate", Count: 7, OwnedAllocated: 11},
	}

	trimmed := trimMapPayload(result)

	assertPayloadLimit(t, trimmed)
	if len(trimmed.Entries) < 2 || trimmed.Entries[len(trimmed.Entries)-1].Kind != "aggregate" {
		t.Fatalf("expected compacted aggregate entry: %#v", trimmed.Entries)
	}
	aggregate := trimmed.Entries[len(trimmed.Entries)-1]
	kept := len(trimmed.Entries) - 1
	wantCount := int64(len(entries)-kept) + result.Remaining.Count
	wantSize := int64(len(entries)-kept)*3 + result.Remaining.OwnedAllocated
	if aggregate.Count != wantCount || aggregate.OwnedAllocated != wantSize {
		t.Fatalf("omitted entries were counted incorrectly: got count=%d size=%d, want count=%d size=%d", aggregate.Count, aggregate.OwnedAllocated, wantCount, wantSize)
	}
}

func assertPayloadLimit(t *testing.T, result MapResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxMapPayloadBytes {
		t.Fatalf("map payload exceeds limit: %d > %d", len(encoded), maxMapPayloadBytes)
	}
}
