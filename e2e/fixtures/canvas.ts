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

/** 画布上全部节点 id（按 DOM 顺序）。 */
export function canvasNodeIds(page: Page): Promise<string[]> {
    return page.locator("[data-node-id]").evaluateAll((elements) => elements.map((element) => element.getAttribute("data-node-id") || ""));
}

/** 点击工具栏按钮新增指定类型节点，返回新节点 id；随后按 Escape 关闭可能弹出的节点面板。 */
export async function addCanvasNodeByLabel(page: Page, label: string): Promise<string> {
    const before = await canvasNodeIds(page);
    await canvasToolbarButton(page, label).click();
    await expect.poll(() => canvasNodeIds(page), { timeout: 10_000 }).toHaveLength(before.length + 1);
    const created = (await canvasNodeIds(page)).find((id) => !before.includes(id));
    if (!created) throw new Error(`未能识别「${label}」新建节点 id`);
    await page.keyboard.press("Escape");
    return created;
}

/** 双击文本节点进入编辑态并替换内容。 */
export async function editCanvasTextNode(page: Page, nodeId: string, text: string): Promise<void> {
    const node = page.locator(`[data-node-id="${nodeId}"]`);
    await node.dblclick();
    const textarea = node.locator("textarea");
    await expect(textarea).toBeVisible({ timeout: 10_000 });
    await textarea.fill(text);
    await page.keyboard.press("Escape");
}

/** 单人画布上的连线数量（ConnectionPath 渲染带 data-connection-id）。 */
export function canvasConnectionCount(page: Page): Promise<number> {
    return page.locator("[data-connection-id]").count();
}

/** 以真实指针拖拽移动单人画布节点（页面监听 window mousemove/mouseup）。 */
export async function dragCanvasNode(page: Page, nodeId: string, dx: number, dy: number): Promise<void> {
    const box = await page.locator(`[data-node-id="${nodeId}"]`).boundingBox();
    if (!box) throw new Error(`找不到节点 ${nodeId} 的包围盒`);
    const startX = box.x + box.width / 2;
    const startY = box.y + box.height / 2;
    await page.mouse.move(startX, startY);
    await page.mouse.down();
    await page.mouse.move(startX + dx / 2, startY + dy / 2, { steps: 5 });
    await page.mouse.move(startX + dx, startY + dy, { steps: 5 });
    await page.mouse.up();
}

/**
 * 单人模式连接两个节点：先点击起点节点选中（露出连线手柄），
 * 再按住右侧 source 手柄拖到终点节点释放（单人模式在 window mouseup 上解析落点）。
 */
export async function connectCanvasNodes(page: Page, fromNodeId: string, toNodeId: string): Promise<void> {
    await page.locator(`[data-node-id="${fromNodeId}"]`).click();
    const handle = page.locator(`[data-node-id="${fromNodeId}"] div.cursor-crosshair`).nth(1);
    const handleBox = await handle.boundingBox();
    const targetBox = await page.locator(`[data-node-id="${toNodeId}"]`).boundingBox();
    if (!handleBox || !targetBox) throw new Error("连接手柄或目标节点不可见");
    await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
    await page.mouse.down();
    await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 8 });
    await page.mouse.up();
}
