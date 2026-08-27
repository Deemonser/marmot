// The level-change animation, split out of App.tsx so its timeline can be tested
// without a DOM. Everything here is read off a native frame sequence of the
// reference — see ADR-0060 and R-061.
import { arc } from "d3-shape";

export type ArcGeom = { a0: number; a1: number; r0: number; r1: number };

// One generator, reconfigured per call: building a new one per arc costs more
// than the path itself, and the morph rebuilds every arc on every frame.
const arcGenerator = arc<unknown>();

export function arcPath(geom: ArcGeom): string {
  return arcGenerator.innerRadius(geom.r0).outerRadius(geom.r1)({ startAngle: geom.a0, endAngle: geom.a1 }) ?? "";
}

// A level change runs in three phases, which is what the reference does. Read off
// a native frame sequence of DaisyDisk (R-061): the wedges that are leaving are
// gone *before* anything moves — the drawn fraction of the ring drops to exactly
// the clicked wedge's own angular share — then the survivors travel, and only
// then does the destination level's own content appear.
//
// What this replaces: a single phase in which the departing arcs were remapped
// onto the whole circle and swept out past 0 or 2pi. That sweep was an invention;
// the reference never shows those arcs in motion at all.
export type MorphPlan = {
  moving: Array<{ renderKey: string; from: ArcGeom; to: ArcGeom }>;
  arriving: Array<{ renderKey: string; geom: ArcGeom }>;
  departing: Array<{ renderKey: string; geom: ArcGeom }>;
  started: number;
};

// paintMorph writes one frame straight to the DOM. React is not involved: a level
// can carry ~1600 arcs, and reconciling that many elements per frame would not
// hold 60fps.
// clearMorphStyles undoes what paintMorph wrote inline. Without it an
// interrupted morph leaves the arriving arcs at opacity 0 — React reuses the same
// DOM nodes across the re-render, so nothing else resets them and the arcs stay
// invisible until the next navigation.
// Only what paintMorph touches. SVGPathElement satisfies it structurally, and so
// does a plain object, which is what lets the timeline be tested off-DOM.
export type MorphNode = {
  setAttribute(name: string, value: string): void;
  style: { opacity: string; removeProperty(name: string): void };
};
export type MorphGroup = { style: { opacity: string } };

export function clearMorphStyles(plan: MorphPlan, nodes: Map<string, MorphNode>): void {
  for (const arc of plan.arriving) {
    const node = nodes.get(arc.renderKey);
    if (node) node.style.removeProperty("opacity");
  }
  // The departing group is deliberately left as it was painted. Clearing it here
  // raised it back to full opacity, and React unmounts the ghosts on a later
  // task — so the level that was just left flashed back over the new one for a
  // frame. Nothing needs the reset: the ghosts are unmounted, and a new morph
  // sets the group's opacity on its very first painted frame.
}

export function paintMorph(
  plan: MorphPlan,
  elapsed: number,
  nodes: Map<string, MorphNode>,
  ghostGroup: MorphGroup | null,
): void {
  const pruneProgress = Math.min(1, Math.max(0, elapsed / morphPrune));
  const motionProgress = Math.min(1, Math.max(0, (elapsed - morphPrune) / morphMotion));
  const revealProgress = Math.min(1, Math.max(0, (elapsed - morphPrune - morphMotion) / morphReveal));

  if (ghostGroup) ghostGroup.style.opacity = String(1 - pruneProgress);
  for (const arc of plan.departing) {
    const node = nodes.get(arc.renderKey);
    if (node) node.setAttribute("d", arcPath(arc.geom));
  }

  const eased = morphEase(motionProgress);
  for (const tween of plan.moving) {
    const node = nodes.get(tween.renderKey);
    if (!node) continue;
    node.setAttribute("d", arcPath({
      a0: tween.from.a0 + (tween.to.a0 - tween.from.a0) * eased,
      a1: tween.from.a1 + (tween.to.a1 - tween.from.a1) * eased,
      r0: tween.from.r0 + (tween.to.r0 - tween.from.r0) * eased,
      r1: tween.from.r1 + (tween.to.r1 - tween.from.r1) * eased,
    }));
  }

  for (const arc of plan.arriving) {
    const node = nodes.get(arc.renderKey);
    if (!node) continue;
    node.setAttribute("d", arcPath(arc.geom));
    node.style.opacity = String(revealProgress);
  }
}

// Cubic ease-in-out (smoothstep), the curve a Mac app gets by default.
//
// It replaces easeOutCubic, which was wrong in exactly the direction the
// reference is gentle: a sixth of the way through, easeOutCubic is already 40%
// done where the reference had moved 11%. Against the measured samples linear
// fits best (mean error 0.036), smoothstep next (0.05), easeOutCubic not at all.
// Smoothstep is chosen over the better-fitting linear because linear reads as
// mechanical and the difference sits inside the measurement's own noise — the
// tension is recorded rather than hidden (ADR-0060 §2).
export function morphEase(t: number): number {
  return t * t * (3 - 2 * t);
}

// Phase durations. The reference's main motion measured 410-625ms across four
// interactions; 520 sits inside that band, where the first pass used 380 and read
// as hurried.
//
// Still provisional: the capture loop was itself among the top CPU consumers on a
// machine already at load 8 of 8 cores, and the same interaction timed
// differently run to run. A time-based animation keeps its duration under load —
// what load costs is frames, so these readings are more likely floors than
// inflations — but they still want an idle machine (R-061 §5).
export const morphPrune = 120;
export const morphMotion = 520;
export const morphReveal = 120;
export const morphDuration = morphPrune + morphMotion + morphReveal;
// Above this many arcs the morph is skipped rather than allowed to stutter: each
// frame rewrites every arc's `d`, so the cost is linear in arc count.
export const morphArcCeiling = 2600;


// planMorph decides what each arc does across a level change.
//
// Identity is matched by key, so anything whose key can repeat across levels
// must be excluded: a folded block is keyed by its depth and position and an
// aggregate by its name, and both collide between two different levels. When
// they did, the grey blocks were the only arcs the morph thought had survived —
// so every real wedge correctly departed or arrived while the grey ones alone
// slid across the wheel. They are never the same object; they always depart and
// arrive.
export function planMorph(
  previous: Map<string, ArcGeom>,
  slices: Array<{ key: string; renderKey: string; geom: ArcGeom; aggregate: boolean }>,
  departing: Array<{ renderKey: string; geom: ArcGeom }>,
  started: number,
): MorphPlan {
  const moving: MorphPlan["moving"] = [];
  const arriving: MorphPlan["arriving"] = [];
  for (const slice of slices) {
    const from = slice.aggregate ? undefined : previous.get(slice.key);
    if (from) {
      moving.push({ renderKey: slice.renderKey, from, to: slice.geom });
    } else {
      arriving.push({ renderKey: slice.renderKey, geom: slice.geom });
    }
  }
  return { moving, arriving, departing, started };
}

// survivingKeys is what the *next* level change will match against. Aggregates
// are left out for the same reason.
export function survivingKeys(slices: Array<{ key: string; aggregate: boolean }>): Set<string> {
  const keys = new Set<string>();
  for (const slice of slices) {
    if (!slice.aggregate) keys.add(slice.key);
  }
  return keys;
}
