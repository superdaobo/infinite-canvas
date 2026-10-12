import axios from "axios";

import { CollabOperationError, type CollabBatchResult } from "@/services/api/collab-operations";
import { useUserStore } from "@/stores/use-user-store";

// 协作 v1 房间历史（审计）与撤销/重做接口（对应 Go handler.CollabRoomHistory /
// CollabUndoRoomHistory / CollabRedoRoomHistory，#11）。
//
// 服务端按用户隔离：只能撤销/重做自己（actorId）的操作；目标被他人修改导致历史失效时，
// 返回 409 version_conflict，details.reason = history_invalidated。这里直接读取原始响应，
// 以保留契约错误码与 details，供上层区分「历史失效」与普通失败。

export type CollabHistoryStatus = "applied" | "undone" | string;

// 与 model.RoomHistoryOp 对应的变更明细（前端只需展示与判定，不重放）。
export type CollabHistoryOp = {
    op: string;
    target: "node" | "edge" | string;
    id: string;
    active: boolean;
    version: number;
    merged?: number;
};

// 与 model.RoomHistory 对应的一次成功批次审计记录。
export type CollabHistoryEntry = {
    id: string;
    roomId: string;
    operationId: string;
    actorId: string;
    revision: number;
    summary: string;
    status: CollabHistoryStatus;
    undoneBy?: string;
    createdAt: string;
    updatedAt: string;
    ops?: CollabHistoryOp[];
};

export type CollabHistoryList = {
    items: CollabHistoryEntry[];
    total: number;
};

function collabToken() {
    const token = useUserStore.getState().token;
    if (!token) throw new CollabOperationError({ code: "unauthorized", message: "请先登录后再访问协作历史", status: 401 });
    return token;
}

async function requestHistory<T>(config: { url: string; method: "GET" | "POST" }): Promise<T> {
    const token = collabToken();
    let response;
    try {
        response = await axios.request<{ code: number; data?: unknown; msg?: string }>({
            url: config.url,
            method: config.method,
            headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
            validateStatus: () => true,
        });
    } catch {
        throw new CollabOperationError({ code: "network_error", message: "网络异常，协作历史请求未完成", network: true });
    }

    const payload = response.data;
    if (response.status >= 200 && response.status < 300 && payload && payload.code === 0) {
        return payload.data as T;
    }
    const errorData = payload?.data as { error?: string; details?: Record<string, unknown> } | undefined;
    throw new CollabOperationError({
        code: errorData?.error || "invalid_payload",
        message: payload?.msg || "协作历史请求被拒绝",
        details: errorData?.details,
        status: response.status,
    });
}

export async function fetchCollabRoomHistory(roomId: string, limit = 50) {
    return requestHistory<CollabHistoryList>({
        url: `/api/v1/collab/rooms/${encodeURIComponent(roomId)}/history?limit=${limit}`,
        method: "GET",
    });
}

export async function undoCollabHistory(roomId: string, historyId: string) {
    return requestHistory<CollabBatchResult>({
        url: `/api/v1/collab/rooms/${encodeURIComponent(roomId)}/history/${encodeURIComponent(historyId)}/undo`,
        method: "POST",
    });
}

export async function redoCollabHistory(roomId: string, historyId: string) {
    return requestHistory<CollabBatchResult>({
        url: `/api/v1/collab/rooms/${encodeURIComponent(roomId)}/history/${encodeURIComponent(historyId)}/redo`,
        method: "POST",
    });
}

// 历史失效判定：目标被他人修改，撤销/重做不会覆盖他人修改。
export function isHistoryInvalidated(error: unknown) {
    return error instanceof CollabOperationError && error.code === "version_conflict" && error.details?.reason === "history_invalidated";
}
