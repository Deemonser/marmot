package probe

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/advisor/openaicompat"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
)

// The first real round trip. The credential is read from the encrypted store the
// app already wrote it to, so it never appears on a command line, in an
// environment variable, in this file, or in any transcript.
//
//	PROBE_ROOT=$HOME go test ./internal/probe -run Advisor -v -timeout 20m
//
// Configure the endpoint and model in the app first (AI 设置). This is a probe,
// not a gate: it spends real money and depends on a third party being up.
func TestAdvisorRoundTripOnARealTree(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real advisor probe")
	}
	adapter := platform.Adapter{}
	rawConfig, err := adapter.LoadCredential("advisor-config")
	if err != nil {
		t.Skipf("no advisor configured yet: %v", err)
	}
	var settings marmotapp.AdvisorSettings
	if err := json.Unmarshal([]byte(rawConfig), &settings); err != nil {
		t.Fatalf("stored advisor config is not readable: %v", err)
	}
	apiKey, err := adapter.LoadCredential("advisor-key")
	if err != nil {
		t.Skipf("no advisor key stored: %v", err)
	}

	store := memtree.OpenStore()
	defer store.Close()
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
		Credentials: adapter,
		AdvisorFactory: func(s marmotapp.AdvisorSettings, key string) (ports.Advisor, error) {
			return openaicompat.New(openaicompat.Config{BaseURL: s.BaseURL, Model: s.Model, APIKey: key, JSONMode: s.JSONMode})
		},
	})
	// A configuration saved before the effort field existed carries an empty
	// value, which omits the field and lets the provider default apply. On
	// deepseek-v4-flash that default is "high", and it spent the whole output
	// budget thinking before it finished the answer.
	effort := settings.ReasoningEffort
	if effort == "" {
		effort = "low"
	}
	advisor, err := openaicompat.New(openaicompat.Config{
		BaseURL: settings.BaseURL, Model: settings.Model, APIKey: apiKey,
		JSONMode: settings.JSONMode, ReasoningEffort: effort,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.SetAdvisor(advisor)
	t.Logf("advisor: %s（推理档 %q）", advisor.Describe(), effort)

	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var final marmotapp.ScanStatus
	for {
		if final, err = service.GetScanStatus(status.TaskID); err != nil {
			t.Fatal(err)
		}
		if final.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	start := time.Now()
	advice, err := service.RunAdvisorAnalysis(ctx, final.SnapshotID)
	if err != nil {
		t.Fatalf("advisor analysis: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("用时 %.1fs：%d 轮，深挖 %d 处，token %d in / %d out",
		elapsed.Seconds(), advice.Rounds, advice.Expanded, advice.InputTokens, advice.OutputTokens)
	t.Logf("规则 %d 条，AI %d 条，合计 %s", advice.RuleItems, advice.AdvisorItems, humanBytes(advice.TotalBytes))
	if advice.AdvisorError != "" {
		t.Logf("advisor error: %s", advice.AdvisorError)
	}
	if len(advice.Rejected) > 0 {
		t.Logf("校验丢弃 %d 条：%s", len(advice.Rejected), advice.RejectedSummary)
		for index, item := range advice.Rejected {
			if index >= 8 {
				break
			}
			t.Logf("  丢弃 node=%d claimed=%q reason=%s", item.NodeID, item.ClaimedName, item.Reason)
		}
	}
	for index, item := range advice.Items {
		if index >= 25 {
			break
		}
		source := "规则"
		if string(item.Source) == "advisor" {
			source = "AI"
		}
		t.Logf("  [%s] %-9s %-6s %-16s %s", source, humanBytes(item.ReclaimableBytes), item.Risk, item.Category, item.Path)
		if string(item.Source) == "advisor" {
			t.Logf("        依据: %v", item.Evidence)
			t.Logf("        删除后: %s", item.WhatBreaks)
		}
	}
	if advice.Rounds == 0 {
		t.Error("the advisor was configured but never called")
	}
}
