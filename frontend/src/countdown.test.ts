import { test } from "node:test";
import assert from "node:assert/strict";
import { countdownDigit, countdownFraction, deleteFraction, progressHoldMs, ringOffset } from "./countdown.ts";

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

// The two phases share one expression and must run in opposite directions: the
// countdown's arc end retreats anticlockwise, the deletion's grows clockwise.
// Both are checked through the offset, which is what actually reaches the DOM.
test("the countdown drains and the deletion fills, on one formula", () => {
  const circumference = 2 * Math.PI * 20;
  // Countdown: full ring at the start, empty at zero.
  assert.equal(ringOffset(countdownFraction(5000, 5000), 20), 0);
  assert.equal(ringOffset(countdownFraction(0, 5000), 20), circumference);
  // Deletion: empty at the start, full at the end.
  assert.equal(ringOffset(deleteFraction(0, 33e9), 20), circumference);
  assert.equal(ringOffset(deleteFraction(33e9, 33e9), 20), 0);
  // And it is monotonic in between, or the ring would go backwards.
  let previous = Infinity;
  for (let done = 0; done <= 33e9; done += 1e9) {
    const offset = ringOffset(deleteFraction(done, 33e9), 20);
    assert.ok(offset <= previous + 1e-9, `offset rose from ${previous} to ${offset}`);
    previous = offset;
  }
});

// The case that made byte weighting necessary: one item holding most of the plan.
// By item count this run reads 8% for almost all of its duration.
test("progress follows bytes, not item count", () => {
  const sizes = [18.5e9, ...Array(11).fill(1.3e9)];
  const total = sizes.reduce((sum, size) => sum + size, 0);
  // After the big one, most of the work really is done.
  assert.ok(deleteFraction(18.5e9, total) > 0.55, "the largest item barely moved the ring");
  // Whereas one item of twelve is 8%.
  assert.ok(1 / 12 < 0.1);
});

test("a plan with no measurable bytes does not divide by zero", () => {
  assert.equal(deleteFraction(0, 0), 0);
  assert.equal(deleteFraction(5, 0), 0);
});

test("a ring that was never shown holds for nothing", () => {
	assert.equal(progressHoldMs(0, 5_000, 400), 0);
});

test("a ring shown a moment ago holds out the rest of its minimum", () => {
	assert.equal(progressHoldMs(1_000, 1_100, 400), 300);
});

test("a ring that has been up long enough holds for nothing", () => {
	assert.equal(progressHoldMs(1_000, 9_000, 400), 0);
});
