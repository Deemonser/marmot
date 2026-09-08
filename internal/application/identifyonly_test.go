package application

import (
	"path/filepath"
	"testing"

	"example.com/marmot/internal/domain/scan"
)

// The regression ADR-0068 introduced and did not notice. Merging the location
// guards into the catalog made them rules, and every rule hit is a finding: so
// ~/Documents reached RuleFindings as an 8 GB "irreplaceable, risky" proposal at
// the top of the list, and ~/.ssh beside it. Invisible only because the advice
// surface was off the screen at the time.
//
// An identification names an object. A proposal offers it. The catalog says
// which is which, and the evidence pack must honour it at the source.
func TestIdentifyOnlyRulesNeverBecomeFindings(t *testing.T) {
	// describeStore roots its snapshot at /Users/tester and memtree rebuilds every
	// path from that root, so the paths asserted on must be built from it too --
	// not from the real home, which is what made the first version of this test
	// pass vacuously.
	home := "/Users/tester"
	docs := filepath.Join(home, "Documents")
	keys := filepath.Join(home, ".ssh")
	cache := filepath.Join(home, "Library", "Caches", "go-build")
	inside := filepath.Join(docs, "proj", "node_modules")
	const gb = 1 << 30
	service, snapshotID := describeStore(t, []scan.Node{
		{ID: 1, Path: home, Name: filepath.Base(home), Kind: "directory", HasChildren: true, OwnedAllocated: 14 * gb},
		{ID: 2, ParentID: 1, Path: docs, Name: "Documents", Kind: "directory", HasChildren: true, OwnedAllocated: 10 * gb},
		{ID: 3, ParentID: 2, Path: filepath.Join(docs, "thesis.pdf"), Name: "thesis.pdf", Kind: "file", OwnedAllocated: 8 * gb},
		{ID: 4, ParentID: 2, Path: filepath.Join(docs, "proj"), Name: "proj", Kind: "directory", HasChildren: true, OwnedAllocated: 2 * gb},
		{ID: 5, ParentID: 4, Path: inside, Name: "node_modules", Kind: "directory", HasChildren: true, OwnedAllocated: 2 * gb},
		{ID: 6, ParentID: 5, Path: filepath.Join(inside, "left-pad"), Name: "left-pad", Kind: "file", OwnedAllocated: 2 * gb},
		{ID: 7, ParentID: 1, Path: keys, Name: ".ssh", Kind: "directory", HasChildren: true, OwnedAllocated: 1 * gb},
		{ID: 8, ParentID: 7, Path: filepath.Join(keys, "id_ed25519"), Name: "id_ed25519", Kind: "file", OwnedAllocated: 1 * gb},
		{ID: 9, ParentID: 1, Path: filepath.Join(home, "Library"), Name: "Library", Kind: "directory", HasChildren: true, OwnedAllocated: 3 * gb},
		{ID: 10, ParentID: 9, Path: filepath.Join(home, "Library", "Caches"), Name: "Caches", Kind: "directory", HasChildren: true, OwnedAllocated: 3 * gb},
		{ID: 11, ParentID: 10, Path: cache, Name: "go-build", Kind: "directory", HasChildren: true, OwnedAllocated: 3 * gb},
		{ID: 12, ParentID: 11, Path: filepath.Join(cache, "aa"), Name: "aa", Kind: "file", OwnedAllocated: 3 * gb},
	})
	pack, err := service.buildEvidencePackFor(snapshotID, 1, 100<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]AdviceItem{}
	for _, finding := range pack.RuleFindings() {
		byPath[finding.Path] = finding
	}
	for _, guarded := range []string{docs, keys} {
		if finding, leaked := byPath[guarded]; leaked {
			t.Errorf("%s was proposed as %q (%s, %s)", guarded, finding.RuleName, finding.Recovery, finding.DeclaredRisk)
		}
	}
	// The guard must not take its neighbourhood with it: a real cache stays a
	// finding, and so does a node_modules that happens to live under Documents --
	// the second was silently dropped by the `ruled` cover while the guard was a
	// hit, which is why the exclusion is at the source and not at the exit.
	for _, proposed := range []string{cache, inside} {
		if _, found := byPath[proposed]; !found {
			t.Errorf("%s is a cleanup candidate and was not proposed", proposed)
		}
	}
	// And the panel still gets the identification: only the proposal is withheld.
	described, err := service.DescribeNode(snapshotID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if described.Rule == "" || described.Recovery != "irreplaceable" {
		t.Errorf("~/Documents lost its identification along with its proposal: %#v", described)
	}
}
