// The capacity meter's colour.
//
// It was a hardcoded #e0492a -- alarm red -- at every fill level, so a disk with
// three quarters free looked exactly as urgent as one with nothing left. Three
// fixed bands fixed that but introduced a cliff: 84% and 85% full were different
// colours, and 84% looked like 70%. The reference does neither. Its colour is a
// continuous function of the fill, so a bar at 84% is orange -- warmer than the
// amber at 70%, not yet the red at 100% -- and two disks are told apart by
// shade, not just by band.
//
// The stops are chosen for what the reading means to someone deciding whether
// to act: below half there is nothing to do, so the colour stays green rather
// than drifting; 70% is where a cleanup is worth planning; 85% is where things
// start failing -- builds, updates, Time Machine snapshots.
//
// Interpolation is in HSL, not RGB: a straight RGB blend from green to amber
// passes through a muddy olive, whereas walking the hue from 104° down to 10°
// passes through yellow-green, yellow and orange the way a real gradient does.
type Stop = readonly [fraction: number, h: number, s: number, l: number];

const stops: readonly Stop[] = [
  [0.0, 104, 37, 57], // #7fb96a green
  [0.5, 104, 37, 57], // still green: the flat part
  [0.7, 40, 67, 54], // #d8a33c amber
  [0.85, 25, 76, 54], // orange, the reference machine's colour
  [1.0, 10, 75, 52], // #e0492a red
];

export function meterColor(usedBytes: number, totalBytes: number): string {
  const [h, s, l] = meterHSL(meterFraction(usedBytes, totalBytes));
  return `hsl(${h.toFixed(1)} ${s.toFixed(1)}% ${l.toFixed(1)}%)`;
}

// meterHSL is the colour at a clamped fill fraction, as [hue, saturation,
// lightness]. Exported for tests, which want to reason about hue rather than
// compare strings.
export function meterHSL(fraction: number): [number, number, number] {
  const f = Math.min(1, Math.max(0, fraction));
  for (let i = 1; i < stops.length; i++) {
    const [f1, h1, s1, l1] = stops[i];
    if (f > f1) continue;
    const [f0, h0, s0, l0] = stops[i - 1];
    const t = f1 === f0 ? 1 : (f - f0) / (f1 - f0);
    return [h0 + (h1 - h0) * t, s0 + (s1 - s0) * t, l0 + (l1 - l0) * t];
  }
  const [, h, s, l] = stops[stops.length - 1];
  return [h, s, l];
}

// meterFraction is how full, clamped. An unknown total reads as empty rather than
// as full: showing alarm red for a volume whose capacity could not be read would
// invent a problem.
export function meterFraction(usedBytes: number, totalBytes: number): number {
  if (!(totalBytes > 0)) return 0;
  return Math.min(1, Math.max(0, usedBytes / totalBytes));
}
