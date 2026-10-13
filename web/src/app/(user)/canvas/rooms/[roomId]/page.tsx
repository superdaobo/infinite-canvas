"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { AlertTriangle, ChevronLeft, Hand, ListOrdered, Loader2, MousePointer2, Plus, RefreshCw, Redo2, Share2, Trash2, Undo2, Users, Wifi, WifiOff, X } from "lucide-react";

import { canvasThemes } from "@/lib/canvas-theme";
import type { CollabCursorInfo } from "@/services/api/collab-realtime";
import { useThemeStore } from "@/stores/use-theme-store";
import { CanvasNode } from "../../components/canvas-node";
import { ConnectionPath } from "../../components/canvas-connections";
import { InfiniteCanvas } from "../../components/infinite-canvas";
import { isSharedCanvasEnabled } from "../../mode";
import type { CanvasNodeData, Position, ViewportTransform } from "../../types";
import { CollabSchoolQueuePanel } from "../collab-school-queue-panel";
import { CollabSharingPanel } from "../collab-sharing-panel";
import { useCollabInviteJoin } from "../use-collab-invite-join";
import { useCollabRoomEditing, type CollabConflict } from "../use-collab-room-editing";
import { useCollabRoomHistory } from "../use-collab-room-history";
import { useCollabRoomRealtime, type CollabRealtimeStatus } from "../use-collab-room-realtime";

// 共享房间画布入口：/canvas/rooms/:roomId
// 与单人项目路由 /canvas/:projectId 完全分离，使用独立状态边界（不使用 useCanvasStore）。
// 编辑能力按 #13 交付：节点/连线变更构造为协作操作并经服务端回执确认（见 use-collab-room-editing）。
// 实时接收按 #14 交付：ticket 建连、分帧快照、changes、presence、cursors 与断线恢复（见 use-collab-room-realtime）。
// 冲突处理/草稿保留/撤销重做按 #15 交付：409 冲突可选择安全应用，未确认草稿刷新后仍保留，
// 撤销/重做按用户隔离并接入 #11 历史接口（见 use-collab-room-editing / use-collab-room-history）。

const noop = () => undefined;
const TEXT_NODE_SIZE = { width: 360, height: 200 };

// 冲突差异摘要：只展示本地草稿与服务端最新版本之间的公开字段差异，供用户安全选择。
function describeNodeConflict(local: CanvasNodeData | null, server: CanvasNodeData | null): string[] {
    if (!local || !server) return [];
    const lines: string[] = [];
    if (local.title !== server.title) lines.push(`标题：本地「${local.title || "空"}」/ 服务端「${server.title || "空"}」`);
    const localContent = typeof local.metadata?.content === "string" ? local.metadata.content : "";
    const serverContent = typeof server.metadata?.content === "string" ? server.metadata.content : "";
    if (localContent !== serverContent) lines.push(`内容：本地 ${localContent.length} 字 / 服务端 ${serverContent.length} 字`);
    if (local.position.x !== server.position.x || local.position.y !== server.position.y) {
        lines.push(`位置：本地 (${Math.round(local.position.x)}, ${Math.round(local.position.y)}) / 服务端 (${Math.round(server.position.x)}, ${Math.round(server.position.y)})`);
    }
    if (local.width !== server.width || local.height !== server.height) {
        lines.push(`尺寸：本地 ${Math.round(local.width)}×${Math.round(local.height)} / 服务端 ${Math.round(server.width)}×${Math.round(server.height)}`);
    }
    return lines.slice(0, 4);
}

function conflictTitle(conflict: CollabConflict) {
    if (conflict.code === "node_deleted") return "该节点已被其他成员删除";
    if (conflict.code === "edge_changed") return "该连线已被其他成员修改";
    return "该节点已被其他成员修改";
}

// 协作光标配色：按 userId 稳定取色，保证同一成员各端颜色一致。
const CURSOR_COLORS = ["#ef4444", "#f97316", "#eab308", "#22c55e", "#06b6d4", "#3b82f6", "#8b5cf6", "#ec4899"];

function cursorColor(userId: string) {
    let hash = 0;
    for (let index = 0; index < userId.length; index += 1) hash = (hash * 31 + userId.charCodeAt(index)) >>> 0;
    return CURSOR_COLORS[hash % CURSOR_COLORS.length];
}

