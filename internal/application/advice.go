package application

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/recommendation"
)

const (
	// The absolute floor. Below this an object is not worth a recommendation,
	// whatever the volume's size. R-062 §3.2 measured this keeping 830 nodes of a
	// 1.73M-node home directory.
	// Written decimal, not 128<<20, so the constant reads as the 128 MB the docs
	// state and as the 128 MB the UI prints.
	evidenceAbsoluteFloor = 128_000_000
	// Scaled floor for large volumes, so a 4 TB disk does not produce a skeleton
	// ten times longer than a 400 GB one.
	evidenceVolumeShare = 0.0005
	// Fail-loud ceiling. Not a truncation point (ADR-0061 §9.5).
	evidenceMaxNodes = 4096
	// Extensions carried per node's residue.
	evidenceExtensionsPerNode = 3
	// Payload ceiling (ADR-0061 §9.5). 128 KB is roughly 32k tokens, which sits
	// comfortably inside every current context window.
	//
	// The number was 64 KB, taken from a $HOME measurement in R-062. A real
	// full-disk pack is 1,238 nodes and 84.9 KB, so that ceiling was both wrong
	// and -- worse -- unenforced: only the node count was checked, and the byte
	// figure was decorative.
	evidenceMaxPayloadBytes = 128_000
	// How many times the floor may be doubled to fit the ceiling. Each attempt
	// is a full walk, so this is bounded rather than open-ended.
	evidenceFloorAttempts = 4
)

// EvidencePack is what an advisor is asked about, and exactly what a user sees
// when they ask what is being sent. There is no second, richer form held back:
// the text this renders IS the payload (ADR-0061 §3).
type EvidencePack struct {
	SnapshotID       int64
	Root             string
	VolumeTotalBytes uint64
	VolumeUsedBytes  uint64
	VolumeFreeBytes  uint64
	FloorBytes       int64
	GeneratedAt      time.Time
	Nodes            []recommendation.EvidenceNode
	// RuleHits maps node ID to the catalog entry that matched, so the advisor is
	// told what is already known and can spend its attention elsewhere.
	RuleHits map[int64]*recommendation.Rule
	// ProjectIdleDays is how long each node's surrounding project has gone
	// without a source change, or recommendation.NoProject. Computed here, never
	// asked of a model: it is arithmetic over mtimes.
	ProjectIdleDays map[int64]int64
}

// BuildEvidencePack assembles the skeleton for one finished snapshot.
//
// The floor is raised and the walk repeated when the rendered pack would exceed
// the payload ceiling. Raising the floor cannot be simulated by filtering the
// result: residues merge into the nearest KEPT ancestor, so dropping nodes after
// the fact would leave every surviving residue understating what it covers.
// The floor actually used is reported on the pack and shown in the UI, so a
// coarser skeleton is visible rather than silent.
func (s *Service) BuildEvidencePack(snapshotID int64) (EvidencePack, error) {
	floor := int64(evidenceAbsoluteFloor)
	var pack EvidencePack
	for attempt := 0; ; attempt++ {
		candidate, err := s.buildEvidencePackAt(snapshotID, floor)
		if err != nil {
			return EvidencePack{}, err
		}
		pack = candidate
		if len(pack.Text()) <= evidenceMaxPayloadBytes || attempt >= evidenceFloorAttempts-1 {
			return pack, nil
		}
		// The store may have applied a larger share-scaled floor than the one
		// asked for; double whichever actually took effect.
		floor = pack.FloorBytes * 2
	}
}

func (s *Service) buildEvidencePackAt(snapshotID, floor int64) (EvidencePack, error) {
	return s.buildEvidencePackFor(snapshotID, 0, floor, evidenceVolumeShare)
}

