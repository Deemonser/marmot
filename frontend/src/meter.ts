// The capacity meter's colour.
//
// It was a hardcoded #e0492a -- alarm red -- at every fill level, so a disk with
// three quarters free looked exactly as urgent as one with nothing left. A meter
// that reads the same whatever it measures is decoration, not information.
//
// Three bands, and the thresholds are chosen for what the reading means to
// someone deciding whether to act rather than for even spacing: below 70% there
// is nothing to do, 70-85% is the range where a cleanup is worth planning, and
// above 85% things start failing -- builds, updates, Time Machine snapshots.
export const meterHealthy = "#7fb96a";
export const meterTight = "#d8a33c";
export const meterFull = "#e0492a";

export function meterColor(usedBytes: number, totalBytes: number): string {
  const used = meterFraction(usedBytes, totalBytes);
  if (used >= 0.85) return meterFull;
  if (used >= 0.7) return meterTight;
  return meterHealthy;
}

// meterFraction is how full, clamped. An unknown total reads as empty rather than
// as full: showing alarm red for a volume whose capacity could not be read would
// invent a problem.
export function meterFraction(usedBytes: number, totalBytes: number): number {
  if (!(totalBytes > 0)) return 0;
  return Math.min(1, Math.max(0, usedBytes / totalBytes));
}