const REALTIME_STATUS_LABEL: Record<CollabRealtimeStatus, string> = {
    connecting: "连接中",
    connected: "已连接",
    reconnecting: "重连中",
    revoked: "访问失效",
    offline: "离线",
};

// 他人协作光标：在画布世界坐标层内渲染，用反向缩放保持图标恒定大小。
function RemoteCursorLayer({ cursors }: { cursors: CollabCursorInfo[] }) {
    if (!cursors.length) return null;
    return (
        <div className="pointer-events-none absolute left-0 top-0 z-[60]">
            {cursors.map((cursor) => {
                const color = cursorColor(cursor.userId);
                return (
                    <div key={cursor.userId} className="absolute" style={{ transform: `translate(${cursor.position.x}px, ${cursor.position.y}px)` }}>
                        <div className="flex items-start" style={{ transform: "scale(var(--canvas-inverse-scale, 1))", transformOrigin: "top left" }}>
                            <MousePointer2 className="size-4" style={{ color, fill: color }} />
                            <span className="ml-0.5 whitespace-nowrap rounded px-1 text-[11px] leading-4 text-white shadow" style={{ background: color }}>
                                {cursor.username}
                            </span>
                        </div>
                    </div>
                );
            })}
        </div>
    );
}

type Connecting = { nodeId: string; handleType: "source" | "target" };

