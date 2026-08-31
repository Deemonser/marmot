import { test } from "node:test";
import assert from "node:assert/strict";
import { countdownDigit, countdownFraction, ringOffset } from "./countdown.ts";

const total = 5000;

// The invariant that was broken: whenever the digit reads n, the ring is showing
// somewhere between (n-1)/5 and n/5 of the circle. Two indicators of one quantity
// must never disagree about it.
test("the ring always agrees with the digit it sits behind", () => {
  for (let remaining = total; remaining >= 0; remaining -= 17) {
    const digit = countdownDigit(remaining);
    const fraction = countdownFraction(remaining, total);
    const lower = Math.max(0, digit - 1) / 5;
    const upper = digit / 5;
    assert.ok(
      fraction >= lower - 1e-9 && fraction <= upper + 1e-9,
      `digit ${digit} but ring at ${fraction.toFixed(3)} (expected ${lower}..${upper}) at ${remaining}ms`,
    );
  }
});

// It used to vanish at 20%, because the digit stopped at 1 and the ring was drawn
// from the digit.
test("the ring closes completely", () => {
  assert.equal(countdownFraction(0, total), 0);
  assert.equal(ringOffset(0, 20), 2 * Math.PI * 20);
  assert.equal(ringOffset(1, 20), 0);
});

test("the digit holds each value for a full second", () => {
  assert.equal(countdownDigit(5000), 5);
  assert.equal(countdownDigit(4001), 5);
  assert.equal(countdownDigit(4000), 4);
  assert.equal(countdownDigit(1000), 1);
  assert.equal(countdownDigit(1), 1);
  assert.equal(countdownDigit(0), 0);
});

// A frame can arrive after the deadline, or the clock can jump.
test("out-of-range time never draws an impossible ring", () => {
  assert.equal(countdownFraction(-500, total), 0);
  assert.equal(countdownFraction(total + 500, total), 1);
  assert.equal(countdownDigit(-500), 0);
});
