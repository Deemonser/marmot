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

// An application bundle is one object. Its interior is not separately
// addressable: deleting part of it yields a broken application and nothing puts
// the part back, so the catalog must never get the chance to label it.
//
// Measured before this guard existed: `**/node_modules` matched
// ~/Downloads/Realm Studio.app/Contents/Resources/app.asar.unpacked/node_modules
// and offered it as 可重新下载, and a whole-disk scan found fifty more inside
// /Applications. Nothing refused any of them.
func TestBundleInteriorIsRefusedButTheBundleItselfIsNot(t *testing.T) {
	refused := []string{
		"/Users/alice/Downloads/Realm Studio.app/Contents/Resources/app.asar.unpacked/node_modules",
		"/Applications/Visual Studio Code.app/Contents/Resources/app/extensions/copilot/node_modules",
		"/Applications/Foo.app/Contents",
		"/Applications/Foo.app/Contents/Frameworks/Bar.framework/Versions/A/Resources",
		"/Users/alice/Library/QuickLook/Thing.qlgenerator/Contents/MacOS",
	}
	for _, path := range refused {
		if reason := DeleteBlock(path); reason != ProtectionBundleInterior {
			// A more specific refusal winning is fine; nothing at all is not.
			if reason == "" {
				t.Errorf("%s was not refused", path)
			}
		}
	}
	// Deleting a whole application is ordinary cleanup and stays allowed. So does
	// an ordinary directory that merely has a dot in its name.
	for _, path := range []string{
		"/Applications/Foo.app",
		"/Users/alice/Downloads/Realm Studio.app",
		"/Users/alice/work/project.old/node_modules",
		"/Users/alice/work/repo/node_modules",
	} {
		if reason := DeleteBlock(path); reason != "" {
			t.Errorf("%s was refused as %q", path, reason)
		}
	}
}
