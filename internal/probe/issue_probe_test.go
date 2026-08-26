package probe

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/platform"
)

// TestScanIssueClassification splits the paths the walk could not read into the
// two kinds that need different fixes: TCC-protected user data (Full Disk Access
// solves it) and root-only Unix paths (only a privileged helper solves it).
func key2(path string) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	key := "/" + parts[0]
	if len(parts) > 1 {
		key += "/" + parts[1]
	}
	return key
}

func TestScanIssueClassification(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	adapter := platform.Adapter{}
	engine := scanner.Scanner{MountResolver: adapter.ListMounts}
	result, err := engine.ScanBatched(context.Background(), root, func([]scan.Node) error { return nil }, func(scan.Phase) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	// TCC guards these user-data locations; everything else that denies us is
	// ordinary Unix ownership.
	tccPrefixes := []string{
		home + "/Desktop", home + "/Documents", home + "/Downloads",
		home + "/Library/Mobile Documents", home + "/Library/Containers",
		home + "/Library/Application Support/com.apple.TCC",
		home + "/Library/Messages", home + "/Library/Mail", home + "/Library/Safari",
		home + "/Pictures/Photos Library.photoslibrary",
	}
	buckets := map[string]int{}
	eperm := map[string]int{}
	eacces := map[string]int{}
	var tcc, unixDenied, other int
	for _, issue := range result.Issues {
		isTCC := false
		for _, prefix := range tccPrefixes {
			if strings.HasPrefix(issue.Path, prefix) {
				isTCC = true
				break
			}
		}
		// errno 1 (EPERM) is what the TCC policy layer returns; errno 13 (EACCES)
		// is ordinary Unix ownership. The first is what Full Disk Access fixes,
		// the second needs a privileged helper.
		switch {
		case strings.HasSuffix(issue.Message, "errno 1"):
			tcc++
			if !isTCC {
				eperm[key2(issue.Path)]++
			}
		case strings.HasSuffix(issue.Message, "errno 13"):
			unixDenied++
			eacces[key2(issue.Path)]++
		default:
			other++
		}
		_ = isTCC
		// Bucket by the first two path segments so the shape is visible.
		parts := strings.SplitN(strings.TrimPrefix(issue.Path, "/"), "/", 3)
		key := "/" + parts[0]
		if len(parts) > 1 {
			key += "/" + parts[1]
		}
		buckets[key]++
	}
	t.Logf("PROBE issues_total=%d eperm_tcc=%d eacces_unix=%d other=%d", len(result.Issues), tcc, unixDenied, other)
	for label, set := range map[string]map[string]int{"EPERM(FDA)": eperm, "EACCES(root)": eacces} {
		top := make([]string, 0, len(set))
		for key := range set {
			top = append(top, key)
		}
		sort.Slice(top, func(i, j int) bool { return set[top[i]] > set[top[j]] })
		for index, key := range top {
			if index >= 6 {
				break
			}
			t.Logf("PROBE %-12s %-44s %5d", label, key, set[key])
		}
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return buckets[keys[i]] > buckets[keys[j]] })
	for index, key := range keys {
		if index >= 12 {
			t.Logf("PROBE   … %d more buckets", len(keys)-index)
			break
		}
		t.Logf("PROBE   %-46s %5d", key, buckets[key])
	}
	for index, issue := range result.Issues {
		if index >= 4 {
			break
		}
		t.Logf("PROBE sample: %s -> %s", issue.Path, issue.Message)
	}
}
