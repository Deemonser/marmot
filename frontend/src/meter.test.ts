import { test } from "node:test";
import assert from "node:assert/strict";
import { meterColor, meterFraction, meterHealthy, meterTight, meterFull } from "./meter.ts";

// The bug: one colour at every level, so a disk with three quarters free looked
// as urgent as one with nothing left.
test("the colour follows how full the volume is", () => {
  assert.equal(meterColor(10, 100), meterHealthy);
  assert.equal(meterColor(69, 100), meterHealthy);
  assert.equal(meterColor(70, 100), meterTight);
  assert.equal(meterColor(84, 100), meterTight);
  assert.equal(meterColor(85, 100), meterFull);
  assert.equal(meterColor(100, 100), meterFull);
});

// This machine, measured: 190.9 GB used of 245.1 GB. It should not read as
// healthy, and it should not read as an emergency either.
test("the reference machine reads as tight", () => {
  assert.equal(meterColor(190.9e9, 245.1e9), meterTight);
});

// An unreadable capacity must not invent an emergency.
test("an unknown total reads as empty, not as full", () => {
  assert.equal(meterFraction(500, 0), 0);
  assert.equal(meterColor(500, 0), meterHealthy);
  assert.equal(meterFraction(500, -1), 0);
});

test("the fraction is clamped", () => {
  assert.equal(meterFraction(-5, 100), 0);
  assert.equal(meterFraction(150, 100), 1);
});
