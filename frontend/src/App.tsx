import { useEffect, useLayoutEffect, useRef, useState, useMemo } from "react";
import { sliceColor, sunburstGeometry, projectionMinSweeps, minArcPixels, ringWidthFor } from "./sunburst";
import type { CSSProperties, DragEvent as ReactDragEvent, PointerEvent as ReactPointerEvent } from "react";
import { arc } from "d3-shape";
import { Dialogs, Events, Window } from "@wailsio/runtime";
import { Service as MarmotService } from "../bindings/example.com/marmot/internal/presentation/wails";
import type * as Models from "../bindings/example.com/marmot/internal/presentation/wails/models";

type NodeView = Models.NodeView;
type MapEntry = Models.MapEntry;
type MapResult = Models.MapResult;
type ScanStatus = Models.ScanStatus;
type ScanProgress = Models.ScanProgress;
type PermissionStatus = Models.PermissionStatus;
type StorageSourceOverview = Models.StorageSourceOverview;
type CleanupPlan = Models.CleanupPlan;
type CleanupValidation = Models.CleanupValidation;
// hue is the level's slice of the colour wheel, carried on the crumb so
// navigating up restores exactly the colours that level had on the way down.
// HueBand is the stretch of the colour wheel a level's children are spread over.
// Position inside it is proportional to angular position, which is proportional
// to size — the reference does the same: its root wedges sit at 97.6/236.6/289.1/
// 318/332 degrees of arc measured from the start of the sequence and carry hues
// 95/235/294/323/340, which is 1:1 (R-055 §5). Neighbouring small wedges
// therefore do come out similar in both, because their arcs are adjacent.
// A wedge's own hue is constant across its whole arc, and its children inherit
// its slice of the band, so an entry's colour does not change when you drill in.
type HueBand = { center: number; width: number };
type Breadcrumb = { id: number; path: string; hue: HueBand };
type Capability = "enter" | "preview" | "reveal" | "collect" | "rescan";
type NavigationMode = "push" | "replace" | "travel";
type ProjectedEntry = Models.ProjectedEntry;

type Page = {
  snapshotId: number;
  parentId: number;
  path: string;
  offset: number;
  crumbs: Breadcrumb[];
  // The band this level's children are drawn from, inherited on drill-down so an
  // entry's colour is the same before and after you enter its parent.
  hue: HueBand;
};

const defaultRoot = "/";
const pageSize = 256;
// Children smaller than this share of the parent fold into the "smaller items"
// aggregate. Derived from the reference: at 0.5% a 192 GB root keeps its seven
// meaningful children and a 133 GB home folder keeps sixteen.
// 0.5% of the parent folded /usr (1.1 GB of a 225 GB root) away, while the
// original lists it and folds only a few MB at the root. Measured against the
// reference: its root fold is 6.5 MB out of 231.9 GB.
const smallEntryShare = 0.0005;
// Seconds the destructive action waits before running, so it can be stopped.
const countdownSeconds = 5;

// foldSmallEntries merges the long tail of tiny children into the aggregate the
// backend already provides, keeping totals intact.
function foldSmallEntries(entries: MapEntry[], parentTotal: number): MapEntry[] {
  if (entries.length === 0 || parentTotal <= 0) return entries;
  const threshold = parentTotal * smallEntryShare;
  const kept: MapEntry[] = [];
  const folded: MapEntry[] = [];
  for (const entry of entries) {
    if (entry.kind === "node" && entrySize(entry) < threshold) folded.push(entry);
    else kept.push(entry);
  }
  if (folded.length === 0) return entries;
  const existing = kept.find((entry) => entry.kind === "aggregate" && entry.virtualType === "smaller_objects");
  const merged: MapEntry = existing
    ? { ...existing }
    : { ...folded[0], kind: "aggregate", node: folded[0].node, name: "较小的项目…", virtualType: "smaller_objects", displayState: "partial", capabilities: ["enter"], count: 0, logicalSize: 0, allocatedSize: 0, ownedAllocated: 0, children: null, childrenTotal: 0, childrenHasMore: false };
  for (const entry of folded) {
    merged.count = (merged.count ?? 0) + 1;
    merged.logicalSize += entry.logicalSize;
    merged.allocatedSize += entry.allocatedSize;
    merged.ownedAllocated += entry.ownedAllocated;
  }
  const result = kept.filter((entry) => entry !== existing);
  result.push(merged);
  return result;
}
const maxHistory = 32;
// Must match volumeMenuName in internal/presentation/wails/menu.go.
const volumeMenuName = "volume-actions";

// The native menu is opened by the @wailsio/runtime contextmenu handler, which
// reads --custom-contextmenu off the event target. Dispatching a synthetic
// contextmenu at the button's bottom-left therefore drops the native menu where
// the original drops it — outside the window, which a DOM menu cannot do
// (ADR-0051).
async function openVolumeMenu(button: HTMLElement, sourceID: string, hasResult: boolean): Promise<void> {
  await MarmotService.PrepareVolumeMenu(sourceID, hasResult);
  // Anchor to the whole split button, not just the chevron: that is where the
  // original drops its menu.
  const box = (button.parentElement ?? button).getBoundingClientRect();
  button.dispatchEvent(new MouseEvent("contextmenu", {
    bubbles: true,
    cancelable: true,
    clientX: Math.round(box.left),
    clientY: Math.round(box.bottom),
  }));
}
// R-014 measured the original's launch window at about 968 x 151; the source
// window grows only by the volume rows it actually has.
const sourceWindowSize = { width: 968, height: 151 };
const sourceRowHeight = 54;
const resultWindowSize = { width: 968, height: 715 };
const phaseLabels: Record<string, string> = {
  catalog: "准备卷",
  volume_overview: "读取概览",
  top_level_publish: "发布首层",
  deep_scan: "深入扫描",
  finalize: "整理结果",
};
const browsableScanStates = new Set(["completed", "completed_with_issues", "cancelled", "interrupted"]);
const virtualLabels: Record<string, string> = {
  smaller_objects: "较小对象",
  hidden_space: "隐藏空间",
  purgeable_space: "可清理空间",
  other_volumes: "其他卷",
  snapshot: "系统快照",
  restricted: "受限空间",
};
// The space map colours by angular position: one continuous sweep around the
// circle, so a child inherits the hue of the wedge it sits in and neighbouring
// branches stay visually distinct. Depth only varies lightness.
// Measured off the reference: hue runs 1:1 with angle from the start of the
// sequence, with no offset. Its first wedge spans relative angle 0-196 and its
// centre lands on hue 95, a yellow-green; the children under it fan 30-189, which
// is that same span (R-055 §5). We used to start at 20 over 290deg, which put the
// biggest wedge in pale orange and compressed the rest.
const sunburstHueStart = 0;
const sunburstHueSpan = 360;
// Folded small siblings use the reference's grey, #888787.
const sunburstAggregate = "#888787";
// The reference's used-space sequence ends exactly at 3 o'clock, and the circle
// spans the volume's capacity — free space is the arc that closes it. d3 measures
// from 12 o'clock clockwise, so 3 o'clock is +PI/2.
const sunburstEndAngle = Math.PI / 2;
// The wheel and the directory list must agree on an entry's colour, so both
// derive it from the same cumulative angular position.
// The reference's hue runs 1:1 with angle from the start of the sequence, so the
// root band is the whole wheel starting at the measured green.
const rootHueBand: HueBand = { center: sunburstHueStart + sunburstHueSpan / 2, width: sunburstHueSpan };

// hueAt maps a position within a band, given as a fraction of the band, to a hue.
function hueAt(fraction: number, band: HueBand): number {
  return (((band.center - band.width / 2 + fraction * band.width) % 360) + 360) % 360;
}

// subBand is the slice of the band an entry hands down to its own children: the
// same fraction of hue that it occupies of angle.
function subBand(from: number, to: number, band: HueBand): HueBand {
  const start = hueAt(from, band);
  const width = (to - from) * band.width;
  return { center: start + width / 2, width };
}

// baseDepth is the tree depth of the level being listed, so its entries sit at
// baseDepth + 1 (ADR-0059 SS1b).
function entryColors(entries: MapEntry[], band: HueBand, baseDepth: number): Record<string, string> {
  const bands = entryBands(entries, band);
  const colors: Record<string, string> = {};
  for (const entry of entries) {
    const own = bands[entryKey(entry)];
    colors[entryKey(entry)] = entry.kind === "node" && own ? sliceColor(own.center, baseDepth + 1) : sunburstAggregate;
  }
  return colors;
}

// Decimal units, like Apple and the original: a 245.1 GB volume is 228.3 GiB, so
// dividing by 1024 while labelling the result "GB" read every number 7.4% low
// (ADR-0052 §1). Measurements in the docs stay in GiB and say so.
function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = value;
  let unit = 0;
  while (size >= 1000 && unit < units.length - 1) {
    size /= 1000;
    unit += 1;
  }
  if (unit === 0) return size.toFixed(0) + " B";
  const text = size.toFixed(1);
  return (text.endsWith(".0") ? text.slice(0, -2) : text) + " " + units[unit];
}

function confidenceLabel(confidence: string): string {
  return ({ exact: "精确", estimated: "估算", partial: "部分结果", unknown: "未知" } as Record<string, string>)[confidence] ?? "待确认";
}

function displayStateLabel(state: string): string {
  return ({ current: "当前结果", stale: "对象已变化", partial: "部分结果" } as Record<string, string>)[state] ?? "待确认";
}

function entryNode(entry: MapEntry | null): NodeView | null {
  return entry?.kind === "node" && entry.node?.id > 0 ? entry.node : null;
}

function entryKey(entry: MapEntry): string {
  if (entry.kind === "node") return "node:" + entry.node.id;
  return entry.kind + ":" + (entry.virtualType || "unknown") + ":" + entry.name;
}

// ArcGeom is everything needed to draw one arc, in a form that interpolates:
// two angles and two radii. Drilling into a wedge maps its angular span onto the
// full circle and moves every ring inward by one, so a slice that survives the
// navigation can be tweened from its old geometry to its new one and the level
// change reads as a zoom instead of a cut.
type ArcGeom = { a0: number; a1: number; r0: number; r1: number };

