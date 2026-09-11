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

// ringRadius is where the arc is drawn, and it is not a free choice: the arc IS
// the badge's outer ring, redrawn. The badge is 50px with a 2px border under
// `box-sizing: border-box`, so that border's centreline sits at 25 - 1 = 24, and
// the stroke has to land on it rather than beside it. The ring used to be given
// `inset: -2px` and a 2.5px stroke over a 44-unit viewBox, which put its
// centreline at 24.5 and its outer edge a pixel PAST the badge -- a second,
// fatter ring hovering outside the first, with the real one showing through
// underneath. The SVG is now a 50-unit viewBox at inset 0, which makes one user
// unit one pixel: these numbers and the stylesheet's are the same numbers.
export const ringRadius = 24;

// ringOffset is the stroke-dashoffset for a full-circumference dash array: 0
// draws the whole ring, the circumference draws none of it.
export function ringOffset(fraction: number, radius: number): number {
  return 2 * Math.PI * radius * (1 - fraction);
}

// deleteFraction is the same ring read the other way round. Given the same
// expression -- offset = C * (1 - f) -- an SVG circle rotated -90deg draws the
// first f of its path clockwise from twelve o'clock. So a fraction falling from 1
// retreats the arc's end anticlockwise (the countdown) and a fraction rising from
// 0 grows it clockwise (the deletion). One formula, two directions, and nothing
// to keep in sync.
//
// By inode, not by byte. Deletion unlinks every file, so its cost is per node and
// almost independent of size: measured on APFS, an 8 GiB file unlinks in 0.000s
// while a 204k inode tree takes 3.2s. A byte-weighted ring spent a big file's
// entire share of the bar in one frame and then stood still through the part that
// actually took the time -- which is the failure it was introduced to fix, moved
// somewhere else.
export function deleteFraction(doneNodes: number, totalNodes: number): number {
	if (totalNodes <= 0) return 0;
	return Math.min(1, Math.max(0, doneNodes / totalNodes));
}

// progressHoldMs is how much longer the delete ring has to stay before it may be
// taken down, given when it appeared. Zero when it was never shown, which is the
// common case: most deletions finish before the reveal.
//
// It exists because the reveal is a bet. Showing the ring only after the run has
// proved slow means a run that finishes just after the reveal would otherwise
// flash it for a frame or two, and a flash reads as a glitch rather than as
// progress.
export function progressHoldMs(shownAt: number, now: number, minMs: number): number {
	if (shownAt <= 0) return 0;
	return Math.max(0, minMs - (now - shownAt));
}
