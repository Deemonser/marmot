import { test } from "node:test";
import assert from "node:assert/strict";
import { leaveDelay, pageEnterMs, pageEnterScale, pageLeaveMs, pageLeaveScale } from "./pagefade.ts";

// Arriving must not be slower than the wheel's own level change, or the page swap
// starts to feel like the slower of two motions the app already has.
test("the page change is quicker than the wheel's level change", () => {
  assert.ok(pageEnterMs < 520, "entering competes with morphMotion");
  assert.ok(pageLeaveMs < pageEnterMs, "leaving should be the shorter half");
});

// At a smaller scale the window reads as zooming, which is what a level change in
// the wheel means. Two motions must not claim the same meaning.
test("the scales stay subtle enough not to read as a zoom", () => {
  assert.ok(pageEnterScale >= 0.95 && pageEnterScale < 1);
  assert.ok(pageLeaveScale >= pageEnterScale && pageLeaveScale < 1);
});

// The leave animation holds a state change behind a timer. Someone who asked for
// no motion must get the state change now, not 180ms from now.
test("reduced motion removes the delay, not just the animation", () => {
  const original = globalThis.window;
  try {
    // @ts-expect-error -- minimal stand-in for the one API leaveDelay reads.
    globalThis.window = { matchMedia: () => ({ matches: true }) };
    assert.equal(leaveDelay(), 0);
    // @ts-expect-error -- same, with the preference off.
    globalThis.window = { matchMedia: () => ({ matches: false }) };
    assert.equal(leaveDelay(), pageLeaveMs);
    // No matchMedia at all must not throw or block the state change forever.
    // @ts-expect-error -- deliberately incomplete.
    globalThis.window = {};
    assert.equal(leaveDelay(), pageLeaveMs);
  } finally {
    globalThis.window = original;
  }
});
