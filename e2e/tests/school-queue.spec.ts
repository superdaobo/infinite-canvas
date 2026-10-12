import { expect, test } from "@playwright/test";
import { H3_REFERENCE_COUNTS } from "../fixtures/node-fixtures";
import { SchoolBridgeStub } from "../fixtures/school-bridge-stub";

/**
 * 学校任务队列用例。
 *
 * 第一条为「测试替身自检」，当前即可运行：它验证替身本身保证「任意时刻最多一个
 * 执行槽」的契约，可作为后续产品用例的参照。其余为依赖 C03（持久化 FIFO 单执行槽、
 * 幂等、设备故障恢复）的骨架用例，当前用 test.fixme 占位。
 */

test.describe("学校队列：单执行槽与幂等", () => {
    test("学校 Bridge 测试替身：任意时刻最多一个执行槽", () => {
        const bridge = new SchoolBridgeStub();
        bridge.enqueue({ roomId: "room-1", submittedBy: "alice", referenceCount: 3 });
        bridge.enqueue({ roomId: "room-1", submittedBy: "bob", referenceCount: 1 });

        expect(bridge.runningCount).toBe(0);
        const first = bridge.startNext();
        expect(first?.submittedBy).toBe("alice");
        expect(bridge.runningCount).toBe(1);
        // 已有任务运行时不能再取下一个。
        expect(bridge.startNext()).toBeNull();
        expect(bridge.queuedCount).toBe(1);

        bridge.complete({ taskId: first!.id, mediaUrls: ["http://127.0.0.1/media/1.png"] });
        expect(bridge.runningCount).toBe(0);
        expect(bridge.startNext()?.submittedBy).toBe("bob");
    });

    test("学校 Bridge 测试替身：H3 参考图数量限制为 1–9", () => {
        const bridge = new SchoolBridgeStub();
        for (const count of H3_REFERENCE_COUNTS) {
            expect(() => bridge.enqueue({ roomId: "room-1", submittedBy: "alice", referenceCount: count })).not.toThrow();
        }
        expect(() => bridge.enqueue({ roomId: "room-1", submittedBy: "alice", referenceCount: 0 })).toThrow();
        expect(() => bridge.enqueue({ roomId: "room-1", submittedBy: "alice", referenceCount: 10 })).toThrow();
    });

    test.fixme("多成员共享队列一次只运行一个学校任务", async ({ request }) => {
        // TODO(C03-02)：多名成员向同一房间提交任务，服务端应串行执行，
        // 任意时刻 running 数量恒为 1，其余按 FIFO 排队。
        await request.get("/api/health");
        expect(true).toBe(true);
    });

    test.fixme("重复提交同一任务被幂等去重，不产生重复学校执行", async ({ request }) => {
        // TODO(C03-01)：同一幂等键的重复提交只执行一次，结果回填到同一共享节点。
        await request.get("/api/health");
        expect(true).toBe(true);
    });

    test.fixme("设备故障后任务可恢复且不产生重复执行", async ({ request }) => {
        // TODO(C03-02/C03-03)：执行器异常中断后任务回到可重试状态，
        // 不重复扣费、不重复回填。
        await request.get("/api/health");
        expect(true).toBe(true);
    });
});
