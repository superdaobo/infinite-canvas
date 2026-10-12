"use client";

import { useEffect, useState } from "react";

import { fetchCollabRoom, fetchCollabRoomGraph, type CollabRoom, type CollabRoomGraph } from "@/services/api/collab-rooms";
import type { CollabEdgeView, CollabNodeView } from "@/services/api/collab-realtime";
import { useUserStore } from "@/stores/use-user-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type CanvasNodeMetadata } from "../types";

// 共享房间专用加载逻辑：只读取协作公共图，不写入个人画布 store/localforage。
// 状态全部留在页面局部，切换或离开共享模式不会污染单人模式的本地持久化。

export const ROOM_NODE_KINDS = new Set<string>(Object.values(CanvasNodeType));

function parseRoomMetadata(raw: string): CanvasNodeMetadata {
    if (!raw) return {};
    try {
        const parsed = JSON.parse(raw) as unknown;
        return parsed && typeof parsed === "object" ? (parsed as CanvasNodeMetadata) : {};
    } catch {
        return {};
    }
}

// 把房间公共图（RoomGraph）转换为画布渲染结构，复用现有 CanvasNode/CanvasConnection 类型。
export function roomGraphToCanvas(graph: CollabRoomGraph) {
    const nodes: CanvasNodeData[] = graph.nodes
        .filter((node) => ROOM_NODE_KINDS.has(node.kind))
        .map((node) => ({
            id: node.id,
            type: node.kind as CanvasNodeType,
            title: node.title,
            position: { x: node.positionX, y: node.positionY },
            width: node.width,
            height: node.height,
            metadata: parseRoomMetadata(node.metadata),
        }));

    const nodeIds = new Set(nodes.map((node) => node.id));
    const connections: CanvasConnection[] = graph.edges
        .filter((edge) => nodeIds.has(edge.fromNodeId) && nodeIds.has(edge.toNodeId))
        .map((edge) => ({ id: edge.id, fromNodeId: edge.fromNodeId, toNodeId: edge.toNodeId }));

    return { nodes, connections };
}

// WS 广播节点视图 -> 画布节点。kind 不在 8 类白名单（含他人私有节点脱敏占位 kind:"private"）时返回 null。
export function collabNodeViewToCanvas(view: CollabNodeView): CanvasNodeData | null {
    if (!ROOM_NODE_KINDS.has(view.kind)) return null;
    return {
        id: view.id,
        type: view.kind as CanvasNodeType,
        title: view.title,
        position: { x: view.position.x, y: view.position.y },
        width: view.width,
        height: view.height,
        metadata: (view.metadata as CanvasNodeMetadata | undefined) ?? {},
    };
}

export function collabEdgeViewToConnection(edge: CollabEdgeView): CanvasConnection {
    return { id: edge.id, fromNodeId: edge.fromNodeId, toNodeId: edge.toNodeId };
}

export function useCollabRoomGraph(roomId: string, enabled: boolean) {
    const token = useUserStore((state) => state.token);
    const [room, setRoom] = useState<CollabRoom | null>(null);
    const [nodes, setNodes] = useState<CanvasNodeData[]>([]);
    const [connections, setConnections] = useState<CanvasConnection[]>([]);
    const [revision, setRevision] = useState(0);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        if (!enabled || !roomId || !token) return;
        let cancelled = false;
        setLoading(true);
        setError(null);
        Promise.all([fetchCollabRoom(roomId), fetchCollabRoomGraph(roomId)])
            .then(([detail, graph]) => {
                if (cancelled) return;
                const mapped = roomGraphToCanvas(graph);
                setRoom(detail);
                setNodes(mapped.nodes);
                setConnections(mapped.connections);
                setRevision(graph.revision);
            })
            .catch((err: unknown) => {
                if (cancelled) return;
                setError(err instanceof Error ? err.message : "加载共享画布失败");
            })
            .finally(() => {
                if (!cancelled) setLoading(false);
            });
        return () => {
            cancelled = true;
        };
    }, [enabled, roomId, token]);

    return { room, nodes, connections, revision, loading, error };
}
