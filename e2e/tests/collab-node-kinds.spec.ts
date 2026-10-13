import { expect, test } from "@playwright/test";

import { registerAccount, seedSession } from "../fixtures/accounts";
import {
    connectRoomNodes,
    deleteRoomNodeByKeyboard,
    editTextNodeContent,
    gotoRoom,
    roomConnectionCount,
    roomNodeIds,
    stopEditingNode,
    waitRoomConnected,
} from "../fixtures/collab-room";
import { apiCreateNode, createRoom, fetchRoomGraph, joinRoom, newNodeId, type RoomNodeSeed } from "../fixtures/rooms";

/**
 * 8 类节点在共享房间的回归（issue #24 / C02-06）。
 *
 * 共享房间 UI 目前只暴露「新建文本节点」入口，其余 7 类节点由 Agent / 学校回填 / 导入等
 * 路径以协作操作（create）写入公共图。因此本用例对非文本节点使用与那些路径相同的
 * `POST /api/v1/collab/rooms/:id/operations` 写入，再用真实浏览器 UI 验证
 * 「渲染 → 协作可见 → 重载读回持久化」，并对文本节点补一条真实 UI 编辑链路。
 * 契约见 docs/collab-v1/contract.md 第 2、3 节。
 */

type Seed = RoomNodeSeed & { id: string };

function buildSeeds() {
    const imageId = newNodeId();
    const videoId = newNodeId();
    const textId = newNodeId();
    const groupId = newNodeId();
    const configId = newNodeId();
    const directorId = newNodeId();
    const panoramaId = newNodeId();
    const audioId = newNodeId();

    const nodes: Seed[] = [
        {
            id: imageId,
            kind: "image",
            title: "回归-图片节点",
            position: { x: -1400, y: -500 },
            width: 640,
            height: 640,
            metadata: { prompt: "赛博朋克城市夜景，霓虹倒影", model: "seedream-4.0", size: "16:9", quality: "high", count: 1, naturalWidth: 1024, naturalHeight: 1024 },
        },
        {
            id: panoramaId,
            kind: "panorama",
            title: "回归-全景节点",
            position: { x: -600, y: -500 },
            width: 680,
            height: 340,
            metadata: { panoramaSourcePrompt: "雪山日出全景", panoramaFinalPrompt: "雪山日出全景，8k", panoramaProjection: "equirectangular" },
        },
        {
            id: videoId,
            kind: "video",
            title: "回归-视频节点",
            position: { x: 200, y: -500 },
            width: 640,
            height: 360,
            metadata: { prompt: "镜头推进", model: "minimax_h3_image_audio_to_video_v2_15s", seconds: "15", vquality: "768p", references: [imageId] },
        },
        {
            id: audioId,
            kind: "audio",
            title: "回归-音频节点",
            position: { x: 1000, y: -500 },
            width: 480,
            height: 160,
            metadata: { audioVoice: "zh_female_01", audioFormat: "mp3", audioSpeed: "1.0" },
        },
        {
            id: textId,
            kind: "text",
            title: "回归-文本节点",
            position: { x: -1400, y: 300 },
            width: 360,
            height: 200,
            metadata: { content: "共享文本回归内容", fontSize: 14 },
        },
        {
            id: groupId,
            kind: "group",
            title: "回归-分组节点",
            position: { x: -600, y: 300 },
            width: 900,
            height: 700,
            metadata: { groupId },
        },
        {
            id: configId,
            kind: "config",
            title: "回归-配置节点",
            position: { x: 400, y: 300 },
            width: 320,
            height: 240,
            metadata: { content: JSON.stringify({ size: "16:9", quality: "high", count: 1 }) },
        },
        {
            id: directorId,
            kind: "director",
            title: "回归-导演台节点",
            position: { x: 1200, y: 300 },
            width: 960,
            height: 540,
            metadata: { directorProject: { shots: [] }, primaryImageId: imageId, references: [imageId], multiShot: "true" },
        },
    ];

    return { nodes, imageId, videoId, textId, configId, directorId };
}

function parseMetadata(graph: Awaited<ReturnType<typeof fetchRoomGraph>>, nodeId: string): Record<string, unknown> {
    const row = graph.nodes.find((node) => node.id === nodeId);
    if (!row) throw new Error(`读回节点 ${nodeId} 失败`);
    return row.metadata ? (JSON.parse(row.metadata) as Record<string, unknown>) : {};
}