func (s *Service) buildEvidencePackFor(snapshotID, rootID, floor int64, share float64) (EvidencePack, error) {
	result, err := s.store.EvidenceNodes(recommendation.EvidenceQuery{
		SnapshotID:        snapshotID,
		RootID:            rootID,
		MinBytes:          floor,
		MinShare:          share,
		MaxNodes:          evidenceMaxNodes,
		ExtensionsPerNode: evidenceExtensionsPerNode,
	})
	if err != nil {
		return EvidencePack{}, err
	}
	// One clock for the whole pack: rules with an age condition and the rendered
	// age columns must agree, or a row could be matched by a rule whose condition
	// the row itself appears not to meet.
	now := time.Now()
	idle := projectIdleDays(result.Nodes, now)
	hits := make(map[int64]*recommendation.Rule, 32)
	for _, node := range result.Nodes {
		rule := recommendation.Match(recommendation.MatchContext{
			Path: node.Path, AgeDays: node.AgeDays(now), ProjectIdleDays: idle[node.ID],
		})
		if rule != nil {
			hits[node.ID] = rule
		}
	}
	nodes := foldUnderSettledRules(result.Nodes, hits)
	return EvidencePack{
		SnapshotID:       snapshotID,
		Root:             result.Root,
		VolumeTotalBytes: result.VolumeTotalBytes,
		VolumeUsedBytes:  result.VolumeUsedBytes,
		VolumeFreeBytes:  result.VolumeFreeBytes,
		FloorBytes:       result.FloorBytes,
		GeneratedAt:      now,
		Nodes:            nodes,
		RuleHits:         hits,
		ProjectIdleDays:  idle,
	}, nil
}

// foldUnderSettledRules drops the descendants of a node whose rule already
// settles the whole subtree, and folds what they held back into it.
//
// Measured on a real full-disk pack: 145 of 848 rows (17.1%) sat below a
// rule-matched ancestor, 107 of them (12.6%) below a `safe` one. Those rows buy
// nothing. The advice for everything under `~/.gradle/caches` is one sentence --
// "delete it, 32.4 GB, Gradle rebuilds it" -- and 77 rows of
// `transforms/<hash>/transformed/react-android-…` do not make it a different
// sentence. That budget belongs where the catalog reaches nothing.
//
// Only `safe` rules fold. A `review` or `risky` match is exactly where a person
// may want to pick from inside: `~/Library/Caches` is one row per application
// and someone may keep one and drop another, so those subtrees stay expanded.
//
// Residues are a partition, so folding is exact rather than approximate: every
// dropped node's residue merges into the surviving ancestor, and the sum over
// the pack is unchanged. Dropping rows WITHOUT this merge is the bug it would
// otherwise be -- the ancestor would understate what it covers.
//
// The extension profile is the one lossy part: each node carries only its top
// few extensions, so the merged profile is built from those rather than from
// the full histogram. It is still far more representative than the ancestor's
// own residue profile, which describes only the handful of bytes that did not
// belong to any kept child.
func foldUnderSettledRules(nodes []recommendation.EvidenceNode, hits map[int64]*recommendation.Rule) []recommendation.EvidenceNode {
	settled := make(map[int64]bool, len(hits))
	for id, rule := range hits {
		if rule.Risk == recommendation.RiskSafe {
			settled[id] = true
		}
	}
	if len(settled) == 0 {
		return nodes
	}
	byID := make(map[int64]int, len(nodes))
	for index, node := range nodes {
		byID[node.ID] = index
	}
	// The nearest settled ancestor of each node, or 0. Nodes arrive biggest
	// first, and a parent is always larger than its child, so a single pass
	// sees every ancestor before its descendants.
	absorber := make(map[int64]int64, len(nodes))
	drop := make(map[int64]bool, len(nodes))
	for _, node := range nodes {
		owner := absorber[node.ParentID]
		if owner == 0 && settled[node.ParentID] {
			owner = node.ParentID
		}
		if owner == 0 {
			continue
		}
		absorber[node.ID] = owner
		drop[node.ID] = true
		target := &nodes[byID[owner]]
		target.Residue += node.Residue
		target.TopExtensions = mergeExtensions(target.TopExtensions, node.TopExtensions, evidenceExtensionsPerNode)
	}
	kept := make([]recommendation.EvidenceNode, 0, len(nodes)-len(drop))
	for _, node := range nodes {
		if !drop[node.ID] {
			kept = append(kept, node)
		}
	}
	// A rule match can itself sit under a settled one -- `.gradle/caches/
	// modules-2/files-2.1` under `.gradle/caches` -- so the hit map has to lose
	// the same entries the node list did. Leaving them behind made RuleFindings
	// look up an id that was no longer present, take the zero EvidenceNode for
	// it, and then dereference a nil rule.
	for id := range drop {
		delete(hits, id)
	}
	return kept
}

