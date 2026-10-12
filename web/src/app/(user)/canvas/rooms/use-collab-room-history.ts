"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import type { CollabBatchResult } from "@/services/api/collab-operations";
import { CollabOperationError } from "@/services/api/collab-operations";
import { fetchCollabRoomHistory, isHistoryInvalidated, redoCollabHistory, undoCollabHistory, type CollabHistoryEntry } from "@/services/api/collab-history";
import { useUserStore } from "@/stores/use-user-store";

// 共享房间撤销/重做（#15 接入 #11 历史接口）。
//
// - 历史按用户隔离：只把本人（actorId === selfUserId）的 applied/undone 记录作为撤销/重做目标，
//   不会把他人操作纳入撤销栈；
// - 目标被他人修改导致历史失效（version_conflict / reason=history_invalidated）时抛出可识别的
//   错误，由页面提示「该操作已失效」并刷新历史，绝不覆盖他人修改；
// - 每次成功后刷新历史，保持按钮范围与最新 revision 一致。

export type CollabRoomHistory = {
    items: CollabHistoryEntry[];
    loading: boolean;
    busy: boolean;
    error: string | null;
    invalidated: boolean;
    undoTarget: CollabHistoryEntry | null;
    redoTarget: CollabHistoryEntry | null;
    refresh: () => Promise<void>;
    undo: () => Promise<CollabBatchResult | null>;
    redo: () => Promise<CollabBatchResult | null>;
    clearError: () => void;
};

export function useCollabRoomHistory(roomId: string, enabled: boolean): CollabRoomHistory {
    const token = useUserStore((state) => state.token);
    const selfUserId = useUserStore((state) => state.user?.id ?? "");
    const [items, setItems] = useState<CollabHistoryEntry[]>([]);
    const [loading, setLoading] = useState(false);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [invalidated, setInvalidated] = useState(false);
    const roomIdRef = useRef(roomId);
    roomIdRef.current = roomId;

    const refresh = useCallback(async () => {
        if (!roomId || !token) return;
        setLoading(true);
        try {
            const list = await fetchCollabRoomHistory(roomId);
            setItems(list.items || []);
            setError(null);
        } catch (err: unknown) {
            setError(err instanceof Error ? err.message : "加载协作历史失败");
        } finally {
            setLoading(false);
        }
    }, [roomId, token]);

    useEffect(() => {
        if (!enabled || !roomId || !token) {
            setItems([]);
            return;
        }
        void refresh();
    }, [enabled, roomId, token, refresh]);

    // 最新一条本人「已生效」历史即撤销目标；最新一条本人「已撤销」历史即重做目标。
    const undoTarget = items.find((item) => item.actorId === selfUserId && item.status === "applied") ?? null;
    const redoTarget = items.find((item) => item.actorId === selfUserId && item.status === "undone") ?? null;

    async function toggle(historyId: string, toAfter: boolean): Promise<CollabBatchResult | null> {
        const activeRoomId = roomIdRef.current;
        if (!activeRoomId || !historyId || busy) return null;
        setBusy(true);
        try {
            const result = toAfter ? await redoCollabHistory(activeRoomId, historyId) : await undoCollabHistory(activeRoomId, historyId);
            setError(null);
            setInvalidated(false);
            await refresh();
            return result;
        } catch (err: unknown) {
            if (isHistoryInvalidated(err)) {
                setInvalidated(true);
                setError("该操作已失效：相关节点或连线已被他人修改，已刷新历史");
                // 失效后历史状态已变，刷新以对齐按钮范围。
                await refresh();
                return null;
            }
            setError(err instanceof CollabOperationError ? err.message : err instanceof Error ? err.message : "撤销/重做失败");
            await refresh();
            return null;
        } finally {
            setBusy(false);
        }
    }

    async function undo() {
        return undoTarget ? toggle(undoTarget.id, false) : null;
    }

    async function redo() {
        return redoTarget ? toggle(redoTarget.id, true) : null;
    }

    function clearError() {
        setError(null);
        setInvalidated(false);
    }

    return { items, loading, busy, error, invalidated, undoTarget, redoTarget, refresh, undo, redo, clearError };
}
