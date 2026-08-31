package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/ports"
)

const (
	// A region the advisor asked to see inside is re-skeletonised at a hundredth
	// of its own size, so a 3 GB lump comes back at ~30 MB resolution instead of
	// the whole-disk floor that made it opaque in the first place.
	expansionShareOfRegion = 100
	// Nothing below this is worth a row even inside an expansion.
	expansionAbsoluteFloor = 4 << 20
	expansionMaxNodes      = 400
	// Round one asks about the whole disk; round two about a handful of regions.
	//
	// Set generously rather than tightly: providers warn that a JSON reply cut
	// off at the cap comes back as truncated JSON, and output is billed on what
	// is generated, not on the cap. The adapter refuses a `length` finish rather
	// than parsing half an answer, so the cost of being wrong here is a failed
	// run, not a quietly shortened one.
	triageMaxOutputTokens = 64000
	expandMaxOutputTokens = 32000
	// How many objects one round asks about. Bounded by the output budget: each
	// verdict costs a couple of hundred tokens, and a question the model runs out
	// of room to answer is worse than one not asked.
	advisorCandidateLimit = 40
	// One request per batch, run concurrently. The 40 candidates are independent
	// by construction -- the contract is one verdict per candidate -- but a single
	// request makes the model think about them in sequence, and thinking is where
	// the time goes: 10,084 of 13,414 output tokens at effort "low" (R-063 §4e).
	// Splitting does not reduce the thinking, it stops it being serialised.
	//
	// The prompt puts the pruned tree first and the candidate list last, so every
	// batch shares a long identical prefix and a provider with prefix caching pays
	// for it once.
	// One batch, measured rather than assumed. Splitting 40 candidates into four
	// concurrent requests of 10 was the obvious fix for a 98s round and it does
	// not work: measured 95.6s against 117.6s, a 19% gain for 3.8x the input
	// tokens and 2.9x the output.
	//
	// The reason is in the numbers. One request thinking about 40 candidates spent
	// 10,084 tokens; each request thinking about 10 spent 7,404-10,273. Thinking
	// barely tracks the candidate count -- it is the fixed cost of digesting the
	// 24.6k-token tree, and every batch pays it again. Total thinking therefore
	// scales with the number of requests, which makes splitting the wrong
	// direction: fewer, larger requests are cheaper, and the wall clock has a
	// floor of roughly one digest of the context (R-063 §4f).
	//
	// The machinery stays because it is what makes that answer checkable, and
	// because a smaller per-batch context is the one hypothesis it leaves open.
	advisorBatchSize = advisorCandidateLimit
	// Concurrency cap, because the failure mode of guessing high is a rate limit
	// that fails the whole analysis rather than a slow one.
	advisorMaxParallel = 4
	// Output budget per candidate, so a smaller batch gets a proportionally
	// smaller cap instead of the whole round's.
	perCandidateOutputTokens = triageMaxOutputTokens / advisorCandidateLimit
	minBatchOutputTokens     = 4000
)

// SetAdvisorBatching overrides the batch size and concurrency. For probes
// comparing shapes against a real endpoint; zero or negative keeps the default.
func (s *Service) SetAdvisorBatching(size, parallel int) {
	s.advisorMu.Lock()
	defer s.advisorMu.Unlock()
	if size > 0 {
		s.batchSize = size
	}
	if parallel > 0 {
		s.maxParallel = parallel
	}
}

func (s *Service) batching() (int, int) {
	s.advisorMu.RLock()
	defer s.advisorMu.RUnlock()
	size, parallel := s.batchSize, s.maxParallel
	if size <= 0 {
		size = advisorBatchSize
	}
	if parallel <= 0 {
		parallel = advisorMaxParallel
	}
	return size, parallel
}

