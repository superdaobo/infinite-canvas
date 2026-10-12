/**
 * 画布 8 类节点与多图引用的可复现 fixture 定义。
 * 节点类型与默认尺寸对齐 web/src/app/(user)/canvas/types.ts 与 constants.ts，
 * 供后续协作 / 学校用例构造稳定输入，不依赖真实 AI 生成。
 */

/** 与 CanvasNodeType 枚举一致的 8 类节点。 */
export const CANVAS_NODE_TYPES = ["image", "panorama", "text", "config", "video", "audio", "director", "group"] as const;

export type CanvasNodeTypeName = (typeof CANVAS_NODE_TYPES)[number];

export type CanvasNodeFixture = {
    type: CanvasNodeTypeName;
    title: string;
    size: { width: number; height: number };
    /** 是否需要真实媒体资源；false 表示纯结构节点，可在无媒体环境下构造。 */
    requiresMedia: boolean;
};

/** 8 类节点的默认尺寸与标题，取自 web/src/app/(user)/canvas/constants.ts。 */
export const NODE_FIXTURES: Record<CanvasNodeTypeName, CanvasNodeFixture> = {
    image: { type: "image", title: "图片", size: { width: 340, height: 240 }, requiresMedia: true },
    panorama: { type: "panorama", title: "全景图", size: { width: 340, height: 240 }, requiresMedia: true },
    text: { type: "text", title: "文本", size: { width: 340, height: 240 }, requiresMedia: false },
    config: { type: "config", title: "生成配置", size: { width: 440, height: 240 }, requiresMedia: false },
    video: { type: "video", title: "视频", size: { width: 420, height: 236 }, requiresMedia: true },
    audio: { type: "audio", title: "音频", size: { width: 340, height: 160 }, requiresMedia: true },
    director: { type: "director", title: "导演台", size: { width: 360, height: 320 }, requiresMedia: false },
    group: { type: "group", title: "组", size: { width: 760, height: 480 }, requiresMedia: false },
};

/** 多图引用 fixture：学校 H3 一次 1–9 张参考图，这里提供可复现的引用数量集合。 */
export const H3_REFERENCE_COUNTS = [1, 2, 3, 4, 5, 6, 7, 8, 9] as const;

/**
 * 生成一组稳定的节点 fixture（不写入页面，仅供用例构造预期/断言）。
 * 不传类型时返回全部 8 类。
 */
export function buildNodeFixtures(types: CanvasNodeTypeName[] = [...CANVAS_NODE_TYPES]): CanvasNodeFixture[] {
    return types.map((type) => NODE_FIXTURES[type]);
}
