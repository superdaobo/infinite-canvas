import { nanoid } from "nanoid";

import type { CollabEdgePayload, CollabNodePayload, CollabOperation, CollabOperationEnvelope } from "@/services/api/collab-operations";
import { COLLAB_PROTOCOL_VERSION } from "@/services/api/collab-operations";
import type { CanvasConnection, CanvasNodeData, CanvasNodeMetadata } from "../types";

// 把前端画布变更构造为协作 v1 操作（docs/collab-v1/contract.md 第 4 节）。
// 纯函数，不触碰 React/DOM：create/update/delete/connect/disconnect 五种操作，
// 统一生成 operationId 幂等键，update/delete/disconnect 携带 expectedNodeVersion。
// 公共图只承载 public 字段：local/private metadata 与凭据类键在构造阶段剥离。

// 与 internal/collab/contract/contract.go 的 privateMetadataFields 对齐。
const PRIVATE_METADATA_KEYS = new Set([
    "composerContent",
    "errorDetails",
    "clientTaskId",
    "imageTaskId",
    "imageTaskResultId",
    "audioTaskId",
    "audioTaskResultId",
    "videoTaskId",
    "videoTaskVideoId",
    "videoTaskTranslation",
]);

// 与 contract.go 的 localMetadataFields 对齐。
const LOCAL_METADATA_KEYS = new Set(["status", "progress", "startedAt"]);

// 与 contract.go 的 forbiddenKeySubstrings 对齐：命中即视为凭据，绝不进入公共图。
const FORBIDDEN_KEY_SUBSTRINGS = ["apikey", "authorization", "password", "credential", "secret", "cookie", "sessionid", "privatekey", "masterkey"];

function normalizeKey(name: string) {
    return name.toLowerCase().replace(/[^a-z0-9]/g, "");
}

// 镜像 contract.IsForbiddenBroadcastKey：归一化精确匹配敏感片段，或以 token 结尾。
export function isForbiddenBroadcastKey(name: string) {
    const key = normalizeKey(name);
    if (key.endsWith("token")) return true;
    return FORBIDDEN_KEY_SUBSTRINGS.some((fragment) => key.includes(fragment));
}

// 只保留可进入公共图的 public metadata；local/private 与凭据类键全部剥离。
export function toPublicMetadata(metadata?: CanvasNodeMetadata): Record<string, unknown> | undefined {
    if (!metadata) return undefined;
    const result: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(metadata)) {
        if (value === undefined) continue;
        if (LOCAL_METADATA_KEYS.has(key) || PRIVATE_METADATA_KEYS.has(key)) continue;
        if (isForbiddenBroadcastKey(key)) continue;
        result[key] = value;
    }
    return Object.keys(result).length ? result : undefined;
}

// 幂等键：op_ 前缀 + nanoid，满足契约 ValidID（4..64，[A-Za-z0-9_-]）。
export function newOperationId() {
    return `op_${nanoid(16)}`;
}

// 共享模式本地新节点/连线 ID，与契约 ValidID 兼容。
export function newNodeId() {
    return `node_${nanoid(16)}`;
}

export function newEdgeId() {
    return `edge_${nanoid(16)}`;
}

export function buildCreateOperation(node: CanvasNodeData): CollabOperation {
    const payload: CollabNodePayload = {
        id: node.id,
        kind: node.type,
        title: node.title,
        position: { x: node.position.x, y: node.position.y },
        width: node.width,
        height: node.height,
        version: 0,
    };
    const metadata = toPublicMetadata(node.metadata);
    if (metadata) payload.metadata = metadata;
    return { type: "create", node: payload };
}

// update 发送完整 public 字段快照；服务端 metadata 为浅合并，不会删除未提交的键。
export function buildUpdateOperation(node: CanvasNodeData, expectedNodeVersion: number): CollabOperation {
    const fields: Record<string, unknown> = {
        title: node.title,
        position: { x: node.position.x, y: node.position.y },
        width: node.width,
        height: node.height,
    };
    const metadata = toPublicMetadata(node.metadata);
    if (metadata) fields.metadata = metadata;
    return { type: "update", id: node.id, version: expectedNodeVersion, fields };
}

export function buildDeleteOperation(id: string, expectedNodeVersion: number): CollabOperation {
    return { type: "delete", id, version: expectedNodeVersion };
}

export function buildConnectOperation(connection: CanvasConnection): CollabOperation {
    const edge: CollabEdgePayload = {
        id: connection.id,
        fromNodeId: connection.fromNodeId,
        toNodeId: connection.toNodeId,
        version: 0,
    };
    return { type: "connect", edge };
}

export function buildDisconnectOperation(id: string, expectedEdgeVersion: number): CollabOperation {
    return { type: "disconnect", id, version: expectedEdgeVersion };
}

export function buildOperationEnvelope(roomId: string, operationId: string, operations: CollabOperation[]): CollabOperationEnvelope {
    return { protocolVersion: COLLAB_PROTOCOL_VERSION, roomId, operationId, operations };
}
