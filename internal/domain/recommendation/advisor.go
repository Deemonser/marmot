package recommendation

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"example.com/marmot/internal/domain/cleanup"
)

// Suggestion is one raw item as an advisor returned it. Nothing here is trusted:
// the type exists to be validated into a Recommendation, or rejected.
//
// It deliberately has no size field. ADR-0061 §7.4 said to overwrite whatever
// figure a model reported with the snapshot's own; not asking for one at all is
// strictly better, because a number that was never requested cannot be wrong,
// cannot disagree with the wheel, and cannot be quoted back at a user who is
// deciding whether to delete something.
type Suggestion struct {
	NodeID int64 `json:"node_id"`
	// Name as the advisor believes it to be. Its only job is to fail: an id that
	// exists but names something else means the model lost track of which row it
	// was talking about, and the suggestion is discarded rather than applied to
	// whatever object happens to hold that id.
	Name         string   `json:"name"`
	Category     string   `json:"category"`
	Recovery     string   `json:"recovery"`
	Risk         string   `json:"risk"`
	Confidence   float64  `json:"confidence"`
	Evidence     []string `json:"evidence"`
	WhatBreaks   string   `json:"what_breaks"`
	HowToRestore string   `json:"how_to_restore"`
}

// Expansion is the advisor saying it cannot classify a region and wants to see
// inside it. R-062 §3.3 is why this is the model's call and not a size rule: the
// largest opaque block on the reference machine was a Gradle transform cache
// that needed no expansion at all, because one rule already settles it.
type Expansion struct {
	NodeID int64  `json:"node_id"`
	Why    string `json:"why"`
}

// AdvisorResult is one round's output.
type AdvisorResult struct {
	Suggestions    []Suggestion `json:"suggestions"`
	NeedsExpansion []Expansion  `json:"needs_expansion"`
	// InputTokens and OutputTokens are reported so the UI can say what a run
	// cost. Zero when the adapter cannot tell.
	InputTokens  int64 `json:"-"`
	OutputTokens int64 `json:"-"`
}

// MaxExpansions bounds round two. Four passes of "look a little deeper" is a
// loop that needs a termination proof; one round of at most this many focused
// look-ins is a fixed bound (ADR-0061 §6).
const MaxExpansions = 8

// Validation is the outcome of checking a round's suggestions.
type Validation struct {
	Accepted []Recommendation
	Rejected []Rejection
}

// Validate turns raw suggestions into recommendations, or into recorded
// rejections. Every check is here rather than spread across the caller, and
// every rejection is kept: a tool that reports "the model proposed three things
// I refused" is easier to trust than one that quietly shows fewer rows.
//
// shown is the set of nodes the advisor was actually given. Validating against
// it rather than against the whole snapshot is deliberate and stricter: a node
// id the model was never shown is not a suggestion it could have reasoned about,
// whether it invented the id or inferred one that happens to exist.
func Validate(suggestions []Suggestion, shown map[int64]EvidenceNode, snapshotID int64) Validation {
	result := Validation{}
	type candidate struct {
		suggestion Suggestion
		node       EvidenceNode
	}
	candidates := make([]candidate, 0, len(suggestions))

	for _, suggestion := range suggestions {
		node, known := shown[suggestion.NodeID]
		if !known {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: suggestion.NodeID, ClaimedName: suggestion.Name, Reason: RejectUnknownNode,
			})
			continue
		}
		// A trailing path fragment counts: a collapsed row is labelled `a/b/c`
		// and the advisor may echo any of it back.
		if !nameMatches(suggestion.Name, node) {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: suggestion.NodeID, ClaimedName: suggestion.Name, Reason: RejectNameMismatch,
			})
			continue
		}
		if reason := cleanup.DeleteBlock(node.Path); reason != "" {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: suggestion.NodeID, ClaimedName: node.Name, Reason: RejectProtected,
			})
			continue
		}
		if !validEnums(suggestion) {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: suggestion.NodeID, ClaimedName: node.Name, Reason: RejectMalformed,
			})
			continue
		}
		candidates = append(candidates, candidate{suggestion: suggestion, node: node})
	}

	// Overlap is resolved after the per-item checks so the survivor is chosen
	// from items that are individually sound. Outermost wins: it reclaims the
	// most, and plan creation refuses a parent and a child together anyway.
	sort.SliceStable(candidates, func(left, right int) bool {
		return len(candidates[left].node.Path) < len(candidates[right].node.Path)
	})
	accepted := make([]string, 0, len(candidates))
	for _, item := range candidates {
		covered := false
		for _, existing := range accepted {
			if existing == item.node.Path || cleanup.IsPathWithin(existing, item.node.Path) {
				covered = true
				break
			}
		}
		if covered {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: item.suggestion.NodeID, ClaimedName: item.node.Name, Reason: RejectOverlapping,
			})
			continue
		}
		accepted = append(accepted, item.node.Path)
		result.Accepted = append(result.Accepted, Recommendation{
			SnapshotID: snapshotID,
			NodeID:     item.node.ID,
			Source:     SourceAdvisor,
			Category:   strings.TrimSpace(item.suggestion.Category),
			// The snapshot's own figure, always. The advisor was never asked for
			// one, so there is nothing to reconcile.
			ReclaimableBytes: item.node.OwnedAllocated,
			Recovery:         Recovery(item.suggestion.Recovery),
			Risk:             Risk(item.suggestion.Risk),
			Confidence:       item.suggestion.Confidence,
			Evidence:         trimAll(item.suggestion.Evidence),
			WhatBreaks:       strings.TrimSpace(item.suggestion.WhatBreaks),
			HowToRestore:     strings.TrimSpace(item.suggestion.HowToRestore),
		})
	}
	sort.SliceStable(result.Accepted, func(left, right int) bool {
		return result.Accepted[left].ReclaimableBytes > result.Accepted[right].ReclaimableBytes
	})
	return result
}

