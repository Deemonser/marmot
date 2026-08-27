import { test } from "node:test";
import assert from "node:assert/strict";
import {
  paintMorph,
  clearMorphStyles,
  planMorph,
  arcPath,
  morphEase,
  morphPrune,
  morphMotion,
  morphReveal,
  morphDuration,
  type ArcGeom,
  type MorphNode,
  type MorphPlan,
} from "./morph.ts";

function fakeNode(): MorphNode & { d: string | null; opacity: string | null } {
  const node = {
    d: null as string | null,
    opacity: null as string | null,
    setAttribute(name: string, value: string) {
      if (name === "d") node.d = value;
    },
    style: {
      get opacity() {
        return node.opacity ?? "";
      },
      set opacity(value: string) {
        node.opacity = value;
      },
      removeProperty(name: string) {
        if (name === "opacity") node.opacity = null;
      },
    },
  };
  return node;
}

function fakeGroup() {
  const group = {
    opacity: null as string | null,
    style: {
      get opacity() {
        return group.opacity ?? "";
      },
      set opacity(value: string) {
        group.opacity = value;
      },
      removeProperty(name: string) {
        if (name === "opacity") group.opacity = null;
      },
    },
  };
  return group;
}

const oldGeom: ArcGeom = { a0: 0, a1: 1, r0: 100, r1: 160 };
const newGeom: ArcGeom = { a0: 0, a1: 6.28, r0: 40, r1: 100 };
const departGeom: ArcGeom = { a0: 1, a1: 2, r0: 100, r1: 160 };
const arriveGeom: ArcGeom = { a0: 0, a1: 3, r0: 160, r1: 220 };

function scenario() {
  const nodes = new Map<string, ReturnType<typeof fakeNode>>();
  for (const key of ["move", "arrive", "depart"]) nodes.set(key, fakeNode());
  const plan: MorphPlan = {
    moving: [{ renderKey: "move", from: oldGeom, to: newGeom }],
    arriving: [{ renderKey: "arrive", geom: arriveGeom }],
    departing: [{ renderKey: "depart", geom: departGeom }],
    started: 0,
  };
  return { nodes, plan, group: fakeGroup() };
}

// ADR-0060 gate 2. The reference removes the departing wedges; it never moves
// them. The build this replaces remapped them onto the whole circle and swept
// them out past 0 or 2pi, which the frame sequence shows no trace of.
test("departing arcs never move", () => {
  const { nodes, plan, group } = scenario();
  const expected = arcPath(departGeom);
  for (const elapsed of [0, 50, morphPrune, morphPrune + 100, morphDuration]) {
    paintMorph(plan, elapsed, nodes as never, group);
    assert.equal(nodes.get("depart")!.d, expected, `moved at ${elapsed}ms`);
  }
});

// ADR-0060 gate 3.
test("arriving arcs stay invisible until the movement is over", () => {
  const { nodes, plan, group } = scenario();
  for (const elapsed of [0, morphPrune - 1, morphPrune, morphPrune + morphMotion - 1]) {
    paintMorph(plan, elapsed, nodes as never, group);
    assert.equal(nodes.get("arrive")!.opacity, "0", `visible at ${elapsed}ms`);
  }
  paintMorph(plan, morphPrune + morphMotion + morphReveal / 2, nodes as never, group);
  const middle = Number(nodes.get("arrive")!.opacity);
  assert.ok(middle > 0 && middle < 1, `mid-reveal opacity is ${middle}`);
  paintMorph(plan, morphDuration, nodes as never, group);
  assert.equal(nodes.get("arrive")!.opacity, "1");
});

// The survivors must not start moving during the prune, and must be finished
// before the reveal: three phases that do not overlap (ADR-0060 gate 5).
test("the moving arcs occupy exactly the middle phase", () => {
  const { nodes, plan, group } = scenario();
  paintMorph(plan, morphPrune, nodes as never, group);
  assert.equal(nodes.get("move")!.d, arcPath(oldGeom), "moved before the prune ended");
  paintMorph(plan, morphPrune + morphMotion, nodes as never, group);
  assert.equal(nodes.get("move")!.d, arcPath(newGeom), "not settled when the reveal began");
  assert.equal(morphDuration, morphPrune + morphMotion + morphReveal);
});

