import { expect, type Page } from "@playwright/test";

/** 画布库（项目列表）页面路径，见 web/src/app/(user)/canvas/page.tsx。 */
export const CANVAS_LIBRARY_PATH = "/canvas";

/** 画布详情页 URL 形态：/canvas/<projectId>。 */
export const CANVAS_DETAIL_URL = /\/canvas\/[^/?#]+$/;

/** 打开画布库并等待本地 store 水合完成（加载占位消失）。 */
export async function gotoCanvasLibrary(page: Page): Promise<void> {
    await page.goto(CANVAS_LIBRARY_PATH);
    await expect(page.getByRole("heading", { name: "无限画布" })).toBeVisible();
    await expect(page.getByText("正在加载画布...")).toHaveCount(0);
}

/** 画布库中可见的项目卡片数量。 */
export function projectCardCount(page: Page): Promise<number> {
    // 项目卡片为链接/可点击卡片，统一使用“打开/进入”语义不稳定，这里用导出按钮作为卡片锚点。
    return page.locator('[aria-label="导出"]').count();
}

/** 新建一个画布并进入详情页，返回新建项目 id。 */
export async function createCanvas(page: Page): Promise<string> {
    await page.getByRole("button", { name: "新建画布" }).first().click();
    await page.waitForURL(CANVAS_DETAIL_URL);
    await expect(canvasToolbarButton(page, "选择")).toBeVisible();
    const match = page.url().match(/\/canvas\/([^/?#]+)/);
    if (!match) throw new Error(`无法从 URL 解析画布 id：${page.url()}`);
    return match[1];
}

/** 工具栏按钮（画布底部 Dock，按钮带 aria-label）。 */
export function canvasToolbarButton(page: Page, label: string) {
    return page.locator(`button[aria-label="${label}"]`).first();
}

/** 画布上的节点数量，节点渲染时带 data-node-id 属性。 */
export function canvasNodeCount(page: Page): Promise<number> {
    return page.locator("[data-node-id]").count();
}

/** 基础操作：点击工具栏“文本”新增一个文本节点，并断言节点数与撤销可用性。 */
export async function addTextNode(page: Page): Promise<void> {
    const before = await canvasNodeCount(page);
    await canvasToolbarButton(page, "文本").click();
    await expect(page.locator("[data-node-id]")).toHaveCount(before + 1);
    await expect(canvasToolbarButton(page, "撤销")).toBeEnabled();
}
