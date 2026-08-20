import { useEffect, useRef, useState } from "react";
import type { DragEvent as ReactDragEvent } from "react";
import { arc } from "d3-shape";
import { Dialogs, Events } from "@wailsio/runtime";
import { Service as MarmotService } from "../bindings/example.com/marmot/internal/presentation/wails";
import type * as Models from "../bindings/example.com/marmot/internal/presentation/wails/models";

type NodeView = Models.NodeView;
type MapEntry = Models.MapEntry;
type MapResult = Models.MapResult;
type ScanStatus = Models.ScanStatus;
type ScanProgress = Models.ScanProgress;
type PermissionStatus = Models.PermissionStatus;
type VolumeOverview = Models.VolumeOverview;
type CleanupPlan = Models.CleanupPlan;
type CleanupValidation = Models.CleanupValidation;
type Breadcrumb = { id: number; path: string };
type Capability = "enter" | "preview" | "reveal" | "collect" | "rescan";
type NavigationMode = "push" | "replace" | "travel";
type Page = {
  snapshotId: number;
  parentId: number;
  path: string;
  offset: number;
  crumbs: Breadcrumb[];
};

const defaultRoot = "/";
const pageSize = 256;
const maxHistory = 32;
const phaseLabels: Record<string, string> = {
  catalog: "准备卷",
  volume_overview: "读取概览",
  top_level_publish: "发布首层",
  deep_scan: "深入扫描",
  finalize: "整理结果",
};
const stateLabels: Record<string, string> = {
  running: "扫描中",
  completed: "已完成",
  completed_with_issues: "部分完成",
  cancelled: "已取消",
  interrupted: "上次中断",
  failed: "失败",
};
const virtualLabels: Record<string, string> = {
  smaller_objects: "较小对象",
  hidden_space: "隐藏空间",
  purgeable_space: "可清理空间",
  other_volumes: "其他卷",
  snapshot: "系统快照",
  restricted: "受限空间",
};
const palette = ["#53c9bc", "#ed8b6b", "#e9bd62", "#729de2", "#bf87d2", "#70bd91", "#df789a", "#89a6a9"];

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return (size >= 10 || unit === 0 ? size.toFixed(0) : size.toFixed(1)) + " " + units[unit];
}

function formatPercent(used: number, total: number): string {
  return total ? Math.min(100, Math.max(0, (used / total) * 100)).toFixed(0) + "%" : "0%";
}

function stateLabel(state: string): string {
  return stateLabels[state] ?? state;
}

function confidenceLabel(confidence: string): string {
  return ({ exact: "精确", estimated: "估算", partial: "部分结果", unknown: "未知" } as Record<string, string>)[confidence] ?? "待确认";
}

function displayStateLabel(state: string): string {
  return ({ current: "当前快照", stale: "对象已变化", partial: "部分结果" } as Record<string, string>)[state] ?? "待确认";
}

function entryNode(entry: MapEntry | null): NodeView | null {
  return entry?.kind === "node" && entry.node?.id > 0 ? entry.node : null;
}

