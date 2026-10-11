"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { AlertTriangle, ChevronLeft, Loader2, Plus, RefreshCw, Trash2 } from "lucide-react";

import { canvasThemes } from "@/lib/canvas-theme";
import { useThemeStore } from "@/stores/use-theme-store";
import { CanvasNode } from "../../components/canvas-node";
import { ConnectionPath } from "../../components/canvas-connections";
import { InfiniteCanvas } from "../../components/infinite-canvas";
import { isSharedCanvasEnabled } from "../../mode";
import type { Position, ViewportTransform } from "../../types";
import { useCollabRoomEditing } from "../use-collab-room-editing";

// 共享房间画布入口：/canvas/rooms/:roomId
// 与单人项目路由 /canvas/:projectId 完全分离，使用独立状态边界（不使用 useCanvasStore）。
// 编辑能力按 #13 交付：节点/连线变更构造为协作操作并经服务端回执确认（见 use-collab-room-editing）。

const noop = () => undefined;
const TEXT_NODE_SIZE = { width: 360, height: 200 };

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
    } = useCollabRoomEditing(params.roomId, sharedEnabled);
    const colorTheme = useThemeStore((state) => state.theme);
    const theme = canvasThemes[colorTheme];
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
    canEditRef.current = canEdit;
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
                        {canEdit ? "可编辑" : "只读"}
                    </span>
                    <span className="text-xs opacity-60">r{revision}</span>
                </div>
                {canEdit ? (
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
                {canEdit && hasSelection ? (
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
                tool="pan"
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
                        readOnly={!canEdit}
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
            </InfiniteCanvas>

            {pending.count > 0 || pending.error ? (
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
        </main>
    );
}
