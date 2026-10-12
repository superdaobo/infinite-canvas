import { expect, test } from "@playwright/test";

import { registerAccount, seedSession } from "../fixtures/accounts";
import { gotoRoom, roomNodeIds, waitRoomConnected } from "../fixtures/collab-room";
import { BACKEND_URL } from "../fixtures/env";
import { apiCreateTextNode, createRoom, fetchRoomDetail, fetchRoomGraph, joinRoom, postOperations, requestWSTicket } from "../fixtures/rooms";

/**
 * 权限负例 E2E（issue #29 / C05-03，契约见 docs/collab-v1/security-boundaries.md 第 6 节）。
 *
 * - N4：viewer 只读，写操作被拒 403/forbidden，且不落库。
 * - N6：非成员无法访问房间（详情 / 公共图 / WS ticket / 浏览器页面）。
 * 房间与邀请用 API 准备，只读 UI 与拒绝行为用真实浏览器 + 真实接口验证。
 */

test.describe("权限：viewer 只读与未授权负例", () => {
    test("viewer 打开共享画布只读，UI 无写入口且写接口返回 403/forbidden", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const viewer = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 只读房间");
        await joinRoom(request, owner, room.id, viewer, "viewer");

        // owner 预置一个节点，验证 viewer 能读。
        await apiCreateTextNode(request, owner, room.id);

        const viewerContext = await browser.newContext();
        await seedSession(viewerContext, viewer);
        const viewerPage = await viewerContext.newPage();
        await gotoRoom(viewerPage, room.id);
        await waitRoomConnected(viewerPage);

        // UI 只读：显示「只读」，且没有「新建文本节点」写入口。
        await expect(viewerPage.getByText("只读", { exact: true })).toBeVisible();
        await expect(viewerPage.getByRole("button", { name: "新建文本节点" })).toHaveCount(0);
        // 能读到 owner 的公共图节点。
        await expect.poll(() => roomNodeIds(viewerPage), { timeout: 15_000 }).toHaveLength(1);

        // 绕过 UI 直接写：服务端拒绝 403，错误码 forbidden。
        const denied = await postOperations(request, viewer, room.id, [
            {
                type: "create",
                node: {
                    id: `node_${Date.now().toString(36)}viewer`,
                    kind: "text",
                    title: "viewer 不该创建成功",
                    position: { x: 0, y: 0 },
                    width: 360,
                    height: 200,
                    version: 0,
                    metadata: { content: "", fontSize: 14 },
                },
            },
        ]);
        expect(denied.status).toBe(403);
        expect(denied.body.data?.error).toBe("forbidden");

        // 拒绝后不落库：房间仍只有 owner 预置的那 1 个节点。
        const graph = await fetchRoomGraph(request, owner, room.id);
        expect(graph.nodes).toHaveLength(1);

        await viewerContext.close();
    });

    test("非成员账号无法访问他人房间（详情/图/ticket/页面均拒绝）", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const outsider = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 私有房间");
        const seededNodeId = await apiCreateTextNode(request, owner, room.id);

        // REST 负例：非成员读取房间详情 / 公共图 / 申请 WS ticket 统一被拒（不泄漏房间存在性）。
        const detail = await fetchRoomDetail(request, outsider, room.id);
        expect(detail.status(), `非成员读取房间详情应被拒：${detail.status()}`).toBe(404);

        const graph = await request.get(`${BACKEND_URL}/api/v1/collab/rooms/${encodeURIComponent(room.id)}/graph`, {
            headers: { Authorization: `Bearer ${outsider.token}` },
        });
        expect(graph.status()).toBe(404);

        const ticket = await requestWSTicket(request, outsider, room.id);
        expect(ticket.status()).toBe(404);

        // 浏览器负例：非成员直接访问共享房间 URL，页面不渲染房间内容。
        const outsiderContext = await browser.newContext();
        await seedSession(outsiderContext, outsider);
        const outsiderPage = await outsiderContext.newPage();
        await gotoRoom(outsiderPage, room.id);

        await expect(outsiderPage.getByText("房间不存在")).toBeVisible({ timeout: 15_000 });
        expect(await roomNodeIds(outsiderPage)).toHaveLength(0);
        await expect(outsiderPage.locator(`[data-node-id="${seededNodeId}"]`)).toHaveCount(0);

        await outsiderContext.close();
    });
});
