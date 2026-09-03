import { test } from "node:test";
import assert from "node:assert/strict";
import { autoStageable, inlineRiskReasons, riskReasonLabel, stageSummary } from "./advice.ts";

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

// Root-owned paths are reported with a command, never staged. Staging one puts an
// item in the dock that can only fail with a permission error after the user has
// already committed to a deletion.
test("a manual finding never stages itself, however safe", () => {
  assert.equal(autoStageable(item({ manual: true })), false);
  assert.equal(autoStageable(item({ manual: true, risk: "safe", recovery: "regenerable" })), false);
});

import { bulkCandidates, sourceLabel } from "./advice.ts";

const pending = (path: string, over: Partial<Parameters<typeof bulkCandidates>[0][0]> = {}) => ({
  source: "rule", risk: "review", recovery: "regenerable", path, ...over,
});

// The bulk button acts on the list the user can see. Anything they removed from
// the dock is marked, and the mark keeps it out of the next bulk add.
test("bulk add skips manual, collected and dismissed suggestions", () => {
  const items = [
    pending("/a"),
    pending("/b", { manual: true }),
    pending("/c"),
    pending("/d"),
  ];
  const collected = (item: { path: string }) => item.path === "/c";
  const out = bulkCandidates(items, collected, new Set(["/d"]));
  assert.deepEqual(out.map((item) => item.path), ["/a"]);
});

test("a dismissed suggestion comes back once it is collected again", () => {
  const items = [pending("/a")];
  // Collected by hand after being dismissed: no longer a candidate for the
  // bulk button, for the ordinary reason that it is already in the dock.
  assert.deepEqual(bulkCandidates(items, () => true, new Set(["/a"])), []);
  // Dismissal cleared (the caller does that on collect): back in the pool.
  assert.deepEqual(bulkCandidates(items, () => false, new Set()).length, 1);
});

test("source label names the rule, or the model with its confidence", () => {
  assert.equal(sourceLabel({ source: "rule", ruleName: "Xcode DerivedData", category: "cache", confidence: 0 }), "Xcode DerivedData");
  assert.equal(sourceLabel({ source: "rule", ruleName: "", category: "cache", confidence: 0 }), "cache");
  assert.equal(sourceLabel({ source: "advisor", ruleName: "", category: "cache", confidence: 0.874 }), "AI · 87%");
});

// A reason code the UI cannot translate is still a reason: shown as itself,
// never dropped.
test("every reason code reads as a sentence and unknown codes survive", () => {
  for (const code of ["irreplaceable", "login_state", "partial_install", "project_active", "project_dormant",
    "cache_cold", "generation_superseded", "redownload_cost", "advisor_uncertain", "generic_rule", "catalog"]) {
    assert.notEqual(riskReasonLabel(code), code);
  }
  assert.equal(riskReasonLabel("something_new"), "something_new");
});

// Inline tags carry a decision; the two that only restate the rule's caution
// stay in the detail view, and a missing list is an empty list.
test("inline reasons leave out the restatements", () => {
  assert.deepEqual(inlineRiskReasons(["project_active", "generic_rule", "catalog", "cache_cold"]), ["project_active", "cache_cold"]);
  assert.deepEqual(inlineRiskReasons(null), []);
  assert.deepEqual(inlineRiskReasons(undefined), []);
});
