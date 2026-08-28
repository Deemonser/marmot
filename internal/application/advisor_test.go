package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/ports"
)

type fakeAdvisor struct {
	rounds   []recommendation.AdvisorResult
	errs     []error
	requests []ports.AdviceRequest
}

func (f *fakeAdvisor) Describe() string { return "fake @ localhost" }

func (f *fakeAdvisor) Advise(ctx context.Context, request ports.AdviceRequest) (recommendation.AdvisorResult, error) {
	if err := ctx.Err(); err != nil {
		return recommendation.AdvisorResult{}, err
	}
	index := len(f.requests)
	f.requests = append(f.requests, request)
	if index < len(f.errs) && f.errs[index] != nil {
		return recommendation.AdvisorResult{}, f.errs[index]
	}
	if index < len(f.rounds) {
		return f.rounds[index], nil
	}
	return recommendation.AdvisorResult{}, nil
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

func goodSuggestion(id int64, name string) recommendation.Suggestion {
	return recommendation.Suggestion{
		NodeID: id, Name: name, Category: "应用缓存",
		Recovery: string(recommendation.RecoveryRegenerable), Risk: string(recommendation.RiskReview),
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
			Suggestions:    []recommendation.Suggestion{goodSuggestion(4, "thing")},
			NeedsExpansion: []recommendation.Expansion{{NodeID: 3, Why: "认不出这个应用的数据目录"}},
			InputTokens:    100, OutputTokens: 10,
		},
		{
			Suggestions: []recommendation.Suggestion{goodSuggestion(3, "Store")},
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
	if len(advisor.requests) != 2 {
		t.Fatalf("expected two calls, got %d", len(advisor.requests))
	}
	// The system block must be byte-identical, or every provider's prompt cache
	// misses on the second call.
	if advisor.requests[0].System != advisor.requests[1].System {
		t.Fatal("the system block changed between rounds")
	}
	if !strings.Contains(advisor.requests[1].User, "id 3") {
		t.Fatalf("round two did not restate what was asked:\n%s", advisor.requests[1].User)
	}
	if !strings.Contains(advisor.requests[1].User, "认不出这个应用的数据目录") {
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
		{Suggestions: []recommendation.Suggestion{goodSuggestion(2, "Caches")}},
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
			Suggestions:    []recommendation.Suggestion{goodSuggestion(4, "thing")},
			NeedsExpansion: []recommendation.Expansion{{NodeID: 3}},
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
