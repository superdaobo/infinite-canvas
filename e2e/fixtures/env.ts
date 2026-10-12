import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

// 统一解析端口与目录，供 playwright.config.ts 和测试夹具复用，避免重复硬编码。
const e2eDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(e2eDir, "..", "..");

export const E2E_DIR = e2eDir;
export const REPO_ROOT = repoRoot;
export const WEB_DIR = resolve(repoRoot, "web");

export const BACKEND_PORT = Number(process.env.E2E_BACKEND_PORT || 8080);
export const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 3000);

export const BACKEND_URL = `http://127.0.0.1:${BACKEND_PORT}`;
export const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`;

// 协作 WebSocket 直连地址：Next 的 /api/[...path] 无法代理 WS 升级，
// 前端通过 NEXT_PUBLIC_COLLAB_WS_BASE 指向 Go 后端（见 collab-realtime.ts）。
export const COLLAB_WS_BASE = `ws://127.0.0.1:${BACKEND_PORT}`;