// One generator, reconfigured per call: building a new one per arc costs more
// than the path itself, and the morph rebuilds every arc on every frame.
const arcGenerator = arc<unknown>();

function arcPath(geom: ArcGeom): string {
  return arcGenerator.innerRadius(geom.r0).outerRadius(geom.r1)({ startAngle: geom.a0, endAngle: geom.a1 }) ?? "";
}

// paintMorph writes one frame of the tween straight to the DOM. React is not
// involved: a level can carry ~1600 arcs, and reconciling that many elements per
// frame would not hold 60fps.
function paintMorph(
  state: { tweens: Array<{ renderKey: string; from: ArcGeom; to: ArcGeom }>; started: number },
  progress: number,
  nodes: Map<string, SVGPathElement>,
): void {
  const eased = easeOutCubic(progress);
  for (const tween of state.tweens) {
    const node = nodes.get(tween.renderKey);
    if (!node) continue;
    node.setAttribute("d", arcPath({
      a0: tween.from.a0 + (tween.to.a0 - tween.from.a0) * eased,
      a1: tween.from.a1 + (tween.to.a1 - tween.from.a1) * eased,
      r0: tween.from.r0 + (tween.to.r0 - tween.from.r0) * eased,
      r1: tween.from.r1 + (tween.to.r1 - tween.from.r1) * eased,
    }));
  }
}

function easeOutCubic(t: number): number {
  const inverse = 1 - t;
  return 1 - inverse * inverse * inverse;
}

// Duration of the level-change morph. Long enough to read as motion, short
// enough that it never delays the next click.
const morphDuration = 420;
// Above this many arcs the morph is skipped rather than allowed to stutter: each
// frame rewrites every arc's `d`, so the cost is linear in arc count.
const morphArcCeiling = 2600;

// projectedArc normalizes a slim projected descendant into a drawable arc.
function projectedArc(child: ProjectedEntry) {
  return {
    key: child.kind === "aggregate" ? "aggregate:" + child.name : "node:" + child.id,
    size: Math.max(0, child.size),
    isDirectory: child.kind === "directory",
    aggregate: child.kind === "aggregate",
    stale: false,
    entry: null as MapEntry | null,
    children: (child.children ?? []).filter(Boolean),
  };
}

function entrySize(entry: MapEntry): number {
  return Math.max(0, entry.ownedAllocated);
}

function entryCapabilities(entry: MapEntry | null): Capability[] {
  return (entry?.capabilities ?? []) as Capability[];
}

function hasCapability(entry: MapEntry | null, capability: Capability): boolean {
  return entryCapabilities(entry).includes(capability);
}


function entryPath(entry: MapEntry): string {
  return entryNode(entry)?.path ?? (entry.virtualType ? virtualLabels[entry.virtualType] ?? entry.name : entry.name);
}

function firstEntry(map: MapResult): MapEntry | null {
  return (map.entries ?? [])[0] ?? null;
}

// The first crumb names the scanned volume the way the reference does; deeper
// crumbs use the directory name.
function crumbLabel(path: string, index: number): string {
  if (index > 0) return path.split("/").pop() || path;
  if (path === "/") return "Macintosh HD";
  return path.split("/").pop() || path;
}

function rootPage(snapshotId: number, path: string): Page {
  return { snapshotId, parentId: 1, path, offset: 0, crumbs: [{ id: 1, path, hue: rootHueBand }], hue: rootHueBand };
}

// entryBands gives every entry its slice of the band, by the same weights the
// wheel divides the angle by.
function entryBands(entries: MapEntry[], band: HueBand): Record<string, HueBand> {
  const total = entries.reduce((sum, entry) => sum + (entrySize(entry) || 1), 0) || 1;
  const bands: Record<string, HueBand> = {};
  let cursor = 0;
  for (const entry of entries) {
    const next = cursor + (entrySize(entry) || 1) / total;
    bands[entryKey(entry)] = subBand(cursor, next, band);
    cursor = next;
  }
  return bands;
}

function statusFromProgress(progress: ScanProgress): ScanStatus {
  return {
    taskId: progress.taskId,
    snapshotId: progress.snapshotId,
    root: progress.root,
    state: progress.state,
    phase: progress.phase,
    nodes: progress.nodes,
    files: progress.files,
    directories: progress.directories,
    countedBytes: progress.countedBytes,
    volumeUsedBytes: progress.volumeUsedBytes,
    bytes: progress.bytes,
    issues: progress.issues ?? [],
    error: progress.error,
  };
}

function staleDisplayEntry(entry: MapEntry | null, staleKey: string | null): MapEntry | null {
  if (!entry || entryKey(entry) !== staleKey) return entry;
  return { ...entry, displayState: "stale", capabilities: ["rescan"] };
}

