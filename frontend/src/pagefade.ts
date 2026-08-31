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

// The content moves too, and it is a horizontal PUSH, not a fade and certainly not
// a scale. Captured mid-transition (R-066 §3.4): going back, the result page's
// wheel and list slide right until the list's numbers are off the window edge,
// while the source row and its buttons enter from the left. Forward is the
// mirror. The two pages travel together, like a carousel.
//
// Honest about what is NOT measured: the push's easing and exact distance. Both
// pages share every mid-transition frame, which made cross-correlation bimodal --
// no single global shift describes a frame holding two contents -- and tracking one
// page by its bright-pixel centroid failed too, because the shrinking window cuts
// the measured band away. What is measured precisely is the window ramp: 200ms,
// linear. The push runs with it on the same clock and the same curve, which is a
// choice consistent with the evidence rather than an observation of the push
// itself.
export const contentPushMs = windowResizeMs;
// Distance is one window width: the outgoing page leaves completely.
export const contentPushPercent = 100;

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

// The reference's measured ramp -- 49.4pt per frame at 60fps -- is NOT reproduced.
// R-066 §4.1: ramping the window while the content moves fought the webview's
// viewport-driven layout through four verified attempts, so the window now changes
// size in one step at the moment when the least is on screen, and the push gets a
// stable viewport. The measurement stays in R-066 because it is still the target if
// this is revisited.
export const measuredStepPt = 49.4;

export function leaveDelay(): number {
  return prefersReducedMotion() ? 0 : windowResizeMs;
}
