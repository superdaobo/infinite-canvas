import { apiGet } from "@/services/api/request";
import { useUserStore } from "@/stores/use-user-store";

// 协作 v1 房间公共图（对应 Go model.RoomGraph / RoomNode / RoomEdge）。
// 注意：这里的 metadata 是服务端持久化的公共字段 JSON 字符串，不含 private/local 字段。

export type CollabRoomNode = {
    id: string;
    kind: string;
    title: string;
    positionX: number;
    positionY: number;
    width: number;
    height: number;
    version: number;
    metadata: string;
};

export type CollabRoomEdge = {
    id: string;
    fromNodeId: string;
    toNodeId: string;
    version: number;
};

export type CollabRoomGraph = {
    roomId: string;
    revision: number;
    nodes: CollabRoomNode[];
    edges: CollabRoomEdge[];
};

export type CollabRoomRole = "owner" | "editor" | "viewer";

export type CollabRoom = {
    id: string;
    ownerId: string;
    title: string;
    revision: number;
    role: CollabRoomRole;
    createdAt: string;
    updatedAt: string;
};

function collabToken() {
    const token = useUserStore.getState().token;
    if (!token) throw new Error("请先登录后再访问共享画布");
    return token;
}

export async function fetchCollabRoom(roomId: string) {
    return apiGet<CollabRoom>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}`, undefined, collabToken());
}

export async function fetchCollabRoomGraph(roomId: string) {
    return apiGet<CollabRoomGraph>(`/api/v1/collab/rooms/${encodeURIComponent(roomId)}/graph`, undefined, collabToken());
}
