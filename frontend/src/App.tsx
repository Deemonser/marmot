import { useEffect, useRef, useState } from "react";
import { arc } from "d3-shape";
import { Events } from "@wailsio/runtime";
import { Service as MarmotService } from "../bindings/example.com/marmot/internal/presentation/wails";
import type * as Models from "../bindings/example.com/marmot/internal/presentation/wails/models";

type NodeView = Models.NodeView;
type MapEntry = Models.MapEntry;
type MapResult = Models.MapResult;
type ScanStatus = Models.ScanStatus;
type PermissionStatus = Models.PermissionStatus;
type VolumeOverview = Models.VolumeOverview;
type CleanupPlan = Models.CleanupPlan;
type CleanupValidation = Models.CleanupValidation;
type Breadcrumb = { id: number; path: string };

const defaultRoot = "/";
const pageSize = 256;
const phaseLabels: Record<string, string> = { catalog: "准备卷", volume_overview: "读取概览", top_level_publish: "发布首层", deep_scan: "深入扫描", finalize: "整理结果" };
const stateLabels: Record<string, string> = { running: "扫描中", completed: "已完成", completed_with_issues: "部分完成", cancelled: "已取消", interrupted: "上次中断", failed: "失败" };
const palette = ["#63c7bd", "#ef8a70", "#e9be67", "#719be0", "#c38bd5", "#74b987", "#df789d", "#89a9ac"];

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit += 1; }
  return `${size >= 10 || unit === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

function formatPercent(used: number, total: number): string { return total ? `${Math.min(100, Math.max(0, (used / total) * 100)).toFixed(0)}%` : "0%"; }
function stateLabel(state: string): string { return stateLabels[state] ?? state; }
function entryNode(entry: MapEntry): NodeView | null { return entry.kind === "node" && entry.node.id > 0 ? entry.node : null; }
function entrySize(entry: MapEntry): number { return Math.max(0, entry.ownedAllocated); }
function confidenceLabel(confidence: string): string { return ({ exact: "精确", estimated: "估算", partial: "部分结果", unknown: "未知" } as Record<string, string>)[confidence] ?? "待确认"; }

function Sunburst({ map, selectedId, onSelect, onOpen, onAggregate, onGoParent }: { map: MapResult | null; selectedId: number | null; onSelect: (entry: MapEntry) => void; onOpen: (entry: MapEntry) => void; onAggregate: (entry: MapEntry) => void; onGoParent: () => void }) {
  const entries = map?.entries ?? [];
  const total = entries.reduce((sum, entry) => sum + entrySize(entry), 0);
  const fallback = entries.length || 1;
  const innerRadius = 100;
  const outerRadius = 235;
  let angle = -Math.PI / 2;
  const makeArc = arc<unknown>().innerRadius(innerRadius).outerRadius(outerRadius);
  const slices = entries.map((entry, index) => {
    const weight = total > 0 ? entrySize(entry) : 1;
    const startAngle = angle;
    angle += (weight / (total || fallback)) * Math.PI * 2;
    return { entry, index, startAngle, endAngle: angle, path: makeArc({ startAngle, endAngle: angle }) ?? "" };
  });
  return <div className="sunburst-wrap" aria-label="空间图">
    <svg className="sunburst" viewBox="0 0 520 520" role="img"><g transform="translate(260 260)">
      {slices.map(({ entry, index, path }) => { const node = entryNode(entry); const aggregate = entry.kind === "aggregate"; return <path key={`${entry.kind}-${node?.id ?? `aggregate-${index}`}`} d={path} className={`sunburst-slice ${node?.id === selectedId ? "is-selected" : ""} ${aggregate ? "is-aggregate" : ""}`} fill={aggregate ? "#65747c" : palette[index % palette.length]} onPointerEnter={(event) => { if (!aggregate) event.currentTarget.setAttribute("draggable", "true"); }} onDragStart={(event) => { if (node) event.dataTransfer.setData("application/marmot-node", JSON.stringify(node)); }} onClick={() => aggregate ? onAggregate(entry) : onSelect(entry)} onDoubleClick={() => node?.kind === "directory" ? onOpen(entry) : undefined} onKeyDown={(event) => { if (event.key === "Enter") aggregate ? onAggregate(entry) : node?.kind === "directory" ? onOpen(entry) : onSelect(entry); }} tabIndex={0} aria-label={`${entry.name}，${formatBytes(entrySize(entry))}`} />; })}
      <circle className="sunburst-center" r={innerRadius - 8} onClick={onGoParent} /><text className="sunburst-center-label" textAnchor="middle" y="-6">{map?.parent.name ?? "空间图"}</text><text className="sunburst-center-value" textAnchor="middle" y="22">{formatBytes(map?.parent.ownedAllocated ?? 0)}</text><text className="sunburst-center-hint" textAnchor="middle" y="49">点击中心返回</text>
    </g></svg>{!map && <div className="chart-placeholder">选择一个磁盘开始分析</div>}
  </div>;
}

function VolumeCard({ volume, onScan }: { volume: VolumeOverview; onScan: (path: string) => void }) {
  const ratio = volume.totalBytes ? Math.min(100, (volume.usedBytes / volume.totalBytes) * 100) : 0;
  return <article className={`volume-card ${volume.scannable ? "" : "is-disabled"}`}><div className="volume-card-top"><div className="volume-icon">{volume.path === "/" ? "HD" : "V"}</div><div className="volume-name"><strong>{volume.name}</strong><span>{volume.path} · {volume.kind}</span></div><span className={`volume-permission ${volume.permission}`}>{volume.permission === "available" ? "可访问" : volume.permission}</span></div><div className="volume-gauge" aria-label={`已使用 ${formatPercent(volume.usedBytes, volume.totalBytes)}`}><span style={{ width: `${ratio}%` }} /></div><div className="volume-stats"><span><strong>{formatBytes(volume.usedBytes)}</strong> 已使用</span><span><strong>{formatBytes(volume.freeBytes)}</strong> 可用</span><span>{formatBytes(volume.totalBytes)} 总计</span></div><div className="volume-card-bottom"><span>{volume.message}</span><button className="text-button" onClick={() => onScan(volume.path)} disabled={!volume.scannable}>扫描此卷</button></div></article>;
}

export default function App() {
  const [permission, setPermission] = useState<PermissionStatus | null>(null);
  const [volumes, setVolumes] = useState<VolumeOverview[]>([]);
  const [root, setRoot] = useState(defaultRoot);
  const [status, setStatus] = useState<ScanStatus | null>(null);
  const [map, setMap] = useState<MapResult | null>(null);
  const [breadcrumbs, setBreadcrumbs] = useState<Breadcrumb[]>([]);
  const [pageOffset, setPageOffset] = useState(0);
  const [selected, setSelected] = useState<NodeView | null>(null);
  const [collector, setCollector] = useState<NodeView[]>([]);
  const [collectorOpen, setCollectorOpen] = useState(false);
  const [plan, setPlan] = useState<CleanupPlan | null>(null);
  const [validation, setValidation] = useState<CleanupValidation | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [mapBusy, setMapBusy] = useState(false);
  const mapRequest = useRef(0);
  const refreshTimer = useRef<number | undefined>(undefined);
  const mapRef = useRef<MapResult | null>(null);
  const parentRef = useRef<Breadcrumb | undefined>(undefined);
  const pageOffsetRef = useRef(0);
  const loadMapRef = useRef<((snapshotId: number, parentId: number, path: string, offset?: number) => Promise<void>) | null>(null);

  const scanActive = status?.state === "running";
  const issueCount = status?.issues?.length ?? 0;
  const currentParent = breadcrumbs[breadcrumbs.length - 1];
  const selectedInCollector = selected ? collector.some((item) => item.id === selected.id) : false;
  const collectorBytes = collector.reduce((sum, item) => sum + item.ownedAllocated, 0);

  mapRef.current = map;
  parentRef.current = currentParent;
  pageOffsetRef.current = pageOffset;

  async function loadVolumes() { try { setVolumes((await MarmotService.GetVolumes()) ?? []); } catch (error) { setNotice(String(error)); } }
  async function loadMap(snapshotId: number, parentId: number, path: string, offset = 0) {
    if (snapshotId <= 0) return;
    const request = ++mapRequest.current;
    setMapBusy(true);
    try {
      const next = await MarmotService.GetMap({ snapshotId, parentId, limit: pageSize, offset, measure: "owned_allocated" });
      if (request !== mapRequest.current) return;
      const nextEntries = next.entries ?? [];
      setMap(next); setPageOffset(offset); setSelected((current) => current && nextEntries.some((entry) => entry.node?.id === current.id) ? current : null);
      setBreadcrumbs((current) => {
        const existingIndex = current.findIndex((crumb) => crumb.id === parentId);
        if (existingIndex >= 0) return current.slice(0, existingIndex + 1);
        return [...current, { id: parentId, path }];
      });
    } catch (error) { if (request === mapRequest.current) setNotice(String(error)); } finally { if (request === mapRequest.current) setMapBusy(false); }
  }
  loadMapRef.current = loadMap;
  function scheduleMapRefresh(event: ScanStatus) {
    const currentMap = mapRef.current;
    const parent = parentRef.current;
    if (event.snapshotId <= 0 || (currentMap && event.snapshotId !== currentMap.snapshotId)) return;
    if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
    if (event.nodes === 0 && event.state === "running") return;
    const parentID = parent?.id ?? 1;
    const parentPath = parent?.path ?? (event.root || root);
    const offset = parent ? pageOffsetRef.current : 0;
    refreshTimer.current = window.setTimeout(() => { if (loadMapRef.current) void loadMapRef.current(event.snapshotId, parentID, parentPath, offset); }, 250);
  }

  useEffect(() => {
    void loadVolumes();
    const savedTaskId = window.localStorage.getItem("marmot.scanTaskId");
    if (savedTaskId) MarmotService.GetScanStatus(savedTaskId).then((next) => { setStatus(next); setRoot(next.root); void loadMap(next.snapshotId, 1, next.root); }).catch(() => window.localStorage.removeItem("marmot.scanTaskId"));
    MarmotService.GetPermissionStatus().then(setPermission).catch((error: unknown) => setNotice(String(error)));
    const off = Events.On("scan-progress", (event: { data: ScanStatus }) => { setStatus(event.data); scheduleMapRefresh(event.data); if (event.data.state !== "running") void loadVolumes(); });
    return () => { off(); if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current); };
  }, []);

  async function startScan(nextRoot = root) {
    setBusy(true); setNotice(""); setMap(null); setBreadcrumbs([]); setSelected(null); setCollector([]); setPlan(null); setValidation(null);
    try { const next = await MarmotService.StartScan({ root: nextRoot }); window.localStorage.setItem("marmot.scanTaskId", next.taskId); setRoot(next.root); setStatus(next); setBreadcrumbs([{ id: 1, path: next.root }]); void loadMap(next.snapshotId, 1, next.root); } catch (error) { setNotice(String(error)); } finally { setBusy(false); }
  }
  async function cancelScan() { if (!status) return; try { setStatus(await MarmotService.CancelScan(status.taskId)); } catch (error) { setNotice(String(error)); } }
  function openEntry(entry: MapEntry) { const node = entryNode(entry); if (!node || node.kind !== "directory" || !status) return; setSelected(node); void loadMap(status.snapshotId, node.id, node.path, 0); }
  function selectEntry(entry: MapEntry) { const node = entryNode(entry); if (node) setSelected(node); }
  function expandAggregate(entry: MapEntry) { if (!map || entry.kind !== "aggregate" || !currentParent) return; void loadMap(map.snapshotId, currentParent.id, currentParent.path, pageOffset + Math.max(1, (map.entries ?? []).filter((item) => item.kind === "node").length)); }
  function goParent() { if (!status || breadcrumbs.length <= 1) return; const parent = breadcrumbs[breadcrumbs.length - 2]; setBreadcrumbs((current) => current.slice(0, -1)); setSelected(null); void loadMap(status.snapshotId, parent.id, parent.path, 0); }
  function jumpToBreadcrumb(index: number) { if (!status) return; const target = breadcrumbs[index]; setBreadcrumbs((current) => current.slice(0, index + 1)); setSelected(null); void loadMap(status.snapshotId, target.id, target.path, 0); }
  function toggleCollector(node: NodeView | null) { if (!node || node.parentId === 0 || (node.kind !== "file" && node.kind !== "directory" && node.kind !== "symlink")) return; setCollector((current) => current.some((item) => item.id === node.id) ? current.filter((item) => item.id !== node.id) : [...current, node]); }
  async function previewNode(node: NodeView | null) { if (!node || !status) return; try { const result = await MarmotService.PreviewNode(status.snapshotId, node.id); setNotice(result.ok ? "Quick Look 已打开" : result.message); } catch (error) { setNotice(String(error)); } }
  async function revealNode(node: NodeView | null) { if (!node || !status) return; try { const result = await MarmotService.RevealNode(status.snapshotId, node.id); setNotice(result.ok ? "已在 Finder 中定位" : result.message); } catch (error) { setNotice(String(error)); } }
  async function createPlan() { if (!status || collector.length === 0) return; try { const next = await MarmotService.CreateCleanupPlan({ snapshotId: status.snapshotId, paths: collector.map((item) => item.path) }); setPlan(next); setValidation(await MarmotService.ValidateCleanupPlan(next.id, next.version)); setCollectorOpen(true); } catch (error) { setNotice(String(error)); } }
  async function confirmPlan() { if (!plan || !validation?.valid) return; try { setPlan(await MarmotService.ConfirmCleanupPlan(plan.id, plan.version)); } catch (error) { setNotice(String(error)); } }
  async function executePlan() { if (!plan || plan.state !== "confirmed") return; try { const applied = await MarmotService.ExecuteCleanupPlan(plan.id, plan.version); setPlan(applied); setNotice(applied.state === "applied" ? "已移入废纸篓，请重新扫描刷新结果" : applied.state); } catch (error) { setNotice(String(error)); } }
  function handleDrop(event: React.DragEvent<HTMLDivElement>) { event.preventDefault(); const file = event.dataTransfer.files[0] as (File & { path?: string }) | undefined; const text = event.dataTransfer.getData("text/uri-list") || event.dataTransfer.getData("text/plain"); const droppedPath = file?.path || text.replace(/^file:\/\//, "").split("\n")[0]; if (droppedPath) setRoot(decodeURIComponent(droppedPath)); }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => { const command = event.metaKey || event.ctrlKey; if (event.key === "Escape") setCollectorOpen(false); if (command && event.key === "ArrowUp") { event.preventDefault(); goParent(); } if (event.key === " " && selected) { event.preventDefault(); void previewNode(selected); } if (command && event.key === "Delete") { event.preventDefault(); toggleCollector(selected); } if (event.key === "Enter" && selected?.kind === "directory") { const entry = (map?.entries ?? []).find((item) => item.node?.id === selected.id); if (entry) openEntry(entry); } if (command && event.key.toLowerCase() === "r" && status) { event.preventDefault(); if (currentParent) void loadMap(status.snapshotId, currentParent.id, currentParent.path, pageOffset); } };
    window.addEventListener("keydown", onKeyDown); return () => window.removeEventListener("keydown", onKeyDown);
  });

  const visibleEntries = map?.entries ?? [];
  const mapTotal = map?.parent.ownedAllocated ?? status?.bytes ?? 0;
  const mapConfidence = map?.confidence || map?.parent.confidence || "unknown";
  return <div className="app-shell" onDragOver={(event) => event.preventDefault()} onDrop={handleDrop}>
    <header className="topbar"><div className="brand-lockup"><span className="brand-mark">M</span><div><strong>Marmot</strong><span>磁盘空间分析</span></div></div><div className={`permission-state ${permission?.state ?? "loading"}`}><span className="status-dot" />{permission?.state === "available" ? "基础目录可访问" : permission?.message ?? "正在读取权限"}</div></header>
    <main className="workspace">
      <section className="hero-band"><div><p className="eyebrow">DISK OVERVIEW</p><h1>先看见空间，再决定动作</h1><p className="lede">从卷概览开始，用空间图找到真正占用空间的对象。</p></div><div className="drop-target"><span>拖入磁盘或文件夹</span><small>Finder 拖放只用于设置扫描范围</small></div></section>
      <section className="volume-grid" aria-label="磁盘概览">{volumes.map((volume) => <VolumeCard key={volume.id} volume={volume} onScan={(path) => { setRoot(path); void startScan(path); }} />)}{volumes.length === 0 && <div className="volume-loading">正在读取已挂载卷…</div>}</section>
      <section className="scan-toolbar"><div className="scan-target"><span className="toolbar-label">扫描范围</span><input value={root} onChange={(event) => setRoot(event.target.value)} spellCheck={false} aria-label="扫描范围" /><span className="target-note">结果口径：去重后实际占用</span></div><div className="toolbar-actions"><button className="secondary-button" onClick={() => void startScan()} disabled={busy || scanActive}>扫描范围</button><button className={`primary-button compact ${scanActive ? "cancel" : ""}`} onClick={scanActive ? () => void cancelScan() : () => void startScan()} disabled={busy}>{scanActive ? "取消扫描" : "开始扫描"}</button></div></section>
      {status && <section className="status-strip" aria-label="扫描状态"><div className="status-main"><span className={`scan-pulse ${scanActive ? "is-running" : ""}`} /><strong>{stateLabel(status.state)}</strong><span>{phaseLabels[status.phase] ?? status.phase}</span><span className="status-path">{status.root}</span></div><div className="status-stats"><span>{status.nodes.toLocaleString()} 节点</span><span>{status.files.toLocaleString()} 文件</span><span>{formatBytes(status.bytes)} 已占用</span>{issueCount > 0 && <span className="warning-text">{issueCount} 个问题</span>}</div></section>}
      {status?.snapshotId ? <><nav className="breadcrumb-bar" aria-label="目录路径">{breadcrumbs.map((crumb, index) => <span key={`${crumb.id}-${crumb.path}`}><button className={index === breadcrumbs.length - 1 ? "current" : ""} onClick={() => jumpToBreadcrumb(index)}>{index === 0 ? "扫描根" : crumb.path.split("/").pop() || "/"}</button>{index < breadcrumbs.length - 1 && <span className="crumb-separator">/</span>}</span>)}<span className="breadcrumb-meta">{map?.total.toLocaleString() ?? 0} 项 · {confidenceLabel(mapConfidence)} · v{map?.snapshotVersion ?? "-"}</span></nav>
        <section className="analysis-grid"><div className="map-panel"><div className="panel-heading map-heading"><div><p className="eyebrow">SUNBURST MAP</p><h2>{currentParent?.path ?? status.root}</h2></div><div className="map-heading-actions"><button className="quiet-button" onClick={goParent} disabled={breadcrumbs.length <= 1}>返回上级</button><button className="quiet-button" onClick={() => currentParent && void loadMap(status.snapshotId, currentParent.id, currentParent.path, pageOffset)} disabled={mapBusy}>刷新</button></div></div><div className="map-stage"><Sunburst map={map} selectedId={selected?.id ?? null} onSelect={selectEntry} onOpen={openEntry} onAggregate={expandAggregate} onGoParent={goParent} />{mapBusy && <span className="map-loading">正在更新空间图…</span>}</div><div className="map-footer"><span>文件为灰色，文件夹按空间贡献着色</span><span>{map?.hasMore ? `已显示 ${pageOffset + visibleEntries.filter((entry) => entry.kind === "node").length} / ${map.total}` : "当前层已显示完整结果"}</span><div className="page-actions"><button onClick={() => currentParent && void loadMap(status.snapshotId, currentParent.id, currentParent.path, Math.max(0, pageOffset - pageSize))} disabled={pageOffset === 0 || mapBusy}>上一页</button><button onClick={() => currentParent && map?.hasMore && void loadMap(status.snapshotId, currentParent.id, currentParent.path, pageOffset + visibleEntries.filter((entry) => entry.kind === "node").length)} disabled={!map?.hasMore || mapBusy}>下一页</button></div></div></div>
          <aside className="inspector-panel"><div className="panel-heading"><div><p className="eyebrow">INSPECTOR</p><h2>{selected ? selected.name : map?.parent.name ?? "当前目录"}</h2></div></div>{selected ? <div className="inspector-content"><div className={`object-glyph ${selected.kind}`}>{selected.kind === "directory" ? "DIR" : "FILE"}</div><p className="object-path">{selected.path}</p><div className="object-facts"><div><span>去重后占用</span><strong>{formatBytes(selected.ownedAllocated)}</strong></div><div><span>逻辑大小</span><strong>{formatBytes(selected.logicalSize)}</strong></div><div><span>可信度</span><strong>{confidenceLabel(selected.confidence)}</strong></div></div><div className="inspector-actions"><button className="action-button" onClick={() => void previewNode(selected)}>预览</button><button className="action-button" onClick={() => void revealNode(selected)}>在 Finder 中显示</button>{selected.kind === "directory" && <button className="action-button" onClick={() => { const entry = (map?.entries ?? []).find((item) => item.node?.id === selected.id); if (entry) openEntry(entry); }}>进入目录</button>}<button className={`action-button collector-action ${selectedInCollector ? "is-added" : ""}`} onClick={() => toggleCollector(selected)}>{selectedInCollector ? "移出收集区" : "加入收集区"}</button></div></div> : <div className="inspector-content parent-inspector"><div className="folder-emblem">{map?.parent.name.slice(0, 1).toUpperCase() || "M"}</div><strong>{map?.parent.name}</strong><p>{map?.parent.path}</p><div className="parent-size">{formatBytes(mapTotal)}<span>去重后占用</span></div><button className="action-button" onClick={goParent} disabled={breadcrumbs.length <= 1}>返回上级目录</button></div>}<div className="inspector-note">大小口径分开保存。当前空间图使用 owned_allocated；受限目录不会伪装成空目录。</div></aside></section>
      </> : <section className="empty-workspace"><div className="empty-disc">M</div><h2>从上方选择一个磁盘开始</h2><p>扫描会先发布首层结果，之后继续在后台补齐目录。</p></section>}
      {notice && <div className="notice" role="status">{notice}</div>}
    </main>
    <section className={`collector-dock ${collectorOpen ? "is-open" : ""}`} onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); const raw = event.dataTransfer.getData("application/marmot-node"); if (!raw) return; try { toggleCollector(JSON.parse(raw) as NodeView); } catch { setNotice("无法读取拖入对象"); } }}><div className="collector-summary"><button className="collector-toggle" onClick={() => setCollectorOpen((open) => !open)} aria-expanded={collectorOpen}><span className="collector-count">{collector.length}</span><span><strong>收集区</strong><small>{collector.length ? `${formatBytes(collectorBytes)} 待审查` : "将对象放在这里，删除前再检查"}</small></span></button><div className="collector-actions">{collector.length > 0 && <button className="quiet-button" onClick={() => setCollector([])}>清空</button>}{collector.length > 0 && <button className="primary-button compact" onClick={() => void createPlan()}>创建清理计划</button>}</div></div>{collectorOpen && <div className="collector-drawer"><div className="drawer-heading"><div><p className="eyebrow">COLLECTOR</p><h2>逐项审查后再执行</h2></div><span>{collector.length} 项 · {formatBytes(collectorBytes)}</span></div>{collector.length === 0 ? <div className="collector-empty">从空间图选择文件或文件夹加入收集区。</div> : <div className="collector-list">{collector.map((item) => <div className="collector-item" key={item.id}><div><strong>{item.name}</strong><span>{item.path}</span></div><b>{formatBytes(item.ownedAllocated)}</b><button className="row-action" onClick={() => void previewNode(item)}>预览</button><button className="row-action remove" onClick={() => toggleCollector(item)}>移除</button></div>)}</div>}{plan && <div className="plan-review"><div><span>计划状态</span><strong>{plan.state}</strong></div><div><span>计划版本</span><strong>{plan.version}</strong></div><div className={validation?.valid ? "validation-ok" : "validation-bad"}>{validation ? (validation.valid ? "执行前校验通过" : "校验失败，不能执行") : "正在校验文件身份"}</div>{plan.state === "validated" && <button className="primary-button" onClick={() => void confirmPlan()}>确认计划</button>}{plan.state === "confirmed" && <button className="danger-button" onClick={() => void executePlan()}>移入 macOS 废纸篓</button>}{plan.results?.map((result) => <div className="plan-result" key={result.path}><span>{result.path}</span><strong>{result.state}</strong></div>)}</div>}<p className="safety-note">收集区不会修改文件。Marmot 默认移入废纸篓，不做永久删除；执行前会重新校验文件身份。</p></div>}</section>
  </div>;
}
