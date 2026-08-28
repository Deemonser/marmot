package recommendation

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"example.com/marmot/internal/domain/cleanup"
)

// Verdict is the advisor's answer about one candidate it was asked about.
//
// One answer per question, and that shape is the finding rather than a
// preference. Asking the open question -- "here is the tree, tell me what is
// cleanable" -- was measured three times over one identical evidence pack:
// 36, 25 and 31 objects, pairwise Jaccard 0.37. Temperature 0 did not move it
// (0.35 -> 0.37), and demanding exhaustiveness in the prompt made it worse
// (0.28, with only a quarter of the large rows accounted for) because the pack
// is a tree and re-ranking 666 rows by size is not something a model does
// reliably against that format.
//
// What stayed stable throughout was its judgement of any given object:
// citations checked out at 100%, and risk labels drifted on one object in
// fifty. So selection moved to our side -- deterministic, size-ordered,
// rule-filtered -- and the advisor is left with the part it is good at.
// Coverage became a property of the request instead of a hope about the reply.
type Verdict struct {
	NodeID int64 `json:"node_id"`
	// Name as the advisor believes it to be. Its only job is to fail: an id that
	// exists but names something else means the model lost track of which row it
	// meant, and the answer is discarded rather than applied to whatever object
	// happens to hold that id.
	Name string `json:"name"`
	// Verdict is Cleanable, Keep or Unknown. Unknown is a legitimate answer: it
	// asks for the object to be opened up rather than guessed at.
	Verdict string `json:"verdict"`
	// Why is required for keep and unknown. A refusal with no reason is
	// indistinguishable from never having looked.
	Why string `json:"why"`
	// The rest is required for cleanable and ignored otherwise. There is
	// deliberately no size field: the snapshot's own figure is used, so a number
	// that was never requested cannot disagree with the wheel.
	Category     string   `json:"category"`
	Recovery     string   `json:"recovery"`
	Risk         string   `json:"risk"`
	Confidence   float64  `json:"confidence"`
	Evidence     []string `json:"evidence"`
	WhatBreaks   string   `json:"what_breaks"`
	HowToRestore string   `json:"how_to_restore"`
}

const (
	VerdictCleanable = "cleanable"
	VerdictKeep      = "keep"
	VerdictUnknown   = "unknown"
)

// AdvisorResult is one round's output: one verdict per candidate asked about.
type AdvisorResult struct {
	Verdicts []Verdict `json:"verdicts"`
	// InputTokens and OutputTokens are reported so the UI can say what a run
	// cost. Zero when the adapter cannot tell.
	InputTokens  int64 `json:"-"`
	OutputTokens int64 `json:"-"`
}

// Cleanable are the verdicts that propose removing something.
func (r AdvisorResult) Cleanable() []Verdict {
	return r.withVerdict(VerdictCleanable)
}

// Unresolved are the candidates the advisor could not classify. Round two opens
// these up; asking is better than guessing.
func (r AdvisorResult) Unresolved() []Verdict {
	return r.withVerdict(VerdictUnknown)
}

func (r AdvisorResult) withVerdict(kind string) []Verdict {
	out := make([]Verdict, 0, len(r.Verdicts))
	for _, verdict := range r.Verdicts {
		if verdict.Verdict == kind {
			out = append(out, verdict)
		}
	}
	return out
}

// Coverage reports how many of the candidates came back with any verdict at all.
// With selection on our side this is a property of the request, so anything
// below total is the advisor dropping a question rather than a limit of ours.
func (r AdvisorResult) Coverage(asked []int64) (total, answered int) {
	seen := make(map[int64]bool, len(r.Verdicts))
	for _, verdict := range r.Verdicts {
		seen[verdict.NodeID] = true
	}
	for _, id := range asked {
		total++
		if seen[id] {
			answered++
		}
	}
	return total, answered
}

// MaxExpansions bounds round two. Four passes of "look a little deeper" is a
// loop that needs a termination proof; one round of at most this many focused
// look-ins is a fixed bound (ADR-0061 §6).
const MaxExpansions = 8

