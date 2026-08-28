package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/infrastructure/advisor/openaicompat"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// R-063: the measurable half of "is the advice any good".
//
// "Accuracy" is the wrong frame for most of this: whether ~/.rustup/toolchains/
// 1.89.0 should go depends on whether the person still uses Rust 1.89, and no
// algorithm answers that. What CAN be measured without an oracle is:
//
//   - citation fidelity -- the model quotes figures, and every figure it was
//     given came from the evidence pack, so each one can be checked back
//     against it. A cited number that appears nowhere is invented.
//   - stability -- the same pack asked N times. An object called `safe` once and
//     `risky` the next time is untrustworthy both times, however plausible each
//     answer looked on its own.
//   - guard violations -- what the validator refused, by reason.
//
// What cannot be measured here is whether a suggestion is a good idea. That
// needs a human, once: this run emits a deduplicated labelling sheet, and the
// labels become a fixture every later prompt or model change is scored against.
//
//	PROBE_ROOT=$HOME PROBE_RUNS=3 go test ./internal/probe -run Baseline -v -timeout 60m
func TestAdvisorBaseline(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the advisor baseline")
	}
	runs := 3
	if raw := os.Getenv("PROBE_RUNS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			runs = parsed
		}
	}
	adapter := platform.Adapter{}
	settings, apiKey, ok := storedAdvisor(t, adapter)
	if !ok {
		return
	}

	store := memtree.OpenStore()
	defer store.Close()
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
		Credentials: adapter,
	})
	// One variable at a time: this run changes temperature and nothing else, so
	// a difference against the 0.35 baseline is attributable.
	temperature := 0.0
	if raw := os.Getenv("PROBE_TEMPERATURE"); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			temperature = parsed
		}
	}
	advisor, err := openaicompat.New(openaicompat.Config{
		BaseURL: settings.BaseURL, Model: settings.Model, APIKey: apiKey,
		JSONMode: settings.JSONMode, ReasoningEffort: effortOrDefault(settings.ReasoningEffort),
		Temperature: &temperature,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.SetAdvisor(advisor)

	final := scanOnce(t, service, root)
	// One snapshot, one pack: every run is asked exactly the same question, so a
	// difference between runs is the model's, not the input's.
	pack, err := service.BuildEvidencePack(final.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	packText := pack.Text()
	t.Logf("证据包：%d 节点 / %d 字节，下限 %s；temperature=%.1f", len(pack.Nodes), len(packText), humanBytes(pack.FloorBytes), temperature)

	observed := []marmotapp.AdviceItem{}
	riskByNode := map[int64]map[string]int{}
	recoveryByPath := map[string]map[string]int{}
	corrections := 0
	seenByRun := []map[int64]bool{}

	for run := 1; run <= runs; run++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		start := time.Now()
		advice, runErr := service.RunAdvisorAnalysis(ctx, final.SnapshotID)
		cancel()
		if runErr != nil {
			t.Fatalf("run %d: %v", run, runErr)
		}
		present := map[int64]bool{}
		advisorItems := 0
		for _, item := range advice.Items {
			if item.Source != recommendation.SourceAdvisor {
				continue
			}
			advisorItems++
			present[item.NodeID] = true
			observed = append(observed, item)
			if riskByNode[item.NodeID] == nil {
				riskByNode[item.NodeID] = map[string]int{}
			}
			riskByNode[item.NodeID][string(item.Risk)]++
			// Keyed by path: recoverability is the axis a person decides on, and
			// node ids change with every scan.
			if recoveryByPath[item.Path] == nil {
				recoveryByPath[item.Path] = map[string]int{}
			}
			recoveryByPath[item.Path][string(item.Recovery)]++
		}
		seenByRun = append(seenByRun, present)
		corrections += len(advice.Corrections)
		coverage := 0.0
		if advice.TopRows > 0 {
			coverage = float64(advice.TopRowsAccounted) / float64(advice.TopRows) * 100
		}
		t.Logf("run %d：%.0fs，%d 轮，深挖 %d，AI %d 条，丢弃 %d（%s）；最大 %d 行交代了 %d 条（%.0f%%）%s",
			run, time.Since(start).Seconds(), advice.Rounds, advice.Expanded, advisorItems,
			len(advice.Rejected), advice.RejectedSummary,
			advice.TopRows, advice.TopRowsAccounted, coverage, faultSuffix(advice.AdvisorError))
		if advice.CorrectionSummary != "" {
			t.Logf("        纠正：%s", advice.CorrectionSummary)
			for _, item := range advice.Corrections {
				t.Logf("          %s ← 模型判 %s，守卫判 %s", item.Path, item.ClaimedRecovery, item.Reason)
			}
		}
	}

	reportStability(t, seenByRun, riskByNode)
	reportRecoveryDrift(t, recoveryByPath, corrections)
	reportCitations(t, observed, packText)
	writeLabelSheet(t, observed, riskByNode, runs, describeContents(pack))
}

func storedAdvisor(t *testing.T, adapter platform.Adapter) (marmotapp.AdvisorSettings, string, bool) {
	t.Helper()
	raw, err := adapter.LoadCredential("advisor-config")
	if err != nil {
		t.Skipf("no advisor configured yet: %v", err)
		return marmotapp.AdvisorSettings{}, "", false
	}
	var settings marmotapp.AdvisorSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatalf("stored advisor config is not readable: %v", err)
	}
	key, err := adapter.LoadCredential("advisor-key")
	if err != nil {
		t.Skipf("no advisor key stored: %v", err)
		return marmotapp.AdvisorSettings{}, "", false
	}
	return settings, key, true
}