// triageInBatches asks about the candidates in concurrent slices and merges the
// answers into one result. A batch that fails costs its own candidates and
// nothing else: the rest of the analysis stands, which is the same degradation
// the two-round flow already uses.
func (s *Service) triageInBatches(
	ctx context.Context, advisor ports.Advisor, system, evidence string,
	candidates []recommendation.EvidenceNode, pack EvidencePack,
) (recommendation.AdvisorResult, []RoundStats, error) {
	size, parallel := s.batching()
	var batches [][]recommendation.EvidenceNode
	for start := 0; start < len(candidates); start += size {
		end := start + size
		if end > len(candidates) {
			end = len(candidates)
		}
		batches = append(batches, candidates[start:end])
	}

	type outcome struct {
		result recommendation.AdvisorResult
		stats  RoundStats
		err    error
	}
	outcomes := make([]outcome, len(batches))
	gate := make(chan struct{}, parallel)
	var wait sync.WaitGroup
	for index, batch := range batches {
		wait.Add(1)
		go func(index int, batch []recommendation.EvidenceNode) {
			defer wait.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			budget := len(batch) * perCandidateOutputTokens
			if budget < minBatchOutputTokens {
				budget = minBatchOutputTokens
			}
			start := time.Now()
			result, err := advisor.Advise(ctx, ports.AdviceRequest{
				System:          system,
				User:            recommendation.TriagePrompt(evidence, pack.RenderCandidates(batch), len(batch)),
				MaxOutputTokens: budget,
			})
			outcomes[index] = outcome{
				result: result,
				stats: RoundStats{
					Name:    fmt.Sprintf("分诊 %d/%d", index+1, len(batches)),
					Seconds: time.Since(start).Seconds(), Asked: len(batch),
					InputTokens: result.InputTokens, OutputTokens: result.OutputTokens,
					ReasoningTokens: result.ReasoningTokens, Failed: err != nil,
				},
				err: err,
			}
		}(index, batch)
	}
	wait.Wait()

	var merged recommendation.AdvisorResult
	stats := make([]RoundStats, 0, len(batches))
	var firstErr error
	failed := 0
	for _, item := range outcomes {
		stats = append(stats, item.stats)
		if item.err != nil {
			failed++
			if firstErr == nil {
				firstErr = item.err
			}
			continue
		}
		merged.Verdicts = append(merged.Verdicts, item.result.Verdicts...)
		merged.InputTokens += item.result.InputTokens
		merged.OutputTokens += item.result.OutputTokens
		merged.ReasoningTokens += item.result.ReasoningTokens
	}
	// Every batch failing is the round failing. Some failing is a smaller answer,
	// which is worth more than no answer.
	if failed == len(batches) {
		return recommendation.AdvisorResult{}, stats, firstErr
	}
	return merged, stats, nil
}

// SetAdvisor installs or replaces the advisor. Nil disables it, which is the
// state the app ships in: without one configured the feature is the rule layer
// alone and nothing leaves the machine (ADR-0061 §4).
func (s *Service) SetAdvisor(advisor ports.Advisor) {
	s.advisorMu.Lock()
	defer s.advisorMu.Unlock()
	s.advisor = advisor
}

func (s *Service) currentAdvisor() ports.Advisor {
	s.advisorMu.RLock()
	defer s.advisorMu.RUnlock()
	return s.advisor
}

// AdvisorDescription names the configured endpoint and model, or "" when there
// is none. It never carries the credential.
func (s *Service) AdvisorDescription() string {
	if advisor := s.currentAdvisor(); advisor != nil {
		return advisor.Describe()
	}
	return ""
}

