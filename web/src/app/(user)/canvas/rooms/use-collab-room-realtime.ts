"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import {
    buildCollabWSURL,
    fetchCollabWSTicket,
    isFatalCollabCloseCode,
    isFatalCollabErrorCode,
    COLLAB_CLOSE_FORBIDDEN,
    COLLAB_REALTIME_PROTOCOL_VERSION,
    type CollabChangeView,
    type CollabCursorInfo,
    type CollabEdgeView,
    type CollabErrorEvent,
    type CollabMemberInfo,
    type CollabNodeView,
    type CollabPosition,
    type CollabServerEvent,
} from "@/services/api/collab-realtime";
import { useUserStore } from "@/stores/use-user-store";

// 共享房间实时同步：ticket 建连 → hello（协议校验）→ 分帧快照/changes/presence/cursors/error。
// 断线（断网、切标签页、服务端 4401/4403/4429）自动重连：带当前 revision 重发 hello，
// 由服务端决定补发 changes 或重发全量快照；重连期间本地未确认草稿由编辑层保留，不清空。
// 权限被撤销（error/关闭码）后停止重连与广播消费，页面据此显示访问失效。

export type CollabRealtimeStatus = "connecting" | "connected" | "reconnecting" | "revoked" | "offline";

export type CollabRealtimeHandlers = {
    applyRemoteChanges: (changes: CollabChangeView[]) => void;
    applyRemoteSnapshot: (snapshot: { revision: number; nodes: CollabNodeView[]; edges: CollabEdgeView[] }) => void;
    resumePending: () => void;
};

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 15000;
// 后端对单连接光标上行做 50ms 节流，这里略大以避免被丢弃。
const CURSOR_SEND_INTERVAL_MS = 60;
// 远端光标在无更新超过该时长后移除（服务端不发送离开事件）。
const CURSOR_TTL_MS = 10000;
const CURSOR_PRUNE_INTERVAL_MS = 3000;

