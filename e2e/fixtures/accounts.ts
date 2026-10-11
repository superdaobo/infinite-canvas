import { expect, type APIRequestContext, type BrowserContext } from "@playwright/test";
import { BACKEND_URL } from "./env";

/** 前端 user store（zustand persist）使用的 localStorage 键，见 web/src/services/api/auth.ts。 */
export const AUTH_TOKEN_KEY = "infinite-canvas-auth-token-v1";

export type TestAccount = {
    username: string;
    password: string;
    token: string;
    userId: string;
};

let accountSeq = 0;

/**
 * 生成一次运行内唯一、且跨运行不冲突的测试用户名。
 * 数据库在多次运行间复用（data/e2e.db），因此不能使用固定用户名。
 */
export function uniqueUsername(prefix = "e2e"): string {
    accountSeq += 1;
    return `${prefix}_${Date.now().toString(36)}_${accountSeq}`;
}

/**
 * 通过后端 /api/auth/register 注册一个真实测试账号，返回可用于浏览器注入的会话。
 * 不接触任何真实学校 / 生产凭据，纯本地 sqlite。
 */
export async function registerAccount(request: APIRequestContext, username = uniqueUsername()): Promise<TestAccount> {
    const password = "e2e-pass-1234";
    const response = await request.post(`${BACKEND_URL}/api/auth/register`, {
        data: { username, password },
    });
    expect(response.ok(), `注册测试账号失败：HTTP ${response.status()}`).toBeTruthy();
    const body = await response.json();
    expect(body.code, `注册接口返回非 0：${JSON.stringify(body)}`).toBe(0);
    return {
        username,
        password,
        token: body.data.token as string,
        userId: body.data.user.id as string,
    };
}

/**
 * 把账号 token 注入到指定 browser context 的 localStorage，使该 context 成为独立登录会话。
 * 每个 context 注入不同账号即模拟两个不同账号（相互独立的 storage / cookie）。
 */
export async function seedSession(context: BrowserContext, account: TestAccount): Promise<void> {
    const persisted = JSON.stringify({ state: { token: account.token }, version: 0 });
    await context.addInitScript(
        ([key, value]) => {
            window.localStorage.setItem(key as string, value as string);
        },
        [AUTH_TOKEN_KEY, persisted] as const,
    );
}

/** 等待顶栏出现已登录账户入口，证明当前 context 的会话已被前端识别。 */
export async function expectLoggedIn(page: import("@playwright/test").Page): Promise<void> {
    await expect(page.getByRole("button", { name: "账户菜单" })).toBeVisible();
    await expect(page.getByRole("link", { name: "登录" })).toHaveCount(0);
}

/** 打开账户菜单并断言当前账号名，用于区分两个独立账号。 */
export async function expectAccountName(page: import("@playwright/test").Page, username: string): Promise<void> {
    await page.getByRole("button", { name: "账户菜单" }).click();
    await expect(page.getByText(username, { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
}
