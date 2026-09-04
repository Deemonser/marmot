// What the panel says about the object under the pointer, from the local rule
// catalog alone.
//
// The one rule this module exists to enforce: silence is not a reassurance. An
// empty `rule` means the catalog recognised nothing, and rendering that as "可以
// 删除" would invent a verdict for every unrecognised directory on the disk --
// next to a button that deletes without a trash step. So every function here
// returns nothing when it knows nothing, and the caller has nothing to print.

// The fields needed from a NodeDescription, declared rather than imported from
// the bindings so this module and its test run under Node with no runtime to
// load. Same reason advice.ts declares its own input type.
export type Described = {
	kind: string;
	nodes: number;
	newestModified: string;
	ageKnown: boolean;
	isProjectRoot: boolean;
	rule: string;
	recovery: string;
	protection: string;
	irreplaceable: string;
	partialInstall: string;
	loginState: string;
};

const kindLabels: Record<string, string> = {
	directory: "目录",
	file: "文件",
	symlink: "符号链接",
};

// The three answers to "what does it cost me to get this back". Recovery is
// deliberately separate from risk: a 40 GB build directory is regenerable and
// deleting it still costs an hour of rebuilding.
const recoveryLabels: Record<string, string> = {
	regenerable: "可重新生成",
	redownloadable: "可重新下载",
	irreplaceable: "不可替代",
};

export function recoveryLabel(recovery: string): string {
	return recoveryLabels[recovery] ?? "";
}

// relativeDays reads a Go RFC3339 timestamp. Empty for anything it cannot use --
// including Go's zero time, which marshals as year 1 and would otherwise render
// as two thousand years old.
export function relativeDays(newestModified: string, now: Date): string {
	const at = new Date(newestModified);
	const stamp = at.getTime();
	if (!newestModified || Number.isNaN(stamp) || at.getUTCFullYear() < 1970) return "";
	const days = Math.floor((now.getTime() - stamp) / 86_400_000);
	if (days < 0) return "";
	if (days === 0) return "今天";
	if (days === 1) return "昨天";
	if (days < 30) return days + " 天前";
	if (days < 365) return Math.floor(days / 30) + " 个月前";
	return Math.floor(days / 365) + " 年前";
}

// factsLine is the baseline, and it is the reason the region has something to
// say about every object: kind, how much is in it, when it last changed. A
// matched rule adds to this, it does not replace it.
//
// ageKnown false means the subtree was too large to walk, so the count is a
// floor and the age is not known at all. Both are then stated as what they are.
export function factsLine(described: Described, now: Date): string {
	const parts: string[] = [];
	const kind = kindLabels[described.kind] ?? "";
	if (kind) parts.push(kind);
	if (described.kind === "directory" && described.nodes > 1) {
		const count = described.nodes.toLocaleString();
		parts.push(described.ageKnown ? count + " 项" : "超过 " + count + " 项");
	}
	if (described.ageKnown) {
		const age = relativeDays(described.newestModified, now);
		if (age) parts.push(age + "修改");
	}
	return parts.join(" · ");
}

// guardLines are the refusals and the warnings, hardest first. A protection is
// the machine's guard and cannot be overridden by anyone; the rest are advisory
// and exist to be read before a decision, not to block it.
export function guardLines(described: Described): string[] {
	const lines: string[] = [];
	if (described.protection) lines.push("不可删除：" + described.protection);
	if (described.irreplaceable) lines.push("不可重建：" + described.irreplaceable);
	if (described.loginState) lines.push(described.loginState);
	if (described.partialInstall) lines.push(described.partialInstall);
	return lines;
}

// hasVerdict is whether anything at all is known about this object beyond its
// plain facts. It exists so the "nothing is known" line has exactly one
// definition: the alternative is a condition spelled out at the call site, which
// is where an added signal gets forgotten and the panel goes back to claiming
// ignorance about something it just recognised.
export function hasVerdict(described: Described): boolean {
	return Boolean(described.rule) || described.isProjectRoot || guardLines(described).length > 0;
}