func mergeExtensions(into, from []recommendation.ExtensionShare, limit int) []recommendation.ExtensionShare {
	merged := make(map[string]recommendation.ExtensionShare, len(into)+len(from))
	for _, share := range append(append([]recommendation.ExtensionShare{}, into...), from...) {
		existing := merged[share.Extension]
		existing.Extension = share.Extension
		existing.Bytes += share.Bytes
		existing.Files += share.Files
		merged[share.Extension] = existing
	}
	shares := make([]recommendation.ExtensionShare, 0, len(merged))
	for _, share := range merged {
		shares = append(shares, share)
	}
	sort.Slice(shares, func(left, right int) bool {
		if shares[left].Bytes != shares[right].Bytes {
			return shares[left].Bytes > shares[right].Bytes
		}
		return shares[left].Extension < shares[right].Extension
	})
	if len(shares) > limit {
		shares = shares[:limit]
	}
	return shares
}

// AdviceItem is one suggestion as the UI shows it: the domain recommendation
// plus the name and path a person needs to recognise the object. The path is
// display material -- what makes a suggestion unable to authorise anything is
// CreateCleanupPlan re-checking every path it is handed, not the absence of a
// field here (ADR-0061 §1).
type AdviceItem struct {
	recommendation.Recommendation
	Name string
	Path string
}

// Advice is one analysis result.
type Advice struct {
	SnapshotID int64
	Items      []AdviceItem
	// TotalBytes is the sum over items. The items never overlap, so this is a
	// real total and not an upper bound.
	TotalBytes int64
	RuleItems  int
	// AdvisorItems stays zero until an Advisor is wired in; the rule layer is
	// the floor that works without one.
	AdvisorItems int
	// Rejected records advisor suggestions that failed validation. Surfaced
	// rather than dropped silently (ADR-0061 §7.5).
	Rejected        []recommendation.Rejection
	RejectedSummary string
	// Corrections are recoverability claims that were overridden. This is the
	// error class that cannot be undone by waiting, so it is reported rather
	// than absorbed.
	Corrections       []recommendation.Correction
	CorrectionSummary string
	// AdvisorError is a round trip that failed. The rule findings still stand,
	// so this is reported alongside them rather than instead of them.
	AdvisorError string
	// Rounds is how many times the advisor was asked, and Expanded how many
	// regions round two looked inside.
	Rounds   int
	Expanded int
	// TopRowsAccounted / TopRows measure whether the largest rows were each
	// dealt with rather than silently passed over. Coverage, not correctness:
	// a row explicitly declined counts as accounted for.
	TopRowsAccounted int
	TopRows          int
	InputTokens      int64
	OutputTokens     int64
	// EvidenceNodes and EvidenceBytes describe what would be sent, so the panel
	// can say so without building the pack twice.
	EvidenceNodes int
	EvidenceBytes int
	FloorBytes    int64
}

// GetCleanupAdvice is the rule layer alone: deterministic, local, and the
// fallback whenever no advisor is configured.
func (s *Service) GetCleanupAdvice(snapshotID int64) (Advice, error) {
	pack, err := s.BuildEvidencePack(snapshotID)
	if err != nil {
		return Advice{}, err
	}
	return adviceFromPack(snapshotID, pack), nil
}