// Correction is a recoverability claim that was wrong and has been overridden.
//
// This is the error class that matters. Reinstalling Flutter to reclaim 2.4 GB
// costs a download; deleting a photo library costs the photos. Everything else
// the advisor can get wrong is recoverable by waiting, so a wrong `regenerable`
// on something permanent is the one mistake that cannot be walked back -- and
// the rate of it is the number that decides whether this feature may ship.
type Correction struct {
	NodeID          int64
	Path            string
	ClaimedRecovery string
	Reason          string
}

// Validation is the outcome of checking a round's verdicts.
type Validation struct {
	Accepted []Recommendation
	Rejected []Rejection
	// Corrections are claims that were overridden rather than discarded: the
	// object may still be worth showing, with the truth attached.
	Corrections []Correction
}

// Validate turns cleanable verdicts into recommendations, or into recorded
// rejections. Every check is here rather than spread across the caller, and
// every rejection is kept: a tool that reports "the model proposed three things
// I refused" is easier to trust than one that quietly shows fewer rows.
//
// shown is the set of nodes the advisor was actually given. Validating against
// it rather than against the whole snapshot is deliberate and stricter: a node
// id the model was never shown is not something it could have reasoned about,
// whether it invented the id or named one that happens to exist.
func Validate(verdicts []Verdict, shown map[int64]EvidenceNode, snapshotID int64) Validation {
	result := Validation{}
	type candidate struct {
		verdict Verdict
		node    EvidenceNode
	}
	candidates := make([]candidate, 0, len(verdicts))

	for _, verdict := range verdicts {
		if verdict.Verdict != VerdictCleanable {
			continue
		}
		node, known := shown[verdict.NodeID]
		if !known {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: verdict.NodeID, ClaimedName: verdict.Name, Reason: RejectUnknownNode,
			})
			continue
		}
		if !nameMatches(verdict.Name, node) {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: verdict.NodeID, ClaimedName: verdict.Name, Reason: RejectNameMismatch,
			})
			continue
		}
		if reason := cleanup.DeleteBlock(node.Path); reason != "" {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: verdict.NodeID, ClaimedName: node.Name, Reason: RejectProtected,
			})
			continue
		}
		if !validEnums(verdict) {
			result.Rejected = append(result.Rejected, Rejection{
				NodeID: verdict.NodeID, ClaimedName: node.Name, Reason: RejectMalformed,
			})
			continue
		}
		candidates = append(candidates, candidate{verdict: verdict, node: node})
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
				NodeID: item.verdict.NodeID, ClaimedName: item.node.Name, Reason: RejectOverlapping,
			})
			continue
		}
		accepted = append(accepted, item.node.Path)
		recovery := Recovery(item.verdict.Recovery)
		risk := Risk(item.verdict.Risk)
		whatBreaks := strings.TrimSpace(item.verdict.WhatBreaks)
		// A wrong recoverability claim is corrected, not believed and not thrown
		// away. Being told the rule in the prompt is not the same as complying
		// with it, and this is the one class of mistake that is permanent.
		// A partial deletion inside a stamp-guarded install does come back, but
		// only by reinstalling the whole toolchain -- so the claim is not wrong
		// about recoverability, it is wrong about the cost. Corrected in place
		// with the real route attached, and the risk raised out of `safe`.
		if reason := PartialInstallReason(item.node.Path); reason != "" {
			result.Corrections = append(result.Corrections, Correction{
				NodeID: item.node.ID, Path: item.node.Path,
				ClaimedRecovery: item.verdict.Recovery, Reason: reason,
			})
			recovery = RecoveryRedownloadable
			if risk == RiskSafe {
				risk = RiskReview
			}
			whatBreaks = strings.TrimSpace(PartialInstallMessage() + " " + whatBreaks)
		}
		// Login state is recoverable -- you can sign in again -- so the correction
		// is on the risk and the wording, not on the recoverability. Calling it
		// irreplaceable would be a lie in the cautious direction.
		if reason := LoginStateReason(item.node.Path); reason != "" {
			result.Corrections = append(result.Corrections, Correction{
				NodeID: item.node.ID, Path: item.node.Path,
				ClaimedRecovery: item.verdict.Recovery, Reason: reason,
			})
			risk = RiskRisky
			whatBreaks = strings.TrimSpace(LoginStateMessage() + " " + whatBreaks)
		}
		if reason := IrreplaceableReason(item.node.Path); reason != "" && recovery != RecoveryIrreplaceable {
			result.Corrections = append(result.Corrections, Correction{
				NodeID: item.node.ID, Path: item.node.Path,
				ClaimedRecovery: item.verdict.Recovery, Reason: reason,
			})
			recovery = RecoveryIrreplaceable
			risk = RiskRisky
			whatBreaks = strings.TrimSpace(IrreplaceableMessage(reason) + " " + whatBreaks)
		}
		result.Accepted = append(result.Accepted, Recommendation{
			SnapshotID: snapshotID,
			NodeID:     item.node.ID,
			Source:     SourceAdvisor,
			Category:   strings.TrimSpace(item.verdict.Category),
			// The snapshot's own figure, always. The advisor was never asked for
			// one, so there is nothing to reconcile.
			ReclaimableBytes: item.node.OwnedAllocated,
			Recovery:         recovery,
			Risk:             risk,
			Confidence:       item.verdict.Confidence,
			Evidence:         trimAll(item.verdict.Evidence),
			WhatBreaks:       whatBreaks,
			HowToRestore:     strings.TrimSpace(item.verdict.HowToRestore),
		})
	}
	sort.SliceStable(result.Accepted, func(left, right int) bool {
		return result.Accepted[left].ReclaimableBytes > result.Accepted[right].ReclaimableBytes
	})
	return result
}

