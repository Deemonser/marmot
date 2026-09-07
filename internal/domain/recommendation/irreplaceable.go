package recommendation

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

// LoginState is a third guard kind, alongside irreplaceable and partial_install.
//
// Browser cookies and saved logins are not irreplaceable -- you can sign in
// again -- so calling them that would be a lie in the cautious direction. But
// signing in to every site again, and re-doing two-factor enrolment, is exactly
// the disruption a cleanup tool must not inflict silently. So the correction is
// on the risk and the wording, not on the recoverability.
const LoginState = "login_state"

// PartialInstall marks a path inside an installed toolchain: the installer
// checks the root and a stamp, not the files, so nothing self-heals.
const PartialInstall = "partial_install"

// LoginStateMessage and PartialInstallMessage are what the advisor path shows
// when it corrects a model's claim. The catalog carries the same sentences on
// the rules themselves, which is where the panel reads them from.
func LoginStateMessage() string {
	return "这里保存的是浏览器的登录状态。删除后所有网站都需要重新登录，" +
		"部分站点的两步验证需要重新设置。"
}

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

// IrreplaceableReason reports why losing this path would be permanent, or "" when
// it would not.
//
// It used to be a second matching system with four tables of its own, answering
// "can this come back" in parallel with the catalog and disagreeing with it. It
// is a query over the one catalog now: whichever rule is most specific about the
// path speaks for it, and the guard it carries is the answer.
func IrreplaceableReason(absolutePath string) string {
	guard := guardFor(absolutePath)
	if guard == LoginState || guard == PartialInstall {
		return ""
	}
	return guard
}

// GuardsFor is the guard the winning rule carries, as a list because Facts
// takes one and because a second guard kind could be added without the callers
// changing. Conditions that need more than a path -- age, project activity --
// cannot be evaluated here, so the rules that depend on them cannot win, which
// leaves the guards winning more often. That is the cautious direction.
func GuardsFor(absolutePath string) []string {
	if guard := guardFor(absolutePath); guard != "" {
		return []string{guard}
	}
	return nil
}

// LoginStateReason and PartialInstallReason are the other two kinds, asked for
// by name. Same query, filtered -- the callers that want one specific kind
// should not have to know that they all come from the same list now.
func LoginStateReason(absolutePath string) string {
	if guardFor(absolutePath) == LoginState {
		return LoginState
	}
	return ""
}

func PartialInstallReason(absolutePath string) string {
	if guardFor(absolutePath) == PartialInstall {
		return PartialInstall
	}
	return ""
}

func guardFor(absolutePath string) string {
	rule := Match(MatchContext{Path: absolutePath, Kind: "directory", ProjectIdleDays: NoProject})
	if rule == nil {
		return ""
	}
	return rule.Guard
}
