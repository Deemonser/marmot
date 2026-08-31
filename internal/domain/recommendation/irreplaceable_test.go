package recommendation

import "testing"

// The `**/` patterns mean "at any depth, anywhere". They were reachable only
// after the path had been resolved relative to a home folder, so everything
// outside one was unguarded: a repository on an external volume, a checkout under
// /opt, an .ssh in a shared folder. The scan root is the whole disk, and with
// permanent deletion the difference stopped being academic.
func TestAnywherePatternsGuardOutsideHomeToo(t *testing.T) {
	for _, path := range []string{
		"/Volumes/Work/app/.git",
		"/opt/service/.git/objects/pack/pack-abc.pack",
		"/Users/Shared/team/project/.git",
		"/Volumes/Backup/home/.ssh",
		"/srv/deploy/.gnupg",
	} {
		if IrreplaceableReason(path) == "" {
			t.Errorf("%s is unguarded", path)
		}
	}
	// Still true inside a home folder, and the transient carve-out still applies
	// wherever it is.
	if IrreplaceableReason("/Users/alice/code/app/.git") == "" {
		t.Error("a repository under a home folder lost its guard")
	}
	if reason := IrreplaceableReason("/Volumes/Work/app/.git/objects/pack/tmp_pack_Z8vjYY"); reason != "" {
		t.Errorf("an abandoned repack temp file was called %q outside home too", reason)
	}
}