function Sunburst({
  map,
  hoveredKey,
  focusedKey,
  selectedKey,
  onHover,
  onFocus,
  onActivate,
  onPreview,
  onReveal,
  onGoParent,
  centerColor,
  hueRange,
  baseDepth,
  onDragEntry,
}: {
  map: MapResult | null;
  hoveredKey: string | null;
  focusedKey: string | null;
  selectedKey: string | null;
  onHover: (entry: MapEntry | null) => void;
  onFocus: (entry: MapEntry) => void;
  onActivate: (entry: MapEntry) => void;
  onPreview: (entry: MapEntry) => void;
  onReveal: (entry: MapEntry) => void;
  onGoParent: () => void;
  centerColor: string | null;
  hueRange: HueBand;
  baseDepth: number;
  onDragEntry: (entry: MapEntry, event: ReactPointerEvent) => void;
}) {
  const entries = foldSmallEntries((map?.entries ?? []).filter(Boolean), map?.parent.ownedAllocated ?? 0);
  // Geometry from ADR-0059 SS2, all as ratios of the main ring width so the chart
  // scales with the window. Measured on six views of the reference, where the hub
  // and the ring width were identical in every one.
  const { viewRadius, mainRings, maxDepth, hubRatio, thinRingRatio, thinGapRatio, radialGapRatio, separatorRatio } = sunburstGeometry;
  const ringWidth = ringWidthFor(viewRadius);
  const innerRadius = ringWidth * hubRatio;
  const thinRing = ringWidth * thinRingRatio;
  const thinGap = thinRing * thinGapRatio;
  // Label metrics measured off the reference's hub, as ratios of its radius: a
  // 32px glyph height at 2x over a 93px hub radius, with the two baselines 50px
  // apart (R-060). Held as ratios so they follow the hub when the chart resizes.
  const hubLabelSize = innerRadius * 0.484;
  const hubLabelBaseline = innerRadius * -0.086;
  const hubLabelLead = innerRadius * 0.538;
  const ringThickness = (depth: number) => (depth < mainRings ? ringWidth : thinRing);
  const ringGap = (depth: number) => (depth < mainRings ? ringWidth * radialGapRatio : thinGap);
  const separatorArc = ringWidth * separatorRatio;
  const ringStart = (depth: number) => {
    let radius = innerRadius;
    for (let level = 0; level < depth; level += 1) {
      radius += ringThickness(level) + ringGap(level);
    }
    return radius;
  };
  const pathRefs = useRef<Record<string, SVGPathElement | null>>({});
  // Separate from pathRefs, which is keyed by node and used for focus: the morph
  // needs one handle per drawn arc, and the same node can be drawn at more than
  // one place across a transition.
  const morphRefs = useRef<Map<string, SVGPathElement>>(new Map());
  // Geometry of the last painted level, keyed by node. The source of the tween.
  const paintedGeom = useRef<Map<string, ArcGeom>>(new Map());
  const morphFrame = useRef<number | null>(null);
  // The in-flight tween. Held in a ref because any re-render during the morph
  // (hovering, which happens immediately: the pointer is still over the wheel
  // when the click lands) rewrites every `d` back to the destination, so the
  // current frame has to be re-applied afterwards.
  const morphState = useRef<{ tweens: Array<{ renderKey: string; from: ArcGeom; to: ArcGeom }>; started: number } | null>(null);
  // Arcs that the level change removes. Without them the wedges that are not
  // part of the destination vanish on the first frame and leave a hole in the
  // wheel; the original sweeps them out of the circle instead.
  const [ghosts, setGhosts] = useState<Array<{ renderKey: string; color: string; from: ArcGeom; to: ArcGeom }>>([]);
  const paintedSlices = useRef<Array<{ key: string; geom: ArcGeom; color: string }>>([]);
  // The level we are leaving, so a hub click can fold the old level back into
  // the wedge it came from.
  const previousParentKey = useRef<string | null>(null);
  const ghostGroupRef = useRef<SVGGElement | null>(null);
  // An arc is either a current-level entry, which is interactive because it
  // carries a full node, or a projected descendant, which is visual only: it has
  // no path and therefore cannot authorise any file operation (ADR-0048).
  type Arc = {
    key: string;
    renderKey: string;
    depth: number;
    hue: number;
    path: string;
    // The geometry behind `path`. Kept so a level change can interpolate from
    // where each arc was to where it lands, instead of cutting.
    geom: ArcGeom;
    entry: MapEntry | null;
    aggregate: boolean;
    stale: boolean;
  };
  const slices: Arc[] = [];
  const pushLevel = (
    items: Array<{ key: string; size: number; isDirectory: boolean; aggregate: boolean; stale: boolean; entry: MapEntry | null; children: ProjectedEntry[] }>,
    startAngle: number,
    endAngle: number,
    depth: number,
    band: HueBand,
  ) => {
    if (depth >= maxDepth || items.length === 0) return;
    const levelTotal = items.reduce((sum, item) => sum + item.size, 0) || items.length;
    const r0 = ringStart(depth);
    const r1 = r0 + ringThickness(depth);
    // Minimum readable width, converted to an angle at this ring's radius: the
    // same byte count is a fat wedge near the hub and a hair at the rim.
    const minAngle = minArcPixels / ((r0 + r1) / 2);
    const span = endAngle - startAngle;
    const drawn: typeof items = [];
    let foldedSize = 0;
    let foldedCount = 0;
    for (const item of items) {
      if (((item.size || 1) / levelTotal) * span >= minAngle) {
        drawn.push(item);
        continue;
      }
      foldedSize += item.size;
      foldedCount += 1;
    }
    // One grey block for the folded tail — but only if the block itself is wide
    // enough to see. Otherwise it is left out and the background shows through,
    // which is what the reference does rather than drawing hairs.
    if (foldedCount > 0 && (foldedSize / levelTotal) * span >= minAngle) {
      drawn.push({
        key: "folded:" + depth + ":" + slices.length,
        size: foldedSize,
        isDirectory: false,
        aggregate: true,
        stale: false,
        entry: null,
        children: [],
      });
    }
    items = drawn;
    let cursor = startAngle;
    items.forEach((item, index) => {
      const next = cursor + ((item.size || 1) / levelTotal) * (endAngle - startAngle);
      // Position within the level, which is position within the band: the wedge's
      // own hue is the middle of the slice it hands down to its children, so its
      // colour is the same before and after you drill into it.
      const from = (cursor - startAngle) / span;
      const to = (next - startAngle) / span;
      const own = subBand(from, to, band);
      const hue = own.center;
      // The separator is background, not a stroke: siblings are inset by half of
      // it on each side. Clamped so a wedge narrower than the separator collapses
      // to a hairline instead of inverting.
      const inset = Math.min(separatorArc / ((r0 + r1) / 2) / 2, (next - cursor) / 2.5);
      const geom: ArcGeom = { a0: cursor + inset, a1: next - inset, r0, r1 };
      slices.push({
        key: item.key,
        renderKey: item.key + ":" + depth + ":" + slices.length,
        depth,
        hue,
        path: arcPath(geom),
        geom,
        entry: item.entry,
        aggregate: item.aggregate,
        stale: item.stale,
      });
      if (item.isDirectory && item.children.length > 0) {
        pushLevel(item.children.map(projectedArc), cursor, next, depth + 1, own);
      }
      cursor = next;
      if (index === items.length - 1) cursor = endAngle;
    });
  };
  // At the scan root the wheel spans the volume's capacity, so the free space is
  // the arc that closes the circle rather than something drawn. Every level's
  // sequence ends at 3 o'clock (R-055).
  const volumeUsed = map?.volumeUsedBytes ?? 0;
  const volumeFree = map?.volumeFreeBytes ?? 0;
  const atScanRoot = map?.parent.parentId === 0 && volumeUsed > 0 && volumeFree > 0;
  const usedSweep = atScanRoot
    ? (volumeUsed / (volumeUsed + volumeFree)) * Math.PI * 2
    : Math.PI * 2;
  pushLevel(
    entries.map((entry) => ({
      key: entryKey(entry),
      size: entrySize(entry),
      isDirectory: entry.kind === "node" && entry.node.kind === "directory",
      aggregate: entry.kind !== "node",
      stale: entry.displayState === "stale",
      entry,
      children: (entry.children ?? []).filter(Boolean),
    })),
    sunburstEndAngle - usedSweep,
    sunburstEndAngle,
    0,
    hueRange,
  );

  // One key per level, so the morph runs on navigation and not on hover.
  const levelKey = map ? map.snapshotId + ":" + map.parent.id + ":" + map.offset : "";
  const sliceSnapshot = slices;

  // useLayoutEffect, not useEffect: React has already committed the target
  // geometry to the DOM, so the source geometry has to be written back before
  // the browser paints or the first frame shows the destination.
  useLayoutEffect(() => {
    const previous = paintedGeom.current;
    const previousSlices = paintedSlices.current;
    const commit = () => {
      const next = new Map<string, ArcGeom>();
      for (const slice of sliceSnapshot) {
        if (!next.has(slice.key)) next.set(slice.key, slice.geom);
      }
      paintedGeom.current = next;
      previousParentKey.current = map ? "node:" + map.parent.id : null;
      paintedSlices.current = sliceSnapshot.map((slice) => ({
        key: slice.key,
        geom: slice.geom,
        color: slice.aggregate ? sunburstAggregate : sliceColor(slice.hue, baseDepth + slice.depth + 1),
      }));
      setGhosts([]);
    };
    if (morphFrame.current !== null) {
      cancelAnimationFrame(morphFrame.current);
      morphFrame.current = null;
    }
    // Nothing to tween from on the first level, and no tween worth the stutter
    // past the ceiling.
    if (previous.size === 0 || sliceSnapshot.length === 0 || sliceSnapshot.length > morphArcCeiling) {
      commit();
      return;
    }
    // An arc that also existed in the previous level starts where it was; a
    // genuinely new one starts as a zero-thickness sliver at its own inner edge,
    // so it grows outward instead of appearing.
    const tweens = sliceSnapshot.map((slice) => {
      const from = previous.get(slice.key) ?? { a0: slice.geom.a0, a1: slice.geom.a1, r0: slice.geom.r0, r1: slice.geom.r0 };
      return { renderKey: slice.renderKey, from, to: slice.geom };
    });

    // Where the departing arcs go. Drilling in maps the clicked wedge's angular
    // span onto the whole circle, so applying that same map to the rest of the
    // old level sweeps it past 0 or 2π — out of the wheel, which is what the
    // original does. Going up is the inverse: everything folds back into the
    // wedge it came from.
    const ringShift = ringWidth + ringGap(0);
    const focusIn = map ? previous.get("node:" + map.parent.id) : undefined;
    const focusOut = previousParentKey.current
      ? sliceSnapshot.find((slice) => slice.key === previousParentKey.current)?.geom
      : undefined;
    const sweep = (geom: ArcGeom): ArcGeom | null => {
      if (focusIn && focusIn.a1 > focusIn.a0) {
        const scale = (Math.PI * 2) / (focusIn.a1 - focusIn.a0);
        return {
          a0: (geom.a0 - focusIn.a0) * scale,
          a1: (geom.a1 - focusIn.a0) * scale,
          r0: Math.max(innerRadius, geom.r0 - ringShift),
          r1: Math.max(innerRadius, geom.r1 - ringShift),
        };
      }
      if (focusOut && focusOut.a1 > focusOut.a0) {
        const scale = (focusOut.a1 - focusOut.a0) / (Math.PI * 2);
        return {
          a0: focusOut.a0 + geom.a0 * scale,
          a1: focusOut.a0 + geom.a1 * scale,
          r0: geom.r0 + ringShift,
          r1: geom.r1 + ringShift,
        };
      }
      return null;
    };
    const live = new Set(sliceSnapshot.map((slice) => slice.key));
    const departing: Array<{ renderKey: string; color: string; from: ArcGeom; to: ArcGeom }> = [];
    previousSlices.forEach((slice, index) => {
      if (live.has(slice.key)) return;
      const to = sweep(slice.geom);
      if (!to) return;
      departing.push({ renderKey: "ghost:" + index, color: slice.color, from: slice.geom, to });
    });
    setGhosts(departing);
    if (departing.length > 0) {
      // The fade has to start on a later frame than the mount, or the browser
      // has no starting value to transition from. Its duration follows
      // morphDuration so the two never drift apart.
      requestAnimationFrame(() => {
        const group = ghostGroupRef.current;
        if (group) group.style.opacity = "0";
      });
    }
    morphState.current = {
      tweens: tweens.concat(departing.map((ghost) => ({ renderKey: ghost.renderKey, from: ghost.from, to: ghost.to }))),
      started: performance.now(),
    };
    paintMorph(morphState.current, 0, morphRefs.current);
    const step = () => {
      const state = morphState.current;
      if (!state) return;
      const progress = Math.min(1, (performance.now() - state.started) / morphDuration);
      paintMorph(state, progress, morphRefs.current);
      if (progress < 1) {
        morphFrame.current = requestAnimationFrame(step);
        return;
      }
      morphFrame.current = null;
      morphState.current = null;
      commit();
    };
    morphFrame.current = requestAnimationFrame(step);
    return () => {
      if (morphFrame.current !== null) {
        cancelAnimationFrame(morphFrame.current);
        morphFrame.current = null;
      }
      // Interrupted mid-tween: record where the arcs actually are, so the next
      // navigation starts from what the user can see rather than from a level
      // that was never painted.
      morphState.current = null;
      commit();
    };
  }, [levelKey]);

  // Deliberately dependency-free: it must run after *every* render, because a
  // re-render mid-morph resets every `d` to the destination.
  useLayoutEffect(() => {
    const state = morphState.current;
    if (!state) return;
    paintMorph(state, Math.min(1, (performance.now() - state.started) / morphDuration), morphRefs.current);
  });

  useEffect(() => {
    if (focusedKey && pathRefs.current[focusedKey]) pathRefs.current[focusedKey]?.focus();
  }, [focusedKey]);

  // The hub shows the current total on two lines. At the scan root it stays
  // white; once drilled in it takes the hue of the node being viewed.
  // Same rule as the heading: at the root the hub shows the volume's used bytes,
  // which is what the arcs add up to once the balancing entry is included.
  const hubTotal = (map?.parent.parentId === 0 && (map?.volumeUsedBytes ?? 0) > 0
    ? map!.volumeUsedBytes
    : map?.parent.ownedAllocated) ?? 0;
  const centerParts = formatBytes(hubTotal).split(" ");
  const hubColor = hueRange.center === rootHueBand.center && hueRange.width === rootHueBand.width
    ? "#f4f4f6"
    : sliceColor(hueRange.center, baseDepth);

  return (
    <div className="sunburst-wrap" aria-label="空间图">
      <svg className="sunburst" viewBox="0 0 600 600" role="img">
        <g transform="translate(300 300)">
          {/* Departing arcs: drawn under the live ones, never interactive. */}
          {ghosts.length > 0 && (
            <g
              className="sunburst-ghosts"
              aria-hidden="true"
              ref={ghostGroupRef}
              style={{ opacity: 1, transition: "opacity " + morphDuration + "ms ease-out" }}
            >
              {ghosts.map((ghost) => (
                <path
                  key={ghost.renderKey}
                  ref={(node) => {
                    if (node) morphRefs.current.set(ghost.renderKey, node);
                    else morphRefs.current.delete(ghost.renderKey);
                  }}
                  className="sunburst-slice is-ghost"
                  d={arcPath(ghost.from)}
                  fill={ghost.color}
                />
              ))}
            </g>
          )}
          {slices.map(({ entry, key, renderKey, depth, path, hue, aggregate, stale }) => {
            // Only current-level arcs are interactive: a projected descendant
            // carries no path, so it can neither be activated nor collected
            // (ADR-0048, ADR-0017 §2).
            const interactive = entry !== null;
            const canActivate = interactive && (!aggregate || depth === 0);
            const draggable = interactive && entry.kind === "node" && hasCapability(entry, "collect");
            const selected = key === selectedKey;
            const focused = key === focusedKey;
            const hovered = key === hoveredKey;
            const color = aggregate ? sunburstAggregate : sliceColor(hue, baseDepth + depth + 1);
            return (
              <path
                key={renderKey}
                ref={(node) => {
                  pathRefs.current[key] = node;
                  if (node) morphRefs.current.set(renderKey, node);
                  else morphRefs.current.delete(renderKey);
                }}
                d={path}
                role={interactive ? "button" : "presentation"}
                tabIndex={interactive ? 0 : -1}
                onPointerDown={(event) => { if (draggable && entry) onDragEntry(entry, event); }}
                className={
                  "sunburst-slice" +
                  " depth-" + depth +
                  (selected ? " is-selected" : "") +
                  (focused ? " is-focused" : "") +
                  (hovered ? " is-hovered" : "") +
                  (aggregate ? " is-aggregate" : "") +
                  (stale ? " is-stale" : "") +
                  (interactive ? "" : " is-projected")
                }
                fill={color}
                aria-label={entry ? entry.name + "，" + formatBytes(entrySize(entry)) : undefined}
                onPointerEnter={() => { if (entry) onHover(entry); }}
                onPointerLeave={() => { if (entry) onHover(null); }}
                onFocus={() => { if (entry) onFocus(entry); }}
                onClick={(event) => {
                  if (!canActivate || !entry) return;
                  if (event.metaKey || event.ctrlKey) {
                    event.preventDefault();
                    onReveal(entry);
                    return;
                  }
                  onActivate(entry);
                }}
                onKeyDown={(event) => {
                  if (!entry) return;
                  if (event.key === "Enter") {
                    event.preventDefault();
                    event.stopPropagation();
                    if (canActivate) onActivate(entry);
                  } else if (event.key === " " && hasCapability(entry, "preview")) {
                    event.preventDefault();
                    onPreview(entry);
                  }
                }}
              />
            );
          })}
          <g className="sunburst-hub" role="button" tabIndex={0} onClick={onGoParent} onKeyDown={(event) => { if (event.key === "Enter") onGoParent(); }}>
            {/* A hole, not a disc. The reference's hub interior is the page
                background to the byte and has no ring at its edge (R-060). The
                circle stays as the click target for going up a level, which is
                why it is transparent rather than unpainted — `fill: none` would
                not hit-test. */}
            <circle className="sunburst-center" r={innerRadius} />
            <text className="sunburst-center-label" textAnchor="middle" style={{ fontSize: hubLabelSize }} y={hubLabelBaseline} fill={hubColor}>{centerParts[0]}</text>
            <text className="sunburst-center-label" textAnchor="middle" style={{ fontSize: hubLabelSize }} y={hubLabelBaseline + hubLabelLead} fill={hubColor}>{centerParts[1] ?? ""}</text>
          </g>
        </g>
      </svg>
      {!map && <div className="chart-placeholder">完成一次扫描后显示空间图</div>}
    </div>
  );
}

