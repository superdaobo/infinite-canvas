import { expect, type APIRequestContext } from "@playwright/test";

import { BACKEND_URL } from "./env";
import type { TestAccount } from "./accounts";

// 协作 v1 房间准备夹具：房间、邀请、成员与公共图/操作接口。
// 房间与邀请的「准备」直接用后端 API（不依赖 #16 的分享 UI），
// 但协作交互本身必须走真实浏览器 UI（见 fixtures/collab-room.ts）。
// 契约见 docs/collab-v1/contract.md；接口见 router/router.go 的 /api/v1/collab/*。

export type RoomSummary = {
    id: string;
    ownerId: string;
    title: string;
    revision: number;
};

export type RoomGraphNode = {
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

export type RoomGraphEdge = {
    id: string;
    fromNodeId: string;
    toNodeId: string;
    version: number;
};

export type RoomGraph = {
    roomId: string;
    revision: number;
    nodes: RoomGraphNode[];
    edges: RoomGraphEdge[];
};

export type OperationResult = {
    status: number;
    body: { code?: number; data?: { error?: string; details?: Record<string, unknown> } & Record<string, unknown>; msg?: string };
};

export type CollabRole = "editor" | "viewer";

function authHeaders(token: string) {
    return { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
}

function randomSuffix() {
    return `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;
}

export function newOperationId() {
    return `op_${randomSuffix()}`;
}

export function newNodeId() {
    return `node_${randomSuffix()}`;
}

/** 公共图节点种子：8 类节点共用结构，metadata 为进入公共图的 public 字段。 */
export type RoomNodeSeed = {
    id: string;
    kind: string;
    title: string;
    position: { x: number; y: number };
    width: number;
    height: number;
    metadata?: Record<string, unknown>;
};

/** owner 创建共享房间（POST /api/v1/collab/rooms）。 */
export async function createRoom(request: APIRequestContext, owner: TestAccount, title = "E2E 协作房间"): Promise<RoomSummary> {
    const response = await request.post(`${BACKEND_URL}/api/v1/collab/rooms`, {
        headers: authHeaders(owner.token),
        data: { title },
    });
    expect(response.ok(), `创建房间失败：HTTP ${response.status()}`).toBeTruthy();
    const body = await response.json();
    expect(body.code, `创建房间返回非 0：${JSON.stringify(body)}`).toBe(0);
    return body.data as RoomSummary;
}

/** owner 创建邀请令牌（POST /api/v1/collab/rooms/:id/invites），明文令牌只在本次返回。 */
export async function createInvite(request: APIRequestContext, owner: TestAccount, roomId: string, role: CollabRole): Promise<string> {
    const response = await request.post(`${BACKEND_URL}/api/v1/collab/rooms/${encodeURIComponent(roomId)}/invites`, {
        headers: authHeaders(owner.token),
        data: { role, expiresInHours: 1 },
    });
    expect(response.ok(), `创建邀请失败：HTTP ${response.status()}`).toBeTruthy();
    const body = await response.json();
    expect(body.code, `创建邀请返回非 0：${JSON.stringify(body)}`).toBe(0);
    return body.data.token as string;
}

/** 账号使用邀请令牌加入房间（POST /api/v1/collab/invites/accept）。 */
export async function acceptInvite(request: APIRequestContext, account: TestAccount, token: string): Promise<void> {
    const response = await request.post(`${BACKEND_URL}/api/v1/collab/invites/accept`, {
        headers: authHeaders(account.token),
        data: { token },
    });
    expect(response.ok(), `接受邀请失败：HTTP ${response.status()}`).toBeTruthy();
    const body = await response.json();
    expect(body.code, `接受邀请返回非 0：${JSON.stringify(body)}`).toBe(0);
}

/** 让 member 以指定角色加入 owner 的房间（创建邀请 + 接受）。 */
export async function joinRoom(request: APIRequestContext, owner: TestAccount, roomId: string, member: TestAccount, role: CollabRole): Promise<void> {
    const token = await createInvite(request, owner, roomId, role);
    await acceptInvite(request, member, token);
}

/** 读取房间公共图快照（GET /api/v1/collab/rooms/:id/graph）。 */
export async function fetchRoomGraph(request: APIRequestContext, account: TestAccount, roomId: string): Promise<RoomGraph> {
    const response = await request.get(`${BACKEND_URL}/api/v1/collab/rooms/${encodeURIComponent(roomId)}/graph`, {
        headers: authHeaders(account.token),
    });
    expect(response.ok(), `读取房间图失败：HTTP ${response.status()}`).toBeTruthy();
    const body = await response.json();
    expect(body.code, `读取房间图返回非 0：${JSON.stringify(body)}`).toBe(0);
    return body.data as RoomGraph;
}

/** 提交一批协作操作（POST /api/v1/collab/rooms/:id/operations），保留原始状态码与错误码。 */
export async function postOperations(request: APIRequestContext, account: TestAccount, roomId: string, operations: unknown[]): Promise<OperationResult> {
    const response = await request.post(`${BACKEND_URL}/api/v1/collab/rooms/${encodeURIComponent(roomId)}/operations`, {
        headers: authHeaders(account.token),
        data: { protocolVersion: "1", roomId, operationId: newOperationId(), operations },
    });
    return { status: response.status(), body: await response.json() };
}

/** 直接以账号身份更新某个公共图节点（用于构造「并发写入」这一真实竞争场景）。 */
export async function apiUpdateNode(request: APIRequestContext, account: TestAccount, roomId: string, nodeId: string, version: number, fields: Record<string, unknown>): Promise<OperationResult> {
    return postOperations(request, account, roomId, [{ type: "update", id: nodeId, version, fields }]);
}

/** 直接以账号身份在房间内新建一个文本节点（用于非成员负例的房间准备）。 */
export async function apiCreateTextNode(request: APIRequestContext, account: TestAccount, roomId: string, id = newNodeId()): Promise<string> {
    const result = await postOperations(request, account, roomId, [
        {
            type: "create",
            node: {
                id,
                kind: "text",
                title: "文本",
                position: { x: 0, y: 0 },
                width: 360,
                height: 200,
                version: 0,
                metadata: { content: "", fontSize: 14 },
            },
        },
    ]);
    expect(result.status, `API 新建节点失败：HTTP ${result.status} ${JSON.stringify(result.body)}`).toBe(200);
    return id;
}

/** 以账号身份在房间内新建一个任意种类的公共图节点（create 操作，version=0）。 */
export async function apiCreateNode(request: APIRequestContext, account: TestAccount, roomId: string, node: RoomNodeSeed): Promise<OperationResult> {
    return postOperations(request, account, roomId, [{ type: "create", node: { ...node, version: 0 } }]);
}

/** 直接读取房间详情（用于非成员 404 负例），保留状态码。 */
export async function fetchRoomDetail(request: APIRequestContext, account: TestAccount, roomId: string) {
    return request.get(`${BACKEND_URL}/api/v1/collab/rooms/${encodeURIComponent(roomId)}`, {
        headers: authHeaders(account.token),
    });
}

/** 直接申请 WS ticket（用于非成员 404 负例），保留状态码。 */
export async function requestWSTicket(request: APIRequestContext, account: TestAccount, roomId: string) {
    return request.post(`${BACKEND_URL}/api/v1/collab/rooms/${encodeURIComponent(roomId)}/ws-ticket`, {
        headers: authHeaders(account.token),
        data: {},
    });
}
