import { expect, type Page } from "@playwright/test";

// 共享房间画布页（/canvas/rooms/:roomId）的真实浏览器 UI 操作封装。
// 页面实现见 web/src/app/(user)/canvas/rooms/[roomId]/page.tsx。
// 协作交互必须走这里：新建节点、双击编辑、拖动移动都通过真实指针事件驱动。

/** 共享房间画布路径。 */
export function roomPath(roomId: string) {
    return `/canvas/rooms/${encodeURIComponent(roomId)}`;
}

/** 打开共享房间画布页并等待工具栏出现。 */
export async function gotoRoom(page: Page, roomId: string): Promise<void> {
    await page.goto(roomPath(roomId));
    await expect(page.getByRole("button", { name: "返回画布库" })).toBeVisible({ timeout: 30_000 });
}

/** 等待实时连接就绪（状态栏显示「已连接」）。 */
export async function waitRoomConnected(page: Page): Promise<void> {
    await expect(page.getByText("已连接", { exact: true })).toBeVisible({ timeout: 30_000 });
}

/** 等待实时连接断开（状态栏不再显示「已连接」）。 */
export async function waitRoomDisconnected(page: Page): Promise<void> {
    await expect(page.getByText("已连接", { exact: true })).toHaveCount(0, { timeout: 30_000 });
}

/** 房间内节点 id 列表（CanvasNode 渲染带 data-node-id）。 */
export function roomNodeIds(page: Page): Promise<string[]> {
    return page.locator("[data-node-id]").evaluateAll((elements) => elements.map((element) => element.getAttribute("data-node-id") || ""));
}

/** 房间内节点数量。 */
export function roomNodeCount(page: Page): Promise<number> {
    return page.locator("[data-node-id]").count();
}

/** 某个节点当前的画布位移（style.transform），用于断言远端移动同步。 */
export function nodeTransform(page: Page, nodeId: string): Promise<string> {
    return page.locator(`[data-node-id="${nodeId}"]`).evaluate((element) => (element as HTMLElement).style.transform);
}

/** 通过真实 UI 点击「新建文本节点」，返回新建节点 id。 */
export async function createTextNode(page: Page): Promise<string> {
    const before = await roomNodeIds(page);
    await page.getByRole("button", { name: "新建文本节点" }).click();
    await expect.poll(() => roomNodeIds(page), { timeout: 15_000 }).toHaveLength(before.length + 1);
    const created = (await roomNodeIds(page)).find((id) => !before.includes(id));
    if (!created) throw new Error("未能在 UI 中识别新建节点 id");
    return created;
}

/** 双击文本节点进入编辑态，并把内容替换为 text（触发一次协作 update 操作）。 */
export async function editTextNodeContent(page: Page, nodeId: string, text: string): Promise<void> {
    const node = page.locator(`[data-node-id="${nodeId}"]`);
    await node.dblclick();
    const textarea = node.locator("textarea");
    await expect(textarea).toBeVisible({ timeout: 10_000 });
    await textarea.fill(text);
}

/** 退出节点编辑态（Escape，不会触发全局删除）。 */
export async function stopEditingNode(page: Page): Promise<void> {
    await page.keyboard.press("Escape");
}

/** 房间内连线数量（ConnectionPath 渲染带 data-connection-id）。 */
export function roomConnectionCount(page: Page): Promise<number> {
    return page.locator("[data-connection-id]").count();
}

/** 通过真实 UI 选中某个节点（点击节点主体，不拖动）。 */
export async function selectRoomNode(page: Page, nodeId: string): Promise<void> {
    await page.locator(`[data-node-id="${nodeId}"]`).click();
}

/**
 * 通过真实 UI 连接两个节点：先点击起点节点选中（露出连线手柄），
 * 再点击右侧 source 手柄进入连线态，最后点击终点节点完成连接。
 * 手柄为节点根元素下的 `div.cursor-crosshair`（左 target / 右 source）。
 */
export async function connectRoomNodes(page: Page, fromNodeId: string, toNodeId: string): Promise<void> {
    await selectRoomNode(page, fromNodeId);
    const handles = page.locator(`[data-node-id="${fromNodeId}"] div.cursor-crosshair`);
    await handles.nth(1).click();
    await page.locator(`[data-node-id="${toNodeId}"]`).click();
}

/** 选中节点后按 Delete 键删除（页面监听 window keydown）。 */
export async function deleteRoomNodeByKeyboard(page: Page, nodeId: string): Promise<void> {
    await selectRoomNode(page, nodeId);
    await page.keyboard.press("Delete");
}

/** 以真实指针拖拽移动节点（页面监听 window pointermove/pointerup）。 */
export async function dragNode(page: Page, nodeId: string, dx: number, dy: number): Promise<void> {
    const box = await page.locator(`[data-node-id="${nodeId}"]`).boundingBox();
    if (!box) throw new Error(`找不到节点 ${nodeId} 的包围盒`);
    const startX = box.x + box.width / 2;
    const startY = box.y + box.height / 2;
    await page.mouse.move(startX, startY);
    await page.mouse.down();
    await page.mouse.move(startX + dx / 2, startY + dy / 2, { steps: 4 });
    await page.mouse.move(startX + dx, startY + dy, { steps: 4 });
    await page.mouse.up();
}
