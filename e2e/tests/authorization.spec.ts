import { expect, test } from "@playwright/test";
import { registerAccount, seedSession } from "../fixtures/accounts";
import { gotoCanvasLibrary } from "../fixtures/canvas";

/**
 * 权限负例骨架用例。
 * 依赖 C01-02（owner/editor/viewer 与可撤销邀请）、C01-05（带权限的 WS）。
 * 当前用 test.fixme 占位。
 */

test.describe("权限：viewer 与未授权负例", () => {
    test.fixme("viewer 打开共享画布只读，不能新增节点", async ({ browser, request }) => {
        const owner = await registerAccount(request);
        const viewer = await registerAccount(request);

        const viewerContext = await browser.newContext();
        await seedSession(viewerContext, viewer);
        const viewerPage = await viewerContext.newPage();
        await gotoCanvasLibrary(viewerPage);

        // TODO(C01-02/C02-05)：owner 邀请 viewer 后，viewer 进入画布应隐藏/禁用编辑工具栏，
        // 直接调用写接口应返回 403，且不产生任何节点变更。
        await expect(viewerPage.getByRole("button", { name: "文本" })).toBeDisabled();
        await expect(viewerPage.getByText("只读")).toBeVisible();

        await viewerContext.close();
    });

    test.fixme("非成员账号访问他人画布返回 403", async ({ browser, request }) => {
        const outsider = await registerAccount(request);
        const outsiderContext = await browser.newContext();
        await seedSession(outsiderContext, outsider);
        const outsiderPage = await outsiderContext.newPage();

        // TODO(C01-02)：非成员直接访问 /canvas/<sharedId> 应被拒绝（403 页面或跳转），
        // 不得读取到画布内容。
        const response = await outsiderPage.goto("/canvas/unknown-shared-room");
        expect(response?.status()).toBe(403);

        await outsiderContext.close();
    });
});
