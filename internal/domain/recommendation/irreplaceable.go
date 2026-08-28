package recommendation

import (
	"path/filepath"
	"strings"
)

// Recoverability, not risk, is the axis that decides whether a cleanup
// suggestion is frightening. Reinstalling Flutter or Rust to reclaim a few GB is
// an inconvenience measured in minutes of download; losing a photo library is
// permanent. The user of this feature said it plainly, and they were right:
// almost anything goes as long as it comes back.
//
// So this file is the counterpart to cleanup.DeleteBlock. That one answers "may
// this be deleted"; this one answers "if it is deleted, is it gone for good".
// A model may claim an object is regenerable or redownloadable, and where that
// claim is wrong the mistake is not a matter of taste -- it is the one error
// class that cannot be undone by waiting.
//
// Deliberately conservative and deliberately short. The cost of a missing entry
// is a wrong `regenerable` reaching the user; the cost of an extra one is an
// object described as irreplaceable when a reinstall would have fixed it, which
// only ever makes the tool more cautious.

const (
	// A person's own files. Nothing regenerates these.
	IrreplaceableUserContent = "user_content"
	// A library or store an application keeps a person's own data in.
	IrreplaceableUserData = "user_data"
	// Version-control history. It may exist on a remote, but nothing here can
	// know that, and "probably pushed" is not a recovery plan.
	IrreplaceableRepository = "repository"
	// A device backup: the only copy of a phone's state.
	IrreplaceableBackup = "device_backup"
	// A virtual machine or container volume: the guest's whole filesystem.
	IrreplaceableVirtualDisk = "virtual_disk"
	// Credentials and keys.
	IrreplaceableCredentials = "credentials"
)

// homeRelativeIrreplaceable are matched against the path below a home folder.
// `*` matches one segment; a leading `**/` matches the segment at any depth.
var homeRelativeIrreplaceable = []struct{ pattern, reason string }{
	{"Documents", IrreplaceableUserContent},
	{"Desktop", IrreplaceableUserContent},
	{"Pictures", IrreplaceableUserContent},
	{"Movies", IrreplaceableUserContent},
	{"Music", IrreplaceableUserContent},
	{"Public", IrreplaceableUserContent},
	// Sandboxed applications keep the person's own documents here, next to their
	// caches. The two are a segment apart and could not be more different.
	{"Library/Containers/*/Data/Documents", IrreplaceableUserContent},
	{"Library/Group Containers/*/Documents", IrreplaceableUserContent},

	{"Library/Application Support/MobileSync/Backup", IrreplaceableBackup},
	{"Library/Keychains", IrreplaceableCredentials},
	{"Library/Mail", IrreplaceableUserData},
	{"Library/Messages", IrreplaceableUserData},
	{"Library/Calendars", IrreplaceableUserData},
	{"Library/Reminders", IrreplaceableUserData},
	{"Library/Photos", IrreplaceableUserContent},
	{"Library/Containers/com.docker.docker/Data/vms", IrreplaceableVirtualDisk},

	{"**/.git", IrreplaceableRepository},
	{"**/.ssh", IrreplaceableCredentials},
	{"**/.gnupg", IrreplaceableCredentials},
}

// suffixIrreplaceable are matched against the whole path, anywhere on disk.
var suffixIrreplaceable = []struct{ suffix, reason string }{
	{".photoslibrary", IrreplaceableUserContent},
	{".photolibrary", IrreplaceableUserContent},
	{".fcpbundle", IrreplaceableUserContent},
	{".logicx", IrreplaceableUserContent},
	{".sparsebundle", IrreplaceableVirtualDisk},
	{".vmdk", IrreplaceableVirtualDisk},
	{".qcow2", IrreplaceableVirtualDisk},
	{".utm", IrreplaceableVirtualDisk},
	{".pvm", IrreplaceableVirtualDisk},
	{".vdi", IrreplaceableVirtualDisk},
}

