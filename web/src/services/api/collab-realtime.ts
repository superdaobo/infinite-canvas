import { apiPost } from "@/services/api/request";
import { useUserStore } from "@/stores/use-user-store";

// 协作 v1 实时层传输封装：ticket 换取、WS 地址推导与协议帧类型。
// 对齐 internal/collab/realtime/*.go 与 docs/collab-v1/contract.md 第 5 节。
// 这里只做纯传输与类型定义，连接状态机在 use-collab-room-realtime.ts 中。

export const COLLAB_REALTIME_PROTOCOL_VERSION = "1";

// WS 关闭码：4401 未授权/协议不符/会话失效，4403 非成员，4429 连接数超限。
export const COLLAB_CLOSE_UNAUTHORIZED = 4401;
export const COLLAB_CLOSE_FORBIDDEN = 4403;

export type CollabPosition = { x: number; y: number };

// 服务端 hello 事件：连接确认，携带协议版本、身份、角色与当前 revision。
export type CollabHelloEvent = {
    type: "hello";
    protocolVersion: string;
    roomId: string;
    userId: string;
    role: string;
    revision: number;
};

// 广播用公共图节点视图（metadata 已剥离 private/local 与凭据字段）。
export type CollabNodeView = {
    id: string;
    kind: string;
    title: string;
    position: CollabPosition;
    width: number;
    height: number;
    version: number;
    metadata?: Record<string, unknown>;
};

export type CollabEdgeView = {
    id: string;
    fromNodeId: string;
    toNodeId: string;
    version: number;
};

export type CollabSnapshotStartEvent = {
    type: "snapshot-start";
    userId: string;
    room: { id: string; title: string; revision: number; role: string };
};

export type CollabSnapshotNodeEvent = { type: "snapshot-node"; node: CollabNodeView; ownPrivate: boolean };
export type CollabSnapshotEdgeEvent = { type: "snapshot-edge"; edge: CollabEdgeView };
export type CollabSnapshotEndEvent = { type: "snapshot-end"; revision: number };

export type CollabChangeView = {
    type: "upsert" | "delete" | "edge-upsert" | "edge-delete" | string;
    node?: CollabNodeView;
    edge?: CollabEdgeView;
};

export type CollabChangesEvent = {
    type: "changes";
    operationId: string;
    historyId: string;
    revision: number;
    changes: CollabChangeView[];
};

export type CollabMemberInfo = { userId: string; username: string };
export type CollabPresenceEvent = { type: "presence"; count: number; members: CollabMemberInfo[] };
export type CollabCursorInfo = { userId: string; username: string; position: CollabPosition };
export type CollabCursorsEvent = { type: "cursors"; cursors: CollabCursorInfo[] };
export type CollabErrorEvent = { type: "error"; code: string; message: string; details?: Record<string, unknown> };

export type CollabServerEvent =
    | CollabHelloEvent
    | CollabSnapshotStartEvent
    | CollabSnapshotNodeEvent
    | CollabSnapshotEdgeEvent
    | CollabSnapshotEndEvent
    | CollabChangesEvent
    | CollabPresenceEvent
    | CollabCursorsEvent
    | CollabErrorEvent;

// 首帧 hello（C→S）：ticket 绑定身份，revision 用于断线恢复判定。
export type CollabHelloMessage = { type: "hello"; protocolVersion: string; roomId: string; ticket: string; revision?: number };

// 协作光标上行帧（C→S）：position 为画布世界坐标。
export type CollabCursorMessage = { type: "cursors"; position: CollabPosition };

// 契约 5.1：JWT 不放进 URL，先用主账号 JWT 换取短时效单次 ticket。
export async function fetchCollabWSTicket(roomId: string) {
    const token = useUserStore.getState().token;
    if (!token) throw new Error("请先登录后再连接共享画布");
    return apiPost<{ ticket: string; roomId: string; protocolVersion: string; expiresAt: string }>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/ws-ticket`, {}, token);
}

// WS 地址默认与页面同源（生产由反向代理把 /api/v1/collab/rooms/:id/ws 升级到 Go 后端）；
// 前后端分离开发时用 NEXT_PUBLIC_COLLAB_WS_BASE 指向 Go 端口，例如 ws://127.0.0.1:8080。
export function buildCollabWSURL(roomId: string) {
    const path = `/api/v1/collab/rooms/${encodeURIComponent(roomId)}/ws`;
    const base = process.env.NEXT_PUBLIC_COLLAB_WS_BASE;
    if (base) return `${base.replace(/\/$/, "")}${path}`;
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${protocol}//${window.location.host}${path}`;
}

// 权限失效错误码：出现即视为访问被撤销，停止重连与广播消费。
const FATAL_ERROR_CODES = new Set(["unauthorized", "forbidden", "not_found", "protocol_version_mismatch", "private_node_forbidden"]);

export function isFatalCollabErrorCode(code: string) {
    return FATAL_ERROR_CODES.has(code);
}

// 关闭码判定：4401/4403 表示身份或权限已失效，4429 只是连接数超限（可退避重连）。
export function isFatalCollabCloseCode(code: number) {
    return code === COLLAB_CLOSE_UNAUTHORIZED || code === COLLAB_CLOSE_FORBIDDEN;
}