// RunAdvisorAnalysis is the two-pass flow. It always returns the rule layer's
// findings, whether or not an advisor is configured and whether or not it
// worked: a failed round trip must not cost the user the results that were
// produced locally.
func (s *Service) RunAdvisorAnalysis(ctx context.Context, snapshotID int64) (Advice, error) {
	pack, err := s.BuildEvidencePack(snapshotID)
	if err != nil {
		return Advice{}, err
	}
	advice := adviceFromPack(snapshotID, pack)

	advisor := s.currentAdvisor()
	if advisor == nil {
		return advice, nil
	}

	shown := indexLabelledNodes(pack)
	system := recommendation.SystemPrompt()

	candidates := pack.Candidates(advisorCandidateLimit)
	if len(candidates) == 0 {
		return advice, nil
	}
	asked := make([]int64, 0, len(candidates))
	for _, node := range candidates {
		asked = append(asked, node.ID)
	}
	round, batchStats, err := s.triageInBatches(ctx, advisor, system, pack.Text(), candidates, pack)
	advice.RoundStats = append(advice.RoundStats, batchStats...)
	if err != nil {
		if ctx.Err() != nil {
			return advice, ctx.Err()
		}
		advice.AdvisorError = err.Error()
		return advice, nil
	}
	advice.InputTokens += round.InputTokens
	advice.OutputTokens += round.OutputTokens

	// Coverage is now a property of the request: we asked about a fixed list, so
	// anything unanswered is a question the advisor dropped.
	advice.TopRows, advice.TopRowsAccounted = round.Coverage(asked)
	validation := recommendation.Validate(round.Cleanable(), shown, snapshotID)
	accepted := validation.Accepted
	advice.Rejected = append(advice.Rejected, validation.Rejected...)
	advice.Corrections = append(advice.Corrections, validation.Corrections...)
	advice.Rounds = 1

	// Round two: the regions the advisor itself said it could not classify.
	if focus := recommendation.LimitExpansions(round.Unresolved(), shown); len(focus) > 0 {
		unresolved := expansionsFor(round.Unresolved(), focus)
		evidence, expandedShown, expandErr := s.expansionEvidence(snapshotID, focus, shown)
		if expandErr != nil {
			advice.AdvisorError = "无法展开深挖区域：" + expandErr.Error()
		} else {
			expandStart := time.Now()
			second, secondErr := advisor.Advise(ctx, ports.AdviceRequest{
				System: system, User: recommendation.ExpandPrompt(evidence, unresolved), MaxOutputTokens: expandMaxOutputTokens,
			})
			advice.RoundStats = append(advice.RoundStats, RoundStats{
				Name: "深挖", Seconds: time.Since(expandStart).Seconds(), Asked: len(focus),
				InputTokens: second.InputTokens, OutputTokens: second.OutputTokens,
				ReasoningTokens: second.ReasoningTokens, Failed: secondErr != nil,
			})
			switch {
			case secondErr != nil && ctx.Err() != nil:
				return advice, ctx.Err()
			case secondErr != nil:
				// The first round's advice stands; only the deeper look failed.
				advice.AdvisorError = "深挖失败：" + secondErr.Error()
			default:
				advice.InputTokens += second.InputTokens
				advice.OutputTokens += second.OutputTokens
				secondValidation := recommendation.Validate(second.Cleanable(), expandedShown, snapshotID)
				accepted = append(accepted, secondValidation.Accepted...)
				advice.Rejected = append(advice.Rejected, secondValidation.Rejected...)
				advice.Corrections = append(advice.Corrections, secondValidation.Corrections...)
				advice.Rounds = 2
			}
		}
		advice.Expanded = len(focus)
	}

	merged, dropped := mergeAdvisorItems(advice.Items, accepted, shown)
	advice.Items = merged
	advice.Rejected = append(advice.Rejected, dropped...)
	advice.AdvisorItems = 0
	advice.TotalBytes = 0
	for _, item := range advice.Items {
		advice.TotalBytes += item.ReclaimableBytes
		if item.Source == recommendation.SourceAdvisor {
			advice.AdvisorItems++
		}
	}
	advice.RejectedSummary = recommendation.RejectionSummary(advice.Rejected)
	advice.CorrectionSummary = correctionSummary(advice.Corrections)
	return advice, nil
}

// mergeAdvisorItems puts advisor suggestions alongside the rule findings without
// letting the two describe the same bytes twice. Rules hold their ground: they
// are not guesses, and where both fire the deterministic answer is the one to
// keep. An advisor item that overlaps anything already accepted is recorded as
// rejected rather than dropped quietly.
func mergeAdvisorItems(ruleItems []AdviceItem, accepted []recommendation.Recommendation, shown map[int64]recommendation.EvidenceNode) ([]AdviceItem, []recommendation.Rejection) {
	merged := append([]AdviceItem(nil), ruleItems...)
	claimed := make([]string, 0, len(ruleItems)+len(accepted))
	for _, item := range ruleItems {
		claimed = append(claimed, item.Path)
	}
	rejected := []recommendation.Rejection{}

	sort.SliceStable(accepted, func(left, right int) bool {
		return accepted[left].ReclaimableBytes > accepted[right].ReclaimableBytes
	})
	for _, item := range accepted {
		node, known := shown[item.NodeID]
		if !known {
			// Round two's nodes are not in round one's index; the validation that
			// produced this item already checked them against the set it was shown.
			node = recommendation.EvidenceNode{ID: item.NodeID}
		}
		if node.Path == "" {
			continue
		}
		if overlapsAny(claimed, node.Path) {
			rejected = append(rejected, recommendation.Rejection{
				NodeID: item.NodeID, ClaimedName: node.Name, Reason: recommendation.RejectOverlapping,
			})
			continue
		}
		claimed = append(claimed, node.Path)
		merged = append(merged, AdviceItem{Recommendation: item, Name: node.Name, Path: node.Path})
	}
	sort.SliceStable(merged, func(left, right int) bool {
		return merged[left].ReclaimableBytes > merged[right].ReclaimableBytes
	})
	return merged, rejected
}

