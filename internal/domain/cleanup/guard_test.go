package cleanup

import "testing"

// The guard is the only answer to "may this be deleted", and both the space map
// and plan creation read it. These cases are the ones a wrong answer would hurt.
func TestDeleteBlockProtectsTheSystemButNotWhatLivesInIt(t *testing.T) {
	blocked := []string{
		"/", "/System", "/System/Library/Fonts", "/Users", "/Users/alice",
		"/Applications", "/Library", "/usr", "/bin", "/sbin", "/private",
		"/private/var/db/dslocal", "/private/var/folders/x/y",
		"/Volumes", "/Volumes/Backup", "/etc", "/var", "/tmp", "/dev", "/cores",
		"/home", "/opt",
		// Trailing separators and unclean paths must not slip past.
		"/System/", "/Users/alice/", "/usr/../usr",
	}
	for _, path := range blocked {
		if DeleteBlock(path) != ProtectionSystemDependency {
			t.Errorf("%s should be protected", path)
		}
	}

	allowed := []string{
		"/Users/alice/Downloads", "/Users/alice/Downloads/big.dmg",
		"/Applications/Xcode.app", "/Library/Caches", "/Library/Caches/x",
		"/usr/local", "/usr/local/Cellar", "/opt/homebrew",
		"/private/var/log", "/Volumes/Backup/archive",
	}
	for _, path := range allowed {
		if reason := DeleteBlock(path); reason != "" {
			t.Errorf("%s should be deletable, got %q", path, reason)
		}
	}
}

// /usr is protected and /usr/local is carved back out of it, so the order the
// two rules are applied in decides the answer.
func TestDeleteBlockCarveOutBeatsTheProtectedTree(t *testing.T) {
	if DeleteBlock("/usr") == "" {
		t.Fatal("/usr itself must stay protected")
	}
	if reason := DeleteBlock("/usr/local/bin/tool"); reason != "" {
		t.Fatalf("/usr/local must be exempt, got %q", reason)
	}
}