test.describe("协作：8 类节点回归（#24 / C02-06）", () => {
    test("8 类节点创建→渲染→协作可见→重载读回持久化", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editor = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 8 类节点回归房间");
        await joinRoom(request, owner, room.id, editor, "editor");

        const ownerContext = await browser.newContext();
        const editorContext = await browser.newContext();
        await seedSession(ownerContext, owner);
        await seedSession(editorContext, editor);
        const ownerPage = await ownerContext.newPage();
        const editorPage = await editorContext.newPage();
        await gotoRoom(ownerPage, room.id);
        await waitRoomConnected(ownerPage);
        await gotoRoom(editorPage, room.id);
        await waitRoomConnected(editorPage);
        expect(await roomNodeIds(editorPage)).toHaveLength(0);

        const { nodes, imageId, videoId, textId, configId, directorId } = buildSeeds();

        // 创建（协作操作）→ 渲染 → 协作可见：每类节点写入后，创建端与协作端都无需刷新即渲染。
        for (const node of nodes) {
            const result = await apiCreateNode(request, owner, room.id, node);
            expect(result.status, `创建 ${node.kind} 节点失败：HTTP ${result.status} ${JSON.stringify(result.body)}`).toBe(200);
            expect(result.body.code, `创建 ${node.kind} 节点返回非 0：${JSON.stringify(result.body)}`).toBe(0);
            await expect.poll(() => roomNodeIds(ownerPage), { timeout: 15_000 }).toContain(node.id);
            await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).toContain(node.id);
            // 节点标题在两端真实渲染。
            await expect(ownerPage.locator(`[data-node-id="${node.id}"]`).getByText(node.title, { exact: true })).toBeVisible();
            await expect(editorPage.locator(`[data-node-id="${node.id}"]`).getByText(node.title, { exact: true })).toBeVisible();
        }

        // 8 类节点全部渲染、无重复、无丢失。
        expect((await roomNodeIds(ownerPage)).sort()).toEqual(nodes.map((node) => node.id).sort());
        expect((await roomNodeIds(editorPage)).sort()).toEqual(nodes.map((node) => node.id).sort());

        // 文本节点内容在两端真实渲染。
        await expect(editorPage.getByText("共享文本回归内容")).toBeVisible({ timeout: 15_000 });

        // 读回持久化：kind / title / position 与关键非敏感 metadata 均落库。
        const graph = await fetchRoomGraph(request, owner, room.id);
        expect(graph.nodes).toHaveLength(8);
        for (const node of nodes) {
            const stored = graph.nodes.find((item) => item.id === node.id);
            expect(stored, `读回缺失 ${node.kind} 节点`).toBeTruthy();
            expect(stored?.kind).toBe(node.kind);
            expect(stored?.title).toBe(node.title);
            expect(stored?.positionX).toBeCloseTo(node.position.x, 3);
            expect(stored?.positionY).toBeCloseTo(node.position.y, 3);
        }
        expect(parseMetadata(graph, textId).content).toBe("共享文本回归内容");
        expect(JSON.parse(String(parseMetadata(graph, configId).content))).toEqual({ size: "16:9", quality: "high", count: 1 });
        expect((parseMetadata(graph, directorId).directorProject as { shots: unknown[] }).shots).toEqual([]);
        expect(parseMetadata(graph, directorId).primaryImageId).toBe(imageId);
        expect(parseMetadata(graph, videoId).references).toEqual([imageId]);
        expect(parseMetadata(graph, imageId).model).toBe("seedream-4.0");

        // 文本节点真实 UI 编辑：owner 双击改写内容，editor 端无需刷新即看到新内容。
        await editTextNodeContent(ownerPage, textId, "共享文本已编辑");
        await stopEditingNode(ownerPage);
        await expect(editorPage.getByText("共享文本已编辑")).toBeVisible({ timeout: 15_000 });
        await expect
            .poll(async () => parseMetadata(await fetchRoomGraph(request, owner, room.id), textId).content, { timeout: 15_000 })
            .toBe("共享文本已编辑");

        // 重载读回：刷新协作端后 8 类节点仍在（持久化）。
        await editorPage.reload();
        await waitRoomConnected(editorPage);
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 20_000 }).toHaveLength(8);

        await ownerContext.close();
        await editorContext.close();
    });

    test("共享房间连线经 UI 建立并在删除节点后一致清除关联连线", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editor = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 连线与级联删除房间");
        await joinRoom(request, owner, room.id, editor, "editor");

        const ownerContext = await browser.newContext();
        const editorContext = await browser.newContext();
        await seedSession(ownerContext, owner);
        await seedSession(editorContext, editor);
        const ownerPage = await ownerContext.newPage();
        const editorPage = await editorContext.newPage();
        await gotoRoom(ownerPage, room.id);
        await waitRoomConnected(ownerPage);
        await gotoRoom(editorPage, room.id);
        await waitRoomConnected(editorPage);

        const textId = newNodeId();
        const imageId = newNodeId();
        expect((await apiCreateNode(request, owner, room.id, { id: textId, kind: "text", title: "连线源文本", position: { x: -500, y: 0 }, width: 360, height: 200, metadata: { content: "hello" } })).status).toBe(200);
        expect((await apiCreateNode(request, owner, room.id, { id: imageId, kind: "image", title: "连线目标图片", position: { x: 500, y: 0 }, width: 512, height: 512, metadata: {} })).status).toBe(200);
        await expect.poll(() => roomNodeIds(ownerPage), { timeout: 15_000 }).toEqual(expect.arrayContaining([textId, imageId]));
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).toEqual(expect.arrayContaining([textId, imageId]));

        // 通过真实 UI 从文本节点连到图片节点。
        await connectRoomNodes(ownerPage, textId, imageId);
        await expect.poll(() => roomConnectionCount(ownerPage), { timeout: 15_000 }).toBe(1);
        await expect.poll(() => roomConnectionCount(editorPage), { timeout: 15_000 }).toBe(1);

        // 连线持久化：刷新协作端后仍在。
        await editorPage.reload();
        await waitRoomConnected(editorPage);
        await expect.poll(() => roomConnectionCount(editorPage), { timeout: 20_000 }).toBe(1);
        expect((await fetchRoomGraph(request, owner, room.id)).edges).toHaveLength(1);

        // 通过真实 UI 删除图片节点：两端节点与其关联连线一致清除。
        await deleteRoomNodeByKeyboard(ownerPage, imageId);
        await expect.poll(() => roomNodeIds(ownerPage), { timeout: 15_000 }).not.toContain(imageId);
        await expect.poll(() => roomConnectionCount(ownerPage), { timeout: 15_000 }).toBe(0);
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).not.toContain(imageId);
        await expect.poll(() => roomConnectionCount(editorPage), { timeout: 15_000 }).toBe(0);

        // 服务端公共图：仅剩文本节点，连线为空，无悬空边。
        const graph = await fetchRoomGraph(request, owner, room.id);
        expect(graph.nodes.map((node) => node.id)).toEqual([textId]);
        expect(graph.edges).toHaveLength(0);

        await ownerContext.close();
        await editorContext.close();
    });

    test("共享房间节点边界：H3 参考图数量越界与凭据字段被拒，正常渠道节点不受影响", async ({ request }) => {
        const owner = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 节点边界房间");

        const imageId = newNodeId();
        expect((await apiCreateNode(request, owner, room.id, { id: imageId, kind: "image", title: "参考图", position: { x: 0, y: 0 }, width: 512, height: 512, metadata: {} })).status).toBe(200);

        // 边界负例 1：学校 H3 参考图 10 张越界（契约 1..9）。
        const overLimit = await apiCreateNode(request, owner, room.id, {
            id: newNodeId(),
            kind: "video",
            title: "越界参考视频",
            position: { x: 0, y: 0 },
            width: 1280,
            height: 720,
            metadata: { references: Array.from({ length: 10 }, () => newNodeId()) },
        });
        expect(overLimit.status).toBe(400);
        expect(overLimit.body.data?.error).toBe("invalid_payload");

        // 边界负例 2：同一张参考图重复。
        const duplicated = await apiCreateNode(request, owner, room.id, {
            id: newNodeId(),
            kind: "video",
            title: "重复参考视频",
            position: { x: 0, y: 0 },
            width: 1280,
            height: 720,
            metadata: { references: [imageId, imageId] },
        });
        expect(duplicated.status).toBe(400);
        expect(duplicated.body.data?.error).toBe("invalid_payload");

        // 安全负例：凭据类键不得进入公共图。
        const credential = await apiCreateNode(request, owner, room.id, {
            id: newNodeId(),
            kind: "text",
            title: "含密钥文本",
            position: { x: 0, y: 0 },
            width: 360,
            height: 200,
            metadata: { content: "hi", apiKey: "sk-should-never-broadcast" },
        });
        expect(credential.status).toBe(400);
        expect(credential.body.data?.error).toBe("forbidden_field");

        // 负例：config 节点 content 必须是 JSON 对象。
        const badConfig = await apiCreateNode(request, owner, room.id, {
            id: newNodeId(),
            kind: "config",
            title: "非法配置",
            position: { x: 0, y: 0 },
            width: 320,
            height: 240,
            metadata: { content: "not-a-json-object" },
        });
        expect(badConfig.status).toBe(400);
        expect(badConfig.body.data?.error).toBe("invalid_payload");

        // 正例：非学校渠道的普通视频节点（空引用 / 单张引用）不被 H3 约束错误阻止。
        const noRef = await apiCreateNode(request, owner, room.id, { id: newNodeId(), kind: "video", title: "普通视频-无引用", position: { x: 0, y: 0 }, width: 1280, height: 720, metadata: { model: "minimax_hailuo_02" } });
        expect(noRef.status).toBe(200);
        const oneRef = await apiCreateNode(request, owner, room.id, { id: newNodeId(), kind: "video", title: "普通视频-单引用", position: { x: 0, y: 0 }, width: 1280, height: 720, metadata: { references: [imageId] } });
        expect(oneRef.status).toBe(200);

        // 被拒节点未落库，合法节点落库。
        const graph = await fetchRoomGraph(request, owner, room.id);
        expect(graph.nodes).toHaveLength(3);
        expect(graph.nodes.some((node) => node.title === "越界参考视频")).toBe(false);
        expect(graph.nodes.some((node) => node.title === "含密钥文本")).toBe(false);
    });

    /**
     * #65：共享房间 config/director 节点渲染与入口。
     * 回归前二者回落为「空图片节点」，此处验证与单人模式一致的配置面板与导演台入口，
     * 并真实打开导演台（iframe）且不产生协作错误。
     */
    test("共享房间 config/director 节点渲染配置面板与导演台入口（#65）", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E config/director 渲染房间");
        const context = await browser.newContext();
        await seedSession(context, owner);
        const page = await context.newPage();
        const pageErrors: string[] = [];
        page.on("pageerror", (error) => pageErrors.push(error.message));
        await gotoRoom(page, room.id);
        await waitRoomConnected(page);

        const configId = newNodeId();
        const directorId = newNodeId();
        expect(
            (await apiCreateNode(request, owner, room.id, { id: configId, kind: "config", title: "渲染-配置节点", position: { x: -700, y: 0 }, width: 440, height: 240, metadata: { content: JSON.stringify({ size: "16:9", quality: "high", count: 1 }) } })).status,
        ).toBe(200);
        expect((await apiCreateNode(request, owner, room.id, { id: directorId, kind: "director", title: "渲染-导演台节点", position: { x: 400, y: 0 }, width: 960, height: 540, metadata: { directorProject: { shots: [] } } })).status).toBe(200);
        await expect.poll(() => roomNodeIds(page), { timeout: 15_000 }).toEqual(expect.arrayContaining([configId, directorId]));

        // config 节点渲染真实配置面板，而不是空图片节点占位。
        const configNode = page.locator(`[data-node-id="${configId}"]`);
        await expect(configNode.getByText("生成配置", { exact: true })).toBeVisible({ timeout: 15_000 });
        await expect(configNode.getByText("空图片节点")).toHaveCount(0);

        // director 节点渲染导演台入口，点击后真实打开导演台 iframe。
        const directorNode = page.locator(`[data-node-id="${directorId}"]`);
        const openDirector = directorNode.getByRole("button", { name: "打开导演台" });
        await expect(openDirector).toBeVisible({ timeout: 15_000 });
        await openDirector.click();
        await expect(page.locator('iframe[title="3D导演台"]')).toBeVisible({ timeout: 15_000 });

        // 打开导演台不产生协作错误、不产生页面异常。
        await expect(page.getByText(/待确认变更/)).toHaveCount(0);
        expect(pageErrors, `页面出现异常：${pageErrors.join(" | ")}`).toEqual([]);

        await context.close();
    });
});
