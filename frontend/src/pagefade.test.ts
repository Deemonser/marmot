import { test } from "node:test";
import assert from "node:assert/strict";
import { contentFadeMs, leaveDelay, measuredStepPt, resizeSteps, windowResizeMs } from "./pagefade.ts";

// R-066 measured the reference: 152 -> 745 in 193ms in twelve uniform steps of
// about 49.4pt. Ours must move at the same speed.
test("the ramp matches the reference's measured step size", () => {
  const steps = resizeSteps(152, 745);
  assert.equal(steps.length, 12, `expected 12 frames, got ${steps.length}`);
  assert.equal(steps[steps.length - 1], 745, "the last frame must land exactly on target");
  const deltas = steps.map((height, index) => height - (index === 0 ? 152 : steps[index - 1]));
  for (const delta of deltas) {
    assert.ok(Math.abs(delta - measuredStepPt) < 2, `step of ${delta} is not the measured ${measuredStepPt}`);
  }
});

// Linear, not eased: the reference's steps were uniform, so the first and last
// must be the same size. An ease would make them differ by several times.
test("the ramp is linear", () => {
  const steps = resizeSteps(200, 715);
  const first = steps[0] - 200;
  const last = steps[steps.length - 1] - steps[steps.length - 2];
  assert.ok(Math.abs(first - last) < 2, `first step ${first} against last ${last} is not linear`);
});

test("the ramp runs the same both ways", () => {
  assert.equal(resizeSteps(745, 152).length, resizeSteps(152, 745).length);
  assert.equal(resizeSteps(745, 152).at(-1), 152);
});

// Deriving the count from the step size keeps the speed constant. A fixed frame
// count would make a taller window resize faster.
test("a shorter distance takes fewer frames, not smaller steps", () => {
  assert.ok(resizeSteps(200, 400).length < resizeSteps(200, 715).length);
  assert.deepEqual(resizeSteps(300, 300), []);
  assert.deepEqual(resizeSteps(300, 300.4), []);
});

// The content fade must finish with the resize, not after it, or the window
// settles and then the content is still catching up.
test("the content fade is shorter than the resize", () => {
  assert.ok(contentFadeMs < windowResizeMs);
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
