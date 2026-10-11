import { expect, test } from "@playwright/test";
import { expectAccountName, expectLoggedIn, registerAccount, seedSession } from "../fixtures/accounts";
import { addTextNode, canvasNodeCount, createCanvas, gotoCanvasLibrary } from "../fixtures/canvas";

/**
 * 基线用例：当前源码即可跑通。
 * 覆盖 issue #4 验收中「双账号打开画布页面、页面正常加载、单账号基础操作」以及
 * 「跨账号隔离」的当前可验证部分（协作实时同步尚未实现，见 collaboration.spec.ts）。
 */
test.describe("双浏览器协作测试基线", () => {
    test("两个独立 browser context 可分别以不同账号打开画布库", async ({ browser, request }) => {
        const alice = await registerAccount(request);
        const bob = await registerAccount(request);
        expect(alice.userId).not.toBe(bob.userId);

        const aliceContext = await browser.newContext();
        const bobContext = await browser.newContext();
        await seedSession(aliceContext, alice);
        await seedSession(bobContext, bob);

        const alicePage = await aliceContext.newPage();
        await gotoCanvasLibrary(alicePage);
        await expectLoggedIn(alicePage);
        await expectAccountName(alicePage, alice.username);

        const bobPage = await bobContext.newPage();
        await gotoCanvasLibrary(bobPage);
        await expectLoggedIn(bobPage);
        await expectAccountName(bobPage, bob.username);

        await aliceContext.close();
        await bobContext.close();
    });

    test("单账号可在画布库新建画布并完成基础节点操作", async ({ browser, request }) => {
        const account = await registerAccount(request);
        const context = await browser.newContext();
        await seedSession(context, account);
        const page = await context.newPage();

        await gotoCanvasLibrary(page);
        await createCanvas(page);
        await addTextNode(page);
        expect(await canvasNodeCount(page)).toBe(1);

        await context.close();
    });

    test("两个账号的画布库相互独立（本地存储隔离）", async ({ browser, request }) => {
        const alice = await registerAccount(request);
        const bob = await registerAccount(request);

        const aliceContext = await browser.newContext();
        await seedSession(aliceContext, alice);
        const alicePage = await aliceContext.newPage();
        await gotoCanvasLibrary(alicePage);
        await createCanvas(alicePage);

        const bobContext = await browser.newContext();
        await seedSession(bobContext, bob);
        const bobPage = await bobContext.newPage();
        await gotoCanvasLibrary(bobPage);
        await expect(bobPage.getByRole("heading", { name: "还没有画布" })).toBeVisible();
        expect(await canvasNodeCount(bobPage)).toBe(0);

        await aliceContext.close();
        await bobContext.close();
    });
});