function entryKey(entry: MapEntry): string {
  if (entry.kind === "node") return "node:" + entry.node.id;
  return entry.kind + ":" + (entry.virtualType || "unknown") + ":" + entry.name;
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

function entryKindLabel(entry: MapEntry): string {
  if (entry.kind === "aggregate") return virtualLabels[entry.virtualType] ?? "聚合对象";
  if (entry.kind === "virtual") return virtualLabels[entry.virtualType] ?? "解释对象";
  if (entry.node.kind === "directory") return "文件夹";
  if (entry.node.kind === "symlink") return "符号链接";
  return "文件";
}

function entryPath(entry: MapEntry): string {
  return entryNode(entry)?.path ?? (entry.virtualType ? virtualLabels[entry.virtualType] ?? entry.name : entry.name);
}

function firstEntry(map: MapResult): MapEntry | null {
  return (map.entries ?? [])[0] ?? null;
}

function rootPage(snapshotId: number, path: string): Page {
  return { snapshotId, parentId: 1, path, offset: 0, crumbs: [{ id: 1, path }] };
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
}) {
  const entries = (map?.entries ?? []).filter(Boolean);
  const innerRadius = 47;
  const ringThickness = 45;
  const maxDepth = 5;
  const pathRefs = useRef<Record<string, SVGPathElement | null>>({});
  const slices: Array<{ entry: MapEntry; key: string; renderKey: string; rootIndex: number; depth: number; path: string }> = [];
  const paletteFor = (rootIndex: number, depth: number) => palette[rootIndex % palette.length];
  const appendLevel = (levelEntries: MapEntry[], startAngle: number, endAngle: number, depth: number, rootIndex: number) => {
    if (depth >= maxDepth || levelEntries.length === 0) return;
    const levelTotal = levelEntries.reduce((sum, entry) => sum + entrySize(entry), 0) || levelEntries.length;
    const makeArc = arc<unknown>().innerRadius(innerRadius + depth * ringThickness).outerRadius(innerRadius + (depth + 1) * ringThickness - 3);
    let cursor = startAngle;
    levelEntries.forEach((entry, index) => {
      const weight = entrySize(entry) || 1;
      const next = cursor + (weight / levelTotal) * (endAngle - startAngle);
      const key = entryKey(entry);
      const renderKey = key + ":" + depth + ":" + slices.length;
      slices.push({ entry, key, renderKey, rootIndex, depth, path: makeArc({ startAngle: cursor, endAngle: next }) ?? "" });
      const children = (entry.children ?? []).filter(Boolean);
      if (children.length > 0 && entry.kind === "node" && entry.node.kind === "directory") {
        appendLevel(children, cursor, next, depth + 1, rootIndex);
      }
      cursor = next;
      if (index === levelEntries.length - 1) cursor = endAngle;
    });
  };
  appendLevel(entries, -Math.PI / 2, Math.PI * 1.5, 0, 0);

  useEffect(() => {
    if (focusedKey && pathRefs.current[focusedKey]) pathRefs.current[focusedKey]?.focus();
  }, [focusedKey]);

  return (
    <div className="sunburst-wrap" aria-label="空间图">
      <svg className="sunburst" viewBox="0 0 600 600" role="img">
        <g transform="translate(300 300)">
          {slices.map(({ entry, key, renderKey, rootIndex, depth, path }) => {
            const aggregate = entry.kind === "aggregate";
            const virtual = entry.kind === "virtual";
            const stale = entry.displayState === "stale";
            const canActivate = !aggregate || depth === 0;
            const selected = key === selectedKey;
            const focused = key === focusedKey;
            const hovered = key === hoveredKey;
            const color = aggregate || virtual ? "#62666d" : paletteFor(rootIndex, depth);
            return (
              <path
                key={renderKey}
                ref={(node) => {
                  pathRefs.current[key] = node;
                  if (node) node.setAttribute("draggable", String(entry.kind === "node" && hasCapability(entry, "collect")));
                }}
                d={path}
                role="button"
                tabIndex={0}
                className={
                  "sunburst-slice" +
                  " depth-" + depth +
                  (selected ? " is-selected" : "") +
                  (focused ? " is-focused" : "") +
                  (hovered ? " is-hovered" : "") +
                  (aggregate ? " is-aggregate" : "") +
                  (virtual ? " is-virtual" : "") +
                  (stale ? " is-stale" : "")
                }
                fill={color}
                aria-label={entry.name + "，" + formatBytes(entrySize(entry))}
                onPointerEnter={() => onHover(entry)}
                onPointerLeave={() => onHover(null)}
                onFocus={() => onFocus(entry)}
                onDragStart={(event) => {
                  event.dataTransfer.setData("application/marmot-entry", JSON.stringify(entry));
                  event.dataTransfer.effectAllowed = "copy";
                }}
                onClick={(event) => {
                  if (!canActivate) return;
                  if (event.metaKey || event.ctrlKey) {
                    event.preventDefault();
                    onReveal(entry);
                    return;
                  }
                  onActivate(entry);
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    event.stopPropagation();
                    if (canActivate) onActivate(entry);
                  } else if (event.key === " ") {
                    event.preventDefault();
                    event.stopPropagation();
                    onPreview(entry);
                  }
                }}
              />
            );
          })}
          <circle className="sunburst-center" r={innerRadius - 8} role="button" tabIndex={0} onClick={onGoParent} onKeyDown={(event) => { if (event.key === "Enter") onGoParent(); }} />
          <text className="sunburst-center-label" textAnchor="middle" y="-7">{formatBytes(map?.parent.ownedAllocated ?? 0)}</text>
          <text className="sunburst-center-value" textAnchor="middle" y="14">{map?.parent.name ?? "空间图"}</text>
        </g>
      </svg>
      {!map && <div className="chart-placeholder">完成一次扫描后显示空间图</div>}
    </div>
  );
}

