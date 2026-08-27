package cleanup

import "testing"

// The guard is the only answer to "may this be deleted", and both the space map
// and plan creation read it. The exact code matters as much as the refusal: it
// picks the sentence the user is shown, and a home folder shown as "something
// macOS depends on" is a lie about their own data.
func TestDeleteBlockReasons(t *testing.T) {
	cases := map[string]string{
		"/":                                ProtectionSystemDependency,
		"/System":                          ProtectionSystemDependency,
		"/System/Library/Fonts":            ProtectionSystemDependency,
		"/Users":                           ProtectionSystemDependency,
		"/Applications":                    ProtectionSystemDependency,
		"/Library":                         ProtectionSystemDependency,
		"/usr":                             ProtectionSystemDependency,
		"/bin":                             ProtectionSystemDependency,
		"/sbin":                            ProtectionSystemDependency,
		"/private":                         ProtectionSystemDependency,
		"/private/var/db/dslocal":          ProtectionSystemDependency,
		"/private/var/folders/x/y":         ProtectionSystemDependency,
		"/etc":                             ProtectionSystemDependency,
		"/var":                             ProtectionSystemDependency,
		"/tmp":                             ProtectionSystemDependency,
		"/dev":                             ProtectionSystemDependency,
		"/cores":                           ProtectionSystemDependency,
		"/home":                            ProtectionSystemDependency,
		"/opt":                             ProtectionSystemDependency,
		"/Volumes":                         ProtectionSystemDependency,
		"/Users/alice":                     ProtectionHomeFolder,
		"/Users/alice/":                    ProtectionHomeFolder,
		"/System/Volumes/Data/Users/alice": ProtectionHomeFolder,
		"/Volumes/Backup":                  ProtectionVolumeRoot,
		"/usr/../usr":                      ProtectionSystemDependency,
		// Deletable: everything a real cleanup is actually after.
		"/Users/alice/Downloads":         "",
		"/Users/alice/Downloads/big.dmg": "",
		"/Users/alice/Library/Caches":    "",
		"/Applications/Xcode.app":        "",
		"/Library/Caches":                "",
		"/usr/local":                     "",
		"/usr/local/Cellar":              "",
		"/opt/homebrew":                  "",
		"/private/var/log":               "",
		"/Volumes/Backup/archive":        "",
	}
	for path, want := range cases {
		if got := DeleteBlock(path); got != want {
			t.Errorf("%s: want %q, got %q", path, want, got)
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
