import { test } from "node:test";
import assert from "node:assert/strict";
import { meterColor, meterFraction, meterHSL } from "./meter.ts";

const hue = (fraction: number) => meterHSL(fraction)[0];

// The bug: one colour at every level, so a disk with three quarters free looked
// as urgent as one with nothing left.
test("the colour warms continuously as the volume fills", () => {
  // Flat and green up to half full: nothing to do, so nothing to signal.
  assert.equal(hue(0), 104);
  assert.equal(hue(0.5), 104);
  // Then strictly warmer with every step, with no cliff anywhere.
  let previous = hue(0.5);
  for (let f = 0.51; f <= 1.0; f += 0.01) {
    const h = hue(f);
    assert.ok(h < previous, `hue at ${f.toFixed(2)} (${h}) should be warmer than at the step before (${previous})`);
    assert.ok(previous - h < 6, `no jump at ${f.toFixed(2)}: ${previous} -> ${h}`);
    previous = h;
  }
  assert.equal(hue(1), 10);
});

test("the named stops are where they were", () => {
  assert.equal(hue(0.7), 40); // amber
  assert.equal(hue(0.85), 25); // orange
});

// DaisyDisk on this machine, measured: 205.7 GB used of 245.1 GB, and its bar
// is orange. Not amber, not red.
test("the reference machine reads as orange", () => {
  const h = hue(meterFraction(205.7e9, 245.1e9));
  assert.ok(h > 20 && h < 32, `expected orange, got hue ${h}`);
});

test("the colour is a CSS hsl() string", () => {
  assert.match(meterColor(84, 100), /^hsl\(\d+(\.\d+)? \d+(\.\d+)?% \d+(\.\d+)?%\)$/);
});

// An unreadable capacity must not invent an emergency.
test("an unknown total reads as empty, not as full", () => {
  assert.equal(meterFraction(500, 0), 0);
  assert.equal(meterColor(500, 0), meterColor(0, 100));
  assert.equal(meterFraction(500, -1), 0);
});

test("the fraction is clamped", () => {
  assert.equal(meterFraction(-5, 100), 0);
  assert.equal(meterFraction(150, 100), 1);
  assert.deepEqual(meterHSL(1.5), meterHSL(1));
  assert.deepEqual(meterHSL(-1), meterHSL(0));
});
