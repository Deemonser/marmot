export type HueBand = { center: number; width: number };

// The space map colours by angular position: one continuous sweep around the
// circle, so a child inherits the hue of the wedge it sits in and neighbouring
// branches stay visually distinct. Depth only varies lightness.
// Measured off the reference: hue runs 1:1 with angle from the start of the
// sequence, with no offset. Its first wedge spans relative angle 0-196 and its
// centre lands on hue 95, a yellow-green; the children under it fan 30-189, which
// is that same span (R-055 §5). We used to start at 20 over 290deg, which put the
// biggest wedge in pale orange and compressed the rest.
export const sunburstHueStart = 0;
export const sunburstHueSpan = 360;
// Folded small siblings use the reference's grey, measured at #888787 =
// HSB(0, 1%, 53%) — a deliberate neutral, not some hue darkened (R-055 SS3.3).
export const sunburstAggregate = "#888787";
// Hidden space is the one aggregate the reference does not paint grey: sampled at
// r=150 between 76 and 86 degrees it reads #874A96, a dimmed violet (R-055
// SS3.2). It is a different kind of statement from the folded tail — that is
// "several things too small to draw", this is "space the walk could not account
// for" (ADR-0052 SS4) — so it does not share the grey.
export const sunburstHiddenSpace = "#874A96";
// The reference's used-space sequence ends exactly at 3 o'clock, and the circle
// spans the volume's capacity — free space is the arc that closes it. d3 measures
// from 12 o'clock clockwise, so 3 o'clock is +PI/2.
export const sunburstEndAngle = Math.PI / 2;
// How long the pointer has to settle on one wedge before the list follows it,
// and how long it has to be away before the list goes back. Both exist so that
// crossing the wheel does not make the panel flicker through every node on the
// way.
export const previewDwellMs = 260;
export const previewLeaveMs = 140;

// The wheel and the directory list must agree on an entry's colour, so both
// derive it from the same cumulative angular position.
// The reference's hue runs 1:1 with angle from the start of the sequence, so the
// root band is the whole wheel starting at the measured green.
export const rootHueBand: HueBand = { center: sunburstHueStart + sunburstHueSpan / 2, width: sunburstHueSpan };

// hueAt maps a position within a band, given as a fraction of the band, to a hue.
export function hueAt(fraction: number, band: HueBand): number {
  return (((band.center - band.width / 2 + fraction * band.width) % 360) + 360) % 360;
}

// subBand is the slice of the band an entry hands down to its own children: the
// same fraction of hue that it occupies of angle.
export function subBand(from: number, to: number, band: HueBand): HueBand {
  const start = hueAt(from, band);
  const width = (to - from) * band.width;
  return { center: start + width / 2, width };
}

// baseDepth is the tree depth of the level being listed, so its entries sit at
// baseDepth + 1 (ADR-0059 SS1b).

// An arc thinner than this is a hair, not a slice. Below it siblings fold into
// one grey aggregate, and if even that would be a hair they are left out and the
// background shows through.
//
// 4.6, not 2.5: measured on the reference, its narrowest drawn arc is 7-11px on a
// 2x display and never smaller, while ours reached 0.3px and drew twice as many
// arcs per ring. Those sub-pixel slivers are the "spiky" rim (R-060 §3.8).
export const minArcPixels = 4.6;

// Pure geometry and colour for the space map, split out of App.tsx so it can be
// tested without a DOM. Everything here is sampled off the reference and pinned
// by sunburst.test.ts — see ADR-0059 and R-060.

// The reference's wheel, sampled at 10deg (R-060 SS3.3d): HSV saturation and
// value per bucket, anchored at absolute tree depth 4, plus a per-depth offset
// added to the saturation. 34 of the 36 buckets are measured across six views of
// the reference; 300 and 310 are interpolated, because the only wedge in that
// range is the dimmed hidden-space one and it is excluded from sampling.
//
// This replaces an HSL model with a hue-independent lightness ramp (R-055). That
// model cannot reproduce the reference: at one depth its HSL lightness varies
// with hue -- 58.4% for blue against 64.7% for green -- because its wheel is a
// hand-authored gradient rather than a formula. Fitting S = wheel(hue) +
// offset(depth) over 259 measured samples leaves a mean residual of 0.37.
//
// These are sampling results, not tuning knobs. Changing one needs new samples;
// the tool and raw data are described in R-060 SS2.
export const sunburstWheel: Array<[number, number]> = [
  [53.0, 94.1],
  [53.1, 95.1],
  [53.4, 96.4],
  [53.7, 96.3],
  [52.8, 98.2],
  [52.5, 99.5],
  [52.2, 99.9],
  [52.3, 99.4],
  [52.5, 99.3],
  [52.0, 98.9],
  [52.3, 98.9],
  [46.8, 98.6],
  [43.2, 98.7],
  [43.5, 98.7],
  [43.6, 98.6],
  [43.7, 98.5],
  [43.9, 98.3],
  [44.1, 98.2],
  [46.1, 99.0],
  [48.0, 98.7],
  [50.4, 98.3],
  [51.9, 97.9],
  [55.1, 98.2],
  [57.9, 97.6],
  [57.8, 97.4],
  [56.6, 97.8],
  [56.1, 97.7],
  [55.3, 97.8],
  [54.3, 98.0],
  [51.7, 92.9],
  [51.4, 92.9], // interpolated
  [51.2, 93.0], // interpolated
  [50.9, 93.0],
  [51.0, 92.8],
  [50.7, 93.2],
  [50.5, 93.3],
];
// Indexed by absolute tree depth minus one. The ramp converges, so anything
// deeper than the table reuses its last entry.
export const sunburstDepthOffset = [18.7, 8.7, 2.2, 0.0, -1.1, -1.8, -1.9];
// sliceColor takes the node's depth in the tree, not the ring it is drawn in.
// The reference keeps a folder's colour identical however deep you have
// navigated: drilling into `private` draws its children in the same colour they
// had one ring out at the root, matched to within 0.2 of a saturation point
// across ten hue buckets (R-060 SS3.3c). Colouring by ring index instead makes
// every colour jump on navigation.
export function sliceColor(hue: number, treeDepth: number): string {
  const wrapped = ((hue % 360) + 360) % 360;
  const position = wrapped / 10;
  const lower = Math.floor(position) % sunburstWheel.length;
  const upper = (lower + 1) % sunburstWheel.length;
  const blend = position - Math.floor(position);
  const [lowS, lowV] = sunburstWheel[lower];
  const [highS, highV] = sunburstWheel[upper];
  const offsetIndex = Math.min(Math.max(treeDepth, 1), sunburstDepthOffset.length) - 1;
  const saturation = lowS + (highS - lowS) * blend + sunburstDepthOffset[offsetIndex];
  const value = lowV + (highV - lowV) * blend;
  return hsvToHex(wrapped, saturation, value);
}

