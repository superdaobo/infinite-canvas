import localforage from "localforage";

import type { CanvasConnection, CanvasNodeData } from "../types";

// 共享房间未确认草稿的浏览器本地持久化（localforage，遵循 AGENTS.md 前端规范）。
//
// 未确认草稿属于 private 数据（契约 2.3：按 owner 隔离，绝不广播），因此按
// `${userId}:${roomId}` 分键存储，避免同一浏览器多账号互相污染。刷新页面后据此恢复
// 待发送目标并补发；草稿与服务端版本对齐（提交成功或用户选择服务端版本）后清除。

export type CollabRoomDraft = {
    userId: string;
    roomId: string;
    savedAt: number;
    revision: number;
    // 本地乐观状态：未确认的节点/连线草稿（含新建与修改）。
    nodes: CanvasNodeData[];
    edges: CanvasConnection[];
    // 本地已删除、等待服务端确认的目标。
    deletedNodeIds: string[];
    deletedEdgeIds: string[];
    // 草稿目标已知的服务端版本（新建节点/连线无版本，不在表内）。
    nodeVersions: Record<string, number>;
    edgeVersions: Record<string, number>;
    // 上次提交的幂等键与内容签名：刷新后重发可命中服务端 (roomId,userId,operationId) 回执。
    operationId?: string;
    signature?: string;
};

const store = localforage.createInstance({ name: "infinite-canvas", storeName: "collab_room_drafts" });

function draftKey(userId: string, roomId: string) {
    return `${userId}:${roomId}`;
}

export async function loadRoomDraft(userId: string, roomId: string): Promise<CollabRoomDraft | null> {
    if (!userId || !roomId) return null;
    try {
        return await store.getItem<CollabRoomDraft>(draftKey(userId, roomId));
    } catch {
        return null;
    }
}

export async function saveRoomDraft(draft: CollabRoomDraft): Promise<void> {
    if (!draft.userId || !draft.roomId) return;
    try {
        await store.setItem(draftKey(draft.userId, draft.roomId), draft);
    } catch {
        // 本地持久化失败不影响协作主流程：内存中的草稿仍然保留。
    }
}

export async function clearRoomDraft(userId: string, roomId: string): Promise<void> {
    if (!userId || !roomId) return;
    try {
        await store.removeItem(draftKey(userId, roomId));
    } catch {
        // 同上，忽略本地存储异常。
    }
}
