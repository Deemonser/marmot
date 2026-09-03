package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/ports"
)

// Triage now runs its batches concurrently, so the stub has to be safe to call
// from several goroutines. Without the lock the race detector finds this the
// moment a fixture has more candidates than one batch holds.
type fakeAdvisor struct {
	mu       sync.Mutex
	rounds   []recommendation.AdvisorResult
	errs     []error
	requests []ports.AdviceRequest
	// answer, when set, builds a reply from the request itself, which is how a
	// concurrent test says what it wants without depending on arrival order.
	answer func(ports.AdviceRequest) (recommendation.AdvisorResult, error)
}

func (f *fakeAdvisor) Describe() string { return "fake @ localhost" }

func (f *fakeAdvisor) Advise(ctx context.Context, request ports.AdviceRequest) (recommendation.AdvisorResult, error) {
	if err := ctx.Err(); err != nil {
		return recommendation.AdvisorResult{}, err
	}
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, request)
	answer := f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(request)
	}
	if index < len(f.errs) && f.errs[index] != nil {
		return recommendation.AdvisorResult{}, f.errs[index]
	}
	if index < len(f.rounds) {
		return f.rounds[index], nil
	}
	return recommendation.AdvisorResult{}, nil
}

func (f *fakeAdvisor) recorded() []ports.AdviceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.AdviceRequest(nil), f.requests...)
}

// The stub honours RootID so an expansion returns a subtree, the way the real
// store does.
func adviceService(t *testing.T, nodes []recommendation.EvidenceNode) (*Service, *stubEvidenceStore) {
	t.Helper()
	store := &stubEvidenceStore{result: recommendation.EvidenceResult{Root: "/Users/alice", Nodes: nodes}}
	return NewService(Dependencies{Store: store}), store
}

const gb = 1_000_000_000

func advisorNodes() []recommendation.EvidenceNode {
	return []recommendation.EvidenceNode{
		evidenceNode(1, 0, "/Users/alice", "alice", "directory", 20*gb, 1*gb),
		// A rule already covers this one.
		evidenceNode(2, 1, "/Users/alice/Library/Caches", "Caches", "directory", 6*gb, 6*gb),
		// Rules reach neither of these.
		evidenceNode(3, 1, "/Users/alice/Library/Containers/com.x/Data/Store", "Store", "directory", 5*gb, 5*gb),
		evidenceNode(4, 1, "/Users/alice/work/thing", "thing", "directory", 8*gb, 8*gb),
	}
}

func goodSuggestion(id int64, name string) recommendation.Verdict {
	return recommendation.Verdict{
		NodeID: id, Name: name, Verdict: recommendation.VerdictCleanable, Category: "应用缓存",
		Recovery:   string(recommendation.RecoveryRegenerable),
		Confidence: 0.7, Evidence: []string{"占用 5GB"},
		WhatBreaks: "应用要重新下载内容。", HowToRestore: "应用自行重建。",
	}
}

// Shipping state: no advisor configured. The feature is the rule layer, it
// works, and nothing leaves the machine.
func TestRunAdvisorAnalysisWithoutAnAdvisorIsTheRuleLayer(t *testing.T) {
	service, _ := adviceService(t, advisorNodes())
	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if advice.RuleItems == 0 || advice.AdvisorItems != 0 || advice.Rounds != 0 {
		t.Fatalf("expected rules only, got %#v", advice)
	}
	if advice.AdvisorError != "" {
		t.Fatalf("a missing advisor is not an error: %q", advice.AdvisorError)
	}
}

// A failed round trip must not cost the user the results produced locally.
func TestRunAdvisorAnalysisKeepsRuleFindingsWhenTheAdvisorFails(t *testing.T) {
	service, _ := adviceService(t, advisorNodes())
	service.SetAdvisor(&fakeAdvisor{errs: []error{errors.New("API key 被拒绝 (HTTP 401)")}})
	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatalf("an advisor failure must not fail the analysis: %v", err)
	}
	if advice.RuleItems == 0 {
		t.Fatal("the rule findings were lost with the advisor")
	}
	if !strings.Contains(advice.AdvisorError, "401") {
		t.Fatalf("the failure was not reported: %q", advice.AdvisorError)
	}
}

