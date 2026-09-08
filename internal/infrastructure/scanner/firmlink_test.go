package scanner

import "testing"

func TestParseFirmlinkSourcesKeepsOnlyTheSystemSidePath(t *testing.T) {
	table := "/AppleInternal\tAppleInternal\n/Applications\tApplications\n\n/System/Library/Caches\tSystem/Library/Caches\n/Users\tUsers\nnot-a-path\tx\n/\t\n"
	sources := parseFirmlinkSources(table)
	for _, want := range []string{"/AppleInternal", "/Applications", "/System/Library/Caches", "/Users"} {
		if _, ok := sources[want]; !ok {
			t.Fatalf("%s missing from %v", want, sources)
		}
	}
	for _, reject := range []string{"Applications", "not-a-path", "/", ""} {
		if _, ok := sources[reject]; ok {
			t.Fatalf("%q must not be a firmlink source: %v", reject, sources)
		}
	}
	if len(sources) != 4 {
		t.Fatalf("expected 4 sources, got %v", sources)
	}
}

func TestParseFirmlinkSourcesOfEmptyTableIsEmpty(t *testing.T) {
	if got := parseFirmlinkSources(""); len(got) != 0 {
		t.Fatalf("expected no sources, got %v", got)
	}
}
