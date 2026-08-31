import { useEffect, useLayoutEffect, useRef, useState, useMemo } from "react";
import { paintMorph, clearMorphStyles, planMorph, arcPath, morphDuration, morphArcCeiling } from "./morph";
import type { ArcGeom, MorphPlan } from "./morph";
import { childEndAngle, subBand, rootHueBand, sunburstAggregate, sunburstHiddenSpace, sunburstEndAngle, previewDwellMs, previewLeaveMs } from "./sunburst";
import type { HueBand } from "./sunburst";
import { autoStageable, stageSummary } from "./advice";
import { sliceColor, sunburstGeometry, projectionMinSweeps, minArcPixels, ringWidthFor } from "./sunburst";
import type { CSSProperties, DragEvent as ReactDragEvent, PointerEvent as ReactPointerEvent } from "react";
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
type Advice = Models.Advice;
type AdviceItem = Models.AdviceItem;
type EvidencePreview = Models.EvidencePreview;
type AdvisorStatus = Models.AdvisorStatus;
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
// name is what labels the crumb when there is no path yet: a level entered
// through a projected arc has only an id and a name, because the projection
// carries no path by design (ADR-0048). loadMap fills the path in once that
// level is actually opened.
type Breadcrumb = { id: number; path: string; name: string; hue: HueBand; endAngle: number };

