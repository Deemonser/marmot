package recommendation

import (
	"strings"
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

func goodSuggestion(id int64, name string) Verdict {
	return Verdict{
		NodeID: id, Name: name, Verdict: VerdictCleanable, Category: "缓存",
		Recovery: string(RecoveryRegenerable), Risk: string(RiskSafe), Confidence: 0.9,
		Evidence:   []string{"占用 6GB", "90 天未改动"},
		WhatBreaks: "应用下次启动会慢一些。", HowToRestore: "自动重建。",
	}
}

// An id the advisor was never shown is not a suggestion it could have reasoned
// about, whether it invented the id or guessed one that happens to exist.
func TestValidateRejectsNodesTheAdvisorWasNotShown(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/Library/Caches", "Caches", 100))
	result := Validate([]Verdict{goodSuggestion(2, "Whatever")}, shown, 7)
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
	result := Validate([]Verdict{goodSuggestion(1, "DerivedData")}, shown, 7)
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
		result := Validate([]Verdict{goodSuggestion(1, claimed)}, shown, 7)
		if len(result.Accepted) != 1 {
			t.Fatalf("claim %q was rejected: %#v", claimed, result.Rejected)
		}
	}
}

func TestValidateRejectsProtectedObjects(t *testing.T) {
	shown := shownNodes(node(1, "/Users/alice", "alice", 100), node(2, "/System", "System", 100))
	result := Validate([]Verdict{goodSuggestion(1, "alice"), goodSuggestion(2, "System")}, shown, 7)
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
	cases := map[string]func(Verdict) Verdict{
		"unknown recovery":     func(s Verdict) Verdict { s.Recovery = "maybe"; return s },
		"unknown risk":         func(s Verdict) Verdict { s.Risk = "probably"; return s },
		"confidence above one": func(s Verdict) Verdict { s.Confidence = 1.7; return s },
		"no what breaks":       func(s Verdict) Verdict { s.WhatBreaks = "  "; return s },
		"no how to restore":    func(s Verdict) Verdict { s.HowToRestore = ""; return s },
		// The one combination that turns a suggestion into a trap. The model is
		// told not to; being told is not the same as complying.
		"irreplaceable and safe": func(s Verdict) Verdict {
			s.Recovery = string(RecoveryIrreplaceable)
			s.Risk = string(RiskSafe)
			return s
		},
	}
	for name, mutate := range cases {
		result := Validate([]Verdict{mutate(goodSuggestion(1, "x"))}, shown, 7)
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
	result := Validate([]Verdict{goodSuggestion(2, "com.example"), goodSuggestion(1, "Caches")}, shown, 7)
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
	result := Validate([]Verdict{goodSuggestion(1, "x")}, shown, 7)
	if len(result.Accepted) != 1 || result.Accepted[0].ReclaimableBytes != 12345 {
		t.Fatalf("expected the snapshot's 12345 bytes, got %#v", result.Accepted)
	}
	if result.Accepted[0].Source != SourceAdvisor {
		t.Fatalf("an advisor suggestion must be labelled as one: %#v", result.Accepted[0])
	}
}

func TestLimitExpansionsIsBoundedAndDeduplicated(t *testing.T) {
	nodes := []EvidenceNode{}
	requested := []Verdict{}
	for id := int64(1); id <= 20; id++ {
		nodes = append(nodes, node(id, "/Users/a/x", "x", 100))
		requested = append(requested, Verdict{NodeID: id, Verdict: VerdictUnknown})
	}
	// A repeat, an unknown id, and a file rather than a directory.
	requested = append(requested, Verdict{NodeID: 1, Verdict: VerdictUnknown}, Verdict{NodeID: 999, Verdict: VerdictUnknown})
	file := node(21, "/Users/a/f", "f", 100)
	file.Kind = "file"
	nodes = append(nodes, file)
	requested = append(requested, Verdict{NodeID: 21, Verdict: VerdictUnknown})

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
		result := Validate([]Verdict{goodSuggestion(1, claimed)}, shown, 7)
		if len(result.Accepted) != 1 {
			t.Fatalf("claim %q was rejected: %#v", claimed, result.Rejected)
		}
	}
	// Tolerance for a faithful echo is not tolerance for a wrong name.
	for _, claimed := range []string{"ists", "somethingelse", "wrapper/other"} {
		result := Validate([]Verdict{goodSuggestion(1, claimed)}, shown, 7)
		if len(result.Accepted) != 0 {
			t.Fatalf("claim %q should not have matched", claimed)
		}
	}
}

// A keep verdict is an answer, not a suggestion: it must never become a
// recommendation, and must not be counted as a refusal either.
func TestValidateIgnoresNonCleanableVerdicts(t *testing.T) {
	shown := shownNodes(node(1, "/Users/a/x", "x", 100), node(2, "/Users/a/y", "y", 100))
	keep := goodSuggestion(1, "x")
	keep.Verdict = VerdictKeep
	unknown := goodSuggestion(2, "y")
	unknown.Verdict = VerdictUnknown
	result := Validate([]Verdict{keep, unknown}, shown, 7)
	if len(result.Accepted) != 0 {
		t.Fatalf("a keep or unknown verdict became a recommendation: %#v", result.Accepted)
	}
	if len(result.Rejected) != 0 {
		t.Fatalf("declining to suggest something is not a rejection: %#v", result.Rejected)
	}
}