func overlapsAny(claimed []string, path string) bool {
	for _, existing := range claimed {
		if existing == path || cleanup.IsPathWithin(existing, path) || cleanup.IsPathWithin(path, existing) {
			return true
		}
	}
	return false
}

// expansionEvidence renders the insides of the regions the advisor asked about,
// and returns the widened set of nodes it may now refer to. The set is widened
// rather than replaced: an advisor is allowed to revise a round-one judgement
// once it has seen inside.
func (s *Service) expansionEvidence(snapshotID int64, focus []int64, shown map[int64]recommendation.EvidenceNode) (string, map[int64]recommendation.EvidenceNode, error) {
	widened := make(map[int64]recommendation.EvidenceNode, len(shown)+len(focus)*32)
	for id, node := range shown {
		widened[id] = node
	}
	var out strings.Builder
	for _, id := range focus {
		region, known := shown[id]
		if !known {
			continue
		}
		floor := region.OwnedAllocated / expansionShareOfRegion
		if floor < expansionAbsoluteFloor {
			floor = expansionAbsoluteFloor
		}
		pack, err := s.buildEvidencePackFor(snapshotID, id, floor, 0)
		if err != nil {
			return "", nil, err
		}
		for id, node := range indexLabelledNodes(pack) {
			widened[id] = node
		}
		fmt.Fprintf(&out, "## id %d：%s\n\n", id, region.Path)
		out.WriteString(pack.Text())
		out.WriteString("\n")
	}
	return out.String(), widened, nil
}

func expansionsFor(requested []recommendation.Verdict, focus []int64) []recommendation.Verdict {
	wanted := make(map[int64]bool, len(focus))
	for _, id := range focus {
		wanted[id] = true
	}
	asked := make([]recommendation.Verdict, 0, len(focus))
	for _, item := range requested {
		if wanted[item.NodeID] {
			wanted[item.NodeID] = false
			asked = append(asked, item)
		}
	}
	return asked
}

// indexLabelledNodes carries the rendered label onto each node, so validation
// checks an advisor's echo against the text it was actually shown.
// indexLabelledNodes carries the rendered label onto each node, so validation
// checks an advisor's echo against the text it was actually shown.
// coverageOfLargestRows counts how many of the biggest rows the advisor dealt
// with at all -- suggested, expanded, or explicitly declined. A row that appears
// in none of the three was passed over without saying so, which is the failure
// the skipped list exists to make visible.

// correctionSummary names what was overridden. Shown, not absorbed: a tool that
// says "the model called your photo library regenerable and I disagreed" is
// telling the user something they need to know about how much to trust it.
func correctionSummary(corrections []recommendation.Correction) string {
	if len(corrections) == 0 {
		return ""
	}
	// The two kinds are not the same statement and must not share a sentence:
	// one says the object never comes back, the other says it comes back only by
	// reinstalling the whole toolchain. Reporting a partial-install correction as
	// "无法找回" would be the tool telling its own lie about its own correction.
	permanent, partial := 0, 0
	for _, item := range corrections {
		if item.Reason == recommendation.PartialInstall {
			partial++
			continue
		}
		permanent++
	}
	parts := make([]string, 0, 2)
	if permanent > 0 {
		parts = append(parts, fmt.Sprintf("%d 条被判为可恢复，实际删除后无法找回，已改标为不可恢复", permanent))
	}
	if partial > 0 {
		parts = append(parts, fmt.Sprintf("%d 条位于已安装工具链内部，删除后不会自动重装，恢复代价已改标", partial))
	}
	return strings.Join(parts, "；")
}

func indexLabelledNodes(pack EvidencePack) map[int64]recommendation.EvidenceNode {
	labels := pack.RowLabels()
	index := make(map[int64]recommendation.EvidenceNode, len(pack.Nodes))
	for _, node := range pack.Nodes {
		node.Label = labels[node.ID]
		index[node.ID] = node
	}
	return index
}

func adviceFromPack(snapshotID int64, pack EvidencePack) Advice {
	items := pack.RuleFindings()
	advice := Advice{
		SnapshotID:    snapshotID,
		Items:         items,
		RuleItems:     len(items),
		EvidenceNodes: len(pack.Nodes),
		EvidenceBytes: len(pack.Text()),
		FloorBytes:    pack.FloorBytes,
	}
	for _, item := range items {
		advice.TotalBytes += item.ReclaimableBytes
	}
	return advice
}