// What the list shows while the pointer is over an arc. The reference switches
// the right-hand panel to the hovered node's own heading and children, on every
// ring — the wheel itself does not navigate. The projection already carries the
// subtree, so nothing has to be fetched.
type HoverPreview = {
  key: string;
  name: string;
  size: number;
  children: ProjectedEntry[];
  hasMore: boolean;
  // The hovered arc's own band and depth in the tree. Both are needed to colour
  // the preview: its children take sub-bands of this band, one level deeper.
  // Looking the colour up by key in the current level's table only ever worked
  // for the innermost ring — a projected node's key is not in that table, so
  // every outer-ring row fell back to the same grey.
  band: HueBand;
  depth: number;
};
// What one drag carries. `entry` is set when the arc or row came from the current
// level, which already has its path and capabilities; it is null for an arc on an
// outer ring, which was drawn from a projection and has to be looked up by
// nodeId before anything may act on it (ADR-0048).
type DragSource = {
  key: string;
  name: string;
  size: number;
  color: string;
  entry: MapEntry | null;
  nodeId: number;
  // Why it may not be deleted, or empty. Known for both kinds of source now, so
  // the dock never has to wait to answer.
  protection: string;
};

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
  // Where this level's sequence ends. Not a constant: the reference keeps the
  // clicked wedge's angular midline fixed while it opens to the full circle, so
  // the child level's seam lands at that midline + PI (ADR-0060 §5, measured to
  // within 0.4deg across the whole expansion). Only the scan root uses the fixed
  // 3 o'clock seam.
  endAngle: number;
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
// Pixels the pointer must travel before a press turns into a drag instead of a
// click. Below this the wheel still navigates and the list still selects.
const dragThreshold = 6;
// How far outside the dock a release still counts as a drop. The empty dock is a
// 52px ring at the bottom-left corner; the original takes a drop that lands near
// it, not only on it, and the corner has nothing else to hit.
const dropSlack = 26;

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
// The wheel's palette runs all the way through yellow, where white text is
// unreadable, so the chip picks its text from the wedge's own luminance rather
// than assuming one colour works on every hue. sRGB relative luminance, the same
// definition WCAG contrast uses.
function readableOn(hex: string): string {
  const channel = (index: number) => {
    const c = parseInt(hex.slice(index, index + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  const luminance = 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
  return luminance > 0.35 ? "#1b1c20" : "#ffffff";
}

// Why a refusal happened, in words. The backend sends a code, not a sentence:
// the policy is its to decide (cleanup.DeleteBlock) but a sentence per entry
// would repeat across a space map payload that is already capped, and the wording
// belongs with the rest of the UI's wording.
const protectionReasons: Record<string, (name: string) => string> = {
  system_dependency: (name) => "“" + name + "” 是 macOS 系统的依赖文件，您不应该将其删除。",
  // Not the same statement as the one above, and it matters: this folder is the
  // account's own data, not something the system depends on.
  home_folder: (name) => "“" + name + "” 是用户的个人文件夹，删除它会一并移除该账户的全部数据。",
  volume_root: (name) => "“" + name + "” 是一个已挂载的卷，不能当作文件夹删除。",
};

function protectionMessage(protection: string | undefined, name: string): string {
  if (!protection) return "";
  const reason = protectionReasons[protection];
  return reason ? reason(name) : "“" + name + "” 不允许删除。";
}

const virtualLabels: Record<string, string> = {
  smaller_objects: "较小对象",
  hidden_space: "隐藏空间",
  purgeable_space: "可清理空间",
  other_volumes: "其他卷",
  snapshot: "系统快照",
  restricted: "受限空间",
};

// previewRows gives each child of the hovered node its own colour, by walking the
// same cumulative-size subdivision the wheel uses. One colour for the whole list
// was wrong twice over: it made every row identical, and it came from the current
// level's colour table, which has no entry for a projected node.
function previewRows(preview: HoverPreview): Array<{ key: string; name: string; size: number; color: string; grey: boolean }> {
  const total = preview.children.reduce((sum, child) => sum + Math.max(0, child.size), 0);
  let cursor = 0;
  return preview.children.map((child) => {
    const size = Math.max(0, child.size);
    const from = total > 0 ? cursor / total : 0;
    cursor += size;
    const to = total > 0 ? cursor / total : 0;
    const band = subBand(from, to, preview.band);
    return {
      key: String(child.id) + ":" + child.name,
      name: child.name,
      size,
      grey: child.kind === "aggregate",
      color: child.kind === "aggregate" ? sunburstAggregate : sliceColor(band.center, preview.depth + 1),
    };
  });
}

// The wheel and the directory list must agree on an entry's colour, so both
// derive it from the same cumulative angular position.
// The reference's hue runs 1:1 with angle from the start of the sequence, so the
// root band is the whole wheel starting at the measured green.

function entryColors(entries: MapEntry[], band: HueBand, baseDepth: number): Record<string, string> {
  const bands = entryBands(entries, band);
  const colors: Record<string, string> = {};
  for (const entry of entries) {
    const own = bands[entryKey(entry)];
    colors[entryKey(entry)] = entry.kind === "node" && own ? sliceColor(own.center, baseDepth + 1) : aggregateColor(entry);
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

// The three answers to "what does it cost me to get this back". Recovery is
// deliberately separate from risk: a 40 GB build directory is regenerable and
// deleting it still costs an hour of rebuilding, and the user is entitled to
// know which of the two they are being asked to accept.
const recoveryLabels: Record<string, string> = {
  regenerable: "会自动重建",
  redownloadable: "可重新下载",
  irreplaceable: "删了就没了",
};

const riskLabels: Record<string, string> = {
  safe: "安全",
  review: "需确认",
  risky: "有风险",
};

// Home paths are shown the way the shell writes them. Only the display is
// abbreviated: what gets sent to CreateCleanupPlan is always the real path.
function homePath(path: string): string {
  const match = /^(?:\/System\/Volumes\/Data)?\/Users\/[^/]+(\/.*)?$/.exec(path);
  if (!match) return path;
  return "~" + (match[1] ?? "");
}

function confidenceLabel(confidence: string): string {
  return ({ exact: "精确", estimated: "估算", partial: "部分结果", unknown: "未知" } as Record<string, string>)[confidence] ?? "待确认";
}

function displayStateLabel(state: string): string {
  return ({ current: "当前结果", stale: "对象已变化", partial: "部分结果" } as Record<string, string>)[state] ?? "待确认";
}

// Not every grey block is the same thing. The folded tail says "several objects
// too small to draw"; hidden space says "this much of the volume the walk could
// not account for" (ADR-0052 SS4). The reference paints the second one a dimmed
// violet rather than grey, so it is not one colour with two meanings.
function aggregateColor(entry: MapEntry | null): string {
  return entry?.virtualType === "hidden_space" ? sunburstHiddenSpace : sunburstAggregate;
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
// projectedArc normalizes a slim projected descendant into a drawable arc.
function projectedArc(child: ProjectedEntry) {
  return {
    key: child.kind === "aggregate" ? "aggregate:" + child.name : "node:" + child.id,
    size: Math.max(0, child.size),
    isDirectory: child.kind === "directory",
    aggregate: child.kind === "aggregate",
    stale: false,
    entry: null as MapEntry | null,
    // A projected descendant carries no path, so it can never authorise a file
    // operation (ADR-0048, DDD invariant 17). It does carry its node id, and
    // navigation is by id — which is why every ring is clickable in the
    // reference and now here too.
    nodeId: child.kind === "directory" ? child.id : 0,
    // Same id, without the "can I be entered" question: a projected file can be
    // dragged to the dock but not navigated into, so collecting reads this and
    // navigation reads nodeId. Both go through the backend by id.
    id: child.kind === "aggregate" ? 0 : child.id,
    // The one thing the projection says about acting on itself, and it only says
    // no. It is here so an outer ring's arc refuses on the frame the drag starts,
    // the way a current-level entry does, instead of a round trip later.
    protection: child.protection ?? "",
    // Empty, and it stays empty: the projection has no path (ADR-0048). The
    // crumb this arc contributes is labelled by name until its level is opened.
    path: "",
    name: child.name,
    hasMore: Boolean(child.more),
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

// The trail's label for one crumb: its path when it has one, and the name the
// projection carried when it does not.
function breadcrumbLabel(crumb: Breadcrumb, index: number): string {
  return crumb.path ? crumbLabel(crumb.path, index) : crumb.name;
}

function rootPage(snapshotId: number, path: string): Page {
  return {
    snapshotId, parentId: 1, path, offset: 0,
    crumbs: [{ id: 1, path, name: path, hue: rootHueBand, endAngle: sunburstEndAngle }],
    hue: rootHueBand, endAngle: sunburstEndAngle,
  };
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
  onHoverArc,
  breathingKey,
  onFocus,
  onActivate,
  onPreview,
  onReveal,
  onGoParent,
  centerColor,
  hueRange,
  baseDepth,
  levelEndAngle,
  onEnterProjected,
  onDragEntry,
  collectedKeys,
  draggingKey,
}: {
  map: MapResult | null;
  hoveredKey: string | null;
  focusedKey: string | null;
  selectedKey: string | null;
  onHover: (entry: MapEntry | null) => void;
  onHoverArc: (preview: HoverPreview | null) => void;
  breathingKey: string | null;
  onFocus: (entry: MapEntry) => void;
  onActivate: (entry: MapEntry, geom?: ArcGeom) => void;
  onPreview: (entry: MapEntry) => void;
  onReveal: (entry: MapEntry) => void;
  onGoParent: () => void;
  centerColor: string | null;
  hueRange: HueBand;
  baseDepth: number;
  levelEndAngle: number;
  // Takes the whole chain, not just the clicked node: the trail is what the
  // breadcrumb needs, and only the wheel knows it.
  onEnterProjected: (trail: Breadcrumb[]) => void;
  onDragEntry: (source: DragSource, event: ReactPointerEvent) => void;
  // Staged in the dock. These arcs keep their slot and stay drawn -- faded back
  // and non-interactive -- because the object is not gone: it is queued for
  // deletion and still on disk until the dock's own action runs.
  collectedKeys: Set<string>;
  // The one arc being dragged right now, which does leave the ring: that is the
  // gesture. Both states carry their projected descendants with them.
  draggingKey: string | null;
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
  // Geometry of the last painted level, keyed by morph identity. The source of
  // the tween.
  const paintedGeom = useRef<Map<string, ArcGeom>>(new Map());
  const morphFrame = useRef<number | null>(null);
  // The in-flight tween. Held in a ref because any re-render during the morph
  // (hovering, which happens immediately: the pointer is still over the wheel
  // when the click lands) rewrites every `d` back to the destination, so the
  // current frame has to be re-applied afterwards.
  const morphState = useRef<MorphPlan | null>(null);
  // Arcs that the level change removes. Without them the wedges that are not
  // part of the destination vanish on the first frame and leave a hole in the
  // wheel; the original sweeps them out of the circle instead.
  const [ghosts, setGhosts] = useState<Array<{ renderKey: string; color: string; geom: ArcGeom }>>([]);
  // Also keyed by morph identity: this is what decides which arcs need a ghost.
  const paintedSlices = useRef<Array<{ morphKey: string; geom: ArcGeom; color: string }>>([]);
  // The level we are leaving, so a hub click can fold the old level back into
  // the wedge it came from.
  const previousParentKey = useRef<string | null>(null);
  const ghostGroupRef = useRef<SVGGElement | null>(null);
  // An arc is either a current-level entry, which is interactive because it
  // carries a full node, or a projected descendant, which is visual only: it has
  // no path and therefore cannot authorise any file operation (ADR-0048).
  type Arc = {
    key: string;
    // Identity across a level change. The same as `key` for a real node; for a
    // grey block, scoped to the parent whose tail it stands for, because an
    // aggregate's name and the folded block's position both repeat at every
    // level. See planMorph.
    morphKey: string;
    renderKey: string;
    depth: number;
    hue: number;
    path: string;
    // The geometry behind `path`. Kept so a level change can interpolate from
    // where each arc was to where it lands, instead of cutting.
    geom: ArcGeom;
    entry: MapEntry | null;
    // Non-zero on a projected descendant that is a directory: it has no path, so
    // it cannot authorise anything, but it can be navigated to by id.
    nodeId: number;
    // The band this arc hands to its own children. Entering the arc must adopt
    // it, or the level below is recoloured from its parent's whole band.
    band: HueBand;
    // What the list shows while the pointer is over this arc.
    preview: HoverPreview | null;
    aggregate: boolean;
    stale: boolean;
    // Decided once, here, because two places paint it: the live arc and the
    // ghost the morph leaves behind. They must not disagree.
    color: string;
    // The snapshot node id, on every ring. Non-zero means the arc can be looked
    // up, which is what collecting one below the current level needs.
    id: number;
    // Why it may not be deleted, or empty. Answered on every ring, so a drag
    // refuses at once wherever it started.
    protection: string;
    name: string;
    size: number;
    // This arc, or one of its ancestors, is staged in the dock: drawn, faded,
    // out of reach.
    collected: boolean;
    // This arc, or one of its ancestors, is being dragged out right now.
    dragging: boolean;
    // The crumbs for every level between the current one and this arc, this arc
    // included. Entering an outer ring crosses all of them at once, and this is
    // where their ids, bands and seams come from -- the wheel drew those levels,
    // so it is the only thing that knows them (ADR-0060 SS5c).
    trail: Breadcrumb[];
  };
  const slices: Arc[] = [];
  const pushLevel = (
    items: Array<{ key: string; size: number; isDirectory: boolean; aggregate: boolean; stale: boolean; entry: MapEntry | null; children: ProjectedEntry[] }>,
    startAngle: number,
    endAngle: number,
    depth: number,
    band: HueBand,
    collected = false,
    dragging = false,
    parentKey = "",
    trail: Breadcrumb[] = [],
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
        key: "folded:" + parentKey,
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
      // Inset on both sides, including the ends of the level's own span: the
      // reference draws a hairline at every boundary, parent boundaries included.
      // (An earlier attempt skipped the ends, on the theory that a child's gap was
      // compounding with its parent's. That theory came from reading a *mean* over
      // every narrow background run, and at the outer rings most of those runs are
      // holes left by culled children, not separators. Reading the low percentiles
      // instead shows the separator is constant with radius; it was only ever too
      // wide.)
      const inset = Math.min(separatorArc / ((r0 + r1) / 2) / 2, (next - cursor) / 2.5);
      const geom: ArcGeom = { a0: cursor + inset, a1: next - inset, r0, r1 };
      const itemCollected = collected || collectedKeys.has(item.key);
      const itemDragging = dragging || draggingKey === item.key;
      // A grey block is a wedge like any other and must animate like one, so it
      // needs an identity that survives a level change. Its own key cannot serve:
      // the projection and the current level spell an aggregate's key
      // differently, and the folded tail has no name at all. The parent it hangs
      // from plus what it is called is the same on both sides.
      const morphKey = item.aggregate
        ? parentKey + ">agg:" + ((item as { name?: string }).name ?? "folded")
        : item.key;
      const itemId = (item as { id?: number }).id ?? 0;
      const itemName = (item as { name?: string }).name ?? "";
      // The chain down to this arc. Aggregates are never in it: nothing recurses
      // through one, so no arc has an aggregate for an ancestor.
      const ownTrail = item.aggregate || itemId <= 0
        ? trail
        : trail.concat({
          id: itemId,
          path: (item as { path?: string }).path ?? "",
          name: itemName,
          hue: own,
          endAngle: childEndAngle(geom, endAngle),
        });
      slices.push({
        key: item.key,
        morphKey,
        renderKey: item.key + ":" + depth + ":" + slices.length,
        depth,
        hue,
        path: arcPath(geom),
        geom,
        entry: item.entry,
        nodeId: (item as { nodeId?: number }).nodeId ?? 0,
        band: own,
        preview: item.aggregate ? null : {
          key: item.key,
          name: (item as { name?: string }).name ?? "",
          size: item.size,
          children: item.children,
          hasMore: Boolean((item as { hasMore?: boolean }).hasMore),
          band: own,
          depth: baseDepth + depth + 1,
        },
        aggregate: item.aggregate,
        stale: item.stale,
        color: item.aggregate ? aggregateColor(item.entry) : sliceColor(hue, baseDepth + depth + 1),
        id: itemId,
        protection: (item as { protection?: string }).protection ?? "",
        name: itemName,
        size: item.size,
        collected: itemCollected,
        dragging: itemDragging,
        trail: ownTrail,
      });
      if (item.isDirectory && item.children.length > 0) {
        pushLevel(item.children.map(projectedArc), cursor, next, depth + 1, own, itemCollected, itemDragging, item.key, ownTrail);
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
      id: entry.kind === "node" ? entry.node.id : 0,
      path: entryPath(entry),
      protection: entry.protection ?? "",
      entry,
      name: entry.name,
      hasMore: Boolean(entry.childrenHasMore),
      children: (entry.children ?? []).filter(Boolean),
    })),
    levelEndAngle - usedSweep,
    levelEndAngle,
    0,
    hueRange,
    false,
    false,
    map ? "node:" + map.parent.id : "",
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
        if (!next.has(slice.morphKey)) next.set(slice.morphKey, slice.geom);
      }
      paintedGeom.current = next;
      previousParentKey.current = map ? "node:" + map.parent.id : null;
      paintedSlices.current = sliceSnapshot.map((slice) => ({
        morphKey: slice.morphKey,
        geom: slice.geom,
        color: slice.color,
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
    // What each arc does across the change is decided in planMorph, which also
    // holds the rule that aggregates never match by identity (their keys repeat
    // across levels).
    // Same identity the tween matches on, or a grey block that is really gone
    // counts as surviving and never gets a ghost to fade out with.
    const live = new Set(sliceSnapshot.map((slice) => slice.morphKey));
    const departingSlices: Array<{ renderKey: string; color: string; geom: ArcGeom }> = [];
    previousSlices.forEach((slice, index) => {
      if (live.has(slice.morphKey)) return;
      // At its own geometry. The reference does not move these; it removes them.
      departingSlices.push({ renderKey: "ghost:" + index, color: slice.color, geom: slice.geom });
    });
    setGhosts(departingSlices);

    morphState.current = planMorph(
      previous,
      sliceSnapshot,
      departingSlices.map((ghost) => ({ renderKey: ghost.renderKey, geom: ghost.geom })),
      performance.now(),
    );
    paintMorph(morphState.current, 0, morphRefs.current, ghostGroupRef.current);
    const step = () => {
      const state = morphState.current;
      if (!state) return;
      const elapsed = performance.now() - state.started;
      paintMorph(state, elapsed, morphRefs.current, ghostGroupRef.current);
      if (elapsed < morphDuration) {
        morphFrame.current = requestAnimationFrame(step);
        return;
      }
      morphFrame.current = null;
      clearMorphStyles(state, morphRefs.current);
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
      // that was never painted. The inline styles have to go with it.
      const interrupted = morphState.current;
      if (interrupted) clearMorphStyles(interrupted, morphRefs.current);
      morphState.current = null;
      commit();
    };
  }, [levelKey]);

  // Deliberately dependency-free: it must run after *every* render, because a
  // re-render mid-morph resets every `d` to the destination.
  useLayoutEffect(() => {
    const state = morphState.current;
    if (!state) return;
    paintMorph(state, performance.now() - state.started, morphRefs.current, ghostGroupRef.current);
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
      {/* The pointer being outside the wheel must mean nothing in the wheel is
          breathing, and one arc's own pointerleave cannot promise that: it does
          not fire if the arc is rebuilt or unmounted while the pointer stands
          still. This one fires on leaving the chart whatever happened inside it,
          which is what makes the hover state self-correcting. */}
      <svg
        className="sunburst"
        viewBox="0 0 600 600"
        role="img"
        onPointerLeave={() => { onHover(null); onHoverArc(null); }}
      >
        <g transform="translate(300 300)">
          {/* Departing arcs: drawn under the live ones, never interactive. */}
          {ghosts.length > 0 && (
            <g
              className="sunburst-ghosts"
              aria-hidden="true"
              ref={ghostGroupRef}
              style={{ opacity: 1 }}
            >
              {ghosts.map((ghost) => (
                <path
                  key={ghost.renderKey}
                  ref={(node) => {
                    if (node) morphRefs.current.set(ghost.renderKey, node);
                    else morphRefs.current.delete(ghost.renderKey);
                  }}
                  className="sunburst-slice is-ghost"
                  d={arcPath(ghost.geom)}
                  fill={ghost.color}
                />
              ))}
            </g>
          )}
          {slices.map(({ entry, key, renderKey, depth, path, aggregate, stale, nodeId, geom, preview, color, id, protection, name, size, collected, dragging, trail }) => {
            // Only current-level arcs are interactive: a projected descendant
            // carries no path, so it can neither be activated nor collected
            // (ADR-0048, ADR-0017 §2).
            const interactive = entry !== null;
            const canActivate = interactive && (!aggregate || depth === 0);
            // Every ring is clickable, not only the innermost. A projected arc
            // navigates by node id (ADR-0060 §6); it stays non-interactive for
            // hover, focus, collect and reveal, all of which need a path.
            const canEnterProjected = !interactive && nodeId > 0;
            // Draggable on every ring, not only the current level. The current
            // level's own entries carry their capabilities; an outer ring's arc
            // is a projection, so all we can check here is that it is a real
            // node -- whether it may be collected is decided by the backend when
            // the drop looks it up (ADR-0048).
            const collectable = !collected && !dragging && !aggregate && id > 0
              && (entry ? entry.kind === "node" : true);
            const selected = key === selectedKey;
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
                tabIndex={interactive && !collected && !dragging ? 0 : -1}
                aria-disabled={collected || dragging || undefined}
                onPointerDown={(event) => {
                  if (collectable) onDragEntry({ key, name, size, color, entry, nodeId: id, protection }, event);
                }}
                className={
                  "sunburst-slice" +
                  " depth-" + depth +
                  (selected ? " is-selected" : "") +
                  (key === breathingKey ? " is-breathing" : "") +
                  (aggregate ? " is-aggregate" : "") +
                  (stale ? " is-stale" : "") +
                  (collected || dragging ? " is-collected" : "") +
                  (interactive ? "" : " is-projected")
                }
                fill={color}
                aria-label={entry ? entry.name + "，" + formatBytes(entrySize(entry)) : undefined}
                onPointerEnter={() => {
                  // Every ring reacts, not only the current level: the reference
                  // breathes whichever arc is under the pointer and swings the
                  // list to that node.
                  if (entry) onHover(entry);
                  onHoverArc(preview);
                }}
                onPointerLeave={() => {
                  if (entry) onHover(null);
                  onHoverArc(null);
                }}
                onFocus={() => { if (entry) onFocus(entry); }}
                onClick={(event) => {
                  if (canEnterProjected) {
                    onEnterProjected(trail);
                    return;
                  }
                  if (!canActivate || !entry) return;
                  if (event.metaKey || event.ctrlKey) {
                    event.preventDefault();
                    onReveal(entry);
                    return;
                  }
                  onActivate(entry, geom);
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
  preview,
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
  onDragEntry,
  pulledKeys,
}: {
  entryColors_: Record<string, string>;
  // While the pointer is over an arc the panel shows that node instead of the
  // current level — on every ring, and without the wheel navigating.
  preview: { name: string; size: number; rows: Array<{ key: string; name: string; size: number; color: string; grey: boolean }>; hasMore: boolean; color: string } | null;
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
  // Rows drag to the dock the same way arcs do, through the pointer path in
  // beginEntryDrag rather than HTML5 drag and drop.
  onDragEntry: (source: DragSource, event: ReactPointerEvent) => void;
  // Rows whose object has left the current directory: the one being dragged, and
  // everything already in the dock. R-014 SS3.6 -- dragging an item back out of
  // the Collector restores it to this list, so it is out of the list while it is
  // in there. The row stays mounted and collapses, so both directions animate.
  pulledKeys: Set<string>;
}) {
  return (
    <aside className="directory-panel" data-testid="directory-list">
      <div className="directory-heading">
        {preview
          ? <span className="directory-parent-dot" style={{ background: preview.color }} />
          : parentDotColor && <span className="directory-parent-dot" style={{ background: parentDotColor }} />}
        <h2>{preview ? preview.name : parent ? crumbLabel(parent.path, parent.parentId === 0 ? 0 : 1) : "当前目录"}</h2>
        <strong>{formatBytes(preview ? preview.size : total)}</strong>
      </div>

      <div className="directory-list" role="listbox" aria-label="当前目录内容">
        {preview ? (
          // Read-only while previewing: these rows come from the projection, so
          // they carry no path and nothing here may act on them.
          preview.rows.map((row) => (
            <div key={row.key} className="directory-row is-preview">
              <span className="directory-dot" style={{ background: row.color }} />
              <span className="directory-name">{row.name}</span>
              <span className="directory-tag" />
              <span className="directory-size">{formatBytes(row.size)}</span>
            </div>
          ))
        ) : entries.length === 0 ? (
          <div className="directory-empty">当前目录没有可显示的项目。</div>
        ) : entries.map((entry, index) => {
          const key = entryKey(entry);
          const isSelected = key === selectedKey;
          const isFocused = key === focusedKey;
          const isHovered = key === hoveredKey;
          const pulled = pulledKeys.has(key);
          const kindClass = entry.kind === "aggregate" || entry.kind === "virtual" ? "virtual" : entry.node.kind === "directory" ? "directory" : "file";
          // The reference writes this one row's name in the same violet as its
          // wedge, not in the list's ordinary colour: it is the only entry that
          // stands for space rather than for objects.
          const hiddenSpace = entry.virtualType === "hidden_space";
          return (
            <div
              key={key}
              className={"directory-row" + (isSelected ? " is-selected" : "") + (isFocused ? " is-focused" : "") + (isHovered ? " is-hovered" : "") + (pulled ? " is-pulled" : "")}
              role="option"
              aria-selected={isSelected}
              aria-hidden={pulled || undefined}
              tabIndex={pulled ? -1 : isFocused || (index === 0 && !focusedKey) ? 0 : -1}
              onPointerDown={(event) => {
                if (pulled || entry.kind !== "node") return;
                onDragEntry({ key, name: entry.name, size: entrySize(entry), color: entryColors_[key] ?? "#7fb96a", entry, nodeId: entry.node.id, protection: entry.protection ?? "" }, event);
              }}
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
              title={entryPath(entry)}
            >
              <span className={"directory-dot " + kindClass} style={entryColors_[entryKey(entry)] ? { background: entryColors_[entryKey(entry)] } : undefined} />
              <span className={"directory-name" + (hiddenSpace ? " is-hidden-space" : "")}>{entry.name}</span>
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
  // What the pointer is over, on any ring. Kept apart from hoveredEntry, which
  // only ever holds a current-level entry.
  //
  // Two states, not one, because the two responses want different timing. The
  // wedge breathes at once — that is the direct answer to "am I on it". The list
  // waits until the pointer has settled, or it snaps between nodes on the way
  // across the wheel.
  const [hoveredArcKey, setHoveredArcKey] = useState<string | null>(null);
  const [hoverPreview, setHoverPreview] = useState<HoverPreview | null>(null);
  const previewTimer = useRef<number | undefined>(undefined);
  // What the panel is currently showing, mirrored in a ref so the decision to
  // schedule can be made without reading state inside a setState updater — React
  // may run an updater twice, and a timer started in there would be started
  // twice with it.
  const shownPreviewKey = useRef<string | null>(null);
  useEffect(() => () => window.clearTimeout(previewTimer.current), []);

  // Leaving is delayed too, and cancellable. Clearing on the way out would flash
  // the level back between two adjacent wedges, which is the flicker this exists
  // to remove.
  function hoverArc(preview: HoverPreview | null) {
    setHoveredArcKey(preview?.key ?? null);
    window.clearTimeout(previewTimer.current);
    if (!preview) {
      previewTimer.current = window.setTimeout(() => {
        shownPreviewKey.current = null;
        setHoverPreview(null);
      }, previewLeaveMs);
      return;
    }
    if (shownPreviewKey.current === preview.key) return;
    previewTimer.current = window.setTimeout(() => {
      shownPreviewKey.current = preview.key;
      setHoverPreview(preview);
    }, previewDwellMs);
  }

  // Identity of the level being drawn -- the same one the wheel's morph runs on,
  // because it is the same event: every arc is rebuilt with new geometry.
  const levelKey = map ? map.snapshotId + ":" + map.parent.id + ":" + map.offset : "";

  // A level change rebuilds every arc under a pointer that has not moved, so the
  // pointerenter that started a wedge breathing never gets its pointerleave:
  // WebKit only re-decides what is under the cursor on the next move. Nothing
  // else clears it -- loadMap resets hoveredEntry but not the arc hover, and the
  // arc hover is what breathes. So the old wedge kept breathing after a
  // drill-in, and going back up brought it into view still going, with the
  // pointer parked on the hub.
  useEffect(() => {
    setHoveredArcKey(null);
    setHoveredEntry(null);
    window.clearTimeout(previewTimer.current);
    shownPreviewKey.current = null;
    setHoverPreview(null);
  }, [levelKey]);
  const [focusedEntry, setFocusedEntry] = useState<MapEntry | null>(null);
  const [selectedEntry, setSelectedEntry] = useState<MapEntry | null>(null);
  const [staleEntry, setStaleEntry] = useState<MapEntry | null>(null);
  const [collector, setCollector] = useState<MapEntry[]>([]);
  const [advice, setAdvice] = useState<Advice | null>(null);
  const [adviceOpen, setAdviceOpen] = useState(false);
  const [adviceBusy, setAdviceBusy] = useState(false);
  // Which suggestion has its reasoning open. One at a time: the panel is a list
  // to scan, and the explanation is what you open when a row is worth deciding.
  const [adviceDetail, setAdviceDetail] = useState<number | null>(null);
  // Why the last analysis produced nothing. Without it the header read "没有结果"
  // after a failed call -- a false statement about the disk, with the real reason
  // only in a transient notice.
  const [adviceError, setAdviceError] = useState("");
  const [adviceStaged, setAdviceStaged] = useState({ added: 0, bytes: 0 });
  // The advisor round is a separate wait from the rule pass, and it must not
  // block it. Measured against deepseek-v4-flash: the rule layer is under a
  // second, the model round was 235 seconds.
  const [advisorBusy, setAdvisorBusy] = useState(false);
  const [advisorSeconds, setAdvisorSeconds] = useState(0);
  // A transport failure or a refused key, as opposed to advice.advisorError which
  // the backend reports when the round itself came back unusable. Kept apart from
  // adviceError so a failed AI round never overwrites a good rule result.
  const [advisorFault, setAdvisorFault] = useState("");
  // A four-minute wait with no moving number is indistinguishable from a hang,
  // and the first thing it costs is the user's trust that the button did anything.
  useEffect(() => {
    if (!advisorBusy) return;
    setAdvisorSeconds(0);
    const started = Date.now();
    const timer = window.setInterval(() => setAdvisorSeconds(Math.round((Date.now() - started) / 1000)), 1000);
    return () => window.clearInterval(timer);
  }, [advisorBusy]);
  const [evidence, setEvidence] = useState<EvidencePreview | null>(null);
  const [advisor, setAdvisor] = useState<AdvisorStatus | null>(null);
  const [advisorOpen, setAdvisorOpen] = useState(false);
  const [advisorForm, setAdvisorForm] = useState({ baseUrl: "https://api.deepseek.com", model: "", jsonMode: "json_object", reasoningEffort: "disabled", apiKey: "" });
  const [advisorSaving, setAdvisorSaving] = useState(false);
  // The in-flight analysis, kept so it can be cancelled. Wails returns a
  // cancellable promise, so stopping is a real cancellation of the request
  // rather than discarding a result that still gets paid for.
  const adviceCall = useRef<{ cancel: () => void } | null>(null);
  const [collectorOpen, setCollectorOpen] = useState(false);
  const [plan, setPlan] = useState<CleanupPlan | null>(null);
  const [validation, setValidation] = useState<CleanupValidation | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [mapBusy, setMapBusy] = useState(false);
  // The hub takes the colour the current node had in its parent's wheel, so a
  // drill-down keeps its identity. Unknown after a restore or a back step.
  const [centerColor, setCenterColor] = useState<string | null>(null);
  // The chip that rides under the cursor while an entry is dragged to the dock,
  // plus whether the pointer is currently over the dock. `over` drives both the
  // chip and the dock's own armed state, so the two always agree.
  const [drag, setDrag] = useState<{
    key: string;
    label: string;
    size: number;
    color: string;
    over: boolean;
    // Why this one cannot be collected, in words, or empty when it can. Known
    // before the first frame on every ring: the projection carries the reason, so
    // the dock never has to wait to answer.
    blocked: string;
  } | null>(null);
  // Where the chip is, and the node it is written to. Kept out of React state on
  // purpose: a state update per pointermove re-rendered every arc in the wheel to
  // move one small box, so the chip trailed the cursor. The pointer handler writes
  // the transform straight to the DOM now, and React re-renders only when the
  // chip's *appearance* changes -- which is when it crosses the dock's edge.
  const dragChip = useRef<HTMLDivElement | null>(null);
  const dragAt = useRef({ x: 0, y: 0 });
  // An outer ring's arc has to be looked up before it can be collected, and the
  // lookup lands a tick after the drop. Holding its key here keeps it pulled out
  // across that gap, so it does not snap back into the wheel for one frame.
  const [pendingCollect, setPendingCollect] = useState<string | null>(null);
  // A drag ends with a pointerup, and WebKit follows that with a click on
  // whatever the press started on. Without this the drop would also navigate.
  const dragSuppressesClick = useRef(false);
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
  // The arc that breathes, from either direction: the wheel sets hoverPreview on
  // any ring, the list sets hoveredEntry on the current level. The preview wins
  // while the pointer is on the wheel, because only it can name an outer ring.
  const breathingKey = hoveredArcKey ?? hoveredKey;
  const hueRange: HueBand = currentPage?.hue ?? rootHueBand;
  // Depth of the level on screen within the scanned tree: 0 is the volume root.
  // Colour is a function of this, not of the ring index (ADR-0059 SS1b).
  const baseDepth = Math.max(pageIndex, 0);
  const levelColors = useMemo(() => entryColors(entries, hueRange, baseDepth), [entries, hueRange, baseDepth]);
  // The hovered node rendered for the panel. Grey rows are the folded and virtual
  // children, matching how they are drawn on the wheel.
  const previewForList = useMemo(() => {
    if (!hoverPreview) return null;
    return {
      name: hoverPreview.name,
      size: hoverPreview.size,
      hasMore: hoverPreview.hasMore,
      color: sliceColor(hoverPreview.band.center, hoverPreview.depth),
      rows: previewRows(hoverPreview),
    };
  }, [hoverPreview]);
  const inspectedInCollector = inspectorEntry ? collector.some((item) => entryKey(item) === entryKey(inspectorEntry)) : false;
  const collectorBytes = collector.reduce((sum, item) => sum + entrySize(item), 0);
  // Staged in the dock, or on its way there while its lookup runs. These arcs
  // stay drawn: the object is still on disk until the dock's own action runs, so
  // an empty slot would overstate it, and in a space map the slot's position is
  // itself information. They are faded back and taken out of reach instead.
  const collectedKeys = useMemo(() => {
    const keys = new Set(collector.map(entryKey));
    if (pendingCollect) keys.add(pendingCollect);
    return keys;
  }, [collector, pendingCollect]);
  // In the air right now. This one really does leave the ring, because that is
  // the gesture: it slides out along its own midline and fades.
  const draggingKey = drag?.key ?? null;
  // Out of the current directory listing and out of keyboard reach, either way.
  // R-014 SS3.6 -- what is in the dock is not in the current directory, and
  // taking it back out of the dock puts it back.
  const pulledKeys = useMemo(() => {
    const keys = new Set(collectedKeys);
    if (draggingKey) keys.add(draggingKey);
    return keys;
  }, [collectedKeys, draggingKey]);
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
      // A page entered by node id arrives without a path; the map's own parent
      // carries it, so it is filled in here rather than guessed at the call site.
      const resolvedPath = target.path || next.parent?.path || "";
      const resolvedCrumbs = target.path
        ? target.crumbs
        : target.crumbs.map((crumb, index) => index === target.crumbs.length - 1 ? { ...crumb, path: resolvedPath } : crumb);
      const resolvedTarget = { ...target, offset: next.offset, path: resolvedPath, crumbs: resolvedCrumbs };
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

  useEffect(() => {
    MarmotService.GetAdvisorStatus()
      .then((status) => {
        setAdvisor(status);
        if (status.settings?.baseUrl) {
          setAdvisorForm((form) => ({
            ...form,
            baseUrl: status.settings.baseUrl || form.baseUrl,
            model: status.settings.model || form.model,
            jsonMode: status.settings.jsonMode || form.jsonMode,
            reasoningEffort: status.settings.reasoningEffort ?? form.reasoningEffort,
          }));
        }
      })
      .catch(() => undefined);
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
    setAdvice(null);
    setAdviceOpen(false);
    setAdviceDetail(null);
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

  function openDirectory(entry: MapEntry, geom?: ArcGeom) {
    const node = entryNode(entry);
    if (!node || node.kind !== "directory" || !hasCapability(entry, "enter") || !currentPage) return;
    const childBand = entryBands(entries, currentPage.hue)[entryKey(entry)] ?? currentPage.hue;
    const endAngle = childEndAngle(geom, currentPage.endAngle);
    const target: Page = {
      snapshotId: currentPage.snapshotId,
      parentId: node.id,
      path: node.path,
      offset: 0,
      crumbs: currentPage.crumbs.concat({ id: node.id, path: node.path, name: entry.name, hue: childBand, endAngle }),
      hue: childBand,
      endAngle,
    };
    void goToPage(target, "push");
  }

  // Entering a projected descendant: it carries a node id but no path, so the
  // path comes back with the map and loadMap fills it in. Its hue band is not
  // known here either — the wheel derived it while drawing — so the level keeps
  // its parent's band until the map arrives.
  // Entering an arc on an outer ring crosses several levels in one click, so
  // every level it crossed gets its own crumb -- otherwise the trail reads
  // "Macintosh HD > com_apple_MobileAsset_iOSSimulatorRuntime" for a node four
  // levels down, which is not the path the object actually has (ADR-0060 SS5c,
  // point 3 of its follow-up work).
  //
  // The chain comes from the wheel, because the wheel is what drew those levels:
  // it has each one's id, band and seam. It does not have their paths, and does
  // not invent any -- the projection carries none (ADR-0048). The intermediate
  // crumbs are labelled by name, and loadMap fills a level's real path in from
  // the store the moment that level is opened.
  function enterProjected(trail: Breadcrumb[]) {
    if (!currentPage || trail.length === 0) return;
    const target = trail[trail.length - 1];
    if (target.id <= 0) return;
    void goToPage({
      snapshotId: currentPage.snapshotId,
      parentId: target.id,
      path: target.path,
      offset: 0,
      crumbs: currentPage.crumbs.concat(trail),
      hue: target.hue,
      endAngle: target.endAngle,
    }, "push");
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

  // The chip follows the pointer, not the render. Called from the pointer handler
  // and again from a layout effect, because the element does not exist yet on the
  // move that starts the drag.
  function placeDragChip() {
    const node = dragChip.current;
    if (node) node.style.transform = "translate3d(" + dragAt.current.x + "px, " + dragAt.current.y + "px, 0)";
  }
  // Deliberately dependency-free, like the morph's repaint: the position lives
  // outside React, so it has to be re-applied after any render that mounts or
  // touches the chip.
  useLayoutEffect(placeDragChip);

  // overDock is the drop test for both drag sources: coordinates against the
  // dock's own box, not the event target, because the chip sits under the cursor
  // and the shell stops hit-testing while a drag is in flight.
  function overDock(x: number, y: number): boolean {
    const rect = collectorRef.current?.getBoundingClientRect();
    if (!rect) return false;
    return x >= rect.left - dropSlack && x <= rect.right + dropSlack
      && y >= rect.top - dropSlack && y <= rect.bottom + dropSlack;
  }

  // Both the wheel and the list drag to the dock, and both do it with pointer
  // events rather than HTML5 drag and drop: an SVG path cannot start a native
  // drag at all, and WebKit refuses to start one on any element under
  // `user-select: none`, which the whole shell sets so a drag does not select
  // the text it passes over. One path for both also means one chip and one
  // armed state, which is what the original shows.
  function beginEntryDrag(source: DragSource, event: ReactPointerEvent) {
    if (event.button !== 0) return;
    // The countdown is running against the set the plan was built from, so
    // nothing may join it: the drop would look accepted and not be deleted.
    if (countdown !== null) return;
    // Draggable means "there is an object here to talk about", not "it may be
    // collected". A protected object has to be draggable so the dock can say why
    // it is refused -- that is the whole point of the refusal (ADR-0015). A
    // projected arc says nothing until it is looked up, so all we need is an id.
    if (source.entry ? !entryNode(source.entry) : source.nodeId <= 0) return;
    const startX = event.clientX;
    const startY = event.clientY;
    const chip = {
      key: source.key,
      label: source.name,
      size: source.size,
      color: source.color,
      blocked: protectionMessage(source.protection, source.name),
    };
    let dragging = false;
    const move = (moveEvent: PointerEvent) => {
      if (!dragging && Math.hypot(moveEvent.clientX - startX, moveEvent.clientY - startY) < dragThreshold) return;
      dragging = true;
      dragAt.current = { x: moveEvent.clientX, y: moveEvent.clientY };
      placeDragChip();
      const over = overDock(moveEvent.clientX, moveEvent.clientY);
      // Returning the same object is React's own bail-out: no re-render while the
      // pointer is moving inside or outside the dock, only when it crosses.
      setDrag((current) => current && current.key === chip.key && current.over === over
        ? current
        : { ...chip, over });
    };
    const finish = (upEvent: PointerEvent) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", finish);
      window.removeEventListener("pointercancel", finish);
      setDrag(null);
      if (!dragging) return;
      // The click that follows this pointerup belongs to the drag, not to the
      // arc or row underneath it.
      dragSuppressesClick.current = true;
      window.setTimeout(() => { dragSuppressesClick.current = false; }, 0);
      if (upEvent.type !== "pointerup" || !overDock(upEvent.clientX, upEvent.clientY)) return;
      if (source.entry) toggleCollector(source.entry, "add");
      else void collectProjected(source);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", finish);
    window.addEventListener("pointercancel", finish);
  }

  // An arc below the current level was drawn from a projection: no path, no
  // capabilities, only a node id (ADR-0048). Collecting one therefore goes back
  // to the snapshot for the real entry, which is where its capabilities are
  // decided -- the frontend never invents them.
  async function collectProjected(source: DragSource) {
    const snapshotId = mapRef.current?.snapshotId ?? 0;
    if (snapshotId <= 0) return;
    setPendingCollect(source.key);
    try {
      toggleCollector(await MarmotService.GetNodeEntry(snapshotId, source.nodeId), "add");
    } catch (error) {
      setNotice("无法收集该对象：" + String(error));
    } finally {
      setPendingCollect(null);
    }
  }

  // Advice always includes the local rule layer. When an advisor is configured
  // the same call adds its suggestions, tagged by source; when one is not, the
  // rule layer is the whole answer and nothing leaves the machine.
  // Two passes, shown as they finish rather than together. The rule layer needs
  // no network and takes under a second; holding it back until a 235-second model
  // round returns meant staring at a spinner while 73.8 GB of deterministic,
  // already-computed answers sat behind it. The model is now an addition that
  // lands late, not a gate on the part that was ready.
  async function runAdvice() {
    const snapshotId = mapRef.current?.snapshotId ?? status?.snapshotId ?? 0;
    if (snapshotId <= 0) {
      setNotice("请先完成一次扫描。");
      return;
    }
    setAdviceOpen(true);
    setAdviceError("");
    setAdvisorFault("");
    setAdviceStaged({ added: 0, bytes: 0 });
    setAdviceBusy(true);
    try {
      const rules = await MarmotService.GetCleanupAdvice(snapshotId);
      setAdvice(rules);
      // The deterministic half of the answer needs no further decision from the
      // user, so it does not wait for one.
      setAdviceStaged(await stageAdviceItems((rules.items ?? []).filter(autoStageable), rules.snapshotId));
    } catch (error) {
      setAdviceError(String(error));
      setNotice("分析失败：" + String(error));
      return;
    } finally {
      setAdviceBusy(false);
    }
    if (!advisor?.configured) return;

    setAdvisorBusy(true);
    const call = MarmotService.RunAdvisorAnalysis(snapshotId);
    adviceCall.current = call;
    try {
      // The merged result contains the rule pass, so it supersedes it. Staging is
      // deliberately NOT repeated: the rule items are already in the dock, and
      // re-staging would put back anything the user removed while waiting.
      setAdvice(await call);
    } catch (error) {
      // A cancelled round is not a failure and does not need a notice.
      if (!String(error).includes("cancel")) {
        setAdvisorFault(String(error));
        setNotice("AI 分析失败：" + String(error));
      }
    } finally {
      adviceCall.current = null;
      setAdvisorBusy(false);
    }
  }

  // stageAdviceItems puts suggestions in the dock through exactly the checks a
  // manual drop goes through: a protected object, an aggregate, or one that
  // changed under us is refused whether a rule named it or a hand dragged it.
  // Staging is not authorisation -- the dock still needs the delete press, its
  // countdown, and the plan's own re-validation of every path.
  async function stageAdviceItems(items: AdviceItem[], snapshotId: number) {
    const pending = items.filter((item) => !isCollected(item));
    if (snapshotId <= 0 || pending.length === 0) return { added: 0, bytes: 0 };
    const entries = await Promise.all(
      pending.map((item) => MarmotService.GetNodeEntry(snapshotId, item.nodeId).catch(() => null)),
    );
    const staged: MapEntry[] = [];
    let bytes = 0;
    entries.forEach((entry, index) => {
      if (!entry || entry.protection || !hasCapability(entry, "collect") || !entryNode(entry)) return;
      if (staleEntry && entryKey(staleEntry) === entryKey(entry)) return;
      staged.push(entry);
      bytes += pending[index].reclaimableBytes;
    });
    if (staged.length > 0) {
      setCollector((current) => {
        const known = new Set(current.map(entryKey));
        return current.concat(staged.filter((entry) => !known.has(entryKey(entry))));
      });
    }
    return { added: staged.length, bytes };
  }

  // The bulk action for everything automatic staging deliberately left behind.
  // Explicit, on a list already on screen -- which is the difference between this
  // and pre-filling the cart with items that say "look at me".
  async function stageRemainingAdvice() {
    if (!advice) return;
    const staged = await stageAdviceItems((advice.items ?? []).filter((item) => !isCollected(item)), advice.snapshotId);
    setNotice(staged.added > 0
      ? "已加入 " + staged.added + " 项 · " + formatBytes(staged.bytes)
      : "没有可加入的项。");
  }

  function stopAdvice() {
    adviceCall.current?.cancel();
    adviceCall.current = null;
    setAdvisorBusy(false);
  }

  async function saveAdvisor() {
    setAdvisorSaving(true);
    try {
      const next = await MarmotService.ConfigureAdvisor(
        {
          provider: "openai_compatible", baseUrl: advisorForm.baseUrl, model: advisorForm.model,
          jsonMode: advisorForm.jsonMode, reasoningEffort: advisorForm.reasoningEffort,
        },
        advisorForm.apiKey,
      );
      setAdvisor(next);
      // The key is never read back, so it must not linger in the form either.
      setAdvisorForm((form) => ({ ...form, apiKey: "" }));
      setAdvisorOpen(false);
      setNotice("已连接 " + next.description);
    } catch (error) {
      setNotice("保存失败：" + String(error));
    } finally {
      setAdvisorSaving(false);
    }
  }

  async function clearAdvisor() {
    try {
      await MarmotService.ClearAdvisor();
      setAdvisor(await MarmotService.GetAdvisorStatus());
      setNotice("已断开 AI，仅使用本机规则。");
    } catch (error) {
      setNotice("清除失败：" + String(error));
    }
  }

  // A suggestion carries a node id and a path for display, and neither
  // authorises anything. Collecting one goes back to the snapshot for the real
  // entry -- the same route an arc from an outer ring takes, and the place
  // capabilities and protection are decided (ADR-0061 §1).
  // One definition, used by the row's badge and by staging. Two copies of "is
  // this already in the dock" would eventually disagree, and the visible symptom
  // would be a suggestion staged twice or a badge that lies.
  function isCollected(item: AdviceItem): boolean {
    return collector.some((entry) => entryNode(entry)?.path === item.path);
  }

  async function collectAdviceItem(item: AdviceItem) {
    const snapshotId = advice?.snapshotId ?? 0;
    if (snapshotId <= 0) return;
    try {
      toggleCollector(await MarmotService.GetNodeEntry(snapshotId, item.nodeId), "add");
    } catch (error) {
      setNotice("无法收集该对象：" + String(error));
    }
  }

  async function showEvidence() {
    const snapshotId = advice?.snapshotId ?? mapRef.current?.snapshotId ?? 0;
    if (snapshotId <= 0) return;
    try {
      setEvidence(await MarmotService.PreviewEvidence(snapshotId));
    } catch (error) {
      setNotice("无法生成证据包：" + String(error));
    }
  }

  function activateEntry(entry: MapEntry, geom?: ArcGeom) {
    if (dragSuppressesClick.current) return;
    setFocusedEntry(entry);
    setSelectedEntry(entry);
    if (entry.kind === "node" && entry.node.kind === "directory") {
      setCenterColor(levelColors[entryKey(entry)] ?? null);
      openDirectory(entry, geom);
    } else if (entry.kind !== "node") {
      expandEntry(entry);
    }
  }

  function navigateToHistory(index: number) {
    if (index < 0 || index >= pages.length || index === pageIndex) return;
    void goToPage(pages[index], "travel", index);
  }

  // Up one directory level, which is not the same as going back. They diverge as
  // soon as you enter a wedge on an outer ring: that crosses several levels at
  // once, and the breadcrumb only gains the node you clicked, so the crumb above
  // it is an ancestor several steps up. Following the crumb would jump all the
  // way there. The node's real parent is on the map itself.
  function goParent() {
    if (!currentPage || !map) return;
    const parentId = map.parent.parentId;
    if (parentId <= 0) return;
    const parentCrumb = currentPage.crumbs.length > 1
      ? currentPage.crumbs[currentPage.crumbs.length - 2]
      : null;
    // The crumb is only usable when it really is the parent; otherwise all we
    // have is the id, and the path and band come back with the map.
    const viaCrumb = parentCrumb !== null && parentCrumb.id === parentId;
    void goToPage({
      snapshotId: currentPage.snapshotId,
      parentId,
      path: viaCrumb ? parentCrumb.path : "",
      offset: 0,
      crumbs: viaCrumb
        ? currentPage.crumbs.slice(0, -1)
        : currentPage.crumbs.slice(0, -1).concat({ id: parentId, path: "", name: "", hue: currentPage.hue, endAngle: currentPage.endAngle }),
      hue: viaCrumb ? parentCrumb.hue : currentPage.hue,
      endAngle: viaCrumb ? parentCrumb.endAngle : currentPage.endAngle,
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
      endAngle: crumb.endAngle,
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
    setAdvice(null);
    setAdviceOpen(false);
    setAdviceDetail(null);
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

  // mode "add" is what a drop does: the dock only ever takes things in, so
  // dropping something already collected must not quietly remove it again.
  // Removing is the row's own cross, and the keyboard's toggle.
  function toggleCollector(entry: MapEntry | null, mode: "toggle" | "add" = "toggle") {
    if (!entry) return;
    if (staleEntry && entryKey(staleEntry) === entryKey(entry)) {
      setNotice("对象已变化，不能加入收集区。");
      return;
    }
    // Protected objects are refused with the reason the backend gave, not with a
    // generic "cannot": the point is that the user learns why.
    //
    // Only "toggle" says it here, though. "add" is a drop, and the dock has been
    // showing the prohibition sign and this same sentence for the whole drag --
    // repeating it in a notice that does not dismiss leaves the explanation on
    // screen long after the gesture it belongs to. The keyboard and the menu have
    // no dock message, so they still need it.
    if (entry.protection) {
      if (mode === "toggle") setNotice(protectionMessage(entry.protection, entry.name));
      return;
    }
    if (!hasCapability(entry, "collect") || !entryNode(entry)) {
      setNotice("聚合对象和受限对象不能加入收集区。");
      return;
    }
    setCollector((current) => {
      if (!current.some((item) => entryKey(item) === entryKey(entry))) return current.concat(entry);
      return mode === "add" ? current : current.filter((item) => entryKey(item) !== entryKey(entry));
    });
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
      const next = await MarmotService.CreateCleanupPlan({
        snapshotId: status.snapshotId,
        paths: collector.map((item) => entryNode(item)?.path ?? ""),
      });
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
      // Execution is per item: some move, some do not, and the plan state is one
      // word for the lot. Reporting that word ("failed") threw away the per-item
      // reasons that arrived in the same response -- so a run that moved 33 GB
      // and skipped one busy cache read as a total failure with no explanation.
      const results = applied.results ?? [];
      const moved = results.filter((item) => item.state === "applied");
      const stuck = results.filter((item) => item.state !== "applied");
      // Whatever moved is gone, so it must leave the dock even when something
      // else did not. Leaving it staged invites a retry that can only fail: the
      // path is no longer there to delete.
      if (moved.length > 0) {
        const movedPaths = new Set(moved.map((item) => item.path));
        setCollector((current) => current.filter((entry) => !movedPaths.has(entryNode(entry)?.path ?? "")));
        setAdvice(null);
        setAdviceOpen(false);
        setAdviceDetail(null);
      }
      if (stuck.length === 0) {
        setNotice("已删除，空间已释放，请重新扫描刷新结果");
      } else {
        setNotice(
          `已处理 ${moved.length} 项，${stuck.length} 项未执行：` +
            stuck.slice(0, 3).map((item) => `${item.path.split("/").pop()}（${item.reason}）`).join("；") +
            (stuck.length > 3 ? ` 等 ${stuck.length} 项` : ""),
        );
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

  // Entries reach the dock through beginEntryDrag, not through HTML5 drag and
  // drop. What arrives here is therefore a Finder drop, and a dropped path is
  // not cleanup authorisation (R-012 SS4.4) -- so it is swallowed rather than
  // left to bubble up to the shell, where it would re-root the scan.
  function handleCollectorDrop(event: ReactDragEvent<HTMLElement>) {
    event.preventDefault();
    event.stopPropagation();
    // Anything without files is one of the dock's own rows being dragged back
    // over the dock, which is not a Finder drop and needs no explanation.
    if (!event.dataTransfer.types.includes("Files")) return;
    setNotice("收集区只接受扫描结果中的对象，不接受从 Finder 拖入的路径");
  }

  // A collected object is out of the current directory (R-014 SS3.6) and its row
  // is collapsed to nothing, so keyboard focus must step over it: land on the
  // wanted row, or the nearest one still in the list, looking the way the key was
  // going first.
  function settleFocus(target: number, direction: number): number {
    for (let index = target; index >= 0 && index < entries.length; index += direction) {
      if (!pulledKeys.has(entryKey(entries[index]))) return index;
    }
    for (let index = target; index >= 0 && index < entries.length; index -= direction) {
      if (!pulledKeys.has(entryKey(entries[index]))) return index;
    }
    return -1;
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
        let direction = 1;
        if (event.key === "ArrowDown" || event.key === "ArrowRight") nextIndex = Math.min(entries.length - 1, nextIndex + 1);
        if (event.key === "ArrowUp" || event.key === "ArrowLeft") { nextIndex = Math.max(0, nextIndex - 1); direction = -1; }
        if (event.key === "PageDown") nextIndex = Math.min(entries.length - 1, nextIndex + Math.max(1, Math.floor(entries.length / 8)));
        if (event.key === "PageUp") { nextIndex = Math.max(0, nextIndex - Math.max(1, Math.floor(entries.length / 8))); direction = -1; }
        if (event.key === "Home") nextIndex = 0;
        if (event.key === "End") { nextIndex = entries.length - 1; direction = -1; }
        const settled = settleFocus(nextIndex, direction);
        if (settled < 0) return;
        setFocusedEntry(entries[settled]);
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
    <div className={"app-shell " + (showResult ? "app-shell-result" : "app-shell-source") + (drag ? " is-dragging" : "")} onDragOver={(event) => event.preventDefault()} onDrop={handleDrop}>
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
                  {breadcrumbLabel(crumb, index)}
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
                  onDragEntry={beginEntryDrag}
                  collectedKeys={collectedKeys}
                  draggingKey={draggingKey}
                  hueRange={hueRange}
                  baseDepth={baseDepth}
                  levelEndAngle={currentPage?.endAngle ?? sunburstEndAngle}
                  onEnterProjected={enterProjected}
                  centerColor={centerColor}
                  map={map}
                  hoveredKey={hoveredKey}
                  onHoverArc={hoverArc}
                  breathingKey={breathingKey}
                  focusedKey={focusedKey}
                  selectedKey={selectedKey}
                  onHover={setHoveredEntry}
                  onFocus={setFocusedEntry}
                  onActivate={activateEntry}
                  onPreview={(entry) => void previewEntry(entry)}
                  onReveal={(entry) => void revealEntry(entry)}
                  onGoParent={goParent}
                />
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
              preview={previewForList}
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
              onDragEntry={beginEntryDrag}
              pulledKeys={pulledKeys}
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
      {drag && (
        /* The chip is the collected row, drawn early: same dot, name and size,
           so what you drag looks like what lands in the dock. */
        <div
          ref={dragChip}
          className={"drag-chip" + (drag.blocked ? " is-refused" : drag.over ? " is-over" : "")}
          style={{ background: drag.color, color: readableOn(drag.color) }}
        >
          <span className="drag-chip-name">{drag.label}</span>
          <span className="drag-chip-size">{formatBytes(drag.size)}</span>
        </div>
      )}
      {notice && <div className="notice" role="status">{notice}</div>}

      {showResult && <section
        ref={collectorRef}
        className={"collector-dock" + (collectorOpen ? " is-open" : "")
          + (drag ? " is-target" : "")
          + (drag?.over && !drag.blocked ? " is-armed" : "")
          + (drag?.blocked ? " is-refused" : "")}
        onDragOver={(event) => event.preventDefault()}
        onDrop={handleCollectorDrop}
        data-testid="collector"
      >
        {collector.length === 0 ? (
          /* Nothing collected: the reference shows only the drop ring and one
             line of instruction. */
          <button className="collector-empty-state" onClick={() => setCollectorOpen((open) => !open)}>
            <span className="collector-target" aria-hidden="true" />
            {/* While a protected object is in the air the dock stops inviting the
                drop and says why it will not take it -- the reference does the
                same, and it does it for the whole drag rather than only once the
                pointer is over the ring. */}
            <span className="collector-caption">{drag?.blocked || "将文件拖放至此，以收集要删除的文件"}</span>
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
                    {/* The dot and the cross live *inside* the button rather than
                        beside it. Stacked as siblings in one grid cell they were
                        ambiguous about which one a press landed on, and the dot won
                        -- measured: a press dead centre on the cross reported
                        `collector-dot` as its target, so the cross did nothing. One
                        element owns the cell now, and whatever is painted in it is
                        a child of the thing being pressed.
                        pointerdown rather than click, with preventDefault, because
                        the row around it is draggable so it can go out to Finder and
                        a press here would otherwise start that drag and eat the
                        click. Deliberately not also on click: toggleCollector would
                        run twice and put the item straight back, so Enter and Space
                        are handled explicitly. */}
                    <button
                      className="collector-remove"
                      draggable={false}
                      onPointerDown={(event) => {
                        event.preventDefault();
                        event.stopPropagation();
                        toggleCollector(item);
                      }}
                      onKeyDown={(event) => {
                        if (event.key !== "Enter" && event.key !== " ") return;
                        event.preventDefault();
                        toggleCollector(item);
                      }}
                      aria-label={"移除 " + item.name}
                    >
                      <span className="collector-dot" style={{ background: levelColors[entryKey(item)] ?? "#7fb96a" }} aria-hidden="true" />
                      <span className="collector-cross" aria-hidden="true">×</span>
                    </button>
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
                {drag?.blocked
                  ? drag.blocked
                  : countdown !== null
                    ? <>秒后开始。选中的文件将<strong className="destructive-note">移入废纸篓</strong></>
                    : plan?.state === "confirmed"
                      ? "正在移入废纸篓…"
                      : validation && !validation.valid
                        ? "校验未通过，不能执行"
                        : formatBytes(collectorBytes).split(" ")[1] + " 已收集"}
              </span>
              {/* One action, and it deletes outright -- the trash is on the same
                  volume, so moving there reclaims nothing. Nothing is rerouted and
                  nothing is undoable; the countdown is the whole confirmation. */}
              {countdown === null
                ? <button className="danger-button compact is-permanent" onClick={() => void createPlan()}>删除</button>
                : <button className="quiet-button" onClick={stopCountdown}>停止</button>}
            </div>
          </div>
        )}
      </section>}

      {/* The dock sits bottom-left (ADR-0018), so the advice entry takes the
          opposite corner. The two are a pair: this side proposes, that side
          stages, and a suggestion crosses between them the same way an arc does
          -- through the snapshot, never by handing a path straight to a delete. */}
      {showResult && (
        <div className={"advice-corner" + (adviceOpen ? " is-open" : "")}>
          {adviceOpen && (
            <section className="advice-panel" aria-label="可清理项">
              <header className="advice-head">
                <div>
                  <p className="eyebrow">可清理项</p>
                  <h3>
                    {adviceBusy
                      ? "分析中…"
                      : adviceError
                        ? "分析失败"
                        : advice
                          ? formatBytes(advice.totalBytes) + " 可回收"
                          : "没有结果"}
                  </h3>
                </div>
                <div className="advice-head-actions">
                  {/* Only the advisor round is stoppable. The rule pass is under a
                      second, so a stop button on it would be decoration. */}
                  {advisorBusy
                    ? <button className="quiet-button" onClick={stopAdvice}>停止 AI</button>
                    : <button className="quiet-button" onClick={() => setAdviceOpen(false)} aria-label="收起">收起</button>}
                </div>
              </header>

              {!adviceBusy && advice && (advice.items ?? []).length > 0 && (
                <div className="advice-stage">
                  <span>
                    {stageSummary(
                      adviceStaged.added,
                      formatBytes(adviceStaged.bytes),
                      (advice.items ?? []).filter((item) => !isCollected(item)).length,
                    )}
                    {advisorBusy && (
                      <><br /><span className="advice-waiting">
                        AI 仍在分析（已 {advisorSeconds} 秒，上次实测约 235 秒）。上面这些来自本机规则，现在就可以用。
                      </span></>
                    )}
                    {!advisorBusy && advisorFault && <><br /><span className="advice-fault">AI 未完成：{advisorFault}</span></>}
                  </span>
                  {(advice.items ?? []).some((item) => !isCollected(item)) && (
                    <button className="quiet-button" onClick={() => void stageRemainingAdvice()}>
                      加入其余 {(advice.items ?? []).filter((item) => !isCollected(item)).length} 项
                    </button>
                  )}
                </div>
              )}

              <div className="advice-list">
                {adviceBusy && <p className="advice-empty">正在读取扫描结果…</p>}
                {!adviceBusy && adviceError && <p className="advice-empty advice-fault">{adviceError}</p>}
                {!adviceBusy && !adviceError && advice && (advice.items ?? []).length === 0 && (
                  <p className="advice-empty">没有找到可清理的对象。</p>
                )}
                {!adviceBusy && (advice?.items ?? []).map((item) => {
                  const open = adviceDetail === item.nodeId;
                  const collected = isCollected(item);
                  return (
                    <article key={item.nodeId} className={"advice-item risk-" + item.risk}>
                      <button
                        className="advice-summary"
                        onClick={() => setAdviceDetail(open ? null : item.nodeId)}
                        aria-expanded={open}
                      >
                        <span className="advice-risk" aria-hidden="true" />
                        <span className="advice-text">
                          <strong>{item.name}</strong>
                          <span className="advice-path">{homePath(item.path)}</span>
                        </span>
                        <span className="advice-size">{formatBytes(item.reclaimableBytes)}</span>
                      </button>
                      <div className="advice-tags">
                        <span className="advice-tag">{item.ruleName || item.category}</span>
                        {item.source === "advisor" && <span className="advice-tag is-ai">AI · {Math.round(item.confidence * 100)}%</span>}
                        {/* Recoverability leads. It is the axis that decides
                            whether a suggestion is frightening: reinstalling a
                            toolchain costs a download, losing a photo library
                            costs the photos. Risk follows it. */}
                        <span className={"advice-tag is-recovery recovery-" + item.recovery}>
                          {recoveryLabels[item.recovery] ?? item.recovery}
                        </span>
                        <span className="advice-tag">{riskLabels[item.risk] ?? item.risk}</span>
                        <button
                          className="advice-collect"
                          disabled={collected}
                          onClick={() => void collectAdviceItem(item)}
                        >
                          {collected ? "已收集" : "加入收集区"}
                        </button>
                      </div>
                      {open && (
                        <dl className="advice-detail">
                          <dt>依据</dt>
                          <dd>{(item.evidence ?? []).join(" · ") || "—"}</dd>
                          <dt>删除后</dt>
                          <dd>{item.whatBreaks}</dd>
                          <dt>如何恢复</dt>
                          <dd>{item.howToRestore}</dd>
                        </dl>
                      )}
                    </article>
                  );
                })}
              </div>

              {advice && !adviceBusy && (
                <footer className="advice-foot">
                  <span>
                    {advice.rounds > 0
                      ? <>规则 {advice.ruleItems} 条 · AI {advice.advisorItems} 条（{advice.rounds} 轮
                        {advice.expanded > 0 ? "，深挖 " + advice.expanded + " 处" : ""}，
                        {(advice.inputTokens + advice.outputTokens).toLocaleString()} token）</>
                      : <>本轮全部来自本机规则，未联网。</>}
                    {" "}证据 {advice.evidenceNodes} 个节点 / {formatBytes(advice.evidenceBytes)} · 下限{" "}
                    {formatBytes(advice.floorBytes)}
                    {advice.correctionSummary && <><br /><span className="advice-fault">{advice.correctionSummary}</span></>}
                    {advice.rejectedSummary && <><br />已丢弃：{advice.rejectedSummary}</>}
                    {advice.advisorError && <><br /><span className="advice-fault">{advice.advisorError}</span></>}
                  </span>
                  <button className="quiet-button" onClick={() => void showEvidence()}>查看发送内容</button>
                </footer>
              )}
            </section>
          )}

          {/* Beside the corner button rather than inside the results panel:
              opening settings must not require running an analysis first. It is
              only on the result page because the source window is 151pt tall and
              a sheet bounded by that window would be an unusable sliver. */}
          <div className="advice-corner-actions">
            <button
              className="advice-button is-icon"
              onClick={() => setAdvisorOpen(true)}
              title={advisor?.configured ? "AI 设置 · " + advisor.description : "AI 设置（未连接）"}
              aria-label="AI 设置"
            >
              <span className={"advice-gear" + (advisor?.configured ? " is-on" : "")} aria-hidden="true">AI</span>
            </button>
            <button
              className="advice-button"
              onClick={() => (adviceOpen ? setAdviceOpen(false) : void runAdvice())}
              disabled={adviceBusy || advisorBusy}
            >
              {adviceBusy
                ? "读取中…"
                : advisorBusy
                  ? "AI " + advisorSeconds + "s"
                  : advisor?.configured ? "AI 分析" : "分析可清理项"}
            </button>
          </div>
        </div>
      )}

      {advisorOpen && (
        <div className="evidence-scrim" role="dialog" aria-modal="true" onClick={() => setAdvisorOpen(false)}>
          <div className="advisor-sheet" onClick={(event) => event.stopPropagation()}>
            <header>
              <div>
                <p className="eyebrow">AI 设置</p>
                <h3>{advisor?.configured ? advisor.description : "未连接"}</h3>
              </div>
              <button className="quiet-button" onClick={() => setAdvisorOpen(false)}>关闭</button>
            </header>
            <div className="advisor-body">
              {/* Any endpoint speaking the OpenAI chat-completions protocol works
                  here: DeepSeek, Kimi, Qwen, OpenRouter, or a local vLLM/Ollama.
                  That is why there is no provider dropdown. */}
              <label>
                <span>Endpoint</span>
                <input
                  value={advisorForm.baseUrl}
                  spellCheck={false}
                  onChange={(event) => setAdvisorForm((form) => ({ ...form, baseUrl: event.target.value }))}
                  placeholder="https://api.deepseek.com"
                />
              </label>
              <label>
                <span>模型</span>
                <input
                  value={advisorForm.model}
                  spellCheck={false}
                  onChange={(event) => setAdvisorForm((form) => ({ ...form, model: event.target.value }))}
                  placeholder="模型 id"
                />
              </label>
              <label>
                <span>API Key</span>
                <input
                  type="password"
                  value={advisorForm.apiKey}
                  spellCheck={false}
                  onChange={(event) => setAdvisorForm((form) => ({ ...form, apiKey: event.target.value }))}
                  placeholder={advisor?.hasKey ? "已保存，留空则保持不变" : "sk-…"}
                />
              </label>
              <label>
                <span>JSON 约束</span>
                <select
                  value={advisorForm.jsonMode}
                  onChange={(event) => setAdvisorForm((form) => ({ ...form, jsonMode: event.target.value }))}
                >
                  <option value="json_object">json_object（DeepSeek 等多数服务）</option>
                  <option value="json_schema">json_schema（部分服务，DeepSeek 不支持）</option>
                  <option value="">不约束（仅靠提示词）</option>
                </select>
              </label>
              <label>
                <span>推理强度</span>
                <select
                  value={advisorForm.reasoningEffort}
                  onChange={(event) => setAdvisorForm((form) => ({ ...form, reasoningEffort: event.target.value }))}
                >
                  {/* Reasoning models default to a high effort. This task is
                      classification against a fixed output contract, and the
                      measured cost of the default was 239s spent thinking and an
                      answer cut off at the output cap. The numbers on each option
                      are real: one identical pack, deepseek-v4-flash, R-063 §4e.
                      They are here because the choice is a trade, and a trade
                      cannot be made from a word like "low". */}
                  <option value="disabled">关闭思考（实测 34s，AI 给出 22 条，更激进）</option>
                  <option value="low">low（实测 118–191s，AI 给出 6–7 条，更保守）</option>
                  <option value="high">high（服务端默认，更慢更贵）</option>
                  <option value="max">max</option>
                  <option value="omit">不发送该字段（非 DeepSeek 服务）</option>
                </select>
              </label>
              {advisor?.fault && <p className="advisor-fault">{advisor.fault}</p>}
              <p className="advisor-note">
                Key 加密保存在应用自己的目录里（AES-256-GCM，密钥绑定本机，
                文件 0600），不写进日志或快照。同机上以你的身份运行的程序仍可解开它。
                只有你点击「AI 分析」时才会发起网络请求，应用不做任何其他出网。
                发送内容可在面板底部的「查看发送内容」中逐字节查看。
              </p>
            </div>
            <footer className="advisor-foot">
              {advisor?.configured && (
                <button className="quiet-button" onClick={() => void clearAdvisor()}>断开</button>
              )}
              <button className="advice-button" disabled={advisorSaving || !advisorForm.model.trim()} onClick={() => void saveAdvisor()}>
                {advisorSaving ? "保存中…" : "保存并连接"}
              </button>
            </footer>
          </div>
        </div>
      )}

      {/* One rendering serves both the payload and this preview, so what is
          shown here cannot drift from what would be sent. */}
      {evidence && (
        <div className="evidence-scrim" role="dialog" aria-modal="true" onClick={() => setEvidence(null)}>
          <div className="evidence-sheet" onClick={(event) => event.stopPropagation()}>
            <header>
              <div>
                <p className="eyebrow">将要发送的内容</p>
                <h3>{evidence.nodes} 个节点 · {formatBytes(evidence.bytes)} · 下限 {formatBytes(evidence.floorBytes)}</h3>
              </div>
              <button className="quiet-button" onClick={() => setEvidence(null)}>关闭</button>
            </header>
            <pre className="evidence-text">{evidence.text}</pre>
          </div>
        </div>
      )}
    </div>
  );
}
