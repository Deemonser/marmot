package wails

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"example.com/marmot/internal/application"
)

// The one failure EventView cannot hand to the compiler: a payload from the
// application layer with no case of its own. It is returned unchanged, the
// emitter then cancels the event, and the feature that depended on it simply
// never moves. The log line is the only thing standing between that and an
// afternoon of reading the frontend, so it is worth a test of its own.
func TestEventViewReportsAnApplicationPayloadWithNoCase(t *testing.T) {
	var logged bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(restore) })

	// Not an event payload today, which is the point: it stands for the one that
	// gets emitted tomorrow without a case being added here.
	if got := EventView(application.LiveUpdateStatus{}); got != any(application.LiveUpdateStatus{}) {
		t.Errorf("EventView changed an unhandled payload to %#v; it must pass through", got)
	}
	if !strings.Contains(logged.String(), "application.LiveUpdateStatus") {
		t.Errorf("an unconverted application payload was not reported; the log said %q", logged.String())
	}
}

// Types that are nobody's business here go through untouched and unremarked --
// the check is scoped to the application package, not to everything.
func TestEventViewLeavesForeignValuesAlone(t *testing.T) {
	var logged bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(restore) })

	if got := EventView(nil); got != nil {
		t.Errorf("EventView(nil) = %#v, want nil", got)
	}
	if got := EventView("plain"); got != "plain" {
		t.Errorf("EventView(%q) = %#v, want it unchanged", "plain", got)
	}
	if logged.Len() > 0 {
		t.Errorf("nothing from the application package was passed, but the log said %q", logged.String())
	}
}

func TestTrimMapPayloadDropsProjectionBeforeCompactingEntries(t *testing.T) {
	children := make([]ProjectedEntry, 0, 120)
	for index := 0; index < 120; index++ {
		children = append(children, ProjectedEntry{Kind: "file", Name: strings.Repeat("child", 600), NodeID: int64(index + 2)})
	}
	result := MapResult{
		SnapshotID: 1,
		Parent:     NodeView{ID: 1, Name: "root"},
		Entries:    []MapEntry{{Kind: "node", Name: "root-child", Node: NodeView{ID: 2}, Children: children}},
	}

	trimmed := trimMapPayload(result)

	if !trimmed.DensityTruncated {
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