// nameMatches accepts the node's own name, the label the row was rendered with,
// or any whole-segment tail of either, so an advisor quoting the row it was
// shown is not treated as having hallucinated.
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
	if label := strings.Trim(node.Label, "/"); label != "" {
		if strings.EqualFold(claimed, label) || hasSegmentSuffix(label, claimed) {
			return true
		}
	}
	return hasSegmentSuffix(node.Path, claimed)
}

// trimKindAnnotation drops a trailing "(d)" or "(f)". The pack no longer glues
// the kind onto the name, but a model that copies a row verbatim is being
// accurate about which row it means, and rejecting that would throw away a
// correct answer. Everything after the trim still has to match.
func trimKindAnnotation(claimed string) string {
	trimmed := strings.TrimSpace(claimed)
	for _, suffix := range []string{"(d)", "(f)", "(directory)", "(file)"} {
		if strings.HasSuffix(trimmed, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(trimmed, suffix))
		}
	}
	return trimmed
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

func validEnums(verdict Verdict) bool {
	switch Recovery(verdict.Recovery) {
	case RecoveryRegenerable, RecoveryRedownloadable, RecoveryIrreplaceable:
	default:
		return false
	}
	switch Risk(verdict.Risk) {
	case RiskSafe, RiskReview, RiskRisky:
	default:
		return false
	}
	if verdict.Confidence < 0 || verdict.Confidence > 1 {
		return false
	}
	// A suggestion that cannot say what breaks or how to recover is not
	// reviewable, and being unreviewable is the failure mode this feature has to
	// avoid.
	if strings.TrimSpace(verdict.WhatBreaks) == "" || strings.TrimSpace(verdict.HowToRestore) == "" {
		return false
	}
	// Nothing irreplaceable is safe. The model is told this; the rule is enforced
	// here because being told is not the same as complying.
	if Recovery(verdict.Recovery) == RecoveryIrreplaceable && Risk(verdict.Risk) == RiskSafe {
		return false
	}
	return true
}

// LimitExpansions keeps round two bounded and free of duplicates, and drops any
// candidate the advisor was not shown or that has no inside to look at.
func LimitExpansions(unresolved []Verdict, shown map[int64]EvidenceNode) []int64 {
	seen := make(map[int64]bool, len(unresolved))
	focus := make([]int64, 0, MaxExpansions)
	for _, item := range unresolved {
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