function VolumeTile({
  source,
  hasResult,
  scanning,
  scanStatus,
  scanLocked,
  onScan,
  onView,
  onCancel,
  onForget,
}: {
  source: StorageSourceOverview;
  hasResult: boolean;
  scanning: boolean;
  scanStatus: ScanStatus | null;
  scanLocked: boolean;
  onScan: (path: string) => void;
  onView: () => void;
  onCancel: () => void;
  onForget: () => void;
}) {
	const members = source.members ?? [];
	const disabled = !source.scannable || (scanLocked && !scanning);
	const sourceTotal = source.totalBytes;
	const sourceUsed = source.usedBytes;
	const ratio = sourceTotal ? Math.min(100, (sourceUsed / sourceTotal) * 100) : 0;
	// The original names the volume, then its capacity and role — not its path,
	// and not a permission badge. Permission problems get their own alert row.
	const roleLabel = source.path === "/" ? "启动盘" : source.kind === "external" ? "外部卷" : "卷";
	const subtitle = members.length > 1
		? `${formatBytes(sourceTotal)} ${roleLabel} · ${members.length} 个技术卷`
		: `${formatBytes(sourceTotal)} ${roleLabel}`;
	const capacityLabel = `${formatBytes(sourceUsed)} 已用 · 剩余 ${formatBytes(source.freeBytes)} · 共 ${formatBytes(sourceTotal)}`;
	// ADR-0050 §2: phase, counts, tree bytes and elapsed time are not visible
	// chrome — the original shows none of them — but they must stay available, so
	// they live in the progress bar's accessible label. No aria-valuenow: the
	// total is unknown until the walk ends (ADR-0027, ADR-0050 §3).
	const scanLabel = scanStatus
		? `${phaseLabels[scanStatus.phase] ?? "扫描中"}：已处理 ${scanStatus.nodes.toLocaleString()} 项、`
			+ `${scanStatus.files.toLocaleString()} 个文件、文件树占用 ${formatBytes(scanStatus.bytes)}`
		: "扫描中";
	// ADR-0053: the fill's width is a real ratio — bytes accounted for over the
	// volume's used bytes, both measured. Without a denominator the bar stays
	// indeterminate instead of guessing one. It tops out short of 100% because
	// hidden space is only computable at the end, so the terminal state fills it.
	// The denominator this row already displays, in preference to the one the
	// snapshot records. Two reasons: it is on screen from the first frame, so a
	// volume scan never has to fall back to the indeterminate style; and it is the
	// group's used bytes, while the snapshot records only the volume whose path
	// matched the root — on an APFS volume group that is the smaller of the two.
	const scanDenominator = sourceUsed > 0 ? sourceUsed : (scanStatus?.volumeUsedBytes ?? 0);
	// The bar stops a few percent short of the end and that is correct, not a
	// stall: hidden space counts towards the volume's used bytes and is not
	// walkable, so the counted total cannot reach it (ADR-0052 §5). There is no
	// "fill to 100% on completion" branch because there is no frame to show it in —
	// the row stops rendering the bar the moment the state leaves "running".
	const scanFraction = scanDenominator > 0
		? Math.max(0, Math.min(1, (scanStatus?.countedBytes ?? 0) / scanDenominator))
		: null;
	const scanProgressLabel = scanFraction === null
		? scanLabel
		: `${scanLabel}；已统计 ${formatBytes(scanStatus?.countedBytes ?? 0)}，占卷已用 ${formatBytes(scanDenominator)} 的 ${(scanFraction * 100).toFixed(0)}%`;
	return (
	  <article className={"volume-row" + (source.scannable ? "" : " is-disabled")}>
	    <span className="volume-icon" aria-hidden="true">
	      <svg viewBox="0 0 30 30">
	        <rect className="disk-body" x="3.5" y="7" width="23" height="16" rx="2.6" />
	        <rect className="disk-gloss" x="4.5" y="8" width="21" height="6.5" rx="1.8" />
	        <circle className="disk-hub" cx="15" cy="15" r="4" />
	        <circle className="disk-pin" cx="15" cy="15" r="1.1" />
	      </svg>
	    </span>
	    <div className="volume-title">
	      <strong>{source.name}</strong>
	      <span title={source.message}>{subtitle}</span>
	    </div>
	    <div className="volume-meter">
	      <div
	        className={"meter-track" + (scanning ? (scanFraction === null ? " is-scanning is-indeterminate" : " is-scanning") : "")}
	        role={scanning ? "progressbar" : undefined}
	        aria-label={scanning ? scanProgressLabel : capacityLabel}
	        aria-valuetext={scanning ? scanProgressLabel : undefined}
	        aria-valuemin={scanning && scanFraction !== null ? 0 : undefined}
	        aria-valuemax={scanning && scanFraction !== null ? scanDenominator : undefined}
	        aria-valuenow={scanning && scanFraction !== null ? (scanStatus?.countedBytes ?? 0) : undefined}
	      >
	        {/* Two elements rather than one with two meanings. The capacity fill and
	            the scan progress are different quantities, and driving both from a
	            single span made the bar animate from the disk's fill level down to
	            zero the moment scanning began. Only one is ever visible: showing the
	            capacity fill underneath the progress just read as a stray band, and
	            scaling the progress to the capacity extent so the two lined up made
	            the bar stop short of the track's end, which reads as stalling. */}
	        {scanning ? (
	          <span
	            className="meter-scan"
	            style={scanFraction === null ? undefined : { width: (scanFraction * 100).toFixed(2) + "%" }}
	          />
	        ) : (
	          <span className="meter-used" style={{ width: ratio + "%" }} />
	        )}
	      </div>
	      <div className={"meter-caption" + (scanning ? " is-scanning" : "")}>
	        {scanning ? <em>扫描中…</em> : <b>{formatBytes(source.freeBytes)}</b>}
	      </div>
	    </div>
	    <div className="volume-action">
	      <button className="volume-action-main" onClick={scanning ? onCancel : hasResult ? onView : () => onScan(source.path)} disabled={disabled}>
	        {scanning ? "取消" : hasResult ? "查看" : "扫描"}
	      </button>
	      {!scanning && (
	        <button
	          className="volume-action-menu"
	          style={{ "--custom-contextmenu": volumeMenuName } as CSSProperties}
	          onClick={(event) => void openVolumeMenu(event.currentTarget, source.id, hasResult)}
	          disabled={disabled}
	          aria-label={source.name + "操作菜单"}
	          aria-haspopup="menu"
	        >
	          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="M2 4 L5 7 L8 4" /></svg>
	        </button>
	      )}
	    </div>
    </article>
  );
}

