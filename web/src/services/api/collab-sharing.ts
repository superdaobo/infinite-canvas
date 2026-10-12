import { apiDelete, apiGet, apiPatch, apiPost } from "@/services/api/request";
import { canvasRoomPath } from "@/app/(user)/canvas/mode";
import { useUserStore } from "@/stores/use-user-store";

// 协作 v1 成员与分享（邀请）管理接口，对应 Go handler/collab_access.go。
// 授权矩阵见 docs/collab-v1/security-boundaries.md 第 2 节：
// 任何成员可查看成员列表；仅 owner 可改角色/移除成员/创建或撤销邀请。
// 邀请令牌明文只在创建响应返回一次，之后列表接口不再返回令牌或哈希。

export type CollabRoomMember = {
    userId: string;
    username: string;
    displayName: string;
    avatarUrl: string;
    role: "owner" | "editor" | "viewer";
    createdAt: string;
    updatedAt: string;
};

export type CollabRoomMemberList = {
    items: CollabRoomMember[];
    total: number;
};

export type CollabRoomInvite = {
    id: string;
    roomId: string;
    role: "editor" | "viewer";
    createdBy: string;
    expiresAt: string;
    revokedAt?: string;
    createdAt: string;
    updatedAt: string;
};

// 创建邀请的响应：token 仅此一次返回，之后无法再取回。
export type CollabRoomInviteCreated = CollabRoomInvite & { token: string };

export type CollabRoomInviteList = {
    items: CollabRoomInvite[];
    total: number;
};

// 邀请状态：撤销优先于过期，均按服务端时间判断。
export type CollabInviteStatus = "active" | "revoked" | "expired";

export function inviteStatus(invite: CollabRoomInvite, now = Date.now()): CollabInviteStatus {
    if (invite.revokedAt) return "revoked";
    return new Date(invite.expiresAt).getTime() <= now ? "expired" : "active";
}

// 邀请链接把令牌放在 URL fragment（#invite=...）：fragment 不会发送到服务端，
// 也不会进入访问日志；房间页读取后立即清除。沿用项目既有的 hash 参数约定。
export function buildRoomInviteLink(roomId: string, token: string) {
    const path = `${canvasRoomPath(roomId)}#invite=${encodeURIComponent(token)}`;
    return typeof window === "undefined" ? path : `${window.location.origin}${path}`;
}

function collabToken() {
    const token = useUserStore.getState().token;
    if (!token) throw new Error("请先登录后再管理共享画布");
    return token;
}

export async function fetchRoomMembers(roomId: string) {
    return apiGet<CollabRoomMemberList>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/members`, undefined, collabToken());
}

export async function updateRoomMemberRole(roomId: string, userId: string, role: "editor" | "viewer") {
    return apiPatch<CollabRoomMember>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/members/${encodeURIComponent(userId)}`, { role }, collabToken());
}

export async function removeRoomMember(roomId: string, userId: string) {
    return apiDelete<null>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/members/${encodeURIComponent(userId)}`, collabToken());
}

export async function fetchRoomInvites(roomId: string) {
    return apiGet<CollabRoomInviteList>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/invites`, undefined, collabToken());
}

export async function createRoomInvite(roomId: string, role: "editor" | "viewer", expiresInHours: number) {
    return apiPost<CollabRoomInviteCreated>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/invites`, { role, expiresInHours }, collabToken());
}

export async function revokeRoomInvite(roomId: string, inviteId: string) {
    return apiPost<null>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/invites/${encodeURIComponent(inviteId)}/revoke`, {}, collabToken());
}

// 使用邀请令牌加入房间。令牌无效/过期/撤销时服务端返回 403，此处抛出中文错误。
export async function acceptRoomInvite(token: string) {
    return apiPost<{ id: string; title: string; role: "owner" | "editor" | "viewer" }>("/api/v1/collab/invites/accept", { token }, collabToken());
}
