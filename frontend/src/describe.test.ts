import { test } from "node:test";
import assert from "node:assert/strict";
import { factsLine, hasVerdict, recoveryLabel, relativeDays } from "./describe.ts";

const described = (over: Partial<Parameters<typeof factsLine>[0]> = {}) => ({
	kind: "directory", nodes: 1, newestModified: "", ageKnown: true,
	isProjectRoot: false, rule: "", recovery: "", protection: "", ...over,
});
const now = new Date("2026-09-04T12:00:00Z");

// The invariant the whole module exists for. Every unrecognised directory on the
// disk arrives here, and none of them may come out looking approved.

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


// The measured contradiction: a node_modules under ~/Documents inherited the
// user-content guard and was described as 可重新下载 and 不可重建 at once.

// The same guard on the object itself is a property of the object, and nothing
// supersedes it -- that direction of error loses files.

// "Some app's cache" is not a statement about this object, so it may not silence
// a guard about where the object sits.

// An inherited guard with nothing to supersede it is still worth saying -- as
// context, not as a refusal, which is what `hard: false` carries.

// The honest-silence rule, which survives the merge unchanged: nothing known
// must not read as approval.
test("nothing known is nothing known", () => {
	assert.equal(hasVerdict(described()), false);
	assert.equal(recoveryLabel(""), "");
});

test("a rule, a project marker or a protection each count as knowing something", () => {
	assert.equal(hasVerdict(described({ rule: "用户缓存" })), true);
	assert.equal(hasVerdict(described({ isProjectRoot: true })), true);
	assert.equal(hasVerdict(described({ protection: "系统目录" })), true);
});
