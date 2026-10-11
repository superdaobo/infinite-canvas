import { defineConfig, devices } from "@playwright/test";
import { BACKEND_PORT, BACKEND_URL, FRONTEND_URL, REPO_ROOT, WEB_DIR } from "./fixtures/env";

/**
 * 一条命令（npm test / npx playwright test）即可拉起本地 Go 后端（sqlite）与 Next.js 前端。
 * - 后端：go run . ，使用独立 sqlite 文件 data/e2e.db，端口默认 8080。
 * - 前端：npm run dev（next dev），端口默认 3000，通过 API_BASE_URL 代理到后端。
 * 两个服务都只在测试期间存活，测试结束后由 Playwright 关闭。
 */
export default defineConfig({
    testDir: "./tests",
    outputDir: "./test-results",
    timeout: 120_000,
    expect: { timeout: 20_000 },
    fullyParallel: false,
    workers: 1,
    forbidOnly: !!process.env.CI,
    retries: process.env.CI ? 1 : 0,
    reporter: [
        ["list"],
        ["html", { open: "never", outputFolder: "playwright-report" }],
    ],
    use: {
        baseURL: FRONTEND_URL,
        trace: "retain-on-failure",
        screenshot: "only-on-failure",
        video: "off",
        locale: "zh-CN",
        timezoneId: "Asia/Shanghai",
    },
    projects: [
        {
            name: "chromium",
            use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
        },
    ],
    webServer: [
        {
            command: "go run .",
            cwd: REPO_ROOT,
            url: `${BACKEND_URL}/api/health`,
            reuseExistingServer: !process.env.CI,
            timeout: 300_000,
            env: {
                PORT: String(BACKEND_PORT),
                STORAGE_DRIVER: "sqlite",
                DATABASE_DSN: "data/e2e.db",
                JWT_SECRET: "e2e-infinite-canvas-secret",
                ADMIN_USERNAME: "admin",
                ADMIN_PASSWORD: "infinite-canvas",
            },
            stdout: "pipe",
            stderr: "pipe",
        },
        {
            command: "npm run dev",
            cwd: WEB_DIR,
            url: `${FRONTEND_URL}/login`,
            reuseExistingServer: !process.env.CI,
            timeout: 300_000,
            env: {
                API_BASE_URL: BACKEND_URL,
            },
            stdout: "pipe",
            stderr: "pipe",
        },
    ],
});
