import { test } from "node:test";
import assert from "node:assert/strict";
import { contentPushMs, contentPushPercent, leaveDelay, measuredStepPt, windowResizeMs } from "./pagefade.ts";

// The reference's step size stays recorded even though the ramp is not reproduced
// (R-066 §4.1): it is the target if the simultaneous version is attempted again.
test("the reference's measured step size is kept on record", () => {
  assert.ok(measuredStepPt > 49 && measuredStepPt < 50);
  assert.equal(Math.round(593 / measuredStepPt), 12, "593pt at 49.4pt per frame is the twelve frames measured");
});

// The push is the only motion now, so its duration is the transition's duration.
test("the push runs for the transition's whole duration", () => {
  assert.equal(contentPushMs, windowResizeMs);
});

// A partial push leaves a sliver of the old page parked at the edge.
test("the outgoing page leaves completely", () => {
  assert.equal(contentPushPercent, 100);
});

test("reduced motion removes the delay, not just the animation", () => {
  const original = globalThis.window;
  try {
    // @ts-expect-error -- minimal stand-in for the one API this reads.
    globalThis.window = { matchMedia: () => ({ matches: true }) };
    assert.equal(leaveDelay(), 0);
    // @ts-expect-error -- same, preference off.
    globalThis.window = { matchMedia: () => ({ matches: false }) };
    assert.equal(leaveDelay(), windowResizeMs);
    // @ts-expect-error -- deliberately incomplete: must not block forever.
    globalThis.window = {};
    assert.equal(leaveDelay(), windowResizeMs);
  } finally {
    globalThis.window = original;
  }
});