func TestRunAdvisorAnalysisReportsCancellation(t *testing.T) {
	service, _ := adviceService(t, advisorNodes())
	service.SetAdvisor(&fakeAdvisor{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.RunAdvisorAnalysis(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// Round two happens only because the advisor asked, and it is asked about the
// inside of the region it named.
func TestRunAdvisorAnalysisExpandsWhatTheAdvisorAsksAbout(t *testing.T) {
	service, _ := adviceService(t, advisorNodes())
	advisor := &fakeAdvisor{rounds: []recommendation.AdvisorResult{
		{
			Verdicts: []recommendation.Verdict{
				goodSuggestion(4, "thing"),
				{NodeID: 3, Name: "Store", Verdict: recommendation.VerdictUnknown, Why: "认不出这个应用的数据目录"},
			},
			InputTokens: 100, OutputTokens: 10,
		},
		{
			Verdicts:    []recommendation.Verdict{goodSuggestion(3, "Store")},
			InputTokens: 50, OutputTokens: 5,
		},
	}}
	service.SetAdvisor(advisor)

	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if advice.Rounds != 2 || advice.Expanded != 1 {
		t.Fatalf("expected two rounds over one region, got %d/%d", advice.Rounds, advice.Expanded)
	}
	if len(advisor.recorded()) != 2 {
		t.Fatalf("expected two calls, got %d", len(advisor.recorded()))
	}
	// The system block must be byte-identical, or every provider's prompt cache
	// misses on the second call.
	if advisor.recorded()[0].System != advisor.recorded()[1].System {
		t.Fatal("the system block changed between rounds")
	}
	if !strings.Contains(advisor.recorded()[1].User, "id 3") {
		t.Fatalf("round two did not restate what was asked:\n%s", advisor.recorded()[1].User)
	}
	if !strings.Contains(advisor.recorded()[1].User, "认不出这个应用的数据目录") {
		t.Fatal("round two dropped the advisor's own reason for asking")
	}
	if advice.InputTokens != 150 || advice.OutputTokens != 15 {
		t.Fatalf("token accounting is %d/%d", advice.InputTokens, advice.OutputTokens)
	}
	if advice.AdvisorItems != 2 {
		t.Fatalf("expected both rounds' suggestions, got %d", advice.AdvisorItems)
	}
}

// Rules are not guesses. Where both fire on the same bytes the deterministic
// answer stays, and the advisor's is recorded as refused rather than dropped.
func TestRunAdvisorAnalysisKeepsTheRuleWhenBothClaimTheSameBytes(t *testing.T) {
	service, _ := adviceService(t, advisorNodes())
	service.SetAdvisor(&fakeAdvisor{rounds: []recommendation.AdvisorResult{
		{Verdicts: []recommendation.Verdict{goodSuggestion(2, "Caches")}},
	}})
	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range advice.Items {
		if item.NodeID == 2 && item.Source != recommendation.SourceRule {
			t.Fatalf("the advisor displaced a rule on node 2: %#v", item)
		}
	}
	found := false
	for _, item := range advice.Rejected {
		if item.NodeID == 2 && item.Reason == recommendation.RejectOverlapping {
			found = true
		}
	}
	if !found {
		t.Fatalf("the overlap was dropped silently: %#v", advice.Rejected)
	}
	if advice.RejectedSummary == "" {
		t.Fatal("a refusal the user cannot see is a refusal they cannot weigh")
	}
}

// The second round failing must not discard the first round's advice.
func TestRunAdvisorAnalysisKeepsRoundOneWhenRoundTwoFails(t *testing.T) {
	service, _ := adviceService(t, advisorNodes())
	service.SetAdvisor(&fakeAdvisor{
		rounds: []recommendation.AdvisorResult{{
			Verdicts: []recommendation.Verdict{
				goodSuggestion(4, "thing"),
				{NodeID: 3, Name: "Store", Verdict: recommendation.VerdictUnknown, Why: "认不出"},
			},
		}},
		errs: []error{nil, errors.New("触发限流 (HTTP 429)")},
	})
	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if advice.AdvisorItems != 1 {
		t.Fatalf("round one's advice was lost: %#v", advice.Items)
	}
	if !strings.Contains(advice.AdvisorError, "深挖失败") {
		t.Fatalf("the second round's failure was not reported: %q", advice.AdvisorError)
	}
}

// Enough candidates that the triage actually splits. Every node is outside the
// rule catalog, so all of them reach the advisor.
func batchableNodes(count int) []recommendation.EvidenceNode {
	nodes := []recommendation.EvidenceNode{
		evidenceNode(1, 0, "/Users/alice", "alice", "directory", int64(count+2)*gb, 0),
		// One the rule layer covers, so "the advisor failed" can be told apart
		// from "there was nothing to find". Rule-covered paths are not offered as
		// candidates, so it does not change the batch arithmetic.
		evidenceNode(999, 1, "/Users/alice/Library/Caches/go-build", "go-build", "directory", gb, gb),
	}
	for index := 0; index < count; index++ {
		id := int64(index + 2)
		name := fmt.Sprintf("thing%02d", index)
		nodes = append(nodes, evidenceNode(id, 1, "/Users/alice/work/"+name, name, "directory", gb, gb))
	}
	return nodes
}

// Batching is off by default -- measured, it costs 3.8x the input tokens for 19%
// (R-063 §4f) -- but the mechanism has to keep working, because a smaller
// per-batch context is the hypothesis that measurement leaves open and this is
// what it would be tested with.
func TestTriageSplitsCandidatesIntoConcurrentBatches(t *testing.T) {
	service, _ := adviceService(t, batchableNodes(25))
	service.SetAdvisorBatching(10, 4)
	var concurrent, peak int32
	advisor := &fakeAdvisor{answer: func(request ports.AdviceRequest) (recommendation.AdvisorResult, error) {
		running := atomic.AddInt32(&concurrent, 1)
		for {
			was := atomic.LoadInt32(&peak)
			if running <= was || atomic.CompareAndSwapInt32(&peak, was, running) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return recommendation.AdvisorResult{}, nil
	}}
	service.SetAdvisor(advisor)
	if _, err := service.RunAdvisorAnalysis(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	requests := advisor.recorded()
	if len(requests) != 3 {
		t.Fatalf("25 candidates in batches of 10 should be 3 requests, got %d", len(requests))
	}
	if peak < 2 {
		t.Fatalf("batches ran one at a time (peak %d); splitting them buys nothing then", peak)
	}
	// The prompt puts the tree first and the candidates last precisely so a
	// provider's prefix cache pays for the tree once. Diverging system blocks or
	// reordered context would silently give that up.
	for index := 1; index < len(requests); index++ {
		if requests[index].System != requests[0].System {
			t.Fatal("the system block differs between batches, so every batch is a cache miss")
		}
	}
	// Every candidate asked exactly once: a dropped one is a silent gap in
	// coverage, a repeated one is paid for twice.
	//
	// Only the candidate list counts. The pruned tree is the shared prefix and
	// names every node in it, so searching the whole prompt says every batch asks
	// about everything -- which is what the tree looks like, not what was asked.
	seen := map[string]int{}
	for _, request := range requests {
		_, list, found := strings.Cut(request.User, "# 候选清单")
		if !found {
			t.Fatal("the prompt no longer separates the candidate list from the tree")
		}
		for index := 0; index < 25; index++ {
			if strings.Contains(list, fmt.Sprintf("thing%02d", index)) {
				seen[fmt.Sprintf("thing%02d", index)]++
			}
		}
	}
	for index := 0; index < 25; index++ {
		name := fmt.Sprintf("thing%02d", index)
		if seen[name] != 1 {
			t.Errorf("%s appeared in %d batches, expected exactly 1", name, seen[name])
		}
	}
}

// One batch failing costs its own candidates and nothing else. The same
// degradation the two-round flow already uses: a smaller answer beats no answer.
func TestOneFailedBatchDoesNotLoseTheOthers(t *testing.T) {
	service, _ := adviceService(t, batchableNodes(25))
	service.SetAdvisorBatching(10, 4)
	var calls int32
	advisor := &fakeAdvisor{answer: func(request ports.AdviceRequest) (recommendation.AdvisorResult, error) {
		if atomic.AddInt32(&calls, 1) == 2 {
			return recommendation.AdvisorResult{}, errors.New("触发限流 (HTTP 429)")
		}
		// Answer for whichever candidate this batch happens to hold.
		var verdicts []recommendation.Verdict
		_, list, _ := strings.Cut(request.User, "# 候选清单")
		for index := 0; index < 25; index++ {
			name := fmt.Sprintf("thing%02d", index)
			if strings.Contains(list, name) {
				verdicts = append(verdicts, goodSuggestion(int64(index+2), name))
			}
		}
		return recommendation.AdvisorResult{Verdicts: verdicts}, nil
	}}
	service.SetAdvisor(advisor)
	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if advice.AdvisorItems == 0 {
		t.Fatal("one rate-limited batch discarded the whole round")
	}
	if advice.AdvisorItems > 20 {
		t.Fatalf("the failed batch's candidates were answered anyway: %d", advice.AdvisorItems)
	}
}

// Every batch failing is the round failing, and it must say so rather than
// report an empty analysis as a clean result.
func TestAllBatchesFailingIsReportedAsAFailure(t *testing.T) {
	service, _ := adviceService(t, batchableNodes(25))
	service.SetAdvisorBatching(10, 4)
	advisor := &fakeAdvisor{answer: func(ports.AdviceRequest) (recommendation.AdvisorResult, error) {
		return recommendation.AdvisorResult{}, errors.New("API key 被拒绝 (HTTP 401)")
	}}
	service.SetAdvisor(advisor)
	advice, err := service.RunAdvisorAnalysis(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(advice.AdvisorError, "401") {
		t.Fatalf("a wholly failed round reported %q", advice.AdvisorError)
	}
	if advice.RuleItems == 0 {
		t.Fatal("the rule findings went down with the advisor")
	}
}