function VolumeTile({ volume, onScan }: { volume: VolumeOverview; onScan: (path: string) => void }) {
  const ratio = volume.totalBytes ? Math.min(100, (volume.usedBytes / volume.totalBytes) * 100) : 0;
  return (
    <article className={"volume-tile" + (volume.scannable ? "" : " is-disabled")}>
      <div className="volume-tile-head">
        <div className="volume-icon">{volume.path === "/" ? "HD" : "V"}</div>
        <div className="volume-name">
          <strong>{volume.name}</strong>
          <span>{volume.path} · {volume.kind}</span>
        </div>
        <span className={"volume-permission " + volume.permission}>{volume.permission === "available" ? "可访问" : volume.permission}</span>
      </div>
      <div className="volume-gauge" aria-label={"已使用 " + formatPercent(volume.usedBytes, volume.totalBytes)}><span style={{ width: ratio + "%" }} /></div>
      <div className="volume-stats">
        <span><strong>{formatBytes(volume.usedBytes)}</strong> 已用</span>
        <span><strong>{formatBytes(volume.freeBytes)}</strong> 可用</span>
        <span>{formatBytes(volume.totalBytes)} 总计</span>
      </div>
      <div className="volume-tile-foot">
        <span>{volume.message}</span>
        <button className="text-button" onClick={() => onScan(volume.path)} disabled={!volume.scannable}>扫描</button>
      </div>
    </article>
  );
}

function DirectoryList({
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
        <div className="directory-title">
          <span className="directory-parent-dot" />
          <div>
            <h2>{parent?.name ?? "当前目录"}</h2>
            <strong>{formatBytes(total)}</strong>
          </div>
        </div>
        <span className="directory-confidence">{confidenceLabel(map?.confidence ?? parent?.confidence ?? "unknown")}</span>
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
              <span className={"directory-dot " + kindClass} />
              <span className="directory-name">{entry.name}</span>
              {entry.kind === "aggregate" && <span className="directory-tag">{entry.count.toLocaleString()} 项</span>}
              <span className="directory-size">{formatBytes(entrySize(entry))}</span>
            </div>
          );
        })}
      </div>
      <div className="directory-context">
        {contextEntry ? (
          <>
            <div className="context-line">
              <span className={"directory-dot " + (contextEntry.kind === "node" && contextEntry.node.kind === "directory" ? "directory" : contextEntry.kind === "node" ? "file" : "virtual")} />
              <strong>{contextEntry.name}</strong>
              <span>{formatBytes(entrySize(contextEntry))}</span>
              <small>{entryKindLabel(contextEntry)}</small>
            </div>
            <div className="context-actions">
              {hasCapability(contextEntry, "preview") && <button className="context-button" onClick={() => onPreview(contextEntry)}>Quick Look</button>}
              {hasCapability(contextEntry, "reveal") && <button className="context-button" onClick={() => onReveal(contextEntry)}>Finder</button>}
              {hasCapability(contextEntry, "enter") && <button className="context-button" onClick={() => onEnter(contextEntry)}>{contextEntry.kind === "aggregate" ? "展开" : "进入"}</button>}
              {hasCapability(contextEntry, "collect") && <button className={"context-button" + (inCollector ? " is-added" : "")} onClick={() => onCollect(contextEntry)}>{inCollector ? "移出 Collector" : "加入 Collector"}</button>}
            </div>
            {contextEntry.displayState !== "current" && <p className="context-warning">{displayStateLabel(contextEntry.displayState)}，当前对象需要重新读取。</p>}
          </>
        ) : (
          <p className="directory-hint">单击文件夹进入，单击文件选中；按 Space 可预览当前文件。</p>
        )}
      </div>
      <div className="directory-footer">
        <span>{map?.hasMore ? "较小对象已聚合" : "当前层已完整显示"}</span>
        <span>{map?.projectionTruncated ? "空间图为部分投影" : "多层空间图"}</span>
      </div>
    </aside>
  );
}