function DirectoryList({
  entryColors_,
  parentDotColor,
  parent,
  entries,
  total,
  map,
  hoveredKey,
  focusedKey,
  selectedKey,
  contextEntry,
  inCollector,
  onHover,
  onFocus,
  onActivate,
  onPreview,
  onReveal,
  onEnter,
  onCollect,
}: {
  entryColors_: Record<string, string>;
  // null at the scan root, where the reference shows no dot beside the volume
  // name; below it the dot carries the node's own colour (R-060 SS3.7).
  parentDotColor: string | null;
  parent: NodeView | null;
  entries: MapEntry[];
  total: number;
  map: MapResult | null;
  hoveredKey: string | null;
  focusedKey: string | null;
  selectedKey: string | null;
  contextEntry: MapEntry | null;
  inCollector: boolean;
  onHover: (entry: MapEntry | null) => void;
  onFocus: (entry: MapEntry) => void;
  onActivate: (entry: MapEntry) => void;
  onPreview: (entry: MapEntry) => void;
  onReveal: (entry: MapEntry) => void;
  onEnter: (entry: MapEntry) => void;
  onCollect: (entry: MapEntry) => void;
}) {
  return (
    <aside className="directory-panel" data-testid="directory-list">
      <div className="directory-heading">
        {parentDotColor && <span className="directory-parent-dot" style={{ background: parentDotColor }} />}
        <h2>{parent ? crumbLabel(parent.path, parent.parentId === 0 ? 0 : 1) : "当前目录"}</h2>
        <strong>{formatBytes(total)}</strong>
      </div>

      <div className="directory-list" role="listbox" aria-label="当前目录内容">
        {entries.length === 0 ? (
          <div className="directory-empty">当前目录没有可显示的项目。</div>
        ) : entries.map((entry, index) => {
          const key = entryKey(entry);
          const isSelected = key === selectedKey;
          const isFocused = key === focusedKey;
          const isHovered = key === hoveredKey;
          const kindClass = entry.kind === "aggregate" || entry.kind === "virtual" ? "virtual" : entry.node.kind === "directory" ? "directory" : "file";
          return (
            <div
              key={key}
              className={"directory-row" + (isSelected ? " is-selected" : "") + (isFocused ? " is-focused" : "") + (isHovered ? " is-hovered" : "")}
              role="option"
              aria-selected={isSelected}
              tabIndex={isFocused || (index === 0 && !focusedKey) ? 0 : -1}
              draggable={entry.kind === "node" && hasCapability(entry, "collect")}
              onMouseEnter={() => onHover(entry)}
              onMouseLeave={() => onHover(null)}
              onFocus={() => onFocus(entry)}
              onClick={() => onActivate(entry)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  onActivate(entry);
                } else if (event.key === " " && hasCapability(entry, "preview")) {
                  event.preventDefault();
                  onPreview(entry);
                }
              }}
              onDragStart={(event) => {
                event.dataTransfer.setData("application/marmot-entry", JSON.stringify(entry));
                event.dataTransfer.effectAllowed = "copy";
              }}
              title={entryPath(entry)}
            >
              <span className={"directory-dot " + kindClass} style={entryColors_[entryKey(entry)] ? { background: entryColors_[entryKey(entry)] } : undefined} />
              <span className="directory-name">{entry.name}</span>
              {/* Always emitted, even when empty: a conditional cell moved the
                  size out of the last grid column, which left the rows without a
                  count 22px short of the ones with it. The reference has one
                  right edge for every row. */}
              <span className="directory-tag">{entry.kind === "aggregate" ? entry.count.toLocaleString() + " 项" : ""}</span>
              <span className="directory-size">{formatBytes(entrySize(entry))}</span>
            </div>
          );
        })}
      </div>
      {/* Root level only: the free-space rows the original shows under the
          entries. Both rows read the same value until we have a separate source
          for purgeable space — inventing a difference would be a fabrication
          (ADR-0052 §5). */}
      {parent?.parentId === 0 && (map?.volumeFreeBytes ?? 0) > 0 && (
        <div className="directory-summary">
          <div className="summary-row">
            <span className="summary-mark">●</span>
            <span className="summary-name">实际可用空间</span>
            <span className="summary-size">{formatBytes(map!.volumeFreeBytes)}</span>
          </div>
          <div className="summary-row">
            <span className="summary-mark">~</span>
            <span className="summary-name">实际可用 + 可清除</span>
            <span className="summary-size">{formatBytes(map!.volumeFreeBytes)}</span>
          </div>
        </div>
      )}
      {/* The result is incomplete -- permissions, cloud placeholders, a cancelled
          run. DDD invariant 5 requires that be visible, so it cannot simply be
          dropped; it moved out of the heading, where it sat as a badge next to
          the volume name, down to where the unaccounted space is already
          explained. */}
      {(map?.confidence ?? parent?.confidence) === "partial" && (
        <p className="directory-partial">部分结果：有目录未能完整读取，未计入的空间归入隐藏空间。</p>
      )}
      {contextEntry?.displayState === "stale" && (
        <p className="context-warning">{displayStateLabel(contextEntry.displayState)}，当前对象需要重新读取。</p>
      )}
    </aside>
  );
}

