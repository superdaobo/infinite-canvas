// 画布运行模式判定：把原生单人项目模式与协作共享模式显式区分开。
//
// - 单人模式：`/canvas/:projectId`，使用 useCanvasStore（本地 localforage + 个人项目接口）。
// - 共享模式：`/canvas/rooms/:roomId`，只读取协作房间公共图，绝不触碰个人画布存储。
//
// 两种模式使用完全不同的路由与状态边界，共享模式的任何读写都不应落到个人项目路径。

export type CanvasMode = "personal" | "shared";

export const PERSONAL_CANVAS_MODE: CanvasMode = "personal";
export const SHARED_CANVAS_MODE: CanvasMode = "shared";

// 共享房间路由前缀，必须与单人项目路由 `/canvas/[id]` 区分。
export const SHARED_CANVAS_ROUTE_PREFIX = "/canvas/rooms";

export function canvasRoomPath(roomId: string) {
    return `${SHARED_CANVAS_ROUTE_PREFIX}/${encodeURIComponent(roomId)}`;
}

// 共享功能开关：默认开启，可通过 NEXT_PUBLIC_ENABLE_CANVAS_COLLAB=false 关闭，
// 关闭后共享房间入口回退为原生单人体验（房间路由重定向回画布库）。
export function isSharedCanvasEnabled() {
    const flag = process.env.NEXT_PUBLIC_ENABLE_CANVAS_COLLAB;
    return flag !== "false" && flag !== "0" && flag !== "off";
}

// 根据路径判定当前画布模式；共享前缀优先于单人项目路由。
export function canvasModeFromPathname(pathname: string): CanvasMode {
    return pathname === SHARED_CANVAS_ROUTE_PREFIX || pathname.startsWith(`${SHARED_CANVAS_ROUTE_PREFIX}/`)
        ? SHARED_CANVAS_MODE
        : PERSONAL_CANVAS_MODE;
}
