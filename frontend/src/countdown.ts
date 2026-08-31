// The delete countdown's two readouts, from one clock.
//
// They used to come from two. The digit was integer state stepped once a second;
// the ring was drawn from that same integer and then smoothed by a 1s CSS
// transition, so it spent every second travelling towards the value the digit had
// already jumped to -- visibly a second behind the thing it was supposed to
// agree with. And because the digit stopped at 1 and never showed 0, the ring
// unmounted with a fifth of it still drawn, so it never closed.
//
// Both are now functions of the time left, which is the only way two indicators
// of the same quantity stay in step.
export function countdownDigit(remainingMs: number): number {
  // ceil, so the digit reads 5 for the whole first second and 1 for the last.
  return Math.max(0, Math.ceil(remainingMs / 1000));
}

export function countdownFraction(remainingMs: number, totalMs: number): number {
  if (totalMs <= 0) return 0;
  return Math.min(1, Math.max(0, remainingMs / totalMs));
}

// ringOffset is the stroke-dashoffset for a full-circumference dash array: 0
// draws the whole ring, the circumference draws none of it.
export function ringOffset(fraction: number, radius: number): number {
  return 2 * Math.PI * radius * (1 - fraction);
}
