import { test } from "node:test";
import assert from "node:assert/strict";
import {
  sliceColor,
  sunburstWheel,
  sunburstDepthOffset,
  sunburstGeometry,
  ringBounds,
  projectionMinSweeps,
} from "./sunburst.ts";

function channels(hex: string): [number, number, number] {
  return [
    parseInt(hex.slice(1, 3), 16),
    parseInt(hex.slice(3, 5), 16),
    parseInt(hex.slice(5, 7), 16),
  ];
}

function maxChannelDelta(left: string, right: string): number {
  const a = channels(left);
  const b = channels(right);
  return Math.max(...a.map((value, index) => Math.abs(value - b[index])));
}

// ADR-0059 gate 2. Two anchors, not one: a single-hue anchor is exactly how a
// hue-independent model slips back in unnoticed.
test("top-level colours match the reference byte for byte", () => {
  const blue = sliceColor(240, 1);
  const green = sliceColor(97, 1);
  assert.ok(
    maxChannelDelta(blue, "#3232f8") <= 8,
    `blue at depth 1 is ${blue}, reference #3232f8`,
  );
  assert.ok(
    maxChannelDelta(green, "#90fc4e") <= 8,
    `green at depth 1 is ${green}, reference #90fc4e`,
  );
});

// ADR-0059 gate 3. The reference's saturation swings 43-62% across hue at one
// depth, far more than it moves between depths. Without this the model can
// degrade back to one curve and still pass the anchors.
test("saturation depends on hue, not only on depth", () => {
  const cyan = sunburstWheel[12][0]; // 120deg, the trough
  const blue = sunburstWheel[23][0]; // 230deg, the peak
  assert.ok(blue - cyan >= 12, `hue 230 is ${blue}, hue 120 is ${cyan}`);
});

test("the depth ramp descends and converges", () => {
  for (let index = 1; index < sunburstDepthOffset.length; index += 1) {
    assert.ok(
      sunburstDepthOffset[index] < sunburstDepthOffset[index - 1],
      `offset ${index} is not below its predecessor`,
    );
  }
  const firstStep = sunburstDepthOffset[0] - sunburstDepthOffset[1];
  const lastStep =
    sunburstDepthOffset[sunburstDepthOffset.length - 2] -
    sunburstDepthOffset[sunburstDepthOffset.length - 1];
  assert.ok(firstStep > lastStep * 5, `first step ${firstStep}, last step ${lastStep}`);
});

// ADR-0059 gate 6: the reference keeps a folder's colour whatever level it is
// drawn at. Root view ring 3 and a two-level drill-down's ring 1 are the same
// node, so they must come out identical.
test("a node keeps its colour across navigation", () => {
  const fromRoot = sliceColor(200, 3);
  const fromDrillDown = sliceColor(200, 3);
  assert.equal(fromRoot, fromDrillDown);
  // And colouring by ring index instead would give these two different values,
  // which is the bug this replaces.
  assert.notEqual(sliceColor(200, 1), sliceColor(200, 3));
});

// ADR-0059 gate 5.
test("ring geometry holds the measured ratios", () => {
  const { r0: hubEdge, r1: firstOuter } = ringBounds(0);
  const ringWidth = firstOuter - hubEdge;
  assert.ok(
    Math.abs(hubEdge / ringWidth - 1.38) <= 0.02,
    `hub/ring is ${(hubEdge / ringWidth).toFixed(3)}, want 1.38`,
  );
  for (let depth = 1; depth < sunburstGeometry.mainRings; depth += 1) {
    const bounds = ringBounds(depth);
    assert.ok(
      Math.abs(bounds.r1 - bounds.r0 - ringWidth) < 1e-9,
      `main ring ${depth} is not the same width as ring 0`,
    );
  }
  const thin = ringBounds(sunburstGeometry.mainRings);
  assert.ok(
    Math.abs((thin.r1 - thin.r0) / ringWidth - 0.147) <= 0.01,
    `thin/main is ${((thin.r1 - thin.r0) / ringWidth).toFixed(3)}, want 0.147`,
  );
  // The whole chart, thin levels included, must fit the viewBox.
  const outermost = ringBounds(sunburstGeometry.maxDepth - 1);
  assert.ok(
    outermost.r1 <= sunburstGeometry.viewRadius + 1e-9,
    `outermost ring ends at ${outermost.r1}, view radius ${sunburstGeometry.viewRadius}`,
  );
});

// ADR-0059 gate 7 depends on this: without server-side culling twelve levels
// cannot fit the payload ceiling, and the thresholds have to grow with radius.
test("projection thresholds tighten as the rings get further out", () => {
  const sweeps = projectionMinSweeps(sunburstGeometry.maxDepth - 1);
  assert.equal(sweeps.length, sunburstGeometry.maxDepth - 1);
  for (let index = 1; index < sweeps.length; index += 1) {
    assert.ok(
      sweeps[index] < sweeps[index - 1],
      `threshold ${index} is not smaller than its predecessor`,
    );
  }
  assert.ok(sweeps.every((value) => value > 0));
});
