//go:build darwin

package scanner

import "testing"

func TestJoinNativePath(t *testing.T) {
	tests := []struct {
		parent string
		name   string
		want   string
	}{
		{parent: "/", name: "Applications", want: "/Applications"},
		{parent: "/Users/deemo", name: "Library", want: "/Users/deemo/Library"},
		{parent: "/Users/deemo/", name: "Library", want: "/Users/deemo/Library"},
		{parent: "/Users/deemo", name: ".", want: "/Users/deemo"},
		{parent: "/Users/deemo", name: "..", want: "/Users"},
	}
	for _, test := range tests {
		if got := joinNativePath(test.parent, test.name); got != test.want {
			t.Fatalf("joinNativePath(%q, %q) = %q, want %q", test.parent, test.name, got, test.want)
		}
	}
}

func TestJoinNativePathAndName(t *testing.T) {
	path, name := joinNativePathAndName("/Users/deemo", []byte("Library"))
	if path != "/Users/deemo/Library" || name != "Library" {
		t.Fatalf("joinNativePathAndName returned path=%q name=%q", path, name)
	}
	path, name = joinNativePathAndName("/", []byte("Applications"))
	if path != "/Applications" || name != "Applications" {
		t.Fatalf("root join returned path=%q name=%q", path, name)
	}
}