// gitTransient are artifacts git creates and abandons. They live under .git but
// are not history: a tmp_pack_* is a partial pack left by an interrupted repack,
// and git does not reliably clean them up -- one was measured sitting at 438 MB.
// Calling it "repository history" was a false positive of the .git rule, and a
// guard that is wrong in the cautious direction is still wrong.
//
// Carved narrowly and by name. Broad exceptions are how safety guards acquire
// holes; these are two literal prefixes git itself documents as temporary.
var gitTransient = []string{"tmp_pack_", "tmp_idx_"}

func isGitTransient(clean string) bool {
	if !strings.Contains(clean, "/.git/") {
		return false
	}
	base := filepath.Base(clean)
	for _, prefix := range gitTransient {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

// IrreplaceableReason reports why losing this path would be permanent, or "" when
// it would not. It is consulted after an advisor answers, so a wrong claim of
// recoverability is corrected rather than believed.
func IrreplaceableReason(absolutePath string) string {
	clean := filepath.Clean(absolutePath)
	if isGitTransient(clean) {
		return ""
	}
	lowered := strings.ToLower(clean)
	for _, entry := range suffixIrreplaceable {
		if strings.HasSuffix(lowered, entry.suffix) || strings.Contains(lowered, entry.suffix+"/") {
			return entry.reason
		}
	}
	relative, ok := homeRelative(clean)
	if !ok {
		return ""
	}
	for _, entry := range homeRelativeIrreplaceable {
		if matchPattern(entry.pattern, relative) {
			return entry.reason
		}
	}
	return ""
}

// PartialInstallReason reports that a path sits *inside* an installed toolchain
// whose installer will not notice it is missing, so nothing self-heals and the
// real recovery is reinstalling the whole thing.
//
// Verified against flutter_tools/lib/src/cache.dart on disk:
//
//	Future<bool> isUpToDate(FileSystem fileSystem) async {
//	  if (!location.existsSync()) return false;
//	  if (version != cache.getStampFor(stampName)) return false;
//	  return isUpToDateInner(fileSystem);   // also only directory existence
//	}
//
// Only the artifact's root directory and its stamp are checked; file contents
// never are. Deleting the whole root therefore does re-download, and deleting a
// subdirectory inside one leaves a broken toolchain that no command repairs.
// Measured: an advisor called `dart-sdk/bin/snapshots` redownloadable via
// `flutter doctor`, which restores nothing -- the only route back is deleting
// bin/cache and pulling a gigabyte again.
//
// Deliberately narrow. Whether a given installer tracks components individually
// (rustup does, per component) or only presence (Flutter) is tool-specific
// knowledge, and a guard that guessed would be worse than the prompt rule that
// covers the general case.
func PartialInstallReason(absolutePath string) string {
	clean := filepath.Clean(absolutePath)
	const marker = "/flutter/bin/cache/dart-sdk/"
	if strings.Contains(clean, marker) {
		return PartialInstall
	}
	return ""
}

// PartialInstall is the reason code for the above.
const PartialInstall = "partial_install"

// PartialInstallMessage explains the real cost.
func PartialInstallMessage() string {
	return "这是已安装工具链的内部目录。安装器只检查根目录和 stamp，不校验里面的文件，" +
		"所以删掉之后不会自动重新下载——工具链会一直是坏的。真实恢复方式是删除整个缓存目录重新拉取。"
}

// IrreplaceableMessage is the sentence shown to a person. The codes are for
// logic; this is for reading.
func IrreplaceableMessage(reason string) string {
	switch reason {
	case IrreplaceableUserContent:
		return "这是你自己的文件，删除后无法再生成。"
	case IrreplaceableUserData:
		return "这是应用为你保存的数据，不是缓存，删除后无法重建。"
	case IrreplaceableRepository:
		return "这是版本库历史。即使远端可能有副本，本工具无法确认，删除后可能永久丢失未推送的提交。"
	case IrreplaceableBackup:
		return "这是设备备份，可能是该设备状态的唯一副本。"
	case IrreplaceableVirtualDisk:
		return "这是虚拟机或容器的磁盘，里面是整个客体文件系统。"
	case IrreplaceableCredentials:
		return "这是密钥或凭据，删除后无法恢复。"
	default:
		return ""
	}
}
