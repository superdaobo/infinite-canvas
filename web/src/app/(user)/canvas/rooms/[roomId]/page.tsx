"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { ChevronLeft, Loader2 } from "lucide-react";

import { canvasThemes } from "@/lib/canvas-theme";
import { useThemeStore } from "@/stores/use-theme-store";
import { CanvasNode } from "../../components/canvas-node";
import { ConnectionPath } from "../../components/canvas-connections";
import { InfiniteCanvas } from "../../components/infinite-canvas";
import { isSharedCanvasEnabled } from "../../mode";
import type { ViewportTransform } from "../../types";
import { useCollabRoomGraph } from "../use-collab-room-graph";

// 只读回调：共享模式骨架只渲染房间公共图，任何交互都不落库、不广播。
const noop = () => undefined;

// 共享房间画布入口：/canvas/rooms/:roomId
// 与单人项目路由 /canvas/:projectId 完全分离，使用独立状态边界（不使用 useCanvasStore）。
export default function CollabRoomPage() {
    const params = useParams<{ roomId: string }>();
    const router = useRouter();
    const [mounted, setMounted] = useState(false);
    const sharedEnabled = isSharedCanvasEnabled();
    const { room, nodes, connections, revision, loading, error } = useCollabRoomGraph(params.roomId, sharedEnabled);
    const colorTheme = useThemeStore((state) => state.theme);
    const theme = canvasThemes[colorTheme];
    const containerRef = useRef<HTMLDivElement>(null);
    const didCenterRef = useRef(false);
    const [viewport, setViewport] = useState<ViewportTransform>({ x: 0, y: 0, k: 1 });

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

    if (!mounted || !sharedEnabled) return null;

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
                        只读
                    </span>
                    <span className="text-xs opacity-60">r{revision}</span>
                </div>
            </div>

            <InfiniteCanvas
                containerRef={containerRef}
                viewport={viewport}
                tool="pan"
                backgroundMode="lines"
                onViewportChange={setViewport}
            >
                <svg className="absolute left-0 top-0 h-[10000px] w-[10000px] overflow-visible" style={{ pointerEvents: "none", transform: "translateZ(0)", zIndex: 6 }}>
                    {connections.map((connection) => {
                        const from = nodes.find((node) => node.id === connection.fromNodeId);
                        const to = nodes.find((node) => node.id === connection.toNodeId);
                        if (!from || !to) return null;
                        return <ConnectionPath key={connection.id} connection={connection} from={from} to={to} active={false} onSelect={noop} />;
                    })}
                </svg>

                {nodes.map((node) => (
                    <CanvasNode
                        key={node.id}
                        data={node}
                        readOnly
                        scale={viewport.k}
                        isSelected={false}
                        isRelated={false}
                        isFocusRelated={false}
                        isConnectionTarget={false}
                        isConnecting={false}
                        showPanel={false}
                        showImageInfo={false}
                        batchCount={batchCountById.get(node.id) || 0}
                        groupChildCount={groupChildCountById.get(node.id) || 0}
                        mentionReferences={[]}
                        onMouseDown={noop}
                        onHoverStart={noop}
                        onHoverEnd={noop}
                        onConnectStart={noop}
                        onResize={noop}
                        onContentChange={noop}
                        onTitleChange={noop}
                        onContextMenu={(event) => event.preventDefault()}
                    />
                ))}
            </InfiniteCanvas>

            {loading ? (
                <div className="pointer-events-none absolute inset-0 z-[90] grid place-items-center text-sm opacity-80">
                    <span className="flex items-center gap-2">
                        <Loader2 className="size-4 animate-spin" />
                        正在加载共享画布...
                    </span>
                </div>
            ) : error ? (
                <div className="pointer-events-none absolute inset-0 z-[90] grid place-items-center">
                    <div className="rounded-lg border px-5 py-4 text-sm" style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}>
                        {error}
                    </div>
                </div>
            ) : !nodes.length ? (
                <div className="pointer-events-none absolute inset-0 z-[90] grid place-items-center text-sm opacity-60">这个共享画布还没有内容</div>
            ) : null}
        </main>
    );
}