test("the departing group fades over the prune and only the prune", () => {
  const { nodes, plan, group } = scenario();
  paintMorph(plan, 0, nodes as never, group);
  assert.equal(group.opacity, "1");
  paintMorph(plan, morphPrune, nodes as never, group);
  assert.equal(group.opacity, "0");
});

// ADR-0060 gate 4: paintMorph writes inline styles, so an interrupted morph
// would otherwise leave the arriving arcs at opacity 0 for good — React reuses
// the same DOM nodes across the re-render and nothing else resets them.
test("interrupting the morph leaves no inline opacity behind", () => {
  const { nodes, plan, group } = scenario();
  paintMorph(plan, morphPrune + 10, nodes as never, group);
  assert.equal(nodes.get("arrive")!.opacity, "0");
  clearMorphStyles(plan, nodes as never);
  assert.equal(nodes.get("arrive")!.opacity, null, "opacity survived the cleanup");
});

// The cleanup must not touch the departing group. Raising it back to full
// opacity made the level just left flash over the new one for a frame, because
// React unmounts the ghosts on a later task than the one that clears the style.
test("cleanup leaves the departing group hidden", () => {
  const { nodes, plan, group } = scenario();
  paintMorph(plan, morphDuration, nodes as never, group);
  assert.equal(group.opacity, "0", "the ghosts should be fully faded by the end");
  clearMorphStyles(plan, nodes as never);
  assert.equal(group.opacity, "0", "the cleanup made the departing arcs visible again");
});

// A grey block travels with the coloured ones when it is the same grey block.
// Both earlier readings were visible on screen: matched bare, the grey keys
// collided between levels and the grey blocks were the *only* arcs that slid;
// excluded outright, they were the only arcs that did not, sitting invisible
// through the whole motion and appearing at the end.
test("a grey block scoped to the same parent travels with the coloured arcs", () => {
  const previous = new Map<string, ArcGeom>([
    ["node:7", oldGeom],
    ["node:7>agg:folded", oldGeom],
    ["node:7>agg:较小对象", oldGeom],
  ]);
  const plan = planMorph(
    previous,
    [
      { morphKey: "node:7", renderKey: "a", geom: newGeom },
      { morphKey: "node:7>agg:folded", renderKey: "b", geom: newGeom },
      { morphKey: "node:7>agg:较小对象", renderKey: "c", geom: newGeom },
    ],
    [],
    0,
  );
  assert.deepEqual(plan.moving.map((t) => t.renderKey), ["a", "b", "c"], "a grey block was left out of the motion");
  assert.equal(plan.arriving.length, 0);
});

// The other half of the rule: the destination's tail is a different tail, and
// must not be tweened out of the one that belonged to another parent.
test("a grey block under a different parent is a different block", () => {
  const previous = new Map<string, ArcGeom>([["node:7>agg:较小对象", oldGeom]]);
  const plan = planMorph(previous, [{ morphKey: "node:9>agg:较小对象", renderKey: "b", geom: newGeom }], [], 0);
  assert.equal(plan.moving.length, 0, "two levels' tails were treated as one object");
  assert.deepEqual(plan.arriving.map((t) => t.renderKey), ["b"]);
});

test("real nodes still match across a level change", () => {
  const previous = new Map<string, ArcGeom>([["node:7", oldGeom]]);
  const plan = planMorph(previous, [{ morphKey: "node:7", renderKey: "a", geom: newGeom }], [], 0);
  assert.equal(plan.moving.length, 1);
  assert.equal(plan.arriving.length, 0);
});

// The interpolator has to be gentle at both ends. easeOutCubic was not: a sixth
// of the way through it had already covered 40% where the reference had moved
// 11% (R-061 §3.1).
test("the easing is gentle at both ends and symmetric", () => {
  assert.equal(morphEase(0), 0);
  assert.equal(morphEase(1), 1);
  assert.ok(Math.abs(morphEase(0.5) - 0.5) < 1e-9, "not symmetric about the midpoint");
  // Symmetry: what it has covered by t equals what it has left at 1 - t.
  for (const t of [0.1, 0.25, 0.4]) {
    assert.ok(Math.abs(morphEase(t) - (1 - morphEase(1 - t))) < 1e-9, `asymmetric at ${t}`);
  }
  // And it must not be front-loaded the way easeOutCubic was.
  assert.ok(morphEase(1 / 6) < 0.2, `covers ${morphEase(1 / 6)} in the first sixth`);
});
