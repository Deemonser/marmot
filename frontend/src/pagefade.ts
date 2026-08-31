// The source page and the result page used to replace each other on one frame.
// The reference crossfades in both directions, and the swap is the one moment in
// the app where the whole window changes at once -- without a transition it reads
// as a different app appearing rather than the same one going somewhere.
//
// Timings sit inside the band R-061 §5 measured on the reference for its main
// motion (410-625ms), scaled down: a page change carries less information than a
// level change in the wheel, so it should be quicker than morphMotion's 520ms and
// must not add noticeable latency to a scan the user already waited for.
//
// Asymmetric on purpose. Arriving is not delayed at all -- the result is what was
// being waited for, so it animates in from wherever the data lands. Leaving is
// deliberate: the user pressed back, so the outgoing page is allowed its exit
// before the state changes, which is what makes it read as one movement rather
// than two unrelated ones.
export const pageEnterMs = 260;
export const pageLeaveMs = 180;

// The scale a page enters from and leaves to. Small: at 0.9 the window reads as
// zooming, which competes with the wheel's own level-change zoom and makes the
// two mean different things.
export const pageEnterScale = 0.965;
export const pageLeaveScale = 0.985;

// prefersReducedMotion is honoured in code as well as in CSS, because the leave
// animation holds a state change behind a timer -- a person who has asked for no
// motion should get the state change immediately, not 180ms later.
export function prefersReducedMotion(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}

// leaveDelay is how long to hold a page change so the outgoing page can animate.
export function leaveDelay(): number {
  return prefersReducedMotion() ? 0 : pageLeaveMs;
}