export function useCollabRoomRealtime(roomId: string, enabled: boolean, revision: number, handlers: CollabRealtimeHandlers) {
    const token = useUserStore((state) => state.token);
    const selfUserId = useUserStore((state) => state.user?.id ?? "");
    const [status, setStatus] = useState<CollabRealtimeStatus>("connecting");
    const [members, setMembers] = useState<CollabMemberInfo[]>([]);
    const [cursors, setCursors] = useState<CollabCursorInfo[]>([]);
    const [error, setError] = useState<CollabErrorEvent | null>(null);

    const socketRef = useRef<WebSocket | null>(null);
    const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const reconnectAttemptRef = useRef(0);
    const fatalRef = useRef(false);
    const snapshotRef = useRef<{ nodes: CollabNodeView[]; edges: CollabEdgeView[] } | null>(null);
    const cursorMapRef = useRef<Map<string, { info: CollabCursorInfo; at: number }>>(new Map());
    const lastCursorSentRef = useRef(0);
    const revisionRef = useRef(revision);
    const handlersRef = useRef(handlers);
    const selfUserIdRef = useRef(selfUserId);

    revisionRef.current = revision;
    handlersRef.current = handlers;
    selfUserIdRef.current = selfUserId;

    const flushCursors = useCallback(() => {
        setCursors([...cursorMapRef.current.values()].map((entry) => entry.info));
    }, []);

    const sendCursor = useCallback((position: CollabPosition) => {
        const socket = socketRef.current;
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        const now = Date.now();
        if (now - lastCursorSentRef.current < CURSOR_SEND_INTERVAL_MS) return;
        lastCursorSentRef.current = now;
        socket.send(JSON.stringify({ type: "cursors", position }));
    }, []);

    useEffect(() => {
        if (!enabled || !roomId || !token) {
            setStatus("offline");
            setMembers([]);
            setCursors([]);
            cursorMapRef.current.clear();
            return;
        }

        let disposed = false;
        fatalRef.current = false;
        reconnectAttemptRef.current = 0;
        setStatus("connecting");

        function clearReconnectTimer() {
            if (reconnectTimerRef.current) {
                clearTimeout(reconnectTimerRef.current);
                reconnectTimerRef.current = null;
            }
        }

        function markRevoked(event: CollabErrorEvent) {
            fatalRef.current = true;
            clearReconnectTimer();
            setError(event);
            setStatus("revoked");
            setMembers([]);
            setCursors([]);
            cursorMapRef.current.clear();
            const socket = socketRef.current;
            socketRef.current = null;
            if (socket) {
                socket.onclose = null;
                socket.onmessage = null;
                socket.onerror = null;
                socket.close();
            }
        }

        function handleMessage(raw: string) {
            if (disposed || fatalRef.current) return;
            let event: CollabServerEvent;
            try {
                event = JSON.parse(raw) as CollabServerEvent;
            } catch {
                return;
            }
            switch (event.type) {
                case "hello": {
                    // 协议版本校验：不匹配按契约要求刷新，不做隐式降级。
                    if (event.protocolVersion !== COLLAB_REALTIME_PROTOCOL_VERSION) {
                        markRevoked({ type: "error", code: "protocol_version_mismatch", message: "协作协议版本不匹配，请刷新页面" });
                        return;
                    }
                    reconnectAttemptRef.current = 0;
                    setError(null);
                    setStatus("connected");
                    return;
                }
                case "snapshot-start": {
                    snapshotRef.current = { nodes: [], edges: [] };
                    return;
                }
                case "snapshot-node": {
                    snapshotRef.current?.nodes.push(event.node);
                    return;
                }
                case "snapshot-edge": {
                    snapshotRef.current?.edges.push(event.edge);
                    return;
                }
                case "snapshot-end": {
                    const buffer = snapshotRef.current;
                    snapshotRef.current = null;
                    // 后端在客户端 revision 已最新时只发开始/结束帧（空快照），此时保留本地图，避免清空。
                    if (buffer && (buffer.nodes.length > 0 || buffer.edges.length > 0 || event.revision !== revisionRef.current)) {
                        handlersRef.current.applyRemoteSnapshot({ revision: event.revision, nodes: buffer.nodes, edges: buffer.edges });
                    }
                    // 断线恢复：重连后重发未确认操作，命中服务端 (roomId,userId,operationId) 幂等回执。
                    handlersRef.current.resumePending();
                    return;
                }
                case "changes": {
                    handlersRef.current.applyRemoteChanges(event.changes || []);
                    return;
                }
                case "presence": {
                    setMembers(event.members || []);
                    return;
                }
                case "cursors": {
                    const now = Date.now();
                    for (const cursor of event.cursors || []) {
                        if (cursor.userId === selfUserIdRef.current) continue;
                        cursorMapRef.current.set(cursor.userId, { info: cursor, at: now });
                    }
                    flushCursors();
                    return;
                }
                case "error": {
                    if (isFatalCollabErrorCode(event.code)) {
                        markRevoked(event);
                        return;
                    }
                    setError(event);
                    return;
                }
                default:
                    return;
            }
        }

        function scheduleReconnect() {
            if (disposed || fatalRef.current) return;
            const attempt = reconnectAttemptRef.current++;
            const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS);
            setStatus("reconnecting");
            reconnectTimerRef.current = setTimeout(() => {
                reconnectTimerRef.current = null;
                void connect();
            }, delay);
        }

        async function connect() {
            if (disposed || fatalRef.current) return;
            clearReconnectTimer();
            setStatus(reconnectAttemptRef.current === 0 ? "connecting" : "reconnecting");

            let ticket: string;
            try {
                ticket = (await fetchCollabWSTicket(roomId)).ticket;
            } catch {
                scheduleReconnect();
                return;
            }
            if (disposed || fatalRef.current) return;

            let socket: WebSocket;
            try {
                socket = new WebSocket(buildCollabWSURL(roomId));
            } catch {
                scheduleReconnect();
                return;
            }
            socketRef.current = socket;

            socket.onopen = () => {
                const currentRevision = revisionRef.current;
                socket.send(JSON.stringify({ type: "hello", protocolVersion: COLLAB_REALTIME_PROTOCOL_VERSION, roomId, ticket, revision: currentRevision > 0 ? currentRevision : undefined }));
            };
            socket.onmessage = (message) => {
                if (typeof message.data === "string") handleMessage(message.data);
            };
            socket.onclose = (event) => {
                if (socketRef.current === socket) socketRef.current = null;
                if (disposed || fatalRef.current) return;
                // 4401/4403 表示身份或权限已失效：停止重连并显示访问失效；4429 仅退避重连。
                if (isFatalCollabCloseCode(event.code)) {
                    markRevoked({ type: "error", code: event.code === COLLAB_CLOSE_FORBIDDEN ? "forbidden" : "unauthorized", message: "协作访问已失效，请刷新页面" });
                    return;
                }
                scheduleReconnect();
            };
            // onerror 之后一定触发 onclose，由 onclose 统一决定重连。
            socket.onerror = () => undefined;
        }

        function handleResume() {
            if (disposed || fatalRef.current || document.visibilityState === "hidden") return;
            const socket = socketRef.current;
            if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) return;
            reconnectAttemptRef.current = 0;
            clearReconnectTimer();
            void connect();
        }

        const pruneTimer = setInterval(() => {
            const now = Date.now();
            let changed = false;
            cursorMapRef.current.forEach((entry, key) => {
                if (now - entry.at > CURSOR_TTL_MS) {
                    cursorMapRef.current.delete(key);
                    changed = true;
                }
            });
            if (changed) flushCursors();
        }, CURSOR_PRUNE_INTERVAL_MS);

        document.addEventListener("visibilitychange", handleResume);
        window.addEventListener("online", handleResume);
        void connect();

        return () => {
            disposed = true;
            clearReconnectTimer();
            clearInterval(pruneTimer);
            document.removeEventListener("visibilitychange", handleResume);
            window.removeEventListener("online", handleResume);
            const socket = socketRef.current;
            socketRef.current = null;
            if (socket) {
                socket.onclose = null;
                socket.onmessage = null;
                socket.onerror = null;
                socket.close();
            }
            cursorMapRef.current.clear();
        };
    }, [enabled, roomId, token, flushCursors]);

    return { status, members, cursors, error, sendCursor };
}
