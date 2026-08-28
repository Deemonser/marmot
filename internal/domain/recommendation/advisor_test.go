package recommendation

import (
	"testing"
)

func shownNodes(nodes ...EvidenceNode) map[int64]EvidenceNode {
	index := make(map[int64]EvidenceNode, len(nodes))
	for _, node := range nodes {
		index[node.ID] = node
	}
	return index
}

func node(id int64, path, name string, bytes int64) EvidenceNode {
	return EvidenceNode{ID: id, Path: path, Name: name, Kind: "directory", OwnedAllocated: bytes}
}

func goodSuggestion(id int64, name string) Suggestion {
	return Suggestion{
		NodeID: id, Name: name, Category: "缓存",
		Recovery: string(RecoveryRegenerable), Risk: string(RiskSafe), Confidence: 0.9,
		Evidence:   []string{"占用 6GB", "90 天未改动"},
		WhatBreaks: "应用下次启动会慢一些。", HowToRestore: "自动重建。",
	}
}

// An id the advisor was never shown is not a suggestion it could have reasoned
// about, whether it invented the id or guessed one that happens to exist.
func TestValidateRejectsNodesTheAdvisorWasNotShown(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/Library/Caches", "Caches", 100))
	result := Validate([]Suggestion{goodSuggestion(2, "Whatever")}, shown, 7)
	if len(result.Accepted) != 0 {
		t.Fatalf("accepted a suggestion for an unshown node: %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != RejectUnknownNode {
		t.Fatalf("expected one unknown_node rejection, got %#v", result.Rejected)
	}
}

// An id that exists but names something else means the model lost track of which
// row it meant. Applying it to whatever holds that id is how a cleanup tool
// deletes the wrong thing.
func TestValidateRejectsANameThatDoesNotMatch(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/Library/Caches", "Caches", 100))
	result := Validate([]Suggestion{goodSuggestion(1, "DerivedData")}, shown, 7)
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != RejectNameMismatch {
		t.Fatalf("expected name_mismatch, got %#v", result.Rejected)
	}
}

// A collapsed row is labelled `a/b/c`, so echoing back any tail of it is the
// model being accurate, not hallucinating.
func TestValidateAcceptsASuffixOfACollapsedLabel(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/x/hash/transformed/react-android", "react-android", 100))
	for _, claimed := range []string{
		"react-android", "transformed/react-android", "/Users/a/x/hash/transformed/react-android",
		// A verbatim echo of a rendered row. Measured against a real advisor:
		// every one of 31 suggestions came back annotated this way, and
		// rejecting them cost the whole run.
		"react-android(d)", "transformed/react-android (d)",
	} {
		result := Validate([]Suggestion{goodSuggestion(1, claimed)}, shown, 7)
		if len(result.Accepted) != 1 {
			t.Fatalf("claim %q was rejected: %#v", claimed, result.Rejected)
		}
	}
}

func TestValidateRejectsProtectedObjects(t *testing.T) {
	shown := shownNodes(node(1, "/Users/alice", "alice", 100), node(2, "/System", "System", 100))
	result := Validate([]Suggestion{goodSuggestion(1, "alice"), goodSuggestion(2, "System")}, shown, 7)
	if len(result.Accepted) != 0 {
		t.Fatalf("a protected object was accepted: %#v", result.Accepted)
	}
	for _, item := range result.Rejected {
		if item.Reason != RejectProtected {
			t.Fatalf("expected protected, got %#v", item)
		}
	}
}

func TestValidateRejectsMalformedItems(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/x", "x", 100))
	cases := map[string]func(Suggestion) Suggestion{
		"unknown recovery":     func(s Suggestion) Suggestion { s.Recovery = "maybe"; return s },
		"unknown risk":         func(s Suggestion) Suggestion { s.Risk = "probably"; return s },
		"confidence above one": func(s Suggestion) Suggestion { s.Confidence = 1.7; return s },
		"no what breaks":       func(s Suggestion) Suggestion { s.WhatBreaks = "  "; return s },
		"no how to restore":    func(s Suggestion) Suggestion { s.HowToRestore = ""; return s },
		// The one combination that turns a suggestion into a trap. The model is
		// told not to; being told is not the same as complying.
		"irreplaceable and safe": func(s Suggestion) Suggestion {
			s.Recovery = string(RecoveryIrreplaceable)
			s.Risk = string(RiskSafe)
			return s
		},
	}
	for name, mutate := range cases {
		result := Validate([]Suggestion{mutate(goodSuggestion(1, "x"))}, shown, 7)
		if len(result.Accepted) != 0 {
			t.Fatalf("%s was accepted", name)
		}
		if len(result.Rejected) != 1 || result.Rejected[0].Reason != RejectMalformed {
			t.Fatalf("%s: expected malformed, got %#v", name, result.Rejected)
		}
	}
}

func TestValidateCollapsesOverlapToTheOutermost(t *testing.T) {
	shown := shownNodes(
		node(1, "/Users/a/Library/Caches", "Caches", 600),
		node(2, "/Users/a/Library/Caches/com.example", "com.example", 500),
	)
	result := Validate([]Suggestion{goodSuggestion(2, "com.example"), goodSuggestion(1, "Caches")}, shown, 7)
	if len(result.Accepted) != 1 || result.Accepted[0].NodeID != 1 {
		t.Fatalf("expected only the outer node, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != RejectOverlapping {
		t.Fatalf("expected an overlap rejection, got %#v", result.Rejected)
	}
}

// The size is the snapshot's, always. The advisor is never asked for one, so
// there is no number of its own to reconcile against the wheel.
func TestValidateTakesSizeFromTheSnapshot(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/x", "x", 12345))
	result := Validate([]Suggestion{goodSuggestion(1, "x")}, shown, 7)
	if len(result.Accepted) != 1 || result.Accepted[0].ReclaimableBytes != 12345 {
		t.Fatalf("expected the snapshot's 12345 bytes, got %#v", result.Accepted)
	}
	if result.Accepted[0].Source != SourceAdvisor {
		t.Fatalf("an advisor suggestion must be labelled as one: %#v", result.Accepted[0])
	}
}

func TestLimitExpansionsIsBoundedAndDeduplicated(t *testing.T) {
	nodes := []EvidenceNode{}
	requested := []Expansion{}
	for id := int64(1); id <= 20; id++ {
		nodes = append(nodes, node(id, "/Users/a/x", "x", 100))
		requested = append(requested, Expansion{NodeID: id})
	}
	// A repeat, an unknown id, and a file rather than a directory.
	requested = append(requested, Expansion{NodeID: 1}, Expansion{NodeID: 999})
	file := node(21, "/Users/a/f", "f", 100)
	file.Kind = "file"
	nodes = append(nodes, file)
	requested = append(requested, Expansion{NodeID: 21})

	focus := LimitExpansions(requested, shownNodes(nodes...))
	if len(focus) != MaxExpansions {
		t.Fatalf("expected the bound of %d, got %d", MaxExpansions, len(focus))
	}
	seen := map[int64]bool{}
	for _, id := range focus {
		if seen[id] {
			t.Fatalf("duplicate expansion for %d", id)
		}
		seen[id] = true
	}
}

func TestRejectionSummaryNamesWhatWasRefused(t *testing.T) {
	summary := RejectionSummary([]Rejection{
		{Reason: RejectUnknownNode}, {Reason: RejectUnknownNode}, {Reason: RejectProtected},
	})
	if summary == "" {
		t.Fatal("a refusal that is not reported is a refusal the user cannot weigh")
	}
}

// A collapsed row reads `a/b/c` while the node's own path ends at `a`.
// Validating against the path alone rejected a faithful quotation of what the
// advisor was shown -- measured against a real model, 19 correct suggestions.
func TestValidateAcceptsTheLabelTheRowWasRenderedWith(t *testing.T) {
	target := node(1, "/Users/a/.gradle/wrapper", "wrapper", 100)
	target.Label = "wrapper/dists"
	shown := shownNodes(target)
	for _, claimed := range []string{"wrapper/dists", "dists", "wrapper", "dists(d)"} {
		result := Validate([]Suggestion{goodSuggestion(1, claimed)}, shown, 7)
		if len(result.Accepted) != 1 {
			t.Fatalf("claim %q was rejected: %#v", claimed, result.Rejected)
		}
	}
	// Tolerance for a faithful echo is not tolerance for a wrong name.
	for _, claimed := range []string{"ists", "somethingelse", "wrapper/other"} {
		result := Validate([]Suggestion{goodSuggestion(1, claimed)}, shown, 7)
		if len(result.Accepted) != 0 {
			t.Fatalf("claim %q should not have matched", claimed)
		}
	}
}