export default function App() {
  const [permission, setPermission] = useState<PermissionStatus | null>(null);
  const [volumes, setVolumes] = useState<VolumeOverview[]>([]);
  const [root, setRoot] = useState(defaultRoot);
  const [status, setStatus] = useState<ScanStatus | null>(null);
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
  const mapRequest = useRef(0);
  const refreshTimer = useRef<number | undefined>(undefined);
  const mapRef = useRef<MapResult | null>(null);
  const pageRef = useRef<Page | null>(null);
  const navigationRef = useRef({ pages: [] as Page[], index: -1 });
  const rootRef = useRef(defaultRoot);
  const statusRef = useRef<ScanStatus | null>(null);
  const loadMapRef = useRef<((target: Page, mode?: NavigationMode, targetIndex?: number) => Promise<boolean>) | null>(null);

  const scanActive = status?.state === "running";
  const currentPage = pages[pageIndex] ?? null;
  const currentParent = map?.parent ?? null;
  const entries = map?.entries ?? [];
  const currentKey = staleEntry ? entryKey(staleEntry) : null;
  const inspectorEntry = staleDisplayEntry(hoveredEntry ?? focusedEntry ?? selectedEntry, currentKey);
  const selectedKey = selectedEntry ? entryKey(selectedEntry) : null;
  const focusedKey = focusedEntry ? entryKey(focusedEntry) : null;
  const hoveredKey = hoveredEntry ? entryKey(hoveredEntry) : null;
  const inspectedInCollector = inspectorEntry ? collector.some((item) => entryKey(item) === entryKey(inspectorEntry)) : false;
  const collectorBytes = collector.reduce((sum, item) => sum + entrySize(item), 0);
  const mapTotal = map?.parent.ownedAllocated ?? status?.bytes ?? 0;
  const mapConfidence = map?.confidence || map?.parent.confidence || "unknown";

  mapRef.current = map;
  pageRef.current = currentPage;
  navigationRef.current = { pages, index: pageIndex };
  rootRef.current = root;
  statusRef.current = status;

  async function loadVolumes() {
    try {
      setVolumes((await MarmotService.GetVolumes()) ?? []);
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

  async function loadMap(target: Page, mode: NavigationMode = "replace", targetIndex?: number): Promise<boolean> {
    if (target.snapshotId <= 0 || target.parentId <= 0) return false;
    const request = ++mapRequest.current;
    setMapBusy(true);
    try {
      const next = await MarmotService.GetMap({ snapshotId: target.snapshotId, parentId: target.parentId, limit: pageSize, offset: target.offset, measure: "owned_allocated", depth: 3, projectionLimit: 384 });
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

  function scheduleMapRefresh(event: ScanProgress) {
    const currentMap = mapRef.current;
    if (event.snapshotId <= 0 || (currentMap && event.snapshotId !== currentMap.snapshotId)) return;
    if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
    if (event.nodes === 0 && event.state === "running") return;
    const current = pageRef.current ?? rootPage(event.snapshotId, event.root || rootRef.current);
    const target = { ...current, snapshotId: event.snapshotId };
    refreshTimer.current = window.setTimeout(() => {
      if (loadMapRef.current) void loadMapRef.current(target, "replace");
    }, 250);
  }

  useEffect(() => {
    void loadVolumes();
    const savedTaskId = window.localStorage.getItem("marmot.scanTaskId");
    if (savedTaskId) {
      MarmotService.GetScanStatus(savedTaskId)
        .then((next) => {
          setStatus(next);
          setRoot(next.root);
          void loadMap(rootPage(next.snapshotId, next.root));
        })
        .catch(() => window.localStorage.removeItem("marmot.scanTaskId"));
    }
    MarmotService.GetPermissionStatus().then(setPermission).catch((error: unknown) => setNotice(String(error)));
    const off = Events.On("scan-progress", (event: { data: ScanProgress }) => {
      setStatus(statusFromProgress(event.data));
      scheduleMapRefresh(event.data);
      if (event.data.state !== "running") void loadVolumes();
    });
    return () => {
      off();
      if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
    };
  }, []);

  async function startScan(nextRoot = root) {
    setBusy(true);
    setNotice("");
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
      setRoot(next.root);
      setStatus(next);
      void loadMap(rootPage(next.snapshotId, next.root));
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

  async function goToPage(target: Page, mode: NavigationMode, targetIndex?: number) {
    await loadMap(target, mode, targetIndex);
  }

  function openDirectory(entry: MapEntry) {
    const node = entryNode(entry);
    if (!node || node.kind !== "directory" || !hasCapability(entry, "enter") || !currentPage) return;
    const target: Page = {
      snapshotId: currentPage.snapshotId,
      parentId: node.id,
      path: node.path,
      offset: 0,
      crumbs: currentPage.crumbs.concat({ id: node.id, path: node.path }),
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

  function activateEntry(entry: MapEntry) {
    setFocusedEntry(entry);
    setSelectedEntry(entry);
    if (entry.kind === "node" && entry.node.kind === "directory") {
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
    }, "push");
  }

  function refreshCurrent() {
    if (currentPage) void goToPage(currentPage, "replace");
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
      setCollectorOpen(true);
    } catch (error) {
      setNotice(String(error));
    }
  }

  async function confirmPlan() {
    if (!plan || !validation?.valid) return;
    try {
      setPlan(await MarmotService.ConfirmCleanupPlan(plan.id, plan.version));
    } catch (error) {
      setNotice(String(error));
    }
  }

  async function executePlan() {
    if (!plan || plan.state !== "confirmed") return;
    try {
      const applied = await MarmotService.ExecuteCleanupPlan(plan.id, plan.version);
      setPlan(applied);
      setNotice(applied.state === "applied" ? "已移入废纸篓，请重新扫描刷新结果" : applied.state);
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
    <div className="app-shell" onDragOver={(event) => event.preventDefault()} onDrop={handleDrop}>
      <header className="topbar">
        <div className="brand-lockup">
          <span className="brand-mark">M</span>
          <div><strong>Marmot</strong><span>本地空间分析</span></div>
        </div>
        <div className={"permission-state " + (permission?.state ?? "loading")}>
          <span className="status-dot" />
          {permission?.state === "available" ? "基础目录可访问" : permission?.message ?? "正在读取权限"}
        </div>
      </header>

      <main className={"workspace " + (status?.snapshotId ? "has-result" : "has-source")}>
        <section className="workspace-head">
          <div className="workspace-title">
            <p className="eyebrow">SPACE ANALYSIS</p>
            <h1>{currentPage?.path ?? "选择一个卷开始"}</h1>
            <p>{map ? map.total.toLocaleString() + " 项 · " + confidenceLabel(mapConfidence) : "先选定范围，结果会在首层发布后逐步补齐。"}</p>
          </div>
          <div className="history-actions" aria-label="浏览历史">
            <button className="icon-button" onClick={() => navigateToHistory(pageIndex - 1)} disabled={pageIndex <= 0} title="后退">后退</button>
            <button className="icon-button" onClick={() => navigateToHistory(pageIndex + 1)} disabled={pageIndex < 0 || pageIndex >= pages.length - 1} title="前进">前进</button>
            <button className="quiet-button" onClick={refreshCurrent} disabled={!currentPage || mapBusy}>刷新当前层</button>
          </div>
        </section>

        <section className="volume-strip" aria-label="磁盘范围">
          <div className="strip-label"><span>范围</span><strong>已挂载卷</strong></div>
          <div className="volume-list">
            {volumes.map((volume) => <VolumeTile key={volume.id} volume={volume} onScan={(path) => { setRoot(path); void startScan(path); }} />)}
            {volumes.length === 0 && <div className="volume-loading">正在读取卷...</div>}
          </div>
        </section>

        <section className="scan-controls">
          <label className="scan-target">
            <span>扫描范围</span>
            <input value={root} onChange={(event) => setRoot(event.target.value)} spellCheck={false} aria-label="扫描范围" />
          </label>
          <span className="measure-note">空间图口径：去重后实际占用</span>
          <div className="toolbar-actions">
            <button className="secondary-button" onClick={() => void chooseFolder()} disabled={busy || scanActive}>扫描文件夹…</button>
            <button className="secondary-button" onClick={() => void startScan()} disabled={busy || scanActive}>扫描范围</button>
            <button className={"primary-button compact" + (scanActive ? " cancel" : "")} onClick={scanActive ? () => void cancelScan() : () => void startScan()} disabled={busy}>{scanActive ? "取消扫描" : "开始扫描"}</button>
          </div>
        </section>

        {status && (
          <section className="status-strip" aria-label="扫描状态">
            <div className="status-main">
              <span className={"scan-pulse" + (scanActive ? " is-running" : "")} />
              <strong>{stateLabel(status.state)}</strong>
              <span>{phaseLabels[status.phase] ?? status.phase}</span>
              <span className="status-path">{status.root}</span>
            </div>
            <div className="status-stats">
              <span>{status.nodes.toLocaleString()} 节点</span>
              <span>{status.files.toLocaleString()} 文件</span>
              <span>{formatBytes(status.bytes)} 已占用</span>
              {(status.issues?.length ?? 0) > 0 && <span className="warning-text">{status.issues?.length ?? 0} 个问题</span>}
            </div>
          </section>
        )}

        {status?.snapshotId ? (
          <>
            <nav className="breadcrumb-bar" aria-label="目录路径">
              <span className="breadcrumb-history">
                <button className="breadcrumb-icon" onClick={() => navigateToHistory(pageIndex - 1)} disabled={pageIndex <= 0} aria-label="后退" title="后退">‹</button>
                <button className="breadcrumb-icon" onClick={() => navigateToHistory(pageIndex + 1)} disabled={pageIndex < 0 || pageIndex >= pages.length - 1} aria-label="前进" title="前进">›</button>
                <button className="breadcrumb-icon" onClick={goParent} disabled={!currentPage || currentPage.crumbs.length <= 1} aria-label="返回上一级" title="返回上一级">↑</button>
              </span>
              <span className="breadcrumb-crumbs">
                {(currentPage?.crumbs ?? []).map((crumb, index) => (
                  <span key={crumb.id + "-" + crumb.path}>
                    <button className={index === (currentPage?.crumbs.length ?? 1) - 1 ? "current" : ""} onClick={() => jumpToBreadcrumb(index)}>
                      {index === 0 ? "扫描根" : crumb.path.split("/").pop() || "/"}
                    </button>
                    {index < (currentPage?.crumbs.length ?? 1) - 1 && <span className="crumb-separator">/</span>}
                  </span>
                ))}
              </span>
              <span className="breadcrumb-meta">历史 {pageIndex + 1} / {pages.length} · v{map?.snapshotVersion ?? "-"}</span>
            </nav>
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
          </>
        ) : (
          <section className="empty-workspace">
            <div className="empty-disc">M</div>
            <div>
              <p className="eyebrow">READY TO SCAN</p>
              <h2>从卷概览开始</h2>
              <p>首层结果会先出现，之后继续在后台补齐。你可以随时取消，并从部分结果继续查看。</p>
            </div>
          </section>
        )}
        {notice && <div className="notice" role="status">{notice}</div>}
      </main>

      <section
        className={"collector-dock" + (collectorOpen ? " is-open" : "")}
        onDragOver={(event) => event.preventDefault()}
        onDrop={handleCollectorDrop}
        data-testid="collector"
      >
        <div className="collector-summary">
          <button className="collector-toggle" onClick={() => setCollectorOpen((open) => !open)} aria-expanded={collectorOpen}>
            <span className="collector-count">{collector.length}</span>
            <span><strong>收集区</strong><small>{collector.length ? formatBytes(collectorBytes) + " · 待审查" : "拖入对象，执行前再检查"}</small></span>
          </button>
          <div className="collector-actions">
            {collector.length > 0 && <button className="quiet-button" onClick={() => setCollector([])}>清空</button>}
            {collector.length > 0 && <button className="primary-button compact" onClick={() => void createPlan()}>创建清理计划</button>}
          </div>
        </div>
        {collectorOpen && (
          <div className="collector-drawer">
            <div className="drawer-heading">
              <div><p className="eyebrow">COLLECTOR</p><h2>逐项审查后再执行</h2></div>
              <span>{collector.length} 项 · {formatBytes(collectorBytes)}</span>
            </div>
            {collector.length === 0 ? (
              <div className="collector-empty">从空间图选择文件或文件夹加入收集区。</div>
            ) : (
              <div className="collector-list">
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
                      <div><strong>{item.name}</strong><span>{node?.path ?? item.name}</span></div>
                      <b>{formatBytes(item.ownedAllocated)}</b>
                      <button className="row-action" onClick={() => void previewEntry(item)}>预览</button>
                      <button className="row-action remove" onClick={() => toggleCollector(item)}>移除</button>
                    </div>
                  );
                })}
              </div>
            )}
            {plan && (
              <div className="plan-review">
                <div><span>计划状态</span><strong>{plan.state}</strong></div>
                <div><span>计划版本</span><strong>{plan.version}</strong></div>
                <div className={validation?.valid ? "validation-ok" : "validation-bad"}>{validation ? (validation.valid ? "执行前校验通过" : "校验失败，不能执行") : "正在校验文件身份"}</div>
                {plan.state === "validated" && <button className="primary-button" onClick={() => void confirmPlan()}>确认计划</button>}
                {plan.state === "confirmed" && <button className="danger-button" onClick={() => void executePlan()}>移入 macOS 废纸篓</button>}
                {(plan.results ?? []).map((result) => <div className="plan-result" key={result.path}><span>{result.path}</span><strong>{result.state}</strong></div>)}
              </div>
            )}
            <p className="safety-note">收集区只改变会话状态，不修改文件。默认移入废纸篓，执行前会重新校验身份。</p>
          </div>
        )}
      </section>
    </div>
  );
}