func effortOrDefault(effort string) string {
	if strings.TrimSpace(effort) == "" {
		return "low"
	}
	return effort
}

func scanOnce(t *testing.T, service *marmotapp.Service, root string) marmotapp.ScanStatus {
	t.Helper()
	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for {
		final, err := service.GetScanStatus(status.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if final.State != "running" {
			return final
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func faultSuffix(fault string) string {
	if fault == "" {
		return ""
	}
	return "  [" + fault + "]"
}

// reportStability asks whether the same question twice gets the same answer. An
// object labelled `safe` in one run and `risky` in the next is not trustworthy
// in either, however reasonable each answer looked alone.
func reportStability(t *testing.T, seenByRun []map[int64]bool, riskByNode map[int64]map[string]int) {
	t.Helper()
	if len(seenByRun) < 2 {
		return
	}
	pairs, total := 0, 0.0
	for left := 0; left < len(seenByRun); left++ {
		for right := left + 1; right < len(seenByRun); right++ {
			total += jaccard(seenByRun[left], seenByRun[right])
			pairs++
		}
	}
	t.Logf("稳定性：两两 Jaccard 平均 %.2f（1.0 表示每次给出完全相同的对象集合）", total/float64(pairs))

	always, sometimes, drifting := 0, 0, 0
	for node, risks := range riskByNode {
		appearances := 0
		for _, run := range seenByRun {
			if run[node] {
				appearances++
			}
		}
		if appearances == len(seenByRun) {
			always++
		} else {
			sometimes++
		}
		if len(risks) > 1 {
			drifting++
		}
	}
	t.Logf("对象：每次都出现 %d 个，时有时无 %d 个", always, sometimes)
	if drifting > 0 {
		t.Logf("**风险等级漂移 %d 个对象**——同一个东西在不同运行里被判成不同风险", drifting)
		for node, risks := range riskByNode {
			if len(risks) > 1 {
				labels := make([]string, 0, len(risks))
				for risk, count := range risks {
					labels = append(labels, fmt.Sprintf("%s×%d", risk, count))
				}
				sort.Strings(labels)
				t.Logf("   node=%d  %s", node, strings.Join(labels, " / "))
			}
		}
	}
}

// reportRecoveryDrift measures the axis that now decides everything. Risk drift
// was being reported while recoverability was the field a person actually acts
// on -- measuring the demoted axis and not the promoted one.
func reportRecoveryDrift(t *testing.T, byPath map[string]map[string]int, corrections int) {
	t.Helper()
	drifting := make([]string, 0, 4)
	for path, kinds := range byPath {
		if len(kinds) > 1 {
			labels := make([]string, 0, len(kinds))
			for kind, count := range kinds {
				labels = append(labels, fmt.Sprintf("%s×%d", kind, count))
			}
			sort.Strings(labels)
			drifting = append(drifting, fmt.Sprintf("%s  %s", strings.Join(labels, " / "), path))
		}
	}
	sort.Strings(drifting)
	t.Logf("恢复性漂移：%d / %d 个对象在不同运行里被判成不同的可恢复性", len(drifting), len(byPath))
	for index, line := range drifting {
		if index >= 8 {
			break
		}
		t.Logf("   %s", line)
	}
	t.Logf("守卫纠正：%d 次（模型说可恢复、已知清单说删了就没了）", corrections)
}

func jaccard(left, right map[int64]bool) float64 {
	union := map[int64]bool{}
	shared := 0
	for id := range left {
		union[id] = true
		if right[id] {
			shared++
		}
	}
	for id := range right {
		union[id] = true
	}
	if len(union) == 0 {
		return 1
	}
	return float64(shared) / float64(len(union))
}

// citationPattern matches the figures a suggestion quotes: sizes rendered the
// way the pack renders them, and ages in days.
var citationPattern = regexp.MustCompile(`\d+(?:\.\d+)?\s*(?:B|KB|MB|GB|TB)\b|\d+\s*天`)

// reportCitations checks every quoted figure back against the pack. Each figure
// the model was given came from there, so one that appears nowhere in it was
// invented -- the sharpest hallucination signal available without a human.
//
// Unmatched is not the same as fabricated: a model may legitimately add two rows
// together. The count is a flag to eyeball, not a verdict, and the unmatched
// values are printed so they can be.
func reportCitations(t *testing.T, items []marmotapp.AdviceItem, packText string) {
	t.Helper()
	normalised := strings.ReplaceAll(packText, " ", "")
	total, matched := 0, 0
	unmatched := map[string]int{}
	for _, item := range items {
		for _, evidence := range item.Evidence {
			for _, figure := range citationPattern.FindAllString(evidence, -1) {
				total++
				needle := strings.ReplaceAll(figure, " ", "")
				needle = strings.TrimSuffix(needle, "天")
				if strings.Contains(normalised, needle) {
					matched++
				} else {
					unmatched[figure]++
				}
			}
		}
	}
	if total == 0 {
		t.Log("引用核验：建议里没有可核验的数字——提示词要求引用输入里的事实，这本身是个问题")
		return
	}
	t.Logf("引用核验：%d 个数字中 %d 个能在证据包里找到（%.1f%%）", total, matched, float64(matched)/float64(total)*100)
	if len(unmatched) > 0 {
		keys := make([]string, 0, len(unmatched))
		for figure := range unmatched {
			keys = append(keys, figure)
		}
		sort.Slice(keys, func(a, b int) bool { return unmatched[keys[a]] > unmatched[keys[b]] })
		t.Log("包里找不到的数字（可能是模型自己加总的，也可能是编的，需人工看）：")
		for index, figure := range keys {
			if index >= 12 {
				break
			}
			t.Logf("   %s ×%d", figure, unmatched[figure])
		}
	}
}

// describeContents summarises what is actually inside each node, so a row can be
// judged without leaving the file. Path plus one sentence was not enough: the
// person who owns the machine still could not tell what several of them were.
func describeContents(pack marmotapp.EvidencePack) map[int64]string {
	children := map[int64][]recommendation.EvidenceNode{}
	for _, node := range pack.Nodes {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	summary := make(map[int64]string, len(pack.Nodes))
	for _, node := range pack.Nodes {
		group := children[node.ID]
		sort.Slice(group, func(a, b int) bool { return group[a].OwnedAllocated > group[b].OwnedAllocated })
		parts := make([]string, 0, 4)
		for index, child := range group {
			if index >= 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %s", child.Name, humanBytes(child.OwnedAllocated)))
		}
		if len(parts) == 0 {
			profile := make([]string, 0, 3)
			for _, share := range node.TopExtensions {
				profile = append(profile, fmt.Sprintf("%s %s", share.Extension, humanBytes(share.Bytes)))
			}
			parts = profile
		}
		summary[node.ID] = fmt.Sprintf("%d 文件 / %d 目录；%s", node.SubtreeFiles, node.SubtreeDirs, strings.Join(parts, "，"))
	}
	return summary
}

const labelSheetPath = "../../docs/research/fixtures/R-063-labels.tsv"

// writeLabelSheet emits the one thing a human has to do, deduplicated across
// runs so the work is bounded. The labels become a fixture: every later prompt
// or model change is scored against them automatically, so this is paid once.
func writeLabelSheet(t *testing.T, items []marmotapp.AdviceItem, riskByNode map[int64]map[string]int, runs int, contents map[int64]string) {
	t.Helper()
	type row struct {
		item  marmotapp.AdviceItem
		seen  int
		risks map[string]int
	}
	// Keyed by path: node ids are assigned per scan, so the same object gets a
	// different id every time the disk is walked and a fixture keyed by id would
	// silently stop matching anything.
	unique := map[string]*row{}
	for _, item := range items {
		if existing, ok := unique[item.Path]; ok {
			existing.seen++
			continue
		}
		unique[item.Path] = &row{item: item, seen: 1, risks: riskByNode[item.NodeID]}
	}
	ordered := make([]*row, 0, len(unique))
	for _, entry := range unique {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(a, b int) bool {
		return ordered[a].item.ReclaimableBytes > ordered[b].item.ReclaimableBytes
	})

	var out strings.Builder
	out.WriteString("# R-063 建议标注表\n")
	out.WriteString("#\n")
	out.WriteString("# 只填第一列。问题不是「这东西该不该删」——那要求你预测自己未来还用不用它。\n")
	out.WriteString("# 也不是「你介不介意」——那还是偏好。问题是一个事实问题：\n")
	out.WriteString("#\n")
	out.WriteString("#     它给的 recovery 判断对不对？删了之后真的能按它说的方式回来吗？\n")
	out.WriteString("#\n")
	out.WriteString("#   ok     = 对。删了确实能重建/重新下载/重装回来\n")
	out.WriteString("#   bad    = 错。删了就没了，或者恢复代价远超它说的   ← 这一条就是缺陷\n")
	out.WriteString("#   unsure = 我不知道这东西删了能不能回来\n")
	out.WriteString("#\n")
	out.WriteString("# 为什么问这个：可恢复的东西判错了，最坏结果是多等一次下载；\n")
	out.WriteString("# 不可恢复的东西被说成可恢复，是唯一无法挽回的错误。重装整个工具链\n")
	out.WriteString("# （Flutter、Rust）算完全可接受的恢复方式，那种不要标 bad。\n")
	out.WriteString("#\n")
	out.WriteString("# unsure 不是「没做完」：如果连你都不知道它能不能回来，那这条建议\n")
	out.WriteString("# 给出的 recovery 判断就没有依据。答不上来的可以留空。\n")
	out.WriteString("#\n")
	out.WriteString("# risk_runs 显示三次运行分别判成什么。带 | 的是它自己都拿不定主意的对象。\n")
	out.WriteString("#\n")
	out.WriteString("# 自动评分按 **path** 对齐，不按 node_id：node_id 是每次扫描重新编号的，\n")
	out.WriteString("# 重扫一次同一个路径就是另一个 id，按它对齐的标注跨扫描全部失效。\n")
	fmt.Fprintf(&out, "# 共 %d 条，来自 %d 次运行的去重合并。其余列不要改。\n", len(ordered), runs)
	out.WriteString("#\n")
	out.WriteString("label\tnode_id\tbytes\trecovery\trisk\trisk_runs\tseen_runs\tcategory\tpath\tcontents\twhat_breaks\thow_to_restore\n")
	for _, entry := range ordered {
		risks := make([]string, 0, len(entry.risks))
		for risk, count := range entry.risks {
			risks = append(risks, fmt.Sprintf("%s×%d", risk, count))
		}
		sort.Strings(risks)
		fmt.Fprintf(&out, "\t%d\t%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\t%s\t%s\n",
			entry.item.NodeID, humanBytes(entry.item.ReclaimableBytes),
			entry.item.Recovery, entry.item.Risk,
			strings.Join(risks, "|"), entry.seen, runs,
			clean(entry.item.Category), clean(entry.item.Path),
			clean(contents[entry.item.NodeID]),
			clean(entry.item.WhatBreaks), clean(entry.item.HowToRestore))
	}
	// An empty result must not overwrite a good sheet. Learned the hard way: a
	// run that failed on a 402 wrote a zero-row file over a 26-row one that had
	// cost fifteen real analyses to produce, and nothing else had a copy.
	if len(ordered) == 0 {
		t.Logf("本次没有产出建议，保留 %s 不动（避免把已有清单覆盖成空）", labelSheetPath)
		return
	}
	if existing, err := os.ReadFile(labelSheetPath); err == nil && len(existing) > 0 {
		backup := labelSheetPath + ".prev"
		if err := os.WriteFile(backup, existing, 0o644); err != nil {
			t.Logf("警告：无法备份既有清单：%v", err)
		}
	}
	if err := os.WriteFile(labelSheetPath, []byte(out.String()), 0o644); err != nil {
		t.Fatalf("write label sheet: %v", err)
	}
	t.Logf("待标注清单已写入 %s：%d 条（%d 次运行去重后）", labelSheetPath, len(ordered), runs)
}

// clean keeps a field on one TSV line.
func clean(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
