"use client";

import { useEffect, useRef, useState } from "react";

import { fetchCollabRoom, fetchCollabRoomGraph, type CollabRoom } from "@/services/api/collab-rooms";
import { CollabOperationError, submitCollabOperations, type CollabOperation, type CollabRoomChange } from "@/services/api/collab-operations";
import type { CollabChangeView, CollabEdgeView, CollabNodeView } from "@/services/api/collab-realtime";
import { useUserStore } from "@/stores/use-user-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type Position } from "../types";
import {
    buildConnectOperation,
    buildCreateOperation,
    buildDeleteOperation,
    buildDisconnectOperation,
    buildOperationEnvelope,
    buildUpdateOperation,
    newEdgeId,
    newNodeId,
    newOperationId,
} from "./collab-operation-builders";
import { collabEdgeViewToConnection, collabNodeViewToCanvas, roomGraphToCanvas } from "./use-collab-room-graph";

// 共享房间编辑闭环：本地乐观应用 + 后台合并提交 + 服务端回执确认。
//
// - 本地状态先改先显示，变更按目标（节点/连线）合并去重后成批发往
//   POST /api/v1/collab/rooms/:id/operations；
// - 服务端确认后按 changes 回写版本（客户端版本与服务端一致），revision 前进；
// - 失败（版本冲突/非法/网络）不回滚本地状态、不清除待发送标记，草稿保留，
//   并通过 pending.error 暴露可观测的待处理状态；完整冲突交互留给 #15。
//
// 版本以 Map 独立保存，不改动热点文件 types.ts / use-canvas-store.ts。

const FLUSH_DEBOUNCE_MS = 350;
const MAX_OPERATIONS_PER_BATCH = 200;

export type CollabPendingError = { code: string; message: string; network: boolean };
export type CollabPendingState = { count: number; sending: boolean; error: CollabPendingError | null };

type Target = { kind: "node" | "edge"; id: string };

function toPendingError(error: unknown): CollabPendingError {
    if (error instanceof CollabOperationError) return { code: error.code, message: error.message, network: error.network };
    return { code: "unknown_error", message: error instanceof Error ? error.message : "协作操作失败", network: false };
}

