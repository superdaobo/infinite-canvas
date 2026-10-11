import { expect, test } from "@playwright/test";
import { registerAccount, seedSession } from "../fixtures/accounts";
import { addTextNode, canvasNodeCount, createCanvas, gotoCanvasLibrary } from "../fixtures/canvas";

/**
 * 协作实时同步骨架用例。
 *
 * 这些能力依赖尚未交付的子任务（C01 后端房间/WS、C02 画布协作映射）。
 * 当前用 test.fixme 占位：功能落地后去掉 fixme 即可作为验收用例运行。
 * 用例主体已按「两个独立 browser context = 两个账号」的既有夹具写好。
 */

test.describe("协作：无刷新同编与冲突", () => {
    test.fixme("两个账号在同一共享画布无刷新实时同编", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const editor = await registerAccount(request);

        const ownerContext = await browser.newContext();
        await seedSession(ownerContext, owner);
        const ownerPage = await ownerContext.newPage();
        await gotoCanvasLibrary(ownerPage);
        await createCanvas(ownerPage);
        await addTextNode(ownerPage);
        expect(await canvasNodeCount(ownerPage)).toBe(1);

        // TODO(C02-03/C01-05)：owner 分享画布给 editor 后，editor 在另一 context 打开同一画布，
        // owner 继续新增节点，editor 端应无需刷新即出现同一节点。
        const editorContext = await browser.newContext();
        await seedSession(editorContext, editor);
        const editorPage = await editorContext.newPage();
        await gotoCanvasLibrary(editorPage);
        await addTextNode(editorPage);
        await expect(editorPage.locator("[data-node-id]")).toHaveCount(2);

        await ownerContext.close();
        await editorContext.close();
    });

    test.fixme("独立节点的并发编辑不相互覆盖（409 冲突处理）", async ({ browser, request }) => {
        const accountA = await registerAccount(request);
        const accountB = await registerAccount(request);

        const contextA = await browser.newContext();
        const contextB = await browser.newContext();
        await seedSession(contextA, accountA);
        await seedSession(contextB, accountB);
        const pageA = await contextA.newPage();
        const pageB = await contextB.newPage();
        await gotoCanvasLibrary(pageA);
        await createCanvas(pageA);
        await addTextNode(pageA);

        // TODO(C01-03/C02-04)：两端基于同一版本编辑不同节点，服务端应返回 409 并保留双方草稿，
        // 冲突节点不覆盖；UI 应提示冲突而不是静默丢失。
        await expect(pageB.getByText("存在冲突")).toBeVisible();

        await contextA.close();
        await contextB.close();
    });

    test.fixme("WebSocket 断线后重连并恢复协作状态", async ({ browser, request }) => {
        const account = await registerAccount(request);
        const context = await browser.newContext();
        await seedSession(context, account);
        const page = await context.newPage();
        await gotoCanvasLibrary(page);
        await createCanvas(page);

        // TODO(C02-03)：模拟网络断开，重新连接后应回放缺失变更、恢复在线状态，
        // 未确认的本地草稿需要保留。
        await context.setOffline(true);
        await context.setOffline(false);
        await expect(page.getByText("已连接")).toBeVisible();

        await context.close();
    });
});
