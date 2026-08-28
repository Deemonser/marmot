import { test } from "node:test";
import assert from "node:assert/strict";
import { autoStageable, stageSummary } from "./advice.ts";

const item = (over: Partial<Parameters<typeof autoStageable>[0]> = {}) => ({
  source: "rule", risk: "safe", recovery: "regenerable", ...over,
});

test("a deterministic safe rule finding stages itself", () => {
  assert.equal(autoStageable(item()), true);
  assert.equal(autoStageable(item({ recovery: "redownloadable" })), true);
});

// The disruption axis reaches the user through `review`. Pre-filling the cart
// with it would let one keypress send an active project's build cache to the
// trash -- the exact failure the axis was built to prevent.
test("review and risky never stage themselves", () => {
  assert.equal(autoStageable(item({ risk: "review" })), false);
  assert.equal(autoStageable(item({ risk: "risky" })), false);
});

// ADR-0061 §1: the advisor produces suggestions, not authorisation. A model that
// mislabelled recoverability 5% of the time does not get to fill the cart.
test("an advisor suggestion never stages itself, however safe it claims to be", () => {
  assert.equal(autoStageable(item({ source: "advisor" })), false);
  assert.equal(autoStageable(item({ source: "advisor", risk: "safe" })), false);
});

// A rule pairing safe with irreplaceable would be a bug in the catalog. If one
// ever ships, it must not be the thing that quietly fills the cart.
test("safe plus irreplaceable is refused rather than trusted", () => {
  assert.equal(autoStageable(item({ recovery: "irreplaceable" })), false);
});

test("the summary says what was withheld, not only what was taken", () => {
  assert.match(stageSummary(12, "33.9 GB", 46), /12 项.*33\.9 GB.*46 项需要你确认/);
  assert.equal(stageSummary(0, "", 0), "");
  assert.match(stageSummary(0, "", 9), /都需要你确认/);
  assert.doesNotMatch(stageSummary(12, "33.9 GB", 0), /需要你确认/);
});
