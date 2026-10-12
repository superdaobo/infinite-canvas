import axios from "axios";

import type { CollabRoomEdge, CollabRoomNode } from "@/services/api/collab-rooms";
import { useUserStore } from "@/stores/use-user-store";

// 协作 v1 操作提交接口（对应 Go handler.CollabApplyOperations / service.ApplyRoomOperations）。
// 契约见 docs/collab-v1/contract.md 第 4 节：operationId 幂等键、expectedNodeVersion、
// 单批原子事务与 16 个错误码。这里只做传输与错误码解析，不构造业务操作。

export const COLLAB_PROTOCOL_VERSION = "1";

// 客户端侧补充的错误码：契约 16 码之外，用于表达“请求未到达服务端”的网络失败。
export const COLLAB_NETWORK_ERROR = "network_error";

export type CollabNodePayload = {
    id: string;
    kind: string;
    title: string;
    position: { x: number; y: number };
    width: number;
    height: number;
    version: number;
    metadata?: Record<string, unknown>;
};

export type CollabEdgePayload = {
    id: string;
    fromNodeId: string;
    toNodeId: string;
    version: number;
};

// 与 contract.Operation 一一对应。create 的 node.version 必须为 0。
export type CollabOperation =
    | { type: "create"; node: CollabNodePayload }
    | { type: "update"; id: string; version: number; fields: Record<string, unknown> }
    | { type: "delete"; id: string; version: number }
    | { type: "connect"; edge: CollabEdgePayload }
    | { type: "disconnect"; id: string; version: number };

export type CollabOperationEnvelope = {
    protocolVersion: string;
    roomId: string;
    operationId: string;
    operations: CollabOperation[];
};

export type CollabRoomChange = {
    type: "upsert" | "delete" | "edge-upsert" | "edge-delete" | string;
    node?: CollabRoomNode;
    edge?: CollabRoomEdge;
};

// 与 model.RoomBatchResult 对应：确认回执携带 revision 与逐项 changes。
export type CollabBatchResult = {
    operationId: string;
    revision: number;
    changes: CollabRoomChange[];
    idempotent: boolean;
};

export class CollabOperationError extends Error {
    readonly code: string;
    readonly details: Record<string, unknown>;
    readonly status: number;
    readonly network: boolean;

    constructor(options: { code: string; message: string; details?: Record<string, unknown>; status?: number; network?: boolean }) {
        super(options.message);
        this.name = "CollabOperationError";
        this.code = options.code;
        this.details = options.details || {};
        this.status = options.status ?? 0;
        this.network = options.network === true;
    }
}

function collabToken() {
    const token = useUserStore.getState().token;
    if (!token) throw new CollabOperationError({ code: "unauthorized", message: "请先登录后再提交协作操作", status: 401 });
    return token;
}

// 提交一批操作。服务端以 { code, data, msg } 响应，契约错误码放在 data.error。
// 为保留错误码用于按契约分支处理，这里直接读取原始响应而不用 apiRequest（后者会丢弃 error）。
export async function submitCollabOperations(roomId: string, envelope: CollabOperationEnvelope): Promise<CollabBatchResult> {
    const token = collabToken();
    let response;
    try {
        response = await axios.request<{ code: number; data?: { error?: string; details?: Record<string, unknown> }; msg?: string }>({
            url: `/api/v1/collab/rooms/${encodeURIComponent(roomId)}/operations`,
            method: "POST",
            data: envelope,
            headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
            validateStatus: () => true,
        });
    } catch {
        throw new CollabOperationError({ code: COLLAB_NETWORK_ERROR, message: "网络异常，协作操作未确认，草稿已保留", network: true });
    }

    const payload = response.data;
    if (response.status >= 200 && response.status < 300 && payload && payload.code === 0) {
        return payload.data as unknown as CollabBatchResult;
    }
    const errorCode = payload?.data?.error || "invalid_payload";
    throw new CollabOperationError({
        code: errorCode,
        message: payload?.msg || "协作操作被拒绝",
        details: payload?.data?.details,
        status: response.status,
    });
}