export default function CollabRoomPage() {
    const params = useParams<{ roomId: string }>();
    const router = useRouter();
    const [mounted, setMounted] = useState(false);
    const sharedEnabled = isSharedCanvasEnabled();
    const {
        room,
        nodes,
        connections,
        revision,
        loading,
        loadError,
        pending,
        conflict,
        canEdit,
        createTextNode,
        moveNode,
        resizeNode,
        updateNodeContent,
        updateNodeTitle,
        deleteNodes,
        connectNodes,
        disconnectEdge,
        flushNow,
        retryPending,
        resolveConflictUseServer,
        resolveConflictKeepLocal,
        applyServerChanges,
        applyRemoteChanges,
        applyRemoteSnapshot,
    } = useCollabRoomEditing(params.roomId, sharedEnabled);
    // 实时接收与在线状态（#14）：断线重连后本地未确认草稿由编辑层保留，重连时自动补发。
    const { status: realtimeStatus, members, cursors, error: realtimeError, sendCursor } = useCollabRoomRealtime(params.roomId, sharedEnabled, revision, {
        applyRemoteChanges,
        applyRemoteSnapshot,
        resumePending: retryPending,
    });
    // 协作撤销/重做（#15）：接入 #11 历史接口，按用户隔离，仅撤销本人操作。
    const history = useCollabRoomHistory(params.roomId, sharedEnabled);
    const colorTheme = useThemeStore((state) => state.theme);
    const theme = canvasThemes[colorTheme];
    // 邀请链接加入（#16）：读取 fragment 令牌，未登录先登录，登录后自动 accept。
    const { joining, joinError } = useCollabInviteJoin(params.roomId, sharedEnabled);
    const [sharingOpen, setSharingOpen] = useState(false);
    // 学校共享队列浮层（#23）：展示全房间共享 FIFO 队列、个人取消与执行器异常提示。
    const [schoolOpen, setSchoolOpen] = useState(false);
    // 画布工具：默认「选择」，节点可拖动/双击编辑；「移动画布」用于平移视图。
    // 必须默认 select：pan 工具会在 pointerdown 捕获阶段把指针捕获到画布容器，
    // 使节点收不到鼠标事件，导致拖动与双击编辑全部失效。
    const [canvasTool, setCanvasTool] = useState<"select" | "pan">("select");
    const containerRef = useRef<HTMLDivElement>(null);
    const didCenterRef = useRef(false);
    const [viewport, setViewport] = useState<ViewportTransform>({ x: 0, y: 0, k: 1 });
    const [selectedNodeIds, setSelectedNodeIds] = useState<Set<string>>(new Set());
    const [selectedConnectionId, setSelectedConnectionId] = useState<string | null>(null);
    const [connecting, setConnecting] = useState<Connecting | null>(null);

    const viewportRef = useRef(viewport);
    viewportRef.current = viewport;
    const connectingRef = useRef<Connecting | null>(null);
    connectingRef.current = connecting;
    const selectionRef = useRef<Set<string>>(new Set());
    selectionRef.current = selectedNodeIds;
    const selectedConnectionRef = useRef<string | null>(null);
    selectedConnectionRef.current = selectedConnectionId;
    const canEditRef = useRef(canEdit);
    // 访问失效（权限被撤销）后立即停止编辑，不再接受协作广播。
    const editable = canEdit && realtimeStatus !== "revoked";
    canEditRef.current = editable;
    const dragRef = useRef<{ id: string; startX: number; startY: number; origin: Position; moved: boolean } | null>(null);

    useEffect(() => setMounted(true), []);

    // 共享功能开关关闭时回退到原生单人画布库。
    useEffect(() => {
        if (!sharedEnabled) router.replace("/canvas");
    }, [router, sharedEnabled]);

    // 首帧把房间公共图居中显示。
    useEffect(() => {
        if (didCenterRef.current || loading || !nodes.length) return;
        const rect = containerRef.current?.getBoundingClientRect();
        if (!rect) return;
        const xs = nodes.map((node) => node.position.x + node.width / 2);
        const ys = nodes.map((node) => node.position.y + node.height / 2);
        const centerX = (Math.min(...xs) + Math.max(...xs)) / 2;
        const centerY = (Math.min(...ys) + Math.max(...ys)) / 2;
        didCenterRef.current = true;
        setViewport({ x: rect.width / 2 - centerX, y: rect.height / 2 - centerY, k: 1 });
    }, [loading, nodes]);

    // 拖动/缩放结束后立即提交，避免等待防抖。
    const flushNowRef = useRef(flushNow);
    flushNowRef.current = flushNow;
    useEffect(() => {
        const handleMouseUp = () => flushNowRef.current();
        window.addEventListener("mouseup", handleMouseUp);
        return () => window.removeEventListener("mouseup", handleMouseUp);
    }, []);

    // 撤销/重做：成功后把回执 changes 应用到本地共享图（WS 广播按版本去重，不会重复应用）。
    async function handleUndo() {
        const result = await history.undo();
        if (result) applyServerChanges(result.changes);
    }

    async function handleRedo() {
        const result = await history.redo();
        if (result) applyServerChanges(result.changes);
    }

    const undoRef = useRef<() => void>(noop);
    const redoRef = useRef<() => void>(noop);
    undoRef.current = () => void handleUndo();
    redoRef.current = () => void handleRedo();

    // 撤销/重做快捷键：Ctrl/⌘+Z 撤销，Ctrl+Y 或 Ctrl+Shift+Z 重做；输入态不拦截。
    useEffect(() => {
        const handleHistoryKey = (event: KeyboardEvent) => {
            if (!canEditRef.current || !(event.ctrlKey || event.metaKey)) return;
            const target = event.target as HTMLElement | null;
            if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
            const key = event.key.toLowerCase();
            if (key === "z" && !event.shiftKey) {
                event.preventDefault();
                undoRef.current();
                return;
            }
            if (key === "y" || (key === "z" && event.shiftKey)) {
                event.preventDefault();
                redoRef.current();
            }
        };
        window.addEventListener("keydown", handleHistoryKey);
        return () => window.removeEventListener("keydown", handleHistoryKey);
    }, []);

    // 上报本端协作光标：换算为画布世界坐标，客户端节流后由服务端再次节流合并。
    useEffect(() => {
        if (!mounted) return;
        const container = containerRef.current;
        if (!container) return;
        const handlePointerMove = (event: PointerEvent) => {
            const rect = container.getBoundingClientRect();
            const current = viewportRef.current;
            sendCursor({ x: (event.clientX - rect.left - current.x) / current.k, y: (event.clientY - rect.top - current.y) / current.k });
        };
        container.addEventListener("pointermove", handlePointerMove);
        return () => container.removeEventListener("pointermove", handlePointerMove);
    }, [mounted, sendCursor]);

    // 键盘删除选中节点/连线；输入态不拦截。
    useEffect(() => {
        const handleKeyDown = (event: KeyboardEvent) => {
            if (!canEditRef.current) return;
            const target = event.target as HTMLElement | null;
            if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
            if (event.key === "Escape") {
                setConnecting(null);
                setSelectedNodeIds(new Set());
                setSelectedConnectionId(null);
                return;
            }
            if (event.key !== "Delete" && event.key !== "Backspace") return;
            if (selectedConnectionRef.current) {
                event.preventDefault();
                disconnectEdge(selectedConnectionRef.current);
                setSelectedConnectionId(null);
                return;
            }
            if (selectionRef.current.size) {
                event.preventDefault();
                deleteNodes([...selectionRef.current]);
                setSelectedNodeIds(new Set());
            }
        };
        window.addEventListener("keydown", handleKeyDown);
        return () => window.removeEventListener("keydown", handleKeyDown);
    }, [deleteNodes, disconnectEdge]);

    const groupChildCountById = useMemo(() => {
        const map = new Map<string, number>();
        nodes.forEach((node) => {
            const groupId = node.metadata?.groupId;
            if (groupId) map.set(groupId, (map.get(groupId) || 0) + 1);
        });
        return map;
    }, [nodes]);

    const batchCountById = useMemo(() => {
        const map = new Map<string, number>();
        nodes.forEach((node) => {
            if (node.metadata?.isBatchRoot) map.set(node.id, node.metadata.batchChildIds?.length || 0);
        });
        return map;
    }, [nodes]);

    // 冲突差异摘要：本地草稿 vs 服务端最新版本，帮助用户安全选择。
    const conflictDiff = useMemo(() => (conflict ? describeNodeConflict(conflict.localNode, conflict.serverNode) : []), [conflict]);

    function canvasCenterPosition(): Position {
        const rect = containerRef.current?.getBoundingClientRect();
        const current = viewportRef.current;
        const centerX = rect ? rect.width / 2 : 0;
        const centerY = rect ? rect.height / 2 : 0;
        const worldX = (centerX - current.x) / current.k;
        const worldY = (centerY - current.y) / current.k;
        return { x: worldX - TEXT_NODE_SIZE.width / 2, y: worldY - TEXT_NODE_SIZE.height / 2 };
    }

    function handleCanvasDeselect() {
        setConnecting(null);
        setSelectedNodeIds(new Set());
        setSelectedConnectionId(null);
    }

    function handleConnectStart(event: React.MouseEvent, nodeId: string, handleType: "source" | "target") {
        if (!canEditRef.current) return;
        event.stopPropagation();
        setSelectedConnectionId(null);
        setConnecting({ nodeId, handleType });
    }

    function handleNodeMouseDown(event: React.MouseEvent, nodeId: string) {
        event.stopPropagation();
        const active = connectingRef.current;
        if (active) {
            setConnecting(null);
            if (active.nodeId === nodeId) return;
            if (active.handleType === "source") connectNodes(active.nodeId, nodeId);
            else connectNodes(nodeId, active.nodeId);
            return;
        }
        if (!canEditRef.current) return;
        setSelectedConnectionId(null);
        setSelectedNodeIds(new Set([nodeId]));
        const node = nodes.find((item) => item.id === nodeId);
        if (!node) return;
        dragRef.current = { id: nodeId, startX: event.clientX, startY: event.clientY, origin: { ...node.position }, moved: false };
        window.addEventListener("pointermove", handleDragMove);
        window.addEventListener("pointerup", handleDragEnd);
    }

    function handleDragMove(event: PointerEvent) {
        const drag = dragRef.current;
        if (!drag) return;
        const dx = (event.clientX - drag.startX) / viewportRef.current.k;
        const dy = (event.clientY - drag.startY) / viewportRef.current.k;
        if (Math.abs(event.clientX - drag.startX) > 3 || Math.abs(event.clientY - drag.startY) > 3) drag.moved = true;
        if (!drag.moved) return;
        moveNode(drag.id, { x: drag.origin.x + dx, y: drag.origin.y + dy });
    }

    function handleDragEnd() {
        window.removeEventListener("pointermove", handleDragMove);
        window.removeEventListener("pointerup", handleDragEnd);
        if (!dragRef.current) return;
        dragRef.current = null;
        flushNowRef.current();
    }

    function handleCreateTextNode() {
        const id = createTextNode(canvasCenterPosition());
        setSelectedNodeIds(new Set([id]));
        setSelectedConnectionId(null);
    }

    if (!mounted || !sharedEnabled) return null;

    const hasSelection = selectedNodeIds.size > 0 || !!selectedConnectionId;

    return (
        <main className="relative h-full min-h-0 overflow-hidden bg-background text-foreground">
            <div className="absolute left-4 top-4 z-[80] flex items-center gap-3">
                <button
                    type="button"
                    onClick={() => router.push("/canvas")}
                    className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80"
                    style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                >
                    <ChevronLeft className="size-4" />
                    返回画布库
                </button>
                <div className="flex h-9 items-center gap-2 rounded-lg border px-3 text-sm backdrop-blur" style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}>
                    <span className="font-medium">{room?.title || "共享画布"}</span>
                    <span className="rounded px-1.5 py-0.5 text-xs opacity-70" style={{ background: theme.canvas.background }}>
                        {editable ? "可编辑" : "只读"}
                    </span>
                    <span className="text-xs opacity-60">r{revision}</span>
                </div>
                <div className="flex h-9 items-center gap-2 rounded-lg border px-3 text-sm backdrop-blur" style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}>
                    <Users className="size-4" />
                    <span className="font-medium">在线 {members.length}</span>
                    {members.length ? <span className="max-w-[200px] truncate text-xs opacity-70">{members.map((member) => member.username).join("、")}</span> : null}
                </div>
                {room ? (
                    <button
                        type="button"
                        onClick={() => setSharingOpen(true)}
                        className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80"
                        style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                    >
                        <Share2 className="size-4" />
                        成员与分享
                    </button>
                ) : null}
                {room ? (
                    <button
                        type="button"
                        onClick={() => setSchoolOpen((current) => !current)}
                        className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80"
                        style={{ background: theme.toolbar.panel, borderColor: schoolOpen ? "#3b82f6" : theme.toolbar.border, color: theme.node.text }}
                    >
                        <ListOrdered className="size-4" />
                        学校队列
                    </button>
                ) : null}
                <div
                    className="flex h-9 items-center gap-1 rounded-lg border px-3 text-xs backdrop-blur"
                    style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: realtimeStatus === "revoked" ? "#ef4444" : theme.node.text }}
                >
                    {realtimeStatus === "connected" ? <Wifi className="size-3.5" /> : <WifiOff className="size-3.5" />}
                    {REALTIME_STATUS_LABEL[realtimeStatus]}
                </div>
                <button
                    type="button"
                    onClick={() => setCanvasTool((current) => (current === "pan" ? "select" : "pan"))}
                    className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80"
                    style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                    title={canvasTool === "pan" ? "切换为选择（可拖动与双击编辑节点）" : "切换为移动画布"}
                >
                    {canvasTool === "pan" ? <Hand className="size-4" /> : <MousePointer2 className="size-4" />}
                    {canvasTool === "pan" ? "移动画布" : "选择"}
                </button>
                {editable ? (
                    <>
                        <button
                            type="button"
                            disabled={!history.undoTarget || history.busy}
                            onClick={() => void handleUndo()}
                            className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-40"
                            style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                            title="撤销（Ctrl/⌘+Z，仅撤销本人操作）"
                        >
                            <Undo2 className="size-4" />
                            撤销
                        </button>
                        <button
                            type="button"
                            disabled={!history.redoTarget || history.busy}
                            onClick={() => void handleRedo()}
                            className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-40"
                            style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                            title="重做（Ctrl+Y / Ctrl+Shift+Z，仅重做本人操作）"
                        >
                            <Redo2 className="size-4" />
                            重做
                        </button>
                    </>
                ) : null}
                {editable ? (
                    <button
                        type="button"
                        onClick={handleCreateTextNode}
                        className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80"
                        style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                    >
                        <Plus className="size-4" />
                        新建文本节点
                    </button>
                ) : null}
                {editable && hasSelection ? (
                    <button
                        type="button"
                        onClick={() => {
                            if (selectedConnectionId) {
                                disconnectEdge(selectedConnectionId);
                                setSelectedConnectionId(null);
                                return;
                            }
                            deleteNodes([...selectedNodeIds]);
                            setSelectedNodeIds(new Set());
                        }}
                        className="flex h-9 items-center gap-1 rounded-lg border px-3 text-sm font-medium backdrop-blur transition hover:opacity-80"
                        style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                    >
                        <Trash2 className="size-4" />
                        删除
                    </button>
                ) : null}
            </div>

            <InfiniteCanvas
                containerRef={containerRef}
                viewport={viewport}
                tool={canvasTool}
                backgroundMode="lines"
                onViewportChange={setViewport}
                onCanvasDeselect={handleCanvasDeselect}
            >
                <svg className="absolute left-0 top-0 h-[10000px] w-[10000px] overflow-visible" style={{ pointerEvents: "none", transform: "translateZ(0)", zIndex: 6 }}>
                    {connections.map((connection) => {
                        const from = nodes.find((node) => node.id === connection.fromNodeId);
                        const to = nodes.find((node) => node.id === connection.toNodeId);
                        if (!from || !to) return null;
                        return (
                            <ConnectionPath
                                key={connection.id}
                                connection={connection}
                                from={from}
                                to={to}
                                active={selectedConnectionId === connection.id}
                                onSelect={() => {
                                    if (!canEditRef.current) return;
                                    setSelectedNodeIds(new Set());
                                    setSelectedConnectionId(connection.id);
                                }}
                            />
                        );
                    })}
                </svg>

                {nodes.map((node) => (
                    <CanvasNode
                        key={node.id}
                        data={node}
                        readOnly={!editable}
                        scale={viewport.k}
                        isSelected={selectedNodeIds.has(node.id)}
                        isRelated={false}
                        isFocusRelated={false}
                        isConnectionTarget={false}
                        isConnecting={!!connecting}
                        showPanel={false}
                        showImageInfo={false}
                        batchCount={batchCountById.get(node.id) || 0}
                        groupChildCount={groupChildCountById.get(node.id) || 0}
                        mentionReferences={[]}
                        onMouseDown={handleNodeMouseDown}
                        onHoverStart={noop}
                        onHoverEnd={noop}
                        onConnectStart={handleConnectStart}
                        onResize={(id, width, height, position) => resizeNode(id, width, height, position)}
                        onContentChange={updateNodeContent}
                        onTitleChange={updateNodeTitle}
                        onContextMenu={(event) => event.preventDefault()}
                    />
                ))}

                <RemoteCursorLayer cursors={cursors} />
            </InfiniteCanvas>

            {realtimeStatus === "revoked" ? (
                <div className="pointer-events-none absolute inset-x-0 top-16 z-[95] grid place-items-center">
                    <div className="rounded-lg border px-5 py-3 text-sm" style={{ background: theme.toolbar.panel, borderColor: "#ef4444", color: "#ef4444" }}>
                        {realtimeError?.message || "协作访问已失效，请刷新页面"}
                    </div>
                </div>
            ) : null}

            {(pending.count > 0 || pending.error) && !conflict ? (
                <div
                    className="absolute bottom-4 left-4 z-[90] flex max-w-[420px] items-start gap-2 rounded-lg border px-3 py-2 text-xs backdrop-blur"
                    style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                >
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" style={{ color: pending.error ? "#f59e0b" : theme.node.muted }} />
                    <div className="flex-1 space-y-0.5">
                        <div>
                            {pending.sending ? "正在提交协作操作..." : `待确认变更 ${pending.count} 项`}
                            {pending.error ? `（${pending.error.code}）` : ""}
                        </div>
                        {pending.error ? <div className="opacity-70">{pending.error.message}，草稿已保留。</div> : null}
                    </div>
                    {pending.error ? (
                        <button
                            type="button"
                            onClick={retryPending}
                            className="flex items-center gap-1 rounded border px-2 py-1 transition hover:opacity-80"
                            style={{ borderColor: theme.toolbar.border }}
                        >
                            <RefreshCw className="size-3" />
                            重试
                        </button>
                    ) : null}
                </div>
            ) : null}

            {conflict ? (
                <div
                    className="absolute bottom-6 left-1/2 z-[92] w-[min(560px,calc(100vw-2rem))] -translate-x-1/2 rounded-lg border px-4 py-3 text-sm backdrop-blur"
                    style={{ background: theme.toolbar.panel, borderColor: "#f59e0b", color: theme.node.text }}
                >
                    <div className="flex items-start gap-2">
                        <AlertTriangle className="mt-0.5 size-4 shrink-0" style={{ color: "#f59e0b" }} />
                        <div className="flex-1 space-y-1">
                            <div className="font-medium">{conflictTitle(conflict)}</div>
                            <div className="text-xs opacity-70">
                                {conflict.targetKind === "node" ? "节点" : "连线"} {conflict.targetId}
                                {conflict.serverVersion !== null ? ` · 服务端版本 v${conflict.serverVersion}` : ""}
                            </div>
                            {conflictDiff.length ? (
                                <ul className="space-y-0.5 text-xs opacity-80">
                                    {conflictDiff.map((line) => (
                                        <li key={line}>· {line}</li>
                                    ))}
                                </ul>
                            ) : null}
                            <div className="text-xs opacity-70">你的草稿已保留，不会被静默丢弃。</div>
                        </div>
                    </div>
                    <div className="mt-3 flex items-center justify-end gap-2">
                        <button type="button" onClick={resolveConflictUseServer} className="rounded border px-3 py-1.5 text-xs transition hover:opacity-80" style={{ borderColor: theme.toolbar.border }}>
                            用服务端最新版本
                        </button>
                        <button type="button" onClick={resolveConflictKeepLocal} className="rounded border px-3 py-1.5 text-xs font-medium transition hover:opacity-80" style={{ borderColor: "#f59e0b", color: theme.node.text }}>
                            保留本地草稿并重试
                        </button>
                    </div>
                </div>
            ) : null}

            {history.error ? (
                <div className="absolute inset-x-0 top-28 z-[94] grid place-items-center px-4">
                    <div
                        className="flex items-start gap-2 rounded-lg border px-4 py-2 text-sm backdrop-blur"
                        style={{ background: theme.toolbar.panel, borderColor: history.invalidated ? "#f59e0b" : "#ef4444", color: history.invalidated ? "#f59e0b" : "#ef4444" }}
                    >
                        <AlertTriangle className="mt-0.5 size-4 shrink-0" />
                        <span>{history.error}</span>
                        <button type="button" onClick={history.clearError} className="ml-2 opacity-70 transition hover:opacity-100" title="关闭">
                            <X className="size-3.5" />
                        </button>
                    </div>
                </div>
            ) : null}

            {loading ? (
                <div className="pointer-events-none absolute inset-0 z-[90] grid place-items-center text-sm opacity-80">
                    <span className="flex items-center gap-2">
                        <Loader2 className="size-4 animate-spin" />
                        正在加载共享画布...
                    </span>
                </div>
            ) : loadError ? (
                <div className="pointer-events-none absolute inset-0 z-[90] grid place-items-center">
                    <div className="rounded-lg border px-5 py-4 text-sm" style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}>
                        {loadError}
                    </div>
                </div>
            ) : !nodes.length ? (
                <div className="pointer-events-none absolute inset-0 z-[90] grid place-items-center text-sm opacity-60">这个共享画布还没有内容</div>
            ) : null}

            {joining ? (
                <div className="pointer-events-none absolute inset-0 z-[95] grid place-items-center">
                    <span className="flex items-center gap-2 rounded-lg border px-5 py-3 text-sm backdrop-blur" style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}>
                        <Loader2 className="size-4 animate-spin" />
                        正在加入共享画布...
                    </span>
                </div>
            ) : joinError ? (
                <div className="pointer-events-none absolute inset-x-0 top-16 z-[95] grid place-items-center">
                    <div className="rounded-lg border px-5 py-3 text-sm" style={{ background: theme.toolbar.panel, borderColor: "#ef4444", color: "#ef4444" }}>
                        {joinError}
                    </div>
                </div>
            ) : null}

            {room ? <CollabSharingPanel room={room} open={sharingOpen} onClose={() => setSharingOpen(false)} /> : null}
            {room ? <CollabSchoolQueuePanel roomId={params.roomId} open={schoolOpen} onClose={() => setSchoolOpen(false)} canSubmit={editable} /> : null}
        </main>
    );
}