export function hsvToHex(hue: number, saturation: number, value: number): string {
  const s = Math.min(Math.max(saturation, 0), 100) / 100;
  const v = Math.min(Math.max(value, 0), 100) / 100;
  const sector = hue / 60;
  const offset = sector - Math.floor(sector);
  const max = v;
  const min = v * (1 - s);
  const rising = min + (max - min) * offset;
  const falling = max - (max - min) * offset;
  let rgb: [number, number, number];
  switch (Math.floor(sector) % 6) {
    case 0: rgb = [max, rising, min]; break;
    case 1: rgb = [falling, max, min]; break;
    case 2: rgb = [min, max, rising]; break;
    case 3: rgb = [min, falling, max]; break;
    case 4: rgb = [rising, min, max]; break;
    default: rgb = [max, min, falling]; break;
  }
  return "#" + rgb.map((channel) => Math.round(channel * 255).toString(16).padStart(2, "0")).join("");
}

// The renderer's geometry, exported so the map query can derive its per-level
// culling thresholds from the very same numbers (ADR-0059 §3). Two copies of
// these ratios would drift, and the drift would show up as missing arcs.
export const sunburstGeometry = {
  viewRadius: 296,
  mainRings: 5,
  maxDepth: 12,
  hubRatio: 1.38,
  thinRingRatio: 0.147,
  thinGapRatio: 0.46,
  radialGapRatio: 1 / 33.5,
  // 0.5pt against a 33.5pt ring -- a hairline. The first pass used 1.5pt, taken
  // from a mean over every narrow background gap; most of those are holes where a
  // child was culled, not separators. The low percentiles put the reference's
  // separator at about 1px on a 2x display (R-060 §3.6, corrected).
  separatorRatio: 0.5 / 33.5,
};

// radiusUnits walks the ring sequence with a unit ring width, so the total is
// derived from the same accumulation ringBounds uses. Writing it as a closed
// formula is how the first attempt lost the five gaps between the main rings and
// overflowed the viewBox — the test caught it.
function radiusUnits(): number {
  const g = sunburstGeometry;
  let radius = g.hubRatio;
  for (let level = 0; level < g.maxDepth; level += 1) {
    radius += level < g.mainRings ? 1 : g.thinRingRatio;
    if (level === g.maxDepth - 1) break;
    radius += level < g.mainRings ? g.radialGapRatio : g.thinRingRatio * g.thinGapRatio;
  }
  return radius;
}

export function ringWidthFor(viewRadius: number): number {
  return viewRadius / radiusUnits();
}

// ringBounds gives the inner and outer radius of one ring, in viewBox units.
export function ringBounds(depth: number): { r0: number; r1: number } {
  const g = sunburstGeometry;
  const ringWidth = ringWidthFor(g.viewRadius);
  const thinRing = ringWidth * g.thinRingRatio;
  let r0 = ringWidth * g.hubRatio;
  for (let level = 0; level < depth; level += 1) {
    r0 += (level < g.mainRings ? ringWidth : thinRing)
      + (level < g.mainRings ? ringWidth * g.radialGapRatio : thinRing * g.thinGapRatio);
  }
  return { r0, r1: r0 + (depth < g.mainRings ? ringWidth : thinRing) };
}

// The narrowest arc worth sending, per projected level. Level 0 of the query is
// the first *projected* ring, which is ring 1 on screen.
export function projectionMinSweeps(depth: number): number[] {
  const sweeps: number[] = [];
  for (let level = 0; level < depth; level += 1) {
    const { r0, r1 } = ringBounds(level + 1);
    sweeps.push(minArcPixels / ((r0 + r1) / 2));
  }
  return sweeps;
}


// childEndAngle gives the seam of the level being entered. The reference holds
// the clicked wedge's angular midline fixed while the wedge opens to the full
// circle — measured to within 0.4deg across the whole expansion, and again in
// reverse on the way out (R-061 §3.6). A full circle laid out as
// [end - 2PI, end] has its midline at end - PI, so end = mid + PI is exactly the
// condition "the midline does not move".
//
// Without a geometry — a click in the list rather than the wheel — the current
// level's seam is carried over rather than guessed.
export function childEndAngle(geom: { a0: number; a1: number } | undefined, fallback: number): number {
  if (!geom) return fallback;
  return (geom.a0 + geom.a1) / 2 + Math.PI;
}
