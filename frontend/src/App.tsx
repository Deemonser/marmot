import { useEffect, useMemo, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import { Service as MarmotService } from "../bindings/example.com/marmot/internal/presentation/wails";

type PermissionStatus = { platform: string; state: string; message: string };
type ScanStatus = { taskId: string; snapshotId: number; root: string; state: string; phase?: string; nodes: number; files: number; directories: number; bytes: number; issues?: string[] | null; error: string };
type NodeView = { id: number; parentId: number; path: string; name: string; kind: string; logicalSize: number; allocatedSize: number; ownedAllocated: number; confidence: string; sizeBasis: string; hasChildren: boolean };
type CleanupPlan = { id: string; snapshotId: number; version: number; state: string; items: number; results?: { path: string; state: string; reason: string }[] };
type CleanupValidation = { planId: string; version: number; valid: boolean; items: { path: string; valid: boolean; reason: string }[] };

const defaultRoot = "/Users";

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit += 1; }
  return `${size >= 10 || unit === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

function stateLabel(state: string): string {
  return ({ running: "扫描中", completed: "已完成", completed_with_issues: "部分完成", cancelled: "已取消", interrupted: "上次中断", failed: "失败" } as Record<string, string>)[state] ?? state;
}

function phaseLabel(phase?: string): string {
  return ({ catalog: "准备卷", volume_overview: "读取概览", top_level_publish: "发布首层", deep_scan: "深入扫描", finalize: "整理结果" } as Record<string, string>)[phase ?? ""] ?? "";
}

export default function App() {
  const [permission, setPermission] = useState<PermissionStatus | null>(null);
  const [root, setRoot] = useState(defaultRoot);
  const [status, setStatus] = useState<ScanStatus | null>(null);
  const [nodes, setNodes] = useState<NodeView[]>([]);
  const [currentParent, setCurrentParent] = useState(1);
  const [currentPath, setCurrentPath] = useState(root);
  const [selectedPaths, setSelectedPaths] = useState<string[]>([]);
  const [plan, setPlan] = useState<CleanupPlan | null>(null);
  const [validation, setValidation] = useState<CleanupValidation | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const currentParentRef = useRef(1);
  const rootLoadInFlight = useRef(false);

  const scanActive = status?.state === "running";
  const issueCount = status?.issues?.length ?? 0;
  const selectedBytes = useMemo(() => nodes.filter((node) => selectedPaths.includes(node.path)).reduce((sum, node) => sum + node.ownedAllocated, 0), [nodes, selectedPaths]);

  useEffect(() => {
    MarmotService.GetPermissionStatus().then(setPermission).catch((error: unknown) => setNotice(String(error)));
    const savedTaskId = window.localStorage.getItem("marmot.scanTaskId");
    if (savedTaskId) {
      MarmotService.GetScanStatus(savedTaskId).then(async (next) => {
        setStatus(next);
        setRoot(next.root);
        setCurrentPath(next.root);
        await loadRootIfAvailable(next.snapshotId, next.root);
      }).catch(() => window.localStorage.removeItem("marmot.scanTaskId"));
    }
    const off = Events.On("scan-progress", (event: { data: ScanStatus }) => {
      setStatus((current) => ({
        taskId: event.data.taskId,
        snapshotId: event.data.snapshotId,
        root: event.data.root || current?.root || root,
        state: event.data.state,
        nodes: event.data.nodes,
        files: event.data.files,
        directories: event.data.directories,
        bytes: event.data.bytes,
        phase: event.data.phase ?? current?.phase,
        issues: event.data.issues ?? current?.issues ?? [],
        error: event.data.error ?? current?.error ?? "",
      }));
      if (event.data.nodes > 1 || event.data.state !== "running") {
        void loadRootIfAvailable(event.data.snapshotId, event.data.root || root);
      }
    });
    return () => off();
  }, []);

  async function loadChildren(snapshotId: number, parentId: number, path: string) {
    const result = await MarmotService.GetChildren({ snapshotId, parentId, limit: 1000, offset: 0 });
    setNodes(result.nodes ?? []);
    currentParentRef.current = parentId;
    setCurrentParent(parentId);
    setCurrentPath(path);
  }

  async function loadRootIfAvailable(snapshotId: number, path: string) {
    if (snapshotId <= 0 || currentParentRef.current !== 1 || rootLoadInFlight.current) return;
    rootLoadInFlight.current = true;
    try {
      const result = await MarmotService.GetChildren({ snapshotId, parentId: 1, limit: 1000, offset: 0 });
      const nodes = result.nodes ?? [];
      if (currentParentRef.current === 1 && nodes.length > 0) {
        setNodes(nodes);
        setCurrentParent(1);
        setCurrentPath(path);
      }
    } finally {
      rootLoadInFlight.current = false;
    }
  }

  async function startScan() {
    setBusy(true); setNotice(""); setSelectedPaths([]); setPlan(null); setValidation(null);
    try {
      const next = await MarmotService.StartScan({ root });
      window.localStorage.setItem("marmot.scanTaskId", next.taskId);
      setStatus(next); setCurrentParent(1); setCurrentPath(root); setNodes([]);
    } catch (error) { setNotice(String(error)); } finally { setBusy(false); }
  }

  async function cancelScan() { if (status) setStatus(await MarmotService.CancelScan(status.taskId)); }

  async function openNode(node: NodeView) {
    if (node.kind !== "directory" || !status) return;
    try { await loadChildren(status.snapshotId, node.id, node.path); } catch (error) { setNotice(String(error)); }
  }

  async function createPlan() {
    if (!status || selectedPaths.length === 0) return;
    try {
      const next = await MarmotService.CreateCleanupPlan({ snapshotId: status.snapshotId, paths: selectedPaths });
      setPlan(next); setValidation(await MarmotService.ValidateCleanupPlan(next.id, next.version));
    } catch (error) { setNotice(String(error)); }
  }

  async function confirmPlan() {
    if (!plan || !validation?.valid) return;
    setPlan(await MarmotService.ConfirmCleanupPlan(plan.id, plan.version));
  }

  async function executePlan() {
    if (!plan || plan.state !== "confirmed") return;
    const applied = await MarmotService.ExecuteCleanupPlan(plan.id, plan.version);
    setPlan(applied); setNotice(applied.state === "applied" ? "已移入废纸篓，请重新扫描刷新结果" : applied.state);
  }

  function toggleSelection(node: NodeView) {
    setSelectedPaths((current) => current.includes(node.path) ? current.filter((path) => path !== node.path) : [...current, node.path]);
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand-lockup"><span className="brand-mark">M</span><div><strong>Marmot</strong><span>磁盘空间分析</span></div></div>
        <div className={`permission-state ${permission?.state ?? "loading"}`}><span className="status-dot" />{permission?.state === "available" ? "基础权限可用" : permission?.message ?? "正在读取权限"}</div>
      </header>

      <main className="workspace">
        <section className="control-band">
          <div><p className="eyebrow">SCAN WORKSPACE</p><h1>看清空间去向</h1><p className="lede">先展示顶层结果，再按目录展开。扫描事实与清理计划始终分开。</p></div>
          <div className="scan-controls"><label htmlFor="root">扫描根目录</label><div className="path-control"><input id="root" value={root} onChange={(event) => setRoot(event.target.value)} spellCheck={false} /><button onClick={scanActive ? cancelScan : startScan} disabled={busy}>{scanActive ? "取消扫描" : "开始扫描"}</button></div></div>
        </section>

        <section className="metrics-band" aria-label="扫描状态">
          <div><span>状态</span><strong>{status ? `${stateLabel(status.state)}${phaseLabel(status.phase) ? ` · ${phaseLabel(status.phase)}` : ""}` : "未开始"}</strong></div><div><span>节点</span><strong>{status?.nodes.toLocaleString() ?? "0"}</strong></div><div><span>文件</span><strong>{status?.files.toLocaleString() ?? "0"}</strong></div><div><span>已占用</span><strong>{formatBytes(status?.bytes ?? 0)}</strong></div><div className="metrics-note">{issueCount ? `${issueCount} 个问题` : "结果会保留可信度和权限限制"}</div>
        </section>

        <section className="content-grid">
          <div className="directory-panel">
            <div className="panel-heading"><div><p className="eyebrow">DIRECTORY MAP</p><h2>{currentPath}</h2></div><button className="quiet-button" onClick={() => status && loadChildren(status.snapshotId, 1, root)} disabled={!status}>回到根目录</button></div>
            {status?.state && status.state !== "running" && nodes.length === 0 && <div className="empty-state">扫描完成后，目录节点会按占用从大到小显示。</div>}
            <div className="node-list">{nodes.map((node) => <div className={`node-row ${selectedPaths.includes(node.path) ? "selected" : ""}`} key={node.id}>
              <button className="node-main" onDoubleClick={() => openNode(node)} title={node.kind === "directory" ? "双击展开目录" : node.path}><span className={`node-icon ${node.kind}`}>{node.kind === "directory" ? "DIR" : "FILE"}</span><span className="node-name">{node.name}</span><span className="node-size">{formatBytes(node.ownedAllocated)}</span></button>
              <div className="node-actions">{node.kind === "directory" && <button className="icon-button" onClick={() => openNode(node)} aria-label="展开目录">→</button>}{node.kind !== "directory" && <button className={`select-button ${selectedPaths.includes(node.path) ? "is-selected" : ""}`} onClick={() => toggleSelection(node)}>{selectedPaths.includes(node.path) ? "已选择" : "选择"}</button>}</div>
            </div>)}</div>
          </div>

          <aside className="inspector-panel">
            <div className="panel-heading"><div><p className="eyebrow">CLEANUP PLAN</p><h2>审查后再执行</h2></div></div>
            {selectedPaths.length === 0 ? <div className="empty-state compact">从目录中选择文件，生成独立的清理计划。</div> : <><div className="selection-summary"><strong>{selectedPaths.length} 项</strong><span>预计释放 {formatBytes(selectedBytes)}</span></div><button className="primary-button" onClick={createPlan}>创建并校验计划</button></>}
            {plan && <div className="plan-state"><div><span>计划版本</span><strong>{plan.version}</strong></div><div><span>状态</span><strong>{plan.state}</strong></div>{validation && <div className={validation.valid ? "validation-ok" : "validation-bad"}>{validation.valid ? "执行前校验通过" : "校验失败，请重新选择"}</div>}{plan.state === "validated" && <button className="primary-button" onClick={confirmPlan}>确认计划</button>}{plan.state === "confirmed" && <button className="danger-button" onClick={executePlan}>移入废纸篓</button>}{plan.results?.map((result) => <div key={result.path} className={result.state === "applied" ? "validation-ok" : "validation-bad"}><span>{result.path}</span><strong>{result.state}</strong></div>)}</div>}
            <div className="safety-note">默认移入 macOS 废纸篓，不做永久删除。文件发生变化时，计划会失效。</div>
          </aside>
        </section>
        {notice && <div className="notice" role="status">{notice}</div>}
      </main>
    </div>
  );
}
