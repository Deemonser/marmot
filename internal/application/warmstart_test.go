package application

import (
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

// ADR-0075 §5: the decision to replay is made from the ID span alone, before
// anything is read. R-078's machine: ~1 directory event per 130 IDs, 0.3 ms per
// event to replay, 1.6 ms per changed directory to splice.
func TestPredictWarmStartUsesCalibration(t *testing.T) {
	// One hour on the reference machine: 190k IDs -> ~1,460 events -> ~1 s.
	hour := predictWarmStart(190_000, scan.SeedCalibration{})
	if hour.Seconds < 0.5 || hour.Seconds > 2.5 {
		t.Fatalf("an hour's span should predict a couple of seconds, got %.2fs (%+v)", hour.Seconds, hour)
	}
	// A week: 74M IDs -> hundreds of seconds, far over any budget.
	week := predictWarmStart(74_000_000, scan.SeedCalibration{})
	if week.Seconds < 100 {
		t.Fatalf("a week's span should predict minutes, got %.1fs", week.Seconds)
	}
	// A machine that measured itself slower predicts slower; zero fields fall
	// back to the defaults rather than to zero cost.
	slow := predictWarmStart(190_000, scan.SeedCalibration{ReplayMsPerEvent: 3})
	if slow.Seconds <= hour.Seconds {
		t.Fatalf("a slower replay rate must raise the prediction: %.2f vs %.2f", slow.Seconds, hour.Seconds)
	}
	if free := predictWarmStart(0, scan.SeedCalibration{}); free.Seconds != seedLoadSeconds {
		t.Fatalf("no span should cost only the load: %.2f", free.Seconds)
	}
}

func TestWarmStartBudgetIsAThirdOfTheFullScan(t *testing.T) {
	if got := warmStartBudget(scan.SeedHeader{FullScanSeconds: 30}); got != 10 {
		t.Fatalf("budget: got %.1f, want 10", got)
	}
	if got := warmStartBudget(scan.SeedHeader{}); got != defaultFullScanSeconds/3 {
		t.Fatalf("default budget: got %.1f", got)
	}
	// R-078: 24 hours of the reference machine (4.9M IDs) must NOT fit an 18 s scan's budget.
	if day := predictWarmStart(4_935_431, scan.SeedCalibration{}); day.Seconds <= warmStartBudget(scan.SeedHeader{FullScanSeconds: 18}) {
		t.Fatalf("a day's span should be over budget, predicted %.1fs", day.Seconds)
	}
	// Six hours (0.8M IDs) fits.
	if six := predictWarmStart(808_079, scan.SeedCalibration{}); six.Seconds > warmStartBudget(scan.SeedHeader{FullScanSeconds: 18}) {
		t.Fatalf("six hours should fit the budget, predicted %.1fs", six.Seconds)
	}
}

func TestSeedMatchesRequiresRootDeviceAndJournal(t *testing.T) {
	header := scan.SeedHeader{Root: "/", Device: 7, JournalUUID: "A", EventID: 9}
	if !seedMatches(header, "/", 7, "A") {
		t.Fatal("identical identity must match")
	}
	for name, tc := range map[string]struct {
		root   string
		device uint64
		uuid   string
	}{
		"other root":    {"/Volumes/X", 7, "A"},
		"other device":  {"/", 8, "A"},
		"other journal": {"/", 7, "B"},
	} {
		if seedMatches(header, tc.root, tc.device, tc.uuid) {
			t.Errorf("%s must not match", name)
		}
	}
	if seedMatches(scan.SeedHeader{Root: "/", Device: 7, JournalUUID: "A"}, "/", 7, "A") {
		t.Fatal("a seed without an event position is useless and must not match")
	}
}

func TestReplayUnreliableReasons(t *testing.T) {
	cases := map[string]ports.FileEventHistoryReport{
		"回放超时":     {HistoryDone: false},
		"事件 ID 回绕": {HistoryDone: true, IDsWrapped: 1},
		"系统报告丢失事件": {HistoryDone: true, UserDropped: 1},
		"扫描根已变更":   {HistoryDone: true, RootChanged: 1},
	}
	for want, report := range cases {
		if report.Reliable() {
			t.Errorf("%s: report must be unreliable", want)
		}
		if got := replayUnreliableReason(report); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if !(ports.FileEventHistoryReport{HistoryDone: true}).Reliable() {
		t.Fatal("a complete, lossless replay is reliable")
	}
}

func TestMeasureCalibrationFoldsInTheReplay(t *testing.T) {
	report := ports.FileEventHistoryReport{Events: 1000, Directories: map[string]uint64{}}
	for i := range 250 {
		report.Directories[string(rune('a'+i%26))+string(rune('A'+i/26))] = 1
	}
	next := measureCalibration(scan.SeedCalibration{}, 130_000, report, 300e6, 250, 400e6)
	if next.EventsPerID < 0.0076 || next.EventsPerID > 0.0078 {
		t.Errorf("events per id: %f", next.EventsPerID)
	}
	if next.DirsPerEvent != 0.25 {
		t.Errorf("dirs per event: %f", next.DirsPerEvent)
	}
	if next.ReplayMsPerEvent != 0.3 {
		t.Errorf("replay ms per event: %f", next.ReplayMsPerEvent)
	}
	if next.SpliceMsPerDir != 1.6 {
		t.Errorf("splice ms per dir: %f", next.SpliceMsPerDir)
	}
	// Nothing measured: nothing changes.
	kept := measureCalibration(next, 0, ports.FileEventHistoryReport{}, 0, 0, 0)
	if kept != next {
		t.Fatalf("an empty replay must not disturb the calibration: %+v", kept)
	}
}