export function useCollabRoomEditing(roomId: string, enabled: boolean) {
    const token = useUserStore((state) => state.token);
    const [room, setRoom] = useState<CollabRoom | null>(null);
    const [nodes, setNodes] = useState<CanvasNodeData[]>([]);
    const [connections, setConnections] = useState<CanvasConnection[]>([]);
    const [revision, setRevision] = useState(0);
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState<string | null>(null);
    const [pending, setPending] = useState<CollabPendingState>({ count: 0, sending: false, error: null });

    const nodesRef = useRef<CanvasNodeData[]>([]);
    const connectionsRef = useRef<CanvasConnection[]>([]);
    const nodeVersionsRef = useRef<Map<string, number>>(new Map());
    const edgeVersionsRef = useRef<Map<string, number>>(new Map());
    const dirtyNodesRef = useRef<Set<string>>(new Set());
    const dirtyEdgesRef = useRef<Set<string>>(new Set());
    const deletedNodesRef = useRef<Set<string>>(new Set());
    const deletedEdgesRef = useRef<Set<string>>(new Set());
    const localRevRef = useRef<Map<string, number>>(new Map());
    const flushingRef = useRef(false);
    const suspendedRef = useRef(false);
    const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const destroyedRef = useRef(false);
    const pendingErrorRef = useRef<CollabPendingError | null>(null);
    // 同一批内容重发时复用 operationId，命中契约幂等键，避免网络超时重试产生重复节点。
    const attemptRef = useRef<{ operationId: string; signature: string } | null>(null);
    const roomIdRef = useRef(roomId);
    roomIdRef.current = roomId;

    function hasDirty() {
        return dirtyNodesRef.current.size > 0 || dirtyEdgesRef.current.size > 0;
    }

    function syncPending() {
        setPending({ count: dirtyNodesRef.current.size + dirtyEdgesRef.current.size, sending: flushingRef.current, error: pendingErrorRef.current });
    }

    function bumpRev(id: string) {
        localRevRef.current.set(id, (localRevRef.current.get(id) || 0) + 1);
    }

    function scheduleFlush(delay = FLUSH_DEBOUNCE_MS) {
        if (timerRef.current) clearTimeout(timerRef.current);
        timerRef.current = setTimeout(() => {
            timerRef.current = null;
            flush();
        }, delay);
    }

    function applyChanges(changes: CollabRoomChange[]) {
        for (const change of changes) {
            if (change.type === "upsert" && change.node) {
                nodeVersionsRef.current.set(change.node.id, change.node.version);
            } else if (change.type === "delete" && change.node) {
                nodeVersionsRef.current.delete(change.node.id);
                deletedNodesRef.current.delete(change.node.id);
            } else if (change.type === "edge-upsert" && change.edge) {
                edgeVersionsRef.current.set(change.edge.id, change.edge.version);
            } else if (change.type === "edge-delete" && change.edge) {
                edgeVersionsRef.current.delete(change.edge.id);
                deletedEdgesRef.current.delete(change.edge.id);
            }
        }
    }

    // 远端增量（WS changes）应用到本地共享画布，与本地乐观操作合并：
    // - 本地未确认草稿（dirty）目标优先，远端不覆盖、不删除，草稿不丢；
    // - 本地待删除目标不因远端 upsert 复活，但同步服务端版本让删除命中正确版本；
    // - 其余目标按 id 幂等 upsert/delete，并用服务端 version 去重，重复事件不重复创建内容。
    function applyRemoteChanges(changes: CollabChangeView[], revision?: number) {
        // 服务端 changes 事件携带提交后的 revision；必须据此推进本地 revision，
        // 否则仅收到他人变更而不自己提交的连接会一直停留在旧 revision（例如断线重连后
        // 由增量补发追平的客户端），导致 revision 显示与房间不一致。
        if (typeof revision === "number") setRevision((current) => (revision > current ? revision : current));
        if (!changes.length) return;
        let nextNodes = nodesRef.current;
        let nextConnections = connectionsRef.current;
        let nodesChanged = false;
        let connectionsChanged = false;

        for (const change of changes) {
            if (change.type === "upsert" && change.node) {
                const view = change.node;
                if (deletedNodesRef.current.has(view.id)) {
                    nodeVersionsRef.current.set(view.id, view.version);
                    continue;
                }
                if (dirtyNodesRef.current.has(view.id)) continue;
                if (nodeVersionsRef.current.get(view.id) === view.version) continue;
                nodeVersionsRef.current.set(view.id, view.version);
                const node = collabNodeViewToCanvas(view);
                if (!node) continue;
                const index = nextNodes.findIndex((item) => item.id === node.id);
                if (index === -1) nextNodes = [...nextNodes, node];
                else {
                    nextNodes = nextNodes.slice();
                    nextNodes[index] = node;
                }
                nodesChanged = true;
            } else if (change.type === "delete" && change.node) {
                const id = change.node.id;
                if (dirtyNodesRef.current.has(id)) continue;
                nodeVersionsRef.current.delete(id);
                if (nextNodes.some((item) => item.id === id)) {
                    nextNodes = nextNodes.filter((item) => item.id !== id);
                    nodesChanged = true;
                }
                if (nextConnections.some((edge) => edge.fromNodeId === id || edge.toNodeId === id)) {
                    nextConnections = nextConnections.filter((edge) => edge.fromNodeId !== id && edge.toNodeId !== id);
                    connectionsChanged = true;
                }
            } else if (change.type === "edge-upsert" && change.edge) {
                const view = change.edge;
                if (deletedEdgesRef.current.has(view.id)) {
                    edgeVersionsRef.current.set(view.id, view.version);
                    continue;
                }
                if (dirtyEdgesRef.current.has(view.id)) continue;
                if (edgeVersionsRef.current.get(view.id) === view.version) continue;
                edgeVersionsRef.current.set(view.id, view.version);
                const edge = collabEdgeViewToConnection(view);
                const index = nextConnections.findIndex((item) => item.id === edge.id);
                if (index === -1) nextConnections = [...nextConnections, edge];
                else {
                    nextConnections = nextConnections.slice();
                    nextConnections[index] = edge;
                }
                connectionsChanged = true;
            } else if (change.type === "edge-delete" && change.edge) {
                const id = change.edge.id;
                if (dirtyEdgesRef.current.has(id)) continue;
                edgeVersionsRef.current.delete(id);
                if (nextConnections.some((edge) => edge.id === id)) {
                    nextConnections = nextConnections.filter((edge) => edge.id !== id);
                    connectionsChanged = true;
                }
            }
        }

        if (nodesChanged) commitNodes(nextNodes);
        if (connectionsChanged) commitConnections(nextConnections);
    }

    // 断线重连后按分帧快照重建本地图，规则与 applyRemoteChanges 一致：本地未确认草稿优先保留。
    function applyRemoteSnapshot(snapshot: { revision: number; nodes: CollabNodeView[]; edges: CollabEdgeView[] }) {
        const draftNodes = new Map<string, CanvasNodeData>();
        nodesRef.current.forEach((node) => {
            if (dirtyNodesRef.current.has(node.id)) draftNodes.set(node.id, node);
        });
        const draftEdges = new Map<string, CanvasConnection>();
        connectionsRef.current.forEach((edge) => {
            if (dirtyEdgesRef.current.has(edge.id)) draftEdges.set(edge.id, edge);
        });

        const nodeVersions = new Map<string, number>();
        const nextNodes: CanvasNodeData[] = [];
        for (const view of snapshot.nodes) {
            nodeVersions.set(view.id, view.version);
            if (deletedNodesRef.current.has(view.id)) continue;
            const draft = draftNodes.get(view.id);
            if (draft) {
                nextNodes.push(draft);
                continue;
            }
            const node = collabNodeViewToCanvas(view);
            if (node) nextNodes.push(node);
        }
        // 快照里没有、但本地有未确认新建草稿的节点必须保留。
        draftNodes.forEach((node, id) => {
            if (!nextNodes.some((item) => item.id === id)) nextNodes.push(node);
        });
        // 草稿目标保留本地已知版本（未确认新建保持无版本），让冲突按契约由后续提交暴露。
        draftNodes.forEach((_node, id) => {
            const known = nodeVersionsRef.current.get(id);
            if (known === undefined) nodeVersions.delete(id);
            else nodeVersions.set(id, known);
        });

        const nodeIds = new Set(nextNodes.map((node) => node.id));
        const edgeVersions = new Map<string, number>();
        const nextConnections: CanvasConnection[] = [];
        for (const view of snapshot.edges) {
            edgeVersions.set(view.id, view.version);
            if (deletedEdgesRef.current.has(view.id)) continue;
            const draft = draftEdges.get(view.id);
            if (draft) {
                nextConnections.push(draft);
                continue;
            }
            if (!nodeIds.has(view.fromNodeId) || !nodeIds.has(view.toNodeId)) continue;
            nextConnections.push(collabEdgeViewToConnection(view));
        }
        draftEdges.forEach((edge, id) => {
            if (!nextConnections.some((item) => item.id === id)) nextConnections.push(edge);
        });
        draftEdges.forEach((_edge, id) => {
            const known = edgeVersionsRef.current.get(id);
            if (known === undefined) edgeVersions.delete(id);
            else edgeVersions.set(id, known);
        });

        nodeVersionsRef.current = nodeVersions;
        edgeVersionsRef.current = edgeVersions;
        commitNodes(nextNodes);
        commitConnections(nextConnections);
        setRevision(snapshot.revision);
    }

    // 把当前待发送标记合并成一批操作。同一目标只出现一次，天然满足契约 duplicate_target 约束。
    function flush() {
        if (destroyedRef.current || flushingRef.current) return;
        const activeRoomId = roomIdRef.current;
        if (!activeRoomId) return;

        const targets: Target[] = [];
        dirtyNodesRef.current.forEach((id) => targets.push({ kind: "node", id }));
        dirtyEdgesRef.current.forEach((id) => targets.push({ kind: "edge", id }));
        if (!targets.length) return;

        const operations: CollabOperation[] = [];
        const sent: { target: Target; rev: number }[] = [];
        for (const target of targets.slice(0, MAX_OPERATIONS_PER_BATCH)) {
            const rev = localRevRef.current.get(target.id) || 0;
            if (target.kind === "node") {
                const version = nodeVersionsRef.current.get(target.id);
                if (deletedNodesRef.current.has(target.id)) {
                    if (version === undefined) {
                        // 从未确认的本地新建节点被删除：服务端无记录，直接丢弃。
                        dirtyNodesRef.current.delete(target.id);
                        localRevRef.current.delete(target.id);
                        continue;
                    }
                    operations.push(buildDeleteOperation(target.id, version));
                } else {
                    const node = nodesRef.current.find((item) => item.id === target.id);
                    if (!node) {
                        dirtyNodesRef.current.delete(target.id);
                        continue;
                    }
                    operations.push(version === undefined ? buildCreateOperation(node) : buildUpdateOperation(node, version));
                }
            } else {
                const version = edgeVersionsRef.current.get(target.id);
                if (deletedEdgesRef.current.has(target.id)) {
                    if (version === undefined) {
                        dirtyEdgesRef.current.delete(target.id);
                        localRevRef.current.delete(target.id);
                        continue;
                    }
                    operations.push(buildDisconnectOperation(target.id, version));
                } else {
                    const edge = connectionsRef.current.find((item) => item.id === target.id);
                    if (!edge) {
                        dirtyEdgesRef.current.delete(target.id);
                        continue;
                    }
                    operations.push(buildConnectOperation(edge));
                }
            }
            sent.push({ target, rev });
        }

        if (!operations.length) {
            syncPending();
            if (hasDirty()) scheduleFlush(0);
            return;
        }

        flushingRef.current = true;
        syncPending();
        // 内容相同则复用上次的 operationId：网络失败重发命中服务端幂等回执，不重复执行。
        const signature = JSON.stringify(operations);
        const operationId = attemptRef.current && attemptRef.current.signature === signature ? attemptRef.current.operationId : newOperationId();
        attemptRef.current = { operationId, signature };
        submitCollabOperations(activeRoomId, buildOperationEnvelope(activeRoomId, operationId, operations))
            .then((result) => {
                if (destroyedRef.current) return;
                applyChanges(result.changes);
                setRevision(result.revision);
                for (const item of sent) {
                    if ((localRevRef.current.get(item.target.id) || 0) !== item.rev) continue; // 期间又有新编辑，保持待发送
                    if (item.target.kind === "node") dirtyNodesRef.current.delete(item.target.id);
                    else dirtyEdgesRef.current.delete(item.target.id);
                }
                pendingErrorRef.current = null;
                suspendedRef.current = false;
                attemptRef.current = null;
            })
            .catch((error: unknown) => {
                if (destroyedRef.current) return;
                // 失败保留草稿：不回滚本地状态、不清待发送标记，等待重试或新的编辑。
                pendingErrorRef.current = toPendingError(error);
                suspendedRef.current = true;
            })
            .finally(() => {
                flushingRef.current = false;
                if (destroyedRef.current) return;
                syncPending();
                if (!suspendedRef.current && hasDirty()) scheduleFlush(0);
            });
    }

    useEffect(() => {
        if (!enabled || !roomId || !token) return;
        let cancelled = false;
        destroyedRef.current = false;
        setLoading(true);
        setLoadError(null);
        nodesRef.current = [];
        connectionsRef.current = [];
        nodeVersionsRef.current = new Map();
        edgeVersionsRef.current = new Map();
        dirtyNodesRef.current = new Set();
        dirtyEdgesRef.current = new Set();
        deletedNodesRef.current = new Set();
        deletedEdgesRef.current = new Set();
        localRevRef.current = new Map();
        pendingErrorRef.current = null;
        suspendedRef.current = false;
        attemptRef.current = null;
        setNodes([]);
        setConnections([]);
        setPending({ count: 0, sending: false, error: null });

        Promise.all([fetchCollabRoom(roomId), fetchCollabRoomGraph(roomId)])
            .then(([detail, graph]) => {
                if (cancelled) return;
                const mapped = roomGraphToCanvas(graph);
                nodesRef.current = mapped.nodes;
                connectionsRef.current = mapped.connections;
                const nodeVersions = new Map<string, number>();
                graph.nodes.forEach((node) => nodeVersions.set(node.id, node.version));
                nodeVersionsRef.current = nodeVersions;
                const edgeVersions = new Map<string, number>();
                graph.edges.forEach((edge) => edgeVersions.set(edge.id, edge.version));
                edgeVersionsRef.current = edgeVersions;
                setRoom(detail);
                setNodes(mapped.nodes);
                setConnections(mapped.connections);
                setRevision(graph.revision);
            })
            .catch((err: unknown) => {
                if (!cancelled) setLoadError(err instanceof Error ? err.message : "加载共享画布失败");
            })
            .finally(() => {
                if (!cancelled) setLoading(false);
            });

        return () => {
            cancelled = true;
            destroyedRef.current = true;
            if (timerRef.current) clearTimeout(timerRef.current);
        };
    }, [enabled, roomId, token]);

    function commitNodes(next: CanvasNodeData[]) {
        nodesRef.current = next;
        setNodes(next);
    }

    function commitConnections(next: CanvasConnection[]) {
        connectionsRef.current = next;
        setConnections(next);
    }

    function markNodeDirty(id: string) {
        dirtyNodesRef.current.add(id);
        bumpRev(id);
        syncPending();
        scheduleFlush();
    }

    function markEdgeDirty(id: string) {
        dirtyEdgesRef.current.add(id);
        bumpRev(id);
        syncPending();
        scheduleFlush();
    }

    function createTextNode(position: Position) {
        const node: CanvasNodeData = {
            id: newNodeId(),
            type: CanvasNodeType.Text,
            title: "文本",
            position,
            width: 360,
            height: 200,
            metadata: { content: "", fontSize: 14 },
        };
        commitNodes([...nodesRef.current, node]);
        markNodeDirty(node.id);
        return node.id;
    }

    function moveNode(id: string, position: Position) {
        const current = nodesRef.current.find((item) => item.id === id);
        if (!current || (current.position.x === position.x && current.position.y === position.y)) return;
        commitNodes(nodesRef.current.map((item) => (item.id === id ? { ...item, position } : item)));
        markNodeDirty(id);
    }

    function resizeNode(id: string, width: number, height: number, position?: Position) {
        if (!nodesRef.current.some((item) => item.id === id)) return;
        commitNodes(nodesRef.current.map((item) => (item.id === id ? { ...item, width, height, position: position || item.position } : item)));
        markNodeDirty(id);
    }

    function updateNodeContent(id: string, content: string) {
        if (!nodesRef.current.some((item) => item.id === id)) return;
        commitNodes(nodesRef.current.map((item) => (item.id === id ? { ...item, metadata: { ...item.metadata, content } } : item)));
        markNodeDirty(id);
    }

    function updateNodeTitle(id: string, title: string) {
        if (!nodesRef.current.some((item) => item.id === id)) return;
        commitNodes(nodesRef.current.map((item) => (item.id === id ? { ...item, title } : item)));
        markNodeDirty(id);
    }

    function deleteNodes(ids: string[]) {
        const remove = new Set(ids);
        if (!remove.size) return;
        const removedEdgeIds = connectionsRef.current.filter((edge) => remove.has(edge.fromNodeId) || remove.has(edge.toNodeId)).map((edge) => edge.id);
        commitNodes(nodesRef.current.filter((node) => !remove.has(node.id)));
        commitConnections(connectionsRef.current.filter((edge) => !remove.has(edge.fromNodeId) && !remove.has(edge.toNodeId)));
        remove.forEach((id) => {
            deletedNodesRef.current.add(id);
            dirtyNodesRef.current.add(id);
            bumpRev(id);
        });
        // 节点被删除时其连线必须同批 disconnect，否则服务端图校验失败。
        removedEdgeIds.forEach((id) => {
            deletedEdgesRef.current.add(id);
            dirtyEdgesRef.current.add(id);
            bumpRev(id);
        });
        syncPending();
        scheduleFlush(0);
    }

    function connectNodes(fromNodeId: string, toNodeId: string) {
        if (!fromNodeId || !toNodeId || fromNodeId === toNodeId) return;
        if (connectionsRef.current.some((edge) => edge.fromNodeId === fromNodeId && edge.toNodeId === toNodeId)) return;
        const edge: CanvasConnection = { id: newEdgeId(), fromNodeId, toNodeId };
        commitConnections([...connectionsRef.current, edge]);
        deletedEdgesRef.current.delete(edge.id);
        markEdgeDirty(edge.id);
    }

    function disconnectEdge(edgeId: string) {
        if (!connectionsRef.current.some((edge) => edge.id === edgeId)) return;
        commitConnections(connectionsRef.current.filter((edge) => edge.id !== edgeId));
        deletedEdgesRef.current.add(edgeId);
        dirtyEdgesRef.current.add(edgeId);
        bumpRev(edgeId);
        syncPending();
        scheduleFlush(0);
    }

    // 立即提交（拖动/缩放结束、显式重试时调用），避免等待防抖。
    function flushNow() {
        if (timerRef.current) {
            clearTimeout(timerRef.current);
            timerRef.current = null;
        }
        flush();
    }

    function retryPending() {
        suspendedRef.current = false;
        pendingErrorRef.current = null;
        syncPending();
        flushNow();
    }

    const canEdit = enabled && !!room && room.role !== "viewer";

    return {
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
        applyRemoteChanges,
        applyRemoteSnapshot,
    };
}
