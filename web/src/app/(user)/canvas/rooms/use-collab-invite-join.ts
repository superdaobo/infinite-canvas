"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import { canvasRoomPath } from "@/app/(user)/canvas/mode";
import { acceptRoomInvite } from "@/services/api/collab-sharing";
import { useUserStore } from "@/stores/use-user-store";

// 通过邀请链接加入房间的流程（对应 #16 交付物 2）：
//   打开 /canvas/rooms/:id#invite=<token> → 未登录先跳登录/注册 → 登录后自动调用 accept 接口加入。
//
// 令牌只放在 URL fragment，读取后立即清除：fragment 不会发送到服务端，也不会进入访问日志。
// 未登录时把令牌暂存 sessionStorage（同样不落 URL/日志），登录回跳后自动消费。
// 加入成功后整页跳回无 fragment 的房间地址，让成员权限在首帧即生效。

export type CollabInviteJoinStatus = "idle" | "joining" | "failed";

function inviteStorageKey(roomId: string) {
    return `infinite-canvas:collab-invite:${roomId}`;
}

// 从当前 fragment 取出 invite 参数并清除，保留其他 hash 参数（如 Agent 连接参数）。
function takeInviteTokenFromHash() {
    const raw = window.location.hash.slice(1);
    if (!raw) return "";
    const params = new URLSearchParams(raw);
    const token = params.get("invite") || "";
    if (!token) return "";
    const remaining = raw
        .split("&")
        .filter((part) => (new URLSearchParams(part).keys().next().value || "") !== "invite")
        .join("&");
    window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search + (remaining ? `#${remaining}` : ""));
    return token;
}

export function useCollabInviteJoin(roomId: string, enabled: boolean) {
    const router = useRouter();
    const isReady = useUserStore((state) => state.isReady);
    const authToken = useUserStore((state) => state.token);
    const [status, setStatus] = useState<CollabInviteJoinStatus>("idle");
    const [error, setError] = useState<string | null>(null);
    const handledRef = useRef(false);

    useEffect(() => {
        if (!enabled || !roomId || !isReady || handledRef.current) return;
        const hashToken = takeInviteTokenFromHash();
        const storageKey = inviteStorageKey(roomId);
        let storedToken = "";
        try {
            storedToken = sessionStorage.getItem(storageKey) || "";
        } catch {
            storedToken = "";
        }
        const token = hashToken || storedToken;
        if (!token) return;
        handledRef.current = true;

        // 未登录：暂存令牌并跳登录，登录后回跳本页自动加入。
        if (!authToken) {
            try {
                sessionStorage.setItem(storageKey, token);
            } catch {
                setError("无法保存邀请信息，请重新打开邀请链接");
                setStatus("failed");
                return;
            }
            router.replace(`/login?redirect=${encodeURIComponent(canvasRoomPath(roomId))}`);
            return;
        }

        try {
            sessionStorage.removeItem(storageKey);
        } catch {
            // 忽略：令牌已消费，清理失败不影响加入。
        }
        setStatus("joining");
        acceptRoomInvite(token)
            .then(() => window.location.replace(canvasRoomPath(roomId)))
            .catch((reason: unknown) => {
                setError(reason instanceof Error ? reason.message : "加入共享画布失败，请重新获取邀请链接");
                setStatus("failed");
            });
    }, [authToken, enabled, isReady, roomId, router]);

    return { joining: status === "joining", joinError: error };
}
