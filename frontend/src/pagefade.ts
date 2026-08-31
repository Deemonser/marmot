// How the source page and the result page change places.
//
// The first attempt animated the content -- crossfade plus a slight scale -- and
// the user reported it was invisible. It was: the reference does not animate its
// content at all. R-066 captured the real thing frame by frame, and the page
// change IS a window resize. DaisyDisk's window is 152pt tall on the source page
// and 745pt on the result page, and it ramps between them:
//
//   forward  152 -> 745 in 193ms, steps of +49, +50, +49, +49, +50, ...
//   back     745 -> 152 in 201ms, steps of -49, -49, -50, -49, -50, ...
//
// Uniform steps, so it is LINEAR, not eased -- 12 frames of ~49.4pt at 60fps. The
// window's Y never moves; it grows and shrinks downward from a fixed top edge.
//
// Our app already resizes the window the same way (968x151+rows against 968x715)
// and did it in one instant SetSize, which is why nothing looked smooth: the jump
// was the whole event, and a content fade underneath it could not be seen.
export const windowResizeMs = 200;

// A scale on the content is actively wrong here: the window is changing height,
// and scaling what is inside at the same time is two motions fighting. Only a
// short opacity fade remains, so content revealed by the growing window does not
// pop in -- and it finishes with the resize rather than after it.
export const contentFadeMs = 140;

// prefersReducedMotion is honoured in code, not only in CSS, because the resize
// is driven from a frame loop: without checking, "no motion" would still get 12
// window resizes.
export function prefersReducedMotion(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}

// resizeSteps is the sequence of heights to apply, linear from `from` to `to`,
// ending exactly on `to`. Measured step size is ~49.4pt per frame; deriving the
// count from it keeps our speed the same as the reference's whatever the distance,
// rather than fixing a duration and going faster on a taller window.
export const measuredStepPt = 49.4;

export function resizeSteps(from: number, to: number): number[] {
  const distance = Math.abs(to - from);
  if (distance < 1) return [];
  const count = Math.max(1, Math.round(distance / measuredStepPt));
  const steps: number[] = [];
  for (let index = 1; index <= count; index++) {
    steps.push(Math.round(from + ((to - from) * index) / count));
  }
  // Exactly on target, never a pixel short: the last frame is the one that
  // decides whether the window looks settled.
  steps[steps.length - 1] = to;
  return steps;
}

// frameMs at 60fps. The reference's steps arrive about every 16ms.
export const frameMs = 1000 / 60;

export function leaveDelay(): number {
  return prefersReducedMotion() ? 0 : windowResizeMs;
}
