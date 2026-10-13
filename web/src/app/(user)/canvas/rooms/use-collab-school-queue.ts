"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { fetchRoomMembers } from "@/services/api/collab-sharing";
import {
    cancelRoomSchoolTask,
    fetchRoomSchoolTasks,
    submitRoomSchoolTask,
    type SchoolChannel,
    type SchoolSubmitResult,
    type SchoolTaskParams,
    type SchoolTaskQueue,
} from "@/services/api/collab-school";
import { useUserStore } from "@/stores/use-user-store";

// 共享房间的学校任务队列状态（C03-06 / #23）：轮询房间队列、个人取消、提交与执行器异常提示。
// 队列接口没有服务端推送，因此按固定间隔轮询；轮询只读，不改变任务状态。

// 轮询间隔：兼顾实时性与请求量，队列为空时仍然轮询以发现新任务。
export const SCHOOL_QUEUE_POLL_MS = 4000;
// 执行中任务在前端观察超过该时长仍无变化即提示「长时间未推进」。仅作提示，绝不改写状态。
export const SCHOOL_STALL_WARNING_MS = 3 * 60 * 1000;

export type SchoolExecutorHint = {
    // waiting：队列有人在等但暂无执行设备；stalled：执行中任务长时间未推进；error：最近有任务失败。
    level: "waiting" | "stalled" | "error";
    title: string;
    detail: string;
};

function runningSinceKey(taskId: string) {
    return `school-running-since:${taskId}`;
}

// 用 sessionStorage 记住执行中任务的首次观察时间，刷新页面后仍可延续判断。
function readRunningSince(taskId: string, now: number) {
    const key = runningSinceKey(taskId);
    const raw = typeof window === "undefined" ? null : window.sessionStorage.getItem(key);
    const parsed = raw ? Number(raw) : Number.NaN;
    if (Number.isFinite(parsed) && parsed > 0) return parsed;
    if (typeof window !== "undefined") window.sessionStorage.setItem(key, String(now));
    return now;
}

// 派生执行器提示：只依据后端返回的 status / running / error 与前端观察时长推断，
// 绝不改写真实状态，也不把设备离线误报为任务已取消（running 始终显示为「执行中」）。
function deriveExecutorHint(queue: SchoolTaskQueue | null, stalledRunningId: string | null): SchoolExecutorHint | null {
    if (!queue) return null;
    const queued = queue.items.filter((task) => task.status === "queued");
    const running = queue.running;
    if (running) {
        const note = running.error?.trim();
        if (note && /恢复|重新绑定|rebind/i.test(note)) {
            return { level: "waiting", title: "执行器恢复中", detail: `执行设备已重新绑定，正在恢复原任务（${note}）。恢复过程不会重复生成，也不会把任务误判为已取消。` };
        }
        if (stalledRunningId === running.id) {
            const minutes = Math.max(1, Math.round(SCHOOL_STALL_WARNING_MS / 60000));
            return {
                level: "stalled",
                title: "任务长时间未推进",
                detail: `执行中任务已超过约 ${minutes} 分钟未见更新，执行设备可能已断线。任务仍保持「执行中」，不会被判定为已取消；请确认设备在线，必要时由设备侧走恢复流程。`,
            };
        }
        return null;
    }
    if (queued.length > 0) {
        return {
            level: "waiting",
            title: "等待执行设备领取",
            detail: `队列中有 ${queued.length} 个任务待执行，当前没有执行设备在处理。请确认学校执行设备已上线并上报心跳；任务会保持排队，不会丢失或自动取消。`,
        };
    }
    const failed = [...queue.items].reverse().find((task) => task.status === "failed" && task.error?.trim());
    if (failed?.error) {
        return { level: "error", title: "最近有任务执行失败", detail: `失败原因：${failed.error}。可重新提交该任务；设备离线期间失败的任务需要设备恢复后重试。` };
    }
    return null;
}

export function useCollabSchoolQueue(roomId: string, enabled: boolean) {
    const token = useUserStore((state) => state.token);
    const userId = useUserStore((state) => state.user?.id ?? "");
    const [queue, setQueue] = useState<SchoolTaskQueue | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [submitting, setSubmitting] = useState(false);
    const [cancelBusyId, setCancelBusyId] = useState<string | null>(null);
    const [actionError, setActionError] = useState<string | null>(null);
    const [stalledRunningId, setStalledRunningId] = useState<string | null>(null);
    const [memberNames, setMemberNames] = useState<Record<string, string>>({});
    const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

    const load = useCallback(async () => {
        if (!roomId) return;
        try {
            const data = await fetchRoomSchoolTasks(roomId);
            setQueue(data);
            setError(null);
            // 观察执行中任务的持续时间：同一任务持续存在超过阈值即提示未推进。
            const now = Date.now();
            const runningId = data.running?.id ?? null;
            if (!runningId) {
                setStalledRunningId(null);
            } else {
                const since = readRunningSince(runningId, now);
                setStalledRunningId(now - since >= SCHOOL_STALL_WARNING_MS ? runningId : null);
            }
        } catch (loadError) {
            setError(loadError instanceof Error ? loadError.message : "加载学校队列失败");
        }
    }, [roomId]);

    // 成员昵称映射：队列只返回提交人账号 ID，用成员列表映射为可读名称。
    const loadMembers = useCallback(async () => {
        if (!roomId) return;
        try {
            const data = await fetchRoomMembers(roomId);
            const map: Record<string, string> = {};
            for (const member of data.items || []) map[member.userId] = member.displayName || member.username || member.userId;
            setMemberNames(map);
        } catch {
            // 成员列表失败不影响队列本身，退回展示账号 ID。
        }
    }, [roomId]);

    useEffect(() => {
        if (!enabled || !token) return;
        setLoading(true);
        void Promise.all([load(), loadMembers()]).finally(() => setLoading(false));
        pollRef.current = setInterval(() => void load(), SCHOOL_QUEUE_POLL_MS);
        return () => {
            if (pollRef.current) clearInterval(pollRef.current);
            pollRef.current = null;
        };
    }, [enabled, load, loadMembers, token]);

    const submit = useCallback(
        async (channel: SchoolChannel, params: SchoolTaskParams): Promise<SchoolSubmitResult> => {
            setSubmitting(true);
            setActionError(null);
            try {
                const result = await submitRoomSchoolTask(roomId, { channel, clientTaskId: crypto.randomUUID(), params });
                await load();
                return result;
            } catch (submitError) {
                const message = submitError instanceof Error ? submitError.message : "提交学校任务失败";
                setActionError(message);
                throw submitError;
            } finally {
                setSubmitting(false);
            }
        },
        [load, roomId],
    );

    const cancel = useCallback(
        async (taskId: string) => {
            setCancelBusyId(taskId);
            setActionError(null);
            try {
                await cancelRoomSchoolTask(roomId, taskId);
                await load();
            } catch (cancelError) {
                const message = cancelError instanceof Error ? cancelError.message : "取消学校任务失败";
                setActionError(message);
                throw cancelError;
            } finally {
                setCancelBusyId(null);
            }
        },
        [load, roomId],
    );

    const executorHint = useMemo(() => deriveExecutorHint(queue, stalledRunningId), [queue, stalledRunningId]);

    const memberName = useCallback((id: string) => memberNames[id] || id, [memberNames]);

    return {
        queue,
        loading,
        error,
        submitting,
        cancelBusyId,
        actionError,
        clearActionError: () => setActionError(null),
        executorHint,
        memberName,
        userId,
        refresh: load,
        submit,
        cancel,
    };
}
