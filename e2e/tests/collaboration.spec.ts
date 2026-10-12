import { expect, test, type BrowserContext, type Page } from "@playwright/test";

import { registerAccount, seedSession } from "../fixtures/accounts";
import {
    createTextNode,
    dragNode,
    editTextNodeContent,
    gotoRoom,
    nodeTransform,
    roomNodeIds,
    stopEditingNode,
    waitRoomConnected,
    waitRoomDisconnected,
} from "../fixtures/collab-room";
import { apiUpdateNode, createRoom, fetchRoomGraph, joinRoom } from "../fixtures/rooms";

/**
 * 真实双账号实时协作 E2E（issue #29 / C05-03）。
 *
 * 两个相互独立的 browser context = 两个不同账号，房间与邀请用后端 API 准备，
 * 协作交互全部走真实浏览器 UI。WebSocket 直连 Go 后端（Next 的 /api 代理无法
 * 代理 WS 升级），由 playwright.config.ts 注入 NEXT_PUBLIC_COLLAB_WS_BASE。
 */

test.describe("协作：双账号实时同编与冲突", () => {
    test("账号 A 新增/移动/编辑节点，账号 B 无需刷新即可看到", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editor = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 实时同编房间");
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

        // 新增：owner 通过 UI 新建文本节点，editor 端不经刷新即出现同一节点。
        const nodeId = await createTextNode(ownerPage);
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).toContain(nodeId);

        // 编辑：owner 双击节点写入文字，editor 端不经刷新即看到相同内容。
        await editTextNodeContent(ownerPage, nodeId, "hello-from-owner");
        await stopEditingNode(ownerPage);
        await expect(editorPage.getByText("hello-from-owner")).toBeVisible({ timeout: 15_000 });

        // 移动：owner 拖动节点，editor 端不经刷新即看到位置变化。
        const beforeTransform = await nodeTransform(editorPage, nodeId);
        await dragNode(ownerPage, nodeId, 180, 140);
        await expect.poll(() => nodeTransform(editorPage, nodeId), { timeout: 15_000 }).not.toBe(beforeTransform);

        await ownerContext.close();
        await editorContext.close();
    });

    test("两账号并发编辑同一节点：后写得到 version_conflict 且未确认草稿不丢", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editor = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 冲突房间");
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

        const nodeId = await createTextNode(ownerPage);
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).toContain(nodeId);

        // editor 基于版本 1 在 UI 中编辑同一节点（本地乐观显示，等待服务端确认）。
        await editTextNodeContent(editorPage, nodeId, "editor-draft");

        // owner 并发写入同一节点（真实账号 + 真实接口），服务端版本推进到 2。
        // 这是真实并发竞争的确定性构造：editor 的待确认写入仍基于版本 1。
        const concurrent = await apiUpdateNode(request, owner, room.id, nodeId, 1, { title: "owner-concurrent" });
        expect(concurrent.status, `owner 并发写入应成功：${JSON.stringify(concurrent.body)}`).toBe(200);

        // 后写（editor 的过期版本写入）被拒：UI 暴露冲突提示且草稿保留。
        // #15 起冲突以中文浮层呈现（不再回显 version_conflict 字面量），并给出两种处理方式。
        await expect(editorPage.getByText("保留本地草稿并重试")).toBeVisible({ timeout: 20_000 });
        await expect(editorPage.getByText("你的草稿已保留，不会被静默丢弃。")).toBeVisible();

        // 草稿不丢：退出编辑后本地仍显示 editor 的草稿内容（未被服务端回滚清空）。
        await stopEditingNode(editorPage);
        await expect(editorPage.getByText("editor-draft")).toBeVisible();
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).toContain(nodeId);

        // 服务端最终状态是 owner 的并发写入，editor 的过期写入未覆盖。
        const graph = await fetchRoomGraph(request, owner, room.id);
        const serverNode = graph.nodes.find((node) => node.id === nodeId);
        expect(serverNode?.title).toBe("owner-concurrent");
        expect(serverNode?.version).toBe(2);

        await ownerContext.close();
        await editorContext.close();
    });

    test("断线期间他人修改，重连后追平到最新 revision", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editor = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 断线恢复房间");
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

        const firstNode = await createTextNode(ownerPage);
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 15_000 }).toContain(firstNode);

        // editor 断网：WS 断开，状态栏不再是「已连接」。
        await editorContext.setOffline(true);
        await waitRoomDisconnected(editorPage);

        // 断线期间 owner 继续新增节点。
        const offlineNode = await createTextNode(ownerPage);
        await expect.poll(() => roomNodeIds(ownerPage), { timeout: 15_000 }).toContain(offlineNode);

        // editor 恢复网络：自动重连并按 revision 全量重建快照，追平最新图。
        await editorContext.setOffline(false);
        await waitRoomConnected(editorPage);
        await expect.poll(() => roomNodeIds(editorPage), { timeout: 20_000 }).toContain(offlineNode);
        expect(await roomNodeIds(editorPage)).toHaveLength(2);

        // 两端 revision 一致（工具栏 r{revision}）。
        const ownerRevision = await ownerPage.getByText(/^r\d+$/).first().innerText();
        const editorRevision = await editorPage.getByText(/^r\d+$/).first().innerText();
        expect(editorRevision).toBe(ownerRevision);

        await ownerContext.close();
        await editorContext.close();
    });

    test("≥3 个账号并发新增节点，最终收敛一致且无重复", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editorA = await registerAccount(request);
        const editorB = await registerAccount(request);
        const room = await createRoom(request, owner, "E2E 并发收敛房间");
        await joinRoom(request, owner, room.id, editorA, "editor");
        await joinRoom(request, owner, room.id, editorB, "editor");

        const accounts = [owner, editorA, editorB];
        const contexts: BrowserContext[] = [];
        const pages: Page[] = [];
        for (const account of accounts) {
            const context = await browser.newContext();
            await seedSession(context, account);
            const page = await context.newPage();
            await gotoRoom(page, room.id);
            await waitRoomConnected(page);
            contexts.push(context);
            pages.push(page);
        }

        // 三端同时新增节点（真实 UI 并发操作）。
        await Promise.all(pages.map((page) => createTextNode(page)));

        // 最终收敛：三端都恰好 3 个节点，且节点 id 集合完全一致（无重复节点）。
        await expect
            .poll(
                async () => {
                    const sets = await Promise.all(pages.map((page) => roomNodeIds(page)));
                    return sets.every((ids) => ids.length === 3) && new Set(sets[0]).size === 3;
                },
                { timeout: 30_000 },
            )
            .toBe(true);

        const finalSets = await Promise.all(pages.map((page) => roomNodeIds(page)));
        const reference = [...finalSets[0]].sort();
        for (const ids of finalSets) expect([...ids].sort()).toEqual(reference);
        expect(reference).toHaveLength(3);

        // 服务端公共图同样是 3 个节点。
        const graph = await fetchRoomGraph(request, owner, room.id);
        expect(graph.nodes).toHaveLength(3);

        for (const context of contexts) await context.close();
    });
});