// nameMatches accepts the node's own name or any segment of the collapsed label
// the row was rendered with, so echoing back `transformed` for a row labelled
// `<hash>/transformed/react-android` is not treated as a hallucination.
func nameMatches(claimed string, node EvidenceNode) bool {
	claimed = strings.TrimSpace(trimKindAnnotation(claimed))
	if claimed == "" {
		return false
	}
	if strings.EqualFold(claimed, node.Name) || strings.EqualFold(claimed, node.Path) {
		return true
	}
	if strings.EqualFold(claimed, path.Base(node.Path)) {
		return true
	}
	// The label the row was rendered with, and any tail of it on segment
	// boundaries: a collapsed row shows `a/b/c` and an advisor may quote the
	// whole thing or just the part it means.
	if label := strings.Trim(node.Label, "/"); label != "" {
		if strings.EqualFold(claimed, label) || hasSegmentSuffix(label, claimed) {
			return true
		}
	}
	// The claim is a tail of the path, on segment boundaries.
	return hasSegmentSuffix(node.Path, claimed)
}

// hasSegmentSuffix reports whether claimed is a whole-segment tail of value, so
// `dists` matches `wrapper/dists` but `ists` does not.
func hasSegmentSuffix(value, claimed string) bool {
	trimmed := strings.Trim(claimed, "/")
	if trimmed == "" {
		return false
	}
	return strings.EqualFold(value, trimmed) || strings.HasSuffix(strings.ToLower(value), "/"+strings.ToLower(trimmed))
}

// trimKindAnnotation drops a trailing "(d)" or "(f)". The pack no longer glues
// the kind onto the name, but a model that copies a row verbatim -- including
// one following an older example or annotating on its own -- is being accurate
// about which row it means, and rejecting that as a hallucinated name would
// throw away a correct suggestion. This is tolerance for a faithful echo, not
// tolerance for a wrong name: everything after the trim still has to match.
func trimKindAnnotation(claimed string) string {
	trimmed := strings.TrimSpace(claimed)
	for _, suffix := range []string{"(d)", "(f)", "(directory)", "(file)"} {
		if strings.HasSuffix(trimmed, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(trimmed, suffix))
		}
	}
	return trimmed
}

func validEnums(suggestion Suggestion) bool {
	switch Recovery(suggestion.Recovery) {
	case RecoveryRegenerable, RecoveryRedownloadable, RecoveryIrreplaceable:
	default:
		return false
	}
	switch Risk(suggestion.Risk) {
	case RiskSafe, RiskReview, RiskRisky:
	default:
		return false
	}
	if suggestion.Confidence < 0 || suggestion.Confidence > 1 {
		return false
	}
	// A suggestion that cannot say what breaks or how to recover is not
	// reviewable, and being unreviewable is the whole failure mode this feature
	// has to avoid.
	if strings.TrimSpace(suggestion.WhatBreaks) == "" || strings.TrimSpace(suggestion.HowToRestore) == "" {
		return false
	}
	// Nothing irreplaceable is safe. The model is told this; the rule is enforced
	// here because being told is not the same as complying.
	if Recovery(suggestion.Recovery) == RecoveryIrreplaceable && Risk(suggestion.Risk) == RiskSafe {
		return false
	}
	return true
}

// LimitExpansions keeps the requested look-ins bounded and free of duplicates,
// and drops any that name a node the advisor was not shown.
func LimitExpansions(requested []Expansion, shown map[int64]EvidenceNode) []int64 {
	seen := make(map[int64]bool, len(requested))
	focus := make([]int64, 0, MaxExpansions)
	for _, item := range requested {
		if len(focus) >= MaxExpansions {
			break
		}
		if seen[item.NodeID] {
			continue
		}
		node, known := shown[item.NodeID]
		if !known || node.Kind != "directory" {
			continue
		}
		seen[item.NodeID] = true
		focus = append(focus, item.NodeID)
	}
	return focus
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// RejectionSummary is what the UI shows about refused suggestions.
func RejectionSummary(rejections []Rejection) string {
	if len(rejections) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, item := range rejections {
		counts[item.Reason]++
	}
	labels := map[string]string{
		RejectUnknownNode:  "指向不存在的对象",
		RejectNameMismatch: "对象名与快照不符",
		RejectProtected:    "对象受保护",
		RejectOverlapping:  "与另一条建议重叠",
		RejectBelowFloor:   "低于体积下限",
		RejectMalformed:    "格式不合契约",
	}
	reasons := make([]string, 0, len(counts))
	for reason, count := range counts {
		label := labels[reason]
		if label == "" {
			label = reason
		}
		reasons = append(reasons, fmt.Sprintf("%s %d 条", label, count))
	}
	sort.Strings(reasons)
	return strings.Join(reasons, "、")
}
