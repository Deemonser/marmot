import { test } from "node:test";
import assert from "node:assert/strict";
import { factsLine, guardLines, hasVerdict, recoveryLabel, relativeDays } from "./describe.ts";

const described = (over: Partial<Parameters<typeof factsLine>[0]> = {}) => ({
	kind: "directory", nodes: 1, newestModified: "", ageKnown: true, isProjectRoot: false,
	rule: "", recovery: "", protection: "", irreplaceable: "",
	partialInstall: "", loginState: "", ...over,
});
const now = new Date("2026-09-04T12:00:00Z");

// The invariant the whole module exists for. Every unrecognised directory on the
// disk arrives here, and none of them may come out looking approved.
test("an unrecognised object gets no verdict and no warning", () => {
	assert.equal(recoveryLabel(""), "");
	assert.deepEqual(guardLines(described()), []);
});

test("an unknown recovery value is not translated into a reassurance", () => {
	assert.equal(recoveryLabel("maybe"), "");
	assert.equal(recoveryLabel("probably-fine"), "");
});

test("the three recovery answers have words", () => {
	assert.equal(recoveryLabel("regenerable"), "可重新生成");
	assert.equal(recoveryLabel("redownloadable"), "可重新下载");
	assert.equal(recoveryLabel("irreplaceable"), "不可替代");
});

// Go's zero time marshals as year 1. Rendered naively it reads as two thousand
// years old, which is worse than saying nothing.
test("a zero or unusable timestamp reads as nothing", () => {
	assert.equal(relativeDays("0001-01-01T00:00:00Z", now), "");
	assert.equal(relativeDays("", now), "");
	assert.equal(relativeDays("not a date", now), "");
});

test("a future timestamp reads as nothing rather than negative days", () => {
	assert.equal(relativeDays("2027-01-01T00:00:00Z", now), "");
});

test("ages read at the scale they are", () => {
	assert.equal(relativeDays("2026-09-04T01:00:00Z", now), "今天");
	assert.equal(relativeDays("2026-09-03T01:00:00Z", now), "昨天");
	assert.equal(relativeDays("2026-08-28T12:00:00Z", now), "7 天前");
	assert.equal(relativeDays("2026-06-04T12:00:00Z", now), "3 个月前");
	assert.equal(relativeDays("2024-06-04T12:00:00Z", now), "2 年前");
});

test("the facts line states kind, count and age", () => {
	assert.equal(
		factsLine(described({ nodes: 214882, newestModified: "2026-09-01T12:00:00Z" }), now),
		"目录 · 214,882 项 · 3 天前修改",
	);
});

// A truncated walk knows neither the real count nor the age. Both are stated as
// what they are instead of being passed off as measurements.
test("a truncated walk reports a floor and withholds the age", () => {
	assert.equal(
		factsLine(described({ nodes: 200000, ageKnown: false, newestModified: "2026-09-01T12:00:00Z" }), now),
		"目录 · 超过 200,000 项",
	);
});

test("a file gets no item count", () => {
	assert.equal(factsLine(described({ kind: "file", nodes: 1, newestModified: "2026-09-03T12:00:00Z" }), now), "文件 · 昨天修改");
});

test("guards are ordered hardest first", () => {
	assert.deepEqual(
		guardLines(described({ protection: "系统目录", irreplaceable: "这是 Git 仓库", loginState: "会退出登录" })),
		["不可删除：系统目录", "不可重建：这是 Git 仓库", "会退出登录"],
	);
});

test("nothing known is nothing known", () => {
	assert.equal(hasVerdict(described()), false);
});

test("a project marker counts as knowing something, with no rule at all", () => {
	assert.equal(hasVerdict(described({ isProjectRoot: true })), true);
});

test("a guard alone counts, and so does a rule alone", () => {
	assert.equal(hasVerdict(described({ irreplaceable: "这是 Git 仓库" })), true);
	assert.equal(hasVerdict(described({ rule: "用户缓存" })), true);
});