// RuleFindings is the floor: what the catalog knows, produced without any model
// involved. It is what the feature falls back to when no advisor is configured,
// and it is deliberately computed here rather than asked of the advisor -- a
// rule is not a guess and does not need one.
//
// Overlapping matches collapse to the outermost: when both `~/Library/Caches`
// and `~/Library/Caches/one-app` match, offering both would double count the
// reclaimable bytes and stage two plan items for the same files.
func (p EvidencePack) RuleFindings() []AdviceItem {
	byID := make(map[int64]recommendation.EvidenceNode, len(p.Nodes))
	for _, node := range p.Nodes {
		byID[node.ID] = node
	}
	matched := make([]recommendation.EvidenceNode, 0, len(p.RuleHits))
	for id := range p.RuleHits {
		// A hit without a node is a bug elsewhere, not a finding: the zero value
		// has an empty path, which the delete guard permits and every later step
		// then treats as real.
		node, ok := byID[id]
		if !ok {
			continue
		}
		// The guard has the last word here as everywhere: a suggestion on a path
		// that can never be staged is not a suggestion (ADR-0061 §7.2).
		if cleanup.DeleteBlock(node.Path) != "" {
			continue
		}
		matched = append(matched, node)
	}
	sort.Slice(matched, func(left, right int) bool {
		return len(matched[left].Path) < len(matched[right].Path)
	})

	findings := make([]AdviceItem, 0, len(matched))
	kept := make([]string, 0, len(matched))
	for _, node := range matched {
		covered := false
		for _, existing := range kept {
			if existing == node.Path || cleanup.IsPathWithin(existing, node.Path) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		kept = append(kept, node.Path)
		rule := p.RuleHits[node.ID]
		risk := rule.Risk
		whatBreaks := rule.WhatBreaks
		if rule.ProjectSensitive {
			adjusted, note := recommendation.AdjustForProjectActivity(risk, p.idleFor(node.ID))
			risk = adjusted
			if note != "" {
				whatBreaks = note + " " + whatBreaks
			}
		}
		findings = append(findings, AdviceItem{
			Recommendation: recommendation.Recommendation{
				SnapshotID:       p.SnapshotID,
				NodeID:           node.ID,
				Source:           recommendation.SourceRule,
				RuleName:         rule.Name,
				Category:         rule.Category,
				ReclaimableBytes: node.OwnedAllocated,
				Recovery:         rule.Recovery,
				Risk:             risk,
				Confidence:       1,
				Evidence:         nodeEvidence(node, p.GeneratedAt),
				WhatBreaks:       whatBreaks,
				HowToRestore:     rule.HowToRestore,
			},
			Name: node.Name,
			Path: node.Path,
		})
	}
	sort.Slice(findings, func(left, right int) bool {
		return findings[left].ReclaimableBytes > findings[right].ReclaimableBytes
	})
	return findings
}

// idleFor is the surrounding project's idle days, or NoProject.
func (p EvidencePack) idleFor(nodeID int64) int64 {
	if p.ProjectIdleDays == nil {
		return recommendation.NoProject
	}
	if days, ok := p.ProjectIdleDays[nodeID]; ok {
		return days
	}
	return recommendation.NoProject
}

// nodeEvidence phrases the facts a person can check against the space map. The
// numbers are the snapshot's own, never restated from anywhere else.
func nodeEvidence(node recommendation.EvidenceNode, now time.Time) []string {
	evidence := []string{fmt.Sprintf("占用 %s", formatBytes(node.OwnedAllocated))}
	if node.SubtreeFiles > 0 {
		evidence = append(evidence, fmt.Sprintf("%d 个文件", node.SubtreeFiles))
	}
	if len(node.TopExtensions) > 0 && node.TopExtensions[0].Extension != "" {
		evidence = append(evidence, fmt.Sprintf("主要是 %s", node.TopExtensions[0].Extension))
	}
	if node.FutureModified {
		evidence = append(evidence, "修改时间在未来，元数据可能异常")
	} else if days := node.AgeDays(now); days > 0 {
		evidence = append(evidence, fmt.Sprintf("最近 %d 天未改动", days))
	}
	return evidence
}

// Text renders the pack. This is both the payload sent to an advisor and the
// text shown by "查看发送内容" -- one rendering, so the preview cannot drift
// from what actually leaves the machine.
//
// The skeleton is an indented tree rather than one absolute path per row.
// Paths in a connected subtree repeat their prefixes endlessly: rendering
// `/Users/deemo/Library/...` on every line costs about 120 bytes a row against
// two bytes per level of indentation, and the tree shape is what a reader --
// person or model -- actually needs. The kept set is closed under ancestors, so
// it is always a tree and this is always renderable.
// Candidates picks what the advisor is asked about: deterministic, size-ordered,
// and free of anything a rule already covers or the delete guard refuses.
//
// Selection is ours because it is the half the model was bad at. Asked to find
// things itself, it produced a different two thirds of its answer on every run
// (Jaccard 0.37); asked about a fixed list, coverage becomes a property of the
// request. Filtering out rule-covered paths also means the whole budget goes
// where the catalog reaches nothing, which is the only place the advisor adds
// anything (R-062 §3.4).
func (p EvidencePack) Candidates(limit int) []recommendation.EvidenceNode {
	ruled := make([]string, 0, len(p.RuleHits))
	byID := make(map[int64]recommendation.EvidenceNode, len(p.Nodes))
	for _, node := range p.Nodes {
		byID[node.ID] = node
	}
	for id := range p.RuleHits {
		if node, ok := byID[id]; ok {
			ruled = append(ruled, node.Path)
		}
	}
	labels := p.RowLabels()

	candidates := make([]recommendation.EvidenceNode, 0, len(p.Nodes))
	for _, node := range p.Nodes {
		// The pack's own root anchors the tree and is never a candidate.
		if _, hasParent := byID[node.ParentID]; !hasParent {
			continue
		}
		if cleanup.DeleteBlock(node.Path) != "" {
			continue
		}
		if coveredByRule(ruled, node.Path) {
			continue
		}
		node.Label = labels[node.ID]
		candidates = append(candidates, node)
	}
	// Ranked by RESIDUE, not by subtree size. Residue is the part of an object
	// no listed child accounts for, so a large residue means the content is on
	// this node itself -- it is the actionable unit -- while a small one means
	// the node is a waypoint whose content sits in children that are listed
	// separately.
	//
	// Ranking by subtree size instead was a real defect, measured: ~/Library is
	// 28 GB and no rule matches it (the rule matches Library/Caches), so it took
	// the top slot, an ancestor-first pass then excluded everything inside it,
	// and every deep find of the previous runs -- Code/CachedExtensionVSIXs,
	// Application Support/Caches -- disappeared. The advisor correctly answered
	// `keep` for ~/Library, and the run produced two suggestions instead of
	// thirty.
	//
	// Residue also partitions the total, so the top N cover disjoint bytes by
	// construction and no ancestor-versus-descendant pass is needed at all.
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Residue != candidates[right].Residue {
			return candidates[left].Residue > candidates[right].Residue
		}
		return candidates[left].ID < candidates[right].ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// projectIdleDays answers, for every node, how long the surrounding project's
// source has been untouched -- or NoProject when it is not inside one.
//
// The nearest project root above a node is what counts, so a module inside a
// monorepo takes the module's activity when the module itself carries a marker
// and the repository's otherwise. The kept set is closed under ancestors, so any
// project root large enough to hold the candidate is in it.
func projectIdleDays(nodes []recommendation.EvidenceNode, now time.Time) map[int64]int64 {
	byID := make(map[int64]recommendation.EvidenceNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	idle := make(map[int64]int64, len(nodes))
	for _, node := range nodes {
		days := recommendation.NoProject
		for cursor := node; ; {
			if cursor.IsProjectRoot && !cursor.SourceNewestModified.IsZero() {
				elapsed := int64(now.Sub(cursor.SourceNewestModified).Hours() / 24)
				if elapsed < 0 {
					elapsed = 0
				}
				days = elapsed
				break
			}
			parent, ok := byID[cursor.ParentID]
			if !ok {
				break
			}
			cursor = parent
		}
		idle[node.ID] = days
	}
	return idle
}

func coveredByRule(paths []string, candidate string) bool {
	for _, existing := range paths {
		if existing == candidate || cleanup.IsPathWithin(existing, candidate) {
			return true
		}
	}
	return false
}

// RenderCandidates writes the candidate rows in the same column layout as the
// tree, so a row means the same thing in both places.
func (p EvidencePack) RenderCandidates(candidates []recommendation.EvidenceNode) string {
	var out strings.Builder
	out.WriteString("# 列: id | 名称 | 类型 | 占用 | residue | 文件数 | 目录数 | 最近改动天数 | 最早改动天数 | 最大单文件 | 扩展名画像 | 所属项目源码空闲天数\n")
	out.WriteString("# 「所属项目源码空闲天数」= 该对象所在项目的源码有多久没改过（排除构建产物目录）。\n")
	out.WriteString("#   - 数字小 = 项目正在用，删掉它的缓存会让下一次构建重新下载/编译，干扰大；\n")
	out.WriteString("#   - 数字大 = 项目已停摆，删掉不会有人察觉；\n")
	out.WriteString("#   - 「-」= 不在任何可识别的项目里。\n")
	out.WriteString("# 判断可清理性时，恢复代价要和这个数一起看：能恢复不等于删了没影响。\n\n")
	for _, node := range candidates {
		kind := "d"
		if node.Kind != "directory" {
			kind = "f"
		}
		age := fmt.Sprintf("%d", node.AgeDays(p.GeneratedAt))
		if node.FutureModified {
			age = "future"
		}
		label := node.Label
		if label == "" {
			label = node.Name
		}
		idle := "-"
		if days := p.idleFor(node.ID); days >= 0 {
			idle = fmt.Sprintf("%d", days)
		}
		fmt.Fprintf(&out, "%d\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%d\t%s\t%s\t%s\n",
			node.ID, label, kind,
			formatBytes(node.OwnedAllocated), formatBytes(node.Residue),
			node.SubtreeFiles, node.SubtreeDirs, age, node.OldestDays(p.GeneratedAt),
			formatBytes(node.BiggestFile), formatExtensions(node.TopExtensions), idle)
	}
	return out.String()
}

// RowLabels is what each rendered row was labelled with, keyed by the id that
// row carries. Validation needs it because a collapsed row shows `a/b/c` while
// the node's own path ends at `a`, and an advisor quoting the row it was shown
// is being accurate.
func (p EvidencePack) RowLabels() map[int64]string {
	labels := make(map[int64]string, len(p.Nodes))
	p.walkRows(func(head, tail recommendation.EvidenceNode, label string, depth int) {
		labels[head.ID] = label
	})
	return labels
}

func (p EvidencePack) Text() string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Marmot 扫描证据\n")
	fmt.Fprintf(&out, "# 扫描根: %s\n", p.Root)
	if p.VolumeTotalBytes > 0 {
		fmt.Fprintf(&out, "# 卷: 容量 %s / 已用 %s / 可用 %s\n",
			formatBytes(int64(p.VolumeTotalBytes)), formatBytes(int64(p.VolumeUsedBytes)), formatBytes(int64(p.VolumeFreeBytes)))
	}
	fmt.Fprintf(&out, "# 骨架下限: %s，共 %d 个节点\n", formatBytes(p.FloorBytes), len(p.Nodes))
	fmt.Fprintf(&out, "# 低于下限的对象不单列，其字节计入最近的上级节点的 residue\n")
	fmt.Fprintf(&out, "#\n")
	// The kind is its own column. It used to be glued onto the name as `x(d)`,
	// and a real advisor faithfully echoed `".transforms(d)"` back as the name --
	// which the validator then rejected as a mismatch. The model was being
	// accurate; the format was ambiguous. Measured: 31 of 31 suggestions lost.
	fmt.Fprintf(&out, "# 列: id | 名称(缩进表示层级) | 类型 | 占用 | residue | 文件数 | 目录数 | 最近改动天数 | 最早改动天数 | 最大单文件 | 扩展名画像 | 已知规则\n")
	fmt.Fprintf(&out, "# 引用某一行时，name 请只写名称列，不要带类型或缩进。\n\n")

	p.walkRows(func(head, tail recommendation.EvidenceNode, label string, depth int) {
		p.writeRow(&out, head, tail, label, depth)
	})
	return out.String()
}

// walkRows is the single traversal both the rendering and the label index use,
// so what the advisor is shown and what its answer is checked against cannot
// drift apart.
func (p EvidencePack) walkRows(visit func(head, tail recommendation.EvidenceNode, label string, depth int)) {
	children := make(map[int64][]recommendation.EvidenceNode, len(p.Nodes))
	byID := make(map[int64]recommendation.EvidenceNode, len(p.Nodes))
	var roots []recommendation.EvidenceNode
	for _, node := range p.Nodes {
		byID[node.ID] = node
	}
	for _, node := range p.Nodes {
		if _, ok := byID[node.ParentID]; ok {
			children[node.ParentID] = append(children[node.ParentID], node)
			continue
		}
		roots = append(roots, node)
	}
	bySize := func(nodes []recommendation.EvidenceNode) {
		sort.Slice(nodes, func(left, right int) bool {
			if nodes[left].OwnedAllocated != nodes[right].OwnedAllocated {
				return nodes[left].OwnedAllocated > nodes[right].OwnedAllocated
			}
			return nodes[left].ID < nodes[right].ID
		})
	}
	bySize(roots)
	for _, group := range children {
		bySize(group)
	}

	type stackEntry struct {
		node  recommendation.EvidenceNode
		depth int
	}
	stack := make([]stackEntry, 0, len(p.Nodes))
	for index := len(roots) - 1; index >= 0; index-- {
		stack = append(stack, stackEntry{node: roots[index]})
	}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		head, tail, label := collapseChain(current.node, children)
		visit(head, tail, label, current.depth)
		group := children[tail.ID]
		for index := len(group) - 1; index >= 0; index-- {
			stack = append(stack, stackEntry{node: group[index], depth: current.depth + 1})
		}
	}
}

// collapseChain folds a run of single-child directories into one row.
//
// A node with exactly one kept child and nothing of its own left over says
// only "the thing below me is here"; on a real tree these run seven deep
// (`.../<hash>/transformed/react-android-0.86.0-debug/prefab/modules/...`),
// seven rows all reporting the same 997 MB. Collapsing removes rows that carry
// no decision without removing any object: the row keeps the OUTERMOST node's
// ID, because that is the outermost thing whose deletion reclaims those bytes,
// and reports the innermost node's residue and extension profile, because that
// is where the content actually sits.
//
// "Nothing of its own left over" is deliberately not "exactly zero": a
// directory carrying a few KB of its own alongside a 1 GB child is still just a
// waypoint.
//
// A protected node never heads a collapsed row, and never disappears into one.
// The head's id is offered as "the outermost object whose deletion reclaims
// these bytes", and for a path the guard refuses that sentence is simply false
// -- the scan root is the everyday case, since a root with one heavy child
// would otherwise swallow the whole chain and report an undeletable id.
func collapseChain(node recommendation.EvidenceNode, children map[int64][]recommendation.EvidenceNode) (head, tail recommendation.EvidenceNode, label string) {
	head, tail = node, node
	label = node.Name
	if cleanup.DeleteBlock(node.Path) != "" {
		return head, tail, label
	}
	for {
		group := children[tail.ID]
		if len(group) != 1 || tail.Kind != "directory" {
			return head, tail, label
		}
		if tail.Residue*20 > tail.OwnedAllocated {
			return head, tail, label
		}
		if cleanup.DeleteBlock(group[0].Path) != "" {
			return head, tail, label
		}
		tail = group[0]
		label += "/" + tail.Name
	}
}

// writeRow reports the outer node's identity and the inner node's contents;
// when nothing was collapsed the two are the same node.
func (p EvidencePack) writeRow(out *strings.Builder, head, tail recommendation.EvidenceNode, label string, depth int) {
	kind := "d"
	if tail.Kind != "directory" {
		kind = "f"
	}
	age := fmt.Sprintf("%d", tail.AgeDays(p.GeneratedAt))
	if tail.FutureModified {
		age = "future"
	}
	rule := ""
	if hit := p.RuleHits[head.ID]; hit != nil {
		rule = hit.Name
	} else if hit := p.RuleHits[tail.ID]; hit != nil {
		rule = hit.Name
	}
	fmt.Fprintf(out, "%d\t%s%s\t%s\t%s\t%s\t%d\t%d\t%s\t%d\t%s\t%s\t%s\n",
		head.ID,
		strings.Repeat("  ", depth), label, kind,
		formatBytes(head.OwnedAllocated),
		formatBytes(tail.Residue),
		tail.SubtreeFiles,
		tail.SubtreeDirs,
		age,
		tail.OldestDays(p.GeneratedAt),
		formatBytes(tail.BiggestFile),
		formatExtensions(tail.TopExtensions),
		rule,
	)
}

func formatExtensions(shares []recommendation.ExtensionShare) string {
	if len(shares) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(shares))
	for _, share := range shares {
		name := share.Extension
		if name == "" {
			name = "(无扩展名)"
		}
		parts = append(parts, fmt.Sprintf("%s:%s", name, formatBytes(share.Bytes)))
	}
	return strings.Join(parts, ",")
}

// formatBytes uses decimal units, the same as the frontend and the same as
// macOS. This was 1024-based at first, which made the pack say "128MB" for the
// floor while the panel above it said "134.2 MB" -- one value, two conventions,
// visibly disagreeing on the same screen. Every figure in this app reads the
// same way as every other one.
func formatBytes(value int64) string {
	if value < 1000 {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	size := float64(value)
	index := -1
	for size >= 1000 && index < len(units)-1 {
		size /= 1000
		index++
	}
	if size >= 100 {
		return fmt.Sprintf("%.0f%s", size, units[index])
	}
	return fmt.Sprintf("%.1f%s", size, units[index])
}
