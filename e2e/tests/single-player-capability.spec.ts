import { expect, test } from "@playwright/test";

import { registerAccount, seedSession } from "../fixtures/accounts";
import {
    addCanvasNodeByLabel,
    canvasConnectionCount,
    canvasNodeCount,
    canvasToolbarButton,
    connectCanvasNodes,
    createCanvas,
    dragCanvasNode,
    editCanvasTextNode,
    gotoCanvasLibrary,
} from "../fixtures/canvas";

/**
 * 单人模式能力保留回归（issue #24 / C02-06）。
 *
 * 协作能力为新增路径，单人本地画布（/canvas/<id>）必须保持原行为。
 * 覆盖：8 类节点创建（含 Ctrl+G 建组）、文本编辑、连线、撤销，以及 Agent 与导演台入口可用。
 * 边界：Agent 内部推理与真实模型调用不在本用例覆盖范围，只断言入口存在、可打开且不报运行时错误。
 *
 * 说明：单人画布新增节点落在视口中心附近（带随机偏移）会互相重叠，本用例在创建后
 * 用真实指针拖拽把文本节点移开，保证编辑/连线命中预期节点。
 */

const REMAINING_KIND_LABELS = ["视频", "音频", "全景图", "导演台", "生成配置"];
const TOTAL_KINDS = 7;

test.describe("单人模式能力保留（#24 / C02-06）", () => {
    test("单人画布建节点、编辑、连线、撤销与 Agent/导演台入口保持可用", async ({ browser, request }) => {
        const account = await registerAccount(request);
        const context = await browser.newContext();
        await seedSession(context, account);
        const page = await context.newPage();
        const pageErrors: string[] = [];
        page.on("pageerror", (error) => pageErrors.push(error.message));

        await gotoCanvasLibrary(page);
        await createCanvas(page);

        // 1) 文本节点：创建后拖离视口中心，避免与后续节点重叠。
        const textId = await addCanvasNodeByLabel(page, "文本");
        await dragCanvasNode(page, textId, -460, -140);

        // 编辑：双击文本节点写入内容。
        await editCanvasTextNode(page, textId, "单人编辑回归内容");
        await expect(page.locator(`[data-node-id="${textId}"]`).getByText("单人编辑回归内容")).toBeVisible({ timeout: 10_000 });

        // 2) 图片节点 + 连线：文本 -> 图片（真实指针拖拽手柄）。
        const imageId = await addCanvasNodeByLabel(page, "图片");
        await connectCanvasNodes(page, textId, imageId);
        await expect.poll(() => canvasConnectionCount(page), { timeout: 15_000 }).toBe(1);

        // 撤销：撤销刚才的连线（历史快照含连线）。
        await page.waitForTimeout(500);
        await expect(canvasToolbarButton(page, "撤销")).toBeEnabled();
        await canvasToolbarButton(page, "撤销").click();
        await expect.poll(() => canvasConnectionCount(page), { timeout: 10_000 }).toBe(0);

        // 3) 其余 5 类工具栏节点。
        for (const label of REMAINING_KIND_LABELS) {
            await addCanvasNodeByLabel(page, label);
        }
        expect(await canvasNodeCount(page)).toBe(TOTAL_KINDS);

        // 4) 第 8 类：分组节点经全选 + Ctrl+G 创建（沿用原版交互）。
        await page.keyboard.press("Control+a");
        await page.waitForTimeout(150);
        await page.keyboard.press("Control+g");
        await expect.poll(() => canvasNodeCount(page), { timeout: 10_000 }).toBe(TOTAL_KINDS + 1);

        // 导演台节点真实渲染（第 3 步已通过工具栏创建）。
        await expect(page.locator('[data-node-id^="director-"]').first()).toBeVisible();

        // 5) Agent 入口：顶栏 Agent 按钮可打开创作 Agent 面板。
        const agentButton = page.getByRole("button", { name: "Agent" }).first();
        await expect(agentButton).toBeVisible({ timeout: 15_000 });
        await agentButton.click();
        await expect(page.locator("[data-canvas-agent-panel]")).toBeVisible({ timeout: 15_000 });
        await expect(page.getByText(/创作 Agent|Codex/).first()).toBeVisible();

        expect(pageErrors, `页面运行时错误：${pageErrors.join(" | ")}`).toEqual([]);

        await context.close();
    });
});