// Coverage is the point of moving selection to our side: we asked about a fixed
// list, so an unanswered id is a question the advisor dropped, not a limit of
// the request.
func TestCoverageCountsUnansweredCandidates(t *testing.T) {
	result := AdvisorResult{Verdicts: []Verdict{
		{NodeID: 1, Verdict: VerdictCleanable},
		{NodeID: 3, Verdict: VerdictKeep},
	}}
	total, answered := result.Coverage([]int64{1, 2, 3, 4})
	if total != 4 || answered != 2 {
		t.Fatalf("coverage is %d/%d, expected 2/4", answered, total)
	}
	if len(result.Cleanable()) != 1 || len(result.Unresolved()) != 0 {
		t.Fatalf("verdict partition is wrong: %#v", result)
	}
}

// The one error class that cannot be walked back. Reinstalling a toolchain costs
// a download; a photo library costs the photos. A wrong recoverability claim is
// therefore corrected in place and counted, not believed and not silently
// dropped -- dropping it would hide both the object and the mistake.
func TestValidateCorrectsWrongRecoverabilityClaims(t *testing.T) {
	cases := map[string]string{
		"/Users/a/Pictures/Trip":                                 IrreplaceableUserContent,
		"/Users/a/work/thing/.git":                               IrreplaceableRepository,
		"/Users/a/Library/Application Support/MobileSync/Backup": IrreplaceableBackup,
		"/Users/a/Library/Containers/com.x/Data/Documents":       IrreplaceableUserContent,
		"/Users/a/vm/ubuntu.qcow2":                               IrreplaceableVirtualDisk,
		"/Users/a/.ssh":                                          IrreplaceableCredentials,
	}
	for path, wantReason := range cases {
		target := node(1, path, "thing", 5_000_000_000)
		suggestion := goodSuggestion(1, "thing")
		suggestion.Recovery = string(RecoveryRegenerable)
		suggestion.Risk = string(RiskSafe)

		result := Validate([]Verdict{suggestion}, shownNodes(target), 7)
		if len(result.Accepted) != 1 {
			t.Fatalf("%s: expected the object to survive with the truth attached, got %#v", path, result.Rejected)
		}
		item := result.Accepted[0]
		if item.Recovery != RecoveryIrreplaceable {
			t.Fatalf("%s: recovery left as %q", path, item.Recovery)
		}
		if item.Risk != RiskRisky {
			t.Fatalf("%s: risk left as %q", path, item.Risk)
		}
		if len(result.Corrections) != 1 || result.Corrections[0].Reason != wantReason {
			t.Fatalf("%s: corrections are %#v, expected reason %q", path, result.Corrections, wantReason)
		}
		if !strings.Contains(item.WhatBreaks, "无法") && !strings.Contains(item.WhatBreaks, "唯一") &&
			!strings.Contains(item.WhatBreaks, "整个") && !strings.Contains(item.WhatBreaks, "永久") {
			t.Fatalf("%s: the correction did not reach what_breaks: %q", path, item.WhatBreaks)
		}
	}
}

// A correct claim is left alone, and a recoverable object is not dragged into
// the irreplaceable bucket by an over-eager guard.
func TestValidateLeavesSoundRecoverabilityClaimsAlone(t *testing.T) {
	for _, path := range []string{
		"/Users/a/dev/flutter/bin/cache/artifacts/engine",
		"/Users/a/.rustup/toolchains/stable/share/doc",
		"/Users/a/work/thing/build/intermediates",
		"/Users/a/Library/Caches/com.example",
	} {
		result := Validate([]Verdict{goodSuggestion(1, "thing")}, shownNodes(node(1, path, "thing", 100)), 7)
		if len(result.Corrections) != 0 {
			t.Fatalf("%s was corrected but is recoverable: %#v", path, result.Corrections)
		}
		if len(result.Accepted) != 1 || result.Accepted[0].Recovery != RecoveryRegenerable {
			t.Fatalf("%s: a sound claim was altered: %#v", path, result.Accepted)
		}
	}
}

// A stale temp pack is not repository history. The .git rule fired on one
// measured at 438 MB and labelled it permanent, which is wrong in the cautious
// direction -- still wrong. The carve-out is two literal prefixes, not a
// pattern, because broad exceptions are how safety guards acquire holes.
func TestIrreplaceableSkipsGitTransientArtifacts(t *testing.T) {
	for _, path := range []string{
		"/Users/a/work/thing/.git/objects/pack/tmp_pack_Z8vjYY",
		"/Users/a/work/thing/.git/objects/pack/tmp_idx_abc123",
	} {
		if reason := IrreplaceableReason(path); reason != "" {
			t.Fatalf("%s was called %q but git abandoned it", path, reason)
		}
	}
	// Everything else under .git stays permanent, including a real pack.
	for _, path := range []string{
		"/Users/a/work/thing/.git",
		"/Users/a/work/thing/.git/objects/pack/pack-abc.pack",
		"/Users/a/work/thing/.git/objects",
	} {
		if IrreplaceableReason(path) != IrreplaceableRepository {
			t.Fatalf("%s is repository history and must stay irreplaceable", path)
		}
	}
}