export default function App() {
  const [permission, setPermission] = useState<PermissionStatus | null>(null);
  const [storageSources, setStorageSources] = useState<StorageSourceOverview[]>([]);
  const [root, setRoot] = useState(defaultRoot);
  const [status, setStatus] = useState<ScanStatus | null>(null);
  const [cachedStatus, setCachedStatus] = useState<ScanStatus | null>(null);
  const [map, setMap] = useState<MapResult | null>(null);
  const [pages, setPages] = useState<Page[]>([]);
  const [pageIndex, setPageIndex] = useState(-1);
  const [hoveredEntry, setHoveredEntry] = useState<MapEntry | null>(null);
  const [focusedEntry, setFocusedEntry] = useState<MapEntry | null>(null);
  const [selectedEntry, setSelectedEntry] = useState<MapEntry | null>(null);
  const [staleEntry, setStaleEntry] = useState<MapEntry | null>(null);
  const [collector, setCollector] = useState<MapEntry[]>([]);
  const [collectorOpen, setCollectorOpen] = useState(false);
  const [plan, setPlan] = useState<CleanupPlan | null>(null);
  const [validation, setValidation] = useState<CleanupValidation | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [mapBusy, setMapBusy] = useState(false);
  // The hub takes the colour the current node had in its parent's wheel, so a
  // drill-down keeps its identity. Unknown after a restore or a back step.
  const [centerColor, setCenterColor] = useState<string | null>(null);
  const [dragGhost, setDragGhost] = useState<{ label: string; x: number; y: number } | null>(null);
  // Delete runs on a countdown that can be stopped, the way the reference does.
  // The safety chain is unchanged: the plan is validated, re-validated at the
  // end of the countdown, and the items go to the Trash, never deleted outright.
  const [countdown, setCountdown] = useState<number | null>(null);
  const countdownTimer = useRef<number | null>(null);
  const collectorRef = useRef<HTMLElement | null>(null);
  const mapRequest = useRef(0);
  const refreshTimer = useRef<number | undefined>(undefined);
  const mapRef = useRef<MapResult | null>(null);
  const pageRef = useRef<Page | null>(null);
  const navigationRef = useRef({ pages: [] as Page[], index: -1 });
  const rootRef = useRef(defaultRoot);
  const statusRef = useRef<ScanStatus | null>(null);
  const storageSourcesRef = useRef<StorageSourceOverview[]>([]);
  const loadMapRef = useRef<((target: Page, mode?: NavigationMode, targetIndex?: number, compact?: boolean) => Promise<boolean>) | null>(null);

  const scanActive = status?.state === "running";
  // No snapshot is written any more, so there is no second terminal state to wait
  // for and nothing to warn about (ADR-0055). The flip side is that the result
  // exists only while this process does.
  const showResult = Boolean(status?.snapshotId && status.state && browsableScanStates.has(status.state) && map);
  const currentPage = pages[pageIndex] ?? null;
  const currentParent = map?.parent ?? null;
  const entries = useMemo(
    () => foldSmallEntries((map?.entries ?? []).filter(Boolean), map?.parent.ownedAllocated ?? 0),
    [map],
  );
  const currentKey = staleEntry ? entryKey(staleEntry) : null;
  const inspectorEntry = staleDisplayEntry(hoveredEntry ?? focusedEntry ?? selectedEntry, currentKey);
  const selectedKey = selectedEntry ? entryKey(selectedEntry) : null;
  const focusedKey = focusedEntry ? entryKey(focusedEntry) : null;
  const hoveredKey = hoveredEntry ? entryKey(hoveredEntry) : null;
  const hueRange: HueBand = currentPage?.hue ?? rootHueBand;
  // Depth of the level on screen within the scanned tree: 0 is the volume root.
  // Colour is a function of this, not of the ring index (ADR-0059 SS1b).
  const baseDepth = Math.max(pageIndex, 0);
  const levelColors = useMemo(() => entryColors(entries, hueRange, baseDepth), [entries, hueRange, baseDepth]);
  const inspectedInCollector = inspectorEntry ? collector.some((item) => entryKey(item) === entryKey(inspectorEntry)) : false;
  const collectorBytes = collector.reduce((sum, item) => sum + entrySize(item), 0);
  // At the root the displayed total is the volume's used bytes, so the number in
  // the hub is the number the entries add up to — the tree total alone excludes
  // the balancing entry and would not add up (ADR-0052 §4).
  const mapTotal = (map?.parent.parentId === 0 && (map?.volumeUsedBytes ?? 0) > 0
    ? map!.volumeUsedBytes
    : map?.parent.ownedAllocated) ?? status?.bytes ?? 0;
  const mapConfidence = map?.confidence || map?.parent.confidence || "unknown";

  mapRef.current = map;
  pageRef.current = currentPage;
  navigationRef.current = { pages, index: pageIndex };
  rootRef.current = root;
  statusRef.current = status;
  storageSourcesRef.current = storageSources;

  async function loadStorageSources() {
    try {
      setStorageSources((await MarmotService.GetStorageSources()) ?? []);
    } catch (error) {
      setNotice(String(error));
    }
  }

  function commitNavigation(target: Page, mode: NavigationMode, targetIndex?: number) {
    const navigation = navigationRef.current;
    if (mode === "travel" && targetIndex !== undefined) {
      setPageIndex(targetIndex);
      return;
    }
    if (mode === "push") {
      let nextPages = navigation.pages.slice(0, navigation.index + 1).concat(target);
      if (nextPages.length > maxHistory) nextPages = nextPages.slice(nextPages.length - maxHistory);
      setPages(nextPages);
      setPageIndex(nextPages.length - 1);
      return;
    }
    if (navigation.pages.length === 0) {
      setPages([target]);
      setPageIndex(0);
      return;
    }
    const nextPages = navigation.pages.slice();
    nextPages[navigation.index] = target;
    setPages(nextPages);
    setPageIndex(navigation.index);
  }

  async function loadMap(target: Page, mode: NavigationMode = "replace", targetIndex?: number, compact = false): Promise<boolean> {
    if (target.snapshotId <= 0 || target.parentId <= 0) return false;
    const request = ++mapRequest.current;
    setMapBusy(true);
    try {
      const next = await MarmotService.GetMap({ snapshotId: target.snapshotId, parentId: target.parentId, limit: pageSize, offset: target.offset, measure: "owned_allocated", depth: compact ? 1 : sunburstGeometry.maxDepth - 1, projectionLimit: compact ? 400 : 2000, minSweeps: projectionMinSweeps(compact ? 1 : sunburstGeometry.maxDepth - 1) });
      if (request !== mapRequest.current) return false;
      const nextEntries = next.entries ?? [];
      setMap(next);
      const resolvedTarget = { ...target, offset: next.offset };
      if (mode !== "replace") {
        setHoveredEntry(null);
        setFocusedEntry(firstEntry(next));
        setSelectedEntry(null);
      } else {
        setHoveredEntry((current) => current ? nextEntries.find((entry) => entryKey(entry) === entryKey(current)) ?? null : null);
        setFocusedEntry((current) => current ? nextEntries.find((entry) => entryKey(entry) === entryKey(current)) ?? firstEntry(next) : firstEntry(next));
        setSelectedEntry((current) => current ? nextEntries.find((entry) => entryKey(entry) === entryKey(current)) ?? null : null);
      }
      setStaleEntry((current) => current ? nextEntries.find((entry) => entryKey(entry) === entryKey(current)) ?? null : null);
      commitNavigation(resolvedTarget, mode, targetIndex);
      return true;
    } catch (error) {
      if (request === mapRequest.current) setNotice(String(error));
      return false;
    } finally {
      if (request === mapRequest.current) setMapBusy(false);
    }
  }

  loadMapRef.current = loadMap;

  // The map is loaded once per scan, when it first becomes browsable. There is
  // only one terminal event now (ADR-0055), and the answer cannot change after
  // it, so re-querying would only make the wheel re-render.
  function scheduleMapRefresh(event: ScanProgress) {
    const currentMap = mapRef.current;
    if (event.snapshotId <= 0 || (currentMap && event.snapshotId === currentMap.snapshotId)) return;
    if (event.state === "running" || !browsableScanStates.has(event.state)) return;
    if (refreshTimer.current !== undefined) {
      window.clearTimeout(refreshTimer.current);
      refreshTimer.current = undefined;
    }
    const current = pageRef.current?.snapshotId === event.snapshotId
      ? pageRef.current
      : rootPage(event.snapshotId, event.root || rootRef.current);
    const target = { ...current, snapshotId: event.snapshotId };
    refreshTimer.current = window.setTimeout(() => {
      refreshTimer.current = undefined;
      if (loadMapRef.current) void loadMapRef.current(target, "replace", undefined, false);
    }, 0);
  }

  useEffect(() => {
    void loadStorageSources();
    const savedTaskId = window.localStorage.getItem("marmot.scanTaskId");
    if (savedTaskId) {
      MarmotService.GetScanStatus(savedTaskId)
        .then((next) => {
          setRoot(next.root);
          if (next.state === "running") {
            setStatus(next);
          } else if (next.snapshotId > 0) {
            setCachedStatus(next);
          } else {
            window.localStorage.removeItem("marmot.scanTaskId");
          }
        })
        .catch(() => window.localStorage.removeItem("marmot.scanTaskId"));
    }
    MarmotService.GetPermissionStatus().then(setPermission).catch((error: unknown) => setNotice(String(error)));
    const off = Events.On("scan-progress", (event: { data: ScanProgress }) => {
      setStatus(statusFromProgress(event.data));
      scheduleMapRefresh(event.data);
      if (event.data.state !== "running") void loadStorageSources();
    });
    // The native row menu only reports which item was picked; the action itself
    // runs here, through the same calls the buttons use (ADR-0051 §4).
    const offMenu = Events.On("volume-menu", (event: { data: { sourceId: string; action: string } }) => {
      void runVolumeMenuAction(event.data.sourceId, event.data.action);
    });
    return () => {
      off();
      offMenu();
      if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
    };
  }, []);

  const sourceRows = Math.max(1, storageSources.length);
  const sourceAlert = Boolean(permission && permission.state !== "available");
  useEffect(() => {
    const height = showResult
      ? resultWindowSize.height
      : sourceWindowSize.height + (sourceRows - 1) * sourceRowHeight + (sourceAlert ? 34 : 0);
    try {
      void Window.SetSize(resultWindowSize.width, height).catch(() => undefined);
    } catch {
      // The ordinary browser preview does not expose the Wails window bridge.
    }
  }, [showResult, sourceRows, sourceAlert]);

  async function startScan(nextRoot = root) {
    setBusy(true);
    setNotice("");
    mapRequest.current += 1;
    if (refreshTimer.current !== undefined) {
      window.clearTimeout(refreshTimer.current);
      refreshTimer.current = undefined;
    }
    setMap(null);
    setPages([]);
    setPageIndex(-1);
    setHoveredEntry(null);
    setFocusedEntry(null);
    setSelectedEntry(null);
    setStaleEntry(null);
    setCollector([]);
    setPlan(null);
    setValidation(null);
    try {
      const next = await MarmotService.StartScan({ root: nextRoot });
      window.localStorage.setItem("marmot.scanTaskId", next.taskId);
      setCachedStatus(null);
      setRoot(next.root);
      setStatus(next);
    } catch (error) {
      setNotice(String(error));
    } finally {
      setBusy(false);
    }
  }

  async function chooseFolder() {
    try {
      const selected = await Dialogs.OpenFile({
        Title: "扫描文件夹",
        ButtonText: "扫描此文件夹",
        CanChooseDirectories: true,
        CanChooseFiles: false,
        AllowsMultipleSelection: false,
        Directory: root,
      });
      if (typeof selected !== "string" || selected.length === 0) return;
      setRoot(selected);
      await startScan(selected);
    } catch (error) {
      setNotice(String(error));
    }
  }

  async function cancelScan() {
    if (!status) return;
    try {
      setStatus(await MarmotService.CancelScan(status.taskId));
    } catch (error) {
      setNotice(String(error));
    }
  }

  function viewCachedResult() {
    if (!cachedStatus || cachedStatus.snapshotId <= 0) return;
    setStatus(cachedStatus);
    setCachedStatus(null);
    setRoot(cachedStatus.root);
    void loadMap(rootPage(cachedStatus.snapshotId, cachedStatus.root));
  }

  async function goToPage(target: Page, mode: NavigationMode, targetIndex?: number) {
    await loadMap(target, mode, targetIndex);
  }

  function openDirectory(entry: MapEntry) {
    const node = entryNode(entry);
    if (!node || node.kind !== "directory" || !hasCapability(entry, "enter") || !currentPage) return;
    const childBand = entryBands(entries, currentPage.hue)[entryKey(entry)] ?? currentPage.hue;
    const target: Page = {
      snapshotId: currentPage.snapshotId,
      parentId: node.id,
      path: node.path,
      offset: 0,
      crumbs: currentPage.crumbs.concat({ id: node.id, path: node.path, hue: childBand }),
      hue: childBand,
    };
    void goToPage(target, "push");
  }

  function expandEntry(entry: MapEntry) {
    if (!hasCapability(entry, "enter") || !currentPage) return;
    if (entry.kind !== "aggregate" || entry.virtualType !== "smaller_objects") {
      setNotice("该解释对象没有可展开的文件节点");
      return;
    }
    const visibleNodes = entries.filter((item) => item.kind === "node").length;
    void goToPage({ ...currentPage, offset: currentPage.offset + Math.max(1, visibleNodes) }, "replace");
  }

  // SVG paths cannot start an HTML5 drag, so the wheel drags with pointer
  // events: track the move, show a ghost, and collect on release over the ring.
  function beginSliceDrag(entry: MapEntry, event: ReactPointerEvent) {
    if (event.button !== 0) return;
    const startX = event.clientX;
    const startY = event.clientY;
    let dragging = false;
    const move = (moveEvent: PointerEvent) => {
      if (!dragging && Math.hypot(moveEvent.clientX - startX, moveEvent.clientY - startY) < 6) return;
      dragging = true;
      setDragGhost({ label: entry.name, x: moveEvent.clientX, y: moveEvent.clientY });
    };
    const finish = (upEvent: PointerEvent) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", finish);
      setDragGhost(null);
      if (!dragging) return;
      const target = collectorRef.current?.getBoundingClientRect();
      if (!target) return;
      const inside = upEvent.clientX >= target.left && upEvent.clientX <= target.right
        && upEvent.clientY >= target.top && upEvent.clientY <= target.bottom;
      if (inside) toggleCollector(entry);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", finish);
  }

  function activateEntry(entry: MapEntry) {
    setFocusedEntry(entry);
    setSelectedEntry(entry);
    if (entry.kind === "node" && entry.node.kind === "directory") {
      setCenterColor(levelColors[entryKey(entry)] ?? null);
      openDirectory(entry);
    } else if (entry.kind !== "node") {
      expandEntry(entry);
    }
  }

  function navigateToHistory(index: number) {
    if (index < 0 || index >= pages.length || index === pageIndex) return;
    void goToPage(pages[index], "travel", index);
  }

  function goParent() {
    if (!currentPage || currentPage.crumbs.length <= 1) return;
    const parentCrumb = currentPage.crumbs[currentPage.crumbs.length - 2];
    for (let index = pageIndex - 1; index >= 0; index -= 1) {
      const candidate = pages[index];
      if (candidate.snapshotId === currentPage.snapshotId && candidate.parentId === parentCrumb.id && candidate.path === parentCrumb.path) {
        navigateToHistory(index);
        return;
      }
    }
    void goToPage({
      snapshotId: currentPage.snapshotId,
      parentId: parentCrumb.id,
      path: parentCrumb.path,
      offset: 0,
      crumbs: currentPage.crumbs.slice(0, -1),
      hue: parentCrumb.hue,
    }, "push");
  }

  function jumpToBreadcrumb(index: number) {
    if (!currentPage || index < 0 || index >= currentPage.crumbs.length) return;
    const crumb = currentPage.crumbs[index];
    for (let page = pageIndex; page >= 0; page -= 1) {
      const candidate = pages[page];
      if (candidate.parentId === crumb.id && candidate.path === crumb.path && candidate.snapshotId === currentPage.snapshotId) {
        navigateToHistory(page);
        return;
      }
    }
    void goToPage({
      snapshotId: currentPage.snapshotId,
      parentId: crumb.id,
      path: crumb.path,
      offset: 0,
      crumbs: currentPage.crumbs.slice(0, index + 1),
      hue: crumb.hue,
    }, "push");
  }

  function refreshCurrent() {
    if (currentPage) void goToPage(currentPage, "replace");
  }

  async function runVolumeMenuAction(sourceID: string, action: string): Promise<void> {
    const source = storageSourcesRef.current.find((item) => item.id === sourceID);
    if (!source) return;
    if (action === "rescan") {
      setRoot(source.path);
      await startScan(source.path);
      return;
    }
    if (action === "forget") {
      forgetResult();
      return;
    }
    if (action === "reveal") {
      try {
        const result = await MarmotService.RevealStorageSource(sourceID);
        if (!result.ok) setNotice(result.message);
      } catch (error) {
        setNotice(String(error));
      }
    }
  }

  function forgetResult() {
    window.localStorage.removeItem("marmot.scanTaskId");
    setStatus(null);
    setCachedStatus(null);
    setMap(null);
    setPages([]);
    setPageIndex(-1);
    setHoveredEntry(null);
    setFocusedEntry(null);
    setSelectedEntry(null);
    setStaleEntry(null);
    setCollector([]);
    setPlan(null);
    setValidation(null);
    setNotice("已放弃扫描结果。结果只存在于内存，重新查看需要再扫描一次。");
  }

  function returnToSource() {
    if (!status || status.snapshotId <= 0 || scanActive) return;
    setCachedStatus(status);
    setStatus(null);
    setMap(null);
    setPages([]);
    setPageIndex(-1);
    setHoveredEntry(null);
    setFocusedEntry(null);
    setSelectedEntry(null);
    setStaleEntry(null);
    setCollectorOpen(false);
  }

  function markStale(entry: MapEntry) {
    setStaleEntry(entry);
    setSelectedEntry(entry);
    setNotice("对象已变化，已停用文件操作。请重新读取当前目录。");
  }

  function toggleCollector(entry: MapEntry | null) {
    if (!entry) return;
    if (staleEntry && entryKey(staleEntry) === entryKey(entry)) {
      setNotice("对象已变化，不能加入收集区。");
      return;
    }
    if (!hasCapability(entry, "collect") || !entryNode(entry)) {
      setNotice("聚合对象和受限对象不能加入收集区。");
      return;
    }
    setCollector((current) => current.some((item) => entryKey(item) === entryKey(entry))
      ? current.filter((item) => entryKey(item) !== entryKey(entry))
      : current.concat(entry));
  }

  async function previewEntry(entry: MapEntry | null) {
    const node = entryNode(entry);
    if (!entry || !node || !hasCapability(entry, "preview") || (staleEntry && entryKey(staleEntry) === entryKey(entry))) {
      setNotice("该对象不能预览。");
      return;
    }
    try {
      const result = await MarmotService.PreviewNode(statusRef.current?.snapshotId ?? 0, node.id);
      if (result.code === "stale_node") markStale(entry);
      else setNotice(result.ok ? "Quick Look 已打开" : result.message);
    } catch (error) {
      setNotice(String(error));
    }
  }

  async function revealEntry(entry: MapEntry | null) {
    const node = entryNode(entry);
    if (!entry || !node || !hasCapability(entry, "reveal") || (staleEntry && entryKey(staleEntry) === entryKey(entry))) {
      setNotice("该对象不能在 Finder 中定位。");
      return;
    }
    try {
      const result = await MarmotService.RevealNode(statusRef.current?.snapshotId ?? 0, node.id);
      if (result.code === "stale_node") markStale(entry);
      else setNotice(result.ok ? "已在 Finder 中定位" : result.message);
    } catch (error) {
      setNotice(String(error));
    }
  }

  async function createPlan() {
    if (!status || collector.length === 0) return;
    try {
      const next = await MarmotService.CreateCleanupPlan({ snapshotId: status.snapshotId, paths: collector.map((item) => entryNode(item)?.path ?? "") });
      const nextValidation = await MarmotService.ValidateCleanupPlan(next.id, next.version);
      setPlan(nextValidation.valid ? { ...next, state: "validated" } : next);
      setValidation(nextValidation);
      if (!nextValidation.valid) {
        setNotice("校验未通过，不能执行清理。");
        return;
      }
      startCountdown(next.id, next.version);
    } catch (error) {
      setNotice(String(error));
    }
  }

  function stopCountdown() {
    if (countdownTimer.current !== null) {
      window.clearInterval(countdownTimer.current);
      countdownTimer.current = null;
    }
    setCountdown(null);
  }

  // The countdown is the confirmation: letting it finish confirms this exact
  // plan version, then the plan is re-validated and executed.
  function startCountdown(planID: string, version: number) {
    stopCountdown();
    setCountdown(countdownSeconds);
    countdownTimer.current = window.setInterval(() => {
      setCountdown((current) => {
        if (current === null) return null;
        if (current > 1) return current - 1;
        stopCountdown();
        void runCleanup(planID, version);
        return null;
      });
    }, 1000);
  }

  async function runCleanup(planID: string, version: number) {
    try {
      const recheck = await MarmotService.ValidateCleanupPlan(planID, version);
      setValidation(recheck);
      if (!recheck.valid) {
        setNotice("执行前校验失败，已中止。");
        return;
      }
      setPlan(await MarmotService.ConfirmCleanupPlan(planID, version));
      const applied = await MarmotService.ExecuteCleanupPlan(planID, version);
      setPlan(applied);
      if (applied.state === "applied") {
        setCollector([]);
        setNotice("已移入废纸篓，请重新扫描刷新结果");
      } else {
        setNotice(applied.state);
      }
    } catch (error) {
      setNotice(String(error));
    }
  }



  function handleDrop(event: ReactDragEvent<HTMLDivElement>) {
    event.preventDefault();
    const file = event.dataTransfer.files[0] as (File & { path?: string }) | undefined;
    const text = event.dataTransfer.getData("text/uri-list") || event.dataTransfer.getData("text/plain");
    const droppedPath = file?.path || text.replace(/^file:\/\//, "").split("\n")[0];
    if (!droppedPath) return;
    try {
      setRoot(decodeURIComponent(droppedPath));
    } catch {
      setRoot(droppedPath);
    }
  }

  function handleCollectorDrop(event: ReactDragEvent<HTMLDivElement>) {
    event.preventDefault();
    event.stopPropagation();
    const raw = event.dataTransfer.getData("application/marmot-entry");
    if (!raw) return;
    try {
      toggleCollector(JSON.parse(raw) as MapEntry);
    } catch {
      setNotice("无法读取拖入对象");
    }
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const command = event.metaKey || event.ctrlKey;
      const target = event.target as HTMLElement | null;
      if (target && ["INPUT", "TEXTAREA", "SELECT", "BUTTON"].includes(target.tagName)) return;
      if (event.key === "Escape") {
        setCollectorOpen(false);
        return;
      }
      if (command && event.key === "ArrowUp") {
        event.preventDefault();
        goParent();
        return;
      }
      if (command && event.key === "[") {
        event.preventDefault();
        navigateToHistory(pageIndex - 1);
        return;
      }
      if (command && event.key === "]") {
        event.preventDefault();
        navigateToHistory(pageIndex + 1);
        return;
      }
      if (command && event.key.toLowerCase() === "r") {
        event.preventDefault();
        if (event.shiftKey) void startScan(statusRef.current?.root || rootRef.current);
        else refreshCurrent();
        return;
      }
      if (command && event.key === "Delete") {
        event.preventDefault();
        toggleCollector(focusedEntry ?? selectedEntry);
        return;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowRight" || event.key === "ArrowUp" || event.key === "ArrowLeft" || event.key === "PageDown" || event.key === "PageUp" || event.key === "Home" || event.key === "End") {
        if (entries.length === 0) return;
        event.preventDefault();
        const focusedIndex = focusedEntry ? entries.findIndex((entry) => entryKey(entry) === entryKey(focusedEntry)) : -1;
        let nextIndex = focusedIndex < 0 ? 0 : focusedIndex;
        if (event.key === "ArrowDown" || event.key === "ArrowRight") nextIndex = Math.min(entries.length - 1, nextIndex + 1);
        if (event.key === "ArrowUp" || event.key === "ArrowLeft") nextIndex = Math.max(0, nextIndex - 1);
        if (event.key === "PageDown") nextIndex = Math.min(entries.length - 1, nextIndex + Math.max(1, Math.floor(entries.length / 8)));
        if (event.key === "PageUp") nextIndex = Math.max(0, nextIndex - Math.max(1, Math.floor(entries.length / 8)));
        if (event.key === "Home") nextIndex = 0;
        if (event.key === "End") nextIndex = entries.length - 1;
        setFocusedEntry(entries[nextIndex]);
        return;
      }
      if (event.key === "Enter" && focusedEntry) {
        event.preventDefault();
        activateEntry(focusedEntry);
        return;
      }
      if (event.key === " " && (focusedEntry || selectedEntry)) {
        event.preventDefault();
        void previewEntry(focusedEntry ?? selectedEntry);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  });

  return (
    <div className={"app-shell " + (showResult ? "app-shell-result" : "app-shell-source")} onDragOver={(event) => event.preventDefault()} onDrop={handleDrop}>
      {/* One chrome row, like the reference: window buttons, navigation and the
          breadcrumb trail. Everything else that used to live up here (title
          block, counters, history meta) is technical detail the reference never
          shows on the result page. */}
      <header className={"topbar " + (showResult ? "topbar-result" : "topbar-source")}>
        {showResult ? (
          <>
            <div className="topbar-nav">
              <button className="topbar-arrow" onClick={() => navigateToHistory(pageIndex - 1)} disabled={pageIndex <= 0} aria-label="后退" title="后退">‹</button>
              <button className="topbar-arrow" onClick={() => navigateToHistory(pageIndex + 1)} disabled={pageIndex < 0 || pageIndex >= pages.length - 1} aria-label="前进" title="前进">›</button>
            </div>
            <nav className="crumbs" aria-label="目录路径">
              <button className="crumb crumb-root" onClick={returnToSource}>磁盘和文件夹</button>
              {(currentPage?.crumbs ?? []).map((crumb, index) => (
                <button
                  key={crumb.id + ":" + index}
                  className={"crumb" + (index === (currentPage?.crumbs.length ?? 1) - 1 ? " is-current" : "")}
                  onClick={() => jumpToBreadcrumb(index)}
                >
                  {crumbLabel(crumb.path, index)}
                </button>
              ))}
            </nav>
          </>
        ) : (
          <div className="topbar-title">Marmot</div>
        )}
      </header>

      {showResult ? (
        <main className="workspace has-result" data-testid="result-view">
          <section className="workbench" data-testid="workbench">
            <div className="map-panel">
              <div className="map-heading">
                <div>
                  <p className="eyebrow">CURRENT MAP</p>
                  <h2>{currentParent?.name ?? currentPage?.path ?? "空间图"}</h2>
                </div>
                <div className="map-heading-meta">
                  <span>{map?.total.toLocaleString() ?? 0} 项</span>
                  <span>{confidenceLabel(mapConfidence)}</span>
                </div>
              </div>
              <div className="map-stage">
                <Sunburst
                  onDragEntry={beginSliceDrag}
                  hueRange={hueRange}
                  baseDepth={baseDepth}
                  centerColor={centerColor}
                  map={map}
                  hoveredKey={hoveredKey}
                  focusedKey={focusedKey}
                  selectedKey={selectedKey}
                  onHover={setHoveredEntry}
                  onFocus={setFocusedEntry}
                  onActivate={activateEntry}
                  onPreview={(entry) => void previewEntry(entry)}
                  onReveal={(entry) => void revealEntry(entry)}
                  onGoParent={goParent}
                />
                {mapBusy && <span className="map-loading">正在更新当前层...</span>}
              </div>
              <div className="map-footer">
                <span>文件夹按空间贡献着色，聚合项只代表统计结果</span>
                <span>{map?.hasMore ? "已显示 " + (currentPage?.offset ?? 0) + " - " + ((currentPage?.offset ?? 0) + entries.filter((entry) => entry.kind === "node").length) + " / " + map.total : "当前层已显示完整结果"}</span>
                <div className="page-actions">
                  <button onClick={() => currentPage && void goToPage({ ...currentPage, offset: Math.max(0, currentPage.offset - pageSize) }, "replace")} disabled={!currentPage || currentPage.offset === 0 || mapBusy}>上一页</button>
                  <button onClick={() => currentPage && map?.hasMore && void goToPage({ ...currentPage, offset: currentPage.offset + entries.filter((entry) => entry.kind === "node").length }, "replace")} disabled={!currentPage || !map?.hasMore || mapBusy}>下一页</button>
                </div>
              </div>
            </div>
            <DirectoryList
              entryColors_={levelColors}
              parentDotColor={baseDepth > 0 ? sliceColor(hueRange.center, baseDepth) : null}
              parent={currentParent}
              entries={entries}
              total={mapTotal}
              map={map}
              hoveredKey={hoveredKey}
              focusedKey={focusedKey}
              selectedKey={selectedKey}
              contextEntry={inspectorEntry}
              inCollector={inspectedInCollector}
              onHover={setHoveredEntry}
              onFocus={setFocusedEntry}
              onActivate={activateEntry}
              onPreview={(entry) => void previewEntry(entry)}
              onReveal={(entry) => void revealEntry(entry)}
              onEnter={(entry) => entry.kind === "node" ? openDirectory(entry) : expandEntry(entry)}
              onCollect={toggleCollector}
            />
          </section>
        </main>
      ) : (
        <main className="workspace has-source" data-testid="source-view">
          {permission && permission.state !== "available" && (
            <div className="source-alert" role="status">{permission.message || "需要完整磁盘访问权限才能扫描系统目录"}</div>
          )}
          <section className="volume-strip" aria-label="磁盘范围">
            <div className="volume-list">
              {storageSources.map((source) => (
                <VolumeTile
                  key={source.id}
                  source={source}
                  hasResult={Boolean(cachedStatus && cachedStatus.snapshotId > 0 && cachedStatus.root === source.path)}
                  scanning={Boolean(scanActive && status?.root === source.path)}
                  scanStatus={scanActive && status?.root === source.path ? status : null}
                  scanLocked={scanActive}
                  onScan={(path) => { setRoot(path); void startScan(path); }}
                  onView={viewCachedResult}
                  onCancel={() => void cancelScan()}
                  onForget={forgetResult}
                />
              ))}
              {storageSources.length === 0 && <div className="volume-loading">正在读取存储源…</div>}
            </div>
          </section>

          <div className="source-foot">
            <button className="ghost-button" onClick={() => void chooseFolder()} disabled={busy || scanActive}>扫描文件夹…</button>
          </div>
	        </main>
      )}
      {dragGhost && (
        <div className="drag-ghost" style={{ left: dragGhost.x + 12, top: dragGhost.y + 12 }}>{dragGhost.label}</div>
      )}
      {notice && <div className="notice" role="status">{notice}</div>}

      {showResult && <section
        ref={collectorRef}
        className={"collector-dock" + (collectorOpen ? " is-open" : "")}
        onDragOver={(event) => event.preventDefault()}
        onDrop={handleCollectorDrop}
        data-testid="collector"
      >
        {collector.length === 0 ? (
          /* Nothing collected: the reference shows only the drop ring and one
             line of instruction. */
          <button className="collector-empty-state" onClick={() => setCollectorOpen((open) => !open)}>
            <span className="collector-target" aria-hidden="true" />
            <span className="collector-caption">将文件拖放至此，以收集要删除的文件</span>
          </button>
        ) : (
          /* With items the bar lives inside the panel as its last row: badge
             straddling the left edge, amount, and the destructive action. */
          <div className="collector-panel">
            {countdown === null && <div className="collector-list">
              {collector.map((item) => {
                const node = entryNode(item);
                return (
                  <div
                    className="collector-item"
                    key={entryKey(item)}
                    draggable={Boolean(node)}
                    onDragStart={(event) => {
                      if (!node) return;
                      event.dataTransfer.setData("text/uri-list", "file://" + encodeURI(node.path));
                      event.dataTransfer.setData("text/plain", node.path);
                      event.dataTransfer.effectAllowed = "copy";
                    }}
                    onDragEnd={(event) => {
                      if (event.dataTransfer.dropEffect !== "none") toggleCollector(item);
                    }}
                  >
                    <span className="collector-dot" style={{ background: levelColors[entryKey(item)] ?? "#7fb96a" }} aria-hidden="true" />
                    <button className="collector-remove" onClick={() => toggleCollector(item)} aria-label={"移除 " + item.name}>×</button>
                    <strong>{item.name}</strong>
                    <b>{formatBytes(item.ownedAllocated)}</b>
                  </div>
                );
              })}
            </div>}
            <div className="collector-bar">
              <span className={"collector-target is-filled" + (countdown !== null ? " is-counting" : "")} aria-hidden="true">
                {countdown !== null && (
                  <svg className="collector-ring" viewBox="0 0 44 44">
                    <circle
                      cx="22"
                      cy="22"
                      r="20"
                      strokeDasharray={2 * Math.PI * 20}
                      strokeDashoffset={2 * Math.PI * 20 * (1 - countdown / countdownSeconds)}
                    />
                  </svg>
                )}
                <span className="collector-count">
                  {countdown !== null ? countdown : formatBytes(collectorBytes).split(" ")[0]}
                </span>
              </span>
              <span className="collector-caption">
                {countdown !== null
                  ? <>秒后开始。选中的文件将<strong className="destructive-note">移入废纸篓</strong></>
                  : plan?.state === "confirmed"
                    ? "正在移入废纸篓…"
                    : validation && !validation.valid
                      ? "校验未通过，不能执行"
                      : formatBytes(collectorBytes).split(" ")[1] + " 已收集"}
              </span>
              {countdown === null
                ? <button className="danger-button compact" onClick={() => void createPlan()}>删除</button>
                : <button className="quiet-button" onClick={stopCountdown}>停止</button>}
            </div>
          </div>
        )}
      </section>}
    </div>
  );
}
